package reconcile

import (
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

// cacheWrite is the comparison class for a day whose write lifetimes the
// ledger cannot see. It never appears on a ledger row.
const cacheWrite focus.TokenClass = "cache_write"

var writeClasses = []focus.TokenClass{focus.CacheWrite5m, focus.CacheWrite1h, focus.CacheWrite30m, focus.CacheWrite12h}

// compared is the order a row's classes print in.
var compared = []focus.TokenClass{
	focus.InputUncached,
	focus.CacheRead,
	cacheWrite,
	focus.CacheWrite5m,
	focus.CacheWrite1h,
	focus.CacheWrite30m,
	focus.CacheWrite12h,
	focus.Output,
}

// compareTokens checks each class either side holds. Codex never reports a
// write's lifetime, so OpenAI writes always compare as one figure. Claude Code
// does unless a row says its lifetime was assumed.
func compareTokens(source string, group *ledgerGroup, usage []Usage) []TokenCheck {
	ledger := group.tokens
	provider := sumTokens(usage)
	if source == SourceOpenAI || group.assumedLifetime {
		ledger, provider = joinWrites(ledger), joinWrites(provider)
	}
	var checks []TokenCheck
	for _, class := range compared {
		mine, theirs := ledger[class], provider[class]
		if mine.Sign() == 0 && theirs.Sign() == 0 {
			continue
		}
		checks = append(checks, TokenCheck{
			Class:    class,
			Ledger:   mine,
			Provider: theirs,
			Delta:    theirs.Sub(mine),
			Within:   within(mine, theirs, TokenTolerancePercent),
		})
	}
	return checks
}

func sumTokens(usage []Usage) map[focus.TokenClass]decimal.Decimal {
	total := map[focus.TokenClass]decimal.Decimal{}
	for _, record := range usage {
		for class, tokens := range record.Tokens {
			total[class] = total[class].Add(tokens)
		}
	}
	return total
}

func joinWrites(tokens map[focus.TokenClass]decimal.Decimal) map[focus.TokenClass]decimal.Decimal {
	joined := map[focus.TokenClass]decimal.Decimal{}
	for class, count := range tokens {
		joined[class] = count
	}
	for _, class := range writeClasses {
		joined[cacheWrite] = joined[cacheWrite].Add(joined[class])
		delete(joined, class)
	}
	return joined
}
