package main

import (
	"bytes"
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
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/focusfile"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/reason"
	"github.com/Costroid/costroid/internal/scm"
	"github.com/Costroid/costroid/internal/vcs"
)

const (
	configFile           = "costroid.yml"
	installationVariable = "COSTROID_INSTALLATION_ID"
	gitLabTokenVariable  = "COSTROID_GITLAB_TOKEN"
	agentResultVariable  = "COSTROID_AGENT_RESULT"
	// refusedVariable and refusedCountVariable carry what the action's
	// install found in a costroid-rows artifact it refused.
	refusedVariable      = "COSTROID_ARTIFACT_REFUSED"
	refusedCountVariable = "COSTROID_ARTIFACT_REFUSED_COUNT"
	// reportTimeout bounds every request of one report together, so a stalled
	// host costs the job half a minute at most.
	reportTimeout = 30 * time.Second
	// settingsTimeout leaves the check time to post after a stalled read.
	settingsTimeout = 8 * time.Second
	// baseHeadTimeout bounds the one request that resolves the base branch.
	baseHeadTimeout = 4 * time.Second
)

var rowIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

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
	// exitOnVerdict ends a report on GitLab CI with the verdict's exit code.
	exitOnVerdict bool
	// budgetEmpty says --budget-usd was given empty, so it is no budget.
	budgetEmpty bool
	// recorded names the recorded run whose rows --rows holds, nil when the
	// rows are the pull request's own.
	recorded *comment.Recorded
}

func parseReport(args []string, stdout, stderr io.Writer) (reportFlags, error) {
	set := flag.NewFlagSet("report", flag.ContinueOnError)
	set.SetOutput(stderr)
	rows := set.String("rows", "", "the rows file costroid run wrote")
	budget := set.String("budget-usd", "", "the run's cap in USD, read only when the run left no result object")
	installation := set.String("installation-id", os.Getenv(installationVariable), "the GitHub App installation, github:<id> or the bare number; GitLab uses the project id")
	agentResult := set.String("agent-result", os.Getenv(agentResultVariable), "the agent job's result: success, failure, cancelled or skipped")
	asJSON := set.Bool("json", false, "print the result object on stdout")
	preview := set.Bool("preview", false, "print the comment as Markdown and post nothing")
	exitOnVerdict := set.Bool("exit-on-verdict", false, "on GitLab CI, exit 1 on a failure verdict and 3 on a neutral one")
	recorded := set.String("recorded", "", "RUN_ID,DATE: the rows are the recorded run RUN_ID's from DATE, not the pull request's")
	if err := parseFlags(set, args, reportHelp, stdout, stderr); err != nil {
		return reportFlags{}, err
	}
	if *rows == "" {
		return reportFlags{}, errors.New("report needs --rows")
	}
	if *preview && *asJSON {
		return reportFlags{}, errors.New("report takes --preview or --json, not both")
	}
	if *preview && *exitOnVerdict {
		return reportFlags{}, errors.New("report takes --preview or --exit-on-verdict, not both")
	}
	flags := reportFlags{rows: *rows, agentResult: *agentResult, json: *asJSON, preview: *preview, exitOnVerdict: *exitOnVerdict}
	if *installation != "" {
		// An unreadable installation costs the comment its account link and
		// nothing else: a missing input never blocks the report.
		if number, ok := appInstallation(*installation); ok {
			flags.installation = number
		} else {
			fmt.Fprintf(stderr, "costroid: warning: --installation-id %q is not github:<id> or a GitHub App installation's bare number, so the comment links no account\n", *installation)
		}
	}
	var err error
	if flags.budget, flags.budgetEmpty, err = parseBudget(set, *budget); err != nil {
		return reportFlags{}, err
	}
	if *recorded != "" {
		if flags.recorded, err = parseRecorded(*recorded); err != nil {
			return reportFlags{}, err
		}
	}
	return flags, nil
}

