package policy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// A customer pack is one Rego module a repository keeps at its base commit,
// beside costroid.yml. It is never compiled or evaluated in the process that
// decides the verdict: a child process of the same executable does both, and
// the parent reads its answer as it would read anything a stranger wrote.
// Its results can only add warnings and failures to Costroid's verdict.

const (
	// maxPackBytes is the largest pack compiled, the bound serve puts on
	// costroid.yml.
	maxPackBytes = 64 << 10
	// maxPackCurrencies and maxPackModels bound the input. A larger rows file
	// is not cut short: the pack does not run.
	maxPackCurrencies = 16
	maxPackModels     = 256
	maxPackResults    = 32
	// maxPackSteps is how many expressions an evaluation may begin. A count is
	// the same on a fast runner and a slow one, so the same rows meet the
	// same bound everywhere. It is at least 100 times the count of each pack
	// in testdata/packs on the largest input.
	maxPackSteps = 200_000
	// maxPackTime bounds the child from its start to its answer, compiling
	// included: the parent kills it then. It also stops the work the count
	// does not see, the compiler's and what one step does. Unlike the count it
	// ends a run sooner on a slower runner, and only ever in a fault that
	// fails.
	maxPackTime     = 2 * time.Second
	maxMessageBytes = 160
	// maxAnswerBytes is the most the parent reads of the child's answer: 32
	// results at their longest come to about 10 KiB.
	maxAnswerBytes = 64 << 10
	// maxChildErrorBytes is how much of the child's stderr a fault quotes.
	maxChildErrorBytes = 2 << 10
)

// The rule of Costroid's own result on a pack that did not run to its end,
// and its reasons.
const (
	// RuleCustomerPack says why the pack costroid.yml names gave no results.
	RuleCustomerPack = "customer_pack"
	// ReasonPackUnread is a file that is absent or too large, or a host that
	// did not answer.
	ReasonPackUnread = "pack_unread"
	// ReasonPackInvalid is a module that does not compile under
	// capabilities.pack.json, declares another package, or names a result for
	// one of Costroid's rules.
	ReasonPackInvalid = "pack_invalid"
	// ReasonPackBound is an input, a count of steps, a time or a count of
	// results past its bound, or a child that ended without an answer.
	ReasonPackBound = "pack_bound"
	// ReasonPackResult is an answer that is not a list of results in the
	// pack's words.
	ReasonPackResult = "pack_result"
)

// packFaultOutcomes is the outcome of Costroid's customer_pack result for
// each fault. A fault that only the base branch can cause, or the host, is
// unproven, as an invalid costroid.yml runs no rule and concludes neutral. A
// fault the rows can cause fails: were it unproven, a rows file the head's job
// wrote could stall a pack that would have failed the run, and the pull
// request would have made its own check pass. A slow runner, which the
// head's workflow picks, is such a cause, so every fault of time fails.
var packFaultOutcomes = map[string]string{
	ReasonPackUnread:  Unproven,
	ReasonPackInvalid: Unproven,
	ReasonPackBound:   Fail,
	ReasonPackResult:  Fail,
}

// reservedRules are the rule ids of Costroid's own results. A pack's result
// never takes one.
var reservedRules = append(slices.Clone(ruleOrder), RuleCustomerPack)

// idPattern is a pack result's rule and reason.
var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

const messageCharacters = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789 .,:;()'=_/+?-"

// PackOutcome is what a pack's result says. It has two values: a pack has no
// word for pass.
type PackOutcome string

// The two outcomes of a pack's result.
const (
	PackWarn PackOutcome = "warn"
	PackFail PackOutcome = "fail"
)

// PackResult is one result a customer pack returned.
type PackResult struct {
	Rule    string      `json:"rule"`
	Outcome PackOutcome `json:"outcome"`
	Reason  string      `json:"reason"`
	// Message holds only the characters a comment prints as a code span, at
	// most 160 bytes. Empty, the pack gave none.
	Message        string `json:"message,omitempty"`
	HeldByWarnOnly bool   `json:"held_by_warn_only,omitempty"`
}

