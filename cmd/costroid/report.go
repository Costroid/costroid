package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/cicontext"
	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/run"
	"github.com/Costroid/costroid/internal/scm"
)

const (
	configFile           = "costroid.yml"
	installationVariable = "COSTROID_INSTALLATION_ID"
	gitLabTokenVariable  = "COSTROID_GITLAB_TOKEN"
	// reportTimeout bounds every request of one report together, so a stalled
	// host costs the job half a minute at most.
	reportTimeout = 30 * time.Second
	// settingsTimeout leaves the check time to post after a stalled read.
	settingsTimeout = 8 * time.Second
)

var headPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// errNoCISystem is why a report outside a supported CI system refuses to run.
var errNoCISystem = errors.New("report refused: neither GitHub Actions nor GitLab CI was detected")

type reportFlags struct {
	rows         string
	budget       *decimal.Decimal
	installation string
	json         bool
}

func parseReport(args []string, stderr io.Writer) (reportFlags, error) {
	set := flag.NewFlagSet("report", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprint(stderr, usage) }
	rows := set.String("rows", "", "the rows file costroid run wrote")
	budget := set.String("budget-usd", "", "the cap the run was given, in USD")
	installation := set.String("installation-id", os.Getenv(installationVariable), "the GitHub App installation id; GitLab uses the project id")
	asJSON := set.Bool("json", false, "print the result object on stdout")
	if err := set.Parse(args); err != nil {
		return reportFlags{}, err
	}
	if *rows == "" {
		return reportFlags{}, errors.New("report needs --rows")
	}
	flags := reportFlags{rows: *rows, installation: *installation, json: *asJSON}
	if *budget != "" {
		amount, err := decimal.Parse(*budget)
		if err != nil {
			return reportFlags{}, fmt.Errorf("--budget-usd: %w", err)
		}
		flags.budget = &amount
	}
	return flags, nil
}

// pullContext is what the CI environment says about the commit.
type pullContext struct {
	head        string
	base        string
	pullRequest int
}

// readPullContext prefers the event file. On a pull request GITHUB_SHA names
// the test merge commit, and the check belongs on the head.
func readPullContext() (pullContext, error) {
	pull := pullContext{head: os.Getenv("GITHUB_SHA")}
	event, err := cicontext.ReadEvent(os.Getenv("GITHUB_EVENT_PATH"))
	if errors.Is(err, cicontext.ErrNoEventFile) {
		return pull, nil
	}
	if err != nil {
		return pull, err
	}
	if event.PullRequest != nil {
		pull.pullRequest = event.PullRequest.Number
		pull.head = cmp.Or(event.PullRequest.Head.SHA, pull.head)
		pull.base = event.PullRequest.Base.SHA
	}
	return pull, nil
}

// settingsRef is the commit costroid.yml is read from. On a pull request that
// is the base, so a pull request cannot change its own setting.
func (p pullContext) settingsRef() string {
	if p.pullRequest > 0 {
		return p.base
	}
	return p.head
}

// rowsReading is what the rows file gave the report, and why it gave no
// figures when it gave none.
type rowsReading struct {
	ledger comment.Ledger
	state  comment.State
	reason comment.Reason
	line   int
}

func readReportRows(path string) (rowsReading, error) {
	unread := rowsReading{state: comment.Unreadable, reason: comment.UnreadRowsFile}
	// The agent's job wrote this path, so a link could name any file on the
	// runner.
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return rowsReading{state: comment.Unreadable, reason: comment.NoRowsFile}, err
	}
	if err != nil {
		return unread, err
	}
	if !info.Mode().IsRegular() {
		return rowsReading{state: comment.Unreadable, reason: comment.NotAFile}, errors.New("the rows path is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return unread, err
	}
	defer file.Close()
	ledger, err := comment.ReadRows(file)
	var invalid *comment.RowError
	if errors.As(err, &invalid) {
		return rowsReading{state: comment.Unreadable, reason: comment.InvalidRow, line: invalid.Line}, err
	}
	if err != nil {
		return unread, err
	}
	if ledger.Rows == 0 {
		return rowsReading{ledger: ledger, state: comment.NoRows, reason: comment.EmptyRowsFile}, nil
	}
	return rowsReading{ledger: ledger, state: comment.Reported}, nil
}

// runURL is the page of the CI run this report belongs to, empty when the
// environment does not name one. The comment checks its form again.
func runURL(system cicontext.System) string {
	if system == cicontext.GitLabCI {
		return os.Getenv("CI_PIPELINE_URL")
	}
	server, repository, run := os.Getenv("GITHUB_SERVER_URL"), os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_RUN_ID")
	if server == "" || repository == "" || run == "" {
		return ""
	}
	return strings.TrimRight(server, "/") + "/" + repository + "/actions/runs/" + run
}

