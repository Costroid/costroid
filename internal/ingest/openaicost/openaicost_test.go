// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package openaicost_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/credentials"
	"github.com/Costroid/costroid/internal/devtools/fakeopenai"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/ingest"
	"github.com/Costroid/costroid/internal/ingest/openaicost"
	"github.com/Costroid/costroid/internal/storage"
)

const fixture = "../../../testdata/openai-cost/fixture"

// floatCorruptibleValue is the fixture's day-1 output amount: it has more
// significant digits than a float64 can hold, so a float64 round-trip loses
// precision. See TestAmountSurvivesFloat64.
const floatCorruptibleValue = "123.4567890123456789"

func startFake(t *testing.T, dir string) (*fakeopenai.Handler, string) {
	t.Helper()
	h := fakeopenai.New(dir)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return h, srv.URL
}

// costRequests counts how many of the fake's served requests hit the costs
// endpoint (Discover also fetches the ten usage endpoints; assertions about
// the cost fetch must not count those).
func costRequests(h *fakeopenai.Handler) int {
	n := 0
	for _, r := range h.Requests() {
		if r.Path == "/v1/organization/costs" {
			n++
		}
	}
	return n
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

// enrichmentCols are the seven columns an enriched row gains atomically (OAI-11).
var enrichmentCols = []string{
	"ConsumedQuantity", "ConsumedUnit", "SkuId", "SkuPriceId", "SkuMeter", "PricingQuantity", "PricingUnit",
}

// TestDiscoverAndRecords proves one month's costs are fetched and synthesized
// into FOCUS-1.4 records per the OAI rules — dollars straight from the JSON
// literal, project SubAccountId, line_item ChargeDescription, the same-row
// quantity enrichment (OAI-11) with a VERBATIM line_item SkuId, and money-only
// rows (null-quantity credit, unknown-unit line item) carrying NONE of the
// enrichment columns.
func TestDiscoverAndRecords(t *testing.T) {
	_, baseURL := startFake(t, fixture)
	secret := credentials.NewSecret(fakeopenai.AdminKey)

	periods, err := openaicost.Discover(context.Background(), http.DefaultClient, baseURL, openaicost.Name, secret, "", "2026-05")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(periods) != 1 || periods[0].Err != nil || periods[0].Conn == nil {
		t.Fatalf("periods = %+v, want one readable 2026-05 period", periods)
	}
	conn := periods[0].Conn
	if got, want := conn.SourceIdentity(), "api.openai.com/openai-cost/2026-05"; got != want {
		t.Errorf("SourceIdentity = %q, want %q", got, want)
	}

	rows := readAll(t, conn)
	if len(rows) != 5 {
		t.Fatalf("got %d records, want 5", len(rows))
	}

	input := rows[0].Record
	for col, want := range map[string]string{
		"BilledCost":        "12.5",
		"EffectiveCost":     "12.5",
		"ListCost":          "12.5",
		"ContractedCost":    "12.5",
		"BillingCurrency":   "USD",
		"ChargePeriodStart": "2026-05-01T00:00:00Z",
		"BillingAccountId":  "api.openai.com/openai-cost",
		"ServiceName":       "OpenAI API",
		"ChargeDescription": "gpt-4o, input",
		"SubAccountId":      "proj_alpha",
		"ConsumedQuantity":  "1500000",
		"ConsumedUnit":      "Tokens",
		"SkuId":             "openai/gpt-4o, input", // line_item kept VERBATIM
		"SkuPriceId":        "openai/gpt-4o, input",
		"SkuMeter":          "Input Tokens",
		"PricingQuantity":   "1.5",
		"PricingUnit":       "1000000 Tokens",
	} {
		if input[col] != want {
			t.Errorf("input row %s = %q, want %q", col, input[col], want)
		}
	}

	// Row 2 — cached input maps to the Cache Read meter, SkuId still verbatim.
	cached := rows[2].Record
	if cached["SkuMeter"] != "Cache Read Tokens" ||
		cached["SkuId"] != "openai/gpt-4o, cached input" ||
		cached["ConsumedQuantity"] != "900000" || cached["PricingQuantity"] != "0.9" {
		t.Errorf("cached-input row wrong: meter=%q sku=%q qty=%q pq=%q",
			cached["SkuMeter"], cached["SkuId"], cached["ConsumedQuantity"], cached["PricingQuantity"])
	}

	// Row 3 — credit: -3.25, null quantity, project null → money-only.
	credit := rows[3].Record
	if credit["BilledCost"] != "-3.25" {
		t.Errorf("credit BilledCost = %q, want -3.25", credit["BilledCost"])
	}
	if _, ok := credit["SubAccountId"]; ok {
		t.Errorf("credit SubAccountId should be absent, got %q", credit["SubAccountId"])
	}
	assertMoneyOnly(t, "credit", credit)

	// Row 4 — unknown unit (quantity present, but line_item has no direction
	// suffix): stays money-only, never guessing a unit.
	unknown := rows[4].Record
	if unknown["ChargeDescription"] != "assistants api | file search" || unknown["BilledCost"] != "5" {
		t.Errorf("unknown-unit row wrong: desc=%q billed=%q", unknown["ChargeDescription"], unknown["BilledCost"])
	}
	assertMoneyOnly(t, "unknown-unit", unknown)
}

// assertMoneyOnly asserts a row carries NONE of the seven enrichment columns.
func assertMoneyOnly(t *testing.T, label string, rec map[string]string) {
	t.Helper()
	for _, col := range enrichmentCols {
		if v, ok := rec[col]; ok {
			t.Errorf("%s row should be money-only but carries %s=%q", label, col, v)
		}
	}
}

// TestQuantitySurvivesFloat64 proves the token quantity is built from its exact
// JSON literal, never through float64 (the day-1 output quantity has more
// precision than a float64 holds). Fails if someone decodes quantity as float64.
func TestQuantitySurvivesFloat64(t *testing.T) {
	const corruptible = "1234567890123456789"
	f, err := strconv.ParseFloat(corruptible, 64)
	if err != nil {
		t.Fatalf("parsing the corruptible value: %v", err)
	}
	if viaFloat := decimal.NewFromFloat(f).String(); viaFloat == corruptible {
		t.Fatalf("test value %q does not corrupt under float64 (got %q)", corruptible, viaFloat)
	}

	_, baseURL := startFake(t, fixture)
	secret := credentials.NewSecret(fakeopenai.AdminKey)
	periods, err := openaicost.Discover(context.Background(), http.DefaultClient, baseURL, openaicost.Name, secret, "", "2026-05")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	rows := readAll(t, periods[0].Conn)
	if got := rows[1].Record["ConsumedQuantity"]; got != corruptible {
		t.Fatalf("ConsumedQuantity = %q, want the exact literal %q", got, corruptible)
	}
}

// TestUnknownUnitOrphanSummary proves the unknown-unit quantity row is counted
// into the per-period orphan summary (never guessed into a FOCUS unit).
func TestUnknownUnitOrphanSummary(t *testing.T) {
	_, baseURL := startFake(t, fixture)
	secret := credentials.NewSecret(fakeopenai.AdminKey)
	periods, err := openaicost.Discover(context.Background(), http.DefaultClient, baseURL, openaicost.Name, secret, "", "2026-05")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	s := periods[0].Conn.AnomalySummary()
	if !strings.HasPrefix(s, "usage/cost reconciliation:") || !strings.Contains(s, "unit could not be safely derived") {
		t.Errorf("summary = %q, want the unknown-unit orphan line", s)
	}
	// A clean month (June: both rows recognized) reports nothing.
	june, err := openaicost.Discover(context.Background(), http.DefaultClient, baseURL, openaicost.Name, secret, "", "2026-06")
	if err != nil {
		t.Fatalf("Discover June: %v", err)
	}
	if got := june[0].Conn.AnomalySummary(); got != "" {
		t.Errorf("clean June should report no anomalies, got %q", got)
	}
}

// TestMoneyInvariantUnderEnrichment proves per-period and grand-total BilledCost
// equal the pre-enrichment fixture totals — enrichment moved no money (D33).
func TestMoneyInvariantUnderEnrichment(t *testing.T) {
	_, baseURL := startFake(t, fixture)
	secret := credentials.NewSecret(fakeopenai.AdminKey)
	perMonth := map[string]string{"2026-05": "139.7067890123456789", "2026-06": "52.6789"}
	for month, want := range perMonth {
		periods, err := openaicost.Discover(context.Background(), http.DefaultClient, baseURL, openaicost.Name, secret, "", month)
		if err != nil {
			t.Fatalf("Discover %s: %v", month, err)
		}
		sum := decimal.Zero
		for _, row := range readAll(t, periods[0].Conn) {
			sum = sum.Add(decimal.RequireFromString(row.Record["BilledCost"]))
		}
		if !sum.Equal(decimal.RequireFromString(want)) {
			t.Errorf("%s total BilledCost = %s, want %s", month, sum, want)
		}
	}
}

// TestAmountSurvivesFloat64 is the load-bearing exactness proof: the day-1
// output amount has more precision than a float64 holds, so decoding it
// through float64 would corrupt it. The connector must preserve the literal
// exactly. This test FAILS if someone "simplifies" the connector to decode
// amount.value as a float64.
func TestAmountSurvivesFloat64(t *testing.T) {
	// Guard: confirm the chosen value actually corrupts under float64, so
	// this test is meaningful.
	f, err := strconv.ParseFloat(floatCorruptibleValue, 64)
	if err != nil {
		t.Fatalf("parsing the corruptible value: %v", err)
	}
	viaFloat := decimal.NewFromFloat(f).String()
	if viaFloat == floatCorruptibleValue {
		t.Fatalf("test value %q does not corrupt under float64 (got %q) — choose a harder value",
			floatCorruptibleValue, viaFloat)
	}

	_, baseURL := startFake(t, fixture)
	secret := credentials.NewSecret(fakeopenai.AdminKey)
	periods, err := openaicost.Discover(context.Background(), http.DefaultClient, baseURL, openaicost.Name, secret, "", "2026-05")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	rows := readAll(t, periods[0].Conn)

	got := rows[1].Record["BilledCost"] // day-1 output row
	if got != floatCorruptibleValue {
		t.Fatalf("BilledCost = %q, want the exact literal %q (float64 would give %q)",
			got, floatCorruptibleValue, viaFloat)
	}
}

// TestWrongKeyRejected proves a bad admin key surfaces a per-period 401
// without echoing the key.
func TestWrongKeyRejected(t *testing.T) {
	_, baseURL := startFake(t, fixture)
	secret := credentials.NewSecret("sk-admin-WRONGKEY")

	periods, err := openaicost.Discover(context.Background(), http.DefaultClient, baseURL, openaicost.Name, secret, "", "2026-05")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if periods[0].Err == nil {
		t.Fatal("wrong key was accepted")
	}
	if !strings.Contains(periods[0].Err.Error(), "rejected (HTTP 401)") {
		t.Errorf("401 error = %q, want the rejected message", periods[0].Err)
	}
	if strings.Contains(periods[0].Err.Error(), "WRONGKEY") {
		t.Errorf("error echoed the API key: %v", periods[0].Err)
	}
}

// TestNonDayBucketFailsPeriod proves a bucket whose span is not exactly one
// day degrades its month to a per-period error naming the bucket, rather than
// being mis-synthesized (the tolerate-unknown-bucket_width path). A two-day
// bucket (start 2026-05-01, end 2026-05-03) is served for 2026-05.
func TestNonDayBucketFailsPeriod(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "2026-05.json"), []byte(`[
	  {"object":"bucket","start_time":1777593600,"end_time":1777766400,
	   "results":[{"amount":{"value":1.0,"currency":"usd"},"line_item":"two-day bucket"}]}
	]`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, baseURL := startFake(t, dir)
	secret := credentials.NewSecret(fakeopenai.AdminKey)

	periods, err := openaicost.Discover(context.Background(), http.DefaultClient, baseURL, openaicost.Name, secret, "", "2026-05")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	reader, err := periods[0].Conn.Records(context.Background())
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	defer func() { _ = reader.Close() }()
	_, err = reader.Next()
	if err == nil || !strings.Contains(err.Error(), "one-day interval") {
		t.Errorf("non-day bucket error = %v, want the one-day-interval refusal", err)
	}
	// The error names the offending bucket (its start/end).
	if err != nil && (!strings.Contains(err.Error(), "1777593600") || !strings.Contains(err.Error(), "1777766400")) {
		t.Errorf("non-day bucket error %v does not name the bucket's start/end", err)
	}
}

// TestRateLimitedThenSucceeds proves the bounded 429 retry loop: the fake
// answers the first two requests with 429 + a tiny Retry-After, then serves,
// and the connector honors the header (parsed as delta-seconds) and
// eventually succeeds. No real sleeping occurs (Retry-After is 0.01s).
func TestRateLimitedThenSucceeds(t *testing.T) {
	fake, baseURL := startFake(t, fixture)
	fake.RateLimitN = 2
	fake.RetryAfter = "0.01"
	fake.PageSize = 100 // one page → one successful request after the 429s
	secret := credentials.NewSecret(fakeopenai.AdminKey)

	periods, err := openaicost.Discover(context.Background(), http.DefaultClient, baseURL, openaicost.Name, secret, "", "2026-05")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if periods[0].Err != nil || periods[0].Conn == nil {
		t.Fatalf("period = %+v, want it to succeed after the retries", periods[0])
	}
	// Count only the COSTS-path requests: the rate-limit gate applies to the cost
	// endpoint (Discover also fetches the ten usage endpoints, which are not
	// throttled). Two 429s then one success on the costs endpoint.
	if got := costRequests(fake); got != 3 {
		t.Errorf("served %d costs requests, want 3 (two 429s then one success)", got)
	}
	if rows := readAll(t, periods[0].Conn); len(rows) != 5 {
		t.Errorf("got %d records after retrying, want 5", len(rows))
	}
}

// TestRateLimitGivesUp proves the retry loop is BOUNDED: a fake that always
// returns 429 makes the connector give up after max429Retries and degrade the
// period, without hanging.
func TestRateLimitGivesUp(t *testing.T) {
	fake, baseURL := startFake(t, fixture)
	fake.RateLimitN = 999 // never stop rate-limiting
	fake.RetryAfter = "0.01"
	fake.PageSize = 100
	secret := credentials.NewSecret(fakeopenai.AdminKey)

	periods, err := openaicost.Discover(context.Background(), http.DefaultClient, baseURL, openaicost.Name, secret, "", "2026-05")
	if err != nil {
		t.Fatalf("Discover aborted instead of degrading per period: %v", err)
	}
	if periods[0].Err == nil || !strings.Contains(periods[0].Err.Error(), "HTTP 429") {
		t.Errorf("give-up error = %v, want a bounded-retry HTTP 429 failure", periods[0].Err)
	}
	// One initial attempt plus max429Retries retries.
	if got := len(fake.Requests()); got != 6 {
		t.Errorf("served %d requests, want 6 (1 + 5 bounded retries)", got)
	}
}

// TestMissingCurrencyFailsPeriod proves a bucket amount with no currency
// fails rather than assuming USD (decision D23).
func TestMissingCurrencyFailsPeriod(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "2026-05.json"), []byte(`[
	  {"object":"bucket","start_time":1777593600,"end_time":1777680000,
	   "results":[{"amount":{"value":1.0},"line_item":"no currency"}]}
	]`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, baseURL := startFake(t, dir)
	secret := credentials.NewSecret(fakeopenai.AdminKey)

	periods, err := openaicost.Discover(context.Background(), http.DefaultClient, baseURL, openaicost.Name, secret, "", "2026-05")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	reader, err := periods[0].Conn.Records(context.Background())
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	defer func() { _ = reader.Close() }()
	if _, err := reader.Next(); err == nil || !strings.Contains(err.Error(), "refusing to assume USD") {
		t.Errorf("missing-currency error = %v, want the D23 refusal", err)
	}
}

// --- OpenAI Usage-API metrics ---

// day1 is 2026-05-01T00:00:00Z (UTC midnight of Unix start_time 1777593600, the
// value the usage fixtures below use).
var day1 = time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

// writeUsage writes one canned usage fixture <month>.usage.<endpoint>.json.
func writeUsage(t *testing.T, dir, month, endpoint, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, month+".usage."+endpoint+".json"), []byte(body), 0o644); err != nil {
		t.Fatalf("writing usage fixture %s.usage.%s.json: %v", month, endpoint, err)
	}
}

