// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package anthropiccost

import (
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/storage"
)

// tokensCost builds one cost_type="tokens" result for the join tests.
func tokensCost(model, ws, tier, tt string) result {
	return result{
		Amount: "100", Currency: "USD", Description: "tokens",
		Model: model, WorkspaceID: ws,
		CostType: "tokens", TokenType: tt, ContextWindow: "0-200k", ServiceTier: tier,
	}
}

// TestEnrichMonth exercises the pure join: the null-cost-geo summed-across-geos
// match, the >1-cost-row collision that enriches NONE, tolerance of a non-tokens
// cost_type, priority-tier and web-search and unmatched-standard usage orphans,
// and the all-or-none atomic decoration.
func TestEnrichMonth(t *testing.T) {
	costBuckets := []bucket{
		{
			StartingAt: "2026-05-01T00:00:00Z",
			Results: []result{
				tokensCost("claude-opus-4-6", "wrkspc_alpha", "standard", ttUncachedInput),
				{Amount: "500", Currency: "USD", Description: "session", Model: "claude-opus-4-6", CostType: "session_usage"},
			},
		},
		{
			StartingAt: "2026-05-02T00:00:00Z",
			Results: []result{
				tokensCost("claude-opus-4-6", "wrkspc_gamma", "standard", ttOutput),
				tokensCost("claude-opus-4-6", "wrkspc_gamma", "standard", ttOutput),
			},
		},
	}
	usageBuckets := []usageBucket{
		{
			StartingAt: "2026-05-01T00:00:00Z",
			Results: []usageResult{
				{Model: "claude-opus-4-6", WorkspaceID: "wrkspc_alpha", ContextWindow: "0-200k", InferenceGeo: "us", ServiceTier: "standard", Uncached: json.Number("700000")},
				{Model: "claude-opus-4-6", WorkspaceID: "wrkspc_alpha", ContextWindow: "0-200k", InferenceGeo: "eu", ServiceTier: "standard", Uncached: json.Number("800000")},
				{Model: "claude-opus-4-6", WorkspaceID: "wrkspc_alpha", ContextWindow: "0-200k", InferenceGeo: "us", ServiceTier: "priority", Uncached: json.Number("999")},
				{Model: "claude-opus-4-6", WorkspaceID: "wrkspc_alpha", ContextWindow: "0-200k", InferenceGeo: "us", ServiceTier: "standard", ServerToolUse: &usageServerToolUse{WebSearchRequests: json.Number("5")}},
			},
		},
		{
			StartingAt: "2026-05-02T00:00:00Z",
			Results: []usageResult{
				{Model: "claude-opus-4-6", WorkspaceID: "wrkspc_gamma", ContextWindow: "0-200k", InferenceGeo: "us", ServiceTier: "standard", Output: json.Number("50000")},
				{Model: "claude-opus-4-6", WorkspaceID: "wrkspc_delta", ContextWindow: "0-200k", InferenceGeo: "us", ServiceTier: "standard", Uncached: json.Number("42")},
			},
		},
	}

	enrich, summary, _ := enrichMonth(costBuckets, usageBuckets)

	// Summed-geo unique match: the null-geo cost row draws 700000+800000.
	got, ok := enrich[rowKey{0, 0}]
	if !ok {
		t.Fatal("day-1 alpha uncached row was not enriched")
	}
	if !got.quantity.Equal(decimal.RequireFromString("1500000")) {
		t.Errorf("summed-geo quantity = %s, want 1500000", got.quantity)
	}
	if got.skuID != "anthropic/claude-opus-4-6/uncached_input_tokens/0-200k" ||
		got.skuPriceID != "anthropic/claude-opus-4-6/uncached_input_tokens/0-200k/standard" ||
		got.skuMeter != "Input Tokens" {
		t.Errorf("minted SKU wrong: %+v", got)
	}
	if !got.pricingQty.Equal(decimal.RequireFromString("1.5")) {
		t.Errorf("pricingQty = %s, want 1.5", got.pricingQty)
	}

	// The non-tokens cost row is tolerated and never enriched.
	if _, ok := enrich[rowKey{0, 1}]; ok {
		t.Error("session_usage row must not be enriched")
	}
	// Both collision rows are enriched by NEITHER.
	if _, ok := enrich[rowKey{1, 0}]; ok {
		t.Error("collision row 0 must not be enriched")
	}
	if _, ok := enrich[rowKey{1, 1}]; ok {
		t.Error("collision row 1 must not be enriched")
	}

	if summary.collisions != 1 || summary.collidedRows != 2 {
		t.Errorf("collisions=%d collidedRows=%d, want 1/2", summary.collisions, summary.collidedRows)
	}
	if summary.orphanUsageKeys != 1 { // the delta standard row
		t.Errorf("orphanUsageKeys=%d, want 1", summary.orphanUsageKeys)
	}
	if summary.tierOrphanRows != 1 { // the priority row
		t.Errorf("tierOrphanRows=%d, want 1", summary.tierOrphanRows)
	}
	if !summary.webSearchRequests.Equal(decimal.RequireFromString("5")) {
		t.Errorf("webSearchRequests=%s, want 5", summary.webSearchRequests)
	}
	if summary.String() == "" || !strings.HasPrefix(summary.String(), "usage/cost reconciliation:") {
		t.Errorf("summary line = %q, want the reconciliation prefix", summary.String())
	}
}

