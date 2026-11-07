package policy

import "github.com/Costroid/costroid/internal/decimal"

// MinBaselineRuns is the fewest earlier runs a baseline is read from before a
// rule holds a run to it.
const MinBaselineRuns = 10

// Baseline is what this agent's earlier runs on the repository cost, in USD.
type Baseline struct {
	// Runs counts the earlier runs the median was read from.
	Runs int
	// MedianUSD is the median of their USD totals. It is never negative.
	MedianUSD decimal.Decimal
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

// baselineInput is the baseline as the pack reads it: for each increase key
// that is set, the USD amount a run may reach. The pack cannot add or
// multiply, so the limits are computed here. A baseline of too few runs is
// none.
func baselineInput(baseline *Baseline, settings Settings) map[string]any {
	if baseline == nil || baseline.Runs < MinBaselineRuns || baseline.MedianUSD.Sign() < 0 {
		return nil
	}
	input := map[string]any{"trust": string(trusted(baseline.Trust))}
	median := baseline.MedianUSD
	if increase, set := settings.Amounts[keyMaxIncrease]; set {
		input[keyMaxIncrease] = median.Add(increase).String()
	}
	if percent, set := settings.Amounts[keyMaxIncreasePercent]; set {
		input[keyMaxIncreasePercent] = median.Mul(decimal.FromInt(100).Add(percent)).DivPow10(2).String()
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
