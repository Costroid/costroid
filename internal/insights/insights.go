// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package insights computes a ranked list of deterministic, formula-transparent
// cost observations from already-queried store data. It has no knowledge of
// storage or HTTP: callers fetch inputs and map the returned values to the wire
// shape. Every number is an exact shopspring/decimal; the only division form is
// DivRound at storage.MaxDecimalScale with an explicit zero-divisor guard.
// Anomaly statistics arrive precomputed from the shared scan; this package
// never reimplements detector arithmetic.
package insights

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/allocation"
	"github.com/Costroid/costroid/internal/anomaly"
	"github.com/Costroid/costroid/internal/anomalyscan"
	"github.com/Costroid/costroid/internal/storage"
)

// Machine type identifiers for each observation.
const (
	TypeTopMover              = "top-mover"
	TypeUntaggedSpend         = "untagged-spend"
	TypeUnallocatedSpend      = "unallocated-spend"
	TypeAnomalyDigest         = "anomaly-digest"
	TypeUnitCostDrift         = "unit-cost-drift"
	TypeCommitmentRealization = "commitment-realization"
)

// Parameters holds the fixed constants needed to recompute every observation.
// Anomaly fields are single-sourced from the anomaly package; DivisionScale is
// storage.MaxDecimalScale.
type Parameters struct {
	K                   string
	ConsistencyConstant string
	WindowDays          int
	MinObservations     int
	RelativeFloor       string
	DivisionScale       int
}

// FixedParameters returns the detector constants and division scale.
func FixedParameters() Parameters {
	return Parameters{
		K:                   anomaly.K.String(),
		ConsistencyConstant: anomaly.ConsistencyConstant.String(),
		WindowDays:          anomaly.WindowDays,
		MinObservations:     anomaly.MinObservations,
		RelativeFloor:       anomaly.RelativeFloor.String(),
		DivisionScale:       storage.MaxDecimalScale,
	}
}

// Evidence is one name/value pair on an observation.
type Evidence struct {
	Name  string
	Value string
}

// Period is the optional date window carried on an observation.
// Zero times are omitted by the API mapper.
type Period struct {
	Start         time.Time
	End           time.Time
	PreviousStart time.Time
	PreviousEnd   time.Time
}

// Link is structured dashboard state (never a URL or hash fragment).
// Empty strings are omitted by the API mapper.
type Link struct {
	View     string
	Start    string
	End      string
	GroupBy  string
	TagKey   string
	Currency string
	Provider string
	Metric   string
}

// Insight is one ranked observation.
type Insight struct {
	Type      string
	Title     string
	Body      string
	Magnitude decimal.Decimal
	Dimension string // empty = omit on the wire
	Key       string // empty = omit on the wire
	Evidence  []Evidence
	Period    Period
	Link      Link
}

// TagSpend is one tag key's untagged bucket total vs the same query's window total.
type TagSpend struct {
	TagKey        string
	UntaggedTotal decimal.Decimal
	WindowTotal   decimal.Decimal
}

// MetricSeries is one business metric's cost and quantity streams for the
// current and previous windows (unit-economics covered-days merge).
type MetricSeries struct {
	Name               string
	CurrentCosts       storage.DailyCosts
	CurrentQuantities  []storage.DayQuantity
	PreviousCosts      storage.DailyCosts
	PreviousQuantities []storage.DayQuantity
}

// Input is everything the engine needs; the handler performs all store access.
type Input struct {
	Currency    string
	WindowStart time.Time // zero when the request omitted start
	WindowEnd   time.Time // zero when the request omitted end

	// Top-mover (only when both window bounds are set).
	CurrentServices  map[string]decimal.Decimal
	PreviousServices map[string]decimal.Decimal
	PreviousHasDays  bool

	// Untagged-spend: one entry per known tag key.
	TagSpends []TagSpend

	// Unallocated-spend: considered only when AllocationReady.
	AllocationReady       bool
	UnallocatedTotal      decimal.Decimal
	AllocationWindowTotal decimal.Decimal

	// Anomaly-digest: already window-filtered flags from anomalyscan.Flags.
	Flags []anomalyscan.ScopedFlag

	// Unit-cost-drift (only when both window bounds are set).
	Metrics []MetricSeries

	// Commitment-realization for the resolved currency.
	BilledTotal    decimal.Decimal
	EffectiveTotal decimal.Decimal
	HasCommitment  bool // true when a CostTotals row exists for the currency
}

