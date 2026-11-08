package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/gateway"
)

const gatewayNote = "gateway-precheck: the gateway checks the key's budget before each request, and requests in flight can pass it by the overshoot bound"

// billedClasses are the classes a request can be charged in. The bound takes
// the highest price the sheet holds for them, so any mix of them is covered. A
// class the provider does not sell has no price and is passed over.
var billedClasses = []focus.TokenClass{
	focus.InputUncached, focus.CacheRead, focus.CacheWrite5m, focus.CacheWrite1h,
	focus.CacheWrite30m, focus.CacheWrite12h, focus.Output,
}

// heldKey is the run's key once the gateway has returned it, and the agent
// that runs on it. settings names a file the run wrote for the agent, which
// holds the key, and removeSettings deletes it.
type heldKey struct {
	session        gateway.Session
	grant          gateway.Grant
	agent          adapter.Gatewayed
	settings       []string
	removeSettings func() error
	stderr         io.Writer
}

// bypassed is an agent that can name a flag of its command that sends its
// requests somewhere other than the gateway, or empty when none does.
type bypassed interface {
	GatewayBypass(argv []string) (string, error)
}

// settled is an agent that reaches the gateway through a settings file the run
// writes for it. Its arguments name the file, and remove deletes it.
type settled interface {
	GatewaySettings(argv []string, base, key string) (args []string, remove func() error, err error)
}

// openGateway waits for the run's key and holds it to what the run needs. A
// key that never authenticates stops the run before the agent starts, because
// the agent would have no credential. A command that routes requests around
// the gateway is refused before the gateway is asked anything, because the
// gateway's grade holds only for the requests that pass through it. An agent
// that takes the gateway from a settings file gets one once the key is held.
func (o Options) openGateway(ctx context.Context, agent adapter.Agent) (*heldKey, error) {
	if o.Gateway == nil {
		return nil, nil
	}
	gatewayed, ok := agent.(adapter.Gatewayed)
	if !ok {
		return nil, fmt.Errorf("run: %s cannot be pointed at a gateway, so a gateway key cannot run it", agent.Name())
	}
	if routed, ok := agent.(bypassed); ok {
		flag, err := routed.GatewayBypass(o.Argv)
		if err != nil {
			return nil, fmt.Errorf("run: %w", err)
		}
		if flag != "" {
			return nil, fmt.Errorf("run: %s sends %s's requests around the gateway, and a gateway cannot hold a run that routes around it; remove it", flag, agent.Name())
		}
	}
	info, err := o.Gateway.Await(ctx)
	if err != nil {
		return nil, fmt.Errorf("run: %w", err)
	}
	grant := gateway.Inspect(o.Gateway.Key, info, gateway.Expect{Budget: o.Budget, Run: o.CI.Run, Now: o.Now()})
	held := &heldKey{session: *o.Gateway, grant: grant, agent: gatewayed, stderr: o.Stderr}
	if configured, ok := agent.(settled); ok {
		held.settings, held.removeSettings, err = configured.GatewaySettings(o.Argv, o.Gateway.Client.URL, o.Gateway.Key)
		if err != nil {
			return nil, fmt.Errorf("run: %w", err)
		}
	}
	return held, nil
}

// argv is the agent's command with what it needs to reach the gateway after
// every option it holds: before a "--" that ends them, or at the end.
func (h *heldKey) argv(argv []string) []string {
	args := slices.Concat(h.agent.GatewayArgs(h.session.Client.URL), h.settings)
	at := len(argv)
	if end := slices.Index(argv, "--"); end > 0 {
		at = end
	}
	return slices.Insert(slices.Clone(argv), at, args...)
}

// env is the agent's environment on the gateway.
func (h *heldKey) env(base []string) []string {
	return h.session.Env(base, h.agent)
}

// enforcement is gateway-precheck while the key and its store hold, and none
// with the reason otherwise.
func (h *heldKey) enforcement(storeFailure string) adapter.Enforcement {
	reasons := slices.Clone(h.grant.Problems)
	if storeFailure != "" {
		reasons = append(reasons, storeFailure)
	}
	if len(reasons) > 0 {
		return adapter.Enforcement{Grade: adapter.GradeNone, Note: "none: " + strings.Join(reasons, "; ")}
	}
	return adapter.Enforcement{Grade: adapter.GradeGatewayPrecheck, Note: gatewayNote}
}

