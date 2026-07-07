// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package openaicost implements the "openai-cost" connector (decisions D7,
// D16, D17, D29, D31): it ingests aggregated cost metadata from the OpenAI
// Admin costs API and synthesizes FOCUS-1.4-shaped records. It shares the
// anthropic-cost skeleton (monthly periods, up-front paged fetch,
// data-elements-only ContentHash, credential slot, --base-url, hygiene) and
// differs only where the OpenAI API differs.
//
// # Endpoints & auth
//
// GET https://api.openai.com/v1/organization/costs is the monetary endpoint;
// the ten read-only usage endpoints under /v1/organization/usage/<name>
// (completions, embeddings, moderations, images, audio_speeches,
// audio_transcriptions, code_interpreter_sessions, vector_stores,
// web_search_calls, file_search_calls) surface non-token usage counts and byte
// totals into the separate usage_metrics store (the usage fetch-more decision;
// see usageEndpoints). All are authenticated with the SAME OpenAI ADMIN API key sent
// as Authorization: Bearer and share the SAME "Usage" read permission — costs is
// a method under the usage resource, so NO separate credential or scope is
// needed. The OpenAI dashboard supports RESTRICTED admin keys
// (per-resource None/Read/Write, enforced server-side); operators should create
// a Restricted key and verify at creation the narrowest scope that still reads
// the usage resource (first-real-account check). Costroid's encrypted credential
// store (decision D32) still carries the least-privilege burden. The Bearer key
// never appears in any log or error; request URLs (with query strings) and
// request headers are never logged.
//
// # Cardinal Rule (decision D7)
//
// Only aggregated cost and usage metadata is fetched — amounts, currency, day
// buckets, line-item labels, project id, the usage endpoints' non-token counts,
// vector-store byte totals, and search-call counts with the minimal model
// grouping documented below. No prompt/response content, ever. The Bearer key
// never appears in any log or error; request URLs (with query strings) and
// request headers are never logged.
//
// # Periods, window, idempotency, ContentHash, --force
//
// Identical to anthropic-cost: one UTC calendar month per period; default
// window = last 12 months; --since / --period; SourceIdentity =
// "api.openai.com/<credential-slot>/<YYYY-MM>"; per-period replace (D26a);
// every month runs including empty ones; ContentHash = "sha256:<hex>" over
// the concatenated raw bytes of the response data elements in fetch order,
// excluding the has_more/next_page envelope; --force is a documented no-op
// beyond re-fetching (no SyncState is kept). The provider host in the
// identity is a fixed constant, not the --base-url host.
//
// # Money — dollars via the JSON literal, never float64
//
// amount.value is a JSON NUMBER denominated in DOLLARS (e.g. 0.06), and
// amount.currency is lowercase "usd". The value is decoded as a raw JSON
// literal (json.RawMessage) and the decimal is built from that text — never
// through float64 (decisions D23, D25) — so a value like 123.4567890123456789
// survives exactly.
//
// # bucket_width & lookback
//
// bucket_width=1d is currently the only value; it is sent explicitly. Any
// bucket the API returns that is not a well-formed day interval degrades its
// MONTH to a per-period error rather than crashing the run, and a month the
// API refuses (e.g. beyond an undocumented lookback horizon) likewise
// degrades to a per-period error while the other months proceed.
//
// # FOCUS mapping (rules OAI-1…, all validation-mandatory columns non-null)
//
//	rule    FOCUS column(s)                 value
//	-----   ------------------------------  ---------------------------------
//	OAI-1   ChargePeriodStart / End         bucket start_time / end_time
//	                                         (Unix seconds, a UTC day) → RFC 3339.
//	OAI-2   BillingPeriodStart / End        the enclosing UTC calendar month.
//	OAI-3   BilledCost = EffectiveCost      = ListCost = ContractedCost =
//	                                         amount.value dollars (from the
//	                                         JSON literal). No list-vs-
//	                                         negotiated distinction is exposed.
//	OAI-4   BillingCurrency                 amount.currency uppercased. A
//	                                         missing currency FAILS the period
//	                                         (decision D23 — never assume USD).
//	OAI-5   BillingAccountId                "api.openai.com/<slot>"
//	                                         (Costroid synthesis — documented).
//	OAI-6   ServiceProviderName             "OpenAI" (= InvoiceIssuerName).
//	        InvoiceIssuerName
//	OAI-7   ServiceName / ServiceCategory   "OpenAI API" / "AI and Machine
//	                                         Learning".
//	OAI-8   ChargeCategory / ChargeFrequency "Usage" / "Usage-Based";
//	                                         ChargeClass null; negative amounts
//	                                         pass through unchanged.
//	OAI-9   ChargeDescription               line_item when present.
//	OAI-10  SubAccountId                    project_id when grouped by project,
//	                                         else null.
//	OAI-11  ConsumedQuantity / ConsumedUnit The costs endpoint returns a nullable
//	        SkuId / SkuPriceId / SkuMeter    `quantity` field (the token count of
//	        PricingQuantity / PricingUnit    the grouped line, decoded from its
//	                                         JSON literal — never float64). SAME
//	                                         ROW, no join. When quantity is
//	                                         non-null AND the line_item ends in a
//	                                         documented direction suffix
//	                                         (", input" / ", output" /
//	                                         ", cached input"), the row gains,
//	                                         atomically: ConsumedQuantity = the
//	                                         count; ConsumedUnit = "Tokens";
//	                                         SkuId = "openai/" + line_item VERBATIM
//	                                         (line_item is already an opaque key
//	                                         and input/output are distinct SKUs, so
//	                                         the suffix stays IN — FROZEN
//	                                         convention, decision D33);
//	                                         SkuPriceId = SkuId; SkuMeter = the
//	                                         friendly meter; PricingQuantity =
//	                                         quantity ÷ 1,000,000 by exact shift;
//	                                         PricingUnit = "1000000 Tokens". Money
//	                                         columns stay byte-identical (money
//	                                         invariance, decision D33).
//	OAI-12  orphans & tolerance             A null/absent-quantity row is the
//	                                         NORMAL money-only case (credits,
//	                                         refunds, non-token line items): no SKU
//	                                         columns, and it is NOT an anomaly, so
//	                                         it is NOT counted. Only a row that DOES
//	                                         carry a quantity but cannot be priced is
//	                                         counted in the per-period anomaly
//	                                         summary: (a) an unknown/call-fee unit
//	                                         (e.g. web search) whose line_item has no
//	                                         documented direction suffix, and (b) a
//	                                         recognized direction whose quantity
//	                                         literal is malformed (e.g. a JSON
//	                                         string) — it degrades to money-only and
//	                                         is counted. That is the deliberate
//	                                         ASYMMETRY with a malformed AMOUNT, which
//	                                         fails the whole period (OAI-4/D23). A
//	                                         unit is NEVER guessed; a non-"Tokens"
//	                                         ConsumedUnit is never emitted.
//
// line_item is an opaque, undocumented display string; a model name is NEVER
// parsed out of it into any column. ListUnitPrice/ContractedUnitPrice stay null
// on enriched rows (documented deviation, decision D33 — unit prices need vendor
// price lists, a later slice). ContentHash needs NO change: quantity arrives
// inside the same cost data payload already hashed, so a quantity-only
// restatement changes the hashed bytes and supersedes normally.
package openaicost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/credentials"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/ingest"
	"github.com/Costroid/costroid/internal/ingest/aiconn"
	"github.com/Costroid/costroid/internal/storage"
)