type serviceDelta struct {
	key             string
	total           decimal.Decimal
	previous        decimal.Decimal
	previousDefined bool
	delta           decimal.Decimal
}

// Compute returns the ranked observation list for in. Empty when nothing qualifies.
func Compute(in Input) []Insight {
	out := make([]Insight, 0)
	basePeriod := Period{Start: in.WindowStart, End: in.WindowEnd}
	baseLink := costsLink(in.Currency, in.WindowStart, in.WindowEnd)

	if !in.WindowStart.IsZero() && !in.WindowEnd.IsZero() {
		out = append(out, topMovers(in, basePeriod, baseLink)...)
	}
	if insight, ok := untaggedSpend(in, basePeriod, baseLink); ok {
		out = append(out, insight)
	}
	if insight, ok := unallocatedSpend(in, basePeriod, baseLink); ok {
		out = append(out, insight)
	}
	if insight, ok := anomalyDigest(in, basePeriod, baseLink); ok {
		out = append(out, insight)
	}
	if !in.WindowStart.IsZero() && !in.WindowEnd.IsZero() {
		out = append(out, unitCostDrifts(in, basePeriod)...)
	}
	if insight, ok := commitmentRealization(in, basePeriod, baseLink); ok {
		out = append(out, insight)
	}

	sortInsights(out)
	return out
}

func costsLink(currency string, start, end time.Time) Link {
	l := Link{View: "costs", Currency: currency}
	if !start.IsZero() {
		l.Start = start.UTC().Format(time.DateOnly)
	}
	if !end.IsZero() {
		l.End = end.UTC().Format(time.DateOnly)
	}
	return l
}

func precedingWindow(start, end time.Time) (prevStart, prevEnd time.Time) {
	// prevEnd = start - 1 day; prevStart = prevEnd - (end - start).
	prevEnd = start.AddDate(0, 0, -1)
	prevStart = prevEnd.Add(-end.Sub(start))
	return prevStart, prevEnd
}

// --- Display helpers (body prose only) ---
//
// These format numbers for human reading and are used ONLY when building an
// Insight's Body. Evidence pair values and the Magnitude field keep the exact
// unrounded decimal, so a reader can always check a rounded sentence against
// the full-precision numbers printed beneath it. Both helpers stay on
// shopspring/decimal end to end: rounding is exact decimal rounding and
// grouping is string manipulation over the decimal's own digits, so no float
// ever exists on this path.

// displayMoney renders an amount rounded to two decimal places with the
// integer part grouped in thousands, e.g. 964050.632653589793238462 becomes
// "964,050.63". Two decimals are always shown, so 100 becomes "100.00".
func displayMoney(v decimal.Decimal) string {
	return groupedFixed(v, 2)
}

// groupedFixed renders v at an exact decimal scale with the integer part
// grouped in thousands.
func groupedFixed(v decimal.Decimal, scale int32) string {
	// StringFixed rounds half away from zero at the given scale and always
	// emits exactly that many decimal places.
	fixed := v.StringFixed(scale)
	sign := ""
	if rest, negative := strings.CutPrefix(fixed, "-"); negative {
		sign, fixed = "-", rest
	}
	integer, fraction, _ := strings.Cut(fixed, ".")
	return sign + groupThousands(integer) + "." + fraction
}

// displayUnitCost renders a per-unit rate. Unit costs routinely sit far below
// one currency unit, where the two-decimal money rule collapses two distinct
// rates into an identical "0.04" and a real movement into "0.00" — a sentence
// that then contradicts the cost it reports beside it. This keeps three
// significant digits instead, with a two-decimal floor so rates at or above
// one unit still read like money.
func displayUnitCost(v decimal.Decimal) string {
	return groupedFixed(v, unitCostScale(v))
}

