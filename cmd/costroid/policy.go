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
	"github.com/Costroid/costroid/internal/policy"
)

type policyFlags struct {
	rows, config, history string
	start                 policy.Start
	at                    time.Time
	json                  bool
}

func parsePolicy(args []string, stdout, stderr io.Writer) (policyFlags, error) {
	set := flag.NewFlagSet("policy", flag.ContinueOnError)
	set.SetOutput(stderr)
	rows := set.String("rows", "", "the rows file costroid run wrote")
	config := set.String("config", configFile, "costroid.yml as the base branch holds it")
	earlier := set.String("history", "", "the result objects of the pull request's earlier check runs, one per line")
	added := set.String("workflow-added", "", "when the workflow reached the base branch, as RFC 3339")
	at := set.String("at", "", "the time of the check, as RFC 3339; now when empty")
	asJSON := set.Bool("json", false, "print the verdict as the machine block carries it")
	if err := parseFlags(set, args, policyHelp, stdout, stderr); err != nil {
		return policyFlags{}, err
	}
	if *rows == "" || set.NArg() > 0 {
		return policyFlags{}, errors.New("policy needs --rows and takes no argument")
	}
	flags := policyFlags{rows: *rows, config: *config, history: *earlier, at: time.Now(), json: *asJSON}
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

// policyCommand gives the verdict the check gives, offline. It exits 1 on
// failure, so a script can stop on it.
func policyCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags, err := parsePolicy(args, stdout, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	verdict, err := offlineVerdict(ctx, flags)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	if problem := verdict.ConfigProblem(); problem != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", problem)
	}
	if err := printVerdict(stdout, verdict, flags.config, flags.json); err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	if verdict.Conclusion == policy.Failure {
		return exitRejected
	}
	return 0
}

func offlineVerdict(ctx context.Context, flags policyFlags) (policy.Verdict, error) {
	file, err := os.Open(flags.rows)
	if err != nil {
		return policy.Verdict{}, err
	}
	defer file.Close()
	usage, err := policy.ReadUsage(file)
	if err != nil {
		return policy.Verdict{}, err
	}
	config := policy.ConfigFromFile(flags.config)
	if config.Unread != nil {
		return policy.Verdict{}, config.Unread
	}
	runs, err := historyOf(ctx, flags)
	if err != nil {
		return policy.Verdict{}, err
	}
	return policy.Evaluate(ctx, policy.Request{
		Usage:                usage,
		Trust:                policy.Unverified,
		Config:               config,
		Start:                flags.start,
		At:                   flags.at,
		Baseline:             runs.baseline,
		Lifetime:             runs.earlier,
		MeasuredPullRequests: measuredReports(runs.lifetime),
	})
}

// historyOf reads --history as the report reads the pull request's check
// runs, with the rows as this run's report. Without the flag there is no
// history, and the rules that read one do not run.
func historyOf(ctx context.Context, flags policyFlags) (earlierRuns, error) {
	if flags.history == "" {
		return earlierRuns{}, nil
	}
	checks, err := readHistoryFile(flags.history)
	if err != nil {
		return earlierRuns{}, err
	}
	rows, err := readReportRows(flags.rows)
	if err != nil {
		return earlierRuns{}, err
	}
	own := comment.Input{Ledger: rows.ledger, State: rows.state, Reason: rows.reason, RowsSHA256: rows.rowsSHA256, RowsFileSHA256: rows.fileSHA256}
	return earlierRunsOf(ctx, checks, 0, historyFileCommit, own)
}

func printVerdict(stdout io.Writer, verdict policy.Verdict, config string, asJSON bool) error {
	if asJSON {
		encoded, err := json.MarshalIndent(verdict, "", "  ")
		if err != nil {
			return fmt.Errorf("encode the verdict: %w", err)
		}
		_, err = fmt.Fprintf(stdout, "%s\n", encoded)
		return err
	}
	fmt.Fprintf(stdout, "Verdict: %s.\n", verdict.Conclusion)
	for _, sentence := range comment.VerdictSentences(verdict, true, comment.LocalConfig(config)) {
		fmt.Fprintln(stdout, sentence)
	}
	return nil
}