// PackFault is why a customer pack gave no results. Its reason alone decides
// the outcome of Costroid's customer_pack result.
type PackFault struct {
	reason string
	// Line is the pack's line the parser or the compiler named, 0 when it
	// named none.
	Line int
	err  error
}

func (f *PackFault) Error() string {
	if f.Line > 0 {
		return fmt.Sprintf("policy: customer pack: %s at line %d: %v", f.reason, f.Line, f.err)
	}
	return fmt.Sprintf("policy: customer pack: %s: %v", f.reason, f.err)
}

func (f *PackFault) Unwrap() error {
	return f.err
}

// Reason is the reason Costroid's customer_pack result states.
func (f *PackFault) Reason() string {
	return f.reason
}

// result is Costroid's own customer_pack result for the fault, with the line
// the compiler named beside pack_invalid alone. A reason the table does not
// hold fails, so a fault never concludes looser than the table says.
func (f *PackFault) result() Result {
	outcome, known := packFaultOutcomes[f.reason]
	if !known {
		outcome = Fail
	}
	result := Result{Rule: RuleCustomerPack, Outcome: outcome, Reason: f.reason}
	if f.reason == ReasonPackInvalid && f.Line > 0 {
		result.Line = f.Line
	}
	return result
}

func fault(reason string, line int, err error) *PackFault {
	return &PackFault{reason: reason, Line: line, err: err}
}

// childRequest is what the parent writes to the child's stdin.
type childRequest struct {
	Source []byte          `json:"source"`
	Input  json.RawMessage `json:"input"`
	Steps  int64           `json:"steps"`
}

// childAnswer is what the child writes to its stdout: the pack's results, or
// its fault, and the steps it counted.
type childAnswer struct {
	Steps   int64        `json:"steps"`
	Results []PackResult `json:"results"`
	Fault   *childFault  `json:"fault,omitempty"`
}

type childFault struct {
	Reason string `json:"reason"`
	Line   int    `json:"line"`
	Error  string `json:"error"`
}

// packRunner starts the child that runs a pack, and bounds it.
type packRunner struct {
	// executable is the absolute path of the child, this process's own.
	executable string
	steps      int64
	time       time.Duration
}

// RunCustomerPack runs a customer pack in a child process over the request
// Costroid's verdict was computed from, and joins its results to that
// verdict. The conclusion it returns is never below the verdict's.
func RunCustomerPack(ctx context.Context, source []byte, request Request, verdict Verdict) Composed {
	if !diverting.Load() {
		return compose(verdict, nil, fault(ReasonPackBound, 0, errors.New("this executable did not call policy.PackChild as it started, so a child it starts would not run the pack")))
	}
	executable, err := selfExecutable()
	if err != nil {
		return compose(verdict, nil, fault(ReasonPackBound, 0, fmt.Errorf("find this executable: %w", err)))
	}
	runner := packRunner{executable: executable, steps: maxPackSteps, time: maxPackTime}
	results, _, problem := runner.run(ctx, source, request, verdict.Results)
	return compose(verdict, results, problem)
}

// run hands the pack and its input to a child, and reads the child's answer.
// It returns the results, the steps the child says it counted, and the
// fault.
func (r packRunner) run(ctx context.Context, source []byte, request Request, results []Result) ([]PackResult, int64, *PackFault) {
	if len(source) > maxPackBytes {
		return nil, 0, fault(ReasonPackUnread, 0, fmt.Errorf("the file holds %d bytes, over %d", len(source), maxPackBytes))
	}
	settings, _ := request.Config.settings()
	input, problem := customerInput(request, settings, results)
	if problem != nil {
		return nil, 0, problem
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, 0, fault(ReasonPackBound, 0, fmt.Errorf("write the input: %w", err))
	}
	frame, err := json.Marshal(childRequest{Source: source, Input: encoded, Steps: r.steps})
	if err != nil {
		return nil, 0, fault(ReasonPackBound, 0, fmt.Errorf("write the request: %w", err))
	}
	answer, problem := r.exchange(ctx, frame)
	if problem != nil {
		return nil, 0, problem
	}
	found, steps, problem := readAnswer(answer, source)
	if problem != nil && problem.reason == ReasonPackInvalid {
		problem = r.confirmInvalid(ctx, source, problem)
	}
	return found, steps, problem
}