// writeCost writes one canned cost fixture <month>.json.
func writeCost(t *testing.T, dir, month, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, month+".json"), []byte(body), 0o644); err != nil {
		t.Fatalf("writing cost fixture %s.json: %v", month, err)
	}
}

// discoverMonth discovers exactly one month from dir through the fake and returns
// its connector (the real fetch → decode → usage-shape-assert path).
func discoverMonth(t *testing.T, h *fakeopenai.Handler, baseURL, month string) *openaicost.Connector {
	t.Helper()
	secret := credentials.NewSecret(fakeopenai.AdminKey)
	periods, err := openaicost.Discover(context.Background(), http.DefaultClient, baseURL, openaicost.Name, secret, "", month)
	if err != nil {
		t.Fatalf("Discover %s: %v", month, err)
	}
	if len(periods) != 1 || periods[0].Err != nil || periods[0].Conn == nil {
		t.Fatalf("periods = %+v, want one readable %s period", periods, month)
	}
	return periods[0].Conn
}

// metricKeys renders a metric slice as a SORTED, comparable key list — order-
// independent so a complete-set assertion couples both sides field-by-field.
func metricKeys(ms []storage.Metric) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, fmt.Sprintf("%s|%s|%s|%s|%s|%s",
			m.ChargePeriodStart.UTC().Format(time.RFC3339), m.ServiceName, m.ServiceTier, m.MetricName, m.Unit, m.Quantity.String()))
	}
	sort.Strings(out)
	return out
}