// unitCostScale returns the scale that preserves three significant digits of v,
// never fewer than the two places money uses. The scale is derived from the
// decimal's own digit string, so this stays exact like the rest of the path.
func unitCostScale(v decimal.Decimal) int32 {
	integer, fraction, _ := strings.Cut(v.Abs().String(), ".")
	if integer != "0" {
		return 2
	}
	lead := 0
	for lead < len(fraction) && fraction[lead] == '0' {
		lead++
	}
	if lead == len(fraction) {
		return 2 // zero has no significant digit to preserve
	}
	return int32(lead + 3)
}

// groupThousands inserts a comma every three digits from the right of an
// already-extracted run of decimal digits.
func groupThousands(digits string) string {
	if len(digits) <= 3 {
		return digits
	}
	var b strings.Builder
	b.Grow(len(digits) + (len(digits)-1)/3)
	lead := len(digits) % 3
	if lead > 0 {
		b.WriteString(digits[:lead])
	}
	for i := lead; i < len(digits); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(digits[i : i+3])
	}
	return b.String()
}

// displayPercent renders a DERIVED share as a human percentage rounded to one
// decimal place, e.g. 0.201595594066514939 becomes "20.2%". A trailing ".0" is
// trimmed, so a whole percentage reads "25%" rather than "25.0%".
func displayPercent(share decimal.Decimal) string {
	fixed := share.Mul(decimal.NewFromInt(100)).StringFixed(1)
	return strings.TrimSuffix(fixed, ".0") + "%"
}

func topMovers(in Input, base Period, baseLink Link) []Insight {
	if !in.PreviousHasDays {
		return nil
	}
	prevStart, prevEnd := precedingWindow(in.WindowStart, in.WindowEnd)
	period := Period{
		Start:         base.Start,
		End:           base.End,
		PreviousStart: prevStart,
		PreviousEnd:   prevEnd,
	}

	var rows []serviceDelta
	for key, total := range in.CurrentServices {
		prev, ok := in.PreviousServices[key]
		row := serviceDelta{key: key, total: total, previousDefined: ok}
		if ok {
			row.previous = prev
		} else {
			prev = decimal.Zero
		}
		row.delta = total.Sub(prev)
		rows = append(rows, row)
	}

	var bestInc, bestDec *serviceDelta
	for i := range rows {
		r := &rows[i]
		if r.delta.IsPositive() {
			if bestInc == nil || r.delta.GreaterThan(bestInc.delta) ||
				(r.delta.Equal(bestInc.delta) && r.key < bestInc.key) {
				bestInc = r
			}
		}
		if r.delta.IsNegative() {
			if bestDec == nil || r.delta.LessThan(bestDec.delta) ||
				(r.delta.Equal(bestDec.delta) && r.key < bestDec.key) {
				bestDec = r
			}
		}
	}

	var out []Insight
	if bestInc != nil {
		out = append(out, makeTopMover(*bestInc, true, period, baseLink))
	}
	if bestDec != nil {
		out = append(out, makeTopMover(*bestDec, false, period, baseLink))
	}
	return out
}

func makeTopMover(row serviceDelta, increase bool, period Period, baseLink Link) Insight {
	ev := []Evidence{{Name: "total", Value: row.total.String()}}
	if row.previousDefined {
		ev = append(ev, Evidence{Name: "previousTotal", Value: row.previous.String()})
	}
	ev = append(ev, Evidence{Name: "delta", Value: row.delta.String()})

	var title, body string
	if increase {
		title = "Largest service cost increase"
		if row.previousDefined {
			body = fmt.Sprintf(
				"Service %s rose from %s to %s (delta %s) between the previous window and the current window.",
				row.key, displayMoney(row.previous), displayMoney(row.total), displayMoney(row.delta),
			)
		} else {
			body = fmt.Sprintf(
				"Service %s is new in the current window with total %s (delta %s).",
				row.key, displayMoney(row.total), displayMoney(row.delta),
			)
		}
	} else {
		title = "Largest service cost decrease"
		if row.previousDefined {
			body = fmt.Sprintf(
				"Service %s fell from %s to %s (delta %s) between the previous window and the current window.",
				row.key, displayMoney(row.previous), displayMoney(row.total), displayMoney(row.delta),
			)
		} else {
			body = fmt.Sprintf(
				"Service %s is new in the current window with total %s (delta %s).",
				row.key, displayMoney(row.total), displayMoney(row.delta),
			)
		}
	}

	return Insight{
		Type:      TypeTopMover,
		Title:     title,
		Body:      body,
		Magnitude: row.delta.Abs(),
		Dimension: "service",
		Key:       row.key,
		Evidence:  ev,
		Period:    period,
		Link:      baseLink,
	}
}

