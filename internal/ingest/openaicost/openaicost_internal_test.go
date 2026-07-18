// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package openaicost

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/storage"
)

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

// TestMalformedQuantityDegradesToMoneyOnly is the item-3 fix-up proof for the
// degrade HALF of the malformed-quantity path (openaicost.go synthesize): a
// recognized direction (", output") whose quantity literal is malformed (a JSON
// string) must DEGRADE to money-only — synthesize keeps the row and does NOT
// fail the period (the deliberate asymmetry with a malformed AMOUNT, which fails
// per D23). Only the counting half was proven (TestOpenaiAnomalies); this pins
// the degrade half. Removing the `if err == nil` guard (emitting garbage
// enrichment from the failed parse) or failing the period breaks this test.
func TestMalformedQuantityDegradesToMoneyOnly(t *testing.T) {
	c := &Connector{
		month:      "2026-05",
		monthStart: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		monthEnd:   time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	}
	b := bucket{StartTime: 1777593600, EndTime: 1777680000} // exactly one day
	res := result{
		Amount:   amount{Value: json.RawMessage("1.0"), Currency: "usd"},
		LineItem: "gpt-4o, output",             // a recognized direction suffix
		Quantity: json.RawMessage(`"1500000"`), // a JSON string → NOT a valid decimal
	}
	rec, err := c.synthesize(b, res)
	if err != nil {
		t.Fatalf("a malformed quantity must degrade to money-only, not fail the period: %v", err)
	}
	if rec["BilledCost"] != "1" {
		t.Errorf("BilledCost = %q, want 1 (money kept on the degraded row)", rec["BilledCost"])
	}
	for _, col := range []string{"ConsumedQuantity", "ConsumedUnit", "SkuId", "SkuPriceId", "SkuMeter", "PricingQuantity", "PricingUnit"} {
		if v, ok := rec[col]; ok {
			t.Errorf("degraded row must be money-only but carries %s=%q", col, v)
		}
	}
}

// TestOpenaiUsageMetrics proves openaiUsageMetrics surfaces exactly the
// unknown-unit quantity-bearing rows (USG-3): unit "Unknown" (never guessed
// "Tokens"), metric_name = the line_item VERBATIM, service_name = "OpenAI API",
// service_tier = "". A recognized direction (enriched onto its cost row), a
// null/absent quantity (normal money-only), and — the ADDENDUM (B) guard — an
// unknown-unit row whose quantity literal is NOT a valid decimal are all
// EXCLUDED (a non-decimal cannot be stored in the DECIMAL column; emit nothing).
func TestOpenaiUsageMetrics(t *testing.T) {
	buckets := []bucket{
		{
			StartTime: 1777593600, EndTime: 1777680000, // 2026-05-01
			Results: []result{
				// unknown unit, valid quantity → EMITTED.
				{LineItem: "assistants api | file search", Quantity: json.RawMessage("42")},
				// recognized direction → enriched onto its cost row, NOT orphaned.
				{LineItem: "gpt-4o, input", Quantity: json.RawMessage("1500000")},
				// null quantity → normal money-only, NOT an orphan.
				{LineItem: "Promotional credit", Quantity: json.RawMessage("null")},
				// unknown unit but MALFORMED quantity (JSON string) → NOT emitted
				// (cannot store; stays money-only, ADDENDUM B).
				{LineItem: "web search tool call", Quantity: json.RawMessage(`"7"`)},
			},
		},
	}
	metrics := openaiUsageMetrics(buckets)
	want := []storage.Metric{{
		ChargePeriodStart: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		ServiceName:       "OpenAI API",
		ServiceTier:       "",
		MetricName:        "assistants api | file search",
		Unit:              "Unknown",
		Quantity:          decimal.RequireFromString("42"),
	}}
	if len(metrics) != len(want) {
		t.Fatalf("metrics = %+v, want exactly the one unknown-unit valid-quantity row (%+v)", metrics, want)
	}
	g := metrics[0]
	if !g.ChargePeriodStart.Equal(want[0].ChargePeriodStart) || g.ServiceName != want[0].ServiceName ||
		g.ServiceTier != want[0].ServiceTier || g.MetricName != want[0].MetricName ||
		g.Unit != want[0].Unit || !g.Quantity.Equal(want[0].Quantity) {
		t.Errorf("metric = %+v, want %+v", g, want[0])
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