// parseRecorded reads --recorded: a CI run's id from 1 and the day it was
// recorded, apart by a comma.
func parseRecorded(value string) (*comment.Recorded, error) {
	id, date, found := strings.Cut(value, ",")
	runID, err := strconv.ParseInt(id, 10, 64)
	if err == nil && found {
		_, err = time.Parse(time.DateOnly, date)
	}
	if !found || err != nil || runID < 1 {
		return nil, fmt.Errorf("--recorded takes RUN_ID,DATE, such as 42368537950,2026-11-07, not %q", value)
	}
	return &comment.Recorded{RunID: runID, Date: date}, nil
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

// reportCommand exits 0 once its flags parse and a CI system is found. A report
// is never why a job fails, so every failure after that ends as a line on
// stderr. Outside CI there is no job to protect, so it refuses and says why.
// --preview needs no CI system. On GitLab CI, where no check can fail,
// --exit-on-verdict makes the job's exit code the gate: verdictExit gives it.
func reportCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags, err := parseReport(args, stdout, stderr)
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
	if flags.exitOnVerdict && system != cicontext.GitLabCI {
		fmt.Fprintln(stderr, "costroid: warning: --exit-on-verdict applies on GitLab CI only. On GitHub Actions the costroid check is the gate, so the report exits 0")
		flags.exitOnVerdict = false
	}
	if system == cicontext.GitHubActions {
		if flags.recorded != nil && os.Getenv("GITHUB_EVENT_NAME") == followUpTrigger {
			return reportRecordedOnFork(ctx, flags, stdout, stderr)
		}
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
	if !flags.exitOnVerdict {
		return 0
	}
	conclusion := input.Conclusion()
	code := verdictExit(conclusion)
	fmt.Fprintf(stderr, "costroid: the verdict is %s, so the report exits %d\n", conclusion, code)
	return code
}

// reportRecordedOnFork posts a recorded run's report on a fork's pull request
// from a workflow_run job. The fork's own run holds a read-only token, and
// this job holds the base repository's. The finished run's head commit is
// bound to the fork's one open pull request as follow-up binds it, and the
// settings are read at the default branch, which this job runs from.
func reportRecordedOnFork(ctx context.Context, flags reportFlags, stdout, stderr io.Writer) int {
	repository := os.Getenv("GITHUB_REPOSITORY")
	input := rowsInput(flags, stderr)
	input.RunURL = runURL(cicontext.GitHubActions)
	input.CIRun = ciRunFromEnvironment(os.Getenv)
	finished, head, err := finishedPullRun(followUpTrigger, os.Getenv("GITHUB_EVENT_PATH"), repository, false)
	input.HeadSHA = head
	pull := pullContext{head: head, base: os.Getenv("GITHUB_SHA"), baseBranch: os.Getenv("GITHUB_REF_NAME")}
	var host scm.Reporter
	if err == nil {
		host, err = hostFromEnvironment(pull)
	}
	if err == nil {
		pull.pullRequest, err = confirmForkPull(ctx, host, finished, repository)
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: not posted: %v\n", err)
	} else {
		input = post(ctx, reportTarget{host: host, pull: pull, installation: flags.installation, platform: os.Getenv(platformVariable), repository: checkedRepository()}, input, flags.rows, stderr)
	}
	publish(input, flags.json, stdout, stderr)
	return 0
}

// The exit codes of report --exit-on-verdict. The GitLab recipe allows the job
// to fail on exitVerdictNeutral alone, so a neutral verdict shows a warning
// and exitVerdictFailure fails the pipeline.
const (
	exitVerdictFailure = 1
	exitVerdictNeutral = 3
)

// verdictExit maps the check's conclusion to the job's exit code. Neutral
// covers the warn-only period, a policy that could not run and a report that
// could not reach the host, so only a rule the policy found broken exits 1.
func verdictExit(conclusion string) int {
	switch conclusion {
	case policy.Success:
		return 0
	case policy.Failure:
		return exitVerdictFailure
	}
	return exitVerdictNeutral
}

// rowsInput is what the rows file, the run's result object beside it and the
// flags give a report, before any host is read. When the action refused the
// artifact that carried them, neither file is read.
func rowsInput(flags reportFlags, stderr io.Writer) comment.Input {
	if refused := refusedArtifact(os.Getenv, stderr); refused != nil {
		return comment.Input{State: comment.Unreadable, Reason: comment.NoRowsFile, Refused: refused, Budget: runCap(flags, statedReason{}, stderr)}
	}
	rows, err := readReportRows(flags.rows)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
	}
	stated := readRunReason(flags.rows, stderr)
	input := comment.Input{
		Ledger:              rows.ledger,
		State:               rows.state,
		Reason:              rows.reason,
		ReasonLine:          rows.line,
		RunReason:           runReasonFor(rows.state, stated.RunReason, stderr),
		ReaderLine:          stated.ReaderError,
		AgentError:          agentErrorFor(stated.AgentError, stderr),
		AgentStated:         agentStatedFor(stated.AgentStated, stderr),
		Budget:              runCap(flags, stated, stderr),
		RowsSHA256:          rows.rowsSHA256,
		RowsFileSHA256:      rows.fileSHA256,
		Recorded:            flags.recorded,
		EstimateUnavailable: readEstimateNote(flags.rows, stderr),
	}
	return withAgentResult(input, flags.agentResult, stderr)
}

