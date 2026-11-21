package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/history"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm"
)

// lifetimeTimeout bounds the reading of earlier check runs, the pull
// request's own and the merged pull requests', so the check and the comment
// keep time to post.
const lifetimeTimeout = 6 * time.Second

// baselineKeys are the settings of the rules that read a baseline, as
// costroid.yml names them under policy.
var baselineKeys = []string{"max_increase_usd", "max_increase_percent"}

// earlierRuns is what earlier check runs give a report: the total the comment
// states, this run's report included, and for the policy the pull request's
// earlier runs' total and the baseline of this run's agent. Each is nil when
// it was not read.
type earlierRuns struct {
	lifetime *comment.Lifetime
	earlier  *policy.Lifetime
	baseline *policy.Baseline
}

// readLifetime reads the pull request's earlier check runs and, when a rule
// that reads a baseline is set, the agent's baseline from the pull requests
// merged into the base branch. The two are read at once, in the same time.
// Off a pull request and on a host that keeps no check runs it gives nothing,
// and a part that cannot be read whole is left out, which a warning says.
func readLifetime(ctx context.Context, host scm.Reporter, pull pullContext, input comment.Input, stderr io.Writer) earlierRuns {
	checks, ok := host.(scm.History)
	if !ok || pull.pullRequest == 0 {
		return earlierRuns{}
	}
	ctx, cancel := context.WithTimeout(ctx, lifetimeTimeout)
	defer cancel()
	own, err := ownReport(input)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: the pull request's total is left out: %v\n", err)
		return earlierRuns{}
	}
	var (
		runs        earlierRuns
		baselineErr error
		group       sync.WaitGroup
	)
	group.Go(func() { runs.baseline, baselineErr = mergedBaseline(ctx, host, pull, own) })
	lifetime, earlier, err := pullRunsOf(ctx, newHistoryCache(checks), pull.pullRequest, pull.head, own)
	group.Wait()
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: the pull request's total is left out: %v\n", err)
	} else {
		runs.lifetime, runs.earlier = lifetime, earlier
	}
	if baselineErr != nil {
		fmt.Fprintf(stderr, "costroid: warning: the baseline is left out: %v\n", baselineErr)
	}
	return runs
}

// ownReport is this run's report as the history reader reads an earlier one.
func ownReport(input comment.Input) (history.Report, error) {
	result, err := comment.Result(input)
	if err != nil {
		return history.Report{}, err
	}
	return history.Parse([]byte(result))
}

// pullRunsOf adds up the reports on the pull request's commits with own,
// this run's.
func pullRunsOf(ctx context.Context, checks scm.History, number int, head string, own history.Report) (*comment.Lifetime, *policy.Lifetime, error) {
	totals, err := history.Read(ctx, checks, number, head, own)
	if err != nil {
		return nil, nil, err
	}
	return &totals.Lifetime, &totals.Earlier, nil
}

// mergedBaseline reads the agent's baseline from the pull requests merged
// into the base branch, and only when costroid.yml at the base sets a rule
// that reads one. A host that lists no merged pull requests gives none.
func mergedBaseline(ctx context.Context, host scm.Reporter, pull pullContext, own history.Report) (*policy.Baseline, error) {
	merged, ok := host.(scm.Merged)
	if !ok || !readsBaseline(policy.ReadConfig(ctx, host, policy.Commits{Base: pull.settingsRef(), Head: pull.head, PullRequest: true})) {
		return nil, nil
	}
	if pull.baseBranch == "" {
		return nil, errors.New("the event names no base branch")
	}
	reports, err := history.Merged(ctx, merged, pull.baseBranch, pull.pullRequest, pull.head)
	if err != nil {
		return nil, err
	}
	return baselineOf(reports, own)
}

// baselineOf is the baseline of own's agent over the reports. A check run is
// a claim, so the baseline is unverified.
func baselineOf(reports []history.Report, own history.Report) (*policy.Baseline, error) {
	costs, err := history.RunCosts(reports, own)
	if err != nil {
		return nil, err
	}
	return policy.BaselineOf(costs, policy.Unverified), nil
}

// readsBaseline says whether the file sets a rule that reads a baseline. A
// file that cannot be read or parsed runs no rule.
func readsBaseline(config policy.ConfigFile) bool {
	if config.Unread != nil || !config.Found {
		return false
	}
	settings, err := policy.ParseSettings(config.Data)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(baselineKeys, func(key string) bool {
		_, set := settings.Amounts[key]
		return set
	})
}
