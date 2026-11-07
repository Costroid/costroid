package main

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
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
	agentResultVariable  = "COSTROID_AGENT_RESULT"
	// reportTimeout bounds every request of one report together, so a stalled
	// host costs the job half a minute at most.
	reportTimeout = 30 * time.Second
	// settingsTimeout leaves the check time to post after a stalled read.
	settingsTimeout = 8 * time.Second
	// baseHeadTimeout bounds the one request that resolves the base branch.
	baseHeadTimeout = 4 * time.Second
)

var (
	headPattern  = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	rowIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// errNoCISystem is why a report outside a supported CI system refuses to run.
var errNoCISystem = errors.New("report refused: neither GitHub Actions nor GitLab CI was detected")

// reportEvent is the one GitHub event report posts on. On any other event the
// event file names no head, and GITHUB_SHA can be the default branch's last
// commit, so a check would land on a commit the run did not review.
const reportEvent = "pull_request"

// eventRefusal says why report posts nothing on a GitHub event, empty on the
// event it posts on.
func eventRefusal(eventName string) string {
	switch eventName {
	case reportEvent:
		return ""
	case followUpTrigger:
		return "not posted: report posts on the pull_request event, and a workflow_run job posts through costroid follow-up"
	case approveEvent:
		return "not posted: report posts on the pull_request event, and a review's job posts through costroid approve"
	}
	return fmt.Sprintf("not posted: report posts on the pull_request event, and this run's event is %q. "+
		"docs/existing-workflow.md says how to report from the workflow you have", eventName)
}

type reportFlags struct {
	rows         string
	budget       *decimal.Decimal
	installation string
	// agentResult is the agent job's result as the CI system states it,
	// empty when the report was not told.
	agentResult string
	json        bool
	// preview prints the comment and posts nothing.
	preview bool
}

func parseReport(args []string, stderr io.Writer) (reportFlags, error) {
	set := flag.NewFlagSet("report", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprint(stderr, usage) }
	rows := set.String("rows", "", "the rows file costroid run wrote")
	budget := set.String("budget-usd", "", "the cap the run was given, in USD")
	installation := set.String("installation-id", os.Getenv(installationVariable), "the GitHub App installation, github:<id> or the bare number; GitLab uses the project id")
	agentResult := set.String("agent-result", os.Getenv(agentResultVariable), "the agent job's result: success, failure, cancelled or skipped")
	asJSON := set.Bool("json", false, "print the result object on stdout")
	preview := set.Bool("preview", false, "print the comment as Markdown and post nothing")
	if err := set.Parse(args); err != nil {
		return reportFlags{}, err
	}
	if *rows == "" {
		return reportFlags{}, errors.New("report needs --rows")
	}
	if *preview && *asJSON {
		return reportFlags{}, errors.New("report takes --preview or --json, not both")
	}
	flags := reportFlags{rows: *rows, agentResult: *agentResult, json: *asJSON, preview: *preview}
	if *installation != "" {
		number, ok := appInstallation(*installation)
		if !ok {
			return reportFlags{}, errors.New("--installation-id must be github:<id> or the GitHub App installation's bare number")
		}
		flags.installation = number
	}
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
	head string
	// base is the event's base commit, the merge-base on both hosts.
	base string
	// baseBranch names the base branch, and baseHead is the commit at its
	// head when the report resolved it.
	baseBranch  string
	baseHead    string
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
		pull.baseBranch = readBaseBranch(os.Getenv("GITHUB_EVENT_PATH"))
	}
	return pull, nil
}

// readBaseBranch reads pull_request.base.ref, which cicontext.Event does not
// carry. A file that cannot be read names no branch.
func readBaseBranch(path string) string {
	var event struct {
		PullRequest *struct {
			Base struct {
				Ref string `json:"ref"`
			} `json:"base"`
		} `json:"pull_request"`
	}
	if readJSON(path, &event) != nil || event.PullRequest == nil {
		return ""
	}
	return event.PullRequest.Base.Ref
}

// settingsRef is the commit costroid.yml is read from. On a pull request that
// is the base branch's head, or the event's base when the head was not
// resolved, so a pull request cannot change its own setting.
func (p pullContext) settingsRef() string {
	if p.pullRequest > 0 {
		return cmp.Or(p.baseHead, p.base)
	}
	return p.head
}