// Name is the connector's registry name and default credential slot name.
const Name = "openai-cost"

// DefaultBaseURL is the production API root; --base-url overrides it.
const DefaultBaseURL = "https://api.openai.com"

// providerHost is the FIXED host used in SourceIdentity and BillingAccountId.
const providerHost = "api.openai.com"

// costsPath is the endpoint path (never echoed in error messages).
const costsPath = "/v1/organization/costs"

// usagePathPrefix is the shared prefix of the ten read-only usage endpoints
// (usage/<name>); the <name> segment is appended per endpoint. These share the
// SAME Admin key and "Usage" read permission as costsPath — costs is a method
// under the usage resource — so no separate credential or scope is needed.
const usagePathPrefix = "/v1/organization/usage/"

// usageLimit is the requested per-endpoint usage-page bucket size. A 1d month
// has at most 31 buckets, and the usage endpoints CAP limit at 31 for 1d — the
// costs endpoint's 180 (pageLimit) would 400 on a real usage account. The fake
// paginates independently to exercise the cursor path.
const usageLimit = "31"

// serviceName is the FOCUS ServiceName every OpenAI cost row carries (OAI-7),
// and the same value its cost-orphaned usage metrics carry. A single const both
// paths reference so the two can never drift (slice-8 review).
const serviceName = "OpenAI API"

const (
	maxBodyBytes  = 1 << 20
	max429Retries = 5
	// pageLimit is the requested time-bucket page size (API max 180). A
	// whole month fits in one page; the fake paginates independently to
	// exercise the cursor path.
	pageLimit = 180
)

// Period is one discovered billing period (one UTC calendar month).
type Period struct {
	Month string
	Conn  *Connector
	Err   error
}

// Discover fetches every month in the window once up front, caches the
// payloads, and returns one Period per month oldest-first. A month whose
// fetch fails degrades to Period.Err. secret is the Admin API key, loaded
// from the credential store by the caller before any network dial.
func Discover(ctx context.Context, client *http.Client, baseURL, slot string, secret credentials.Secret, since, period string) ([]Period, error) {
	if err := aiconn.ValidateBaseURL(baseURL, DefaultBaseURL); err != nil {
		return nil, err
	}
	months, err := aiconn.Window(since, period, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = http.DefaultClient
	}
	base := strings.TrimSuffix(baseURL, "/")

	out := make([]Period, 0, len(months))
	for _, month := range months {
		conn, err := fetchMonth(ctx, client, base, secret.Reveal(), slot, month)
		if err != nil {
			out = append(out, Period{Month: month, Err: err})
			continue
		}
		out = append(out, Period{Month: month, Conn: conn})
	}
	return out, nil
}