// watch probes the gateway's store while the agent runs. Without a gateway it
// watches nothing. The run stops it once the agent has ended, and stopping it
// also removes the settings file the run wrote for the agent, because the
// file holds the key.
func (h *heldKey) watch(ctx context.Context) func() string {
	if h == nil {
		return func() string { return "" }
	}
	stop := h.session.Watch(ctx)
	return func() string {
		failure := stop()
		h.dropSettings()
		return failure
	}
}

// dropSettings removes the settings file the run wrote for the agent. A file
// that cannot be removed still holds the key, so the run says so.
func (h *heldKey) dropSettings() {
	if h.removeSettings == nil {
		return
	}
	err := h.removeSettings()
	if err == nil || errors.Is(err, fs.ErrNotExist) || h.stderr == nil {
		return
	}
	fmt.Fprintf(h.stderr, "costroid: warning: the settings file the run wrote for the agent holds the run's key and could not be removed: %v\n", err)
}

// overshootBound is the most a run can pass its budget by: the requests the
// key lets run at once, and one more, each of the largest size the key states,
// each token at the highest unit price the model's sheet holds at that context
// length. The one more is a request that has ended and that the gateway has not
// counted yet: LiteLLM v1.108.1 counts a request after it answers, and in six
// runs it had counted it before the second request after it was admitted. That
// is an observation, so checkOvershoot holds every run to it.
func overshootBound(prices *catalog.Catalog, grant gateway.Grant, request adapter.Request) (decimal.Decimal, error) {
	start, _ := focus.ChargePeriod(request.Start, request.End)
	var highest, highestFallback decimal.Decimal
	for _, class := range billedClasses {
		price, err := prices.Lookup(catalog.Query{
			Provider:           request.Provider,
			Model:              request.Model,
			Class:              class,
			ServiceTier:        request.ServiceTier,
			Region:             request.Region,
			ContextInputTokens: grant.MaxRequestTokens,
			At:                 start,
		})
		if errors.Is(err, catalog.ErrNoPrice) {
			continue
		}
		if err != nil {
			return decimal.Decimal{}, err
		}
		target := &highest
		if price.Fallback {
			target = &highestFallback
		}
		if price.ListUnitPrice.Cmp(*target) > 0 {
			*target = price.ListUnitPrice
		}
	}
	if highest.Sign() == 0 {
		highest = highestFallback
	}
	tokens := decimal.FromInt((grant.MaxParallel + 1) * grant.MaxRequestTokens).DivPow10(6)
	return tokens.Mul(highest), nil
}

// compareSpend sets the gateway's count of the key's spend beside the rows'
// catalog cost. The gateway holds the budget in its own prices, so a gap
// between the two is a gap in what the budget held. A gap within the float
// rounding of the gateway's sum is no gap. The rows keep their own figure
// whatever the gateway counted.
func (h *heldKey) compareSpend(ctx context.Context, result *Result) {
	spend, err := h.session.Spend(ctx, result.ListTotal)
	if err != nil {
		result.Report.Warnings = append(result.Report.Warnings, fmt.Sprintf("the gateway's count of the key's spend could not be read, so it is not set beside the rows: %v", err))
		return
	}
	if gateway.SameSpend(spend, result.ListTotal) {
		return
	}
	result.Report.Warnings = append(result.Report.Warnings, fmt.Sprintf(
		"the gateway counted USD %s on the run's key, and the rows, priced at the catalog, come to USD %s. The gateway holds the budget in its own figure, and the rows keep theirs", spend, result.ListTotal))
}

// checkOvershoot holds a gateway-precheck run to the bound its rows state. When
// the rows, priced at the catalog, pass the key's budget by more than a row's
// bound, the grade claimed more than the run showed, and it falls to none.
func (b rowBuilder) checkOvershoot(rows []focus.Row) (adapter.Enforcement, string) {
	if b.held == nil || b.enforcement.Grade != adapter.GradeGatewayPrecheck {
		return adapter.Enforcement{}, ""
	}
	total := listTotal(rows)
	over := total.Sub(b.held.grant.MaxBudget)
	for _, row := range rows {
		if row.OvershootBound == nil || over.Cmp(*row.OvershootBound) <= 0 {
			continue
		}
		reason := fmt.Sprintf("the run passed the key's budget by USD %s, more than the overshoot bound of USD %s its rows state", over, *row.OvershootBound)
		warning := fmt.Sprintf("the rows, priced at the catalog, come to USD %s against the key's budget of USD %s: an overshoot of USD %s, above the bound of USD %s, so the rows read none", total, b.held.grant.MaxBudget, over, *row.OvershootBound)
		return adapter.Enforcement{Grade: adapter.GradeNone, Note: "none: " + reason}, warning
	}
	return adapter.Enforcement{}, ""
}
