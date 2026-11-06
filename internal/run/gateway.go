package run

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/adapter/claudecode"
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

// heldKey is the run's key once the gateway has returned it.
type heldKey struct {
	session gateway.Session
	grant   gateway.Grant
}

// openGateway waits for the run's key and holds it to what the run needs. A
// key that never authenticates stops the run before the agent starts, because
// the agent would have no credential.
func (o Options) openGateway(ctx context.Context, agent adapter.Agent) (*heldKey, error) {
	if o.Gateway == nil {
		return nil, nil
	}
	if agent.Name() != claudecode.Name {
		return nil, fmt.Errorf("run: a gateway key runs Claude Code, not %s", agent.Name())
	}
	info, err := o.Gateway.Await(ctx)
	if err != nil {
		return nil, fmt.Errorf("run: %w", err)
	}
	grant := gateway.Inspect(o.Gateway.Key, info, gateway.Expect{Budget: o.Budget, Run: o.CI.Run, Now: o.Now()})
	return &heldKey{session: *o.Gateway, grant: grant}, nil
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
// watches nothing.
func (h *heldKey) watch(ctx context.Context) func() string {
	if h == nil {
		return func() string { return "" }
	}
	return h.session.Watch(ctx)
}

// overshootBound is the most a run can pass its budget by: the requests the
// key lets run at once, each of the largest size the key states, each token at
// the highest unit price the model's sheet holds at that context length.
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
	tokens := decimal.FromInt(grant.MaxParallel * grant.MaxRequestTokens).DivPow10(6)
	return tokens.Mul(highest), nil
}
