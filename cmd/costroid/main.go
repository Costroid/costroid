// Command costroid is the cost check for pull requests.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"unicode/utf8"

	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/run"
)

const usage = `costroid runs a coding agent under a budget and records what it cost.

Usage:
  costroid run --budget-usd N --out rows.jsonl [--billing-account ID] -- <agent command>
  costroid report --rows rows.jsonl [--budget-usd N] [--installation-id ID]
  costroid version

The agent command is claude -p or codex exec. Costroid adds the flags it needs.

--billing-account fills BillingAccountId on every row. Without it, the
COSTROID_BILLING_ACCOUNT variable does. With neither, the rows read unknown.

costroid report posts one check run and one sticky comment from CI, with the
GITHUB_TOKEN and GITHUB_* variables of the job. It exits 0 whatever GitHub
answers, so a failed post never fails the job. --budget-usd restates the run's
budget, which rows do not record. --installation-id puts the GitHub App
installation on the account link; COSTROID_INSTALLATION_ID does the same.

Exit codes:
  the agent's own   the run finished, or the agent stopped itself
  124               costroid stopped the agent at the cap
  125               costroid could not run the agent or record the run
`

const (
	exitBudgetStop = 124
	exitFailure    = 125

	billingAccountVariable = "COSTROID_BILLING_ACCOUNT"
	// maxBillingAccount is the row contract's limit on a text column.
	maxBillingAccount = 512
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
	case "report":
		return reportCommand(ctx, args[1:], stderr)
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

type runFlags struct {
	budget  decimal.Decimal
	out     string
	account string
	argv    []string
}

func parseRun(args []string, stderr io.Writer) (runFlags, error) {
	set := flag.NewFlagSet("run", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprint(stderr, usage) }
	budget := set.String("budget-usd", "", "the run's budget in USD")
	out := set.String("out", "", "the file the FOCUS rows go to, as JSON lines")
	account := set.String("billing-account", os.Getenv(billingAccountVariable), "the account the run is billed to")
	if err := set.Parse(args); err != nil {
		return runFlags{}, err
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
	if utf8.RuneCountInString(*account) > maxBillingAccount {
		return runFlags{}, fmt.Errorf("--billing-account must be at most %d characters", maxBillingAccount)
	}
	return runFlags{budget: amount, out: *out, account: *account, argv: set.Args()}, nil
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
	rows, err := os.Create(flags.out)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	result, err := run.Run(ctx, run.Options{
		Argv:           flags.argv,
		Budget:         flags.budget,
		BillingAccount: flags.account,
		Rows:           rows,
		Stdin:          stdin,
		Stdout:         stdout,
		Stderr:         stderr,
		Catalog:        pinned,
	})
	if closeErr := rows.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("close %s: %w", flags.out, closeErr)
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	summarize(stderr, flags, pinned.Version(), result)
	if result.Stopped {
		return exitBudgetStop
	}
	return result.AgentExit
}

// labels are the accuracy labels a wrapped run can carry.
var labels = map[focus.CostBasis]string{
	focus.Measured:       "Measured",
	focus.ListEquivalent: "List-equivalent, not billed",
}

func summarize(w io.Writer, flags runFlags, catalogVersion string, result run.Result) {
	report := result.Report
	fmt.Fprintf(w, "costroid: %s, %d %s\n", result.Agent, len(report.Requests), noun(result, len(report.Requests)))
	fmt.Fprintf(w, "costroid: %d rows in %s, catalog %s\n", result.Rows, flags.out, catalogVersion)
	fmt.Fprintf(w, "costroid: USD %s at list, %s\n", result.ListTotal, labels[report.CostBasis])
	fmt.Fprintf(w, "costroid: budget USD %s, %s\n", flags.budget, result.Enforcement.Note)
	if result.Stopped {
		fmt.Fprintln(w, "costroid: stopped the agent at the cap")
	}
	if over := result.ListTotal.Sub(flags.budget); over.Sign() > 0 {
		fmt.Fprintf(w, "costroid: over budget by USD %s\n", over)
	}
	if flags.account == "" {
		fmt.Fprintf(w, "costroid: warning: BillingAccountId reads unknown: pass --billing-account or set %s\n", billingAccountVariable)
	}
	for _, warning := range report.Warnings {
		fmt.Fprintf(w, "costroid: warning: %s\n", warning)
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