// reportTarget is where a report goes and what it says about itself there.
type reportTarget struct {
	host         scm.Reporter
	pull         pullContext
	installation string
	accountHost  string
}

// targetFromEnvironment selects the adapter. A job on a known CI system gets an
// error when it lacks what a post needs, and that error ends as a line on
// stderr.
func targetFromEnvironment(system cicontext.System, flags reportFlags, stderr io.Writer) (reportTarget, error) {
	if system == cicontext.GitLabCI {
		return gitLabTarget()
	}
	pull, err := readPullContext()
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
	}
	host, err := hostFromEnvironment(pull)
	return reportTarget{host: host, pull: pull, installation: flags.installation}, err
}

// reportCommand exits 0 once its flags parse and a CI system is found. A report
// is never why a job fails, so every failure after that ends as a line on
// stderr. Outside CI there is no job to protect, so it refuses and says why.
func reportCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags, err := parseReport(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	system := cicontext.Detect(os.Getenv)
	if system == cicontext.NoSystem {
		fmt.Fprintf(stderr, "costroid: %v\n", errNoCISystem)
		return exitFailure
	}
	rows, err := readReportRows(flags.rows)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
	}
	input := comment.Input{
		Ledger:     rows.ledger,
		State:      rows.state,
		Reason:     rows.reason,
		ReasonLine: rows.line,
		RunReason:  runReasonFor(rows.state, readRunReason(flags.rows, stderr), stderr),
		Budget:     flags.budget,
		RunURL:     runURL(system),
	}
	target, err := targetFromEnvironment(system, flags, stderr)
	input.HeadSHA = target.pull.head
	if err != nil {
		fmt.Fprintf(stderr, "costroid: not posted: %v\n", err)
	} else {
		input = post(ctx, target, input, flags.rows, stderr)
	}
	publish(input, flags.json, stdout, stderr)
	return 0
}

// maxRunResult bounds the run's result object the report reads. The object
// the run writes is a few kilobytes.
const maxRunResult = 1 << 20

// readRunReason reads the reason from the result object costroid run wrote
// beside the rows. The agent's job wrote that file, as it wrote the rows, so
// only the reason is read, and only a value from the closed list is used. A
// run that wrote no object, as an older release does, gives no reason.
func readRunReason(rows string, stderr io.Writer) run.Reason {
	path := runResultPath(rows)
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if err == nil && !info.Mode().IsRegular() {
		err = errors.New("not a regular file")
	}
	if err == nil && info.Size() > maxRunResult {
		err = fmt.Errorf("larger than %d bytes", maxRunResult)
	}
	var object struct {
		RunReason run.Reason `json:"run_reason"`
	}
	if err == nil {
		err = readJSON(path, &object)
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: the run's result object %s is ignored: %v\n", path, err)
		return ""
	}
	return object.RunReason
}

func readJSON(path string, into any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, into)
}

// runReasonFor keeps the run's reason for an empty rows file. A reason this
// release does not know, from a newer run, is ignored, and so is one beside a
// rows file that is not empty, which says more than the reason does.
func runReasonFor(state comment.State, reason run.Reason, stderr io.Writer) run.Reason {
	switch {
	case reason == "":
		return ""
	case reason.Sentence() == "":
		fmt.Fprintf(stderr, "costroid: warning: the run's reason %q is not one this release knows, so it is ignored\n", reason)
		return ""
	case state != comment.NoRows:
		fmt.Fprintf(stderr, "costroid: warning: the run's reason %s is ignored, because only an empty rows file carries one\n", reason)
		return ""
	}
	return reason
}

// post posts the check and the comment, and returns the input as posted: with
// the installation, the amounts setting, the policy's verdict and the check
// run's id.
func post(ctx context.Context, target reportTarget, input comment.Input, rowsPath string, stderr io.Writer) comment.Input {
	host, pull := target.host, target.pull
	ctx, cancel := context.WithTimeout(ctx, reportTimeout)
	defer cancel()
	input.Installation = target.installation
	input.AccountHost = target.accountHost
	input.Amounts = resolveAmounts(ctx, host, pull.settingsRef(), stderr)
	input.Verdict = decideVerdict(ctx, host, pull, rowsPath, input.State, stderr)
	input.CheckRunID = postCheck(ctx, host, input, stderr)
	if input.CheckRunID != nil {
		input.IDInCheck = nameCheck(ctx, host, input, stderr)
	}
	if pull.pullRequest > 0 {
		postComment(ctx, host, input, pull.pullRequest, stderr)
	}
	return input
}