// TestEnrichMonthMetrics proves the THIRD return of enrichMonth: the
// cost-orphaned usage metrics surfaced for the usage_metrics store. Every orphan
// CLASS appears at its exact quantity/unit/tier — priority-tier tokens (unit
// "Tokens"), the web-search count (unit "Requests", metric web_search_requests),
// and a standard-tier usage key no cost row referenced (unit "Tokens") — while a
// standard-tier key that DOES join to a cost row is ABSENT (over-capturing an
// enriched/referenced key would surface it here, so this is the guard). The
// slice order is nondeterministic (the agg map loop), so it is sorted first.
func TestEnrichMonthMetrics(t *testing.T) {
	costBuckets := []bucket{{
		StartingAt: "2026-05-01T00:00:00Z",
		Results: []result{
			// References the alpha/standard/uncached key → that usage key is NOT
			// an orphan and must not appear as a metric.
			tokensCost("claude-opus-4-6", "wrkspc_alpha", "standard", ttUncachedInput),
		},
	}}
	usageBuckets := []usageBucket{
		{
			StartingAt: "2026-05-01T00:00:00Z",
			Results: []usageResult{
				{Model: "claude-opus-4-6", WorkspaceID: "wrkspc_alpha", ContextWindow: "0-200k", InferenceGeo: "us", ServiceTier: "standard", Uncached: json.Number("700000")},                                         // referenced → absent
				{Model: "claude-opus-4-6", WorkspaceID: "wrkspc_alpha", ContextWindow: "0-200k", InferenceGeo: "us", ServiceTier: "priority", Uncached: json.Number("999")},                                            // priority orphan
				{Model: "claude-opus-4-6", WorkspaceID: "wrkspc_alpha", ContextWindow: "0-200k", InferenceGeo: "us", ServiceTier: "standard", ServerToolUse: &usageServerToolUse{WebSearchRequests: json.Number("5")}}, // web-search
			},
		},
		{
			StartingAt: "2026-05-02T00:00:00Z",
			Results: []usageResult{
				{Model: "claude-opus-4-6", WorkspaceID: "wrkspc_delta", ContextWindow: "0-200k", InferenceGeo: "us", ServiceTier: "standard", Uncached: json.Number("42")}, // standard orphan (no cost row)
			},
		},
	}

	_, _, metrics := enrichMonth(costBuckets, usageBuckets)
	slices.SortFunc(metrics, func(a, b storage.Metric) int {
		if c := a.ChargePeriodStart.Compare(b.ChargePeriodStart); c != 0 {
			return c
		}
		if c := strings.Compare(a.ServiceTier, b.ServiceTier); c != 0 {
			return c
		}
		return strings.Compare(a.MetricName, b.MetricName)
	})

	day := func(d int) time.Time { return time.Date(2026, 5, d, 0, 0, 0, 0, time.UTC) }
	want := []storage.Metric{
		{ChargePeriodStart: day(1), ServiceName: "claude-opus-4-6", ServiceTier: "priority", MetricName: "uncached_input_tokens", Unit: "Tokens", Quantity: decimal.RequireFromString("999")},
		{ChargePeriodStart: day(1), ServiceName: "claude-opus-4-6", ServiceTier: "standard", MetricName: "web_search_requests", Unit: "Requests", Quantity: decimal.RequireFromString("5")},
		{ChargePeriodStart: day(2), ServiceName: "claude-opus-4-6", ServiceTier: "standard", MetricName: "uncached_input_tokens", Unit: "Tokens", Quantity: decimal.RequireFromString("42")},
	}
	if len(metrics) != len(want) {
		t.Fatalf("metrics = %+v, want %d (the referenced standard key must be ABSENT)", metrics, len(want))
	}
	for i, w := range want {
		g := metrics[i]
		if !g.ChargePeriodStart.Equal(w.ChargePeriodStart) || g.ServiceName != w.ServiceName ||
			g.ServiceTier != w.ServiceTier || g.MetricName != w.MetricName || g.Unit != w.Unit ||
			!g.Quantity.Equal(w.Quantity) {
			t.Errorf("metric %d = %+v, want %+v", i, g, w)
		}
	}
}