// assertMetricSet asserts got equals want as a COMPLETE set (not a subset), so a
// spuriously-emitted field (including a token) reddens.
func assertMetricSet(t *testing.T, label string, got, want []storage.Metric) {
	t.Helper()
	g, w := metricKeys(got), metricKeys(want)
	if len(g) != len(w) {
		t.Fatalf("%s: %d metric(s) %v, want %d %v", label, len(g), g, len(w), w)
	}
	for i := range g {
		if g[i] != w[i] {
			t.Errorf("%s: metric %d = %q, want %q", label, i, g[i], w[i])
		}
	}
}

func req(model, qty string) storage.Metric {
	return storage.Metric{ChargePeriodStart: day1, ServiceName: model, ServiceTier: "", MetricName: "num_model_requests", Unit: "Requests", Quantity: decimal.RequireFromString(qty)}
}

// TestUsageTokenDoubleCountImpossible (mandated test #1) proves a token count can
// NEVER be surfaced into usage_metrics: a completions bucket carrying ALL FIVE
// documented token fields plus num_model_requests emits only the Requests row and
// ZERO Unit=="Tokens" rows. The embeddings and moderations fixtures also carry an
// input_tokens field so their token-skip is not vacuously tested. This is
// structural — the closed-whitelist usageResult has no token field, so the token
// literals are un-decodable, not merely filtered.
func TestUsageTokenDoubleCountImpossible(t *testing.T) {
	dir := t.TempDir()
	writeUsage(t, dir, "2026-05", "completions", `[
	  {"start_time":1777593600,"results":[{"model":"gpt-4o",
	    "num_model_requests":10,
	    "input_tokens":111,"output_tokens":222,"input_cached_tokens":333,
	    "input_audio_tokens":444,"output_audio_tokens":555}]}]`)
	writeUsage(t, dir, "2026-05", "embeddings", `[
	  {"start_time":1777593600,"results":[{"model":"text-embedding-3-large","num_model_requests":4,"input_tokens":999}]}]`)
	writeUsage(t, dir, "2026-05", "moderations", `[
	  {"start_time":1777593600,"results":[{"model":"omni-moderation-latest","num_model_requests":2,"input_tokens":888}]}]`)

	h := fakeopenai.New(dir)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	metrics := discoverMonth(t, h, srv.URL, "2026-05").UsageMetrics()

	for _, m := range metrics {
		if m.Unit == "Tokens" {
			t.Errorf("a token count leaked into usage_metrics: %+v (token fields are structurally un-emittable)", m)
		}
	}
	// No cost fixture → zero USG-3 orphans, so the set is EXACTLY the three
	// Requests rows.
	assertMetricSet(t, "token-double-count", metrics, []storage.Metric{
		req("gpt-4o", "10"), req("text-embedding-3-large", "4"), req("omni-moderation-latest", "2"),
	})
}

