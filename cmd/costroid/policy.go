package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/history"
	"github.com/Costroid/costroid/internal/policy"
)

type policyFlags struct {
	rows, config, history, baseline string
	start                           policy.Start
	at                              time.Time
	json                            bool
	// warnings takes a warning, on the stderr the flags were parsed with.
	warnings io.Writer
}

func parsePolicy(args []string, stdout, stderr io.Writer) (policyFlags, error) {
	set := flag.NewFlagSet("policy", flag.ContinueOnError)
	set.SetOutput(stderr)
	rows := set.String("rows", "", "the rows file costroid run wrote")
	config := set.String("config", configFile, "costroid.yml as the base branch holds it")
	earlier := set.String("history", "", "the result objects of the pull request's earlier check runs, one per line")
	baseline := set.String("baseline", "", "the result objects of the check runs on the merged pull requests' heads, one per line")
	added := set.String("workflow-added", "", "when the workflow reached the base branch, as RFC 3339")
	at := set.String("at", "", "the time of the check, as RFC 3339; now when empty")
	asJSON := set.Bool("json", false, "print the verdict as the machine block carries it")
	if err := parseFlags(set, args, policyHelp, stdout, stderr); err != nil {
		return policyFlags{}, err
	}
	if *rows == "" || set.NArg() > 0 {
		return policyFlags{}, errors.New("policy needs --rows and takes no argument")
	}
	flags := policyFlags{rows: *rows, config: *config, history: *earlier, baseline: *baseline, at: time.Now(), json: *asJSON, warnings: stderr}
	if *added != "" {
		start, err := time.Parse(time.RFC3339, *added)
		if err != nil {
			return policyFlags{}, fmt.Errorf("--workflow-added: %w", err)
		}
		flags.start = policy.Start{At: start}
	}
	if *at != "" {
		checked, err := time.Parse(time.RFC3339, *at)
		if err != nil {
			return policyFlags{}, fmt.Errorf("--at: %w", err)
		}
		flags.at = checked
	}
	return flags, nil
}

// policyCommand gives the verdict the check gives, offline, and ends on the
// sentence the check's title carries. It exits 1 on failure, so a script can
// stop on it.
func policyCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags, err := parsePolicy(args, stdout, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	verdict, runs, err := offlineCheck(ctx, flags)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	if problem := verdict.ConfigProblem(); problem != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", problem)
	}
	if err := printVerdict(stdout, verdict, runs.lifetime, flags); err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	if verdict.Conclusion == policy.Failure {
		return exitRejected
	}
	return 0
}

func offlineVerdict(ctx context.Context, flags policyFlags) (policy.Verdict, error) {
	verdict, _, err := offlineCheck(ctx, flags)
	return verdict, err
}

// offlineCheck gives the verdict and the history it was read with, so the
// pull request's total the last line names is the one the verdict read.
func offlineCheck(ctx context.Context, flags policyFlags) (policy.Verdict, earlierRuns, error) {
	file, err := os.Open(flags.rows)
	if err != nil {
		return policy.Verdict{}, earlierRuns{}, err
	}
	defer file.Close()
	usage, err := policy.ReadUsage(file)
	if err != nil {
		return policy.Verdict{}, earlierRuns{}, err
	}
	config := policy.ConfigFromFile(flags.config)
	if config.Unread != nil {
		return policy.Verdict{}, earlierRuns{}, config.Unread
	}
	runs, err := historyOf(ctx, flags, config)
	if err != nil {
		return policy.Verdict{}, earlierRuns{}, err
	}
	verdict, err := policy.Evaluate(ctx, policy.Request{
		Usage:                usage,
		Trust:                policy.Unverified,
		Config:               config,
		Start:                flags.start,
		At:                   flags.at,
		Baseline:             runs.baseline,
		Lifetime:             runs.earlier,
		MeasuredPullRequests: measuredReports(runs.lifetime),
	})
	return verdict, runs, err
}

// errHistoryIsNoBaseline refuses --history alone where a rule reads a
// baseline. The file once gave the baseline too, and a verdict without it
// would differ from the one it gave.
var errHistoryIsNoBaseline = errors.New("--history gives no baseline: run_increase, run_increase_percent and run_p95 read the merged pull requests' check runs, which --baseline names")

// historyOf reads --history and --baseline as the report reads the pull
// request's check runs and the merged pull requests', with the rows as this
// run's report. Without a flag its part is not read, and the rules that read
// it do not run. A baseline file whose part cannot be read is left out, as
// the report leaves out the baseline, and a warning says so.
func historyOf(ctx context.Context, flags policyFlags, config policy.ConfigFile) (earlierRuns, error) {
	if flags.history == "" && flags.baseline == "" {
		return earlierRuns{}, nil
	}
	if flags.baseline == "" && readsBaseline(config) {
		return earlierRuns{}, errHistoryIsNoBaseline
	}
	rows, err := readReportRows(flags.rows)
	if err != nil {
		return earlierRuns{}, err
	}
	own, err := ownReport(comment.Input{Ledger: rows.ledger, State: rows.state, Reason: rows.reason, RowsSHA256: rows.rowsSHA256, RowsFileSHA256: rows.fileSHA256})
	if err != nil {
		return earlierRuns{}, err
	}
	var runs earlierRuns
	if flags.history != "" {
		checks, err := readCheckFile(flags.history, "--history")
		if err != nil {
			return earlierRuns{}, err
		}
		if runs.lifetime, runs.earlier, err = pullRunsOf(ctx, &fileHistory{checks}, 0, historyFileCommit, own); err != nil {
			return earlierRuns{}, err
		}
	}
	if flags.baseline != "" {
		checks, err := readCheckFile(flags.baseline, "--baseline")
		if err != nil {
			return earlierRuns{}, err
		}
		reports, err := history.Merged(ctx, &fileMerged{checks}, "", 0, historyFileCommit)
		if err != nil {
			fmt.Fprintf(flags.warnings, "costroid: warning: the baseline is left out: %v\n", err)
			return runs, nil
		}
		if runs.baseline, err = baselineOf(reports, own); err != nil {
			return earlierRuns{}, err
		}
	}
	return runs, nil
}

// printVerdict prints what the verdict sentence leaves unsaid, one sentence a
// line, and last the sentence itself, which names the rows' figures and,
// under --history, the bases of lifetime, the pull request's total. --json
// prints the verdict alone.
func printVerdict(stdout io.Writer, verdict policy.Verdict, lifetime *comment.Lifetime, flags policyFlags) error {
	if flags.json {
		encoded, err := json.MarshalIndent(verdict, "", "  ")
		if err != nil {
			return fmt.Errorf("encode the verdict: %w", err)
		}
		_, err = fmt.Fprintf(stdout, "%s\n", encoded)
		return err
	}
	file, err := os.Open(flags.rows)
	if err != nil {
		return err
	}
	defer file.Close()
	ledger, err := comment.ReadRows(file)
	if err != nil {
		return err
	}
	report := comment.Input{Ledger: ledger, State: comment.Reported, Verdict: &verdict, Lifetime: lifetime, Amounts: comment.AmountsShown}
	if ledger.Rows == 0 {
		report.State, report.Reason = comment.NoRows, comment.EmptyRowsFile
	}
	for _, line := range comment.VerdictLines(report, comment.LocalConfig(flags.config)) {
		fmt.Fprintln(stdout, line)
	}
	_, err = fmt.Fprintln(stdout, comment.VerdictSentence(report))
	return err
}
