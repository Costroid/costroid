// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package anthropiccost implements the "anthropic-cost" connector
// (decisions D7, D16, D17, D29, D31): it ingests aggregated cost metadata
// from the Anthropic Admin cost-report API and synthesizes FOCUS-1.4-shaped
// records. It is the repo's FIRST non-FOCUS source — there is no upstream
// FOCUS export to transform — so its shape is the template for future
// non-FOCUS connectors.
//
// # Endpoint & auth
//
// GET https://api.anthropic.com/v1/organizations/cost_report, authenticated
// with an Anthropic ADMIN API key (sk-ant-admin01-…) sent as the x-api-key
// header plus anthropic-version: 2023-06-01. Admin keys are UNSCOPEABLE
// full-org-admin credentials, so the least-privilege burden falls entirely
// on Costroid's encrypted credential store (decision D32), not on the
// vendor — treat the stored key accordingly.
//
// # Cardinal Rule (decision D7)
//
// The cost-report endpoint returns aggregated cost metadata only — amounts,
// currency, day buckets, model identity, and a cost-line description. NO
// prompt or response content is fetched, stored, or logged, ever. The
// x-api-key never appears in any log or error at any level; request URLs
// (with their query strings) and request headers are never logged either.
//
// # Periods, window, and idempotency
//
// One billing period = one UTC calendar month. The default window is the
// last 12 calendar months (the current month and the 11 before it); --since
// YYYY-MM moves the start; --period YYYY-MM fetches exactly one month. Each
// month is one batch under the frozen ingest seam (decision D16):
//
//	SourceIdentity = "api.anthropic.com/<credential-slot>/<YYYY-MM>"
//
// Re-ingesting a month REPLACES its batch (decision D26a), so a month
// restated to a new total supersedes the stale one. EVERY month in the
// window runs the pipeline, INCLUDING empty ones: an empty month stores an
// empty batch, which is exactly what makes a month restated-to-zero
// actually drop stale data. The provider host in the identity is a fixed
// constant, NOT the --base-url host, so pointing --base-url at a proxy or
// the offline fake does not re-home data. Repointing one credential slot at
// a different org would supersede the old org's data; single-org-per-slot
// is the supported v1 shape.
//
// # ContentHash — data elements only, over BOTH payload sets
//
// ContentHash is "sha256:<hex>" over the concatenated raw bytes of the
// response data-array elements (the bucket objects exactly as received) —
// FIRST the cost_report elements, THEN the usage_report/messages elements — in
// fetch order, EXCLUDING each pagination envelope: has_more/next_page cursor
// tokens are undocumented and may be time-derived (the API's own example
// next_page is a timestamp), so including them would defeat the store's
// unchanged short-circuit. It covers the usage payloads too because a
// quantity-only restatement (unchanged money, changed token counts) MUST NOT
// be skipped by the unchanged short-circuit — the enrichment it drives changed.
// Discover fetches each month's cost AND usage pages once up front and caches
// the payloads, so ContentHash and Records share one fetch.
//
// # --force
//
// --force is accepted for CLI uniformity but is a documented NO-OP beyond
// re-fetching: this connector keeps no incremental sync state (no
// storage.SyncState tuple to bypass), so every month always runs, and the
// store's unchanged short-circuit on a byte-identical re-sync is
// unconditional and untouched.
//
// # FOCUS mapping (rules ANT-1…, all validation-mandatory columns non-null)
//
//	rule    FOCUS column(s)                 value
//	-----   ------------------------------  ---------------------------------
//	ANT-1   ChargePeriodStart / End         bucket starting_at / ending_at
//	                                         (a UTC day), RFC 3339.
//	ANT-2   BillingPeriodStart / End        the enclosing UTC calendar month.
//	ANT-3   BilledCost                      bucket amount, shifted from cents
//	                                         to dollars by exactly -2 (never
//	                                         via float64).
//	ANT-4   EffectiveCost = ListCost        = ContractedCost = BilledCost —
//	        = ContractedCost                 the API exposes no list-vs-
//	                                         negotiated distinction.
//	ANT-5   BillingCurrency                 response currency, uppercased. A
//	                                         bucket with no currency FAILS the
//	                                         period (decision D23 — never
//	                                         assume USD).
//	ANT-6   BillingAccountId                "api.anthropic.com/<slot>"
//	                                         (Costroid synthesis — documented).
//	ANT-7   ServiceProviderName             "Anthropic" (= InvoiceIssuerName).
//	        InvoiceIssuerName
//	ANT-8   ServiceName / ServiceCategory   "Claude API" / "AI and Machine
//	                                         Learning".
//	ANT-9   ChargeCategory / ChargeFrequency "Usage" / "Usage-Based";
//	                                         ChargeClass null. Negative
//	                                         amounts (credits/refunds) pass
//	                                         through unchanged.
//	ANT-10  ChargeDescription / SkuMeter    ChargeDescription = raw description
//	                                         (model identity lives here). SkuMeter
//	                                         is null UNLESS the row is enriched
//	                                         (ANT-13) — FOCUS 1.4 requires
//	                                         SkuMeter be null when SkuId is null.
//	                                         [Behavior change vs slice 5, which
//	                                         set SkuMeter = model on every
//	                                         model-bearing row: that was a latent
//	                                         conformance bug; first re-sync of
//	                                         existing data reports `replaced`.]
//	ANT-11  SubAccountId                    workspace_id when grouped by
//	                                         workspace, else null.
//	ANT-12  usage fetch                     In the SAME per-month sync run, the
//	                                         usage_report/messages endpoint is
//	                                         fetched (1d buckets; group_by[]=
//	                                         model, workspace_id, context_window,
//	                                         inference_geo, service_tier; cursor
//	                                         paginated). A usage failure degrades
//	                                         the whole month to a per-period error
//	                                         (never a silently quantity-less
//	                                         ingest). ContentHash covers both
//	                                         payload sets (see above).
//	ANT-13  join & mint (enrichment)        Each usage row unpivots to up to five
//	                                         (token_type, quantity) pairs (nested
//	                                         cache_creation descended); quantities
//	                                         PRE-AGGREGATE per (day, model,
//	                                         context_window, workspace_id,
//	                                         inference_geo, service_tier,
//	                                         token_type) — duplicate buckets SUM.
//	                                         A cost row with cost_type=="tokens"
//	                                         joins on (model, context_window,
//	                                         workspace_id [empty tolerated],
//	                                         inference_geo [null cost-side matches
//	                                         usage summed across all geo values],
//	                                         service_tier ∈ {standard,batch},
//	                                         token_type). On a UNIQUE match the
//	                                         row gains, atomically (all-or-none):
//	                                         ConsumedQuantity = token count;
//	                                         ConsumedUnit = "Tokens";
//	                                         SkuId = anthropic/<model>/<token_type>/
//	                                         <context_window>;
//	                                         SkuPriceId = <SkuId>/<service_tier>;
//	                                         SkuMeter = the friendly meter name;
//	                                         PricingQuantity = quantity ÷ 1,000,000
//	                                         by exact decimal shift;
//	                                         PricingUnit = "1000000 Tokens". These
//	                                         minted SKU identifiers are a Costroid
//	                                         convention (decision D33) — FROZEN
//	                                         once shipped. BilledCost and the other
//	                                         money columns stay byte-identical
//	                                         (money invariance, decision D33).
//	ANT-14  ambiguity & orphans             If MORE THAN ONE cost row matches one
//	                                         aggregated usage key, enrich NONE and
//	                                         count the collision. Priority/flex-tier
//	                                         usage, web_search_requests counts, and
//	                                         standard/batch usage keys with no cost
//	                                         row are counted in the per-period
//	                                         anomaly summary (one log line) and
//	                                         NEVER emitted as FOCUS rows (D33). Cost
//	                                         rows with cost_type ∉ {tokens}
//	                                         (web_search, code_execution,
//	                                         session_usage, unknown future values)
//	                                         ingest money-only: quantity-null and
//	                                         SkuMeter/SkuId/SkuPriceId null.
//
// ListUnitPrice/ContractedUnitPrice stay null on minted rows (documented
// deviation, decision D33): unit prices need vendor price lists, a later slice —
// stated honestly rather than synthesized. ConsumedQuantity is thus non-null
// only on enriched rows; the FOCUS 1.3+ coupling (ConsumedQuantity requires a
// non-null SkuPriceId; ConsumedUnit non-null iff ConsumedQuantity non-null;
// SkuMeter null when SkuId null) is enforced by this connector's all-or-none
// mint and its tests, NOT by the per-column validator.
//
// The draft-1.5 SkuPriceDetails model-identity properties (spec PR #2442) are
// intentionally deferred (decision D33): its property set changed mid-review, so
// adopting 1.5 will be a rename onto these minted identifiers, not a remodel.
package anthropiccost

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
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/credentials"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/ingest"
	"github.com/Costroid/costroid/internal/ingest/aiconn"
	"github.com/Costroid/costroid/internal/ingest/aiwire"
	"github.com/Costroid/costroid/internal/storage"
)