// TestEnrichMonthAggregatesDuplicateBuckets proves duplicate usage buckets for
// one key SUM (never last-wins), e.g. the same key split across pages.
func TestEnrichMonthAggregatesDuplicateBuckets(t *testing.T) {
	costBuckets := []bucket{{
		StartingAt: "2026-05-01T00:00:00Z",
		Results:    []result{tokensCost("claude-opus-4-6", "wrkspc_alpha", "standard", ttOutput)},
	}}
	dup := usageResult{Model: "claude-opus-4-6", WorkspaceID: "wrkspc_alpha", ContextWindow: "0-200k", InferenceGeo: "us", ServiceTier: "standard", Output: json.Number("1000")}
	usageBuckets := []usageBucket{
		{StartingAt: "2026-05-01T00:00:00Z", Results: []usageResult{dup}},
		{StartingAt: "2026-05-01T00:00:00Z", Results: []usageResult{dup}},
	}
	enrich, _, _ := enrichMonth(costBuckets, usageBuckets)
	got, ok := enrich[rowKey{0, 0}]
	if !ok || !got.quantity.Equal(decimal.RequireFromString("2000")) {
		t.Errorf("duplicate buckets did not sum: got %v (ok=%t), want 2000", got.quantity, ok)
	}
}

// TestEncodeQueryUsesBracketedGroupBy proves the wire query carries the
// bracketed group_by[]= parameters (Anthropic's documented form) for BOTH the
// cost and usage dim sets, never a bare group_by=.
func TestEncodeQueryUsesBracketedGroupBy(t *testing.T) {
	q := url.Values{}
	q.Set("bucket_width", "1d")
	q.Set("limit", "31")
	got := encodeQuery(q, costGroupBy)
	if want := "group_by[]=description&group_by[]=workspace_id"; !strings.Contains(got, want) {
		t.Errorf("encodeQuery(cost) = %q, want it to contain %q", got, want)
	}
	if strings.Contains(got, "group_by=description") || strings.Contains(got, "group_by=workspace_id") {
		t.Errorf("encodeQuery(cost) = %q, must not carry a bare group_by=", got)
	}
	gotUsage := encodeQuery(url.Values{}, usageGroupBy)
	for _, dim := range usageGroupBy {
		if !strings.Contains(gotUsage, "group_by[]="+dim) {
			t.Errorf("encodeQuery(usage) = %q, want bracketed group_by[]=%s", gotUsage, dim)
		}
		if strings.Contains(gotUsage, "group_by="+dim) {
			t.Errorf("encodeQuery(usage) = %q, must not carry a bare group_by=%s", gotUsage, dim)
		}
	}
}

// TestContentHashCoversCostAndUsage proves the change token depends on the cost
// AND usage data bytes and their fetch order, but not the pagination envelope —
// so a quantity-only restatement (usage changed, cost unchanged) supersedes.
func TestContentHashCoversCostAndUsage(t *testing.T) {
	a := []byte(`{"starting_at":"2026-05-01T00:00:00Z"}`)
	b := []byte(`{"starting_at":"2026-05-02T00:00:00Z"}`)
	u := []byte(`{"uncached_input_tokens":700000}`)
	u2 := []byte(`{"uncached_input_tokens":600000}`)

	base := contentHash([][]byte{a, b}, [][]byte{u})
	if contentHash([][]byte{a, b}, [][]byte{u}) != base {
		t.Error("content hash not stable for identical input")
	}
	if contentHash([][]byte{b, a}, [][]byte{u}) == base {
		t.Error("content hash ignored cost fetch order")
	}
	if contentHash([][]byte{a, b}, [][]byte{u2}) == base {
		t.Error("content hash ignored the usage payload (a quantity-only restatement would be missed)")
	}
	if contentHash(nil, nil) != "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Error("empty content hash is not the empty-stream digest")
	}
}

