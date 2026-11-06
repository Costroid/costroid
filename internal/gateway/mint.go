package gateway

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
)

// The metadata keys a run's key carries.
const (
	MetaRepository       = "costroid_repository"
	MetaPullRequest      = "costroid_pull_request"
	MetaHead             = "costroid_head"
	MetaRun              = "costroid_run_id"
	MetaAttempt          = "costroid_run_attempt"
	MetaMaxRequestTokens = "costroid_max_request_tokens"
)

// DailyCaps are the caps a key's team and user carry. A nil cap sets nothing.
type DailyCaps struct {
	Repository *decimal.Decimal
	Author     *decimal.Decimal
}

// Mint is one key to register for one run.
type Mint struct {
	Client           *Client
	AdminKey         string
	Run              Binding
	Request          Request
	Budget           decimal.Decimal
	Lifetime         time.Duration
	MaxParallel      int
	MaxRequestTokens int64
	PullRequest      string
	Head             string
	Author           string
	Caps             DailyCaps
}

// Register opens the request and registers its key with the run's budget,
// expiry and metadata. It returns the key's id.
func (m Mint) Register(ctx context.Context) (string, error) {
	switch {
	case m.Budget.Sign() <= 0:
		return "", errors.New("gateway: the budget must be above 0")
	case m.Lifetime < time.Minute:
		return "", errors.New("gateway: the key must live at least one minute")
	case m.MaxParallel < 1:
		return "", errors.New("gateway: the parallel-request limit must be at least 1")
	case m.MaxRequestTokens < 1:
		return "", errors.New("gateway: the largest request must be at least 1 token")
	}
	key, err := m.Request.Open(m.AdminKey, m.Run)
	if err != nil {
		return "", err
	}
	spec := KeySpec{
		Key:         key,
		Alias:       m.Run.Alias(),
		MaxBudget:   m.Budget,
		Lifetime:    m.Lifetime,
		MaxParallel: m.MaxParallel,
		Metadata: map[string]string{
			MetaRepository:       m.Run.Repository,
			MetaPullRequest:      m.PullRequest,
			MetaHead:             m.Head,
			MetaRun:              m.Run.Run,
			MetaAttempt:          m.Run.Attempt,
			MetaMaxRequestTokens: strconv.FormatInt(m.MaxRequestTokens, 10),
		},
	}
	if spec.Team, err = m.ensure(ctx, "team", "costroid:"+m.Run.Repository, m.Caps.Repository); err != nil {
		return "", err
	}
	if spec.User, err = m.ensure(ctx, "user", m.authorID(), m.Caps.Author); err != nil {
		return "", err
	}
	if err := m.Client.Generate(ctx, m.AdminKey, spec); err != nil {
		return "", err
	}
	return KeyID(key), nil
}

// authorID names the author on the repository's host, or nothing when the
// event names no author.
func (m Mint) authorID() string {
	if m.Author == "" {
		return ""
	}
	host, _, _ := strings.Cut(m.Run.Repository, "/")
	return "costroid:" + host + "/" + m.Author
}

func (m Mint) ensure(ctx context.Context, kind, id string, limit *decimal.Decimal) (string, error) {
	if limit == nil || id == "" {
		return "", nil
	}
	if err := m.Client.EnsureDailyBudget(ctx, m.AdminKey, kind, id, *limit); err != nil {
		return "", fmt.Errorf("gateway: set the %s's daily cap: %w", kind, err)
	}
	return id, nil
}

// Close deletes the run's key. It reports whether a key was there to delete.
func Close(ctx context.Context, client *Client, adminKey string, run Binding) (bool, error) {
	return client.Delete(ctx, adminKey, run.Alias())
}
