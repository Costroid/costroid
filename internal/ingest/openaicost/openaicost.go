// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package openaicost implements the "openai-cost" connector (decisions D7,
// D16, D17, D29, D31): it ingests aggregated cost metadata from the OpenAI
// Admin costs API and synthesizes FOCUS-1.4-shaped records. It shares the
// anthropic-cost skeleton (monthly periods, up-front paged fetch,
// data-elements-only ContentHash, credential slot, --base-url, hygiene) and
// differs only where the OpenAI API differs.
//
// # Endpoint & auth
//
// GET https://api.openai.com/v1/organization/costs — the SOLE monetary
// endpoint (usage endpoints are out of scope) — authenticated with an
// OpenAI ADMIN API key sent as Authorization: Bearer. The OpenAI dashboard
// supports RESTRICTED admin keys (per-resource None/Read/Write, enforced
// server-side); a usage-specific read-only scope is NOT confirmed in the
// official docs, so operators should create a Restricted key and verify at
// creation the narrowest scope that still reads costs (first-real-account
// check). Costroid's encrypted credential store (decision D32) still carries
// the least-privilege burden.
//
// # Cardinal Rule (decision D7)
//
// Only aggregated cost metadata is fetched — amounts, currency, day buckets,
// line-item labels, and project id. No prompt/response content, ever. The
// Bearer key never appears in any log or error; request URLs (with query
// strings) and request headers are never logged.
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
//
// ConsumedQuantity stays null this slice (the costs endpoint's cost metadata
// carries no token quantities; this does not reverse decision D4).
package openaicost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/credentials"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/ingest"
)

// Name is the connector's registry name and default credential slot name.
const Name = "openai-cost"

// DefaultBaseURL is the production API root; --base-url overrides it.
const DefaultBaseURL = "https://api.openai.com"

// providerHost is the FIXED host used in SourceIdentity and BillingAccountId.
const providerHost = "api.openai.com"

// costsPath is the endpoint path (never echoed in error messages).
const costsPath = "/v1/organization/costs"

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
	if err := validateBaseURL(baseURL); err != nil {
		return nil, err
	}
	months, err := monthWindow(since, period, time.Now().UTC())
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