// TestUsageQuantityJSONSurvivesFloat64 proves a usage token count survives the
// REAL JSON round-trip (usageResult unmarshal → tokenPairs → enrichMonth)
// exactly, built from its literal and never through float64. The value 2^53+1
// is the smallest integer float64 cannot represent (it rounds to 2^53). This
// FAILS if usageResult's token fields are changed from json.Number to float64.
func TestUsageQuantityJSONSurvivesFloat64(t *testing.T) {
	const hazard = "9007199254740993" // 2^53 + 1; float64 rounds it to ...992
	// Guard: confirm the value truly corrupts under float64, so the test bites.
	f, err := strconv.ParseFloat(hazard, 64)
	if err != nil {
		t.Fatalf("parsing the hazard value: %v", err)
	}
	if viaFloat := decimal.NewFromFloat(f).String(); viaFloat == hazard {
		t.Fatalf("test value %q does not corrupt under float64 (got %q) — choose a harder value", hazard, viaFloat)
	}

	// Decode BOTH payloads from JSON so the real unmarshal path runs — not a
	// hand-built decimal.
	var usageBuckets []usageBucket
	if err := json.Unmarshal([]byte(`[{
		"starting_at":"2026-05-01T00:00:00Z","ending_at":"2026-05-02T00:00:00Z",
		"results":[{"model":"claude-opus-4-6","workspace_id":"wrkspc_alpha","context_window":"0-200k",
		            "inference_geo":"us","service_tier":"standard","uncached_input_tokens":9007199254740993}]
	}]`), &usageBuckets); err != nil {
		t.Fatalf("unmarshaling usage bucket: %v", err)
	}
	var costBuckets []bucket
	if err := json.Unmarshal([]byte(`[{
		"starting_at":"2026-05-01T00:00:00Z","ending_at":"2026-05-02T00:00:00Z",
		"results":[{"amount":"100","currency":"USD","model":"claude-opus-4-6","workspace_id":"wrkspc_alpha",
		            "cost_type":"tokens","context_window":"0-200k","service_tier":"standard","token_type":"uncached_input_tokens"}]
	}]`), &costBuckets); err != nil {
		t.Fatalf("unmarshaling cost bucket: %v", err)
	}

	enrich, _, _ := enrichMonth(costBuckets, usageBuckets)
	got, ok := enrich[rowKey{0, 0}]
	if !ok {
		t.Fatal("the matching cost tokens row was not enriched")
	}
	if got.quantity.String() != hazard {
		t.Errorf("ConsumedQuantity = %q, want the exact literal %q (float64 would corrupt it)", got.quantity.String(), hazard)
	}
	if got.pricingQty.String() != "9007199254.740993" {
		t.Errorf("PricingQuantity = %q, want 9007199254.740993 (exact ÷1,000,000 shift)", got.pricingQty.String())
	}
}

// TestUnmatchedTokensRowStaysMoneyOnly guards enrich.go's `if !found { continue }`
// (the no-zero-quantity-row rule, decision D33): a cost tokens row whose join
// key has NO matching usage must gain NO enrichment — never a minted
// ConsumedQuantity="0" + full SKU set. Removing the guard would fail this test.
func TestUnmatchedTokensRowStaysMoneyOnly(t *testing.T) {
	costBuckets := []bucket{{
		StartingAt: "2026-05-01T00:00:00Z",
		EndingAt:   "2026-05-02T00:00:00Z",
		Results:    []result{tokensCost("claude-opus-4-6", "wrkspc_alpha", "standard", ttUncachedInput)},
	}}
	// Usage exists ONLY for a different workspace, so the cost row finds nothing.
	usageBuckets := []usageBucket{{
		StartingAt: "2026-05-01T00:00:00Z",
		Results: []usageResult{{
			Model: "claude-opus-4-6", WorkspaceID: "wrkspc_other", ContextWindow: "0-200k",
			InferenceGeo: "us", ServiceTier: "standard", Uncached: json.Number("123"),
		}},
	}}

	enrich, _, _ := enrichMonth(costBuckets, usageBuckets)
	if _, ok := enrich[rowKey{0, 0}]; ok {
		t.Fatal("an unmatched standard-tier tokens row must NOT be enriched (no zero-quantity FOCUS row, D33)")
	}

	// The synthesized record must carry NONE of the seven enrichment columns and
	// still keep its money (an ingested money-only row, not a dropped one).
	c := &Connector{
		month:      "2026-05",
		monthStart: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		monthEnd:   time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		buckets:    costBuckets,
		enrich:     enrich,
	}
	rec, err := c.synthesize(costBuckets[0], costBuckets[0].Results[0], rowKey{0, 0})
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	for _, col := range []string{"ConsumedQuantity", "ConsumedUnit", "SkuId", "SkuPriceId", "SkuMeter", "PricingQuantity", "PricingUnit"} {
		if v, ok := rec[col]; ok {
			t.Errorf("unmatched tokens row must be money-only but carries %s=%q", col, v)
		}
	}
	if rec["BilledCost"] != "1" { // amount 100 cents → 1 dollar
		t.Errorf("BilledCost = %q, want 1 (money-only row still ingested)", rec["BilledCost"])
	}
}