// Name is the connector's registry name; it is also the default credential
// slot name.
const Name = "anthropic-cost"

// DefaultBaseURL is the production API root; --base-url overrides it.
const DefaultBaseURL = "https://api.anthropic.com"

// providerHost is the FIXED host used in SourceIdentity and BillingAccountId
// (never the --base-url host), so a proxy or the offline fake never re-homes
// data.
const providerHost = "api.anthropic.com"

// anthropicVersion is the required API version header.
const anthropicVersion = "2023-06-01"

// costReportPath is the cost endpoint path (kept out of error messages, which
// never echo full request URLs).
const costReportPath = "/v1/organizations/cost_report"

// usageReportPath is the token-usage endpoint path — the ONLY token-usage
// endpoint — fetched in the same sync run as the cost report (ANT-12).
const usageReportPath = "/v1/organizations/usage_report/messages"

// usageLimit is the requested usage-page bucket size. Usage buckets are 1d, so
// a whole month fits in at most 31 buckets; the fake paginates independently to
// exercise the cursor path.
const usageLimit = "31"

// Period is one discovered billing period (one UTC calendar month). Conn is
// non-nil for every month in the window — including empty ones — unless the
// month's fetch failed, in which case Err carries the per-period failure and
// the other months proceed.
type Period struct {
	Month string
	Conn  *Connector
	Err   error
}