// TestUsagePerEndpointCompleteSet (mandated test #2) asserts the EXACT complete
// set of storage.Metric tuples each of the ten endpoints produces, values
// coupled to the fixture on both sides. Each case is ISOLATED (only that one
// usage file, NO cost fixture) so the cost fetch returns empty buckets and USG-3
// contributes zero rows — UsageMetrics() then equals exactly the endpoint's rows.
// A stray token field on each fixture proves no token can slip into the set.
func TestUsagePerEndpointCompleteSet(t *testing.T) {
	sessions := storage.Metric{ChargePeriodStart: day1, ServiceName: "OpenAI API", ServiceTier: "", MetricName: "num_sessions", Unit: "Sessions", Quantity: decimal.RequireFromString("8")}
	special := func(model, metric, unit, qty string) storage.Metric {
		return storage.Metric{ChargePeriodStart: day1, ServiceName: model, ServiceTier: "", MetricName: metric, Unit: unit, Quantity: decimal.RequireFromString(qty)}
	}
	cases := []struct {
		endpoint string
		body     string
		want     []storage.Metric
	}{
		{"completions", `[{"start_time":1777593600,"results":[{"model":"gpt-4o","num_model_requests":7,"input_tokens":5}]}]`,
			[]storage.Metric{req("gpt-4o", "7")}},
		{"embeddings", `[{"start_time":1777593600,"results":[{"model":"text-embedding-3-small","num_model_requests":3,"input_tokens":5}]}]`,
			[]storage.Metric{req("text-embedding-3-small", "3")}},
		{"moderations", `[{"start_time":1777593600,"results":[{"model":"omni-moderation-latest","num_model_requests":5}]}]`,
			[]storage.Metric{req("omni-moderation-latest", "5")}},
		{"images", `[{"start_time":1777593600,"results":[{"model":"gpt-image-1","num_model_requests":2,"images":9,"input_tokens":5}]}]`,
			[]storage.Metric{req("gpt-image-1", "2"), special("gpt-image-1", "images", "Images", "9")}},
		{"audio_speeches", `[{"start_time":1777593600,"results":[{"model":"tts-1","num_model_requests":4,"characters":2500}]}]`,
			[]storage.Metric{req("tts-1", "4"), special("tts-1", "characters", "Characters", "2500")}},
		{"audio_transcriptions", `[{"start_time":1777593600,"results":[{"model":"whisper-1","num_model_requests":6,"seconds":720}]}]`,
			[]storage.Metric{req("whisper-1", "6"), special("whisper-1", "seconds", "Seconds", "720")}},
		{"code_interpreter_sessions", `[{"start_time":1777593600,"results":[{"num_sessions":8}]}]`,
			[]storage.Metric{sessions}},
		{"vector_stores", `[{"start_time":1777593600,"results":[{"usage_bytes":1099511627776,"input_tokens":5}]}]`,
			[]storage.Metric{special("OpenAI API", "usage_bytes", "Bytes", "1099511627776")}},
		{"web_search_calls", `[{"start_time":1777593600,"results":[{"model":"gpt-4o-search-preview","num_requests":11,"num_model_requests":99,"input_tokens":5}]}]`,
			[]storage.Metric{special("gpt-4o-search-preview", "web_search_num_requests", "Calls", "11")}},
		{"file_search_calls", `[{"start_time":1777593600,"results":[{"num_requests":12,"input_tokens":5}]}]`,
			[]storage.Metric{special("OpenAI API", "file_search_num_requests", "Calls", "12")}},
	}
	for _, tc := range cases {
		t.Run(tc.endpoint, func(t *testing.T) {
			dir := t.TempDir()
			writeUsage(t, dir, "2026-05", tc.endpoint, tc.body)
			h := fakeopenai.New(dir)
			srv := httptest.NewServer(h)
			t.Cleanup(srv.Close)
			got := discoverMonth(t, h, srv.URL, "2026-05").UsageMetrics()
			assertMetricSet(t, tc.endpoint, got, tc.want)
		})
	}
}

