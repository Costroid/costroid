package policy

import (
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

// MinBaselineRuns is the fewest earlier runs a baseline is read from before a
// rule holds a run to it. Below it the nearest-rank P95 of 9 runs is their
// highest cost, and one run moves the P50.
const MinBaselineRuns = 10

// MinP95Runs is the fewest earlier runs whose P95 run_p95 holds a run to. The
// nearest-rank P95 of n runs is the cost at rank ceil(0.95 n), which for every
// n below 20 is the highest cost, so one costly run would set the limit. At 20
// it is the 19th, and the window holds no more.
const MinP95Runs = BaselineWindow

// maxBaselineUSD is the largest amount a ledger holds, numeric(38, 12). A
// median or a P95 past it, or of more fractional digits, is no run's cost.
// The largest factor times it is 55 characters, so the pack still compares
// it as a decimal string.
var maxBaselineUSD = decimal.MustParse("99999999999999999999999999.999999999999")

// usable reports a baseline whose median and P95 are amounts a ledger holds.
// Any other is none.
func usable(baseline *Baseline) bool {
	return baseline != nil && holdable(baseline.MedianUSD) && holdable(baseline.P95USD)
}

func holdable(amount decimal.Decimal) bool {
	return amount.Sign() >= 0 && amount.Scale() <= focus.MaxScale && amount.Cmp(maxBaselineUSD) <= 0
}

// Baseline is what this agent's earlier runs on the repository cost, in USD.
type Baseline struct {
	// Runs counts the earlier runs the median was read from.
	Runs int
	// MedianUSD is the median of their USD totals, their nearest-rank P50.
	// It is never negative.
	MedianUSD decimal.Decimal
	// P95USD is the nearest-rank P95 of their USD totals.
	P95USD decimal.Decimal
	// Trust is who vouches for those runs' rows. Only a token-verified
	// baseline can show that a run stayed within it.
	Trust Trust
}

// Lifetime is what the pull request's earlier runs cost, as their reports
// recorded it. It leaves out the run the check is about.
type Lifetime struct {
	// USD is the earlier runs' USD total over every cost basis. It is never
	// negative.
	USD decimal.Decimal
	// OtherCurrency says an earlier run recorded an amount in another
	// currency, which USD leaves out.
	OtherCurrency bool
	// MayRepeat says a report may be counted twice, so USD can read high.
	MayRepeat bool
	// Trust is who vouches for those runs' rows. Only a token-verified
	// lifetime can show that a pull request stayed within its budget.
	Trust Trust
}

// baselineInput is the baseline as the pack reads it: its trust, its runs,
// its P50 and P95 in USD, for each increase key that is set, the USD amount a
// run may reach, and with max_p95_factor set and MinP95Runs runs, the P95
// times the factor. The pack cannot add or multiply, so the limits are
// computed here, exactly. A baseline of too few runs, or one that is not
// usable, is none.
func baselineInput(baseline *Baseline, settings Settings) map[string]any {
	if !usable(baseline) || baseline.Runs < MinBaselineRuns {
		return nil
	}
	input := map[string]any{
		"trust":   string(trusted(baseline.Trust)),
		"runs":    baseline.Runs,
		"p50_usd": baseline.MedianUSD.String(),
		"p95_usd": baseline.P95USD.String(),
	}
	median := baseline.MedianUSD
	if increase, set := settings.Amounts[keyMaxIncrease]; set {
		input[keyMaxIncrease] = median.Add(increase).String()
	}
	if percent, set := settings.Amounts[keyMaxIncreasePercent]; set {
		input[keyMaxIncreasePercent] = median.Mul(decimal.FromInt(100).Add(percent)).DivPow10(2).String()
	}
	if factor, set := settings.Amounts[keyMaxP95Factor]; set && baseline.Runs >= MinP95Runs {
		input[keyMaxP95Factor] = baseline.P95USD.Mul(factor).String()
	}
	return input
}

// lifetimeInput is the pull request's total as the pack reads it, this run
// included.
func lifetimeInput(lifetime *Lifetime, usage Usage) map[string]any {
	if lifetime == nil || lifetime.USD.Sign() < 0 {
		return nil
	}
	return map[string]any{
		"trust":          string(trusted(lifetime.Trust)),
		"total_usd":      lifetime.total(usage).String(),
		"may_repeat":     lifetime.MayRepeat,
		"other_currency": lifetime.OtherCurrency,
	}
}

// total is the pull request's USD total with this run's rows added.
func (l Lifetime) total(usage Usage) decimal.Decimal {
	run, _ := usage.usd()
	return l.USD.Add(run)
}

// trusted reads any trust but a token's as unverified.
func trusted(trust Trust) Trust {
	if trust != TokenVerified {
		return Unverified
	}
	return trust
}