// confirmInvalid holds a child's claim that the pack is not valid to the
// base branch alone. The claim concludes unproven, and the parent never
// compiles a pack to check it. So a second child is handed the pack and no
// rows: the same claim at the same line stands, because no row reached that
// child, and any other answer is a pack_result fault, which fails. A child
// compiles before it reads its input, so a pack that is not valid answers
// alike both times.
func (r packRunner) confirmInvalid(ctx context.Context, source []byte, claimed *PackFault) *PackFault {
	frame, err := json.Marshal(childRequest{Source: source, Input: json.RawMessage("{}"), Steps: 1})
	if err != nil {
		return fault(ReasonPackResult, 0, fmt.Errorf("write the request: %w", err))
	}
	answer, again := r.exchange(ctx, frame)
	if again == nil {
		_, _, again = readAnswer(answer, source)
	}
	if again == nil || again.reason != ReasonPackInvalid || again.Line != claimed.Line {
		return fault(ReasonPackResult, 0, fmt.Errorf("the pack's process stated it is not valid at line %d, and without this run's rows it answered %v", claimed.Line, again))
	}
	return claimed
}

// PackRun is the customer pack a verdict ran, as result/0.12 states it.
type PackRun struct {
	// Path is the pack's path as costroid.yml names it.
	Path string `json:"path"`
	// SHA256 hashes the bytes read, and zero bytes when none were.
	SHA256 string `json:"sha256"`
	// Results are the pack's own, empty when it returned none and when
	// Costroid's customer_pack result says why it did not run to its end.
	Results []PackResult `json:"results"`
}

// withPack joins the customer pack costroid.yml names to Costroid's verdict,
// after every adjustment of Costroid's own. A pack that was not read for the
// request's costroid.yml never runs: it is pack_unread. Every other pack runs
// in a child, and compose joins its answer, so the conclusion never drops.
func withPack(ctx context.Context, request Request, settings Settings, verdict Verdict) Verdict {
	if settings.Pack == "" {
		return verdict
	}
	var joined Composed
	if problem := request.Pack.fault(request.Config); problem != nil {
		joined = compose(verdict, nil, problem)
	} else {
		joined = RunCustomerPack(ctx, request.Pack.data, request, verdict)
	}
	out := joined.Verdict
	if joined.problem != nil {
		out.packProblem = joined.problem
	}
	out.Pack = &PackRun{
		Path:    settings.Pack,
		SHA256:  digest(request.Pack.bytesFor(request.Config)),
		Results: append([]PackResult{}, joined.Pack...),
	}
	return out
}

// exchange starts the child by this executable's own path, with no shell,
// an empty environment, a working directory that holds nothing and no file
// but its three pipes, writes the request, and reads at most maxAnswerBytes
// of its answer. The child is killed at the deadline, and waited for, so no
// process outlives the call.
func (r packRunner) exchange(ctx context.Context, frame []byte) ([]byte, *PackFault) {
	if !filepath.IsAbs(r.executable) {
		return nil, fault(ReasonPackBound, 0, fmt.Errorf("the child %q is not an absolute path", r.executable))
	}
	dir, err := os.MkdirTemp("", "costroid-pack-")
	if err != nil {
		return nil, fault(ReasonPackBound, 0, fmt.Errorf("make the child's directory: %w", err))
	}
	defer os.RemoveAll(dir)
	if err := closeInheritedOnExec(); err != nil {
		return nil, fault(ReasonPackBound, 0, err)
	}
	ctx, cancel := context.WithTimeout(ctx, r.time)
	defer cancel()
	stdout, stderr := &capped{limit: maxAnswerBytes}, &capped{limit: maxChildErrorBytes}
	command := exec.CommandContext(ctx, r.executable, PackChildArgument)
	command.Env = []string{}
	command.Dir = dir
	command.Stdin = bytes.NewReader(frame)
	command.Stdout, command.Stderr = stdout, stderr
	command.SysProcAttr = childAttributes()
	command.WaitDelay = 100 * time.Millisecond
	err = command.Run()
	switch {
	case ctx.Err() != nil:
		return nil, fault(ReasonPackBound, 0, fmt.Errorf("the pack did not answer within %s", r.time))
	case err != nil:
		return nil, fault(ReasonPackBound, 0, fmt.Errorf("the pack's process ended without an answer: %w: %s", err, firstLine(stderr.data)))
	case stdout.over:
		return nil, fault(ReasonPackResult, 0, fmt.Errorf("the pack's process wrote more than %d bytes", maxAnswerBytes))
	}
	return stdout.data, nil
}