// fetchMonth fetches one month's costs (which degrade the whole month on
// failure) AND — orthogonally — the ten read-only usage endpoints, whose
// non-token counts and byte totals are surfaced into usage_metrics alongside
// the OAI-12 USG-3 cost-orphans. A usage-endpoint failure never blocks cost
// ingest: it records the failing endpoint and returns usageMetrics=nil so the
// driver SKIPS the usage write and the prior rows survive. ContentHash stays
// COST-ONLY — usage is rewritten unconditionally, so no hash coverage is needed.
func fetchMonth(ctx context.Context, client *http.Client, base, apiKey, slot, month string) (*Connector, error) {
	start, end, err := aiconn.MonthBounds(month)
	if err != nil {
		return nil, err
	}
	rawBuckets, buckets, err := fetchCost(ctx, client, base, apiKey, month, start, end)
	if err != nil {
		return nil, err // a cost failure degrades the whole month (Period.Err).
	}
	summary := openaiAnomalies(buckets)
	// The USG-3 cost-orphans are ALWAYS computed; the ten usage endpoints'
	// metrics are APPENDED to the SAME slice — the USG-3 rows MUST remain.
	usageMetrics := openaiUsageMetrics(buckets)
	endpointMetrics, failedEndpoint := fetchUsage(ctx, client, base, apiKey, month, start, end)
	if failedEndpoint != "" {
		// Usage is orthogonal to cost: record the failure and skip the usage
		// write entirely (usageMetrics=nil). A partial USG-3-only slice is
		// FORBIDDEN — ReplaceUsageBatch is a whole-batch replace, so it would
		// WIPE the prior 7-endpoint rows.
		summary.usageFetchFailed = failedEndpoint
		usageMetrics = nil
	} else {
		usageMetrics = append(usageMetrics, endpointMetrics...)
	}
	return &Connector{
		slot:         slot,
		month:        month,
		monthStart:   start,
		monthEnd:     end,
		buckets:      buckets,
		summary:      summary,
		usageMetrics: usageMetrics,
		contentHash:  contentHash(rawBuckets), // COST-ONLY — usage is never hashed.
	}, nil
}

// fetchCost pages through one month's cost report, returning the raw data-
// element bytes (for the cost-only ContentHash) and the parsed buckets.
func fetchCost(ctx context.Context, client *http.Client, base, apiKey, month string, start, end time.Time) ([][]byte, []bucket, error) {
	var (
		rawBuckets [][]byte
		buckets    []bucket
		page       string
	)
	for {
		q := url.Values{}
		q.Set("start_time", strconv.FormatInt(start.Unix(), 10))
		q.Set("end_time", strconv.FormatInt(end.Unix(), 10))
		q.Set("bucket_width", "1d")
		// OpenAI's OpenAPI spec documents a bare, repeated group_by= (NOT the
		// bracketed group_by[]= Anthropic uses); url.Values.Encode emits it
		// bare, which is exactly what this endpoint wants.
		q["group_by"] = []string{"project_id", "line_item"} // finest documented combination
		q.Set("limit", strconv.Itoa(pageLimit))
		if page != "" {
			q.Set("page", page)
		}
		body, err := doGet(ctx, client, base+costsPath+"?"+q.Encode(), apiKey, month, "costs")
		if err != nil {
			return nil, nil, err
		}
		var resp costsPage
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, nil, fmt.Errorf("openai-cost %s: decoding costs response: %w", month, err)
		}
		for _, raw := range resp.Data {
			rawBuckets = append(rawBuckets, raw)
			var b bucket
			if err := json.Unmarshal(raw, &b); err != nil {
				return nil, nil, fmt.Errorf("openai-cost %s: decoding cost bucket: %w", month, err)
			}
			buckets = append(buckets, b)
		}
		if !resp.HasMore || resp.NextPage == "" {
			break
		}
		page = resp.NextPage
	}
	return rawBuckets, buckets, nil
}

// fetchUsage fetches the ten read-only usage endpoints for one month and
// returns their surfaced usage metrics, appended in fetch order. Usage is
// ORTHOGONAL to cost: it flows only into usage_metrics and never blocks cost
// ingest, so the FIRST endpoint failure aborts usage for the month (all-or-
// nothing — ReplaceUsageBatch is a whole-batch replace keyed on the month, so a
// partial slice would WIPE the prior rows) and returns the failing endpoint name
// instead of an error. A clean run returns (metrics, "").
func fetchUsage(ctx context.Context, client *http.Client, base, apiKey, month string, start, end time.Time) (metrics []storage.Metric, failedEndpoint string) {
	for _, ep := range usageEndpoints {
		epMetrics, err := fetchUsageEndpoint(ctx, client, base, apiKey, month, start, end, ep)
		if err != nil {
			return nil, ep.name
		}
		metrics = append(metrics, epMetrics...)
	}
	return metrics, ""
}

// fetchUsageEndpoint pages through one month of one usage endpoint, surfacing
// each bucket's whitelisted counts (usageBucketMetrics). It follows
// has_more/next_page exactly like the cost fetch.
func fetchUsageEndpoint(ctx context.Context, client *http.Client, base, apiKey, month string, start, end time.Time, ep usageEndpoint) ([]storage.Metric, error) {
	var (
		metrics []storage.Metric
		page    string
	)
	for {
		q := url.Values{}
		q.Set("start_time", strconv.FormatInt(start.Unix(), 10))
		q.Set("end_time", strconv.FormatInt(end.Unix(), 10))
		q.Set("bucket_width", "1d")
		q.Set("limit", usageLimit)
		if ep.groupByModel {
			// A bare, repeated group_by= (never bracketed), exactly one dim: model.
			// Model-less endpoints send NO group_by.
			q["group_by"] = []string{"model"}
		}
		if page != "" {
			q.Set("page", page)
		}
		body, err := doGet(ctx, client, base+usagePathPrefix+ep.name+"?"+q.Encode(), apiKey, month, "usage endpoint "+ep.name)
		if err != nil {
			return nil, err
		}
		var resp usagePage
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("openai-cost %s: decoding usage response: %w", month, err)
		}
		for _, raw := range resp.Data {
			var b usageBucket
			if err := json.Unmarshal(raw, &b); err != nil {
				return nil, fmt.Errorf("openai-cost %s: decoding usage bucket: %w", month, err)
			}
			metrics = append(metrics, usageBucketMetrics(b, ep)...)
		}
		if !resp.HasMore || resp.NextPage == "" {
			break
		}
		page = resp.NextPage
	}
	return metrics, nil
}

