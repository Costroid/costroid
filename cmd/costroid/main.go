// Command costroid is the cost check for pull requests.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/cicontext"
	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/gateway"
	"github.com/Costroid/costroid/internal/reason"
	"github.com/Costroid/costroid/internal/run"
)

const (
	// exitUsage is a command line that names no command, as with no argument.
	exitUsage      = 2
	exitBudgetStop = 124
	exitFailure    = 125

	billingAccountVariable = "COSTROID_BILLING_ACCOUNT"
	credentialVariable     = "COSTROID_CREDENTIAL"
	// maxAccount is the row contract's limit on a text column.
	maxAccount = 512
)

// version is set at link time.
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := dispatch(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func dispatch(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	std := stdio{stdin: stdin, stdout: stdout, stderr: stderr}
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return exitUsage
	}
	switch name := args[0]; {
	case name == "help":
		return helpCommand(args[1:], std)
	case isHelpFlag(name):
		return helpCommand(nil, std)
	case name == "--version":
		return versionCommand(ctx, nil, std)
	}
	entry, ok := lookup(args[0])
	if !ok {
		return unknownCommand(args[0], stderr)
	}
	if len(args) > 1 && isHelpFlag(args[1]) {
		fmt.Fprint(stdout, entry.help)
		return 0
	}
	return entry.run(ctx, args[1:], std)
}

