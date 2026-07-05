// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package anthropiccost_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/credentials"
	"github.com/Costroid/costroid/internal/devtools/fakeanthropic"
	"github.com/Costroid/costroid/internal/ingest"
	"github.com/Costroid/costroid/internal/ingest/anthropiccost"
)

const fixture = "../../../testdata/anthropic-cost/fixture"

func startFake(t *testing.T, dir string) (*fakeanthropic.Handler, string) {
	t.Helper()
	h := fakeanthropic.New(dir)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return h, srv.URL
}

func readAll(t *testing.T, conn ingest.Connector) []ingest.Row {
	t.Helper()
	reader, err := conn.Records(context.Background())
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	defer func() { _ = reader.Close() }()
	var rows []ingest.Row
	for {
		row, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return rows
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		rows = append(rows, row)
	}
}

// enrichmentCols are the seven columns an enriched row gains atomically (ANT-13)
// — all present together on a minted row, all absent on a money-only row.
var enrichmentCols = []string{
	"ConsumedQuantity", "ConsumedUnit", "SkuId", "SkuPriceId", "SkuMeter", "PricingQuantity", "PricingUnit",
}

// TestDiscoverAndRecords proves one month's cost report is fetched, joined with
// the usage report, and synthesized into FOCUS-1.4 records per the ANT rules —
// the cents→dollars shift, the workspace SubAccountId, the enriched token row
// (ConsumedQuantity/minted SKU/PricingQuantity), the batch-tier SkuPriceId, and
// money-only rows (session_usage, code_execution, credit, collision) carrying
// NONE of the enrichment columns (ANT-10 re-point + all-or-none atomicity).
func TestDiscoverAndRecords(t *testing.T) {
	_, baseURL := startFake(t, fixture)
	secret := credentials.NewSecret(fakeanthropic.AdminKey)

	periods, err := anthropiccost.Discover(context.Background(), http.DefaultClient, baseURL, anthropiccost.Name, secret, "", "2026-05")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(periods) != 1 || periods[0].Err != nil || periods[0].Conn == nil {
		t.Fatalf("periods = %+v, want one readable 2026-05 period", periods)
	}
	conn := periods[0].Conn
	if conn.Name() != "anthropic-cost" || string(conn.FOCUSVersion()) != "1.4" || conn.Month() != "2026-05" {
		t.Errorf("connector = %s/%s/%s", conn.Name(), conn.FOCUSVersion(), conn.Month())
	}
	if got, want := conn.SourceIdentity(), "api.anthropic.com/anthropic-cost/2026-05"; got != want {
		t.Errorf("SourceIdentity = %q, want %q", got, want)
	}

	rows := readAll(t, conn)
	if len(rows) != 10 {
		t.Fatalf("got %d records, want 10", len(rows))
	}

	// Row 0 — Opus uncached input, enriched by the summed-across-geo usage
	// (700000 us + 800000 eu = 1,500,000). SkuMeter is now the meter name, NOT
	// the model id (ANT-10 re-point); model identity stays in ChargeDescription.
	opus := rows[0].Record
	for col, want := range map[string]string{
		"BilledCost":          "12.345678",
		"EffectiveCost":       "12.345678",
		"ListCost":            "12.345678",
		"ContractedCost":      "12.345678",
		"BillingCurrency":     "USD",
		"ChargePeriodStart":   "2026-05-01T00:00:00Z",
		"BillingAccountId":    "api.anthropic.com/anthropic-cost",
		"ServiceProviderName": "Anthropic",
		"ServiceName":         "Claude API",
		"ChargeDescription":   "Claude Opus 4 Usage - Input Tokens",
		"SubAccountId":        "wrkspc_alpha",
		"ConsumedQuantity":    "1500000",
		"ConsumedUnit":        "Tokens",
		"SkuId":               "anthropic/claude-opus-4-6/uncached_input_tokens/0-200k",
		"SkuPriceId":          "anthropic/claude-opus-4-6/uncached_input_tokens/0-200k/standard",
		"SkuMeter":            "Input Tokens",
		"PricingQuantity":     "1.5",
		"PricingUnit":         "1000000 Tokens",
	} {
		if opus[col] != want {
			t.Errorf("opus row %s = %q, want %q", col, opus[col], want)
		}
	}
	if opus["SkuMeter"] == "claude-opus-4-6" {
		t.Error("SkuMeter must not carry the model id (ANT-10 re-point)")
	}

	// Row 2 — cache-write 5m: the nested cache_creation token type mints its
	// meter and SkuId with the dotted token_type.
	cache5m := rows[2].Record
	if cache5m["SkuMeter"] != "Cache Write Tokens (5m)" ||
		cache5m["SkuId"] != "anthropic/claude-opus-4-6/cache_creation.ephemeral_5m_input_tokens/0-200k" ||
		cache5m["ConsumedQuantity"] != "100000" {
		t.Errorf("cache-5m row wrong: meter=%q sku=%q qty=%q", cache5m["SkuMeter"], cache5m["SkuId"], cache5m["ConsumedQuantity"])
	}

	// Row 4 — Haiku BATCH tier: the SkuPriceId carries the batch tier.
	haiku := rows[4].Record
	if haiku["SkuPriceId"] != "anthropic/claude-haiku-4/uncached_input_tokens/0-200k/batch" ||
		haiku["ConsumedQuantity"] != "500000" {
		t.Errorf("haiku batch row wrong: skuPriceId=%q qty=%q", haiku["SkuPriceId"], haiku["ConsumedQuantity"])
	}

	// Row 5 — session_usage: money-only (BilledCost 5), NO enrichment columns.
	session := rows[5].Record
	if session["BilledCost"] != "5" {
		t.Errorf("session_usage BilledCost = %q, want 5", session["BilledCost"])
	}
	assertMoneyOnly(t, "session_usage", session)

	// Row 7 — credit: -250 cents → -2.5, no workspace, money-only.
	credit := rows[7].Record
	if credit["BilledCost"] != "-2.5" {
		t.Errorf("credit BilledCost = %q, want -2.5", credit["BilledCost"])
	}
	if _, ok := credit["SubAccountId"]; ok {
		t.Errorf("credit SubAccountId should be absent, got %q", credit["SubAccountId"])
	}
	assertMoneyOnly(t, "credit", credit)

	// Rows 8 & 9 — collision (two cost rows share one usage key): enrich NONE.
	assertMoneyOnly(t, "collision-A", rows[8].Record)
	assertMoneyOnly(t, "collision-B", rows[9].Record)
}