// TestUsagePerModelSplit (mandated test #3) proves one metric row per (day, model)
// on a multi-model completions bucket, with ServiceName carrying the model, and
// the null/absent-model fallback to "OpenAI API".
func TestUsagePerModelSplit(t *testing.T) {
	dir := t.TempDir()
	writeUsage(t, dir, "2026-05", "completions", `[{"start_time":1777593600,"results":[
	  {"model":"gpt-4o","num_model_requests":10},
	  {"model":"gpt-4.1","num_model_requests":20},
	  {"num_model_requests":5}
	]}]`)
	h := fakeopenai.New(dir)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	got := discoverMonth(t, h, srv.URL, "2026-05").UsageMetrics()
	assertMetricSet(t, "per-model split", got, []storage.Metric{
		req("gpt-4o", "10"), req("gpt-4.1", "20"), req("OpenAI API", "5"),
	})
}

// TestUsageOrphansPreservedAndDistinct (mandated test #4) proves the existing
// USG-3 "Unknown" cost-orphan rows STILL emit alongside the new usage-endpoint
// metrics, and that a special-unit row (Images) is a DISTINCT (metric_name, unit)
// tuple from the USG-3 row (no silent SUM-merge).
func TestUsageOrphansPreservedAndDistinct(t *testing.T) {
	dir := t.TempDir()
	// A cost row with a quantity but no direction suffix → USG-3 "Unknown" orphan.
	writeCost(t, dir, "2026-05", `[{"object":"bucket","start_time":1777593600,"end_time":1777680000,
	  "results":[{"amount":{"value":5.0,"currency":"usd"},"line_item":"assistants api | file search","quantity":42}]}]`)
	writeUsage(t, dir, "2026-05", "images", `[{"start_time":1777593600,"results":[{"model":"gpt-image-1","num_model_requests":2,"images":9}]}]`)
	h := fakeopenai.New(dir)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	metrics := discoverMonth(t, h, srv.URL, "2026-05").UsageMetrics()

	usg3 := storage.Metric{ChargePeriodStart: day1, ServiceName: "OpenAI API", ServiceTier: "", MetricName: "assistants api | file search", Unit: "Unknown", Quantity: decimal.RequireFromString("42")}
	images := storage.Metric{ChargePeriodStart: day1, ServiceName: "gpt-image-1", ServiceTier: "", MetricName: "images", Unit: "Images", Quantity: decimal.RequireFromString("9")}
	// Distinctness is covered by the complete-set assertion: the USG-3 and
	// Images rows carry DIFFERENT (metric_name, unit) tuples, so DailyUsageMetrics
	// can never silently merge them.
	assertMetricSet(t, "orphans preserved", metrics, []storage.Metric{usg3, req("gpt-image-1", "2"), images})
}