// TestFrozenSkuMints is the golden test pinning ALL FIVE frozen Costroid mints
// (decision D33 — frozen once shipped): the token_type → SkuMeter map, the SkuId
// (dotted token_type for the two cache_creation kinds), the SkuPriceId, and the
// "Tokens" / "1000000 Tokens" units. Usage is decoded from JSON (real
// usageResult + nested cache_creation unmarshal, carrying BOTH ephemeral_5m and
// ephemeral_1h) and one cost tokens row per token_type shares the same
// day/model/context_window/workspace/standard-tier key (distinct token_type →
// distinct keys, no collision). It FAILS if any frozen meter string is typo'd,
// any usageResult/usageCacheCreation json tag is wrong, or the ephemeral_1h
// unpivot line is deleted.
func TestFrozenSkuMints(t *testing.T) {
	const (
		model = "claude-opus-4-6"
		ctx   = "0-200k"
		ws    = "wrkspc_alpha"
	)
	// One usage row carrying all five token quantities (two nested). Distinct
	// values so a mis-wired token_type would surface as the wrong quantity.
	var usageBuckets []usageBucket
	if err := json.Unmarshal([]byte(`[{
		"starting_at":"2026-05-01T00:00:00Z","ending_at":"2026-05-02T00:00:00Z",
		"results":[{"model":"claude-opus-4-6","workspace_id":"wrkspc_alpha","context_window":"0-200k",
		            "inference_geo":"us","service_tier":"standard",
		            "uncached_input_tokens":1000000,
		            "cache_read_input_tokens":2000000,
		            "output_tokens":3000000,
		            "cache_creation":{"ephemeral_5m_input_tokens":4000000,"ephemeral_1h_input_tokens":5000000}}]
	}]`), &usageBuckets); err != nil {
		t.Fatalf("unmarshaling usage bucket: %v", err)
	}

	// One cost tokens row per token_type, in a fixed order so rowKey ri is known.
	var costBuckets []bucket
	if err := json.Unmarshal([]byte(`[{
		"starting_at":"2026-05-01T00:00:00Z","ending_at":"2026-05-02T00:00:00Z",
		"results":[
		  {"amount":"100","currency":"USD","model":"claude-opus-4-6","workspace_id":"wrkspc_alpha","cost_type":"tokens","context_window":"0-200k","service_tier":"standard","token_type":"uncached_input_tokens"},
		  {"amount":"100","currency":"USD","model":"claude-opus-4-6","workspace_id":"wrkspc_alpha","cost_type":"tokens","context_window":"0-200k","service_tier":"standard","token_type":"cache_read_input_tokens"},
		  {"amount":"100","currency":"USD","model":"claude-opus-4-6","workspace_id":"wrkspc_alpha","cost_type":"tokens","context_window":"0-200k","service_tier":"standard","token_type":"output_tokens"},
		  {"amount":"100","currency":"USD","model":"claude-opus-4-6","workspace_id":"wrkspc_alpha","cost_type":"tokens","context_window":"0-200k","service_tier":"standard","token_type":"cache_creation.ephemeral_5m_input_tokens"},
		  {"amount":"100","currency":"USD","model":"claude-opus-4-6","workspace_id":"wrkspc_alpha","cost_type":"tokens","context_window":"0-200k","service_tier":"standard","token_type":"cache_creation.ephemeral_1h_input_tokens"}
		]
	}]`), &costBuckets); err != nil {
		t.Fatalf("unmarshaling cost bucket: %v", err)
	}

	enrich, _, _ := enrichMonth(costBuckets, usageBuckets)
	c := &Connector{
		month:      "2026-05",
		monthStart: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		monthEnd:   time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		buckets:    costBuckets,
		enrich:     enrich,
	}

	cases := []struct {
		ri              int
		tokenType       string
		meter, qty, pqy string
	}{
		{0, "uncached_input_tokens", "Input Tokens", "1000000", "1"},
		{1, "cache_read_input_tokens", "Cache Read Tokens", "2000000", "2"},
		{2, "output_tokens", "Output Tokens", "3000000", "3"},
		{3, "cache_creation.ephemeral_5m_input_tokens", "Cache Write Tokens (5m)", "4000000", "4"},
		{4, "cache_creation.ephemeral_1h_input_tokens", "Cache Write Tokens (1h)", "5000000", "5"},
	}
	for _, tc := range cases {
		rec, err := c.synthesize(costBuckets[0], costBuckets[0].Results[tc.ri], rowKey{0, tc.ri})
		if err != nil {
			t.Fatalf("%s: synthesize: %v", tc.tokenType, err)
		}
		sku := "anthropic/" + model + "/" + tc.tokenType + "/" + ctx
		want := map[string]string{
			"ConsumedQuantity": tc.qty,
			"ConsumedUnit":     "Tokens",
			"SkuId":            sku,
			"SkuPriceId":       sku + "/standard",
			"SkuMeter":         tc.meter,
			"PricingQuantity":  tc.pqy,
			"PricingUnit":      "1000000 Tokens",
		}
		for col, w := range want {
			if rec[col] != w {
				t.Errorf("%s: %s = %q, want %q", tc.tokenType, col, rec[col], w)
			}
		}
	}
	_ = ws // documents the shared workspace dimension used in the fixtures above
}

