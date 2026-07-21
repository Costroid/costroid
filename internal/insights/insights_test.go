// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package insights

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/anomaly"
	"github.com/Costroid/costroid/internal/anomalyscan"
	"github.com/Costroid/costroid/internal/storage"
)

func d(t *testing.T, s string) decimal.Decimal {
	t.Helper()
	v, err := decimal.NewFromString(s)
	if err != nil {
		t.Fatalf("decimal %q: %v", s, err)
	}
	return v
}

func day(y, m, day int) time.Time {
	return time.Date(y, time.Month(m), day, 0, 0, 0, 0, time.UTC)
}

func TestFixedParametersSingleSource(t *testing.T) {
	p := FixedParameters()
	if p.K != anomaly.K.String() || p.ConsistencyConstant != anomaly.ConsistencyConstant.String() ||
		p.WindowDays != anomaly.WindowDays || p.MinObservations != anomaly.MinObservations ||
		p.RelativeFloor != anomaly.RelativeFloor.String() || p.DivisionScale != storage.MaxDecimalScale {
		t.Fatalf("FixedParameters = %+v, want anomaly package constants and scale 18", p)
	}
}

func TestRankingMagnitudeThenTypeThenKey(t *testing.T) {
	// Equal magnitudes: type asc, then key asc. Different magnitudes: desc.
	in := Input{
		Currency:    "USD",
		WindowStart: day(2026, 5, 10),
		WindowEnd:   day(2026, 5, 20),
		// commitment magnitude 50 (billed 100, effective 50)
		HasCommitment:  true,
		BilledTotal:    d(t, "100"),
		EffectiveTotal: d(t, "50"),
		// untagged magnitude 50 (tie with commitment: commitment-realization < untagged-spend)
		TagSpends: []TagSpend{{
			TagKey: "env", UntaggedTotal: d(t, "50"), WindowTotal: d(t, "100"),
		}},
		// top-mover increase magnitude 30
		PreviousHasDays: true,
		CurrentServices: map[string]decimal.Decimal{
			"EC2": d(t, "40"),
			"S3":  d(t, "5"),
		},
		PreviousServices: map[string]decimal.Decimal{
			"EC2": d(t, "10"),
			"S3":  d(t, "20"),
		},
	}
	got := Compute(in)
	// Magnitudes: commitment 50, untagged 50, top-mover inc 30, top-mover dec 15
	// At magnitude 50: commitment-realization < untagged-spend
	// At magnitude 30/15: both top-mover; key EC2 before S3 but different magnitudes
	if len(got) < 3 {
		t.Fatalf("insights = %d, want >= 3", len(got))
	}
	// Exact order
	wantTypes := []string{TypeCommitmentRealization, TypeUntaggedSpend, TypeTopMover, TypeTopMover}
	wantKeys := []string{"", "env", "EC2", "S3"}
	wantMags := []string{"50", "50", "30", "15"}
	if len(got) != len(wantTypes) {
		t.Fatalf("got %d insights %+v, want %d", len(got), typesOf(got), len(wantTypes))
	}
	for i := range wantTypes {
		if got[i].Type != wantTypes[i] || got[i].Key != wantKeys[i] || got[i].Magnitude.String() != wantMags[i] {
			t.Errorf("rank %d = type=%s key=%q mag=%s, want type=%s key=%q mag=%s",
				i, got[i].Type, got[i].Key, got[i].Magnitude, wantTypes[i], wantKeys[i], wantMags[i])
		}
	}
}

func typesOf(in []Insight) []string {
	out := make([]string, len(in))
	for i, x := range in {
		out[i] = x.Type + ":" + x.Key + ":" + x.Magnitude.String()
	}
	return out
}

func evidenceOf(in Insight) map[string]string {
	m := make(map[string]string, len(in.Evidence))
	for _, e := range in.Evidence {
		m[e.Name] = e.Value
	}
	return m
}

func ofType(in []Insight, typ string) []Insight {
	var out []Insight
	for _, x := range in {
		if x.Type == typ {
			out = append(out, x)
		}
	}
	return out
}

