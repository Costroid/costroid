package run

import (
	"fmt"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

// figureParts is how closely an agent's own figure must equal a tier's: to
// one part in 10^figureParts. Claude Code adds its figures in binary64, which
// rounds each step by under one part in 10^16. Every recorded costUSD is the
// pinned catalog's exact figure to 1.6 parts in 10^16, and a sum over a
// million requests stays within one part in 10^10. The pinned sheet's two
// context tiers price the same tokens five times apart.
const figureParts = 9

// withContextTier reads the context tier of an entry that carries tier
// evidence, and returns the entry as its rows are priced.
//
// The entry's input is the sum of its requests', which bounds each of them.
// When that sum selects each class's lowest tier, every request is in that
// tier, and the entry stands. Past the lowest tier the sum says nothing about
// any one request. The agent's own figure does: it is the entry's tokens at
// the prices of the tier its requests were billed in. The tier whose figure
// it matches prices the entry, and the sum is kept only when it selects that
// tier. When neither matches, or the agent states no figure, the sum is kept:
// the tier it selects is the highest any request could reach, it prices the
// entry, and the note says the tier is unknown and the figure may read high.
func (b rowBuilder) withContextTier(request adapter.Request) (adapter.Request, error) {
	evidence := request.TierEvidence
	if evidence == nil {
		return request, nil
	}
	high, err := b.tierFigure(request)
	if err != nil {
		return adapter.Request{}, err
	}
	if high.floor == nil {
		return request, nil
	}
	lowest := request
	lowest.ContextInputTokens = 0
	low, err := b.tierFigure(lowest)
	if err != nil {
		return adapter.Request{}, err
	}
	head := fmt.Sprintf("this model's input of %d tokens is a sum over its requests, which selects no context tier", request.ContextInputTokens)
	stated := evidence.USD
	var note string
	switch {
	case stated == nil:
		note = fmt.Sprintf("%s; %s states no figure for it, so its context tier is unknown, and the prices above %d input tokens apply, which may read high", head, evidence.Agent, *high.floor)
	case sameFigure(*stated, low.total):
		note = fmt.Sprintf("%s; %s's own figure for it, USD %s, is its tokens at the prices up to %d input tokens, so that tier prices them", head, evidence.Agent, stated, low.top)
		request.ContextInputTokens = 0
	case sameFigure(*stated, high.total):
		note = fmt.Sprintf("%s; %s's own figure for it, USD %s, is its tokens at the prices above %d input tokens, so that tier prices them", head, evidence.Agent, stated, *high.floor)
	default:
		note = fmt.Sprintf("%s; %s's own figure for it, USD %s, is its tokens at neither tier's prices, so its context tier is unknown, and the prices above %d input tokens apply, which may read high", head, evidence.Agent, stated, *high.floor)
	}
	if request.Note != "" {
		note = request.Note + "; " + note
	}
	request.Note = note
	return request, nil
}

// tiered is what an entry's billed tokens come to at the context tier its
// stated input selects.
type tiered struct {
	total decimal.Decimal
	// floor is the input the selected tier starts above, nil when the input
	// selected each class's lowest tier.
	floor *int64
	// top is the most input the selected tier holds, zero when it is open.
	top int64
}

// tierFigure prices an entry's billed tokens at the tier its stated input
// selects, with the catalog its rows are priced with.
func (b rowBuilder) tierFigure(request adapter.Request) (tiered, error) {
	start, _ := focus.ChargePeriod(request.Start, request.End)
	var out tiered
	for _, count := range request.Tokens.Counts() {
		if count.Class.Informational() {
			continue
		}
		price, err := b.catalog.Lookup(catalog.Query{
			Provider:           request.Provider,
			Model:              request.Model,
			Class:              count.Class,
			ServiceTier:        request.ServiceTier,
			Region:             request.Region,
			ContextInputTokens: request.ContextInputTokens,
			At:                 start,
		})
		if err != nil {
			return tiered{}, fmt.Errorf("run: price %s %s to read its context tier: %w", request.Model, count.Class, err)
		}
		out.total = out.total.Add(price.ListUnitPrice.Mul(decimal.FromInt(count.Tokens).DivPow10(6)))
		if price.ContextTierMin != nil {
			out.floor = price.ContextTierMin
		}
		if price.ContextTierMax != nil {
			out.top = *price.ContextTierMax
		}
	}
	return out, nil
}

// sameFigure says whether the agent's figure is the exact one, as binary64
// arithmetic leaves it.
func sameFigure(stated, exact decimal.Decimal) bool {
	difference := stated.Sub(exact)
	if difference.Sign() < 0 {
		difference = exact.Sub(stated)
	}
	return difference.Cmp(exact.DivPow10(figureParts)) <= 0
}
