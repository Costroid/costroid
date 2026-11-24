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
	Client   *Client
	AdminKey string
	// SealingKey is the private half the request was sealed to.
	SealingKey       string
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

// Registered is what Register did: the key's id, and whether the gateway
// held the key already, registered as this mint registers it.
type Registered struct {
	KeyID string
	Held  bool
}

// heldExpirySkew is how far the gateway's clock may run ahead of the job's
// when a held key's expiry is compared with the one a key registered now
// would have.
const heldExpirySkew = time.Minute

// Register opens the request and registers its key with the run's budget,
// expiry and metadata. A request sealed to the public key earlier versions
// derived from the admin key is refused. So is a key the gateway already
// holds, unless the gateway holds it as this mint registers it: under the
// run's alias, with the same budget, limits, metadata, team and user, no
// budget window, and an expiry no later than a key registered now would
// have. That key is a mint of this run and attempt run again, and Register
// registers nothing and reports it held. Any other held key is refused: the
// job that seals a request chooses its key, and a key minted once, such as
// one every run shares, is never a run's own. A key under the run's alias
// with another id is refused too, because a run has one key.
func (m Mint) Register(ctx context.Context) (Registered, error) {
	switch {
	case m.Budget.Sign() <= 0:
		return Registered{}, errors.New("gateway: the budget must be above 0")
	case m.Lifetime < time.Minute:
		return Registered{}, errors.New("gateway: the key must live at least one minute")
	case m.MaxParallel < 1:
		return Registered{}, errors.New("gateway: the parallel-request limit must be at least 1")
	case m.MaxRequestTokens < 1:
		return Registered{}, errors.New("gateway: the largest request must be at least 1 token")
	}
	if m.Request.SealingKey == derivedPublicKey(m.AdminKey) {
		return Registered{}, ErrDerivedSealingKey
	}
	key, err := m.Request.Open(m.SealingKey, m.Run)
	if err != nil {
		return Registered{}, err
	}
	spec := m.spec(key)
	held, found, err := m.Client.Held(ctx, m.AdminKey, KeyID(key))
	switch {
	case err != nil:
		return Registered{}, err
	case found && !spec.registered(held, time.Now()):
		return Registered{}, errors.New("gateway: the gateway holds the request's key already, under another alias or with other limits than this mint sets, and a run's key is one that only the run's own mint registered")
	case found:
		return Registered{KeyID: KeyID(key), Held: true}, nil
	}
	others, err := m.Client.HeldUnder(ctx, m.AdminKey, spec.Alias)
	if err != nil {
		return Registered{}, err
	}
	if len(others) > 0 {
		return Registered{}, fmt.Errorf("gateway: the gateway holds another key for run %s, attempt %s, already, and a run has one key", m.Run.Run, m.Run.Attempt)
	}
	if err := m.ensure(ctx, "team", spec.Team, m.Caps.Repository); err != nil {
		return Registered{}, err
	}
	if err := m.ensure(ctx, "user", spec.User, m.Caps.Author); err != nil {
		return Registered{}, err
	}
	if err := m.Client.Generate(ctx, m.AdminKey, spec); err != nil {
		return Registered{}, err
	}
	return Registered{KeyID: KeyID(key)}, nil
}

// spec is the key as the gateway registers it for the run: its team when the
// repository has a cap, and its user when the author has one and the event
// names the author.
func (m Mint) spec(key string) KeySpec {
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
	if m.Caps.Repository != nil {
		spec.Team = "costroid:" + m.Run.Repository
	}
	if m.Caps.Author != nil {
		spec.User = m.authorID()
	}
	return spec
}

// registered reports whether the held key is the spec as the gateway
// registered it, each fact compared both ways: the alias, the budget, the
// parallel-request limit, the metadata, every value and no other, the team
// and the user, each none where the spec sets none, and no budget window. The
// expiry is the one fact compared one way: the gateway set it when the key
// was registered, earlier than now, so it is no later than the spec's lifetime
// from now.
func (s KeySpec) registered(held HeldKey, now time.Time) bool {
	if held.Alias != s.Alias || held.MaxBudget == nil || held.MaxParallel == nil || *held.MaxParallel != s.MaxParallel {
		return false
	}
	if held.BudgetDuration != nil && *held.BudgetDuration != "" {
		return false
	}
	if budget, err := decimal.ParseNumber(held.MaxBudget.String()); err != nil || budget.Cmp(s.MaxBudget) != 0 {
		return false
	}
	if len(held.Metadata) != len(s.Metadata) {
		return false
	}
	for name, value := range s.Metadata {
		if stated, ok := held.Metadata[name].(string); !ok || stated != value {
			return false
		}
	}
	if orNone(held.Team) != s.Team || orNone(held.User) != s.User {
		return false
	}
	expires, set, err := held.ExpiresAt()
	return err == nil && set && !expires.After(now.Add(s.Lifetime+heldExpirySkew))
}

// orNone is a field the gateway may leave null, with null read as none.
func orNone(field *string) string {
	if field == nil {
		return ""
	}
	return *field
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

func (m Mint) ensure(ctx context.Context, kind, id string, limit *decimal.Decimal) error {
	if limit == nil || id == "" {
		return nil
	}
	if err := m.Client.EnsureDailyBudget(ctx, m.AdminKey, kind, id, *limit); err != nil {
		return fmt.Errorf("gateway: set the %s's daily cap: %w", kind, err)
	}
	return nil
}

// Close deletes the run's key. It reports whether a key was there to delete.
func Close(ctx context.Context, client *Client, adminKey string, run Binding) (bool, error) {
	return client.Delete(ctx, adminKey, run.Alias())
}