func TestTieBreakTypeThenKey(t *testing.T) {
	// Two top-movers suppressed; two untagged-like via equal commitment impossible.
	// Force two unit-cost-drift with equal costOfDrift and different keys.
	// Simpler: equal magnitude top-movers only one each side. Use two metrics
	// with identical derived magnitude via crafted numbers.
	//
	// metric a: cur cost 30 qty 10 unit 3; prev unit 1 (cost 10 qty 10); drift cost = |30 - 1*10| = 20
	// metric b: same magnitude 20
	costsCur := storage.DailyCosts{Currency: "USD", Days: []storage.DayCosts{{
		Date: day(2026, 5, 15), Services: []storage.ServiceCost{{ServiceName: "x", Cost: d(t, "30")}},
	}}}
	qtyCur := []storage.DayQuantity{{Date: day(2026, 5, 15), Quantity: d(t, "10")}}
	costsPrev := storage.DailyCosts{Currency: "USD", Days: []storage.DayCosts{{
		Date: day(2026, 5, 5), Services: []storage.ServiceCost{{ServiceName: "x", Cost: d(t, "10")}},
	}}}
	qtyPrev := []storage.DayQuantity{{Date: day(2026, 5, 5), Quantity: d(t, "10")}}
	in := Input{
		Currency:         "USD",
		WindowStart:      day(2026, 5, 10),
		WindowEnd:        day(2026, 5, 20),
		PreviousHasDays:  true,
		CurrentServices:  map[string]decimal.Decimal{"z": d(t, "1")},
		PreviousServices: map[string]decimal.Decimal{"z": d(t, "1")},
		Metrics: []MetricSeries{
			{Name: "beta", CurrentCosts: costsCur, CurrentQuantities: qtyCur, PreviousCosts: costsPrev, PreviousQuantities: qtyPrev},
			{Name: "alpha", CurrentCosts: costsCur, CurrentQuantities: qtyCur, PreviousCosts: costsPrev, PreviousQuantities: qtyPrev},
		},
	}
	got := Compute(in)
	// Both unit-cost-drift magnitude 20; type equal so key asc: alpha then beta.
	// top-mover suppressed (zero deltas).
	var drifts []Insight
	for _, g := range got {
		if g.Type == TypeUnitCostDrift {
			drifts = append(drifts, g)
		}
	}
	if len(drifts) != 2 {
		t.Fatalf("unit-cost-drift = %d (%v), want 2", len(drifts), typesOf(got))
	}
	if drifts[0].Key != "alpha" || drifts[1].Key != "beta" {
		t.Fatalf("tie-break keys = %q, %q want alpha, beta", drifts[0].Key, drifts[1].Key)
	}
	if !drifts[0].Magnitude.Equal(drifts[1].Magnitude) {
		t.Fatalf("magnitudes not equal: %s vs %s", drifts[0].Magnitude, drifts[1].Magnitude)
	}
}

func TestTopMoverSuppressedWhenPreviousEmpty(t *testing.T) {
	in := Input{
		Currency:         "USD",
		WindowStart:      day(2026, 5, 10),
		WindowEnd:        day(2026, 5, 20),
		PreviousHasDays:  false, // anti-vacuity: current has deltas that would produce movers
		CurrentServices:  map[string]decimal.Decimal{"EC2": d(t, "100")},
		PreviousServices: map[string]decimal.Decimal{},
	}
	got := Compute(in)
	for _, g := range got {
		if g.Type == TypeTopMover {
			t.Fatalf("top-mover present despite empty previous window: %+v", g)
		}
	}
}

func TestTopMoverAndUnitCostAbsentWithoutBothBounds(t *testing.T) {
	costs := storage.DailyCosts{Currency: "USD", Days: []storage.DayCosts{{
		Date: day(2026, 5, 15), Services: []storage.ServiceCost{{ServiceName: "x", Cost: d(t, "30")}},
	}}}
	qty := []storage.DayQuantity{{Date: day(2026, 5, 15), Quantity: d(t, "10")}}
	in := Input{
		Currency:         "USD",
		WindowStart:      day(2026, 5, 10), // end missing
		PreviousHasDays:  true,
		CurrentServices:  map[string]decimal.Decimal{"EC2": d(t, "100")},
		PreviousServices: map[string]decimal.Decimal{"EC2": d(t, "10")},
		Metrics: []MetricSeries{{
			Name: "requests", CurrentCosts: costs, CurrentQuantities: qty,
			PreviousCosts: costs, PreviousQuantities: qty,
		}},
	}
	got := Compute(in)
	for _, g := range got {
		if g.Type == TypeTopMover || g.Type == TypeUnitCostDrift {
			t.Fatalf("%s present without both start and end", g.Type)
		}
	}
}