// closeInheritedOnExec marks every descriptor above stderr close-on-exec.
// Go opens each of its own that way already, so this reaches only those the
// process inherited from its own parent, which would otherwise pass to the
// child.
func closeInheritedOnExec() error {
	entries, err := os.ReadDir(descriptorDirectory)
	if err != nil {
		return fmt.Errorf("list the open descriptors: %w", err)
	}
	for _, entry := range entries {
		if descriptor, err := strconv.Atoi(entry.Name()); err == nil && descriptor > 2 {
			syscall.CloseOnExec(descriptor)
		}
	}
	return nil
}

// capped keeps the first limit bytes written to it and drops the rest, so a
// child that writes without end never blocks on a full pipe.
type capped struct {
	limit int
	data  []byte
	over  bool
}

func (c *capped) Write(p []byte) (int, error) {
	room := c.limit - len(c.data)
	c.data = append(c.data, p[:min(room, len(p))]...)
	c.over = c.over || len(p) > room
	return len(p), nil
}

func firstLine(data []byte) string {
	line, _, _ := bytes.Cut(bytes.TrimSpace(data), []byte("\n"))
	return printable(string(line))
}

// readAnswer holds the child's answer to what the child itself was bound to
// write: one JSON object of known fields and nothing after it, results in the
// pack's words or a fault of a known reason, never both. The child is not
// trusted, so anything else is a fault, never a result.
func readAnswer(data, source []byte) ([]PackResult, int64, *PackFault) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var answer childAnswer
	if err := decoder.Decode(&answer); err != nil {
		return nil, 0, fault(ReasonPackResult, 0, fmt.Errorf("read the pack's answer: %w", err))
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, 0, fault(ReasonPackResult, 0, errors.New("the pack's answer holds more than one value"))
	}
	if answer.Fault != nil {
		return nil, answer.Steps, answeredFault(*answer.Fault, len(answer.Results), source)
	}
	if len(answer.Results) > maxPackResults {
		return nil, answer.Steps, fault(ReasonPackBound, 0, fmt.Errorf("the pack returned %d results, over %d", len(answer.Results), maxPackResults))
	}
	for _, result := range answer.Results {
		if problem := checkResult(result); problem != nil {
			return nil, answer.Steps, problem
		}
	}
	slices.SortFunc(answer.Results, comparePackResults)
	return answer.Results, answer.Steps, nil
}

// answeredFault reads a fault the child stated. Its reason must be one only
// the child can find, and its line one the pack has. The size of the file and
// the bounds of the input are the parent's to judge.
func answeredFault(stated childFault, results int, source []byte) *PackFault {
	lines := bytes.Count(source, []byte("\n")) + 1
	known := slices.Contains([]string{ReasonPackInvalid, ReasonPackBound, ReasonPackResult}, stated.Reason)
	if !known || results > 0 || stated.Line < 0 || stated.Line > lines {
		return fault(ReasonPackResult, 0, fmt.Errorf("the pack's process stated the fault %q at line %d", printable(stated.Reason), stated.Line))
	}
	return fault(stated.Reason, stated.Line, errors.New(printable(stated.Error)))
}