// assertMoneyOnly asserts a row carries NONE of the seven enrichment columns —
// the all-or-none guarantee for unjoined/ineligible rows.
func assertMoneyOnly(t *testing.T, label string, rec map[string]string) {
	t.Helper()
	for _, col := range enrichmentCols {
		if v, ok := rec[col]; ok {
			t.Errorf("%s row should be money-only but carries %s=%q", label, col, v)
		}
	}
}

// TestMoneyInvariantUnderEnrichment is the money-invariance proof (decision
// D33): the per-period and grand-total BilledCost equal the values computed from
// the cost fixtures ALONE (the pre-enrichment cents-shifts), and every row's
// four money columns are byte-identical to that value — enrichment decorated
// the rows without moving a cent.
func TestMoneyInvariantUnderEnrichment(t *testing.T) {
	_, baseURL := startFake(t, fixture)
	secret := credentials.NewSecret(fakeanthropic.AdminKey)

	perMonth := map[string]string{"2026-05": "117.845678", "2026-06": "92.005"}
	grand := decimal.Zero
	for _, month := range []string{"2026-05", "2026-06"} {
		periods, err := anthropiccost.Discover(context.Background(), http.DefaultClient, baseURL, anthropiccost.Name, secret, "", month)
		if err != nil {
			t.Fatalf("Discover %s: %v", month, err)
		}
		sum := decimal.Zero
		for _, row := range readAll(t, periods[0].Conn) {
			billed := decimal.RequireFromString(row.Record["BilledCost"])
			// The money quad must all equal BilledCost (ANT-4) regardless of
			// whether the row was enriched.
			for _, col := range []string{"EffectiveCost", "ListCost", "ContractedCost"} {
				if !decimal.RequireFromString(row.Record[col]).Equal(billed) {
					t.Errorf("%s %s = %q, want == BilledCost %s", month, col, row.Record[col], billed)
				}
			}
			sum = sum.Add(billed)
		}
		if want := decimal.RequireFromString(perMonth[month]); !sum.Equal(want) {
			t.Errorf("%s total BilledCost = %s, want %s (pre-enrichment fixture total)", month, sum, want)
		}
		grand = grand.Add(sum)
	}
	if want := decimal.RequireFromString("209.850678"); !grand.Equal(want) {
		t.Errorf("grand-total BilledCost = %s, want %s", grand, want)
	}
}

