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
// # ContentHash — data elements only
//
// ContentHash is "sha256:<hex>" over the concatenated raw bytes of the
// response data-array elements (the bucket objects exactly as received) in
// fetch order, EXCLUDING the pagination envelope: has_more/next_page cursor
// tokens are undocumented and may be time-derived (the API's own example
// next_page is a timestamp), so including them would defeat the store's
// unchanged short-circuit. Discover fetches each month's pages once up
// front and caches the payloads, so ContentHash and Records share one fetch.
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
//	ANT-10  ChargeDescription / SkuMeter    raw description; SkuMeter = model
//	                                         id when the bucket carries one.
//	ANT-11  SubAccountId                    workspace_id when grouped by
//	                                         workspace, else null.
//
// ConsumedQuantity stays null this slice: the cost endpoint carries no token
// quantities (this does not reverse decision D4; token-quantity ingestion
// via the usage endpoint is a later slice). Priority Tier costs are EXCLUDED
// from the cost-report endpoint by Anthropic; tracking them via the usage
// report is out of scope.
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

// costReportPath is the endpoint path (kept out of error messages, which
// never echo full request URLs).
const costReportPath = "/v1/organizations/cost_report"

// maxBodyBytes caps how much of a response body is read into memory or an
// error message.
const maxBodyBytes = 1 << 20

// max429Retries bounds the Retry-After honoring on 429 responses.
const max429Retries = 5

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

// fetchMonth pages through one month's cost report, following
// has_more/next_page, and returns a Connector caching the fetched buckets.
func fetchMonth(ctx context.Context, client *http.Client, base, apiKey, slot, month string) (*Connector, error) {
	start, end, err := aiconn.MonthBounds(month)
	if err != nil {
		return nil, err
	}
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
		body, err := doGet(ctx, client, base+costReportPath+"?"+encodeQuery(q), apiKey, month)
		if err != nil {
			return nil, err
		}
		var resp costReport
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("anthropic-cost %s: decoding cost report response: %w", month, err)
		}
		for _, raw := range resp.Data {
			rawBuckets = append(rawBuckets, raw)
			var b bucket
			if err := json.Unmarshal(raw, &b); err != nil {
				return nil, fmt.Errorf("anthropic-cost %s: decoding cost bucket: %w", month, err)
			}
			buckets = append(buckets, b)
		}
		if !resp.HasMore || resp.NextPage == "" {
			break
		}
		page = resp.NextPage
	}
	return &Connector{
		slot:        slot,
		month:       month,
		monthStart:  start,
		monthEnd:    end,
		buckets:     buckets,
		contentHash: contentHash(rawBuckets),
	}, nil
}

// groupBy is the finest documented grouping combination. Anthropic documents
// the parameter as the bracketed, repeated group_by[]= (not a bare
// group_by=), so encodeQuery emits it with literal brackets.
var groupBy = []string{"description", "workspace_id"}

// encodeQuery renders q plus the repeated group_by[]= parameters with LITERAL
// brackets, so the wire key is exactly "group_by[]" (url.Values.Encode would
// percent-encode the brackets, and a bare "group_by" is the wrong parameter
// name for this endpoint). The group_by values are fixed identifiers needing
// no escaping.
func encodeQuery(q url.Values) string {
	encoded := q.Encode()
	for _, g := range groupBy {
		encoded += "&group_by[]=" + g
	}
	return encoded
}

// doGet issues one GET with the auth headers and bounded 429 retries. It
// NEVER logs or echoes the api key, request headers, or the request URL
// (which would carry the query string).
func doGet(ctx context.Context, client *http.Client, requestURL, apiKey, month string) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
		if err != nil {
			return nil, fmt.Errorf("anthropic-cost %s: building request: %w", month, err)
		}
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", anthropicVersion)

		resp, err := client.Do(req)
		if err != nil {
			// A transport error may embed the request URL — scrub the query.
			return nil, fmt.Errorf("anthropic-cost %s: requesting the cost report failed: %s", month, scrubTransportErr(err))
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		_ = resp.Body.Close()

		switch {
		case resp.StatusCode == http.StatusOK:
			if readErr != nil {
				return nil, fmt.Errorf("anthropic-cost %s: reading the cost report body: %w", month, readErr)
			}
			return body, nil
		case resp.StatusCode == http.StatusTooManyRequests && attempt < max429Retries:
			if err := waitRetryAfter(ctx, resp.Header.Get("Retry-After")); err != nil {
				return nil, err
			}
			continue
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return nil, fmt.Errorf("anthropic-cost %s: the Anthropic Admin API key was rejected (HTTP %d) — check the "+
				"credential slot holds a valid Anthropic Admin API key with cost-report access: %s",
				month, resp.StatusCode, truncateBody(body))
		default:
			return nil, fmt.Errorf("anthropic-cost %s: cost report request failed (HTTP %d): %s",
				month, resp.StatusCode, truncateBody(body))
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

// costReport is the response envelope; Data elements are kept raw so
// ContentHash hashes the bytes exactly as received.
type costReport struct {
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
// and workspace. Token-type/usage fields are intentionally ignored.
type result struct {
	Amount      string `json:"amount"`   // decimal string in cents
	Currency    string `json:"currency"` // e.g. "USD"
	Description string `json:"description"`
	Model       string `json:"model"`
	WorkspaceID string `json:"workspace_id"`
}

// contentHash is "sha256:<hex>" over the concatenated raw bytes of the data
// elements in fetch order (see the package documentation).
func contentHash(rawBuckets [][]byte) string {
	h := sha256.New()
	for _, b := range rawBuckets {
		_, _ = h.Write(b)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// Connector reads one billing month of the Anthropic cost report. Instances
// are produced by Discover, one per month, holding that month's cached
// buckets so Records and ContentHash never re-fetch.
type Connector struct {
	slot        string
	month       string
	monthStart  time.Time
	monthEnd    time.Time
	buckets     []bucket
	contentHash string
}

var _ ingest.Connector = (*Connector)(nil)

// Name implements ingest.Connector.
func (c *Connector) Name() string { return Name }

// FOCUSVersion implements ingest.Connector: the connector synthesizes
// 1.4-shaped records directly (the identity transform).
func (c *Connector) FOCUSVersion() focus.Version { return focus.V1_4 }

// Month returns the connector's billing month ("YYYY-MM").
func (c *Connector) Month() string { return c.month }

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

// Close implements ingest.RecordReader.
func (r *recordReader) Close() error { return nil }

// synthesize maps one Anthropic cost line to a FOCUS-1.4 RawRecord per the
// ANT rule table.
func (c *Connector) synthesize(b bucket, res result) (focus.RawRecord, error) {
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
	if res.Model != "" {
		rec["SkuMeter"] = res.Model
	}
	if res.WorkspaceID != "" {
		rec["SubAccountId"] = res.WorkspaceID
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

// truncateBody bounds an error-embedded response body and collapses
// whitespace so nothing sprawling reaches a log line. Bodies are aggregated
// cost metadata or provider error JSON — never prompt/response content.
func truncateBody(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// scrubTransportErr removes any URL query string from a transport error so a
// request URL never reaches a log or error verbatim.
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