func fetchMonth(ctx context.Context, client *http.Client, base, apiKey, slot, month string) (*Connector, error) {
	start, end, err := monthBounds(month)
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
		q.Set("start_time", strconv.FormatInt(start.Unix(), 10))
		q.Set("end_time", strconv.FormatInt(end.Unix(), 10))
		q.Set("bucket_width", "1d")
		q["group_by"] = []string{"project_id", "line_item"} // finest documented combination
		q.Set("limit", strconv.Itoa(pageLimit))
		if page != "" {
			q.Set("page", page)
		}
		body, err := doGet(ctx, client, base+costsPath+"?"+q.Encode(), apiKey, month)
		if err != nil {
			return nil, err
		}
		var resp costsPage
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("openai-cost %s: decoding costs response: %w", month, err)
		}
		for _, raw := range resp.Data {
			rawBuckets = append(rawBuckets, raw)
			var b bucket
			if err := json.Unmarshal(raw, &b); err != nil {
				return nil, fmt.Errorf("openai-cost %s: decoding cost bucket: %w", month, err)
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

// doGet issues one GET with the Bearer auth header and bounded 429 retries.
// It NEVER logs or echoes the api key, request headers, or the request URL.
func doGet(ctx context.Context, client *http.Client, requestURL, apiKey, month string) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
		if err != nil {
			return nil, fmt.Errorf("openai-cost %s: building request: %w", month, err)
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("openai-cost %s: requesting costs failed: %s", month, scrubTransportErr(err))
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		_ = resp.Body.Close()

		switch {
		case resp.StatusCode == http.StatusOK:
			if readErr != nil {
				return nil, fmt.Errorf("openai-cost %s: reading the costs body: %w", month, readErr)
			}
			return body, nil
		case resp.StatusCode == http.StatusTooManyRequests && attempt < max429Retries:
			if err := waitRetryAfter(ctx, resp.Header.Get("Retry-After")); err != nil {
				return nil, err
			}
			continue
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return nil, fmt.Errorf("openai-cost %s: the OpenAI Admin API key was rejected (HTTP %d) — check the "+
				"credential slot holds a valid admin key whose Restricted scope can read costs: %s",
				month, resp.StatusCode, truncateBody(body))
		default:
			return nil, fmt.Errorf("openai-cost %s: costs request failed (HTTP %d) — if the window predates the "+
				"available history, ingest a more recent --since/--period: %s", month, resp.StatusCode, truncateBody(body))
		}
	}
}

func waitRetryAfter(ctx context.Context, header string) error {
	wait := 2 * time.Second
	if header != "" {
		if secs, err := time.ParseDuration(header + "s"); err == nil && secs > 0 {
			wait = min(secs, 60*time.Second)
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
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
type result struct {
	Amount    amount `json:"amount"`
	LineItem  string `json:"line_item"`
	ProjectID string `json:"project_id"`
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

// Connector reads one billing month of the OpenAI costs report.
type Connector struct {
	slot        string
	month       string
	monthStart  time.Time
	monthEnd    time.Time
	buckets     []bucket
	contentHash string
}

var _ ingest.Connector = (*Connector)(nil)

func (c *Connector) Name() string { return Name }

func (c *Connector) FOCUSVersion() focus.Version { return focus.V1_4 }

// Month returns the connector's billing month ("YYYY-MM").
func (c *Connector) Month() string { return c.month }

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

func (c *Connector) synthesize(b bucket, res result) (focus.RawRecord, error) {
	if b.EndTime <= b.StartTime {
		return nil, fmt.Errorf("openai-cost %s: a cost bucket is not a well-formed day interval "+
			"(start_time %d, end_time %d) — the API may have changed bucket_width semantics", c.month, b.StartTime, b.EndTime)
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
		"ServiceName":         "OpenAI API",
		"ServiceCategory":     "AI and Machine Learning",
	}
	if res.LineItem != "" {
		rec["ChargeDescription"] = res.LineItem
	}
	if res.ProjectID != "" {
		rec["SubAccountId"] = res.ProjectID
	}
	return rec, nil
}

func monthBounds(month string) (start, end time.Time, err error) {
	start, err = time.Parse("2006-01", month)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid month %q, want YYYY-MM", month)
	}
	start = start.UTC()
	return start, start.AddDate(0, 1, 0), nil
}

func monthWindow(since, period string, now time.Time) ([]string, error) {
	if period != "" {
		if _, err := time.Parse("2006-01", period); err != nil {
			return nil, fmt.Errorf("invalid --period %q, want YYYY-MM", period)
		}
		return []string{period}, nil
	}
	current := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	start := current.AddDate(0, -11, 0)
	if since != "" {
		s, err := time.Parse("2006-01", since)
		if err != nil {
			return nil, fmt.Errorf("invalid --since %q, want YYYY-MM", since)
		}
		start = s.UTC()
		if start.After(current) {
			return nil, fmt.Errorf("--since %q is in the future (current month %s)", since, current.Format("2006-01"))
		}
	}
	var months []string
	for m := start; !m.After(current); m = m.AddDate(0, 1, 0) {
		months = append(months, m.Format("2006-01"))
	}
	return months, nil
}

func validateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("invalid --base-url: expected an https:// API endpoint, e.g. https://api.openai.com")
	}
	if u.Scheme == "http" && !isLoopback(u.Hostname()) {
		return fmt.Errorf("--base-url %q uses http:// with a non-loopback host — use https:// for real endpoints "+
			"(http is allowed only for a loopback test server)", u.Scheme+"://"+u.Host)
	}
	return nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