// Discover fetches every month in the window once up front (Discover-style,
// mirroring the azure connector), caches each month's payloads, and returns
// one Period per month oldest-first. A month whose fetch fails degrades to
// Period.Err; only argument errors (bad base URL, bad --since/--period)
// abort discovery. secret is the Admin API key, already loaded from the
// credential store by the caller so a missing credential fails BEFORE any
// network dial.
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

// fetchMonth pages through one month's cost report AND its token usage report
// (ANT-12), following has_more/next_page on each, joins the usage quantities
// onto the cost tokens rows (ANT-13/14), and returns a Connector caching the
// enriched buckets. A usage failure degrades the whole month (returns an error
// → Period.Err), so the month is never ingested with silently-missing
// quantities.
func fetchMonth(ctx context.Context, client *http.Client, base, apiKey, slot, month string) (*Connector, error) {
	start, end, err := aiconn.MonthBounds(month)
	if err != nil {
		return nil, err
	}
	rawCost, buckets, err := fetchCost(ctx, client, base, apiKey, month, start, end)
	if err != nil {
		return nil, err
	}
	rawUsage, usage, err := fetchUsage(ctx, client, base, apiKey, month, start, end)
	if err != nil {
		return nil, err
	}
	enrich, summary, metrics := enrichMonth(buckets, usage)
	return &Connector{
		slot:         slot,
		month:        month,
		monthStart:   start,
		monthEnd:     end,
		buckets:      buckets,
		enrich:       enrich,
		summary:      summary,
		usageMetrics: metrics,
		contentHash:  contentHash(rawCost, rawUsage),
	}, nil
}