// usageBucketMetrics surfaces one usage bucket's whitelisted counts as
// storage.Metric rows for endpoint ep. Each emitted count/byte total is built
// from its raw JSON literal via decimal.NewFromString — never float64 (decisions
// D23/D25). A field that is absent/null or whose literal is not a valid decimal
// is SKIPPED for that field on that row (no zero-fabrication, no garbage) while
// the row's other valid fields still emit — the per-field degrade. ServiceName
// is the row's model, falling back to "OpenAI API" when the model is
// empty/absent and for the model-less endpoints. ServiceTier is always "" (this
// slice does not group by service_tier — Cardinal-Rule minimal dims), bound as
// "" and never SQL NULL. ChargePeriodStart is the UTC-midnight of the bucket day
// (mirroring openaiUsageMetrics). A token count is NEVER emitted — the closed
// whitelist usageResult has no token field.
func usageBucketMetrics(b usageBucket, ep usageEndpoint) []storage.Metric {
	t := time.Unix(b.StartTime, 0).UTC()
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	var metrics []storage.Metric
	for _, res := range b.Results {
		svc := res.Model
		if svc == "" {
			svc = serviceName // "OpenAI API" fallback (empty model / no model dim).
		}
		for _, e := range ep.emits {
			lit := strings.TrimSpace(string(e.field(res)))
			if lit == "" || lit == "null" {
				continue // absent/null count: skip this field, never fabricate a zero.
			}
			d, err := decimal.NewFromString(lit)
			if err != nil {
				continue // malformed literal: cannot store; skip this field only.
			}
			metrics = append(metrics, storage.Metric{
				ChargePeriodStart: day,
				ServiceName:       svc,
				ServiceTier:       "",
				MetricName:        e.metricName,
				Unit:              e.unit,
				Quantity:          d,
			})
		}
	}
	return metrics
}

// openaiUsageMetrics surfaces the cost-orphaned usage metrics OAI-12 leaves
// money-only: each recognized-but-unpriced (unknown-unit) quantity-bearing row —
// the ones openaiAnomalies counts and synthesize value-drops. Each becomes a
// metric with unit "Unknown" (USG-3: a deliberate NON-assertion — never guess
// "Tokens", never fabricate a unit) and metric_name = the line_item VERBATIM (an
// opaque billing descriptor, Cardinal-Rule safe — a model name is never parsed
// out of it). service_name is the SAME "OpenAI API" the cost record carries and
// service_tier is "" (OpenAI exposes no tier). A null/absent-quantity row is the
// normal money-only case (not an orphan), and a recognized-direction row is
// enriched onto its cost record, not orphaned. A quantity whose literal is NOT a
// valid decimal cannot be stored in the DECIMAL column, so it stays money-only
// (counted, never emitted) — the same posture as the recognized-direction
// malformed case. Returns a non-nil slice (empty when the month has no orphans).
func openaiUsageMetrics(buckets []bucket) []storage.Metric {
	metrics := []storage.Metric{}
	for _, b := range buckets {
		t := time.Unix(b.StartTime, 0).UTC()
		day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		for _, res := range b.Results {
			qty := quantityLiteral(res)
			if qty == "" {
				continue // null/absent quantity: normal money-only, not an orphan.
			}
			if _, ok := openaiSkuMeter(res.LineItem); ok {
				continue // a recognized direction is enriched onto its cost row.
			}
			d, err := decimal.NewFromString(qty)
			if err != nil {
				continue // malformed literal: money-only, cannot store — never emit garbage.
			}
			metrics = append(metrics, storage.Metric{
				ChargePeriodStart: day,
				ServiceName:       serviceName,
				ServiceTier:       "",
				MetricName:        res.LineItem,
				Unit:              "Unknown",
				Quantity:          d,
			})
		}
	}
	return metrics
}

// doGet issues one GET with the Bearer auth header and bounded 429 retries.
// what names the endpoint (e.g. "costs", "usage endpoint completions") for
// accurate error messages. It NEVER logs or echoes the api key, request headers,
// or the request URL (which would carry the query string).
func doGet(ctx context.Context, client *http.Client, requestURL, apiKey, month, what string) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
		if err != nil {
			return nil, fmt.Errorf("openai-cost %s: building request: %w", month, err)
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("openai-cost %s: requesting %s failed: %s", month, what, scrubTransportErr(err))
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		_ = resp.Body.Close()

		switch {
		case resp.StatusCode == http.StatusOK:
			if readErr != nil {
				return nil, fmt.Errorf("openai-cost %s: reading the %s body: %w", month, what, readErr)
			}
			return body, nil
		case resp.StatusCode == http.StatusTooManyRequests && attempt < max429Retries:
			if err := waitRetryAfter(ctx, resp.Header.Get("Retry-After")); err != nil {
				return nil, err
			}
			continue
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return nil, fmt.Errorf("openai-cost %s: the OpenAI Admin API key was rejected (HTTP %d) — check the "+
				"credential slot holds a valid admin key whose Restricted scope can read the usage resource "+
				"(costs and usage share one 'Usage' read permission): %s",
				month, resp.StatusCode, truncateBody(body))
		default:
			return nil, fmt.Errorf("openai-cost %s: %s request failed (HTTP %d) — if the window predates the "+
				"available history, ingest a more recent --since/--period: %s", month, what, resp.StatusCode, truncateBody(body))
		}
	}
}