func TestCommitmentDirections(t *testing.T) {
	below := Compute(Input{
		Currency: "USD", HasCommitment: true,
		BilledTotal: d(t, "100"), EffectiveTotal: d(t, "80"),
	})
	if len(below) != 1 || below[0].Type != TypeCommitmentRealization {
		t.Fatalf("below = %+v", below)
	}
	if below[0].Magnitude.String() != "20" || below[0].Evidence[2].Value != "20" {
		t.Fatalf("below savings = %+v", below[0])
	}
	if !contains(below[0].Body, "commitment discounts and amortization reduced effective cost below billed cost") {
		t.Fatalf("below body missing distinctive phrase: %s", below[0].Body)
	}

	above := Compute(Input{
		Currency: "USD", HasCommitment: true,
		BilledTotal: d(t, "100"), EffectiveTotal: d(t, "120"),
	})
	if len(above) != 1 {
		t.Fatalf("above = %+v", above)
	}
	if !contains(above[0].Body, "amortized effective cost exceeds billed cost this window") {
		t.Fatalf("above body missing distinctive phrase: %s", above[0].Body)
	}
	// savings = 100 - 120 = -20; magnitude abs = 20
	if above[0].Magnitude.String() != "20" || above[0].Evidence[2].Value != "-20" {
		t.Fatalf("above savings = %+v", above[0])
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(len(s) > 0 && (func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		})()))
}

func TestAnomalyDigestPicksLargestDeviation(t *testing.T) {
	in := Input{
		Currency: "USD",
		Flags: []anomalyscan.ScopedFlag{
			{Flag: anomaly.Flag{
				Date: day(2026, 6, 12), Direction: "increase",
				Observed: d(t, "100"), Median: d(t, "10"), Threshold: d(t, "1"), Deviation: d(t, "90"),
			}, Scope: "key", Key: "S3"},
			{Flag: anomaly.Flag{
				Date: day(2026, 6, 11), Direction: "increase",
				Observed: d(t, "150"), Median: d(t, "15"), Threshold: d(t, "1"), Deviation: d(t, "135"),
			}, Scope: "total"},
		},
	}
	got := Compute(in)
	if len(got) != 1 || got[0].Type != TypeAnomalyDigest {
		t.Fatalf("got %+v", got)
	}
	// Largest |obs-med| is 135 on total scope day 11.
	if got[0].Magnitude.String() != "135" {
		t.Fatalf("magnitude = %s, want 135", got[0].Magnitude)
	}
	if got[0].Key != "" || got[0].Dimension != "" {
		t.Fatalf("total-scope must omit key/dimension: key=%q dim=%q", got[0].Key, got[0].Dimension)
	}
	if got[0].Evidence[0].Value != "2" { // flagCount
		t.Fatalf("flagCount = %v", got[0].Evidence[0])
	}
}

