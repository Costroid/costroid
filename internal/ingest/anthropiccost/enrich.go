// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package anthropiccost

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// The five cost-side token_type enum values (decision D33). They map 1:1 onto
// the five usage-report token quantities: three names are byte-identical, and
// the two cache-write values match the dotted usage path cache_creation.* so a
// cost row's token_type selects exactly one usage quantity.
const (
	ttUncachedInput = "uncached_input_tokens"
	ttCacheRead     = "cache_read_input_tokens"
	ttOutput        = "output_tokens"
	ttCacheWrite5m  = "cache_creation.ephemeral_5m_input_tokens"
	ttCacheWrite1h  = "cache_creation.ephemeral_1h_input_tokens"
)

// skuMeterByTokenType is the frozen Costroid SkuMeter naming (decision D33):
// a friendly meter label per token_type. A cost tokens row whose token_type is
// absent from this table is tolerated (never a crash) and left money-only.
var skuMeterByTokenType = map[string]string{
	ttUncachedInput: "Input Tokens",
	ttOutput:        "Output Tokens",
	ttCacheRead:     "Cache Read Tokens",
	ttCacheWrite5m:  "Cache Write Tokens (5m)",
	ttCacheWrite1h:  "Cache Write Tokens (1h)",
}

// usageBucket is one day's bucket of the usage_report/messages response.
type usageBucket struct {
	StartingAt string        `json:"starting_at"`
	EndingAt   string        `json:"ending_at"`
	Results    []usageResult `json:"results"`
}

// usageResult is one grouped usage row: the five cost-side join dims plus the
// five token quantities (two nested under cache_creation) and the web-search
// request COUNT. Token quantities are decoded as json.Number so their exact
// integer literal survives — NEVER through float64 (decisions D23, D25). Grouped
// dims can be null/empty (default workspace, Console usage); a JSON null decodes
// to "" and is tolerated. Cardinal Rule (D7): only aggregated counts are read —
// nothing content-shaped exists on this surface.
type usageResult struct {
	Model         string `json:"model"`
	WorkspaceID   string `json:"workspace_id"`
	ContextWindow string `json:"context_window"`
	InferenceGeo  string `json:"inference_geo"`
	ServiceTier   string `json:"service_tier"`

	Uncached      json.Number         `json:"uncached_input_tokens"`
	CacheRead     json.Number         `json:"cache_read_input_tokens"`
	Output        json.Number         `json:"output_tokens"`
	CacheCreation *usageCacheCreation `json:"cache_creation"`
	ServerToolUse *usageServerToolUse `json:"server_tool_use"`
}

type usageCacheCreation struct {
	Ephemeral5m json.Number `json:"ephemeral_5m_input_tokens"`
	Ephemeral1h json.Number `json:"ephemeral_1h_input_tokens"`
}

type usageServerToolUse struct {
	WebSearchRequests json.Number `json:"web_search_requests"`
}

// rowKey identifies one cost result by its position (bucket index, result index)
// so a precomputed enrichment can be attached to it without re-deriving keys.
type rowKey struct{ bi, ri int }

// joinKey is the daily-grain join key WITHOUT inference geo — the realistic
// match granularity, because cost_report's group_by allows only
// {description, workspace_id}, so cost-side inference_geo is null in production
// and matches usage summed across all geo values for the remaining key
// (decision D33). token_type is part of the key: the cost row already names it.
type joinKey struct {
	day, model, contextWindow, workspaceID, serviceTier, tokenType string
}

// geoKey is a joinKey plus a concrete inference geo, for the (rare) case a cost
// row carries a non-null geo — then it matches only that geo's usage.
type geoKey struct {
	joinKey
	geo string
}

// enrichment is the atomic, all-or-none decoration a unique cost/usage match
// adds to one cost tokens row (decision D33). The money columns are never part
// of it — enrichment leaves BilledCost byte-identical.
type enrichment struct {
	quantity   decimal.Decimal // token count → ConsumedQuantity
	skuID      string
	skuPriceID string
	skuMeter   string
	pricingQty decimal.Decimal // quantity ÷ 1,000,000, exact
}