// TestEnrichMonthMixedGeoCollision is the item-1 fix-up proof: a null-geo cost
// row and a per-geo ("us") cost row that share the SAME remaining join key are
// ambiguous. The OLD grouping (joinKey + cost-side geo) put them in separate
// groups, so BOTH enriched — the null-geo row taking the all-geos sum
// (1,500,000) and the us row its per-geo slice (700,000), storing 2,200,000
// against 1,500,000 of actual usage with the collision counter at zero. Under
// the joinKey-alone grouping they collide → enrich NONE, collision counted. This
// FAILS if the grouping regresses to include the cost-side geo.
func TestEnrichMonthMixedGeoCollision(t *testing.T) {
	var costBuckets []bucket
	if err := json.Unmarshal([]byte(`[{
		"starting_at":"2026-05-01T00:00:00Z","ending_at":"2026-05-02T00:00:00Z",
		"results":[
		  {"amount":"100","currency":"USD","model":"claude-opus-4-6","workspace_id":"wrkspc_alpha","cost_type":"tokens","context_window":"0-200k","service_tier":"standard","token_type":"output_tokens"},
		  {"amount":"100","currency":"USD","model":"claude-opus-4-6","workspace_id":"wrkspc_alpha","cost_type":"tokens","context_window":"0-200k","service_tier":"standard","token_type":"output_tokens","inference_geo":"us"}
		]
	}]`), &costBuckets); err != nil {
		t.Fatalf("unmarshaling cost bucket: %v", err)
	}
	// Usage for the shared key in two geos: us=700000, eu=800000 (geoless=1,500,000).
	var usageBuckets []usageBucket
	if err := json.Unmarshal([]byte(`[{
		"starting_at":"2026-05-01T00:00:00Z","ending_at":"2026-05-02T00:00:00Z",
		"results":[
		  {"model":"claude-opus-4-6","workspace_id":"wrkspc_alpha","context_window":"0-200k","inference_geo":"us","service_tier":"standard","output_tokens":700000},
		  {"model":"claude-opus-4-6","workspace_id":"wrkspc_alpha","context_window":"0-200k","inference_geo":"eu","service_tier":"standard","output_tokens":800000}
		]
	}]`), &usageBuckets); err != nil {
		t.Fatalf("unmarshaling usage bucket: %v", err)
	}

	enrich, summary, _ := enrichMonth(costBuckets, usageBuckets)
	if _, ok := enrich[rowKey{0, 0}]; ok {
		t.Error("null-geo cost row must NOT be enriched (ambiguous mixed-geo collision, D33)")
	}
	if _, ok := enrich[rowKey{0, 1}]; ok {
		t.Error("us-geo cost row must NOT be enriched (ambiguous mixed-geo collision, D33)")
	}
	if summary.collisions != 1 || summary.collidedRows != 2 {
		t.Errorf("collisions=%d collidedRows=%d, want 1/2 (the mixed-geo pair)", summary.collisions, summary.collidedRows)
	}
}

// TestEmptyWorkspaceJoinEnriches is the item-2 fix-up proof: the default-
// workspace/Console join path — workspace_id empty on BOTH the cost and usage
// side — enriches. Every committed fixture carries a non-empty workspace, so
// this is the only coverage of the empty-on-both-sides tolerance. It FAILS if a
// guard ever skips empty-workspace rows.
func TestEmptyWorkspaceJoinEnriches(t *testing.T) {
	var costBuckets []bucket
	if err := json.Unmarshal([]byte(`[{
		"starting_at":"2026-05-01T00:00:00Z","ending_at":"2026-05-02T00:00:00Z",
		"results":[{"amount":"100","currency":"USD","model":"claude-opus-4-6","workspace_id":"",
		            "cost_type":"tokens","context_window":"0-200k","service_tier":"standard","token_type":"uncached_input_tokens"}]
	}]`), &costBuckets); err != nil {
		t.Fatalf("unmarshaling cost bucket: %v", err)
	}
	var usageBuckets []usageBucket
	if err := json.Unmarshal([]byte(`[{
		"starting_at":"2026-05-01T00:00:00Z","ending_at":"2026-05-02T00:00:00Z",
		"results":[{"model":"claude-opus-4-6","workspace_id":"","context_window":"0-200k",
		            "inference_geo":"us","service_tier":"standard","uncached_input_tokens":123456}]
	}]`), &usageBuckets); err != nil {
		t.Fatalf("unmarshaling usage bucket: %v", err)
	}

	enrich, _, _ := enrichMonth(costBuckets, usageBuckets)
	got, ok := enrich[rowKey{0, 0}]
	if !ok {
		t.Fatal("empty-workspace cost row was not enriched (default-workspace/Console join path)")
	}
	if !got.quantity.Equal(decimal.RequireFromString("123456")) {
		t.Errorf("empty-workspace join quantity = %s, want 123456", got.quantity)
	}
}

