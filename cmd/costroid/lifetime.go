package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/history"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm"
)

// lifetimeTimeout bounds the reading of a pull request's earlier check runs,
// so the check and the comment keep time to post.
const lifetimeTimeout = 6 * time.Second

// earlierRuns is what the pull request's earlier check runs give a report:
// the total the comment states, this run's report included, and for the
// policy the earlier runs' total and the baseline of this run's agent. Each
// is nil when the history was not read.
type earlierRuns struct {
	lifetime *comment.Lifetime
	earlier  *policy.Lifetime
	baseline *policy.Baseline
}

// readLifetime reads the pull request's earlier check runs. Off a pull
// request, on a host that keeps no check runs, and when the history cannot be
// read whole, which a warning says, it gives nothing.
func readLifetime(ctx context.Context, host scm.Reporter, pull pullContext, input comment.Input, stderr io.Writer) earlierRuns {
	checks, ok := host.(scm.History)
	if !ok || pull.pullRequest == 0 {
		return earlierRuns{}
	}
	ctx, cancel := context.WithTimeout(ctx, lifetimeTimeout)
	defer cancel()
	runs, err := earlierRunsOf(ctx, newHistoryCache(checks), pull.pullRequest, pull.head, input)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: the pull request's total is left out: %v\n", err)
		return earlierRuns{}
	}
	return runs
}

// earlierRunsOf adds up the reports on the pull request's commits with this
// run's, and reads the agent's baseline from them. A check run is a claim, so
// the baseline is unverified.
func earlierRunsOf(ctx context.Context, checks scm.History, number int, head string, input comment.Input) (earlierRuns, error) {
	result, err := comment.Result(input)
	if err != nil {
		return earlierRuns{}, err
	}
	own, err := history.Parse([]byte(result))
	if err != nil {
		return earlierRuns{}, err
	}
	totals, err := history.Read(ctx, checks, number, head, own)
	if err != nil {
		return earlierRuns{}, err
	}
	reports, err := history.Reports(ctx, checks, number, head)
	if err != nil {
		return earlierRuns{}, err
	}
	costs, err := history.RunCosts(reports, own)
	if err != nil {
		return earlierRuns{}, err
	}
	return earlierRuns{
		lifetime: &totals.Lifetime,
		earlier:  &totals.Earlier,
		baseline: policy.BaselineOf(costs, policy.Unverified),
	}, nil
}