// anomalySummary counts the per-period usage⇔cost reconciliation surfaces that
// are counted but NEVER emitted as FOCUS rows (decision D33): ambiguous
// collisions, cost-orphaned usage keys, priority/flex-tier usage, and
// web-search request counts. String renders one summary line (empty when there
// is nothing to report).
type anomalySummary struct {
	collisions        int
	collidedRows      int
	orphanUsageKeys   int
	tierOrphanRows    int
	webSearchRequests decimal.Decimal
}

func (s anomalySummary) empty() bool {
	return s.collisions == 0 && s.orphanUsageKeys == 0 &&
		s.tierOrphanRows == 0 && s.webSearchRequests.IsZero()
}

func (s anomalySummary) String() string {
	if s.empty() {
		return ""
	}
	var parts []string
	if s.collisions > 0 {
		parts = append(parts, fmt.Sprintf("%d ambiguous cost/usage collision(s) enriched none (%d cost row(s))",
			s.collisions, s.collidedRows))
	}
	if s.orphanUsageKeys > 0 {
		parts = append(parts, fmt.Sprintf("%d cost-orphaned usage key(s)", s.orphanUsageKeys))
	}
	if s.tierOrphanRows > 0 {
		parts = append(parts, fmt.Sprintf("%d priority/flex-tier usage row(s)", s.tierOrphanRows))
	}
	if !s.webSearchRequests.IsZero() {
		parts = append(parts, fmt.Sprintf("%s web-search request(s)", s.webSearchRequests.String()))
	}
	return "usage/cost reconciliation: " + strings.Join(parts, ", ") +
		" (usage-only surfaces are counted, never emitted as FOCUS rows — decision D33)"
}

// joinableTier reports whether a service tier carries money on cost_report and
// so can be joined; cost_report exposes money only for standard|batch, leaving
// priority/flex tiers structurally cost-orphaned (decision D33).
func joinableTier(t string) bool { return t == "standard" || t == "batch" }

// bucketDay reduces a bucket's RFC 3339 starting_at to its UTC calendar day
// ("2006-01-02"), the daily join grain shared by cost and usage. An
// unparseable bound yields "" and simply fails to match.
func bucketDay(s string) string {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return ""
	}
	return t.UTC().Format("2006-01-02")
}

// tokenPair is one (token_type, quantity) pair unpivoted from a usage row.
type tokenPair struct {
	tokenType string
	qty       decimal.Decimal
}

// tokenPairs unpivots one usage row's up-to-five token quantities, descending
// into the nested cache_creation object and skipping absent/empty values. Each
// quantity is built from its exact JSON literal (never float64).
func tokenPairs(ur usageResult) []tokenPair {
	var pairs []tokenPair
	add := func(tt string, num json.Number) {
		s := strings.TrimSpace(string(num))
		if s == "" {
			return
		}
		q, err := decimal.NewFromString(s)
		if err != nil {
			return
		}
		pairs = append(pairs, tokenPair{tt, q})
	}
	add(ttUncachedInput, ur.Uncached)
	add(ttCacheRead, ur.CacheRead)
	add(ttOutput, ur.Output)
	if ur.CacheCreation != nil {
		add(ttCacheWrite5m, ur.CacheCreation.Ephemeral5m)
		add(ttCacheWrite1h, ur.CacheCreation.Ephemeral1h)
	}
	return pairs
}

