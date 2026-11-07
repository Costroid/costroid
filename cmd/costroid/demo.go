package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/policy"
)

// demoRows are the rows ingest-run writes from a recorded claude-code-action
// run. testdata/demo/README.md names the recording.
//
//go:embed testdata/demo/rows.jsonl
var demoRows string

// The pull request the demo's comment is posted on. Its commit, check run,
// installation and CI run are the examples docs/comment-example.md shows; its
// figures are the recorded run's.
const (
	demoHead         = "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d"
	demoBase         = "1111111111111111111111111111111111111111"
	demoCheckRun     = int64(16873409921)
	demoInstallation = "58120394"
	demoRunURL       = "https://github.com/example/widgets/actions/runs/19203384756"
	demoConfig       = "policy:\n  run_budget_usd: \"5\"\n"
)

var (
	demoWorkflowAdded = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	demoCheckedAt     = time.Date(2026, 11, 7, 11, 40, 0, 0, time.UTC)
)

// demoCommand prints, as Markdown, the comment report posts for a recorded
// run on a pull request whose costroid.yml sets a USD 5 run budget. It needs
// no CI system, no host and no key.
func demoCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("demo", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprint(stderr, usage) }
	err := set.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err == nil && set.NArg() > 0 {
		err = errors.New("demo takes no argument")
	}
	if err == nil {
		err = printDemo(ctx, stdout)
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	return 0
}

func printDemo(ctx context.Context, stdout io.Writer) error {
	body, err := demoComment(ctx)
	if err != nil {
		return fmt.Errorf("demo: %w", err)
	}
	if _, err := io.WriteString(stdout, body); err != nil {
		return fmt.Errorf("demo: print the comment: %w", err)
	}
	return nil
}

func demoComment(ctx context.Context) (string, error) {
	ledger, err := comment.ReadRows(strings.NewReader(demoRows))
	if err != nil {
		return "", err
	}
	usage, err := policy.ReadUsage(strings.NewReader(demoRows))
	if err != nil {
		return "", err
	}
	verdict, err := policy.Evaluate(ctx, policy.Request{
		Usage:  usage,
		Trust:  policy.Unverified,
		Config: policy.ConfigFile{Data: []byte(demoConfig), Found: true, Ref: demoBase, FromBase: true},
		Start:  policy.Start{At: demoWorkflowAdded},
		At:     demoCheckedAt,
	})
	if err != nil {
		return "", err
	}
	checkRun := demoCheckRun
	return comment.Comment(comment.Input{
		Ledger:       ledger,
		State:        comment.Reported,
		HeadSHA:      demoHead,
		Installation: demoInstallation,
		CheckRunID:   &checkRun,
		Amounts:      comment.AmountsShown,
		RunURL:       demoRunURL,
		Verdict:      &verdict,
	})
}