// resolveBaseHead reads the commit at the head of the base branch. A head that
// cannot be read leaves the event's base, and a warning says so.
func resolveBaseHead(ctx context.Context, host scm.Reporter, pull pullContext, stderr io.Writer) pullContext {
	if pull.pullRequest == 0 {
		return pull
	}
	head, err := resolveBranchHead(ctx, host, pull.baseBranch)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %s is read at the event's base commit %s, not the base branch's head: %v\n", configFile, pull.base, err)
		return pull
	}
	pull.baseHead = head
	return pull
}

func resolveBranchHead(ctx context.Context, host scm.Reporter, branch string) (string, error) {
	branches, ok := host.(policy.Branches)
	if !ok {
		return "", errors.New("this host resolves no branch")
	}
	ctx, cancel := context.WithTimeout(ctx, baseHeadTimeout)
	defer cancel()
	return policy.BaseHead(ctx, branches, branch)
}

// rowsReading is what the rows file gave the report, and why it gave no
// figures when it gave none. rowsSHA256 and fileSHA256 are the digests of
// the rows and of the file, empty when the file was not read to its end.
type rowsReading struct {
	ledger     comment.Ledger
	state      comment.State
	reason     comment.Reason
	line       int
	rowsSHA256 string
	fileSHA256 string
}

// rowsDigest hashes the rows file as the report reads it: every byte of it,
// and each row's x_CostroidRowId, which name the rows whatever order or line
// endings the file gives them.
type rowsDigest struct {
	file    hash.Hash
	pending []byte
	ids     []string
	unnamed bool
}

func newRowsDigest() *rowsDigest {
	return &rowsDigest{file: sha256.New()}
}

// Write takes the bytes the rows reader reads and splits them into lines as
// that reader does.
func (d *rowsDigest) Write(p []byte) (int, error) {
	d.file.Write(p)
	rest := p
	for {
		end := bytes.IndexByte(rest, '\n')
		if end < 0 {
			d.pending = append(d.pending, rest...)
			return len(p), nil
		}
		d.row(append(d.pending, rest[:end]...))
		d.pending = d.pending[:0]
		rest = rest[end+1:]
	}
}

// row keeps one line's row id. The rows reader skips an empty line, so this
// does too.
func (d *rowsDigest) row(line []byte) {
	line = bytes.TrimSuffix(line, []byte{'\r'})
	if len(line) == 0 {
		return
	}
	var row struct {
		ID string `json:"x_CostroidRowId"`
	}
	if json.Unmarshal(line, &row) != nil || !rowIDPattern.MatchString(row.ID) {
		d.unnamed = true
		return
	}
	d.ids = append(d.ids, row.ID)
}

