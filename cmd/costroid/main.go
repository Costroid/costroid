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
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/cicontext"
	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/gateway"
	"github.com/Costroid/costroid/internal/reconcile"
	"github.com/Costroid/costroid/internal/run"
	"github.com/Costroid/costroid/internal/server"
)

const usage = `costroid runs a coding agent under a budget and records what it cost.

Usage:
  costroid run --budget-usd N --out rows.jsonl [--billing-account ID] [--credential ID] [--gateway URL --gateway-key-file F] [--json] -- <agent command>
  costroid ingest-run --execution-file F --out rows.jsonl [--billing-account ID] [--credential ID]
  costroid report --rows rows.jsonl [--budget-usd N] [--installation-id ID] [--json]
  costroid follow-up --rows rows.jsonl
  costroid reconcile --rows rows.jsonl --from D --to D [--credential ID] [--out FILE] [--reports DIR --as-of T]
  costroid policy --rows rows.jsonl [--config costroid.yml] [--workflow-added T] [--at T] [--json]
  costroid validate [costroid.yml]
  costroid lint-recipes <paths>
  costroid gateway public-key | request | mint | close
  costroid serve
  costroid keys create --owner host:owner
  costroid keys revoke <id>
  costroid egress report [--json]
  costroid version

The agent command is claude -p or codex exec. Costroid adds the flags it needs.

--billing-account fills BillingAccountId on every row. Without it, the
COSTROID_BILLING_ACCOUNT variable does. With neither, the rows read unknown.

--credential fills SubAccountId: the id of the key, workspace or project the run
spends on, never the key itself. Without it, COSTROID_CREDENTIAL does. With
neither, the column is null and reconcile asks for one.

--gateway and --gateway-key-file run Claude Code through a LiteLLM gateway on
the run's own key, which costroid gateway request drew and costroid gateway
mint registered. Without them, COSTROID_GATEWAY_URL and
COSTROID_GATEWAY_KEY_FILE do. run waits for the key, reads it back, and gives the
agent the gateway's URL and the key in place of any provider credential. The
key's id fills SubAccountId. The rows read gateway-precheck when the key has a
budget at or under --budget-usd, an expiry, a parallel-request limit, a largest
request and this run's id, and when the gateway's store answered before, during
and after the run. Otherwise they read none and say why. costroid gateway help
describes the other steps.

Inside GitHub Actions or GitLab CI, run and ingest-run state the repository, the
pull request, the head commit and the CI run on every row, and run sets the
vcs.* and cicd.* attributes through OTEL_RESOURCE_ATTRIBUTES for the agent. These
are claims. Rows stay unverified and unattributed until a ledger checks them
against the job's OIDC token.

--json prints one result object on stdout when the command ends: figures per
currency and basis, token counts by class, grades, the cap and what is left or
over, and a reason when there are no figures. run then sends the agent's
stream to stderr, and its object says why it wrote no rows when it wrote none.
report prints the object its check run carries, byte for byte. run prints none
when it exits 125, unless another run held the agent's state directory.
docs/agents.md describes the object.

run writes the same object beside its rows file whether or not --json is
given: --out rows.jsonl gives rows.run.json. report reads the run's reason for
an empty rows file from it.

ingest-run writes the rows of a claude-code-action run from the action's
execution_file output, after the action ended. No budget held that run, so the
grade reads none.

report posts from CI, and selects the host from the job's variables. On GitHub
Actions it posts one check run and one sticky comment, with GITHUB_TOKEN and the
GITHUB_* variables, then writes the step's outputs and a job summary. The
summary withholds amounts and token counts where the comment does. On GitLab CI
it posts one sticky merge request note, with COSTROID_GITLAB_TOKEN and the CI_*
variables, and no check. It exits 0 whatever the host answers, so a failed post
never fails the job. Outside both it refuses and exits 125. --budget-usd
restates the run's cap, which rows do not record.
--installation-id puts the GitHub App installation on the account link;
COSTROID_INSTALLATION_ID does the same. GitLab's link names the project id.

follow-up posts the check for a fork's pull request from a workflow_run job,
which holds the base repository's token. It reads the finished run's rows as
data, up to 8 MiB, and costroid.yml at the default branch's commit. It posts no
comment, and nothing for a branch of the repository itself, whose report job
posts the check. Like report, it exits 0 whatever the host answers, and outside
GitHub Actions it refuses and exits 125.

policy gives, offline, the verdict the check gives: it holds the rows to the
policy keys of the costroid.yml named, which should be the base commit's.
--workflow-added and --at state when the workflow reached the base branch and
when the check ran, both as RFC 3339. Without --workflow-added the warn-only
period has not ended. --json prints the verdict as the check's machine block
carries it.

reconcile compares rows with the provider's usage and cost reports, per
credential per UTC day. Tokens must match within 1% by class. List-price dollars
are compared within 10%, and the residual is split into named causes.

It runs where the admin key lives, and it reads the key from
ANTHROPIC_ADMIN_KEY or OPENAI_ADMIN_KEY. The key never leaves the process.
--reports reads saved report files from a directory instead, and needs no key;
--as-of states when they were saved. docs/reconcile.md names the files.

A row's credential is its SubAccountId. --credential names one for rows where
that is null. --out keeps one row per source, credential and day, and a rerun
replaces it. Rerun through the eighth day after month end, when restating stops.

Exit codes of run:
  the agent's own   the run finished, or the agent stopped itself
  124               costroid stopped the agent at the cap
  125               costroid could not run the agent or record the run

Exit codes of policy:
  0                 success or neutral
  1                 failure
  125               costroid could not read the rows or the file

Exit codes of reconcile:
  0                 no token miss
  1                 a token miss, which is a defect
  125               costroid could not read the rows or a report

validate and lint-recipes exit 0 when every file passes, 1 when one is rejected
and 125 when they cannot read it.
`