// fetchCost pages through one month's cost report, returning the raw data-
// element bytes (for ContentHash) and the parsed buckets.
func fetchCost(ctx context.Context, client *http.Client, base, apiKey, month string, start, end time.Time) ([][]byte, []bucket, error) {
	var (
		rawBuckets [][]byte
		buckets    []bucket
		page       string
	)
	for {
		q := url.Values{}
		q.Set("starting_at", start.Format(time.RFC3339))
		q.Set("ending_at", end.Format(time.RFC3339))
		q.Set("bucket_width", "1d")
		q.Set("limit", "31")
		if page != "" {
			q.Set("page", page)
		}
		body, err := doGet(ctx, client, base+costReportPath+"?"+encodeQuery(q, costGroupBy), apiKey, month, "cost report")
		if err != nil {
			return nil, nil, err
		}
		var resp pagedResponse
		if err := body.Decode(&resp); err != nil {
			return nil, nil, fmt.Errorf("anthropic-cost %s: decoding cost report response: %w", month, err)
		}
		for _, raw := range resp.Data {
			rawBuckets = append(rawBuckets, raw)
			var b bucket
			if err := json.Unmarshal(raw, &b); err != nil {
				return nil, nil, fmt.Errorf("anthropic-cost %s: decoding cost bucket: %w", month, err)
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

// fetchUsage pages through one month's token usage report, returning the raw
// data-element bytes (for ContentHash) and the parsed usage buckets.
func fetchUsage(ctx context.Context, client *http.Client, base, apiKey, month string, start, end time.Time) ([][]byte, []usageBucket, error) {
	var (
		rawBuckets [][]byte
		buckets    []usageBucket
		page       string
	)
	for {
		q := url.Values{}
		q.Set("starting_at", start.Format(time.RFC3339))
		q.Set("ending_at", end.Format(time.RFC3339))
		q.Set("bucket_width", "1d")
		q.Set("limit", usageLimit)
		if page != "" {
			q.Set("page", page)
		}
		body, err := doGet(ctx, client, base+usageReportPath+"?"+encodeQuery(q, usageGroupBy), apiKey, month, "usage report")
		if err != nil {
			return nil, nil, err
		}
		var resp pagedResponse
		if err := body.Decode(&resp); err != nil {
			return nil, nil, fmt.Errorf("anthropic-cost %s: decoding usage report response: %w", month, err)
		}
		for _, raw := range resp.Data {
			rawBuckets = append(rawBuckets, raw)
			var b usageBucket
			if err := json.Unmarshal(raw, &b); err != nil {
				return nil, nil, fmt.Errorf("anthropic-cost %s: decoding usage bucket: %w", month, err)
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

// costGroupBy is the finest documented cost grouping combination. Anthropic
// documents the parameter as the bracketed, repeated group_by[]= (not a bare
// group_by=), so encodeQuery emits it with literal brackets.
var costGroupBy = []string{"description", "workspace_id"}

// usageGroupBy is the five join dims requested from usage_report/messages
// (ANT-12) — the cost-side join dimensions, and ONLY those (never api_key_id /
// account_id / service_account_id / speed).
var usageGroupBy = []string{"model", "workspace_id", "context_window", "inference_geo", "service_tier"}

// encodeQuery renders q plus the given repeated group_by[]= parameters with
// LITERAL brackets, so the wire key is exactly "group_by[]" (url.Values.Encode
// would percent-encode the brackets, and a bare "group_by" is the wrong
// parameter name for these endpoints). The group_by values are fixed
// identifiers needing no escaping.
func encodeQuery(q url.Values, groups []string) string {
	encoded := q.Encode()
	for _, g := range groups {
		encoded += "&group_by[]=" + g
	}
	return encoded
}

// doGet issues one GET with the Anthropic auth headers through the shared
// aiwire chokepoint and translates aiwire's typed *StatusError back into this
// connector's bespoke, body-free error prose. what names the endpoint (e.g.
// "cost report", "usage report") for accurate error messages. The raw response
// body never enters an error: it stays trapped inside the returned aiwire.Body
// until a caller pulls out modeled fields via Body.Decode. The api key, request
// headers, and request URL (with its query string) are never logged or echoed —
// aiwire owns the bounded 429 retry and the query scrub.
func doGet(ctx context.Context, client *http.Client, requestURL, apiKey, month, what string) (aiwire.Body, error) {
	header := http.Header{}
	header.Set("x-api-key", apiKey)
	header.Set("anthropic-version", anthropicVersion)
	body, err := aiwire.Get(ctx, client, requestURL, header)
	if err == nil {
		return body, nil
	}
	var se *aiwire.StatusError
	if errors.As(err, &se) {
		switch se.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return aiwire.Body{}, fmt.Errorf("anthropic-cost %s: the Anthropic Admin API key was rejected (HTTP %d) — check the "+
				"credential slot holds a valid Anthropic Admin API key with cost/usage-report access: %s",
				month, se.Status, se.VendorCode)
		default:
			return aiwire.Body{}, fmt.Errorf("anthropic-cost %s: %s request failed (HTTP %d): %s",
				month, what, se.Status, se.VendorCode)
		}
	}
	// A transport failure, request-build error, or 200-body-read error is already
	// body-free and query-scrubbed by aiwire — wrap it with this connector's own
	// transport prose (verbatim).
	return aiwire.Body{}, fmt.Errorf("anthropic-cost %s: requesting the %s failed: %s", month, what, err)
}

// pagedResponse is the shared cost/usage response envelope; Data elements are
// kept raw so ContentHash hashes the bytes exactly as received.
type pagedResponse struct {
	Data     []json.RawMessage `json:"data"`
	HasMore  bool              `json:"has_more"`
	NextPage string            `json:"next_page"`
}

// bucket is one day's cost bucket.
type bucket struct {
	StartingAt string   `json:"starting_at"`
	EndingAt   string   `json:"ending_at"`
	Results    []result `json:"results"`
}

// result is one cost line within a bucket. Only cost METADATA is read
// (Cardinal Rule): amount, currency, model identity, cost-line description,
// workspace, and the STRUCTURED enum fields Anthropic supplies (never parsed
// out of the description string) — cost_type, and, when cost_type=="tokens",
// the token_type/service_tier/context_window/inference_geo that name the usage
// quantity this row's money paid for (ANT-13). No prompt/response content
// exists on this surface.
type result struct {
	Amount      string `json:"amount"`   // decimal string in cents
	Currency    string `json:"currency"` // e.g. "USD"
	Description string `json:"description"`
	Model       string `json:"model"`
	WorkspaceID string `json:"workspace_id"`

	CostType      string `json:"cost_type"`
	TokenType     string `json:"token_type"`
	ServiceTier   string `json:"service_tier"`
	ContextWindow string `json:"context_window"`
	InferenceGeo  string `json:"inference_geo"`
}

// contentHash is "sha256:<hex>" over the concatenated raw bytes of the cost
// data elements THEN the usage data elements, in fetch order (see the package
// documentation). Covering usage lets a quantity-only restatement supersede.
func contentHash(rawCost, rawUsage [][]byte) string {
	h := sha256.New()
	for _, b := range rawCost {
		_, _ = h.Write(b)
	}
	for _, b := range rawUsage {
		_, _ = h.Write(b)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// Connector reads one billing month of the Anthropic cost report, enriched
// with token quantities from the usage report (ANT-13). Instances are produced
// by Discover, one per month, holding that month's cached cost buckets, the
// precomputed per-row enrichment, and the per-period anomaly summary so Records
// and ContentHash never re-fetch.
type Connector struct {
	slot         string
	month        string
	monthStart   time.Time
	monthEnd     time.Time
	buckets      []bucket
	enrich       map[rowKey]enrichment
	summary      anomalySummary
	usageMetrics []storage.Metric
	contentHash  string
}

var _ ingest.Connector = (*Connector)(nil)

// Name implements ingest.Connector.
func (c *Connector) Name() string { return Name }

// FOCUSVersion implements ingest.Connector: the connector synthesizes
// 1.4-shaped records directly (the identity transform).
func (c *Connector) FOCUSVersion() focus.Version { return focus.V1_4 }

// Month returns the connector's billing month ("YYYY-MM").
func (c *Connector) Month() string { return c.month }

// AnomalySummary returns one line summarizing this month's usage⇔cost
// reconciliation anomalies (collisions, cost-orphaned usage, priority/flex-tier
// usage, web-search counts), or "" when there is nothing to report. These
// surfaces are counted but never emitted as FOCUS rows (ANT-14, decision D33).
func (c *Connector) AnomalySummary() string { return c.summary.String() }

// UsageMetrics returns this month's cost-orphaned usage metrics (priority/flex-
// tier tokens, web-search request counts, and standard/batch usage keys no cost
// row referenced) — the quantities ANT-14 counts but never emits as FOCUS rows,
// surfaced for the separate usage_metrics store. It is a concrete method on
// *Connector (off the frozen ingest.Connector interface, decision D16),
// mirroring AnomalySummary's accessor shape; the driver reads it in the AI
// discovery loop and writes it only after this period's cost ingest succeeds.
// The slice is always non-nil (empty when the month has no orphans); its order
// is unspecified — the store's DailyUsageMetrics makes the surfaced view
// deterministic. Never touches cost_records or any money/token total.
func (c *Connector) UsageMetrics() []storage.Metric { return c.usageMetrics }

// SourceIdentity implements ingest.Connector (see the package documentation
// for why the provider host is fixed).
func (c *Connector) SourceIdentity() string {
	return providerHost + "/" + c.slot + "/" + c.month
}

// ContentHash implements ingest.Connector from the cached buckets — no
// network.
func (c *Connector) ContentHash(context.Context) (string, error) { return c.contentHash, nil }

// Records implements ingest.Connector: it walks the cached buckets and
// synthesizes one FOCUS-1.4 RawRecord per cost line.
func (c *Connector) Records(context.Context) (ingest.RecordReader, error) {
	return &recordReader{conn: c}, nil
}

type recordReader struct {
	conn *Connector
	bi   int // bucket index
	ri   int // result index within bucket
	num  int
}

// Next implements ingest.RecordReader.
func (r *recordReader) Next() (ingest.Row, error) {
	for r.bi < len(r.conn.buckets) {
		b := r.conn.buckets[r.bi]
		if r.ri >= len(b.Results) {
			r.bi++
			r.ri = 0
			continue
		}
		res := b.Results[r.ri]
		rk := rowKey{r.bi, r.ri}
		r.ri++
		r.num++
		rec, err := r.conn.synthesize(b, res, rk)
		if err != nil {
			return ingest.Row{}, err
		}
		return ingest.Row{Number: r.num, Record: rec}, nil
	}
	return ingest.Row{}, io.EOF
}

// Close implements ingest.RecordReader.
func (r *recordReader) Close() error { return nil }

// synthesize maps one Anthropic cost line to a FOCUS-1.4 RawRecord per the
// ANT rule table, applying the precomputed token-quantity enrichment (ANT-13)
// when this row (identified by rk) uniquely matched a usage key.
func (c *Connector) synthesize(b bucket, res result, rk rowKey) (focus.RawRecord, error) {
	if res.Currency == "" {
		return nil, fmt.Errorf("anthropic-cost %s: a cost bucket carries no currency — refusing to assume USD "+
			"(decision D23); the Anthropic cost report must supply a currency", c.month)
	}
	// Cents → dollars, shifted by exactly -2, never through float64.
	amount, err := decimal.NewFromString(strings.TrimSpace(res.Amount))
	if err != nil {
		return nil, fmt.Errorf("anthropic-cost %s: cost amount %q is not a decimal", c.month, res.Amount)
	}
	dollars := amount.Shift(-2).String()

	start, err := normalizeDayBound(b.StartingAt)
	if err != nil {
		return nil, fmt.Errorf("anthropic-cost %s: bucket starting_at: %w", c.month, err)
	}
	end, err := normalizeDayBound(b.EndingAt)
	if err != nil {
		return nil, fmt.Errorf("anthropic-cost %s: bucket ending_at: %w", c.month, err)
	}

	rec := focus.RawRecord{
		"BilledCost":          dollars,
		"EffectiveCost":       dollars,
		"ListCost":            dollars,
		"ContractedCost":      dollars,
		"BillingCurrency":     strings.ToUpper(res.Currency),
		"ChargeCategory":      "Usage",
		"ChargeFrequency":     "Usage-Based",
		"ChargePeriodStart":   start,
		"ChargePeriodEnd":     end,
		"BillingPeriodStart":  c.monthStart.Format(time.RFC3339),
		"BillingPeriodEnd":    c.monthEnd.Format(time.RFC3339),
		"BillingAccountId":    providerHost + "/" + c.slot,
		"ServiceProviderName": "Anthropic",
		"InvoiceIssuerName":   "Anthropic",
		"ServiceName":         "Claude API",
		"ServiceCategory":     "AI and Machine Learning",
	}
	if res.Description != "" {
		rec["ChargeDescription"] = res.Description
	}
	if res.WorkspaceID != "" {
		rec["SubAccountId"] = res.WorkspaceID
	}
	// Enrichment (ANT-13): only rows that uniquely matched a usage key gain the
	// full quantity/SKU set, atomically. All other rows — unjoined token rows,
	// web_search/session_usage/code_execution, credits — carry no SkuMeter,
	// SkuId, SkuPriceId, ConsumedQuantity, or PricingQuantity (ANT-10, ANT-14),
	// keeping the money-only rows FOCUS-conformant.
	if enr, ok := c.enrich[rk]; ok {
		rec["ConsumedQuantity"] = enr.quantity.String()
		rec["ConsumedUnit"] = "Tokens"
		rec["SkuId"] = enr.skuID
		rec["SkuPriceId"] = enr.skuPriceID
		rec["SkuMeter"] = enr.skuMeter
		rec["PricingQuantity"] = enr.pricingQty.String()
		rec["PricingUnit"] = "1000000 Tokens"
	}
	return rec, nil
}

// normalizeDayBound parses a bucket boundary (RFC 3339) and re-emits it in
// UTC RFC 3339 so downstream parsing is uniform.
func normalizeDayBound(s string) (string, error) {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return "", fmt.Errorf("%q is not an RFC 3339 timestamp", s)
	}
	return t.UTC().Format(time.RFC3339), nil
}