// TestUnknownTokenTypeToleratedNoJoin is the item-3 fix-up proof for the
// skuMeterByTokenType tolerance guard (enrich.go: unknown token_type → tolerate,
// leave money-only): two cost tokens rows carrying an unmintable token_type must
// NOT be treated as join candidates, so they cannot register as an ambiguous
// collision. Without the guard the two share a joinKey and would be counted as a
// collision — so asserting collisions==0 makes the guard mutation-proven.
func TestUnknownTokenTypeToleratedNoJoin(t *testing.T) {
	var costBuckets []bucket
	if err := json.Unmarshal([]byte(`[{
		"starting_at":"2026-05-01T00:00:00Z","ending_at":"2026-05-02T00:00:00Z",
		"results":[
		  {"amount":"100","currency":"USD","model":"claude-opus-4-6","workspace_id":"wrkspc_alpha","cost_type":"tokens","context_window":"0-200k","service_tier":"standard","token_type":"thinking_tokens"},
		  {"amount":"100","currency":"USD","model":"claude-opus-4-6","workspace_id":"wrkspc_alpha","cost_type":"tokens","context_window":"0-200k","service_tier":"standard","token_type":"thinking_tokens"}
		]
	}]`), &costBuckets); err != nil {
		t.Fatalf("unmarshaling cost bucket: %v", err)
	}
	enrich, summary, _ := enrichMonth(costBuckets, nil)
	if len(enrich) != 0 {
		t.Errorf("unknown-token_type rows must not be enriched, got %d", len(enrich))
	}
	if summary.collisions != 0 || summary.collidedRows != 0 {
		t.Errorf("unmintable token_type rows must not count as a collision: collisions=%d collidedRows=%d, want 0/0",
			summary.collisions, summary.collidedRows)
	}
}

// TestNonJoinableTierTokensRowTolerated is the item-3 fix-up proof for the
// non-joinable-tier guard (a tokens row on priority/flex stays money-only): two
// priority-tier cost tokens rows (a KNOWN token_type, so they clear the
// skuMeter guard) must not be join candidates. Without the tier guard they share
// a joinKey and would be counted as a collision — so collisions==0 makes it
// mutation-proven.
func TestNonJoinableTierTokensRowTolerated(t *testing.T) {
	var costBuckets []bucket
	if err := json.Unmarshal([]byte(`[{
		"starting_at":"2026-05-01T00:00:00Z","ending_at":"2026-05-02T00:00:00Z",
		"results":[
		  {"amount":"100","currency":"USD","model":"claude-opus-4-6","workspace_id":"wrkspc_alpha","cost_type":"tokens","context_window":"0-200k","service_tier":"priority","token_type":"output_tokens"},
		  {"amount":"100","currency":"USD","model":"claude-opus-4-6","workspace_id":"wrkspc_alpha","cost_type":"tokens","context_window":"0-200k","service_tier":"priority","token_type":"output_tokens"}
		]
	}]`), &costBuckets); err != nil {
		t.Fatalf("unmarshaling cost bucket: %v", err)
	}
	enrich, summary, _ := enrichMonth(costBuckets, nil)
	if len(enrich) != 0 {
		t.Errorf("priority-tier tokens rows must not be enriched, got %d", len(enrich))
	}
	if summary.collisions != 0 || summary.collidedRows != 0 {
		t.Errorf("non-joinable-tier tokens rows must not count as a collision: collisions=%d collidedRows=%d, want 0/0",
			summary.collisions, summary.collidedRows)
	}
}