// publish hands the result to the job: the step's outputs and its summary
// where GitHub Actions names their files, and the result object on stdout
// under --json. A failure here is a warning, as a failed post is.
func publish(input comment.Input, asJSON bool, stdout, stderr io.Writer) {
	if path := os.Getenv("GITHUB_OUTPUT"); path != "" {
		if err := writeOutputs(path, input); err != nil {
			fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
		}
	}
	if path := os.Getenv("GITHUB_STEP_SUMMARY"); path != "" {
		if err := writeSummary(path, input); err != nil {
			fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
		}
	}
	if asJSON {
		if err := printResult(stdout, input); err != nil {
			fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
		}
	}
}

// writeOutputs appends name=value lines. Every value is one line, so no
// value can start another output.
func writeOutputs(path string, input comment.Input) error {
	outputs, err := comment.Outputs(input)
	if err != nil {
		return fmt.Errorf("step outputs: %w", err)
	}
	var lines strings.Builder
	for _, output := range outputs {
		lines.WriteString(output.Name + "=" + output.Value + "\n")
	}
	return appendTo(path, "step outputs", lines.String())
}

func writeSummary(path string, input comment.Input) error {
	summary, err := comment.Summary(input)
	if err != nil {
		return fmt.Errorf("job summary: %w", err)
	}
	return appendTo(path, "job summary", summary)
}

func appendTo(path, what, text string) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	_, err = file.WriteString(text)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

// printResult prints the result object exactly as the check run carries it.
func printResult(stdout io.Writer, input comment.Input) error {
	result, err := comment.Result(input)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(stdout, result); err != nil {
		return fmt.Errorf("print the result: %w", err)
	}
	return nil
}

// hostFromEnvironment runs in the customer's CI, outside the service's egress
// allowlist, so its client is a plain one.
func hostFromEnvironment(pull pullContext) (scm.Reporter, error) {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return nil, errors.New("GITHUB_TOKEN is not set")
	}
	if !headPattern.MatchString(pull.head) {
		return nil, errors.New("no head commit: GITHUB_SHA and the event file name none")
	}
	return scm.NewGitHub(scm.GitHubOptions{
		API:        os.Getenv("GITHUB_API_URL"),
		Repository: os.Getenv("GITHUB_REPOSITORY"),
		Tokens:     scm.StaticToken(token),
		Client:     &http.Client{},
	})
}

// gitLabTarget reads a merge request pipeline in the customer's CI, outside the
// service's egress allowlist, so its client is a plain one. The project is the merge
// request's, and a fork's pipeline is refused here as well as in the recipe's
// rules, so a fork's merge request never gets a post.
func gitLabTarget() (reportTarget, error) {
	run, err := cicontext.Read(os.Getenv)
	if err != nil {
		return reportTarget{}, err
	}
	token := os.Getenv(gitLabTokenVariable)
	project := os.Getenv("CI_MERGE_REQUEST_PROJECT_ID")
	switch {
	case token == "":
		return reportTarget{}, errors.New(gitLabTokenVariable + " is not set")
	case run.PullRequest == "":
		return reportTarget{}, errors.New("no merge request: CI_MERGE_REQUEST_IID names none")
	case run.Head == "":
		return reportTarget{}, errors.New("no head commit: CI_COMMIT_SHA names none")
	case os.Getenv("CI_MERGE_REQUEST_SOURCE_PROJECT_ID") != project || os.Getenv("CI_PROJECT_ID") != project:
		return reportTarget{}, errors.New("this pipeline is for a fork's merge request")
	}
	number, err := strconv.Atoi(run.PullRequest)
	if err != nil {
		return reportTarget{}, fmt.Errorf("merge request: %w", err)
	}
	host, err := scm.NewGitLab(scm.GitLabOptions{
		API:     os.Getenv("CI_API_V4_URL"),
		Project: project,
		Tokens:  scm.StaticToken(token),
		Client:  &http.Client{},
	})
	if err != nil {
		return reportTarget{}, err
	}
	return reportTarget{
		host:         host,
		pull:         pullContext{head: run.Head, base: run.Base, pullRequest: number},
		installation: project,
		accountHost:  comment.AccountGitLab,
	}, nil
}

// postCheck runs before the comment, because the comment names its id.
func postCheck(ctx context.Context, host scm.Reporter, input comment.Input, stderr io.Writer) *int64 {
	output, err := comment.Check(input)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: check not posted: %v\n", err)
		return nil
	}
	id, err := host.PostCheck(ctx, scm.CheckRun{
		Name:       comment.CheckName,
		HeadSHA:    input.HeadSHA,
		Conclusion: input.Conclusion(),
		Title:      output.Title,
		Summary:    output.Summary,
		Text:       output.Text,
		Completed:  time.Now(),
	})
	if err != nil {
		fmt.Fprintf(stderr, "costroid: check not posted: %v\n", err)
		return nil
	}
	fmt.Fprintf(stderr, "costroid: check run %d, conclusion %s\n", id, input.Conclusion())
	return &id
}