func untaggedSpend(in Input, base Period, baseLink Link) (Insight, bool) {
	if len(in.TagSpends) == 0 {
		return Insight{}, false
	}
	best := in.TagSpends[0]
	for _, ts := range in.TagSpends[1:] {
		if ts.UntaggedTotal.GreaterThan(best.UntaggedTotal) ||
			(ts.UntaggedTotal.Equal(best.UntaggedTotal) && ts.TagKey < best.TagKey) {
			best = ts
		}
	}
	if best.UntaggedTotal.IsZero() {
		return Insight{}, false
	}

	share := decimal.Zero
	if !best.WindowTotal.IsZero() {
		share = best.UntaggedTotal.DivRound(best.WindowTotal, storage.MaxDecimalScale)
	}
	pct := displayPercent(share)

	link := baseLink
	link.GroupBy = "tag"
	link.TagKey = best.TagKey

	return Insight{
		Type:      TypeUntaggedSpend,
		Title:     "Untagged spend",
		Body:      fmt.Sprintf("Of the window total %s, %s is untagged for tag key %s (%s of the window).", displayMoney(best.WindowTotal), displayMoney(best.UntaggedTotal), best.TagKey, pct),
		Magnitude: best.UntaggedTotal,
		Dimension: "tagKey",
		Key:       best.TagKey,
		Evidence: []Evidence{
			{Name: "untaggedTotal", Value: best.UntaggedTotal.String()},
			{Name: "windowTotal", Value: best.WindowTotal.String()},
			{Name: "share", Value: share.String()},
		},
		Period: base,
		Link:   link,
	}, true
}

func unallocatedSpend(in Input, base Period, baseLink Link) (Insight, bool) {
	if !in.AllocationReady || in.UnallocatedTotal.IsZero() {
		return Insight{}, false
	}

	share := decimal.Zero
	if !in.AllocationWindowTotal.IsZero() {
		share = in.UnallocatedTotal.DivRound(in.AllocationWindowTotal, storage.MaxDecimalScale)
	}
	pct := displayPercent(share)

	link := baseLink
	link.GroupBy = "allocation"

	return Insight{
		Type:      TypeUnallocatedSpend,
		Title:     "Unallocated spend",
		Body:      fmt.Sprintf("Of the window total %s, %s is unallocated (%s of the window).", displayMoney(in.AllocationWindowTotal), displayMoney(in.UnallocatedTotal), pct),
		Magnitude: in.UnallocatedTotal,
		Key:       allocation.UnallocatedLabel,
		Evidence: []Evidence{
			{Name: "unallocatedTotal", Value: in.UnallocatedTotal.String()},
			{Name: "windowTotal", Value: in.AllocationWindowTotal.String()},
			{Name: "share", Value: share.String()},
		},
		Period: base,
		Link:   link,
	}, true
}