// sums returns the digest of the rows and of the file, once the file has been
// read to its end. The rows' digest is the SHA-256 of their row ids, sorted,
// each followed by a line feed. It is empty when a row names no row id.
func (d *rowsDigest) sums() (rows, file string) {
	d.row(d.pending)
	d.pending = nil
	file = hex.EncodeToString(d.file.Sum(nil))
	if d.unnamed {
		return "", file
	}
	slices.Sort(d.ids)
	sum := sha256.New()
	for _, id := range d.ids {
		io.WriteString(sum, id+"\n")
	}
	return hex.EncodeToString(sum.Sum(nil)), file
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
	digest := newRowsDigest()
	ledger, err := comment.ReadRows(io.TeeReader(file, digest))
	var invalid *comment.RowError
	if errors.As(err, &invalid) {
		return rowsReading{state: comment.Unreadable, reason: comment.InvalidRow, line: invalid.Line}, err
	}
	if err != nil {
		return unread, err
	}
	rowsSHA256, fileSHA256 := digest.sums()
	if ledger.Rows == 0 {
		return rowsReading{ledger: ledger, state: comment.NoRows, reason: comment.EmptyRowsFile, fileSHA256: fileSHA256}, nil
	}
	return rowsReading{ledger: ledger, state: comment.Reported, rowsSHA256: rowsSHA256, fileSHA256: fileSHA256}, nil
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
// --preview needs no CI system.
func reportCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags, err := parseReport(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	if flags.preview {
		return previewReport(ctx, flags, time.Now(), stdout, stderr)
	}
	system := cicontext.Detect(os.Getenv)
	if system == cicontext.NoSystem {
		fmt.Fprintf(stderr, "costroid: %v\n", errNoCISystem)
		return exitFailure
	}
	if system == cicontext.GitHubActions {
		if refusal := eventRefusal(os.Getenv("GITHUB_EVENT_NAME")); refusal != "" {
			fmt.Fprintf(stderr, "costroid: %s\n", refusal)
			return 0
		}
	}
	input := rowsInput(flags, stderr)
	input.RunURL = runURL(system)
	input.CIRun = ciRunFromEnvironment(os.Getenv)
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

// rowsInput is what the rows file, the run's result object beside it and the
// flags give a report, before any host is read.
func rowsInput(flags reportFlags, stderr io.Writer) comment.Input {
	rows, err := readReportRows(flags.rows)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
	}
	stated := readRunReason(flags.rows, stderr)
	input := comment.Input{
		Ledger:         rows.ledger,
		State:          rows.state,
		Reason:         rows.reason,
		ReasonLine:     rows.line,
		RunReason:      runReasonFor(rows.state, stated.RunReason, stderr),
		ReaderLine:     stated.ReaderError,
		AgentError:     agentErrorFor(stated.AgentError, stderr),
		Budget:         flags.budget,
		RowsSHA256:     rows.rowsSHA256,
		RowsFileSHA256: rows.fileSHA256,
	}
	return withAgentResult(input, flags.agentResult, stderr)
}

// previewReport prints, as Markdown, the comment a report posts for the rows,
// and reads and posts nothing on any host. The verdict holds the rows to
// costroid.yml in the working directory, as costroid policy does, and the
// amounts are shown.
func previewReport(ctx context.Context, flags reportFlags, at time.Time, stdout, stderr io.Writer) int {
	input := rowsInput(flags, stderr)
	input.Installation = flags.installation
	input.Amounts = comment.AmountsShown
	input.Verdict = previewVerdict(ctx, flags.rows, input.State, at, stderr)
	body, err := comment.Comment(input)
	if err == nil {
		_, err = io.WriteString(stdout, body)
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", fmt.Errorf("preview: %w", err))
		return exitFailure
	}
	return 0
}

// previewVerdict evaluates the policy offline at the given time. It reads no
// workflow history, so a rule that fails warns, as it does under costroid
// policy without --workflow-added.
func previewVerdict(ctx context.Context, rowsPath string, state comment.State, at time.Time, stderr io.Writer) *policy.Verdict {
	usage, err := reportUsage(rowsPath, state)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
	}
	verdict, err := policy.Evaluate(ctx, policy.Request{
		Usage:  usage,
		Trust:  policy.Unverified,
		Config: policy.ConfigFromFile(configFile),
		At:     at,
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

// maxRunResult bounds the run's result object the report reads. The object
// the run writes is a few kilobytes.
const maxRunResult = 1 << 20

// statedReason is what the run's result object says about why the run wrote
// no rows, and about the error its agent ended in. The comment cleans
// ReaderError again before it shows it.
type statedReason struct {
	RunReason   run.Reason        `json:"run_reason"`
	ReaderError string            `json:"reader_error"`
	AgentError  *statedAgentError `json:"agent_error"`
}

// statedAgentError is the class of error the run says its agent ended in.
type statedAgentError struct {
	Class string `json:"class"`
}

// readRunReason reads the reason from the result object costroid run or
// ingest-run wrote beside the rows. The agent's job wrote that file, as it
// wrote the rows, so only the reason, the reader's error line and the agent's
// error class are read, and only a reason or a class from its closed list is
// used. A run that wrote no object, as an older release does, gives no
// reason.
func readRunReason(rows string, stderr io.Writer) statedReason {
	path := runResultPath(rows)
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return statedReason{}
	}
	if err == nil && !info.Mode().IsRegular() {
		err = errors.New("not a regular file")
	}
	if err == nil && info.Size() > maxRunResult {
		err = fmt.Errorf("larger than %d bytes", maxRunResult)
	}
	var object statedReason
	if err == nil {
		err = readJSON(path, &object)
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: the run's result object %s is ignored: %v\n", path, err)
		return statedReason{}
	}
	return object
}

func readJSON(path string, into any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, into)
}

// agentErrorFor keeps the agent's error class when it is on the closed list.
func agentErrorFor(stated *statedAgentError, stderr io.Writer) adapter.AgentError {
	if stated == nil {
		return ""
	}
	class := adapter.AgentError(stated.Class)
	if run.AgentErrorFix(class) == "" {
		fmt.Fprintf(stderr, "costroid: warning: the agent's error class %q is not one this release knows, so it is ignored\n", stated.Class)
		return ""
	}
	return class
}

// The results GitHub Actions states for a job a report needs.
const (
	agentSucceeded = "success"
	agentFailed    = "failure"
	agentCancelled = "cancelled"
	agentSkipped   = "skipped"
)

// withAgentResult applies the agent job's result. A skipped job wrote no
// rows, so a report whose rows file is empty or absent says the agent did not
// run, in place of any other reason. A result the report does not know is
// ignored, and so is a skipped job beside a rows file that holds rows or a
// line that is not one, which says more than the result does.
func withAgentResult(input comment.Input, result string, stderr io.Writer) comment.Input {
	switch result {
	case "":
		return input
	case agentSucceeded, agentFailed, agentCancelled:
		ran := false
		input.AgentSkipped = &ran
		return input
	case agentSkipped:
	default:
		fmt.Fprintf(stderr, "costroid: warning: the agent's result %q is not success, failure, cancelled or skipped, so it is ignored\n", result)
		return input
	}
	if input.State != comment.NoRows && input.Reason != comment.NoRowsFile {
		fmt.Fprintln(stderr, "costroid: warning: the agent's job reads skipped, and the rows file is not empty, so the result is ignored")
		return input
	}
	skipped := true
	input.State, input.Reason, input.ReasonLine, input.RunReason = comment.NoRows, comment.AgentSkipped, 0, ""
	input.AgentSkipped = &skipped
	return input
}

// ciRunFromEnvironment is GitHub Actions' run id and attempt, nil outside it
// or when either is not a whole number from 1.
func ciRunFromEnvironment(getenv func(string) string) *comment.BlockCIRun {
	if cicontext.Detect(getenv) != cicontext.GitHubActions {
		return nil
	}
	id, idErr := strconv.ParseInt(getenv("GITHUB_RUN_ID"), 10, 64)
	attempt, attemptErr := strconv.Atoi(getenv("GITHUB_RUN_ATTEMPT"))
	if idErr != nil || attemptErr != nil || id < 1 || attempt < 1 {
		return nil
	}
	return &comment.BlockCIRun{ID: id, Attempt: attempt}
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
	ctx, cancel := context.WithTimeout(ctx, reportTimeout)
	defer cancel()
	host, pull := target.host, resolveBaseHead(ctx, target.host, target.pull, stderr)
	input.Installation = target.installation
	input.AccountHost = target.accountHost
	input.Amounts = resolveAmounts(ctx, host, pull.settingsRef(), stderr)
	lifetime, earlier := readLifetime(ctx, host, pull, input, stderr)
	input.Lifetime = lifetime
	input.Verdict = decideVerdict(ctx, host, pull, rowsPath, input.State, earlier, stderr)
	input.Approval = reportApproval(ctx, host, pull, input.Verdict, stderr)
	input.CheckRunID = postCheck(ctx, host, input, stderr)
	if input.CheckRunID != nil {
		input.IDInCheck = nameCheck(ctx, host, input, stderr)
	}
	if pull.pullRequest > 0 {
		postComment(ctx, host, input, lifetime, pull.pullRequest, stderr)
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
		pull:         pullContext{head: run.Head, base: run.Base, baseBranch: os.Getenv("CI_MERGE_REQUEST_TARGET_BRANCH_NAME"), pullRequest: number},
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

func postComment(ctx context.Context, host scm.Reporter, input comment.Input, lifetime *comment.Lifetime, pullRequest int, stderr io.Writer) {
	body, err := comment.CommentWithLifetime(input, lifetime)
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

// decideVerdict evaluates the policy. The settings come from the base branch's
// head and the rows from the agent's job, so the trust path is unverified.
// earlier is what the pull request's earlier runs cost, nil when it could not
// be read, and then the pull request budget does not run. A policy that
// cannot run leaves no verdict, and the check concludes neutral.
func decideVerdict(ctx context.Context, host scm.Reporter, pull pullContext, rowsPath string, state comment.State, earlier *policy.Lifetime, stderr io.Writer) *policy.Verdict {
	ctx, cancel := context.WithTimeout(ctx, settingsTimeout)
	defer cancel()
	usage, err := reportUsage(rowsPath, state)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
	}
	verdict, err := policy.Evaluate(ctx, policy.Request{
		Usage:    usage,
		Trust:    policy.Unverified,
		Config:   policy.ReadConfig(ctx, host, policy.Commits{Base: pull.settingsRef(), Head: pull.head, PullRequest: pull.pullRequest > 0}),
		Start:    workflowStart(ctx, host, pull, stderr),
		At:       time.Now(),
		Lifetime: earlier,
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
// branch, at the commit the settings are read at. A host without the history,
// as GitLab is here, starts no period.
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
