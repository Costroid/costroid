// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package focuscsv

import (
	"slices"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/focus"
)

// TestColumnCountsPinned makes columns.go's "Pinned counts" comment real: it
// asserts the known-column and Mandatory-presence set sizes the comment
// promises. Regenerating a table from a different spec tag (adding/removing a
// column) reddens this.
func TestColumnCountsPinned(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{"knownColumns10", len(knownColumns10), 43},
		{"knownColumns11", len(knownColumns11), 50},
		{"knownColumns12", len(knownColumns12), 57},
		{"knownColumns13", len(knownColumns13), 65},
		{"knownColumns14", len(knownColumns14), 65},
		{"mandatory12", len(mandatory12), 21},
		{"mandatory13", len(mandatory13), 23},
		{"notNull14", len(notNull14), 15},
		{"mandatoryNullable14", len(mandatoryNullable14), 6},
		// The RESOLVED 1.0/1.1 Mandatory-presence sets are the 21-column set.
		{"mandatoryFor(1.0)", len(mandatoryFor(focus.V1_0)), 21},
		{"mandatoryFor(1.1)", len(mandatoryFor(focus.V1_1)), 21},
	} {
		if tc.got != tc.want {
			t.Errorf("len(%s) = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

// TestKnownColumns10And11Membership pins the 1.0/1.1 tables to their spec-derived
// decomposition (verified against FOCUS spec tags v1.0/v1.1): knownColumns10 is
// EXACTLY knownColumns12 minus 14 named columns, and knownColumns11 is EXACTLY
// knownColumns10 plus 7 named columns. An accidental edit to any of the three
// tables reddens this — a count check alone could not.
func TestKnownColumns10And11Membership(t *testing.T) {
	sortedClone := func(cols []string) []string { c := slices.Clone(cols); slices.Sort(c); return c }
	removed14 := []string{
		"BillingAccountType", "CapacityReservationId", "CapacityReservationStatus",
		"CommitmentDiscountQuantity", "CommitmentDiscountUnit", "InvoiceId", "PricingCurrency",
		"PricingCurrencyContractedUnitPrice", "PricingCurrencyEffectiveCost",
		"PricingCurrencyListUnitPrice", "ServiceSubcategory", "SkuMeter", "SkuPriceDetails",
		"SubAccountType",
	}
	added7 := []string{
		"CapacityReservationId", "CapacityReservationStatus", "CommitmentDiscountQuantity",
		"CommitmentDiscountUnit", "ServiceSubcategory", "SkuMeter", "SkuPriceDetails",
	}

	// Each removed/added name must actually exist where claimed (guards a stale list).
	for _, c := range removed14 {
		if !slices.Contains(knownColumns12, c) {
			t.Errorf("removed name %q is not in knownColumns12", c)
		}
	}
	for _, c := range added7 {
		if !slices.Contains(knownColumns11, c) {
			t.Errorf("added name %q is not in knownColumns11", c)
		}
	}

	// knownColumns10 == knownColumns12 \ removed14.
	removedSet := make(map[string]struct{}, len(removed14))
	for _, c := range removed14 {
		removedSet[c] = struct{}{}
	}
	var want10 []string
	for _, c := range knownColumns12 {
		if _, gone := removedSet[c]; !gone {
			want10 = append(want10, c)
		}
	}
	if !slices.Equal(sortedClone(knownColumns10), sortedClone(want10)) {
		t.Errorf("knownColumns10 != knownColumns12 \\ the 14 removed names\n got=%v\nwant=%v",
			sortedClone(knownColumns10), sortedClone(want10))
	}

	// knownColumns11 == knownColumns10 ∪ added7.
	want11 := append(slices.Clone(knownColumns10), added7...)
	if !slices.Equal(sortedClone(knownColumns11), sortedClone(want11)) {
		t.Errorf("knownColumns11 != knownColumns10 ∪ the 7 added names\n got=%v\nwant=%v",
			sortedClone(knownColumns11), sortedClone(want11))
	}
}

// TestVersionRoutingSetEquality is the additive-only guard: every accepted
// version must resolve to its EXACT known-column and Mandatory-presence set — set
// EQUALITY, not sizes. A count check is insufficient (knownColumns13 and
// knownColumns14 are both 65, differing by 2 names; several Mandatory sizes
// collide), so a switch mis-routing V1_3 → knownColumns14 would pass a size check
// yet fail this. Asserting 1.2/1.3/1.4 route to their pre-slice tables proves the
// 1.0/1.1 additions did not disturb the existing routing. 1.0r2 is included via
// its canonicalization to 1.0.
func TestVersionRoutingSetEquality(t *testing.T) {
	sortedClone := func(cols []string) []string { c := slices.Clone(cols); slices.Sort(c); return c }
	setSorted := func(set map[string]struct{}) []string {
		out := make([]string, 0, len(set))
		for c := range set {
			out = append(out, c)
		}
		slices.Sort(out)
		return out
	}

	for _, tc := range []struct {
		v         focus.Version
		wantKnown []string
	}{
		{focus.V1_0, knownColumns10},
		{focus.Version("1.0r2"), knownColumns10}, // canonicalizes to 1.0
		{focus.V1_1, knownColumns11},
		{focus.V1_2, knownColumns12},
		{focus.V1_3, knownColumns13},
		{focus.V1_4, knownColumns14},
	} {
		got := setSorted(knownColumnsFor(canonicalVersion(tc.v)))
		if !slices.Equal(got, sortedClone(tc.wantKnown)) {
			t.Errorf("knownColumnsFor(canonical %s) set mismatch:\n got=%v\nwant=%v", tc.v, got, sortedClone(tc.wantKnown))
		}
	}

	// mandatoryFor is called for 1.0/1.1/1.2/1.3 (1.4 uses notNull14 directly).
	for _, tc := range []struct {
		v        focus.Version
		wantMand []string
	}{
		{focus.V1_0, mandatory12},
		{focus.Version("1.0r2"), mandatory12},
		{focus.V1_1, mandatory12},
		{focus.V1_2, mandatory12},
		{focus.V1_3, mandatory13},
	} {
		got := sortedClone(mandatoryFor(canonicalVersion(tc.v)))
		if !slices.Equal(got, sortedClone(tc.wantMand)) {
			t.Errorf("mandatoryFor(canonical %s) set mismatch:\n got=%v\nwant=%v", tc.v, got, sortedClone(tc.wantMand))
		}
	}
}

// TestFourteenMandatoryPartition proves the 1.4 Mandatory-presence set (the
// "21" in the columns.go comment) partitions cleanly into the 15 not-null and
// the 6 Mandatory-but-nullable columns: the two are disjoint and their union
// has exactly 21 distinct columns.
func TestFourteenMandatoryPartition(t *testing.T) {
	union := map[string]bool{}
	for _, c := range notNull14 {
		union[c] = true
	}
	for _, c := range mandatoryNullable14 {
		if union[c] {
			t.Errorf("column %q is in BOTH notNull14 and mandatoryNullable14 (must be disjoint)", c)
		}
		union[c] = true
	}
	if len(union) != 21 {
		t.Errorf("notNull14 ∪ mandatoryNullable14 has %d columns, want 21 (the 1.4 Mandatory-presence set)", len(union))
	}
}

// TestNotNull14MatchesValidator makes the columns.go claim that notNull14 is
// "the exact set the pipeline's validation enforces row by row (validate.go)"
// a real assertion rather than a comment: it derives the required-non-null set
// straight from focus.DefaultRules() (the not-null rules describe themselves as
// "<col> MUST NOT be null.") and requires it to equal notNull14 exactly. Adding
// or dropping a not-null validation rule without updating notNull14 reddens
// this — so the GEN-3 "15 not-null columns" gate can never silently drift from
// what the validator actually enforces.
func TestNotNull14MatchesValidator(t *testing.T) {
	var fromValidator []string
	for _, r := range focus.DefaultRules() {
		if col, ok := strings.CutSuffix(r.Description, " MUST NOT be null."); ok {
			fromValidator = append(fromValidator, col)
		}
	}
	slices.Sort(fromValidator)

	want := slices.Clone(notNull14)
	slices.Sort(want) // notNull14 is already sorted; clone+sort keeps this robust to reordering

	if !slices.Equal(fromValidator, want) {
		t.Errorf("validator required-non-null set %v\n != notNull14 %v", fromValidator, want)
	}
}
