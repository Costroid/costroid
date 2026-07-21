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
	if !contains(got[0].Body, "25%") {
		t.Fatalf("body missing 25%%: %s", got[0].Body)
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