// checkResult holds one result to the pack's words: warn or fail, a rule and
// a reason that are ids and no rule of Costroid's, a message a comment can
// print, and no warn-only hold, which only compose sets.
func checkResult(result PackResult) *PackFault {
	switch {
	case result.Outcome != PackWarn && result.Outcome != PackFail:
		return fault(ReasonPackResult, 0, fmt.Errorf("a result's outcome is %q, not warn or fail", printable(string(result.Outcome))))
	case !idPattern.MatchString(result.Rule) || !idPattern.MatchString(result.Reason):
		return fault(ReasonPackResult, 0, fmt.Errorf("a result's rule %q or reason %q is not an id", printable(result.Rule), printable(result.Reason)))
	case slices.Contains(reservedRules, result.Rule):
		return fault(ReasonPackResult, 0, fmt.Errorf("a result names Costroid's rule %s", result.Rule))
	case printable(result.Message) != result.Message || result.HeldByWarnOnly:
		return fault(ReasonPackResult, 0, errors.New("a result holds a message a comment cannot print, or a warn-only hold"))
	}
	return nil
}

// customerInput is everything a customer pack reads, and the one place it is
// built. It holds no row, tag, path, branch, commit, pull request, person,
// credential, clock, environment, or warn-only state: only the rows' counts
// and costs, the settings the base commit states, the baseline and the pull
// request's total, and Costroid's own results without their figures.
func customerInput(request Request, settings Settings, results []Result) (map[string]any, *PackFault) {
	usage := request.Usage
	if len(usage.Currencies) > maxPackCurrencies {
		return nil, fault(ReasonPackBound, 0, fmt.Errorf("the rows hold %d currencies, over %d", len(usage.Currencies), maxPackCurrencies))
	}
	models := 0
	entries := make([]any, 0, len(usage.Currencies))
	for _, entry := range usage.Currencies {
		models += len(entry.Models)
		perModel := make(map[string]any, len(entry.Models))
		for model, rows := range entry.Models {
			perModel[model] = rows
		}
		entries = append(entries, map[string]any{
			"currency":          entry.Currency,
			"list_cost":         entry.ListCost.String(),
			"rows":              entry.Rows,
			"unattributed_rows": entry.UnattributedRows,
			"joinable_rows":     entry.JoinableRows,
			"models":            perModel,
		})
	}
	if models > maxPackModels {
		return nil, fault(ReasonPackBound, 0, fmt.Errorf("the rows name %d model ids, over %d", models, maxPackModels))
	}
	limits := map[string]any{}
	for key, amount := range settings.Amounts {
		limits[key] = amount.String()
	}
	if len(settings.Models) > 0 {
		limits[keyModels] = settings.Models
	}
	seen := make([]any, 0, len(results))
	for _, result := range results {
		seen = append(seen, map[string]any{"rule": result.Rule, "outcome": result.Outcome, "reason": result.Reason})
	}
	input := map[string]any{
		"trust":    string(trusted(request.Trust)),
		"rows":     usage.Rows,
		"usage":    entries,
		"settings": limits,
		"results":  seen,
	}
	if baseline := baselineInput(request.Baseline, Settings{}); baseline != nil {
		input["baseline"] = baseline
	}
	if lifetime := lifetimeInput(request.Lifetime, usage); lifetime != nil {
		input["lifetime"] = lifetime
	}
	return input, nil
}

// printable keeps the characters a comment prints in a code span, replaces
// each other character with ?, and cuts the message at 160 bytes.
func printable(message string) string {
	var out strings.Builder
	for _, char := range message {
		if out.Len() == maxMessageBytes {
			break
		}
		if !strings.ContainsRune(messageCharacters, char) {
			char = '?'
		}
		out.WriteRune(char)
	}
	return out.String()
}

func comparePackResults(a, b PackResult) int {
	return strings.Compare(
		strings.Join([]string{a.Rule, a.Reason, string(a.Outcome), a.Message}, "\x00"),
		strings.Join([]string{b.Rule, b.Reason, string(b.Outcome), b.Message}, "\x00"),
	)
}