func anomalyDigest(in Input, base Period, baseLink Link) (Insight, bool) {
	if len(in.Flags) == 0 {
		return Insight{}, false
	}
	best := in.Flags[0]
	for _, sf := range in.Flags[1:] {
		if betterAnomaly(sf, best) {
			best = sf
		}
	}
	// magnitude = |observed - median| (exact); Flag.Deviation already holds that absolute.
	magnitude := best.Flag.Observed.Sub(best.Flag.Median).Abs()

	ev := []Evidence{
		{Name: "flagCount", Value: fmt.Sprintf("%d", len(in.Flags))},
		{Name: "date", Value: best.Flag.Date.UTC().Format(time.DateOnly)},
	}
	if best.Scope == "key" {
		ev = append(ev, Evidence{Name: "key", Value: best.Key})
	}
	ev = append(ev,
		Evidence{Name: "observed", Value: best.Flag.Observed.String()},
		Evidence{Name: "median", Value: best.Flag.Median.String()},
		Evidence{Name: "threshold", Value: best.Flag.Threshold.String()},
		Evidence{Name: "deviation", Value: best.Flag.Deviation.String()},
		Evidence{Name: "direction", Value: best.Flag.Direction},
	)

	body := fmt.Sprintf(
		"%d anomaly flag(s) landed in the window; the largest absolute deviation is %s on %s (%s).",
		len(in.Flags), displayMoney(magnitude), best.Flag.Date.UTC().Format(time.DateOnly), best.Flag.Direction,
	)
	if best.Scope == "key" {
		body = fmt.Sprintf(
			"%d anomaly flag(s) landed in the window; the largest absolute deviation is %s on %s for service %s (%s).",
			len(in.Flags), displayMoney(magnitude), best.Flag.Date.UTC().Format(time.DateOnly), best.Key, best.Flag.Direction,
		)
	}

	insight := Insight{
		Type:      TypeAnomalyDigest,
		Title:     "Anomaly digest",
		Body:      body,
		Magnitude: magnitude,
		Evidence:  ev,
		Period:    base,
		Link:      baseLink,
	}
	if best.Scope == "key" {
		insight.Dimension = "service"
		insight.Key = best.Key
	}
	return insight, true
}

// betterAnomaly reports whether a is preferred over b for the digest pick:
// largest |deviation|, then earliest date, then total scope before key scope,
// then key ascending.
func betterAnomaly(a, b anomalyscan.ScopedFlag) bool {
	aDev := a.Flag.Observed.Sub(a.Flag.Median).Abs()
	bDev := b.Flag.Observed.Sub(b.Flag.Median).Abs()
	if !aDev.Equal(bDev) {
		return aDev.GreaterThan(bDev)
	}
	if !a.Flag.Date.Equal(b.Flag.Date) {
		return a.Flag.Date.Before(b.Flag.Date)
	}
	if a.Scope != b.Scope {
		return a.Scope == "total"
	}
	return a.Key < b.Key
}

func unitCostDrifts(in Input, base Period) []Insight {
	prevStart, prevEnd := precedingWindow(in.WindowStart, in.WindowEnd)
	period := Period{
		Start:         base.Start,
		End:           base.End,
		PreviousStart: prevStart,
		PreviousEnd:   prevEnd,
	}
	var out []Insight
	for _, m := range in.Metrics {
		curCost, curQty, curOK := periodAggregate(m.CurrentCosts, m.CurrentQuantities)
		prevCost, prevQty, prevOK := periodAggregate(m.PreviousCosts, m.PreviousQuantities)
		if !curOK || !prevOK {
			continue
		}
		// Both windows yield a positive quantity (periodAggregate enforces that).
		curUnit := curCost.DivRound(curQty, storage.MaxDecimalScale)
		prevUnit := prevCost.DivRound(prevQty, storage.MaxDecimalScale)
		drift := curUnit.Sub(prevUnit)
		if drift.IsZero() {
			continue
		}
		// magnitude (DERIVED) = |currentCost - previousUnitCost * currentQuantity|
		costOfDrift := curCost.Sub(prevUnit.Mul(curQty)).Abs()

		link := Link{
			View:     "unit-economics",
			Metric:   m.Name,
			Currency: in.Currency,
		}
		if !in.WindowStart.IsZero() {
			link.Start = in.WindowStart.UTC().Format(time.DateOnly)
		}
		if !in.WindowEnd.IsZero() {
			link.End = in.WindowEnd.UTC().Format(time.DateOnly)
		}

		out = append(out, Insight{
			Type:      TypeUnitCostDrift,
			Title:     "Unit cost drift",
			Body:      fmt.Sprintf("Unit cost for metric %s moved from %s to %s (drift %s); the cost of that drift on current quantity is %s.", m.Name, displayUnitCost(prevUnit), displayUnitCost(curUnit), displayUnitCost(drift), displayMoney(costOfDrift)),
			Magnitude: costOfDrift,
			Dimension: "metric",
			Key:       m.Name,
			Evidence: []Evidence{
				{Name: "currentUnitCost", Value: curUnit.String()},
				{Name: "previousUnitCost", Value: prevUnit.String()},
				{Name: "drift", Value: drift.String()},
				{Name: "currentCost", Value: curCost.String()},
				{Name: "previousCost", Value: prevCost.String()},
				{Name: "currentQuantity", Value: curQty.String()},
				{Name: "previousQuantity", Value: prevQty.String()},
				{Name: "costOfDrift", Value: costOfDrift.String()},
			},
			Period: period,
			Link:   link,
		})
	}
	return out
}