// TestUsageSearchRequestsSourceQualified proves the two search-call endpoints'
// upstream num_requests fields are stored as endpoint-qualified metric names.
func TestUsageSearchRequestsSourceQualified(t *testing.T) {
	dir := t.TempDir()
	writeUsage(t, dir, "2026-05", "web_search_calls", `[{"start_time":1777593600,"results":[{"num_requests":7,"num_model_requests":70}]}]`)
	writeUsage(t, dir, "2026-05", "file_search_calls", `[{"start_time":1777593600,"results":[{"num_requests":8}]}]`)
	h := fakeopenai.New(dir)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	got := discoverMonth(t, h, srv.URL, "2026-05").UsageMetrics()

	assertMetricSet(t, "search requests source-qualified", got, []storage.Metric{
		{ChargePeriodStart: day1, ServiceName: "OpenAI API", ServiceTier: "", MetricName: "web_search_num_requests", Unit: "Calls", Quantity: decimal.RequireFromString("7")},
		{ChargePeriodStart: day1, ServiceName: "OpenAI API", ServiceTier: "", MetricName: "file_search_num_requests", Unit: "Calls", Quantity: decimal.RequireFromString("8")},
	})
}

// TestUsageSearchRequestsStoreCollisionAvoided proves the source-qualified names
// are not just cosmetic: after a store round-trip, model-less web_search_calls
// and file_search_calls rows with the same ServiceName remain two daily metric
// rows instead of SUM-merging under a shared bare num_requests metric.
func TestUsageSearchRequestsStoreCollisionAvoided(t *testing.T) {
	dir := t.TempDir()
	writeUsage(t, dir, "2026-05", "web_search_calls", `[{"start_time":1777593600,"results":[{"num_requests":7,"num_model_requests":70}]}]`)
	writeUsage(t, dir, "2026-05", "file_search_calls", `[{"start_time":1777593600,"results":[{"num_requests":8}]}]`)
	h := fakeopenai.New(dir)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	metrics := discoverMonth(t, h, srv.URL, "2026-05").UsageMetrics()
	store, err := storage.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	batch := storage.UsageBatch{
		Connector:      openaicost.Name,
		SourceIdentity: "api.openai.com/openai-cost/2026-05",
		TenantID:       focus.DefaultTenant,
	}
	if err := store.ReplaceUsageBatch(context.Background(), batch, metrics); err != nil {
		t.Fatalf("ReplaceUsageBatch: %v", err)
	}
	rows, err := store.DailyUsageMetrics(context.Background(), focus.DefaultTenant, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyUsageMetrics: %v", err)
	}
	wantRows := []string{
		"2026-05-01T00:00:00Z|OpenAI API||file_search_num_requests|Calls|8",
		"2026-05-01T00:00:00Z|OpenAI API||web_search_num_requests|Calls|7",
	}
	if len(rows) != len(wantRows) {
		t.Fatalf("stored search metrics = %+v, want %d distinct rows", rows, len(wantRows))
	}
	for i, w := range wantRows {
		gotKey := fmt.Sprintf("%s|%s|%s|%s|%s|%s",
			rows[i].Date.UTC().Format(time.RFC3339), rows[i].ServiceName, rows[i].ServiceTier,
			rows[i].MetricName, rows[i].Unit, rows[i].Quantity.String())
		if gotKey != w {
			t.Errorf("stored search metric row %d = %q, want %q", i, gotKey, w)
		}
	}
}