func versionCommand(_ context.Context, _ []string, std stdio) int {
	pinned, err := catalog.Load()
	if err != nil {
		fmt.Fprintf(std.stderr, "costroid: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(std.stdout, "costroid %s, catalog %s\n", version, pinned.Version())
	return 0
}

// accountFlags are the two columns a caller states about who a run spends on.
type accountFlags struct {
	billing    *string
	credential *string
}

func addAccountFlags(set *flag.FlagSet) accountFlags {
	return accountFlags{
		billing:    set.String("billing-account", os.Getenv(billingAccountVariable), "the account the run is billed to"),
		credential: set.String("credential", os.Getenv(credentialVariable), "the id of the key, workspace or project the run spends on"),
	}
}

func (a accountFlags) check() error {
	if utf8.RuneCountInString(*a.billing) > maxAccount {
		return fmt.Errorf("--billing-account must be at most %d characters", maxAccount)
	}
	if utf8.RuneCountInString(*a.credential) > maxAccount {
		return fmt.Errorf("--credential must be at most %d characters", maxAccount)
	}
	if focus.ReadsLikeKey(*a.credential) {
		return errors.New("--credential takes the key's id, never the key: this value reads like a key")
	}
	return nil
}

const runHelp = `Usage:
  costroid run --budget-usd N [--out FILE] [--agent NAME]
               [--billing-account ID] [--credential ID]
               [--tag KEY=VALUE]... [--focus-profile strict]
               [--gateway URL --gateway-key-file F] [--json] -- <agent command>

The agent command is claude -p or codex exec. Costroid adds the flags it needs.

--out names the file the rows go to. Without it, run writes
costroid-rows.jsonl in the working directory and says so on stderr.

--agent names the agent when the command's name does not: claude-code or codex
for a command that starts the agent through npx, bunx or node, with the flags
placed after the package or script; claude-code-sdk or codex-sdk for a program
that runs the agent through its SDK. An SDK program runs as it is, and its
output is not read. The rows come from every Claude Code session or Codex
thread the program started while it ran. A claude-code-sdk run grades none
unless a gateway holds it, because the budget sits in the program's own
maxBudgetUsd. A codex-sdk run stops the program at the cap, as codex exec does.

` + accountHelp + `
` + rowFlagsHelp + `
--gateway and --gateway-key-file run Claude Code through a LiteLLM gateway on
the run's own key, which costroid gateway request drew and costroid gateway
mint registered. Without them, COSTROID_GATEWAY_URL and
COSTROID_GATEWAY_KEY_FILE do. run waits for the key, reads it back, and gives
the agent the gateway's URL and the key in place of any provider credential.
The key's id fills SubAccountId. The rows read gateway-precheck when the key
has a budget at or under --budget-usd, an expiry, a parallel-request limit, a
largest request and this run's id, and when the gateway's store answered
before, during and after the run. Otherwise they read none and say why.
costroid gateway help describes the other steps.

` + ciHelp + `
--json prints one result object on stdout when run ends: figures per currency
and basis, token counts by class, grades, the cap and what is left or over, and
a reason when there are no figures. The agent's stream then goes to stderr,
and the object says why run wrote no rows when it wrote none. run prints no
object when it exits 125 before it creates the rows file. docs/agents.md
describes the object.

` + resultFileHelp + `
` + statusFileHelp + `
Exit codes of run:
  the agent's own   the run finished, or the agent stopped itself
  124               costroid stopped the agent at the cap
  125               costroid could not run the agent or record the run
`

type runFlags struct {
	agent   string
	budget  decimal.Decimal
	out     string
	account accountFlags
	// shape is what --tag and --focus-profile add to every row.
	shape focus.Shape
	argv  []string
	json  bool
	// gateway and gatewayKeyFile are both set, or neither.
	gateway, gatewayKeyFile string
}

func parseRun(args []string, stdout, stderr io.Writer) (runFlags, error) {
	set := flag.NewFlagSet("run", flag.ContinueOnError)
	set.SetOutput(stderr)
	budget := set.String("budget-usd", "", "the run's budget in USD")
	agent := set.String("agent", "", "the agent, when the command's name does not say: "+strings.Join(run.Agents, ", "))
	out := set.String("out", "", "the file the FOCUS rows go to, as JSON lines: "+defaultRowsFile+" when not given")
	asJSON := set.Bool("json", false, "print the result object on stdout, and the agent's stream on stderr")
	gatewayURL := set.String("gateway", os.Getenv(gatewayURLVar), "the LiteLLM gateway the agent runs through")
	keyFile := set.String("gateway-key-file", os.Getenv(gatewayKeyFileVar), "the file costroid gateway request wrote the run's key to")
	account := addAccountFlags(set)
	shaping := addRowFlags(set)
	if err := parseFlags(set, args, runHelp, stdout, stderr); err != nil {
		return runFlags{}, err
	}
	if (*gatewayURL == "") != (*keyFile == "") {
		return runFlags{}, errors.New("--gateway and --gateway-key-file go together")
	}
	if *agent != "" && !slices.Contains(run.Agents, *agent) {
		return runFlags{}, fmt.Errorf("--agent takes %s, not %q", strings.Join(run.Agents, ", "), *agent)
	}
	if missing := missingRunArguments(*budget, set.NArg()); missing != "" {
		return runFlags{}, errors.New("run needs " + missing)
	}
	amount, err := decimal.Parse(*budget)
	if err != nil {
		return runFlags{}, fmt.Errorf("--budget-usd: %w", err)
	}
	if amount.Sign() <= 0 {
		return runFlags{}, errors.New("--budget-usd must be above 0")
	}
	if err := account.check(); err != nil {
		return runFlags{}, err
	}
	shape, err := shaping.shape()
	if err != nil {
		return runFlags{}, err
	}
	if *out == "" {
		*out = defaultRowsFile
		fmt.Fprintf(stderr, "costroid: no --out was given, so the rows go to %s in the working directory\n", defaultRowsFile)
	}
	return runFlags{
		agent: *agent, budget: amount, out: *out, account: account, shape: shape, argv: set.Args(), json: *asJSON,
		gateway: *gatewayURL, gatewayKeyFile: *keyFile,
	}, nil
}

// defaultRowsFile is where run writes the rows when --out names no file: the
// name the action's run stage gives them.
const defaultRowsFile = "costroid-rows.jsonl"

// missingRunArguments names each of run's required arguments that is absent,
// empty when none is.
func missingRunArguments(budget string, commandWords int) string {
	var missing []string
	if budget == "" {
		missing = append(missing, "--budget-usd")
	}
	if commandWords == 0 {
		missing = append(missing, "an agent command after --")
	}
	switch len(missing) {
	case 0:
		return ""
	case 1:
		return missing[0]
	}
	return strings.Join(missing[:len(missing)-1], ", ") + " and " + missing[len(missing)-1]
}

// readCI never stops a run: a job whose event file cannot be read still spends
// money, and its rows still need writing.
func readCI(stderr io.Writer) cicontext.Context {
	context, err := cicontext.Read(os.Getenv)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: the rows state no pull request: %v\n", err)
	}
	return context
}

func runCommand(ctx context.Context, args []string, std stdio) int {
	stdin, stdout, stderr := std.stdin, std.stdout, std.stderr
	flags, err := parseRun(args, stdout, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	pinned, err := catalog.Load()
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	// An object an earlier run left would give this run's rows its reason.
	if err := os.Remove(runResultPath(flags.out)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	rows, err := os.Create(flags.out)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	// Under --json stdout carries the result object alone.
	agentOutput := stdout
	if flags.json {
		agentOutput = stderr
	}
	ci := readCI(stderr)
	record := runRecord{out: flags.out, budget: &flags.budget, head: ci.Head, json: flags.json, ciRun: ciRunFromEnvironment(os.Getenv)}
	session, err := gatewaySession(flags)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", errors.Join(err, rows.Close()))
		recordRunResult(stdout, stderr, record)
		return exitFailure
	}
	result, err := run.Run(ctx, run.Options{
		Agent:          flags.agent,
		Argv:           flags.argv,
		Budget:         flags.budget,
		BillingAccount: *flags.account.billing,
		Credential:     *flags.account.credential,
		CI:             ci,
		Rows:           focus.Shaped(rows, flags.shape),
		Stdin:          stdin,
		Stdout:         agentOutput,
		Stderr:         stderr,
		Catalog:        pinned,
		Gateway:        session,
	})
	if closeErr := rows.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("close %s: %w", flags.out, closeErr)
	}
	record.reason, record.agentError = result.Reason, result.AgentError
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		recordRunResult(stdout, stderr, record.failed(err))
		return exitFailure
	}
	summarize(stderr, flags, pinned.Version(), result)
	recordRunResult(stdout, stderr, record)
	if result.Stopped {
		return exitBudgetStop
	}
	return result.AgentExit
}