func TestEmptyInputYieldsEmpty(t *testing.T) {
	got := Compute(Input{})
	if got == nil {
		t.Fatal("nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestUntaggedShareDerived(t *testing.T) {
	// untagged 25, window 100 => share 0.25, pct "25%"
	in := Input{
		Currency: "USD",
		TagSpends: []TagSpend{
			{TagKey: "team", UntaggedTotal: d(t, "10"), WindowTotal: d(t, "100")},
			{TagKey: "env", UntaggedTotal: d(t, "25"), WindowTotal: d(t, "100")},
		},
	}
	got := Compute(in)
	if len(got) != 1 || got[0].Key != "env" {
		t.Fatalf("got %+v, want env", got)
	}
	// share = 25/100 = 0.25 exactly at scale 18
	wantShare := d(t, "25").DivRound(d(t, "100"), storage.MaxDecimalScale).String()
	if got[0].Evidence[2].Name != "share" || got[0].Evidence[2].Value != wantShare {
		t.Fatalf("share = %+v, want %s", got[0].Evidence[2], wantShare)
	}
	// The body states the percentage as share * 100 and formats every amount for
	// reading, while the evidence above keeps the exact share. Pinned as a whole
	// string: a body carrying the un-multiplied share would read "(0.3% of the
	// window)", and an unformatted body would read "total 100" rather than
	// "total 100.00".
	wantBody := "Of the window total 100.00, 25.00 is untagged for tag key env (25% of the window)."
	if got[0].Body != wantBody {
		t.Fatalf("body = %q, want %q", got[0].Body, wantBody)
	}
}

func TestBodyRoundsWhileEvidenceStaysExact(t *testing.T) {
	// The split the digest depends on: the sentence is readable, the evidence
	// printed beneath it is the untouched audit trail. Both halves are pinned on
	// the SAME insight so a change that formatted the evidence too would fail.
	windowTotal := "964050.632653589793238462"
	// Carries more than two decimals on purpose: an amount that already ends at
	// two places would round to itself, leaving the magnitude assertion below
	// unable to see display rounding leak into the wire value.
	unallocatedTotal := "194348.364999999999999999"
	in := Input{
		Currency:              "USD",
		AllocationReady:       true,
		UnallocatedTotal:      d(t, unallocatedTotal),
		AllocationWindowTotal: d(t, windowTotal),
	}
	got := Compute(in)
	if len(got) != 1 || got[0].Type != TypeUnallocatedSpend {
		t.Fatalf("got %v, want one unallocated-spend", typesOf(got))
	}

	wantBody := "Of the window total 964,050.63, 194,348.36 is unallocated (20.2% of the window)."
	if got[0].Body != wantBody {
		t.Fatalf("body = %q, want %q", got[0].Body, wantBody)
	}

	// The evidence keeps every digit, including the 18-decimal window total and
	// the derived share the body rounded to "20.2%".
	wantShare := d(t, unallocatedTotal).DivRound(d(t, windowTotal), storage.MaxDecimalScale).String()
	if wantShare != "0.201595599252964521" {
		t.Fatalf("fixture drifted: share = %s", wantShare)
	}
	ev := evidenceOf(got[0])
	if ev["windowTotal"] != windowTotal {
		t.Errorf("windowTotal evidence = %q, want the full-precision %q", ev["windowTotal"], windowTotal)
	}
	if ev["unallocatedTotal"] != unallocatedTotal {
		t.Errorf("unallocatedTotal evidence = %q, want the full-precision %q", ev["unallocatedTotal"], unallocatedTotal)
	}
	if ev["share"] != wantShare {
		t.Errorf("share evidence = %q, want the full-precision %q", ev["share"], wantShare)
	}
	// The magnitude is a wire value too: it must not pick up display rounding.
	if got[0].Magnitude.String() != unallocatedTotal {
		t.Errorf("magnitude = %s, want the exact %s", got[0].Magnitude, unallocatedTotal)
	}
}

func TestDisplayMoneyExactAtEighteenDecimals(t *testing.T) {
	// Values carrying the full division scale must round exactly and group by
	// digit, with no float anywhere on the path: a float64 round-trip of the
	// first case below cannot represent the operand at all.
	cases := []struct{ in, want string }{
		{"964050.632653589793238462", "964,050.63"},
		{"194348.36", "194,348.36"},
		{"0.201595594066514939", "0.20"},
		{"0.005000000000000001", "0.01"}, // rounds up on the 18th decimal
		{"-1234567.895000000000000001", "-1,234,567.90"},
		{"1000", "1,000.00"},
		{"999", "999.00"},
		{"1234567890.129999999999999999", "1,234,567,890.13"},
		{"0", "0.00"},
		{"-0.001", "0.00"}, // rounds to zero without keeping a negative sign
	}
	for _, tc := range cases {
		if got := displayMoney(d(t, tc.in)); got != tc.want {
			t.Errorf("displayMoney(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDisplayUnitCostKeepsSignificantDigits(t *testing.T) {
	// Per-unit rates sit far below one currency unit. Two decimals would render
	// the first two cases below as an identical "0.04" and the third as "0.00",
	// producing a sentence that reports a zero movement next to the non-zero
	// cost of that movement.
	cases := []struct{ in, want string }{
		{"0.040762276863408085", "0.0408"},
		{"0.038743231197771588", "0.0387"},
		{"-0.002019045665636497", "-0.00202"},
		{"0.04908784029229762", "0.0491"},
		{"-0.0081514762039273", "-0.00815"},
		{"0.201595594066514939", "0.202"},
		// At or above one unit the money floor of two places applies.
		{"1.005", "1.01"},
		{"8698.04872756203018", "8,698.05"},
		{"0", "0.00"},
	}
	for _, tc := range cases {
		if got := displayUnitCost(d(t, tc.in)); got != tc.want {
			t.Errorf("displayUnitCost(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDisplayPercentExactAtEighteenDecimals(t *testing.T) {
	cases := []struct{ in, want string }{
		{"0.201595594066514939", "20.2%"},
		{"0.25", "25%"}, // a whole percentage drops the trailing ".0"
		{"0", "0%"},
		{"1", "100%"},
		{"0.200020382749938528", "20%"}, // rounds to a whole percentage
		{"0.203148883062654743", "20.3%"},
		{"0.000499999999999999", "0%"},
		{"-0.0815147620392730", "-8.2%"},
	}
	for _, tc := range cases {
		if got := displayPercent(d(t, tc.in)); got != tc.want {
			t.Errorf("displayPercent(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestUnallocatedSuppressedWhenNotReady(t *testing.T) {
	in := Input{
		Currency:              "USD",
		AllocationReady:       false,
		UnallocatedTotal:      d(t, "50"), // would produce if ready
		AllocationWindowTotal: d(t, "100"),
	}
	got := Compute(in)
	for _, g := range got {
		if g.Type == TypeUnallocatedSpend {
			t.Fatal("unallocated present when not ready")
		}
	}
}

func TestUnallocatedSuppressedWhenZero(t *testing.T) {
	base := Input{
		Currency:              "USD",
		AllocationReady:       true,
		AllocationWindowTotal: d(t, "100"),
	}
	zero := base
	zero.UnallocatedTotal = d(t, "0")
	for _, g := range Compute(zero) {
		if g.Type == TypeUnallocatedSpend {
			t.Fatalf("unallocated present with a zero unallocated total: %+v", g)
		}
	}

	// Anti-vacuity: the same input with a non-zero unallocated total emits.
	nonZero := base
	nonZero.UnallocatedTotal = d(t, "30")
	got := Compute(nonZero)
	if len(got) != 1 || got[0].Type != TypeUnallocatedSpend || got[0].Magnitude.String() != "30" {
		t.Fatalf("anti-vacuity: a non-zero unallocated total must emit, got %v", typesOf(got))
	}
}

func TestTopMoverSuppressedPerSideWithoutStrictDelta(t *testing.T) {
	// One service whose delta is exactly zero: neither a strictly positive nor a
	// strictly negative delta exists, so BOTH sides are suppressed.
	in := Input{
		Currency:         "USD",
		WindowStart:      day(2026, 5, 10),
		WindowEnd:        day(2026, 5, 20),
		PreviousHasDays:  true,
		CurrentServices:  map[string]decimal.Decimal{"EC2": d(t, "10")},
		PreviousServices: map[string]decimal.Decimal{"EC2": d(t, "10")},
	}
	got := Compute(in)
	for _, g := range got {
		if g.Type == TypeTopMover {
			t.Fatalf("top-mover emitted for a zero delta: %+v", g)
		}
	}
	if len(got) != 0 {
		t.Fatalf("insights = %v, want none", typesOf(got))
	}

	// Anti-vacuity: raising the current total by 1 emits exactly the increase
	// side (10 -> 11, delta 1) and still no decrease side.
	in.CurrentServices = map[string]decimal.Decimal{"EC2": d(t, "11")}
	got = Compute(in)
	movers := ofType(got, TypeTopMover)
	if len(movers) != 1 || movers[0].Magnitude.String() != "1" {
		t.Fatalf("anti-vacuity: want one top-mover of magnitude 1, got %v", typesOf(got))
	}
	if evidenceOf(movers[0])["delta"] != "1" {
		t.Fatalf("anti-vacuity delta = %v, want 1", evidenceOf(movers[0]))
	}
}

func TestTopMoverNewServiceInNonEmptyPreviousWindow(t *testing.T) {
	// Lambda is absent from an otherwise NON-empty previous window, so it is new:
	// no previousTotal in evidence and delta equals the total (7 - 0 = 7).
	// EC2's delta is zero, which keeps Lambda the only mover.
	in := Input{
		Currency:         "USD",
		WindowStart:      day(2026, 5, 10),
		WindowEnd:        day(2026, 5, 20),
		PreviousHasDays:  true,
		CurrentServices:  map[string]decimal.Decimal{"EC2": d(t, "10"), "Lambda": d(t, "7")},
		PreviousServices: map[string]decimal.Decimal{"EC2": d(t, "10")},
	}
	got := Compute(in)
	movers := ofType(got, TypeTopMover)
	if len(movers) != 1 {
		t.Fatalf("top-movers = %v, want exactly the new service", typesOf(got))
	}
	m := movers[0]
	if m.Key != "Lambda" || m.Magnitude.String() != "7" {
		t.Fatalf("mover = key %q magnitude %s, want Lambda/7", m.Key, m.Magnitude)
	}
	// Evidence carries total and delta only; a previousTotal pair would mean the
	// absent service was treated as a known zero rather than as new.
	want := []Evidence{{Name: "total", Value: "7"}, {Name: "delta", Value: "7"}}
	if len(m.Evidence) != len(want) {
		t.Fatalf("evidence = %+v, want %+v", m.Evidence, want)
	}
	for i := range want {
		if m.Evidence[i] != want[i] {
			t.Fatalf("evidence[%d] = %+v, want %+v", i, m.Evidence[i], want[i])
		}
	}
	if !contains(m.Body, "is new in the current window") {
		t.Fatalf("body must read as new: %s", m.Body)
	}
}

// driftSeries builds one metric's current and previous cost and quantity streams
// from single-day values, mirroring the unit-economics covered-days inputs.
func driftSeries(t *testing.T, name, curCost, curQty, prevCost, prevQty string) MetricSeries {
	t.Helper()
	curDay, prevDay := day(2026, 5, 15), day(2026, 5, 5)
	return MetricSeries{
		Name: name,
		CurrentCosts: storage.DailyCosts{Currency: "USD", Days: []storage.DayCosts{{
			Date: curDay, Services: []storage.ServiceCost{{ServiceName: "x", Cost: d(t, curCost)}},
		}}},
		CurrentQuantities: []storage.DayQuantity{{Date: curDay, Quantity: d(t, curQty)}},
		PreviousCosts: storage.DailyCosts{Currency: "USD", Days: []storage.DayCosts{{
			Date: prevDay, Services: []storage.ServiceCost{{ServiceName: "x", Cost: d(t, prevCost)}},
		}}},
		PreviousQuantities: []storage.DayQuantity{{Date: prevDay, Quantity: d(t, prevQty)}},
	}
}

// driftInput wraps metric series in a bounded window with no other observations.
func driftInput(metrics ...MetricSeries) Input {
	return Input{
		Currency:    "USD",
		WindowStart: day(2026, 5, 10),
		WindowEnd:   day(2026, 5, 20),
		Metrics:     metrics,
	}
}

func TestUnitCostDriftSuppressedWhenDriftZero(t *testing.T) {
	// steady: current 30/10 = 3, previous 15/5 = 3, drift 3 - 3 = 0 -> suppressed.
	// moving: current 30/10 = 3, previous 10/10 = 1, drift 3 - 1 = 2 -> emitted,
	// costOfDrift = |30 - 1 * 10| = 20 (the anti-vacuity guard: the same shape
	// with a non-zero drift does reach the output).
	got := Compute(driftInput(
		driftSeries(t, "steady", "30", "10", "15", "5"),
		driftSeries(t, "moving", "30", "10", "10", "10"),
	))
	drifts := ofType(got, TypeUnitCostDrift)
	if len(drifts) != 1 {
		t.Fatalf("unit-cost-drift = %v, want only the moving metric", typesOf(got))
	}
	if drifts[0].Key != "moving" || drifts[0].Magnitude.String() != "20" {
		t.Fatalf("drift = key %q magnitude %s, want moving/20", drifts[0].Key, drifts[0].Magnitude)
	}
	if ev := evidenceOf(drifts[0]); ev["drift"] != "2" || ev["costOfDrift"] != "20" {
		t.Fatalf("evidence = %v, want drift 2 and costOfDrift 20", ev)
	}
}

func TestUnitCostDriftSkippedUnlessBothWindowsHavePositiveQuantity(t *testing.T) {
	// A metric is skipped unless BOTH windows yield a strictly positive quantity.
	// That gate is also what keeps the unit-cost division away from a zero
	// divisor, so a skipped metric must never reach DivRound.
	got := Compute(driftInput(
		driftSeries(t, "previousQuantityZero", "30", "10", "10", "0"),
		driftSeries(t, "currentQuantityZero", "30", "0", "10", "10"),
		driftSeries(t, "moving", "30", "10", "10", "10"),
	))
	drifts := ofType(got, TypeUnitCostDrift)
	if len(drifts) != 1 {
		t.Fatalf("unit-cost-drift = %v, want only the metric with two positive quantities", typesOf(got))
	}
	// Anti-vacuity: the surviving metric is the fully-covered one, magnitude
	// |30 - 1 * 10| = 20.
	if drifts[0].Key != "moving" || drifts[0].Magnitude.String() != "20" {
		t.Fatalf("drift = key %q magnitude %s, want moving/20", drifts[0].Key, drifts[0].Magnitude)
	}
}

func TestCommitmentSuppressedWhenBilledNotPositive(t *testing.T) {
	cases := []struct {
		name      string
		billed    string
		effective string
	}{
		// An all-credit window: nothing was billed and credits pushed effective
		// cost below zero. The ratio would have no divisor.
		{name: "zero billed total", billed: "0", effective: "-50"},
		{name: "negative billed total", billed: "-10", effective: "-40"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Compute(Input{
				Currency: "USD", HasCommitment: true,
				BilledTotal: d(t, tc.billed), EffectiveTotal: d(t, tc.effective),
			})
			for _, g := range got {
				if g.Type == TypeCommitmentRealization {
					t.Fatalf("commitment emitted with billed total %s: %+v", tc.billed, g)
				}
			}
		})
	}

	// Anti-vacuity: the same shape with a positive billed total does emit.
	// savings = 10 - (-40) = 50; ratio = 50 / 10 = 5.
	got := Compute(Input{
		Currency: "USD", HasCommitment: true,
		BilledTotal: d(t, "10"), EffectiveTotal: d(t, "-40"),
	})
	if len(got) != 1 || got[0].Type != TypeCommitmentRealization || got[0].Magnitude.String() != "50" {
		t.Fatalf("anti-vacuity: a positive billed total must emit magnitude 50, got %v", typesOf(got))
	}
	if ev := evidenceOf(got[0]); ev["ratio"] != "5" {
		t.Fatalf("anti-vacuity ratio = %v, want 5", ev)
	}
}

func TestAnomalyDigestTieBreakEarliestDate(t *testing.T) {
	// Both flags deviate by |100 - 10| = 90 on the same scope. The earliest date
	// wins, even though the later flag's key sorts first alphabetically.
	in := Input{
		Currency: "USD",
		Flags: []anomalyscan.ScopedFlag{
			{Flag: anomaly.Flag{
				Date: day(2026, 6, 12), Direction: "increase",
				Observed: d(t, "100"), Median: d(t, "10"), Threshold: d(t, "1"), Deviation: d(t, "90"),
			}, Scope: "key", Key: "A"},
			{Flag: anomaly.Flag{
				Date: day(2026, 6, 11), Direction: "increase",
				Observed: d(t, "100"), Median: d(t, "10"), Threshold: d(t, "1"), Deviation: d(t, "90"),
			}, Scope: "key", Key: "B"},
		},
	}
	got := Compute(in)
	if len(got) != 1 || got[0].Type != TypeAnomalyDigest {
		t.Fatalf("got %v, want one anomaly digest", typesOf(got))
	}
	if got[0].Key != "B" {
		t.Fatalf("digest key = %q, want B (the earlier date)", got[0].Key)
	}
	if ev := evidenceOf(got[0]); ev["date"] != "2026-06-11" {
		t.Fatalf("digest date = %v, want 2026-06-11", ev)
	}
}

func TestAnomalyDigestTieBreakTotalScopeBeforeKeyScope(t *testing.T) {
	// Both flags deviate by 90 on the same date, so scope decides. The scan never
	// sets a key on a total-scope flag, which makes the scope rule and the
	// key-ascending fallback that follows it indistinguishable on scan output;
	// the key below is set purely to separate the two rules.
	in := Input{
		Currency: "USD",
		Flags: []anomalyscan.ScopedFlag{
			{Flag: anomaly.Flag{
				Date: day(2026, 6, 11), Direction: "increase",
				Observed: d(t, "100"), Median: d(t, "10"), Threshold: d(t, "1"), Deviation: d(t, "90"),
			}, Scope: "key", Key: "A"},
			{Flag: anomaly.Flag{
				Date: day(2026, 6, 11), Direction: "increase",
				Observed: d(t, "100"), Median: d(t, "10"), Threshold: d(t, "1"), Deviation: d(t, "90"),
			}, Scope: "total", Key: "Z"},
		},
	}
	got := Compute(in)
	if len(got) != 1 || got[0].Type != TypeAnomalyDigest {
		t.Fatalf("got %v, want one anomaly digest", typesOf(got))
	}
	if got[0].Key != "" || got[0].Dimension != "" {
		t.Fatalf("total scope must win the tie and omit key/dimension: key=%q dim=%q", got[0].Key, got[0].Dimension)
	}
	if _, has := evidenceOf(got[0])["key"]; has {
		t.Fatalf("total-scope evidence must omit key: %+v", got[0].Evidence)
	}
}

func TestUntaggedTieBreakTagKeyAscending(t *testing.T) {
	// Equal untagged totals: the alphabetically first tag key wins, even though
	// the other one comes first in the input.
	in := Input{
		Currency: "USD",
		TagSpends: []TagSpend{
			{TagKey: "team", UntaggedTotal: d(t, "25"), WindowTotal: d(t, "100")},
			{TagKey: "env", UntaggedTotal: d(t, "25"), WindowTotal: d(t, "100")},
		},
	}
	got := Compute(in)
	if len(got) != 1 || got[0].Type != TypeUntaggedSpend {
		t.Fatalf("got %v, want one untagged-spend", typesOf(got))
	}
	if got[0].Key != "env" {
		t.Fatalf("tie-break key = %q, want env", got[0].Key)
	}
}

func TestTieBreakTypeAscendingBeforeKey(t *testing.T) {
	// Equal magnitudes whose KEY order contradicts their TYPE order:
	// "unallocated-spend" < "untagged-spend" while key "A" < "Unallocated".
	// Type therefore has to decide first.
	in := Input{
		Currency:              "USD",
		TagSpends:             []TagSpend{{TagKey: "A", UntaggedTotal: d(t, "40"), WindowTotal: d(t, "100")}},
		AllocationReady:       true,
		UnallocatedTotal:      d(t, "40"),
		AllocationWindowTotal: d(t, "100"),
	}
	got := Compute(in)
	untagged, unallocated := ofType(got, TypeUntaggedSpend), ofType(got, TypeUnallocatedSpend)
	if len(got) != 2 || len(untagged) != 1 || len(unallocated) != 1 {
		t.Fatalf("got %v, want one insight of each type", typesOf(got))
	}
	// Anti-vacuity, both checks independent of the resulting order: the two
	// magnitudes really are tied, and the key order really does contradict the
	// expected type order.
	if !untagged[0].Magnitude.Equal(unallocated[0].Magnitude) {
		t.Fatalf("fixture no longer ties: %v", typesOf(got))
	}
	if untagged[0].Key >= unallocated[0].Key {
		t.Fatalf("fixture keys no longer contradict the type order: %v", typesOf(got))
	}
	if got[0].Type != TypeUnallocatedSpend || got[1].Type != TypeUntaggedSpend {
		t.Fatalf("tie order = %v, want unallocated-spend before untagged-spend", typesOf(got))
	}
}

func TestShareZeroDivisorGuards(t *testing.T) {
	// A zero window total against a non-zero untagged (resp. unallocated) total is
	// reachable when credits cancel the other buckets exactly. The share is then
	// reported as zero rather than divided.
	untagged := Compute(Input{
		Currency:  "USD",
		TagSpends: []TagSpend{{TagKey: "env", UntaggedTotal: d(t, "25"), WindowTotal: d(t, "0")}},
	})
	if len(untagged) != 1 || untagged[0].Type != TypeUntaggedSpend {
		t.Fatalf("got %v, want one untagged-spend", typesOf(untagged))
	}
	if untagged[0].Magnitude.String() != "25" {
		t.Fatalf("untagged magnitude = %s, want 25", untagged[0].Magnitude)
	}
	if ev := evidenceOf(untagged[0]); ev["share"] != "0" || ev["windowTotal"] != "0" {
		t.Fatalf("untagged evidence = %v, want share 0 against window total 0", ev)
	}
	wantUntaggedBody := "Of the window total 0.00, 25.00 is untagged for tag key env (0% of the window)."
	if untagged[0].Body != wantUntaggedBody {
		t.Fatalf("untagged body = %q, want %q", untagged[0].Body, wantUntaggedBody)
	}

	unallocated := Compute(Input{
		Currency:              "USD",
		AllocationReady:       true,
		UnallocatedTotal:      d(t, "30"),
		AllocationWindowTotal: d(t, "0"),
	})
	if len(unallocated) != 1 || unallocated[0].Type != TypeUnallocatedSpend {
		t.Fatalf("got %v, want one unallocated-spend", typesOf(unallocated))
	}
	if unallocated[0].Magnitude.String() != "30" {
		t.Fatalf("unallocated magnitude = %s, want 30", unallocated[0].Magnitude)
	}
	if ev := evidenceOf(unallocated[0]); ev["share"] != "0" || ev["windowTotal"] != "0" {
		t.Fatalf("unallocated evidence = %v, want share 0 against window total 0", ev)
	}
	wantUnallocatedBody := "Of the window total 0.00, 30.00 is unallocated (0% of the window)."
	if unallocated[0].Body != wantUnallocatedBody {
		t.Fatalf("unallocated body = %q, want %q", unallocated[0].Body, wantUnallocatedBody)
	}
}
