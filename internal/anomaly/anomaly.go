// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package anomaly is the pure statistical spike/dip detector over a daily
// decimal series. It has NO knowledge of storage or HTTP: it takes a
// date-ascending sequence of observations in and returns the anomalous ones
// out, so the whole method is unit-testable in isolation and reusable across
// scopes (a total series and each grouping-key series).
//
// # Method (robust, transparent, exact)
//
// For each scored observation x on day d of a series, the baseline is the
// trailing window of up to WindowDays observed days STRICTLY BEFORE d (never
// including d). A day is scored only when its window holds at least
// MinObservations observed days; otherwise it is never flagged. Over the window:
//
//	median    — the exact median (odd count: the middle element; even count:
//	            the two middle elements averaged as a.Add(b).DivRound(2, 19),
//	            never decimal.Avg or plain Div, both of which silently round at
//	            DivisionPrecision).
//	mad       — the median (same rule) of the absolute deviations |x_i − median|.
//	scaledMad — mad × ConsistencyConstant (1.4826, the standard MAD→σ constant,
//	            as R's stats::mad uses).
//	deviation — |x − median|.
//
// d is flagged iff deviation > K × scaledMad AND deviation ≥ RelativeFloor ×
// |median| (two-sided, strict >). The relative floor is what keeps a flat but
// NONZERO series (mad = 0, median ≠ 0) from flagging noise: there the first
// condition degenerates to deviation > 0, so the floor alone gates. An all-zero
// window (median = 0 AND mad = 0) degenerates both conditions, so ANY nonzero
// observation is flagged (a genuine first cost after a sustained-zero baseline)
// while a zero observation is not (strict >). Direction is "increase" when
// x > median and "decrease" when x < median.
//
// # Exactness
//
// Every value is a shopspring/decimal — NEVER float64. Inputs carry at most
// scale 18 (the store's DECIMAL(38,18)); an even-count median then needs at most
// scale 19, so DivRound(_, 19) is exact, and the deviations of a scale-19 median
// end in a 5 at scale 19, so their own even-count median halves exactly at scale
// 19 too. The engine's median()/mad() interpolate in floating point on even
// counts and are therefore never used here.
//
// # Input contract
//
// Detect REQUIRES its input to be date-ascending and does not re-sort it (least
// of all by value): the store already returns days ascending. Missing days are
// simply absent observations — this package never zero-fills a gap, so a hole in
// the calendar is "no observation", not "$0". Zero and negative observations
// that ARE present (credits, corrections netting down) are real observations.
//
// # Transparency
//
// K, WindowDays, MinObservations, ConsistencyConstant, and RelativeFloor are
// fixed constants with no per-request knobs, and every flag carries median, mad,
// scaledMad, threshold, and deviation so it is hand-recomputable from the
// published daily decimal strings — that auditability is the product
// differentiator versus opaque ML detectors.
package anomaly

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// Fixed detection parameters. WindowDays caps the trailing baseline;
// MinObservations is the floor below which a day is never scored.
const (
	// WindowDays is the maximum number of trailing observed days in the baseline.
	WindowDays = 30
	// MinObservations is the minimum baseline size required to score a day.
	MinObservations = 10
)

// Detection constants as exact decimals. They are treated as immutable and are
// echoed (via String) in the API response so every flag is hand-recomputable.
var (
	// K is the strict-threshold multiple of the scaled MAD.
	K = decimal.NewFromInt(3)
	// ConsistencyConstant scales the MAD to a standard-deviation-comparable
	// spread (the R stats::mad convention).
	ConsistencyConstant = decimal.RequireFromString("1.4826")
	// RelativeFloor is the fraction of |median| a deviation must also reach, so a
	// flat nonzero series does not flag mere rounding noise.
	RelativeFloor = decimal.RequireFromString("0.1")
)

// two is the exact divisor for even-count medians (never a float).
var two = decimal.NewFromInt(2)

// Observation is one day's value in a series. Detect requires the input
// date-ascending; Value is exact (never float64).
type Observation struct {
	Date  time.Time
	Value decimal.Decimal
}

// Flag is one detected anomaly, carrying every statistic needed to recompute
// the decision by hand. Threshold is K × ScaledMAD.
type Flag struct {
	Date      time.Time
	Direction string // "increase" or "decrease"
	Observed  decimal.Decimal
	Median    decimal.Decimal
	MAD       decimal.Decimal
	ScaledMAD decimal.Decimal
	Threshold decimal.Decimal
	Deviation decimal.Decimal
}

// Detect returns a flag for each observation that qualifies as an anomaly, in
// the input's (date-ascending) order. The input MUST be date-ascending; Detect
// does not re-sort it. An observation is scored only when at least
// MinObservations observations precede it (capped at the trailing WindowDays);
// otherwise it is skipped, never flagged.
func Detect(series []Observation) []Flag {
	var flags []Flag
	for i := range series {
		lo := i - WindowDays
		if lo < 0 {
			lo = 0
		}
		window := series[lo:i]
		if len(window) < MinObservations {
			continue
		}
		if f, ok := score(series[i], window); ok {
			flags = append(flags, f)
		}
	}
	return flags
}

// score evaluates one observation against its (non-empty) baseline window and
// reports whether it is flagged, with the full statistic set.
func score(obs Observation, window []Observation) (Flag, bool) {
	values := make([]decimal.Decimal, len(window))
	for i, w := range window {
		values[i] = w.Value
	}
	median := medianOf(values)

	deviations := make([]decimal.Decimal, len(values))
	for i, v := range values {
		deviations[i] = v.Sub(median).Abs()
	}
	mad := medianOf(deviations)
	scaledMAD := mad.Mul(ConsistencyConstant)
	threshold := K.Mul(scaledMAD)

	deviation := obs.Value.Sub(median).Abs()
	floor := RelativeFloor.Mul(median.Abs())

	// Two-sided, strict >: a deviation exactly equal to the threshold does not
	// flag; the relative floor independently gates a flat nonzero series. (The
	// negated form — deviation ≤ threshold OR deviation < floor — of "deviation >
	// threshold AND deviation ≥ floor".)
	if deviation.LessThanOrEqual(threshold) || deviation.LessThan(floor) {
		return Flag{}, false
	}
	direction := "increase"
	if obs.Value.LessThan(median) {
		direction = "decrease"
	}
	return Flag{
		Date:      obs.Date,
		Direction: direction,
		Observed:  obs.Value,
		Median:    median,
		MAD:       mad,
		ScaledMAD: scaledMAD,
		Threshold: threshold,
		Deviation: deviation,
	}, true
}

// medianOf returns the exact median of values. It sorts a COPY by Cmp (never
// mutating the caller's slice) and, for an even count, averages the two middle
// elements as a.Add(b).DivRound(2, 19) — exact for scale-≤18 inputs. It panics
// on an empty slice; Detect only ever calls it with a window of at least
// MinObservations elements.
func medianOf(values []decimal.Decimal) decimal.Decimal {
	sorted := make([]decimal.Decimal, len(values))
	copy(sorted, values)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Cmp(sorted[j]) < 0 })
	n := len(sorted)
	mid := n / 2
	if n%2 == 1 {
		return sorted[mid]
	}
	return sorted[mid-1].Add(sorted[mid]).DivRound(two, 19)
}