// periodAggregate mirrors the unit-economics covered-days period sum: a day
// contributes only when both cost and quantity streams have that day and the
// quantity is strictly positive. Returns ok=false when the covered quantity is
// not strictly positive.
func periodAggregate(costs storage.DailyCosts, quantities []storage.DayQuantity) (cost, quantity decimal.Decimal, ok bool) {
	periodCost, periodQuantity := decimal.Zero, decimal.Zero
	for ci, qi := 0, 0; ci < len(costs.Days) || qi < len(quantities); {
		costPresent := ci < len(costs.Days)
		quantityPresent := qi < len(quantities)
		useCost := costPresent && (!quantityPresent || costs.Days[ci].Date.Before(quantities[qi].Date))
		useQuantity := quantityPresent && (!costPresent || quantities[qi].Date.Before(costs.Days[ci].Date))

		var dayCost, dayQty decimal.Decimal
		switch {
		case useCost:
			dayCost = dayServiceSum(costs.Days[ci])
			ci++
			quantityPresent = false
		case useQuantity:
			dayQty = quantities[qi].Quantity
			qi++
			costPresent = false
		default:
			dayCost = dayServiceSum(costs.Days[ci])
			dayQty = quantities[qi].Quantity
			ci++
			qi++
		}
		if costPresent && quantityPresent && dayQty.IsPositive() {
			periodCost = periodCost.Add(dayCost)
			periodQuantity = periodQuantity.Add(dayQty)
		}
	}
	if !periodQuantity.IsPositive() {
		return decimal.Zero, decimal.Zero, false
	}
	return periodCost, periodQuantity, true
}

func dayServiceSum(day storage.DayCosts) decimal.Decimal {
	total := decimal.Zero
	for _, svc := range day.Services {
		total = total.Add(svc.Cost)
	}
	return total
}

func commitmentRealization(in Input, base Period, baseLink Link) (Insight, bool) {
	if !in.HasCommitment || !in.BilledTotal.IsPositive() || in.EffectiveTotal.Equal(in.BilledTotal) {
		return Insight{}, false
	}
	savings := in.BilledTotal.Sub(in.EffectiveTotal)
	ratio := savings.DivRound(in.BilledTotal, storage.MaxDecimalScale)

	var body string
	if in.EffectiveTotal.LessThan(in.BilledTotal) {
		body = fmt.Sprintf(
			"Billed total %s exceeds effective total %s by %s; commitment discounts and amortization reduced effective cost below billed cost.",
			displayMoney(in.BilledTotal), displayMoney(in.EffectiveTotal), displayMoney(savings),
		)
	} else {
		body = fmt.Sprintf(
			"Effective total %s exceeds billed total %s by %s; amortized effective cost exceeds billed cost this window.",
			displayMoney(in.EffectiveTotal), displayMoney(in.BilledTotal), displayMoney(savings.Neg()),
		)
	}

	return Insight{
		Type:      TypeCommitmentRealization,
		Title:     "Commitment realization",
		Body:      body,
		Magnitude: savings.Abs(),
		Evidence: []Evidence{
			{Name: "billedTotal", Value: in.BilledTotal.String()},
			{Name: "effectiveTotal", Value: in.EffectiveTotal.String()},
			{Name: "savings", Value: savings.String()},
			{Name: "ratio", Value: ratio.String()},
		},
		Period: base,
		Link:   baseLink,
	}, true
}

func sortInsights(out []Insight) {
	sort.SliceStable(out, func(i, j int) bool {
		cmp := out[i].Magnitude.Cmp(out[j].Magnitude)
		if cmp != 0 {
			return cmp > 0 // magnitude descending
		}
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Key < out[j].Key
	})
}