// TestUsageFetchLeavesCostAndTokensInvariant (mandated test #5) proves adding the
// usage fetch is byte-invariant for cost_records: the same month's BilledCost
// grand total and enriched token total are IDENTICAL to the pre-slice values (the
// usage path never touches cost_records). The fixture dir carries usage files, so
// the usage endpoints genuinely run alongside the cost fetch.
func TestUsageFetchLeavesCostAndTokensInvariant(t *testing.T) {
	_, baseURL := startFake(t, fixture)
	secret := credentials.NewSecret(fakeopenai.AdminKey)
	periods, err := openaicost.Discover(context.Background(), http.DefaultClient, baseURL, openaicost.Name, secret, "", "2026-05")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	billed := decimal.Zero
	tokens := decimal.Zero
	for _, row := range readAll(t, periods[0].Conn) {
		billed = billed.Add(decimal.RequireFromString(row.Record["BilledCost"]))
		if row.Record["ConsumedUnit"] == "Tokens" {
			tokens = tokens.Add(decimal.RequireFromString(row.Record["ConsumedQuantity"]))
		}
	}
	if !billed.Equal(decimal.RequireFromString("139.7067890123456789")) {
		t.Errorf("BilledCost total = %s, want the pre-slice 139.7067890123456789 (usage fetch moved no money)", billed)
	}
	if !tokens.Equal(decimal.RequireFromString("1234567890125856789")) {
		t.Errorf("enriched token total = %s, want the pre-slice 1234567890125856789 (usage fetch moved no token count)", tokens)
	}
}