// TestBadBucketTimestampCounted is the item-8 fix-up proof for the unparseable-
// timestamp silent degrade (enrich.go bucketDay → ""): a usage bucket AND a cost
// bucket whose starting_at will not parse each have their whole day fail to
// match. Both are now COUNTED in the per-period summary rather than silently
// stripping quantities. It FAILS if either day-guard's counter is removed.
func TestBadBucketTimestampCounted(t *testing.T) {
	costBuckets := []bucket{{
		StartingAt: "not-a-timestamp",
		Results:    []result{tokensCost("claude-opus-4-6", "wrkspc_alpha", "standard", ttUncachedInput)},
	}}
	usageBuckets := []usageBucket{{
		StartingAt: "also-not-a-timestamp",
		Results: []usageResult{{
			Model: "claude-opus-4-6", WorkspaceID: "wrkspc_alpha", ContextWindow: "0-200k",
			InferenceGeo: "us", ServiceTier: "standard", Uncached: json.Number("700000"),
		}},
	}}

	enrich, summary, _ := enrichMonth(costBuckets, usageBuckets)
	if len(enrich) != 0 {
		t.Errorf("rows in unparseable-timestamp buckets must not be enriched, got %d", len(enrich))
	}
	if summary.badBucketDays != 2 {
		t.Errorf("badBucketDays = %d, want 2 (one usage + one cost bucket)", summary.badBucketDays)
	}
	if !strings.Contains(summary.String(), "unparseable timestamp") {
		t.Errorf("summary %q missing the unparseable-timestamp line", summary.String())
	}
}

// TestInvalidTokenLiteralCounted is the item-8 fix-up proof for the malformed
// token-literal silent degrade (tokenPairs' decimal parse failure): a usage
// token count that is present but not a valid decimal was silently skipped;
// it is now COUNTED. A json.Number is built directly (bypassing the JSON
// unmarshal that would reject a non-number) so the literal reaches the parse.
// It FAILS if the badLiterals counter is removed.
func TestInvalidTokenLiteralCounted(t *testing.T) {
	usageBuckets := []usageBucket{{
		StartingAt: "2026-05-01T00:00:00Z",
		Results: []usageResult{{
			Model: "claude-opus-4-6", WorkspaceID: "wrkspc_alpha", ContextWindow: "0-200k",
			InferenceGeo: "us", ServiceTier: "standard", Uncached: json.Number("not-a-number"),
		}},
	}}

	_, summary, _ := enrichMonth(nil, usageBuckets)
	if summary.badTokenLiterals != 1 {
		t.Errorf("badTokenLiterals = %d, want 1 (the malformed literal)", summary.badTokenLiterals)
	}
	if !strings.Contains(summary.String(), "malformed literal") {
		t.Errorf("summary %q missing the malformed-literal line", summary.String())
	}
}

// TestNonNullCostGeoJoinBranch exercises the per-geo join branch (a cost row
// with a NON-NULL inference_geo matches only that geo's usage sum, not the
// geoless sum across geos). Every committed fixture has an empty cost-side geo,
// so this is the only guard for that branch. It FAILS if the branch is broken to
// use the geoless (summed-across-geos) aggregate.
func TestNonNullCostGeoJoinBranch(t *testing.T) {
	// A cost tokens row scoped to inference_geo="us".
	var costBuckets []bucket
	if err := json.Unmarshal([]byte(`[{
		"starting_at":"2026-05-01T00:00:00Z","ending_at":"2026-05-02T00:00:00Z",
		"results":[{"amount":"100","currency":"USD","model":"claude-opus-4-6","workspace_id":"wrkspc_alpha",
		            "cost_type":"tokens","context_window":"0-200k","service_tier":"standard",
		            "token_type":"output_tokens","inference_geo":"us"}]
	}]`), &costBuckets); err != nil {
		t.Fatalf("unmarshaling cost bucket: %v", err)
	}
	// Usage for the same key in BOTH geos: us=700000, eu=800000.
	var usageBuckets []usageBucket
	if err := json.Unmarshal([]byte(`[{
		"starting_at":"2026-05-01T00:00:00Z","ending_at":"2026-05-02T00:00:00Z",
		"results":[
		  {"model":"claude-opus-4-6","workspace_id":"wrkspc_alpha","context_window":"0-200k","inference_geo":"us","service_tier":"standard","output_tokens":700000},
		  {"model":"claude-opus-4-6","workspace_id":"wrkspc_alpha","context_window":"0-200k","inference_geo":"eu","service_tier":"standard","output_tokens":800000}
		]
	}]`), &usageBuckets); err != nil {
		t.Fatalf("unmarshaling usage bucket: %v", err)
	}

	enrich, _, _ := enrichMonth(costBuckets, usageBuckets)
	got, ok := enrich[rowKey{0, 0}]
	if !ok {
		t.Fatal("the non-null-geo cost row was not enriched")
	}
	if !got.quantity.Equal(decimal.RequireFromString("700000")) {
		t.Errorf("per-geo join quantity = %s, want 700000 (the us sum ONLY, not the geoless 1500000)", got.quantity)
	}
}
