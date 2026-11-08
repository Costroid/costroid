package claudecode

import (
	"fmt"
	"sync"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

// pinned is the catalog release every row is priced with, read once.
var pinned = sync.OnceValues(catalog.Load)

// figureParts is how closely Claude Code's own figure must equal a tier's:
// to one part in 10^figureParts. Claude Code adds its figures in binary64,
// which rounds each step by under one part in 10^16. Every recorded costUSD
// is the pinned catalog's exact figure to 1.6 parts in 10^16, and a sum over
// a million requests stays within one part in 10^10. The pinned sheet's two
// context tiers price the same tokens five times apart.
const figureParts = 9

// contextTier is the whole input a per-model entry states, and the note that
// says how its context tier was chosen.
//
// The entry's input is the sum of its requests', which bounds each of them.
// When that sum selects each class's lowest tier, every request is in that
// tier, and the sum is stated. Past the lowest tier the sum says nothing
// about any one request. Claude Code's own figure for the model does: it is
// the model's tokens at the prices of the tier its requests were billed in.
// The tier whose figure it matches prices the entry, and the sum is stated
// only when it selects that tier. When neither matches, or Claude Code
// states no figure, no input is stated, the lowest tier prices the entry,
// and the note says the tier is unknown and the figure may read low.
func contextTier(entry adapter.Request, stated *decimal.Decimal) (int64, string, error) {
	sheets, err := pinned()
	if err != nil {
		return 0, "", err
	}
	sum := entry.ContextInputTokens
	high, err := tierFigure(sheets, entry)
	if err != nil {
		return 0, "", err
	}
	if high.floor == nil {
		return sum, "", nil
	}
	lowest := entry
	lowest.ContextInputTokens = 0
	low, err := tierFigure(sheets, lowest)
	if err != nil {
		return 0, "", err
	}
	head := fmt.Sprintf("this model's input of %d tokens is a sum over its requests, which selects no context tier", sum)
	switch {
	case stated == nil:
		return 0, fmt.Sprintf("%s; Claude Code states no figure for it, so its context tier is unknown, and the prices up to %d input tokens apply, which may read low", head, low.top), nil
	case sameFigure(*stated, low.total):
		return 0, fmt.Sprintf("%s; Claude Code's own figure for it, USD %s, is its tokens at the prices up to %d input tokens, so that tier prices them", head, stated, low.top), nil
	case sameFigure(*stated, high.total):
		return sum, fmt.Sprintf("%s; Claude Code's own figure for it, USD %s, is its tokens at the prices above %d input tokens, so that tier prices them", head, stated, *high.floor), nil
	}
	return 0, fmt.Sprintf("%s; Claude Code's own figure for it, USD %s, is its tokens at neither tier's prices, so its context tier is unknown, and the prices up to %d input tokens apply, which may read low", head, stated, low.top), nil
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

// tierFigure prices an entry's billed tokens as its rows would be priced.
func tierFigure(sheets *catalog.Catalog, entry adapter.Request) (tiered, error) {
	start, _ := focus.ChargePeriod(entry.Start, entry.End)
	var out tiered
	for _, count := range entry.Tokens.Counts() {
		if count.Class.Informational() {
			continue
		}
		price, err := sheets.Lookup(catalog.Query{
			Provider:           entry.Provider,
			Model:              entry.Model,
			Class:              count.Class,
			ServiceTier:        entry.ServiceTier,
			Region:             entry.Region,
			ContextInputTokens: entry.ContextInputTokens,
			At:                 start,
		})
		if err != nil {
			return tiered{}, fmt.Errorf("claude-code: price %s %s to read its context tier: %w", entry.Model, count.Class, err)
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

// sameFigure says whether Claude Code's figure is the exact one, as binary64
// arithmetic leaves it.
func sameFigure(stated, exact decimal.Decimal) bool {
	difference := stated.Sub(exact)
	if difference.Sign() < 0 {
		difference = exact.Sub(stated)
	}
	return difference.Cmp(exact.DivPow10(figureParts)) <= 0
}
