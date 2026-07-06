// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package openaicost

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestRetryAfterDelay proves the Retry-After parser honors BOTH delta-seconds
// AND an RFC 1123 HTTP-date (a bare date previously fell through to the
// default), caps the wait, and treats a past date as an immediate retry.
func TestRetryAfterDelay(t *testing.T) {
	future := time.Now().UTC().Add(30 * time.Second).Format(http.TimeFormat)
	past := "Mon, 01 Jan 2001 00:00:00 GMT"
	tests := []struct {
		name        string
		header      string
		wantZero    bool
		wantAtMost  time.Duration
		wantAtLeast time.Duration
	}{
		{name: "absent → default", header: "", wantAtLeast: 2 * time.Second, wantAtMost: 2 * time.Second},
		{name: "delta seconds", header: "5", wantAtLeast: 5 * time.Second, wantAtMost: 5 * time.Second},
		{name: "fractional delta", header: "0.01", wantAtMost: 20 * time.Millisecond, wantAtLeast: time.Millisecond},
		{name: "delta capped at 60s", header: "600", wantAtLeast: 60 * time.Second, wantAtMost: 60 * time.Second},
		{name: "http-date in the future", header: future, wantAtLeast: 20 * time.Second, wantAtMost: 60 * time.Second},
		{name: "http-date in the past → zero", header: past, wantZero: true},
		{name: "garbage → default", header: "not-a-number", wantAtLeast: 2 * time.Second, wantAtMost: 2 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := retryAfterDelay(tt.header)
			if tt.wantZero {
				if got != 0 {
					t.Errorf("retryAfterDelay(%q) = %v, want 0", tt.header, got)
				}
				return
			}
			if got < tt.wantAtLeast || got > tt.wantAtMost {
				t.Errorf("retryAfterDelay(%q) = %v, want within [%v, %v]", tt.header, got, tt.wantAtLeast, tt.wantAtMost)
			}
		})
	}
}

// TestWaitRetryAfterHonorsContext proves a cancelled context aborts the wait
// rather than sleeping (no wall-clock dependency).
func TestWaitRetryAfterHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitRetryAfter(ctx, "600"); err == nil {
		t.Error("waitRetryAfter with a cancelled context returned nil, want ctx.Err()")
	}
}

// TestOpenaiAnomalies is the item-4 + item-5 fix-up proof for the per-period
// anomaly counting (OAI-12):
//   - a null/absent-quantity row is the normal money-only case and is NOT
//     counted (item 5 — the disclosure table was corrected to match the code);
//   - a quantity-bearing row whose line_item unit is not derivable IS counted;
//   - a quantity-bearing row on a RECOGNIZED direction whose quantity literal is
//     malformed (a JSON string) IS counted (item 4 — previously swallowed).
//
// The credit row is null-quantity AND unknown-unit: asserting unknownUnitRows==1
// makes item 5 mutation-proven (counting null rows would push it to 2), and
// asserting malformedQuantityRows==1 makes item 4 mutation-proven.
func TestOpenaiAnomalies(t *testing.T) {
	buckets := []bucket{{
		Results: []result{
			// null quantity, no direction suffix → normal money-only, NOT counted.
			{LineItem: "Promotional credit", Quantity: json.RawMessage("null")},
			// absent quantity → normal money-only, NOT counted.
			{LineItem: "gpt-4o, input"},
			// quantity present, unknown unit → counted as unknownUnitRows.
			{LineItem: "assistants api | file search", Quantity: json.RawMessage("42")},
			// quantity present but malformed (JSON string) on a recognized
			// direction → counted as malformedQuantityRows (item 4).
			{LineItem: "gpt-4o, output", Quantity: json.RawMessage(`"1500000"`)},
		},
	}}
	s := openaiAnomalies(buckets)
	if s.unknownUnitRows != 1 {
		t.Errorf("unknownUnitRows = %d, want 1 (null-quantity rows must NOT be counted, item 5)", s.unknownUnitRows)
	}
	if s.malformedQuantityRows != 1 {
		t.Errorf("malformedQuantityRows = %d, want 1 (malformed quantity must be counted, item 4)", s.malformedQuantityRows)
	}
	line := s.String()
	if !strings.HasPrefix(line, "usage/cost reconciliation:") ||
		!strings.Contains(line, "unit could not be safely derived") ||
		!strings.Contains(line, "malformed quantity literal") {
		t.Errorf("summary = %q, want both the unknown-unit and malformed-quantity phrases", line)
	}
}

// TestOpenAISkuMeterGolden pins the frozen unit derivation (OAI-11/OAI-12) over
// representative opaque line_items: only the three documented trailing
// direction suffixes map to a meter; everything else is left unpriced (a unit is
// never guessed). ", cached input" must win over ", input".
func TestOpenAISkuMeterGolden(t *testing.T) {
	cases := []struct {
		lineItem  string
		wantMeter string
		wantOK    bool
	}{
		{"gpt-4o, input", "Input Tokens", true},
		{"ft-gpt-4o-2024-08-06, input", "Input Tokens", true},
		{"gpt-4o, output", "Output Tokens", true},
		{"gpt-4o, cached input", "Cache Read Tokens", true},
		{"o3-mini, cached input", "Cache Read Tokens", true},
		{"assistants api | file search", "", false},
		{"web search tool call", "", false},
		{"gpt-4o", "", false},
		{"gpt-image-1, image input", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		meter, ok := openaiSkuMeter(c.lineItem)
		if ok != c.wantOK || meter != c.wantMeter {
			t.Errorf("openaiSkuMeter(%q) = (%q, %t), want (%q, %t)", c.lineItem, meter, ok, c.wantMeter, c.wantOK)
		}
	}
}
