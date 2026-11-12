package policy

import (
	"cmp"
	"slices"

	"github.com/Costroid/costroid/internal/decimal"
)

// BaselineWindow is how many of an agent's latest runs with a cost in one
// currency a baseline reads. Twenty runs put the nearest-rank P95 on the 19th
// cost of 20, so one outlier does not set it, and the window is the twenty
// reports that end the warn-only period.
const BaselineWindow = 20

// RunCost is what one earlier run of an agent cost in one currency, at list,
// over every basis, as the run rules read a run. CheckRun orders the runs:
// the host numbers check runs in the order it creates them.
type RunCost struct {
	CheckRun int64
	Currency string
	Cost     decimal.Decimal
}

// percentiles are a baseline in one currency: the nearest-rank P50 and P95 of
// the costs of the latest Runs runs, at most BaselineWindow of them.
type percentiles struct {
	Currency string
	Runs     int
	P50      decimal.Decimal
	P95      decimal.Decimal
}

// rolling reads a baseline per currency from the runs' costs, sorted by
// currency. Each currency reads its own runs alone, so no baseline adds or
// compares amounts in two currencies.
func rolling(costs []RunCost) []percentiles {
	byCurrency := map[string][]RunCost{}
	for _, cost := range costs {
		byCurrency[cost.Currency] = append(byCurrency[cost.Currency], cost)
	}
	baselines := make([]percentiles, 0, len(byCurrency))
	for currency, runs := range byCurrency {
		slices.SortStableFunc(runs, func(a, b RunCost) int { return cmp.Compare(b.CheckRun, a.CheckRun) })
		runs = runs[:min(len(runs), BaselineWindow)]
		sorted := make([]decimal.Decimal, len(runs))
		for index, run := range runs {
			sorted[index] = run.Cost
		}
		slices.SortFunc(sorted, decimal.Decimal.Cmp)
		baselines = append(baselines, percentiles{
			Currency: currency,
			Runs:     len(sorted),
			P50:      nearestRank(sorted, 50),
			P95:      nearestRank(sorted, 95),
		})
	}
	slices.SortFunc(baselines, func(a, b percentiles) int { return cmp.Compare(a.Currency, b.Currency) })
	return baselines
}

// nearestRank is the percent-th percentile of costs sorted from the lowest by
// the nearest-rank method: the cost at rank ceil(percent × n / 100), counted
// from 1. It is always one of the costs, so no amount is interpolated.
func nearestRank(sorted []decimal.Decimal, percent int) decimal.Decimal {
	rank := (percent*len(sorted) + 99) / 100
	return sorted[max(rank, 1)-1]
}

// BaselineOf is the USD baseline the increase rules read, nil when no run had
// a cost in USD. trust is who vouches for the runs' costs.
func BaselineOf(costs []RunCost, trust Trust) *Baseline {
	for _, baseline := range rolling(costs) {
		if baseline.Currency == "USD" {
			return &Baseline{Runs: baseline.Runs, MedianUSD: baseline.P50, P95USD: baseline.P95, Trust: trust}
		}
	}
	return nil
}