// readEstimateNote reads the note ingest-run --optional left beside the rows
// when the estimate it was given was not there, nil when there is none. The
// agent's job wrote it, so a link, a large file or a reason off the closed
// list is ignored with a warning.
func readEstimateNote(rows string, stderr io.Writer) *focusfile.Unavailable {
	path := estimateNotePath(rows)
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err == nil && !info.Mode().IsRegular() {
		err = errors.New("not a regular file")
	}
	if err == nil && info.Size() > focusfile.MaxUnavailableBytes {
		err = fmt.Errorf("larger than %d bytes", focusfile.MaxUnavailableBytes)
	}
	var data []byte
	if err == nil {
		data, err = os.ReadFile(path)
	}
	var note focusfile.Unavailable
	if err == nil {
		note, err = focusfile.ParseUnavailable(data)
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: the estimate's note %s is ignored: %v\n", path, err)
		return nil
	}
	return &note
}

// refusedArtifact reads what the action's install found in a costroid-rows
// artifact it refused: how many entries the artifact held besides the rows
// and the run object, and the first names, separated by a comma and a space.
// nil when the install refused nothing. A count that is not a whole number
// from 1 is still a refusal, of at least the entries named.
func refusedArtifact(getenv func(string) string, stderr io.Writer) *comment.RefusedArtifact {
	names, stated := getenv(refusedVariable), getenv(refusedCountVariable)
	if names == "" && stated == "" {
		return nil
	}
	var entries []string
	if names != "" {
		entries = strings.Split(names, ", ")
	}
	count, err := strconv.Atoi(stated)
	if least := max(len(entries), 1); err != nil || count < least {
		fmt.Fprintf(stderr, "costroid: warning: %s is %q, which is fewer than the entries the artifact was refused for, so the report counts %d\n", refusedCountVariable, stated, least)
		count = least
	}
	return &comment.RefusedArtifact{Count: count, Entries: entries}
}

// previewReport prints, as Markdown, the comment a report posts for the rows,
// and reads and posts nothing on any host. The verdict holds the rows to
// costroid.yml in the working directory, as costroid policy does, the
// comment says so, and the amounts are shown. A rows file that is not there
// is refused: no job ran that could say why.
func previewReport(ctx context.Context, flags reportFlags, at time.Time, stdout, stderr io.Writer) int {
	err := previewRowsExist(flags)
	var body string
	if err == nil {
		input := rowsInput(flags, stderr)
		input.Installation = flags.installation
		input.Amounts = comment.AmountsShown
		input.Verdict = previewVerdict(ctx, flags.rows, input.State, at, stderr)
		input.Preview = true
		body, err = comment.Comment(input)
	}
	if err == nil {
		_, err = io.WriteString(stdout, body)
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", fmt.Errorf("preview: %w", err))
		return exitFailure
	}
	return 0
}

