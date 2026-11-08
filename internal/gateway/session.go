package gateway

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/decimal"
)

// Session is one run's key on one gateway, as the agent's job holds it.
type Session struct {
	Client *Client
	Key    string
	// Wait is how long to wait for the key to be registered.
	Wait time.Duration
	// Every is how often the store is probed while the agent runs.
	Every time.Duration
	// Poll is how often the key is asked for while it is awaited or while
	// its spend settles.
	Poll time.Duration
	// Settle bounds the wait for the key's spend to reach the gateway's store,
	// and Stable is how long an unchanged figure counts as settled. LiteLLM
	// writes spend to its store in batches.
	Settle, Stable time.Duration
}

// Expect is what the key must say about itself for the run to hold.
type Expect struct {
	Budget decimal.Decimal
	Run    string
	Now    time.Time
}

// Grant is what the gateway holds the run to.
type Grant struct {
	KeyID            string
	MaxBudget        decimal.Decimal
	MaxParallel      int64
	MaxRequestTokens int64
	// Problems name each fact the key lacks. A grant with any reads none.
	Problems []string
}

// Await waits until the key authenticates, then reads it back. A URL the key
// would cross the network in the clear to is refused before any wait.
func (s Session) Await(ctx context.Context) (KeyInfo, error) {
	if err := CheckURL(s.Client.URL); err != nil {
		return KeyInfo{}, err
	}
	deadline := time.Now().Add(s.Wait)
	for {
		info, err := s.Client.Info(ctx, s.Key, "")
		if err == nil {
			return info, nil
		}
		if time.Now().After(deadline) {
			return KeyInfo{}, fmt.Errorf("gateway: the run's key was not registered within %s: %w", s.Wait, err)
		}
		select {
		case <-ctx.Done():
			return KeyInfo{}, ctx.Err()
		case <-time.After(s.Poll):
		}
	}
}

// spendTolerance is how far the gateway's count may sit from the rows' total,
// as a fraction of the total, and still be the same figure. LiteLLM sums spend
// in binary floating point, so three requests of 0.006 read
// 0.018000000000000002. A float64 sum of n terms drifts by about n × 1.1e-16
// of the total; 1e-9 holds that for any run and is far below a cent of any
// budget.
var spendTolerance = decimal.MustParse("0.000000001")

// SameSpend reports whether the gateway's count and the rows' total are the
// same figure within spendTolerance.
func SameSpend(counted, total decimal.Decimal) bool {
	gap := counted.Sub(total)
	if gap.Sign() < 0 {
		gap = decimal.Decimal{}.Sub(gap)
	}
	return gap.Cmp(total.Mul(spendTolerance)) <= 0
}

// Spend reads what the gateway counted on the key, in its own prices. It waits
// while the figure is not the same as want and is still moving, because the
// gateway stores spend after the response.
func (s Session) Spend(ctx context.Context, want decimal.Decimal) (decimal.Decimal, error) {
	deadline := time.Now().Add(s.Settle)
	var last decimal.Decimal
	since := time.Now()
	for {
		info, err := s.Client.Info(ctx, s.Key, "")
		if err != nil {
			return decimal.Decimal{}, err
		}
		spend, err := decimal.ParseNumber(info.Spend.String())
		if err != nil {
			return decimal.Decimal{}, fmt.Errorf("gateway: the key's spend field reads %.40q, which is not a number", info.Spend.String())
		}
		if SameSpend(spend, want) {
			return spend, nil
		}
		if spend.Cmp(last) != 0 {
			last, since = spend, time.Now()
		}
		if time.Since(since) >= s.Stable || time.Now().After(deadline) {
			return spend, nil
		}
		select {
		case <-ctx.Done():
			return spend, ctx.Err()
		case <-time.After(s.Poll):
		}
	}
}

// Inspect holds the key the gateway returned to what the run needs.
func Inspect(key string, info KeyInfo, expect Expect) Grant {
	grant := Grant{KeyID: KeyID(key)}
	if info.MaxBudget == nil {
		grant.Problems = append(grant.Problems, "the key has no budget")
	} else if budget, err := decimal.Parse(info.MaxBudget.String()); err != nil || budget.Sign() <= 0 || budget.Cmp(expect.Budget) > 0 {
		grant.Problems = append(grant.Problems, fmt.Sprintf("the key's budget %s is not above 0 and at or under the run's %s", info.MaxBudget, expect.Budget))
	} else {
		grant.MaxBudget = budget
	}
	switch expires, set, err := info.ExpiresAt(); {
	case err != nil:
		grant.Problems = append(grant.Problems, err.Error())
	case !set || !expires.After(expect.Now):
		grant.Problems = append(grant.Problems, "the key has no expiry ahead of now")
	}
	if info.MaxParallel == nil || *info.MaxParallel < 1 {
		grant.Problems = append(grant.Problems, "the key has no parallel-request limit")
	} else {
		grant.MaxParallel = int64(*info.MaxParallel)
	}
	tokens, err := strconv.ParseInt(metadata(info, MetaMaxRequestTokens), 10, 64)
	if err != nil || tokens < 1 {
		grant.Problems = append(grant.Problems, "the key's metadata states no largest request")
	} else {
		grant.MaxRequestTokens = tokens
	}
	if run := metadata(info, MetaRun); run == "" || run != expect.Run {
		grant.Problems = append(grant.Problems, fmt.Sprintf("the key was minted for run %q, not this run %q", run, expect.Run))
	}
	return grant
}

func metadata(info KeyInfo, key string) string {
	value, _ := info.Metadata[key].(string)
	return value
}

// Env is the agent's environment: the base given, with the agent's own
// variables for the gateway's URL and the run's key in place of any it held,
// and without each credential the agent names to be removed.
func (s Session) Env(base []string, agent adapter.Gatewayed) []string {
	var set []string
	replaced := map[string]bool{}
	for _, entry := range agent.GatewayEnv(s.Client.URL, s.Key) {
		name, value, _ := strings.Cut(entry, "=")
		replaced[name] = true
		if value != "" {
			set = append(set, entry)
		}
	}
	env := slices.DeleteFunc(slices.Clone(base), func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		return replaced[name]
	})
	return append(env, set...)
}

// Watch probes the gateway's store until stop is called, and once more then.
// It returns the first reason the store did not hold, or empty.
func (s Session) Watch(ctx context.Context) (stop func() string) {
	watch := &watcher{session: s}
	watch.probe(ctx)
	probeCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(s.Every)
		defer ticker.Stop()
		for {
			select {
			case <-probeCtx.Done():
				return
			case <-ticker.C:
				watch.probe(probeCtx)
			}
		}
	}()
	return func() string {
		cancel()
		<-done
		watch.probe(ctx)
		return watch.reason()
	}
}

type watcher struct {
	session Session
	mu      sync.Mutex
	failed  string
}

func (w *watcher) probe(ctx context.Context) {
	readiness, err := w.session.Client.Readiness(ctx)
	if ctx.Err() != nil {
		return
	}
	var reason string
	switch {
	case err != nil:
		reason = "the gateway did not answer its readiness probe, so its budget check is unknown"
	case readiness.HTTPStatus == http.StatusOK && readiness.Store != storeConnected:
		reason = fmt.Sprintf("the gateway served while its store read %q, and a gateway that serves without its store reads no budget", readiness.Store)
	case readiness.HTTPStatus != http.StatusOK:
		reason = fmt.Sprintf("the gateway's readiness probe answered %d, so its budget check is unknown", readiness.HTTPStatus)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failed == "" {
		w.failed = reason
	}
}

func (w *watcher) reason() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.failed
}