const (
	exitBudgetStop = 124
	exitFailure    = 125

	billingAccountVariable = "COSTROID_BILLING_ACCOUNT"
	credentialVariable     = "COSTROID_CREDENTIAL"
	// maxAccount is the row contract's limit on a text column.
	maxAccount = 512
	// secretKeyPrefix opens every provider key this product reads. A key pasted
	// as a credential would otherwise reach every row and every ledger.
	secretKeyPrefix = "sk-"
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
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitFailure
	}
	switch args[0] {
	case "run":
		return runCommand(ctx, args[1:], stdin, stdout, stderr)
	case "serve":
		return server.Main(ctx, args[1:], stderr)
	case "keys":
		return server.KeysMain(ctx, args[1:], stdout, stderr)
	case "egress":
		return server.EgressMain(ctx, args[1:], stdout, stderr)
	case "reconcile":
		return reconcileCommand(ctx, args[1:], os.Getenv, reconcile.NewClient(), time.Now, stdout, stderr)
	case "lint-recipes":
		return lintRecipes(args[1:], stdout, stderr)
	case "gateway":
		return gatewayCommand(ctx, args[1:], stdout, stderr)
	case "ingest-run":
		return ingestRunCommand(args[1:], stderr)
	case "report":
		return reportCommand(ctx, args[1:], stdout, stderr)
	case "follow-up":
		return followUpCommand(ctx, args[1:], stdout, stderr)
	case "policy":
		return policyCommand(ctx, args[1:], stdout, stderr)
	case "validate":
		return validateCommand(args[1:], stdout, stderr)
	case "version":
		return versionCommand(stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprintf(stderr, "costroid: unknown command %q\n\n%s", args[0], usage)
	return exitFailure
}

func versionCommand(stdout, stderr io.Writer) int {
	pinned, err := catalog.Load()
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(stdout, "costroid %s, catalog %s\n", version, pinned.Version())
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
	if strings.HasPrefix(*a.credential, secretKeyPrefix) {
		return errors.New("--credential takes the key's id, never the key: this value reads like a key")
	}
	return nil
}

type runFlags struct {
	budget  decimal.Decimal
	out     string
	account accountFlags
	argv    []string
	json    bool
	// gateway and gatewayKeyFile are both set, or neither.
	gateway, gatewayKeyFile string
}

func parseRun(args []string, stderr io.Writer) (runFlags, error) {
	set := flag.NewFlagSet("run", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprint(stderr, usage) }
	budget := set.String("budget-usd", "", "the run's budget in USD")
	out := set.String("out", "", "the file the FOCUS rows go to, as JSON lines")
	asJSON := set.Bool("json", false, "print the result object on stdout, and the agent's stream on stderr")
	gatewayURL := set.String("gateway", os.Getenv(gatewayURLVar), "the LiteLLM gateway the agent runs through")
	keyFile := set.String("gateway-key-file", os.Getenv(gatewayKeyFileVar), "the file costroid gateway request wrote the run's key to")
	account := addAccountFlags(set)
	if err := set.Parse(args); err != nil {
		return runFlags{}, err
	}
	if (*gatewayURL == "") != (*keyFile == "") {
		return runFlags{}, errors.New("--gateway and --gateway-key-file go together")
	}
	if *budget == "" || *out == "" || set.NArg() == 0 {
		return runFlags{}, errors.New("run needs --budget-usd, --out and an agent command after --")
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
	return runFlags{
		budget: amount, out: *out, account: account, argv: set.Args(), json: *asJSON,
		gateway: *gatewayURL, gatewayKeyFile: *keyFile,
	}, nil
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

func runCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags, err := parseRun(args, stderr)
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
	session, err := gatewaySession(flags)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	ci := readCI(stderr)
	result, err := run.Run(ctx, run.Options{
		Argv:           flags.argv,
		Budget:         flags.budget,
		BillingAccount: *flags.account.billing,
		Credential:     *flags.account.credential,
		CI:             ci,
		Rows:           rows,
		Stdin:          stdin,
		Stdout:         agentOutput,
		Stderr:         stderr,
		Catalog:        pinned,
		Gateway:        session,
	})
	if closeErr := rows.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("close %s: %w", flags.out, closeErr)
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		var refused *run.RefusedError
		if errors.As(err, &refused) {
			recordRunResult(stdout, stderr, flags, ci.Head, result.Reason)
		}
		return exitFailure
	}
	summarize(stderr, flags, pinned.Version(), result)
	recordRunResult(stdout, stderr, flags, ci.Head, result.Reason)
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

// recordRunResult writes the result object beside the rows, where the report
// reads the run's reason, and prints it under --json. A file that cannot be
// written costs the report the reason and nothing else, so it is a warning.
func recordRunResult(stdout, stderr io.Writer, flags runFlags, head string, reason run.Reason) {
	object, err := runResult(stderr, flags, head, reason)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
		return
	}
	if err := writeNew(runResultPath(flags.out), object+"\n"); err != nil {
		fmt.Fprintf(stderr, "costroid: warning: the run's result object was not written: %v\n", err)
	}
	if flags.json {
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
func runResult(stderr io.Writer, flags runFlags, head string, reason run.Reason) (string, error) {
	rows, err := readReportRows(flags.out)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
	}
	input := comment.Input{
		Ledger:      rows.ledger,
		State:       rows.state,
		Reason:      rows.reason,
		ReasonLine:  rows.line,
		RunReason:   reason,
		HeadSHA:     head,
		Budget:      &flags.budget,
		Amounts:     comment.AmountsShown,
		BeforeCheck: true,
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
	fmt.Fprintf(w, "costroid: USD %s at list, %s\n", result.ListTotal, labels[report.CostBasis])
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
