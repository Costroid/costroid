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

// readLifetime adds up this run's report and those the pull request's earlier
// check runs carry: for the comment, and the earlier runs alone for the
// policy. Both are nil off a pull request, on a host that keeps no check runs,
// and when the history cannot be read whole, which a warning says.
func readLifetime(ctx context.Context, host scm.Reporter, pull pullContext, input comment.Input, stderr io.Writer) (*comment.Lifetime, *policy.Lifetime) {
	checks, ok := host.(scm.History)
	if !ok || pull.pullRequest == 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, lifetimeTimeout)
	defer cancel()
	totals, err := lifetimeOf(ctx, checks, pull, input)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: the pull request's total is left out: %v\n", err)
		return nil, nil
	}
	return &totals.Lifetime, &totals.Earlier
}

func lifetimeOf(ctx context.Context, checks scm.History, pull pullContext, input comment.Input) (history.Totals, error) {
	result, err := comment.Result(input)
	if err != nil {
		return history.Totals{}, err
	}
	own, err := history.Parse([]byte(result))
	if err != nil {
		return history.Totals{}, err
	}
	return history.Read(ctx, checks, pull.pullRequest, pull.head, own)
}