// previewRowsExist refuses a rows path with no file at it, unless the agent's
// job was skipped or the action refused the artifact, which the comment
// states in place of the rows.
func previewRowsExist(flags reportFlags) error {
	_, err := os.Lstat(flags.rows)
	if !errors.Is(err, fs.ErrNotExist) || flags.agentResult == agentSkipped ||
		os.Getenv(refusedVariable) != "" || os.Getenv(refusedCountVariable) != "" {
		return nil
	}
	return fmt.Errorf("no rows file at %s. costroid run writes one at --out, and costroid demo shows a comment without one", flags.rows)
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
// no rows, about the error its agent ended in, and about the cap it was given.
// The comment cleans ReaderError again before it shows it.
type statedReason struct {
	RunReason   reason.Reason     `json:"run_reason"`
	ReaderError string            `json:"reader_error"`
	AgentError  *statedAgentError `json:"agent_error"`
	AgentStated *statedFigure     `json:"agent_stated"`
	// Cap is the run's cap, nil when the run was given none.
	Cap *recordCap `json:"cap"`
	// read says an object was read, so its cap, or its lack of one, is the
	// run's own.
	read bool
}

// recordCap is the cap the run's object states, as costroid run and
// ingest-run --cap-usd recorded it.
type recordCap struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

// statedFigure is the agent's own figure for the run, in its own unit, as the
// run's object states it.
type statedFigure struct {
	Unit     string `json:"unit"`
	Quantity string `json:"quantity"`
	Source   string `json:"source"`
}

// statedAgentError is the class of error the run says its agent ended in.
type statedAgentError struct {
	Class string `json:"class"`
}

// readRunReason reads the reason from the result object costroid run or
// ingest-run wrote beside the rows. The agent's job wrote that file, as it
// wrote the rows, so only the reason, the reader's error line, the agent's
// error class, the agent's own figure and the cap are read, and only a reason,
// a class or a figure from its closed list, and a cap in USD above 0, is used.
// A run that wrote no object, as an older release does, gives no reason.
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
	object.read = true
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
	if reason.AgentErrorFix(class) == "" {
		fmt.Fprintf(stderr, "costroid: warning: the agent's error class %q is not one this release knows, so it is ignored\n", stated.Class)
		return ""
	}
	return class
}

// agentStatedFor keeps the agent's own figure when its unit, source and
// quantity are ones this release states. It is copied as the run stated it,
// and never converted.
func agentStatedFor(stated *statedFigure, stderr io.Writer) *adapter.Stated {
	if stated == nil {
		return nil
	}
	figure := adapter.Stated{Unit: stated.Unit, Quantity: stated.Quantity, Source: stated.Source}
	if !comment.KnownStated(figure) {
		fmt.Fprintln(stderr, "costroid: warning: the run's object states a figure of the agent's own in a unit or from a source this release does not know, so it is ignored")
		return nil
	}
	return &figure
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

// runReasonFor keeps the run's reason for an empty rows file. A reason this
// release does not know, from a newer run, is ignored, and so is one beside a
// rows file that is not empty, which says more than the reason does.
func runReasonFor(state comment.State, reason reason.Reason, stderr io.Writer) reason.Reason {
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
	input.Platform, input.Repository, input.PullRequest = target.platform, target.repository, pull.pullRequest
	input.Amounts = resolveAmounts(ctx, host, pull.settingsRef(), stderr)
	// A recorded run's figures were spent on no commit of this pull request,
	// so they join no total and no baseline.
	var runs earlierRuns
	if input.Recorded == nil {
		runs = readLifetime(ctx, host, pull, input, stderr)
	}
	input.Lifetime = runs.lifetime
	if input.Refused == nil {
		input.Verdict = decideVerdict(ctx, host, verdictRequest{
			commits:  policy.Commits{Base: pull.settingsRef(), Head: pull.head, PullRequest: pull.pullRequest > 0},
			start:    func(ctx context.Context) policy.Start { return workflowStart(ctx, host, pull, stderr) },
			rowsPath: rowsPath,
			state:    input.State,
			earlier:  runs.earlier,
			baseline: runs.baseline,
			measured: measuredReports(runs.lifetime),
		}, stderr)
		input.Approval = reportApproval(ctx, host, pull, input.Verdict, stderr)
	}
	input = postInputCheck(ctx, host, input, stderr)
	if pull.pullRequest > 0 {
		postComment(ctx, host, input, runs.lifetime, pull.pullRequest, stderr)
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
	if !vcs.IsCommit(ref) {
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
	fmt.Fprintf(stderr, "costroid: comment on %s %d\n", requestNoun(host), pullRequest)
}

// requestNoun is what the host calls the change a comment goes on: GitLab's
// merge request, GitHub's pull request.
func requestNoun(host scm.Reporter) string {
	if _, ok := host.(*scm.GitLab); ok {
		return "merge request"
	}
	return "pull request"
}

// measuredReports counts the pull request's reports with a measured figure,
// this run's included, as the lifetime reader added them up. The reader counts
// a report once per currency, so the count is the largest of those counts. It
// reads low when reports state measured figures in different currencies, and a
// low count only lets the period run longer. A history that could not be read
// counts none, so the period runs its 7 days.
func measuredReports(lifetime *comment.Lifetime) int {
	if lifetime == nil {
		return 0
	}
	count := 0
	for _, figure := range lifetime.Figures {
		if figure.Basis == string(focus.Measured) {
			count = max(count, figure.Reports)
		}
	}
	return count
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
