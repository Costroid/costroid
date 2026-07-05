// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package openaicost_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/credentials"
	"github.com/Costroid/costroid/internal/devtools/fakeopenai"
	"github.com/Costroid/costroid/internal/ingest"
	"github.com/Costroid/costroid/internal/ingest/openaicost"
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

// TestDiscoverAndRecords proves one month's costs are fetched and synthesized
// into FOCUS-1.4 records per the OAI rules — dollars straight from the JSON
// literal, project SubAccountId, line_item ChargeDescription, Unix-second
// buckets → RFC 3339, and a negative credit passing through.
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
	if len(rows) != 3 {
		t.Fatalf("got %d records, want 3", len(rows))
	}

	input := rows[0].Record
	for col, want := range map[string]string{
		"BilledCost":          "12.5",
		"EffectiveCost":       "12.5",
		"ListCost":            "12.5",
		"ContractedCost":      "12.5",
		"BillingCurrency":     "USD",
		"ChargeCategory":      "Usage",
		"ChargeFrequency":     "Usage-Based",
		"ChargePeriodStart":   "2026-05-01T00:00:00Z",
		"ChargePeriodEnd":     "2026-05-02T00:00:00Z",
		"BillingPeriodStart":  "2026-05-01T00:00:00Z",
		"BillingPeriodEnd":    "2026-06-01T00:00:00Z",
		"BillingAccountId":    "api.openai.com/openai-cost",
		"ServiceProviderName": "OpenAI",
		"InvoiceIssuerName":   "OpenAI",
		"ServiceName":         "OpenAI API",
		"ServiceCategory":     "AI and Machine Learning",
		"ChargeDescription":   "gpt-4o, input",
		"SubAccountId":        "proj_alpha",
	} {
		if input[col] != want {
			t.Errorf("input row %s = %q, want %q", col, input[col], want)
		}
	}

	// The credit row: -3.25, project null → no SubAccountId.
	credit := rows[2].Record
	if credit["BilledCost"] != "-3.25" {
		t.Errorf("credit BilledCost = %q, want -3.25", credit["BilledCost"])
	}
	if _, ok := credit["SubAccountId"]; ok {
		t.Errorf("credit SubAccountId should be absent, got %q", credit["SubAccountId"])
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
	if got := len(fake.Requests()); got != 3 {
		t.Errorf("served %d requests, want 3 (two 429s then one success)", got)
	}
	if rows := readAll(t, periods[0].Conn); len(rows) != 3 {
		t.Errorf("got %d records after retrying, want 3", len(rows))
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
