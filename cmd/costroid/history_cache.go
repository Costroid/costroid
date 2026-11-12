package main

import (
	"context"
	"sync"

	"github.com/Costroid/costroid/internal/scm"
)

// historyCache answers each question about the pull request's commits from
// the host once, so the lifetime and the baseline read one set of requests.
// A failed answer is not kept.
type historyCache struct {
	host scm.History

	mu      sync.Mutex
	commits map[int]pullCommits
	checks  map[string][]scm.ListedCheck
}

type pullCommits struct {
	commits []string
	capped  bool
}

func newHistoryCache(host scm.History) *historyCache {
	return &historyCache{host: host, commits: map[int]pullCommits{}, checks: map[string][]scm.ListedCheck{}}
}

func (c *historyCache) PullCommits(ctx context.Context, number int) ([]string, bool, error) {
	c.mu.Lock()
	kept, found := c.commits[number]
	c.mu.Unlock()
	if found {
		return kept.commits, kept.capped, nil
	}
	commits, capped, err := c.host.PullCommits(ctx, number)
	if err != nil {
		return nil, false, err
	}
	c.mu.Lock()
	c.commits[number] = pullCommits{commits, capped}
	c.mu.Unlock()
	return commits, capped, nil
}

func (c *historyCache) CheckRuns(ctx context.Context, sha, name string) ([]scm.ListedCheck, error) {
	key := name + "@" + sha
	c.mu.Lock()
	kept, found := c.checks[key]
	c.mu.Unlock()
	if found {
		return kept, nil
	}
	checks, err := c.host.CheckRuns(ctx, sha, name)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.checks[key] = checks
	c.mu.Unlock()
	return checks, nil
}
