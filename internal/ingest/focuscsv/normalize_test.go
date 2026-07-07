// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package focuscsv

import (
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/focus"
)

// TestNormalizeTimestampAcceptReject is the primary per-form correctness proof for
// --lenient. It is white-box (package focuscsv) because normalizeTimestamp is
// unexported. Each ACCEPT row must, after normalization, parse under the SHARED
// strict focus.ParseTime and equal an INDEPENDENTLY hard-coded instant (never one
// derived from the normalizer — that would tautologically pass an identity no-op
// or a wrong-but-consistent normalizer). Each REJECT row must come back verbatim
// AND still fail focus.ParseTime with no fabricated zone — the money-safety
// boundary (zone-less/ambiguous values are never silently assumed UTC).
func TestNormalizeTimestampAcceptReject(t *testing.T) {
	// Two independent, hand-written reference instants the ACCEPT rows collapse to.
	jan1 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	sep18 := time.Date(2024, 9, 18, 22, 0, 0, 0, time.UTC)
	may1 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	accept := []struct {
		in   string
		want time.Time
		note string
	}{
		{"2024-01-01T00:00Z", jan1, "no-seconds + Z (OCI, Azure 1.0)"},
		{"2024-01-01T00:00+00:00", jan1, "no-seconds + offset"},
		{"2024-01-01T00:00:00+0000", jan1, "offset without colon (JVM)"},
		{"2024-01-01T05:00:00+05:00", jan1, "non-zero offset → UTC arithmetic (05:00+05:00 == 00:00Z)"},
		{"2024-01-01 00:00:00Z", jan1, "space + Z"},
		{"2024-01-01 00:00:00+00:00", jan1, "space + offset, with colon"},
		{"2024-09-18 22:00:00 UTC", sep18, "space + named UTC (BigQuery)"},
		{"2024-09-18 22:00:00.000000 UTC", sep18, "space + fractional + named UTC"},
		{"2026-05-01T00:00:00Z", may1, "already canonical → itself (idempotence)"},
	}
	for _, tc := range accept {
		got := normalizeTimestamp(tc.in)
		parsed, err := focus.ParseTime(got)
		if err != nil {
			t.Errorf("ACCEPT %q (%s): normalizeTimestamp→%q, ParseTime error %v; want it to parse",
				tc.in, tc.note, got, err)
			continue
		}
		if !parsed.Equal(tc.want) {
			t.Errorf("ACCEPT %q (%s): got instant %s, want %s", tc.in, tc.note, parsed, tc.want)
		}
	}

	// Idempotence: a canonical RFC3339 input returns itself byte-for-byte.
	if got := normalizeTimestamp("2026-05-01T00:00:00Z"); got != "2026-05-01T00:00:00Z" {
		t.Errorf("idempotence: normalizeTimestamp(canonical) = %q, want it unchanged", got)
	}

	reject := []struct {
		in   string
		note string
	}{
		{"2024-01-01 00:00:00", "zone-less (Alibaba-class local-time hazard)"},
		{"2024-01-01T00:00:00", "zone-less (T form)"},
		{"2024-01-01 00:00", "zone-less, no seconds"},
		{"2024-09-18 22:00:00 PST", "non-UTC named zone — must NOT parse at a wrong (zero) offset"},
		{"2024-01-01", "date-only"},
		{"1780272000", "epoch (unit-ambiguous)"},
		{"6/1/2026", "locale/ambiguous date"},
	}
	for _, tc := range reject {
		got := normalizeTimestamp(tc.in)
		// (a) returned verbatim per the step-5 contract.
		if got != tc.in {
			t.Errorf("REJECT %q (%s): normalizeTimestamp mutated it to %q; want it unchanged", tc.in, tc.note, got)
		}
		// (b) the real money-safety boundary: the strict parser still errors, and
		// no fabricated zone was appended (holds regardless of the (a) contract).
		if _, err := focus.ParseTime(got); err == nil {
			t.Errorf("REJECT %q (%s): ParseTime(%q) succeeded; a non-RFC3339 value must still be rejected", tc.in, tc.note, got)
		}
		if strings.HasSuffix(got, "Z") || strings.Contains(got, "+00:00") || strings.HasSuffix(got, "+0000") {
			t.Errorf("REJECT %q (%s): output %q carries a fabricated UTC zone", tc.in, tc.note, got)
		}
	}
}

// TestNormalizeRecordTimestampsLeavesNonDateColumns proves --lenient is
// timestamp-FORMAT-only at the record level: normalizeRecordTimestamps rewrites
// ONLY the four Date/Time columns and never a value column, so a locale-formatted
// numeric (a thousands separator that the strict decimal parser will reject) is
// left byte-verbatim for that same strict rejection — --lenient never coerces a
// number (step-0 NIT). A regression that widened the rewrite set to a value
// column would redden here.
func TestNormalizeRecordTimestampsLeavesNonDateColumns(t *testing.T) {
	rec := focus.RawRecord{
		"ChargePeriodStart": "2024-01-01 00:00:00 UTC", // a genuine quirk → rewritten
		"BilledCost":        "1,234.56",                // locale-formatted number → untouched
		"PricingQuantity":   "1.000,50",                // European grouping → untouched
		"ServiceName":       "Compute",                 // free text → untouched
	}
	normalizeRecordTimestamps(rec)
	if rec["ChargePeriodStart"] != "2024-01-01T00:00:00Z" {
		t.Errorf("ChargePeriodStart = %q, want the normalized 2024-01-01T00:00:00Z", rec["ChargePeriodStart"])
	}
	for col, want := range map[string]string{
		"BilledCost":      "1,234.56",
		"PricingQuantity": "1.000,50",
		"ServiceName":     "Compute",
	} {
		if rec[col] != want {
			t.Errorf("--lenient touched non-date column %s: got %q, want it unchanged %q", col, rec[col], want)
		}
	}
}