// waitRetryAfter honors a Retry-After header — either delta-seconds or an
// RFC 1123 HTTP-date (both forms the spec permits) — bounded to a sane
// maximum, or a short default when the header is absent or unparseable. A
// date already in the past yields a zero wait (retry immediately).
func waitRetryAfter(ctx context.Context, header string) error {
	wait := retryAfterDelay(header)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

// retryAfterDelay parses a Retry-After header into a bounded wait duration.
func retryAfterDelay(header string) time.Duration {
	const maxWait = 60 * time.Second
	if header == "" {
		return 2 * time.Second
	}
	if secs, err := time.ParseDuration(header + "s"); err == nil && secs > 0 {
		return min(secs, maxWait)
	}
	if t, err := http.ParseTime(header); err == nil {
		return min(max(time.Until(t), 0), maxWait)
	}
	return 2 * time.Second
}

// costsPage is the response envelope (object="page"); Data elements are kept
// raw for ContentHash.
type costsPage struct {
	Data     []json.RawMessage `json:"data"`
	HasMore  bool              `json:"has_more"`
	NextPage string            `json:"next_page"` // null → ""
}

// bucket is one day's cost bucket (object="bucket"); times are Unix seconds.
type bucket struct {
	StartTime int64    `json:"start_time"`
	EndTime   int64    `json:"end_time"`
	Results   []result `json:"results"`
}

// result is one cost line. Only cost metadata is read (Cardinal Rule).
// Quantity is the nullable token count of the grouped line (OAI-11), kept as a
// raw JSON literal so the exact decimal survives (never through float64).
type result struct {
	Amount    amount          `json:"amount"`
	LineItem  string          `json:"line_item"`
	ProjectID string          `json:"project_id"`
	Quantity  json.RawMessage `json:"quantity"`
}

// usagePage is the usage endpoints' response envelope (object="page"). The
// pagination cursor is followed but NEVER hashed: usage is orthogonal to cost
// and flows only into usage_metrics (rewritten unconditionally), so ContentHash
// stays cost-only (see the package doc).
type usagePage struct {
	Data     []json.RawMessage `json:"data"`
	HasMore  bool              `json:"has_more"`
	NextPage string            `json:"next_page"` // null → ""
}

// usageBucket is one day's usage bucket (object="bucket"); start_time is Unix
// seconds (a UTC day), mirroring the cost bucket.
type usageBucket struct {
	StartTime int64         `json:"start_time"`
	Results   []usageResult `json:"results"`
}

// usageResult holds ONLY the whitelisted, emittable count fields plus the model
// dimension (Cardinal Rule D7). This is a CLOSED WHITELIST by construction
// (mirroring anthropiccost/enrich.go's usageResult): token fields
// (input/output/cached/audio tokens) are deliberately ABSENT from the struct, so
// a token count is structurally UN-emittable — token double-count is impossible,
// not merely blacklisted. Every count is a raw JSON literal (never json.Number):
// a json.Number would fail the WHOLE bucket decode on one malformed count
// literal, defeating the mandated per-field degrade; a json.RawMessage lets the
// bucket decode succeed so a bad field skips per-field while its siblings emit.
type usageResult struct {
	Model            string          `json:"model"`
	NumModelRequests json.RawMessage `json:"num_model_requests"`
	UsageBytes       json.RawMessage `json:"usage_bytes"`
	NumRequests      json.RawMessage `json:"num_requests"`
	Images           json.RawMessage `json:"images"`
	Characters       json.RawMessage `json:"characters"`
	Seconds          json.RawMessage `json:"seconds"`
	NumSessions      json.RawMessage `json:"num_sessions"`
}

// usageEmit names one emittable count/byte field: the metric_name it surfaces,
// its frozen unit, and the extractor pulling the raw literal from a result. Most
// metric names are the upstream field verbatim; the two search-call `num_requests`
// fields are source-qualified because usage_metrics has no endpoint dimension.
// Keeping the emit list per-endpoint (not a blanket struct walk) means an
// endpoint surfaces ONLY its documented counts — e.g. completions can never emit
// an `images` count even if a payload carried one.
type usageEmit struct {
	metricName string
	unit       string
	field      func(usageResult) json.RawMessage
}

var (
	emitRequests           = usageEmit{"num_model_requests", "Requests", func(r usageResult) json.RawMessage { return r.NumModelRequests }}
	emitImages             = usageEmit{"images", "Images", func(r usageResult) json.RawMessage { return r.Images }}
	emitCharacters         = usageEmit{"characters", "Characters", func(r usageResult) json.RawMessage { return r.Characters }}
	emitSeconds            = usageEmit{"seconds", "Seconds", func(r usageResult) json.RawMessage { return r.Seconds }}
	emitSessions           = usageEmit{"num_sessions", "Sessions", func(r usageResult) json.RawMessage { return r.NumSessions }}
	emitBytes              = usageEmit{"usage_bytes", "Bytes", func(r usageResult) json.RawMessage { return r.UsageBytes }}
	emitWebSearchRequests  = usageEmit{"web_search_num_requests", "Calls", func(r usageResult) json.RawMessage { return r.NumRequests }}
	emitFileSearchRequests = usageEmit{"file_search_num_requests", "Calls", func(r usageResult) json.RawMessage { return r.NumRequests }}
)

// usageEndpoint describes one read-only usage endpoint. groupByModel sends the
// SINGLE Cardinal-Rule-safe dim group_by=model (the model endpoints); model-less
// endpoints send NO group_by. emits is the closed set of counts the endpoint
// surfaces.
type usageEndpoint struct {
	name         string // the path's last segment, VERBATIM (also the fixture token)
	groupByModel bool
	emits        []usageEmit
}

// usageEndpoints is the frozen extract table for the ten read-only usage
// endpoints. It surfaces NON-TOKEN usage counts and byte totals into
// usage_metrics; a token count is NEVER emitted (usageResult has no token field).
// Frozen mapping (metric_name is upstream-verbatim except the source-qualified
// search-call metrics; unit is the frozen vocabulary):
//
//	endpoint                    group_by  metric_name → unit
//	--------------------------  --------  --------------------------------------
//	completions                 model     num_model_requests → Requests
//	embeddings                  model     num_model_requests → Requests
//	moderations                 model     num_model_requests → Requests
//	images                      model     num_model_requests → Requests;
//	                                      images → Images
//	audio_speeches              model     num_model_requests → Requests;
//	                                      characters → Characters
//	audio_transcriptions        model     num_model_requests → Requests;
//	                                      seconds → Seconds
//	code_interpreter_sessions   (none)    num_sessions → Sessions
//	vector_stores               (none)    usage_bytes → Bytes
//	web_search_calls            model     web_search_num_requests → Calls
//	file_search_calls           (none)    file_search_num_requests → Calls
//
// ServiceName = the result row's model, falling back to "OpenAI API" when the
// model is empty/absent and (fixed) for the model-less endpoints. ServiceTier =
// "" — the completions endpoint exposes a service_tier group_by dim but this
// slice deliberately does NOT group by it (Cardinal-Rule minimal dims), so every
// row's tier is the empty string, never SQL NULL. group_by is ONLY `model` (bare,
// repeated) or ABSENT — never user_id/api_key_id/project_id/service_tier/
// vector_store_id/context_level (Cardinal Rule D7); /projects and /users are
// never called and no opaque ID is resolved to a name. num_model_requests is
// verbatim across the six model endpoints that emit it, so a model appearing on
// two endpoints on one day SUM-merges into one (day, model, num_model_requests,
// Requests) total in DailyUsageMetrics (no endpoint dimension exists) — an
// INTENDED, accepted merge under the frozen usage_metrics schema. Web search
// decodes but does NOT emit its num_model_requests field: a web-search-invoking
// request is also counted by the completions endpoint, so emitting it here would
// double-count model requests. The two search endpoints both expose upstream
// num_requests for different things; their metric names are therefore
// endpoint-qualified so web-search and file-search calls never SUM-merge just
// because a ServiceName collides. vector_stores usage_bytes is a daily storage
// stock: one day is exact, while summing Bytes across days yields byte-days (the
// billing basis), not current storage.
var usageEndpoints = []usageEndpoint{
	{name: "completions", groupByModel: true, emits: []usageEmit{emitRequests}},
	{name: "embeddings", groupByModel: true, emits: []usageEmit{emitRequests}},
	{name: "moderations", groupByModel: true, emits: []usageEmit{emitRequests}},
	{name: "images", groupByModel: true, emits: []usageEmit{emitRequests, emitImages}},
	{name: "audio_speeches", groupByModel: true, emits: []usageEmit{emitRequests, emitCharacters}},
	{name: "audio_transcriptions", groupByModel: true, emits: []usageEmit{emitRequests, emitSeconds}},
	{name: "code_interpreter_sessions", groupByModel: false, emits: []usageEmit{emitSessions}},
	{name: "vector_stores", groupByModel: false, emits: []usageEmit{emitBytes}},
	{name: "web_search_calls", groupByModel: true, emits: []usageEmit{emitWebSearchRequests}},
	{name: "file_search_calls", groupByModel: false, emits: []usageEmit{emitFileSearchRequests}},
}

// openaiSkuMeter maps a line_item's documented trailing direction suffix to a
// SkuMeter, reporting ok=false when no direction suffix is recognized. The
// ", cached input" case is checked before ", input" (the latter is not a suffix
// of the former, but the order documents intent). A unit is never guessed
// (decision D33): an unrecognized line_item leaves the row money-only.
func openaiSkuMeter(lineItem string) (meter string, ok bool) {
	switch {
	case strings.HasSuffix(lineItem, ", cached input"):
		return "Cache Read Tokens", true
	case strings.HasSuffix(lineItem, ", output"):
		return "Output Tokens", true
	case strings.HasSuffix(lineItem, ", input"):
		return "Input Tokens", true
	default:
		return "", false
	}
}

// quantityLiteral returns the row's non-null quantity literal, or "" when the
// quantity field is absent or JSON null.
func quantityLiteral(res result) string {
	s := strings.TrimSpace(string(res.Quantity))
	if s == "" || s == "null" {
		return ""
	}
	return s
}

// anomalySummary counts the per-period surfaces OAI-12 leaves money-only. Both
// counters are over QUANTITY-BEARING rows only (a null/absent-quantity row is
// the normal money-only case — credits, refunds, non-token line items — and is
// NOT an anomaly): unknownUnitRows is a quantity whose line_item carries no
// documented direction suffix; malformedQuantityRows is a recognized direction
// whose quantity literal is not a valid decimal. String renders one summary
// line (empty when there is nothing to report).
type anomalySummary struct {
	unknownUnitRows       int
	malformedQuantityRows int
	// usageFetchFailed, when non-empty, names the usage endpoint whose fetch
	// failed this run. Usage is orthogonal to cost: the cost still
	// ingested and the prior usage_metrics rows were preserved (the write was
	// skipped), so this is surfaced — never silent — but is not a cost anomaly.
	usageFetchFailed string
}

func (s anomalySummary) String() string {
	var lines []string
	if s.unknownUnitRows > 0 || s.malformedQuantityRows > 0 {
		var parts []string
		if s.unknownUnitRows > 0 {
			parts = append(parts, fmt.Sprintf("%d cost row(s) carry a quantity whose line_item unit "+
				"could not be safely derived", s.unknownUnitRows))
		}
		if s.malformedQuantityRows > 0 {
			parts = append(parts, fmt.Sprintf("%d cost row(s) carry a malformed quantity literal "+
				"(enrichment stripped; money kept)", s.malformedQuantityRows))
		}
		lines = append(lines, "usage/cost reconciliation: "+strings.Join(parts, ", ")+
			"; left unpriced (a unit is never guessed, a quantity is never repaired — decision D33)")
	}
	if s.usageFetchFailed != "" {
		lines = append(lines, fmt.Sprintf("usage endpoint %q fetch failed; usage_metrics not refreshed this run "+
			"— prior rows preserved, cost ingested", s.usageFetchFailed))
	}
	return strings.Join(lines, "; ")
}

// openaiAnomalies counts the OAI-12 orphans over a month's cost buckets so the
// summary can be reported at Discover time (before any record is read). Only a
// row that DOES carry a quantity but cannot be priced is counted: a null/absent
// quantity is the normal money-only case, never an anomaly (OAI-12).
func openaiAnomalies(buckets []bucket) anomalySummary {
	var s anomalySummary
	for _, b := range buckets {
		for _, res := range b.Results {
			qty := quantityLiteral(res)
			if qty == "" {
				continue // null/absent quantity: normal money-only, not an anomaly.
			}
			if _, ok := openaiSkuMeter(res.LineItem); !ok {
				s.unknownUnitRows++
				continue
			}
			// Recognized direction but a quantity that is not a valid decimal:
			// synthesize degrades it to money-only (a malformed AMOUNT fails the
			// whole period per D23; a malformed QUANTITY only strips enrichment —
			// the deliberate asymmetry). Count it rather than swallow it silently.
			if _, err := decimal.NewFromString(qty); err != nil {
				s.malformedQuantityRows++
			}
		}
	}
	return s
}

// amount is the money object; Value is kept as a raw JSON literal so the
// exact decimal survives (never through float64).
type amount struct {
	Value    json.RawMessage `json:"value"`
	Currency string          `json:"currency"`
}

func contentHash(rawBuckets [][]byte) string {
	h := sha256.New()
	for _, b := range rawBuckets {
		_, _ = h.Write(b)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// Connector reads one billing month of the OpenAI costs report, enriches each
// cost row from its same-row quantity (OAI-11), and exposes the month's separate
// usage_metrics rows from OAI-12 cost orphans plus the read-only usage endpoints.
type Connector struct {
	slot         string
	month        string
	monthStart   time.Time
	monthEnd     time.Time
	buckets      []bucket
	summary      anomalySummary
	usageMetrics []storage.Metric
	contentHash  string
}

var _ ingest.Connector = (*Connector)(nil)

func (c *Connector) Name() string { return Name }

func (c *Connector) FOCUSVersion() focus.Version { return focus.V1_4 }

// Month returns the connector's billing month ("YYYY-MM").
func (c *Connector) Month() string { return c.month }

// AnomalySummary returns one line summarizing this month's OAI-12 orphans
// (quantity-bearing rows whose unit could not be derived), or "" when there is
// nothing to report.
func (c *Connector) AnomalySummary() string { return c.summary.String() }

// UsageMetrics returns this month's usage metrics for the separate usage_metrics
// store: the cost-orphaned USG-3 "Unknown" rows (the unknown-unit quantity-
// bearing rows OAI-12 leaves money-only) APPENDED with the ten usage endpoints'
// non-token counts and byte totals (num_model_requests→Requests plus images/
// characters/seconds/num_sessions/usage_bytes and the source-qualified search
// call metrics; never a token count — see usageEndpoints). It is a concrete
// method on *Connector (off the frozen ingest.Connector interface, decision D16),
// mirroring AnomalySummary's accessor shape; the driver reads it in the AI
// discovery loop and writes it (a whole-batch replace) only after this period's
// cost ingest succeeds. Non-nil normally (empty when the month has no orphans and
// no usage rows); nil ONLY on a usage-endpoint fetch failure — signalling the
// driver to SKIP the write and PRESERVE the prior rows while cost still ingests
// (usage is orthogonal to cost). Never touches cost_records or any money/token
// total.
func (c *Connector) UsageMetrics() []storage.Metric { return c.usageMetrics }

func (c *Connector) SourceIdentity() string {
	return providerHost + "/" + c.slot + "/" + c.month
}

func (c *Connector) ContentHash(context.Context) (string, error) { return c.contentHash, nil }

func (c *Connector) Records(context.Context) (ingest.RecordReader, error) {
	return &recordReader{conn: c}, nil
}

type recordReader struct {
	conn *Connector
	bi   int
	ri   int
	num  int
}

func (r *recordReader) Next() (ingest.Row, error) {
	for r.bi < len(r.conn.buckets) {
		b := r.conn.buckets[r.bi]
		if r.ri >= len(b.Results) {
			r.bi++
			r.ri = 0
			continue
		}
		res := b.Results[r.ri]
		r.ri++
		r.num++
		rec, err := r.conn.synthesize(b, res)
		if err != nil {
			return ingest.Row{}, err
		}
		return ingest.Row{Number: r.num, Record: rec}, nil
	}
	return ingest.Row{}, io.EOF
}

func (r *recordReader) Close() error { return nil }

// daySeconds is the span of the one-day buckets this connector requests
// (bucket_width=1d). A bucket whose span differs degrades its month rather
// than being mis-synthesized onto a wrong ChargePeriod.
const daySeconds = 24 * 60 * 60

func (c *Connector) synthesize(b bucket, res result) (focus.RawRecord, error) {
	// Tolerate an unknown bucket_width: the connector always requests 1d, so
	// any bucket that is not exactly a one-day interval (the API changing
	// bucket_width semantics, or returning a wider bucket) degrades this
	// month to a per-period error naming the offending bucket — never a
	// silent mis-mapping onto a wrong ChargePeriod.
	if b.EndTime-b.StartTime != daySeconds {
		return nil, fmt.Errorf("openai-cost %s: a cost bucket is not a well-formed one-day interval "+
			"(start_time %d, end_time %d, span %ds; want %ds) — the API may have changed bucket_width semantics",
			c.month, b.StartTime, b.EndTime, b.EndTime-b.StartTime, daySeconds)
	}
	if res.Amount.Currency == "" {
		return nil, fmt.Errorf("openai-cost %s: a cost bucket carries no currency — refusing to assume USD "+
			"(decision D23)", c.month)
	}
	// Dollars, built from the JSON literal — never through float64.
	valStr := strings.TrimSpace(string(res.Amount.Value))
	if valStr == "" || valStr == "null" {
		return nil, fmt.Errorf("openai-cost %s: a cost bucket carries no amount value", c.month)
	}
	d, err := decimal.NewFromString(valStr)
	if err != nil {
		return nil, fmt.Errorf("openai-cost %s: cost amount %q is not a decimal", c.month, valStr)
	}
	dollars := d.String()

	rec := focus.RawRecord{
		"BilledCost":          dollars,
		"EffectiveCost":       dollars,
		"ListCost":            dollars,
		"ContractedCost":      dollars,
		"BillingCurrency":     strings.ToUpper(res.Amount.Currency),
		"ChargeCategory":      "Usage",
		"ChargeFrequency":     "Usage-Based",
		"ChargePeriodStart":   time.Unix(b.StartTime, 0).UTC().Format(time.RFC3339),
		"ChargePeriodEnd":     time.Unix(b.EndTime, 0).UTC().Format(time.RFC3339),
		"BillingPeriodStart":  c.monthStart.Format(time.RFC3339),
		"BillingPeriodEnd":    c.monthEnd.Format(time.RFC3339),
		"BillingAccountId":    providerHost + "/" + c.slot,
		"ServiceProviderName": "OpenAI",
		"InvoiceIssuerName":   "OpenAI",
		"ServiceName":         serviceName,
		"ServiceCategory":     "AI and Machine Learning",
	}
	if res.LineItem != "" {
		rec["ChargeDescription"] = res.LineItem
	}
	if res.ProjectID != "" {
		rec["SubAccountId"] = res.ProjectID
	}
	// Enrichment (OAI-11): same-row, no join. Only when the quantity is non-null
	// AND the line_item's suffix unambiguously names a token direction does the
	// row gain the full quantity/SKU set, atomically. Everything else stays
	// money-only (OAI-12). The SkuId keeps line_item VERBATIM (including the
	// direction suffix), and the decimal is built from the JSON literal — never
	// float64.
	if qty := quantityLiteral(res); qty != "" {
		if meter, ok := openaiSkuMeter(res.LineItem); ok {
			d, err := decimal.NewFromString(qty)
			// A malformed quantity literal (err != nil) degrades this row to
			// money-only rather than failing the period (the asymmetry with a
			// malformed AMOUNT, which fails per D23); openaiAnomalies counts it
			// in the per-period summary (OAI-12) so it is never swallowed.
			if err == nil {
				sku := "openai/" + res.LineItem
				rec["ConsumedQuantity"] = d.String()
				rec["ConsumedUnit"] = "Tokens"
				rec["SkuId"] = sku
				rec["SkuPriceId"] = sku
				rec["SkuMeter"] = meter
				rec["PricingQuantity"] = d.Shift(-6).String()
				rec["PricingUnit"] = "1000000 Tokens"
			}
		}
	}
	return rec, nil
}

func truncateBody(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

func scrubTransportErr(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if u, perr := url.Parse(urlErr.URL); perr == nil {
			u.RawQuery = ""
			return urlErr.Op + " " + u.String() + ": " + urlErr.Err.Error()
		}
	}
	return err.Error()
}