// TestAnomalySummaryReported proves the per-period orphan/collision surfaces are
// counted into a summary line (never emitted as FOCUS rows), and that a clean
// month reports nothing.
func TestAnomalySummaryReported(t *testing.T) {
	_, baseURL := startFake(t, fixture)
	secret := credentials.NewSecret(fakeanthropic.AdminKey)

	may, err := anthropiccost.Discover(context.Background(), http.DefaultClient, baseURL, anthropiccost.Name, secret, "", "2026-05")
	if err != nil {
		t.Fatalf("Discover May: %v", err)
	}
	s := may[0].Conn.AnomalySummary()
	if !strings.HasPrefix(s, "usage/cost reconciliation:") {
		t.Errorf("May summary = %q, want the reconciliation prefix", s)
	}
	for _, want := range []string{"collision", "cost-orphaned usage", "priority/flex-tier", "web-search"} {
		if !strings.Contains(s, want) {
			t.Errorf("May summary %q missing %q", s, want)
		}
	}

	june, err := anthropiccost.Discover(context.Background(), http.DefaultClient, baseURL, anthropiccost.Name, secret, "", "2026-06")
	if err != nil {
		t.Fatalf("Discover June: %v", err)
	}
	if got := june[0].Conn.AnomalySummary(); got != "" {
		t.Errorf("clean June should report no anomalies, got %q", got)
	}
}

// TestUsageFetchFailureDegradesPeriod proves a usage-fetch failure degrades the
// whole month to a per-period error (never a silently quantity-less ingest)
// while OTHER months still ingest.
func TestUsageFetchFailureDegradesPeriod(t *testing.T) {
	fake, baseURL := startFake(t, fixture)
	fake.UsageFailMonth = "2026-05"
	secret := credentials.NewSecret(fakeanthropic.AdminKey)

	periods, err := anthropiccost.Discover(context.Background(), http.DefaultClient, baseURL, anthropiccost.Name, secret, "2026-05", "")
	if err != nil {
		t.Fatalf("Discover aborted instead of degrading one period: %v", err)
	}
	byMonth := map[string]anthropiccost.Period{}
	for _, p := range periods {
		byMonth[p.Month] = p
	}
	if p := byMonth["2026-05"]; p.Err == nil || p.Conn != nil {
		t.Errorf("2026-05 should have degraded to a per-period error, got %+v", p)
	} else if !strings.Contains(p.Err.Error(), "HTTP 500") {
		t.Errorf("2026-05 error = %v, want the usage-fetch HTTP 500 failure", p.Err)
	}
	if p := byMonth["2026-06"]; p.Err != nil || p.Conn == nil {
		t.Errorf("2026-06 should still ingest, got %+v", p)
	}
}