// enrichMonth joins one month's usage quantities onto its cost tokens rows
// (decision D33). It is a pure function of the fetched buckets — no I/O — so
// the join, collision, summed-geo, and orphan-counting rules are unit-testable.
//
// Steps:
//  1. Pre-aggregate usage per (day, model, context_window, workspace_id,
//     service_tier, token_type): duplicate buckets for one key (e.g. across
//     pages) SUM, never last-wins. A geoless sum and a per-geo sum are both
//     kept. Priority/flex-tier token rows and web-search counts are counted as
//     orphans and never aggregated. Only cost_type=="tokens" rows are join
//     candidates.
//  2. Group cost tokens rows by their match key. If MORE THAN ONE cost row
//     shares a match key, the usage is ambiguous → enrich NONE of them and
//     count the collision (never split/duplicate/guess).
//  3. A uniquely-matched cost row draws its quantity from the geoless sum (null
//     cost geo) or the per-geo sum (non-null cost geo) and, atomically, gains
//     the full enrichment set.
//  4. Standard/batch usage keys no cost tokens row referenced are cost-orphaned
//     and counted.
func enrichMonth(costBuckets []bucket, usageBuckets []usageBucket) (map[rowKey]enrichment, anomalySummary) {
	var summary anomalySummary
	agg := map[joinKey]decimal.Decimal{}
	aggByGeo := map[geoKey]decimal.Decimal{}

	for _, ub := range usageBuckets {
		day := bucketDay(ub.StartingAt)
		for _, ur := range ub.Results {
			if ur.ServerToolUse != nil && ur.ServerToolUse.WebSearchRequests != "" {
				if n, err := decimal.NewFromString(string(ur.ServerToolUse.WebSearchRequests)); err == nil {
					summary.webSearchRequests = summary.webSearchRequests.Add(n)
				}
			}
			pairs := tokenPairs(ur)
			if len(pairs) == 0 {
				continue
			}
			if !joinableTier(ur.ServiceTier) {
				// Priority/flex/unknown-tier tokens are structurally cost-
				// orphaned; count them and never aggregate or emit.
				summary.tierOrphanRows++
				continue
			}
			for _, p := range pairs {
				jk := joinKey{day, ur.Model, ur.ContextWindow, ur.WorkspaceID, ur.ServiceTier, p.tokenType}
				agg[jk] = agg[jk].Add(p.qty)
				gk := geoKey{jk, ur.InferenceGeo}
				aggByGeo[gk] = aggByGeo[gk].Add(p.qty)
			}
		}
	}

	type costRow struct {
		rk  rowKey
		jk  joinKey
		geo string
	}
	groups := map[geoKey][]costRow{} // keyed by match key (joinKey + geo selector)
	referenced := map[joinKey]bool{}

	for bi, b := range costBuckets {
		day := bucketDay(b.StartingAt)
		for ri, res := range b.Results {
			if res.CostType != "tokens" || res.TokenType == "" {
				continue // only tokens rows with a token_type can mint a SKU.
			}
			if _, ok := skuMeterByTokenType[res.TokenType]; !ok {
				continue // unknown token_type: tolerate, leave money-only.
			}
			if !joinableTier(res.ServiceTier) {
				continue // a tokens row on a non-joinable tier stays money-only.
			}
			jk := joinKey{day, res.Model, res.ContextWindow, res.WorkspaceID, res.ServiceTier, res.TokenType}
			referenced[jk] = true
			mk := geoKey{jk, res.InferenceGeo}
			groups[mk] = append(groups[mk], costRow{rowKey{bi, ri}, jk, res.InferenceGeo})
		}
	}

	enrich := map[rowKey]enrichment{}
	for _, rows := range groups {
		if len(rows) > 1 {
			summary.collisions++
			summary.collidedRows += len(rows)
			continue // ambiguous → enrich NONE.
		}
		cr := rows[0]
		var (
			qty   decimal.Decimal
			found bool
		)
		if cr.geo == "" {
			qty, found = agg[cr.jk]
		} else {
			qty, found = aggByGeo[geoKey{cr.jk, cr.geo}]
		}
		if !found {
			continue // a tokens row with no matching usage stays money-only.
		}
		sku := "anthropic/" + cr.jk.model + "/" + cr.jk.tokenType + "/" + cr.jk.contextWindow
		enrich[cr.rk] = enrichment{
			quantity:   qty,
			skuID:      sku,
			skuPriceID: sku + "/" + cr.jk.serviceTier,
			skuMeter:   skuMeterByTokenType[cr.jk.tokenType],
			pricingQty: qty.Shift(-6),
		}
	}

	for jk := range agg {
		if !referenced[jk] {
			summary.orphanUsageKeys++
		}
	}
	return enrich, summary
}
