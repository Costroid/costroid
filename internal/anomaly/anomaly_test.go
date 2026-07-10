// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package anomaly

import (
	"strconv"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// series builds a date-ascending series starting 2026-06-01, one observation
// per consecutive day, from the given decimal-string values.
func series(values ...string) []Observation {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	obs := make([]Observation, len(values))
	for i, v := range values {
		obs[i] = Observation{Date: base.AddDate(0, 0, i), Value: d(v)}
	}
	return obs
}

// repeat returns n copies of v.
func repeat(v string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// withTail builds a series from base values plus the trailing values.
func withTail(base []string, tail ...string) []Observation {
	return series(append(append([]string{}, base...), tail...)...)
}

// TestMedianEvenCountExactBeatsEngineFloat is the exactness proof: the two-value
// even-count median interpolates EXACTLY, where the embedded engine's float
// median collapses {…001, …003} to …000. Asserted with Equal (String trims the
// trailing zero the padded literal carries). MUTATION M1/M2: a float64 median or
// a plain Div (DivisionPrecision rounding) reddens here.
func TestMedianEvenCountExactBeatsEngineFloat(t *testing.T) {
	got := medianOf([]decimal.Decimal{d("0.100000000000000001"), d("0.100000000000000003")})
	if !got.Equal(decimal.RequireFromString("0.1000000000000000020")) {
		t.Fatalf("medianOf = %s, want 0.100000000000000002 (exact even-count interpolation)", got)
	}
	if got.Equal(d("0.1")) {
		t.Fatalf("median collapsed to 0.1 — the float-interpolation error the decimal path must avoid")
	}
	// String trims the trailing zero (documented); pin the trimmed form.
	if got.String() != "0.100000000000000002" {
		t.Fatalf("median String = %q, want 0.100000000000000002", got.String())
	}
}

// TestFlatNonzeroSeriesFloorGates covers a flat NONZERO baseline (mad = 0, median
// ≠ 0), where the first condition degenerates to deviation > 0 and the relative
// floor alone gates. MUTATION M5: removing the floor flags the sub-floor case and
// reddens this.
func TestFlatNonzeroSeriesFloorGates(t *testing.T) {
	base := repeat("4.6809", 10) // median 4.6809, mad 0, floor 0.46809

	// Sub-floor deviation (0.4 < 0.46809): NOT flagged, despite mad = 0.
	if got := Detect(withTail(base, "5.0809")); len(got) != 0 {
		t.Fatalf("sub-floor deviation flagged: %+v", got)
	}
	// At/above floor, increase (0.5 ≥ 0.46809).
	up := Detect(withTail(base, "5.1809"))
	if len(up) != 1 || up[0].Direction != "increase" || up[0].MAD.String() != "0" || up[0].Deviation.String() != "0.5" || up[0].Median.String() != "4.6809" {
		t.Fatalf("above-floor increase = %+v", up)
	}
	// At/above floor, decrease.
	down := Detect(withTail(base, "4.1809"))
	if len(down) != 1 || down[0].Direction != "decrease" || down[0].Deviation.String() != "0.5" {
		t.Fatalf("above-floor decrease = %+v", down)
	}
}

// TestAllZeroWindow pins the degenerate all-zero baseline (median 0 AND mad 0):
// any nonzero observation is flagged with the right direction, while a zero
// observation is not (strict >).
func TestAllZeroWindow(t *testing.T) {
	zeros := repeat("0", 10)

	up := Detect(withTail(zeros, "5"))
	if len(up) != 1 || up[0].Direction != "increase" || up[0].Median.String() != "0" || up[0].Deviation.String() != "5" {
		t.Fatalf("all-zero baseline + positive = %+v", up)
	}
	down := Detect(withTail(zeros, "-3"))
	if len(down) != 1 || down[0].Direction != "decrease" || down[0].Deviation.String() != "3" {
		t.Fatalf("all-zero baseline + negative = %+v", down)
	}
	if got := Detect(withTail(zeros, "0")); len(got) != 0 {
		t.Fatalf("all-zero baseline + zero must NOT flag (strict >): %+v", got)
	}
}

// TestMinHistoryGate pins the ≥ MinObservations gate. MUTATION M3: removing the
// gate scores the 9-prior day and reddens this.
func TestMinHistoryGate(t *testing.T) {
	nine := repeat("10", 9)
	if got := Detect(withTail(nine, "100")); len(got) != 0 {
		t.Fatalf("scored a day with only 9 prior observations: %+v", got)
	}
	ten := repeat("10", 10)
	got := Detect(withTail(ten, "100"))
	if len(got) != 1 || got[0].Direction != "increase" {
		t.Fatalf("10 prior observations should score: %+v", got)
	}
}

// TestMissingDaysSkippedNotZeroFilled proves a calendar GAP is not zero-filled: a
// series with a hole before the scored day yields the SAME statistics as the
// contiguous equivalent (only the flag DATE differs). A zero-fill would insert a
// 0 into the window and shift the median.
func TestMissingDaysSkippedNotZeroFilled(t *testing.T) {
	values := []string{"8", "12", "8", "12", "8", "12", "8", "12", "8", "12", "40"}
	contig := Detect(series(values...))
	if len(contig) != 1 {
		t.Fatalf("contiguous series = %+v", contig)
	}

	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	gapped := make([]Observation, len(values))
	for i, v := range values {
		day := i
		if i == len(values)-1 {
			day = i + 5 // a five-day calendar hole before the scored day
		}
		gapped[i] = Observation{Date: base.AddDate(0, 0, day), Value: d(v)}
	}
	holed := Detect(gapped)
	if len(holed) != 1 {
		t.Fatalf("gapped series = %+v", holed)
	}
	a, b := contig[0], holed[0]
	if !a.Median.Equal(b.Median) || !a.MAD.Equal(b.MAD) || !a.Deviation.Equal(b.Deviation) ||
		!a.Threshold.Equal(b.Threshold) || a.Direction != b.Direction {
		t.Fatalf("the calendar gap changed the statistics: contiguous=%+v gapped=%+v", a, b)
	}
	if a.Date.Equal(b.Date) {
		t.Fatalf("test setup bug: the two flags must fall on different calendar days")
	}
}

// TestStrictThresholdBoundary pins strict >: a deviation EXACTLY equal to the
// threshold does NOT flag, while an epsilon above DOES. The floor comfortably
// holds (deviation ≫ 0.1×median), so the boundary is gated only by the strict
// comparison — a strict→lax (>=) mutation would flag the on-edge case and redden.
func TestStrictThresholdBoundary(t *testing.T) {
	// median 10, mad 2, scaledMad 2.9652, threshold 8.8956, floor 1.
	window := []string{"8", "8", "8", "8", "8", "12", "12", "12", "12", "12"}

	if got := Detect(withTail(window, "18.8956")); len(got) != 0 { // deviation == 8.8956
		t.Fatalf("deviation exactly at the threshold flagged (strict > violated): %+v", got)
	}
	above := Detect(withTail(window, "18.8957")) // deviation == 8.8957
	if len(above) != 1 || above[0].Threshold.String() != "8.8956" || above[0].Deviation.String() != "8.8957" || above[0].Direction != "increase" {
		t.Fatalf("epsilon-above threshold = %+v", above)
	}
}

// TestTwoSidedSpikeAndDip pins both directions. MUTATION M4: a one-sided detector
// (increases only) leaves the dip unflagged and reddens.
func TestTwoSidedSpikeAndDip(t *testing.T) {
	base := repeat("10", 10)
	spike := Detect(withTail(base, "40"))
	if len(spike) != 1 || spike[0].Direction != "increase" {
		t.Fatalf("spike = %+v", spike)
	}
	dip := Detect(withTail(base, "1"))
	if len(dip) != 1 || dip[0].Direction != "decrease" {
		t.Fatalf("dip = %+v", dip)
	}
}

// TestNegativeAndZeroBaseline proves negatives and zero participate in the
// baseline without error and stay exact (median 2.5, mad 2.5, scaledMad 3.7065,
// threshold 11.1195).
func TestNegativeAndZeroBaseline(t *testing.T) {
	window := []string{"-2", "-1", "0", "1", "2", "3", "4", "5", "6", "7"}
	got := Detect(withTail(window, "20"))
	if len(got) != 1 {
		t.Fatalf("negative/zero baseline = %+v", got)
	}
	f := got[0]
	if f.Median.String() != "2.5" || f.MAD.String() != "2.5" || f.ScaledMAD.String() != "3.7065" ||
		f.Threshold.String() != "11.1195" || f.Deviation.String() != "17.5" || f.Direction != "increase" {
		t.Fatalf("negative/zero baseline flag = %+v", f)
	}
}

// TestWindowCapUsesTrailingThirty proves only the trailing WindowDays are used:
// the oldest observation (0) sits just outside the window and would drop the
// median from 15.5 to 15 if the cap leaked. The increasing 0..30 baseline never
// self-flags (its MAD grows with the spread), so exactly the spike is flagged.
func TestWindowCapUsesTrailingThirty(t *testing.T) {
	values := make([]string, 0, 32)
	for i := 0; i <= 30; i++ {
		values = append(values, strconv.Itoa(i))
	}
	values = append(values, "100")
	got := Detect(series(values...))
	if len(got) != 1 {
		t.Fatalf("window-cap series flagged %d observations, want exactly the spike: %+v", len(got), got)
	}
	if got[0].Median.String() != "15.5" {
		t.Fatalf("median = %s, want 15.5 — the oldest value (0) must be OUTSIDE the trailing-30 window (a leak gives 15)", got[0].Median)
	}
}