// TestContentHashStableAndCursorFollowed proves the connector follows
// has_more/next_page (small page size) and that ContentHash — computed over
// data elements only — is stable across syncs despite the (potentially
// time-derived) cursor tokens.
func TestContentHashStableAndCursorFollowed(t *testing.T) {
	fake, baseURL := startFake(t, fixture)
	fake.PageSize = 1 // one bucket per page → force cursor following
	secret := credentials.NewSecret(fakeanthropic.AdminKey)

	discover := func() string {
		periods, err := anthropiccost.Discover(context.Background(), http.DefaultClient, baseURL, anthropiccost.Name, secret, "", "2026-05")
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		h, err := periods[0].Conn.ContentHash(context.Background())
		if err != nil {
			t.Fatalf("ContentHash: %v", err)
		}
		return h
	}
	h1 := discover()
	h2 := discover()
	if h1 != h2 {
		t.Errorf("ContentHash not stable across syncs: %q vs %q", h1, h2)
	}
	// The 2026-05 fixture has 2 buckets; a page size of 1 means the second
	// page carried a cursor.
	sawCursor := false
	for _, req := range fake.Requests() {
		if req.HadCursor {
			sawCursor = true
		}
	}
	if !sawCursor {
		t.Error("no request presented a cursor; pagination was not exercised")
	}
}

// TestEmptyMonthYieldsEmptyBatch proves a month with no data still produces a
// (record-less) connector with a stable content hash — the mechanism that
// lets a month restated-to-zero replace stale data.
func TestEmptyMonthYieldsEmptyBatch(t *testing.T) {
	_, baseURL := startFake(t, fixture)
	secret := credentials.NewSecret(fakeanthropic.AdminKey)

	periods, err := anthropiccost.Discover(context.Background(), http.DefaultClient, baseURL, anthropiccost.Name, secret, "", "2026-09")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(periods) != 1 || periods[0].Conn == nil {
		t.Fatalf("periods = %+v, want one empty 2026-09 period", periods)
	}
	if rows := readAll(t, periods[0].Conn); len(rows) != 0 {
		t.Errorf("empty month yielded %d rows, want 0", len(rows))
	}
	hash, _ := periods[0].Conn.ContentHash(context.Background())
	// sha256 of the empty byte stream.
	if hash != "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("empty-month ContentHash = %q, want the empty-stream digest", hash)
	}
}

// TestRateLimitedThenSucceeds proves the bounded 429 retry loop: the fake
// answers the first two requests with 429 + a tiny Retry-After, then serves,
// and the connector honors the header and eventually succeeds. No real
// sleeping occurs (Retry-After is 0.01s).
func TestRateLimitedThenSucceeds(t *testing.T) {
	fake, baseURL := startFake(t, fixture)
	fake.RateLimitN = 2
	fake.RetryAfter = "0.01"
	fake.PageSize = 100 // one page → one successful request after the 429s
	secret := credentials.NewSecret(fakeanthropic.AdminKey)

	periods, err := anthropiccost.Discover(context.Background(), http.DefaultClient, baseURL, anthropiccost.Name, secret, "", "2026-05")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if periods[0].Err != nil || periods[0].Conn == nil {
		t.Fatalf("period = %+v, want it to succeed after the retries", periods[0])
	}
	// The 429s only gate the cost endpoint; count cost requests specifically
	// (the connector also fetches the usage endpoint).
	if got := countPath(fake.Requests(), "/v1/organizations/cost_report"); got != 3 {
		t.Errorf("served %d cost requests, want 3 (two 429s then one success)", got)
	}
	if rows := readAll(t, periods[0].Conn); len(rows) != 10 {
		t.Errorf("got %d records after retrying, want 10", len(rows))
	}
}

// countPath counts served requests whose path equals p.
func countPath(reqs []fakeanthropic.Request, p string) int {
	n := 0
	for _, r := range reqs {
		if r.Path == p {
			n++
		}
	}
	return n
}

