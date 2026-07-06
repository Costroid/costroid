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
		{"knownColumns12", len(knownColumns12), 57},
		{"knownColumns13", len(knownColumns13), 65},
		{"knownColumns14", len(knownColumns14), 65},
		{"mandatory12", len(mandatory12), 21},
		{"mandatory13", len(mandatory13), 23},
		{"notNull14", len(notNull14), 15},
		{"mandatoryNullable14", len(mandatoryNullable14), 6},
	} {
		if tc.got != tc.want {
			t.Errorf("len(%s) = %d, want %d", tc.name, tc.got, tc.want)
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
