// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package anthropiccost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
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

	enrich, summary := enrichMonth(costBuckets, usageBuckets)

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
	enrich, _ := enrichMonth(costBuckets, usageBuckets)
	got, ok := enrich[rowKey{0, 0}]
	if !ok || !got.quantity.Equal(decimal.RequireFromString("2000")) {
		t.Errorf("duplicate buckets did not sum: got %v (ok=%t), want 2000", got.quantity, ok)
	}
}

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

	enrich, _ := enrichMonth(costBuckets, usageBuckets)
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

	enrich, _ := enrichMonth(costBuckets, usageBuckets)
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

	enrich, _ := enrichMonth(costBuckets, usageBuckets)
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

	enrich, _ := enrichMonth(costBuckets, usageBuckets)
	got, ok := enrich[rowKey{0, 0}]
	if !ok {
		t.Fatal("the non-null-geo cost row was not enriched")
	}
	if !got.quantity.Equal(decimal.RequireFromString("700000")) {
		t.Errorf("per-geo join quantity = %s, want 700000 (the us sum ONLY, not the geoless 1500000)", got.quantity)
	}
}