// TestRateLimitGivesUp proves the retry loop is BOUNDED: a fake that always
// returns 429 makes the connector give up after max429Retries and degrade the
// period, without hanging.
func TestRateLimitGivesUp(t *testing.T) {
	fake, baseURL := startFake(t, fixture)
	fake.RateLimitN = 999 // never stop rate-limiting
	fake.RetryAfter = "0.01"
	fake.PageSize = 100
	secret := credentials.NewSecret(fakeanthropic.AdminKey)

	periods, err := anthropiccost.Discover(context.Background(), http.DefaultClient, baseURL, anthropiccost.Name, secret, "", "2026-05")
	if err != nil {
		t.Fatalf("Discover aborted instead of degrading per period: %v", err)
	}
	if periods[0].Err == nil || !strings.Contains(periods[0].Err.Error(), "HTTP 429") {
		t.Errorf("give-up error = %v, want a bounded-retry HTTP 429 failure", periods[0].Err)
	}
	// One initial attempt plus max429Retries retries, all on the cost endpoint;
	// the usage endpoint is never reached because the cost fetch fails first.
	if got := countPath(fake.Requests(), "/v1/organizations/cost_report"); got != 6 {
		t.Errorf("served %d cost requests, want 6 (1 + 5 bounded retries)", got)
	}
	if got := countPath(fake.Requests(), "/v1/organizations/usage_report/messages"); got != 0 {
		t.Errorf("usage endpoint reached %d times, want 0 (cost fetch failed first)", got)
	}
}

// TestWrongKeyRejected proves a bad Admin key surfaces a per-period 401
// without echoing the key.
func TestWrongKeyRejected(t *testing.T) {
	_, baseURL := startFake(t, fixture)
	secret := credentials.NewSecret("sk-ant-admin01-WRONGKEY")

	periods, err := anthropiccost.Discover(context.Background(), http.DefaultClient, baseURL, anthropiccost.Name, secret, "", "2026-05")
	if err != nil {
		t.Fatalf("Discover aborted instead of degrading per period: %v", err)
	}
	if periods[0].Err == nil {
		t.Fatal("wrong key was accepted")
	}
	if got := periods[0].Err.Error(); !strings.Contains(got, "rejected (HTTP 401)") {
		t.Errorf("401 error = %q, want the rejected message", got)
	}
	if strings.Contains(periods[0].Err.Error(), "WRONGKEY") {
		t.Errorf("error echoed the API key: %v", periods[0].Err)
	}
}

// TestMissingCurrencyFailsPeriod proves a bucket with no currency fails
// rather than assuming USD (decision D23).
func TestMissingCurrencyFailsPeriod(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "2026-05.json"), []byte(`[
	  {"starting_at":"2026-05-01T00:00:00Z","ending_at":"2026-05-02T00:00:00Z",
	   "results":[{"amount":"100","description":"no currency"}]}
	]`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, baseURL := startFake(t, dir)
	secret := credentials.NewSecret(fakeanthropic.AdminKey)

	periods, err := anthropiccost.Discover(context.Background(), http.DefaultClient, baseURL, anthropiccost.Name, secret, "", "2026-05")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	reader, err := periods[0].Conn.Records(context.Background())
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	defer func() { _ = reader.Close() }()
	_, err = reader.Next()
	if err == nil || !strings.Contains(err.Error(), "refusing to assume USD") {
		t.Errorf("missing-currency error = %v, want the D23 refusal", err)
	}
}

// TestBaseURLValidation proves malformed and non-loopback http:// base URLs
// are refused.
func TestBaseURLValidation(t *testing.T) {
	secret := credentials.NewSecret(fakeanthropic.AdminKey)
	for _, bad := range []string{"", "not-a-url", "ftp://x", "http://api.anthropic.com"} {
		_, err := anthropiccost.Discover(context.Background(), http.DefaultClient, bad, anthropiccost.Name, secret, "", "2026-05")
		if err == nil {
			t.Errorf("Discover(%q) succeeded, want a base-url error", bad)
		}
	}
	// http:// loopback is allowed (the offline fake).
	_, baseURL := startFake(t, fixture)
	if _, err := anthropiccost.Discover(context.Background(), http.DefaultClient, baseURL, anthropiccost.Name, secret, "", "2026-05"); err != nil {
		t.Errorf("Discover(loopback http) = %v, want it allowed", err)
	}
}
