package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/history"
	"github.com/Costroid/costroid/internal/scm"
)

// lifetimeTimeout bounds the reading of a pull request's earlier check runs,
// so the check and the comment keep time to post.
const lifetimeTimeout = 6 * time.Second

// readLifetime adds up this run's report and those the pull request's earlier
// check runs carry. It is nil off a pull request, on a host that keeps no
// check runs, and when the history cannot be read whole, which a warning
// says.
func readLifetime(ctx context.Context, host scm.Reporter, pull pullContext, input comment.Input, stderr io.Writer) *comment.Lifetime {
	checks, ok := host.(scm.History)
	if !ok || pull.pullRequest == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, lifetimeTimeout)
	defer cancel()
	lifetime, err := lifetimeOf(ctx, checks, pull, input)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: the pull request's total is left out: %v\n", err)
		return nil
	}
	return &lifetime
}

func lifetimeOf(ctx context.Context, checks scm.History, pull pullContext, input comment.Input) (comment.Lifetime, error) {
	result, err := comment.Result(input)
	if err != nil {
		return comment.Lifetime{}, err
	}
	own, err := history.Parse([]byte(result))
	if err != nil {
		return comment.Lifetime{}, err
	}
	return history.Read(ctx, checks, pull.pullRequest, pull.head, own)
}
