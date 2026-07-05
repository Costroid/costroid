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

// TestDiscoverAndRecords proves one month's cost report is fetched and
// synthesized into FOCUS-1.4 records per the ANT rules — including the
// cents→dollars shift, the workspace SubAccountId, the model SkuMeter, and a
// negative credit passing through unchanged.
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
	if len(rows) != 3 {
		t.Fatalf("got %d records, want 3 (2 on day 1 + 1 credit)", len(rows))
	}

	// Day-1 Opus row: 1234.5678 cents → 12.345678 dollars.
	opus := rows[0].Record
	for col, want := range map[string]string{
		"BilledCost":          "12.345678",
		"EffectiveCost":       "12.345678",
		"ListCost":            "12.345678",
		"ContractedCost":      "12.345678",
		"BillingCurrency":     "USD",
		"ChargeCategory":      "Usage",
		"ChargeFrequency":     "Usage-Based",
		"ChargePeriodStart":   "2026-05-01T00:00:00Z",
		"ChargePeriodEnd":     "2026-05-02T00:00:00Z",
		"BillingPeriodStart":  "2026-05-01T00:00:00Z",
		"BillingPeriodEnd":    "2026-06-01T00:00:00Z",
		"BillingAccountId":    "api.anthropic.com/anthropic-cost",
		"ServiceProviderName": "Anthropic",
		"InvoiceIssuerName":   "Anthropic",
		"ServiceName":         "Claude API",
		"ServiceCategory":     "AI and Machine Learning",
		"ChargeDescription":   "Claude Opus 4 Usage - Input Tokens",
		"SkuMeter":            "claude-opus-4-6",
		"SubAccountId":        "wrkspc_alpha",
	} {
		if opus[col] != want {
			t.Errorf("opus row %s = %q, want %q", col, opus[col], want)
		}
	}
	if _, ok := opus["ChargeClass"]; ok {
		t.Errorf("ChargeClass should be null, got %q", opus["ChargeClass"])
	}

	// The credit row: -250 cents → -2.5, no model, no workspace.
	credit := rows[2].Record
	if credit["BilledCost"] != "-2.5" {
		t.Errorf("credit BilledCost = %q, want -2.5", credit["BilledCost"])
	}
	if _, ok := credit["SkuMeter"]; ok {
		t.Errorf("credit SkuMeter should be absent, got %q", credit["SkuMeter"])
	}
	if _, ok := credit["SubAccountId"]; ok {
		t.Errorf("credit SubAccountId should be absent, got %q", credit["SubAccountId"])
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