// gatewaySession reads the run's key from its file. The key goes to the agent
// alone, and no line costroid prints holds it.
func gatewaySession(flags runFlags) (*gateway.Session, error) {
	if flags.gateway == "" {
		return nil, nil
	}
	key, err := os.ReadFile(flags.gatewayKeyFile)
	if err != nil {
		return nil, fmt.Errorf("read the gateway key: %w", err)
	}
	return &gateway.Session{
		Client: gateway.NewClient(flags.gateway),
		Key:    strings.TrimSpace(string(key)),
		Wait:   gatewayKeyWait,
		Every:  gatewayProbeEvery,
		Poll:   gatewayPoll,
		Settle: gatewaySpendSettle,
		Stable: gatewaySpendStable,
	}, nil
}

// runResultPath is where run writes its result object: beside the rows file,
// named after it, so whatever carries the rows can carry the object, and two
// rows files in one directory never share one.
func runResultPath(rows string) string {
	return strings.TrimSuffix(rows, filepath.Ext(rows)) + ".run.json"
}

// runRecord is what the result object beside the rows states about a run that
// created its rows file, whether or not it wrote a row.
type runRecord struct {
	out    string
	budget *decimal.Decimal
	head   string
	reason reason.Reason
	// readerLine is the reader's first error line, with reader_refused.
	readerLine string
	// agentError is the class of error the agent ended in, empty for none.
	agentError adapter.AgentError
	// ciRun is the CI run the environment names, nil when it names none.
	ciRun *comment.BlockCIRun
	// agentStated is the agent's own figure for the run, nil when it states
	// none.
	agentStated *adapter.Stated
	json        bool
}

// failed adds what err says about a run that ended before its rows were
// written: a reader that refused the stream names its first error line.
func (r runRecord) failed(err error) runRecord {
	var reader *run.ReaderError
	if errors.As(err, &reader) {
		r.reason = reason.ReaderRefused
		r.readerLine = reason.ReaderLine(reader.Err.Error())
	}
	return r
}