// nameCheck writes the check run's id into its own result object. GitHub
// assigns the id when the check is created, so it takes a second request. The
// check keeps the object it was created with when that request fails.
func nameCheck(ctx context.Context, host scm.Reporter, input comment.Input, stderr io.Writer) bool {
	input.IDInCheck = true
	output, err := comment.Check(input)
	if err == nil {
		err = host.UpdateCheck(ctx, *input.CheckRunID, scm.CheckRun{Title: output.Title, Summary: output.Summary, Text: output.Text})
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: check run %d does not name itself: %v\n", *input.CheckRunID, err)
		return false
	}
	return true
}

// resolveAmounts applies the comment.show_cost rule. When the setting or the visibility
// cannot be read, a repository not known to be private withholds amounts. It
// spends part of the report's time at most, so the check still posts.
func resolveAmounts(ctx context.Context, host scm.Reporter, ref string, stderr io.Writer) comment.Amounts {
	ctx, cancel := context.WithTimeout(ctx, settingsTimeout)
	defer cancel()
	config, err := readConfig(ctx, host, ref)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %s: %v\n", configFile, err)
	}
	if err == nil && config.ShowCost {
		return comment.AmountsShown
	}
	visibility, visibilityErr := host.Visibility(ctx)
	switch {
	case visibilityErr != nil:
		fmt.Fprintf(stderr, "costroid: warning: repository visibility: %v\n", visibilityErr)
		return comment.AmountsUnsettled
	case visibility == scm.Private:
		return comment.AmountsShown
	case err != nil:
		return comment.AmountsUnsettled
	}
	return comment.AmountsOff
}

func readConfig(ctx context.Context, host scm.Reporter, ref string) (comment.Config, error) {
	if !headPattern.MatchString(ref) {
		return comment.Config{}, errors.New("no commit to read it from")
	}
	data, err := host.ReadFile(ctx, configFile, ref)
	if errors.Is(err, scm.ErrNotFound) {
		return comment.ParseConfig(nil)
	}
	if err != nil {
		return comment.Config{}, err
	}
	return comment.ParseConfig(data)
}

func postComment(ctx context.Context, host scm.Reporter, input comment.Input, pullRequest int, stderr io.Writer) {
	body, err := comment.Comment(input)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: comment not posted: %v\n", err)
		return
	}
	if err := host.UpsertComment(ctx, pullRequest, comment.Marker, body); err != nil {
		fmt.Fprintf(stderr, "costroid: comment not posted: %v\n", err)
		return
	}
	fmt.Fprintf(stderr, "costroid: comment on pull request %d\n", pullRequest)
}

// decideVerdict evaluates the policy. The settings come from the base commit
// and the rows from the agent's job, so the trust path is unverified. A policy
// that cannot run leaves no verdict, and the check concludes neutral.
func decideVerdict(ctx context.Context, host scm.Reporter, pull pullContext, rowsPath string, state comment.State, stderr io.Writer) *policy.Verdict {
	ctx, cancel := context.WithTimeout(ctx, settingsTimeout)
	defer cancel()
	usage, err := reportUsage(rowsPath, state)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
	}
	verdict, err := policy.Evaluate(ctx, policy.Request{
		Usage:  usage,
		Trust:  policy.Unverified,
		Config: policy.ReadConfig(ctx, host, policy.Commits{Base: pull.base, Head: pull.head, PullRequest: pull.pullRequest > 0}),
		Start:  workflowStart(ctx, host, pull, stderr),
		At:     time.Now(),
	})
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
		return nil
	}
	if problem := verdict.ConfigProblem(); problem != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", problem)
	}
	return &verdict
}

// reportUsage reads the rows again for the policy. A file the comment could
// not read gives the policy no rows either.
func reportUsage(path string, state comment.State) (policy.Usage, error) {
	if state != comment.Reported {
		return policy.Usage{}, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return policy.Usage{}, err
	}
	defer file.Close()
	usage, err := policy.ReadUsage(file)
	if err != nil {
		return policy.Usage{}, err
	}
	return usage, nil
}

// workflowStart reads when the workflow this job runs from reached the base
// branch. A host without the history, as GitLab is here, starts no period.
func workflowStart(ctx context.Context, host scm.Reporter, pull pullContext, stderr io.Writer) policy.Start {
	history, ok := host.(policy.History)
	if !ok {
		return policy.Start{}
	}
	path, err := policy.WorkflowPath(os.Getenv("GITHUB_WORKFLOW_REF"), os.Getenv("GITHUB_REPOSITORY"))
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
		return policy.Start{}
	}
	start, err := policy.ReadStart(ctx, history, path, pull.settingsRef())
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
	}
	return start
}