// TestUsagePerFieldDegrade (mandated test #7) proves the per-FIELD degrade inside
// a SUCCESSFULLY-fetched bucket: a row whose `images` literal is malformed (a JSON
// string) and another whose `images` field is absent both emit their valid
// num_model_requests row but NO fabricated/zero Images row. Distinct from the
// usage-endpoint-FAILURE degrade.
func TestUsagePerFieldDegrade(t *testing.T) {
	dir := t.TempDir()
	writeUsage(t, dir, "2026-05", "images", `[{"start_time":1777593600,"results":[
	  {"model":"gpt-image-1","num_model_requests":3,"images":"not-a-number"},
	  {"model":"gpt-image-2","num_model_requests":4}
	]}]`)
	h := fakeopenai.New(dir)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	metrics := discoverMonth(t, h, srv.URL, "2026-05").UsageMetrics()
	for _, m := range metrics {
		if m.Unit == "Images" {
			t.Errorf("a malformed/absent images field fabricated a row: %+v", m)
		}
	}
	// The valid num_model_requests siblings still emit.
	assertMetricSet(t, "per-field degrade", metrics, []storage.Metric{
		req("gpt-image-1", "3"), req("gpt-image-2", "4"),
	})
}

// TestUsagePaginationFollowsCursor (part of mandated test #8) proves the connector
// genuinely follows has_more/next_page on a usage endpoint: a three-bucket
// completions fixture served one bucket per page (PageSize=1) yields ALL three
// days' rows.
func TestUsagePaginationFollowsCursor(t *testing.T) {
	dir := t.TempDir()
	writeUsage(t, dir, "2026-05", "completions", `[
	  {"start_time":1777593600,"results":[{"model":"gpt-4o","num_model_requests":1}]},
	  {"start_time":1777680000,"results":[{"model":"gpt-4o","num_model_requests":2}]},
	  {"start_time":1777766400,"results":[{"model":"gpt-4o","num_model_requests":3}]}
	]`)
	h := fakeopenai.New(dir)
	h.PageSize = 1 // force ≥3 pages on this endpoint
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	metrics := discoverMonth(t, h, srv.URL, "2026-05").UsageMetrics()
	total := decimal.Zero
	for _, m := range metrics {
		if m.MetricName == "num_model_requests" {
			total = total.Add(m.Quantity)
		}
	}
	if !total.Equal(decimal.RequireFromString("6")) {
		t.Errorf("paginated num_model_requests total = %s, want 6 (1+2+3 across three pages)", total)
	}
	if len(metrics) != 3 {
		t.Errorf("got %d metric rows, want 3 (one per paged bucket)", len(metrics))
	}
}

// TestUsageCardinalRulePathsOnly (mandated test #10) proves the connector issues
// requests ONLY to /costs and the ten usage paths — never /projects, /users, or
// any ID→name resolution call (Cardinal Rule D7).
func TestUsageCardinalRulePathsOnly(t *testing.T) {
	allowed := map[string]bool{"/v1/organization/costs": true}
	for _, name := range []string{
		"completions", "embeddings", "moderations", "images",
		"audio_speeches", "audio_transcriptions", "code_interpreter_sessions",
		"vector_stores", "web_search_calls", "file_search_calls",
	} {
		allowed["/v1/organization/usage/"+name] = true
	}
	h, baseURL := startFake(t, fixture)
	discoverMonth(t, h, baseURL, "2026-05")
	sawUsage := map[string]bool{}
	for _, r := range h.Requests() {
		if !allowed[r.Path] {
			t.Errorf("connector hit a forbidden path %q (only /costs and the 10 usage paths are permitted)", r.Path)
		}
		if strings.HasPrefix(r.Path, "/v1/organization/usage/") {
			sawUsage[r.Path] = true
		}
	}
	if len(sawUsage) != 10 {
		t.Errorf("connector fetched %d distinct usage paths, want all 10: %v", len(sawUsage), sawUsage)
	}
}