// recordRunResult writes the result object beside the rows, where the report
// reads the run's reason, and prints it under --json. A file that cannot be
// written costs the report the reason and nothing else, so it is a warning.
func recordRunResult(stdout, stderr io.Writer, record runRecord) {
	object, err := runResult(stderr, record)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
		return
	}
	if err := writeNew(runResultPath(record.out), object+"\n"); err != nil {
		fmt.Fprintf(stderr, "costroid: warning: the run's result object was not written: %v\n", err)
	}
	if record.json {
		if _, err := fmt.Fprintln(stdout, object); err != nil {
			fmt.Fprintf(stderr, "costroid: warning: %v\n", fmt.Errorf("print the result: %w", err))
		}
	}
}

// writeNew creates path and fails if anything is there, so a link the agent
// left in its place is never followed.
func writeNew(path, text string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = file.WriteString(text)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

// runResult is the result object of the rows the run wrote, read as the
// report reads them, with the run's reason when it wrote none. No check has
// concluded on them yet.
func runResult(stderr io.Writer, record runRecord) (string, error) {
	rows, err := readReportRows(record.out)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
	}
	input := comment.Input{
		Ledger:         rows.ledger,
		State:          rows.state,
		Reason:         rows.reason,
		ReasonLine:     rows.line,
		RunReason:      record.reason,
		ReaderLine:     record.readerLine,
		AgentError:     record.agentError,
		CIRun:          record.ciRun,
		HeadSHA:        record.head,
		Budget:         record.budget,
		Amounts:        comment.AmountsShown,
		BeforeCheck:    true,
		RowsSHA256:     rows.rowsSHA256,
		RowsFileSHA256: rows.fileSHA256,
		AgentStated:    record.agentStated,
	}
	return comment.Result(input)
}

// labels are the accuracy labels a wrapped run can carry.
var labels = map[focus.CostBasis]string{
	focus.Measured:       "Measured",
	focus.ListEquivalent: "List-equivalent, not billed",
}

func summarize(w io.Writer, flags runFlags, catalogVersion string, result run.Result) {
	report := result.Report
	summarizeRows(w, flags.out, catalogVersion, result)
	fmt.Fprintf(w, "costroid: budget USD %s, %s\n", flags.budget, result.Enforcement.Note)
	if result.Stopped {
		fmt.Fprintln(w, "costroid: stopped the agent at the cap")
	}
	if over := result.ListTotal.Sub(flags.budget); over.Sign() > 0 {
		fmt.Fprintf(w, "costroid: over budget by USD %s\n", over)
	}
	warnAccount(w, flags.account)
	for _, warning := range report.Warnings {
		fmt.Fprintf(w, "costroid: warning: %s\n", warning)
	}
}

func summarizeRows(w io.Writer, out, catalogVersion string, result run.Result) {
	report := result.Report
	// A run-grain entry is one model's total for the whole run, so the count is
	// of models, never of runs.
	if count := len(report.Requests); count > 0 && report.Requests[0].Grain == adapter.PerRun {
		fmt.Fprintf(w, "costroid: %s, one run, %d %s\n", result.Agent, count, plural("model", count))
	} else {
		fmt.Fprintf(w, "costroid: %s, %d %s\n", result.Agent, count, noun(result, count))
	}
	fmt.Fprintf(w, "costroid: %d rows in %s, catalog %s\n", result.Rows, out, catalogVersion)
	// A run with no rows measured nothing, so it states no cost.
	if result.Rows > 0 {
		fmt.Fprintf(w, "costroid: USD %s at list, %s\n", result.ListTotal, labels[report.CostBasis])
	}
}

func warnAccount(w io.Writer, account accountFlags) {
	if *account.billing == "" {
		fmt.Fprintf(w, "costroid: warning: BillingAccountId reads unknown: pass --billing-account or set %s\n", billingAccountVariable)
	}
}

func noun(result run.Result, count int) string {
	word := "request"
	if len(result.Report.Requests) > 0 {
		word = string(result.Report.Requests[0].Grain)
	}
	if count == 1 {
		return word
	}
	return word + "s"
}

func plural(word string, count int) string {
	if count == 1 {
		return word
	}
	return word + "s"
}
