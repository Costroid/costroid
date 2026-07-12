// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/allocation"
	"github.com/Costroid/costroid/internal/api"
	"github.com/Costroid/costroid/internal/devtools/fakeanthropic"
	"github.com/Costroid/costroid/internal/devtools/fakebigquery"
	"github.com/Costroid/costroid/internal/devtools/fakeopenai"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/ingest/aiconn"
	"github.com/Costroid/costroid/internal/storage"
)

// syncBuf is a goroutine-safe buffer for the fakes' request logs.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func runtimeGCPServiceAccount(t *testing.T, email string) (string, *rsa.PublicKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{
		"type": "service_account", "client_email": email,
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(body), &key.PublicKey
}

// TestOfflineE2EGCPFocusBigQuery is the ordered acceptance transcript for the
// Preview GCP FOCUS linked-export connector. It covers runtime-generated vault
// and ambient credentials, exact nested BigQuery row envelopes, delayed jobs,
// enforced pagination, tenant-aware skip state, force bypass, correction
// restatement scoping, --period, tag persistence, exact 18-digit money, and
// per-period rejection/degradation.
func TestOfflineE2EGCPFocusBigQuery(t *testing.T) {
	dataDir := t.TempDir()
	keyPath := filepath.Join(t.TempDir(), "cfg", "credentials.key")
	t.Setenv("COSTROID_DATA_DIR", dataDir)
	t.Setenv("COSTROID_CREDENTIALS_KEY_FILE", keyPath)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")

	fixtureDir := t.TempDir()
	copyTree(t, "../../testdata/gcp-focus-bq/fixture", fixtureDir)
	fakeLog := &syncBuf{}
	fake := fakebigquery.New(fixtureDir)
	fake.LogWriter = fakeLog
	fake.SchemaAdditions = []string{"x_FuturePreviewColumn"}
	vaultJSON, vaultPublic := runtimeGCPServiceAccount(t, "vault-gcp@example.test")
	ambientJSON, ambientPublic := runtimeGCPServiceAccount(t, "ambient-gcp@example.test")
	fake.AllowServiceAccount("vault-gcp@example.test", vaultPublic)
	fake.AllowServiceAccount("ambient-gcp@example.test", ambientPublic)
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	var transcript strings.Builder
	cli := func(stdin string, args ...string) (string, error) {
		out, err := runCLI(args, stdin)
		fmt.Fprintf(&transcript, "$ costroid %s\n%s", strings.Join(args, " "), out)
		if err != nil {
			fmt.Fprintf(&transcript, "  [exit: %v]\n", err)
		}
		return out, err
	}
	mustCLI := func(stdin string, args ...string) string {
		out, err := cli(stdin, args...)
		if err != nil {
			t.Fatalf("costroid %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}

	mustCLI("", "credentials", "init")
	mustCLI(vaultJSON, "credentials", "set", "gcp-focus-bq")
	args := []string{
		"ingest", "--connector", "gcp-focus-bq",
		"--dataset-project", "billing-host", "--dataset", "gcp_billing_immutable_demo_EU",
		"--table", "gcp_billing_export_focus_demo", "--location", "EU", "--job-project", "query-project",
		"--base-url", server.URL + "/bigquery/v2/", "--token-url", server.URL + "/token", "--since", "2026-05",
	}

	// Vault leg, with no ambient path: fresh ingest. The fixture coupling is:
	// May = 1.123456789012345678 + 2; June = 4 + 5 + 6.
	fresh := mustCLI("", args...)
	for _, want := range []string{
		"gcp-focus-bq table probe: timePartitioning=absent",
		"Preview schema added column(s) not selected by this connector: x_FuturePreviewColumn",
		"period 2026-05: ingested 2 record(s)",
		"period 2026-06: ingested 3 record(s)",
	} {
		if !strings.Contains(fresh, want) {
			t.Errorf("fresh ingest missing %q:\n%s", want, fresh)
		}
	}
	if issuers := fake.Issuers(); len(issuers) == 0 || issuers[len(issuers)-1] != "vault-gcp@example.test" {
		t.Fatalf("vault-leg JWT issuers = %v", issuers)
	}
	if got := gcpServiceTotal(t, focus.DefaultTenant, "Compute Engine"); !got.Equal(decimal.RequireFromString("1.123456789012345678")) {
		t.Errorf("18-fractional-digit Compute Engine BilledCost = %s", got)
	}
	if got := gcpServiceTotal(t, focus.DefaultTenant, "Cloud Billing"); !got.Equal(decimal.RequireFromString("8")) {
		t.Errorf("null-heavy rows did not land exactly: Cloud Billing total = %s", got)
	}
	// Stored x_Labels -> Tags: env=prod selects the May compute row and June
	// BigQuery row, totalling 1.123456789012345678 + 4 exactly.
	dim, err := allocation.Parse(strings.NewReader(`{"dimensions":[{"name":"env","rules":[{"label":"Production","match":[{"dimension":"tag:env","operator":"equals","value":"prod"}]}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := gcpAllocationTotal(t, focus.DefaultTenant, dim, "Production"); !got.Equal(decimal.RequireFromString("5.123456789012345678")) {
		t.Errorf("stored x_Labels tag allocation total = %s", got)
	}

	// Ambient file leg wins when no explicit slot is named. This run is also
	// the unchanged-sync proof: token + tables.get + one aggregate, no fetch.
	ambientPath := filepath.Join(t.TempDir(), "ambient-service-account.json")
	if err := os.WriteFile(ambientPath, []byte(ambientJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", ambientPath)
	before := len(fake.Calls())
	unchanged := mustCLI("", args...)
	// Exact skip lines: LastModified is Format(time.RFC3339) — no fractional
	// seconds — derived from MAX(x_ExportTime) micros (May 1778846400123456 →
	// 2026-05-15T12:00:00Z; June 1781524800000003 → 2026-06-15T12:00:00Z).
	for _, want := range []string{
		"period 2026-05: unchanged since 2026-05-15T12:00:00Z; skipped",
		"period 2026-06: unchanged since 2026-06-15T12:00:00Z; skipped",
	} {
		if !strings.Contains(unchanged, want) {
			t.Errorf("unchanged sync missing exact skip line %q:\n%s", want, unchanged)
		}
	}
	wantDelta := []string{"token iss=ambient-gcp@example.test", "tables.get", "jobs.query aggregate"}
	if delta := fake.Calls()[before:]; !slices.Equal(delta, wantDelta) {
		t.Errorf("unchanged call delta = %v, want %v", delta, wantDelta)
	}

	// Explicit vault slot wins over the simultaneously present ambient path.
	mustCLI("", append(args, "--credential", "gcp-focus-bq", "--period", "2026-05")...)
	issuers := fake.Issuers()
	if issuers[len(issuers)-1] != "vault-gcp@example.test" {
		t.Fatalf("explicit vault precedence issuer = %q, want vault", issuers[len(issuers)-1])
	}

	// --force empties prior state, so both month queries run; the store hash
	// still keeps both batches unchanged. June proves delayed poll + pagination.
	before = len(fake.Calls())
	forced := mustCLI("", append(args, "--force")...)
	for _, month := range []string{"2026-05", "2026-06"} {
		if !strings.Contains(forced, "period "+month+": source content unchanged") {
			t.Errorf("forced sync missing content short-circuit for %s:\n%s", month, forced)
		}
	}
	forceDelta := strings.Join(fake.Calls()[before:], "\n")
	for _, want := range []string{"jobs.query period=2026-05", "jobs.query period=2026-06", "jobs.getQueryResults period=2026-06 offset=0", "jobs.getQueryResults period=2026-06 offset=2"} {
		if !strings.Contains(forceDelta, want) {
			t.Errorf("force call delta missing %q:\n%s", want, forceDelta)
		}
	}

	// Restated overlay adds June correction rows -4 and +2. May's tuple stays
	// identical and must not be fetched; June changes from 3 rows / 15 to 5 / 13.
	copyTree(t, "../../testdata/gcp-focus-bq/restated", fixtureDir)
	before = len(fake.Calls())
	restated := mustCLI("", args...)
	if !strings.Contains(restated, "period 2026-05: unchanged since ") {
		t.Errorf("restatement did not skip unchanged May:\n%s", restated)
	}
	if !strings.Contains(restated, "period 2026-06: replaced (5 records; BilledCost 15 → 13)") {
		t.Errorf("restatement delta missing/wrong:\n%s", restated)
	}
	restateDelta := strings.Join(fake.Calls()[before:], "\n")
	if strings.Contains(restateDelta, "jobs.query period=2026-05") || !strings.Contains(restateDelta, "jobs.query period=2026-06") {
		t.Errorf("restatement fetch scope wrong:\n%s", restateDelta)
	}

	// --period output contains only the requested month.
	periodOut := mustCLI("", append(args, "--period", "2026-06")...)
	if strings.Contains(periodOut, "period 2026-05:") || !strings.Contains(periodOut, "period 2026-06:") {
		t.Errorf("--period output scope wrong:\n%s", periodOut)
	}

	// Tenant switch drops default-tenant tuples, fetches both months, and
	// re-homes every batch. The default tenant loses all GCP rows.
	tenantOut := mustCLI("", append(args, "--tenant", "acme")...)
	for _, month := range []string{"2026-05", "2026-06"} {
		if !strings.Contains(tenantOut, "period "+month+": replaced") || strings.Contains(tenantOut, "period "+month+": unchanged since") {
			t.Errorf("tenant switch did not re-home %s:\n%s", month, tenantOut)
		}
	}
	if got := gcpServiceTotal(t, focus.DefaultTenant, "Compute Engine"); !got.IsZero() {
		t.Errorf("default tenant retained GCP rows after re-home: %s", got)
	}
	if got := gcpServiceTotal(t, "acme", "Compute Engine"); got.IsZero() {
		t.Error("acme tenant has no GCP rows after re-home")
	}

	// One month can fail its fetch while the other succeeds unchanged.
	fake.FailMonth = "2026-06"
	degradeOut, degradeErr := cli("", append(args, "--tenant", "acme", "--force")...)
	degradeCombined := degradeOut
	if degradeErr != nil {
		degradeCombined += degradeErr.Error()
	}
	if degradeErr == nil || !strings.Contains(degradeCombined, "period 2026-05: source content unchanged") ||
		!strings.Contains(degradeCombined, "period 2026-06: failed") || !strings.Contains(degradeCombined, "1 of 2 period(s) failed") {
		t.Errorf("per-period degradation = err %v\n%s", degradeErr, degradeCombined)
	}
	fake.FailMonth = ""

	// A NULL required money value reaches the pipeline and aborts only June
	// with a row-numbered error; unchanged May remains skipped.
	copyTree(t, "../../testdata/gcp-focus-bq/reject", fixtureDir)
	rejectOut, rejectErr := cli("", append(args, "--tenant", "acme")...)
	rejectCombined := rejectOut
	if rejectErr != nil {
		rejectCombined += rejectErr.Error()
	}
	if rejectErr == nil || !strings.Contains(rejectCombined, "period 2026-05: unchanged since") ||
		!strings.Contains(rejectCombined, "period 2026-06: failed") || !strings.Contains(rejectCombined, "row 1") ||
		!strings.Contains(rejectCombined, "BilledCost is null") {
		t.Errorf("not-null rejection = err %v\n%s", rejectErr, rejectCombined)
	}

	all := transcript.String() + "\n# fake request logs\n" + fakeLog.String()
	if strings.Contains(all, "BEGIN"+" PRIVATE KEY") {
		t.Fatal("private-key material leaked into transcript or fake logs")
	}
	t.Logf("\n===== OFFLINE E2E GCP FOCUS BIGQUERY TRANSCRIPT =====\n%s\n# fake request logs\n%s", transcript.String(), fakeLog.String())
}

func gcpServiceTotal(t *testing.T, tenant, service string) decimal.Decimal {
	t.Helper()
	store, err := storage.Open(context.Background(), os.Getenv("COSTROID_DATA_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	daily, err := store.DailyCostsByService(context.Background(), tenant, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	total := decimal.Zero
	for _, day := range daily.Days {
		for _, item := range day.Services {
			if item.ServiceName == service {
				total = total.Add(item.Cost)
			}
		}
	}
	return total
}

func gcpAllocationTotal(t *testing.T, tenant string, dim allocation.Dimension, label string) decimal.Decimal {
	t.Helper()
	store, err := storage.Open(context.Background(), os.Getenv("COSTROID_DATA_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	daily, err := store.DailyCostsByAllocation(context.Background(), tenant, time.Time{}, time.Time{}, dim)
	if err != nil {
		t.Fatal(err)
	}
	total := decimal.Zero
	for _, day := range daily.Days {
		for _, item := range day.Services {
			if item.ServiceName == label {
				total = total.Add(item.Cost)
			}
		}
	}
	return total
}

// TestOfflineE2EAICost is the hermetic, loopback-only end-to-end proof for
// the credential store and the AI-vendor connectors (acceptance criteria 3,
// 4). It creates a key file, sets both credentials from stdin, starts both
// fakes, ingests both connectors alongside cloud sample data, queries the
// daily-cost API, re-syncs unchanged, restates a month, exercises --period /
// --force / a tenant switch, runs every negative case, and asserts no secret
// material appears anywhere in the captured output.
func TestOfflineE2EAICost(t *testing.T) {
	dataDir := t.TempDir()
	keyPath := filepath.Join(t.TempDir(), "cfg", "credentials.key")
	t.Setenv("COSTROID_DATA_DIR", dataDir)
	t.Setenv("COSTROID_CREDENTIALS_KEY_FILE", keyPath)

	// The AI connectors' --since window runs from the given month through the
	// CURRENT month, so the set of ingested months grows as the wall clock
	// advances (a fixed "3 months" assertion is a time bomb). Derive the
	// expected months from the REAL shared window computation the connectors
	// use, pinned to a single `now` captured before any ingest. Every ingest
	// below runs at now >= now0, so its window is a superset of expectedMonths
	// — asserting each expected month appears (never an exact count) is
	// therefore race-free across a mid-test UTC month rollover. The fixtures
	// carry data only for dataMonths; any later month in the window is empty.
	now0 := time.Now().UTC()
	expectedMonths, err := aiconn.Window("2026-05", "", now0)
	if err != nil {
		t.Fatalf("computing the expected window: %v", err)
	}
	dataMonths := map[string]bool{"2026-05": true, "2026-06": true}

	// Serve copies of the fixtures so a month can be restated in place.
	anthDir, oaiDir := t.TempDir(), t.TempDir()
	copyTree(t, "../../testdata/anthropic-cost/fixture", anthDir)
	copyTree(t, "../../testdata/openai-cost/fixture", oaiDir)

	fakeLog := &syncBuf{}
	anthFake := fakeanthropic.New(anthDir)
	anthFake.LogWriter = fakeLog
	anthFake.PageSize = 1 // force cursor following
	oaiFake := fakeopenai.New(oaiDir)
	oaiFake.LogWriter = fakeLog
	oaiFake.PageSize = 1
	anthSrv := httptest.NewServer(anthFake)
	t.Cleanup(anthSrv.Close)
	oaiSrv := httptest.NewServer(oaiFake)
	t.Cleanup(oaiSrv.Close)

	var transcript strings.Builder
	// cli runs one costroid command, appending its args (NEVER stdin, which
	// carries secrets) and captured stdout+stderr to the transcript.
	cli := func(stdin string, args ...string) error {
		out, err := runCLI(args, stdin)
		fmt.Fprintf(&transcript, "$ costroid %s\n%s", strings.Join(args, " "), out)
		if err != nil {
			fmt.Fprintf(&transcript, "  [exit: %v]\n", err)
		}
		return err
	}
	mustCLI := func(stdin string, args ...string) string {
		before := transcript.Len()
		if err := cli(stdin, args...); err != nil {
			t.Fatalf("costroid %s: %v", strings.Join(args, " "), err)
		}
		return transcript.String()[before:]
	}

	anthBase := "--base-url=" + anthSrv.URL
	oaiBase := "--base-url=" + oaiSrv.URL

	// --- setup: key file + both credentials from stdin ---
	mustCLI("", "credentials", "init")
	mustCLI(fakeanthropic.AdminKey, "credentials", "set", "anthropic-cost")
	mustCLI(fakeopenai.AdminKey, "credentials", "set", "openai-cost")
	listOut := mustCLI("", "credentials", "list")
	if !strings.Contains(listOut, "anthropic-cost") || !strings.Contains(listOut, "openai-cost") {
		t.Errorf("credentials list = %q, want both slots", listOut)
	}

	// --- ingest cloud sample data + both AI connectors ---
	mustCLI("", "ingest", "--connector", "aws-focus", "--path", "../../testdata/aws-focus-1.2/sample-export.csv.gz")
	anthOut := mustCLI("", "ingest", "--connector", "anthropic-cost", anthBase, "--since", "2026-05")
	for _, m := range expectedMonths {
		if !strings.Contains(anthOut, "period "+m+":") {
			t.Errorf("anthropic ingest missing period %s:\n%s", m, anthOut)
		}
		if !dataMonths[m] && !strings.Contains(anthOut, "period "+m+": ingested 0 record(s)") {
			// A month past the fixtures is empty — it must still ingest an
			// (empty) batch, the mechanism that lets a restated-to-zero month
			// drop stale data.
			t.Errorf("empty month %s did not ingest an empty batch:\n%s", m, anthOut)
		}
	}
	// The Anthropic ingest reports the per-period usage⇔cost reconciliation
	// anomalies (priority/flex tiers, web search, collision, orphan usage) that
	// are counted but never emitted as FOCUS rows (decision D33).
	if !strings.Contains(anthOut, "usage/cost reconciliation:") {
		t.Errorf("anthropic ingest missing the anomaly summary line:\n%s", anthOut)
	}
	mustCLI("", "ingest", "--connector", "openai-cost", oaiBase, "--since", "2026-05")

	// --- token quantities landed in the store, money invariant (decision D33) ---
	// A minted row carries the token count + minted SKU; money is untouched.
	assertEnriched(t, "anthropic-cost", "anthropic/claude-opus-4-6/uncached_input_tokens/0-200k", "1500000")
	assertEnriched(t, "openai-cost", "openai/gpt-4o, input", "1500000")
	// Grand-total BilledCost equals the pre-enrichment fixture totals (May+June;
	// later window months are empty): 117.845678+92.005 and 139.7067890123456789
	// +52.6789. Enrichment moved no money.
	assertBilledTotal(t, "anthropic-cost", "209.850678")
	assertBilledTotal(t, "openai-cost", "192.3856890123456789")

	// --- daily-cost API shows AI spend alongside cloud data (default tenant) ---
	daily := dailyView(t)
	transcript.WriteString("\n# GET /api/v1/costs/daily (default tenant)\n" + daily + "\n")
	for _, svc := range []string{"Claude API", "OpenAI API"} {
		if !strings.Contains(daily, svc) {
			t.Errorf("daily view missing AI service %q", svc)
		}
	}
	if !strings.Contains(daily, "Compute") {
		t.Errorf("daily view missing cloud sample data (Compute):\n%s", daily)
	}

	// --- token-usage API surfaces enriched token quantities end-to-end
	// (decision D33) with decimal-string precision, deterministic ordering, and
	// ONLY enriched "Tokens" rows (money-only rows absent). Pinned to the
	// fixture data ingested above: later window months are empty, so this has no
	// wall-clock dependence. ---
	tokens := tokensView(t)
	transcript.WriteString("\n# GET /api/v1/usage/tokens/daily (default tenant)\n" + tokens + "\n")
	var tokenRows []tokenRow
	if err := json.Unmarshal([]byte(tokens), &tokenRows); err != nil {
		t.Fatalf("decoding token usage: %v (body: %s)", err, tokens)
	}
	// The OpenAI float-hazard count (1234567890123456789) sums with the day's
	// other two OpenAI token rows (1500000 + 900000) into an exact 19-digit
	// value no float64 can hold — proof the whole path stays decimal. Anthropic's
	// minted quantities appear under "Claude API"/"Tokens". Ordering is day
	// ascending, then serviceName (Claude API < OpenAI API), then consumedUnit.
	wantTokens := []tokenRow{
		{Date: "2026-05-01", ServiceName: "Claude API", ConsumedUnit: "Tokens", ConsumedQuantity: "4400000"},
		{Date: "2026-05-01", ServiceName: "OpenAI API", ConsumedUnit: "Tokens", ConsumedQuantity: "1234567890125856789"},
		{Date: "2026-06-01", ServiceName: "Claude API", ConsumedUnit: "Tokens", ConsumedQuantity: "3000000"},
		{Date: "2026-06-01", ServiceName: "OpenAI API", ConsumedUnit: "Tokens", ConsumedQuantity: "3000000"},
		{Date: "2026-06-02", ServiceName: "Claude API", ConsumedUnit: "Tokens", ConsumedQuantity: "4000000"},
		{Date: "2026-06-02", ServiceName: "OpenAI API", ConsumedUnit: "Tokens", ConsumedQuantity: "500000"},
	}
	if len(tokenRows) != len(wantTokens) {
		t.Fatalf("token usage rows = %+v, want %d rows (%+v)", tokenRows, len(wantTokens), wantTokens)
	}
	for i, w := range wantTokens {
		if tokenRows[i] != w {
			t.Errorf("token row %d = %+v, want %+v", i, tokenRows[i], w)
		}
	}
	// Money-only rows never surface: 2026-05-02 (OpenAI promo credit + file-search
	// call fee; Anthropic's collided output rows) yields NO enriched token row,
	// and no row carries a non-"Tokens" unit.
	for _, r := range tokenRows {
		if r.Date == "2026-05-02" {
			t.Errorf("money-only day 2026-05-02 surfaced in token usage: %+v", r)
		}
		if r.ConsumedUnit != "Tokens" {
			t.Errorf("non-Tokens unit surfaced in token usage: %+v", r)
		}
	}

	// --- cost-orphaned usage metrics surface via the new endpoint (D18c) ---
	// COMPLETE SET, right after both-vendor ingests and BEFORE any restatement:
	// exactly the enumerated classes and NOTHING else. Anthropic (unchanged):
	// priority (999) and flex_discount (123) tier tokens, the web_search_requests
	// count (5, "Requests"), the standard-tier orphan key no cost row referenced
	// (delta uncached 42), and the OpenAI recognized-but-unpriced USG-3 line item
	// (assistants api | file search 42, "Unknown"). OpenAI Usage-API metrics
	// (ALL on May 1 — see the testdata/openai-cost/fixture usage files):
	// per-model num_model_requests ("Requests") for gpt-4o/gpt-image-1/tts-1/
	// whisper-1, plus the special units images/characters/seconds ("Images"/
	// "Characters"/"Seconds") and the model-less code_interpreter_sessions
	// num_sessions ("Sessions", ServiceName "OpenAI API"), vector-store
	// usage_bytes ("Bytes"), and source-qualified search-call counts ("Calls").
	// NEVER a token count.
	// Ordering is day, serviceName, serviceTier, metricName, unit (binary ASC:
	// "OpenAI API" < "claude-*" < "gpt-*" < "tts-*" < "whisper-*").
	metricsBody := usageMetricsView(t)
	transcript.WriteString("\n# GET /api/v1/usage/metrics/daily (default tenant)\n" + metricsBody + "\n")
	var metricRows []usageMetricRow
	if err := json.Unmarshal([]byte(metricsBody), &metricRows); err != nil {
		t.Fatalf("decoding usage metrics: %v (body: %s)", err, metricsBody)
	}
	wantMetrics := []usageMetricRow{
		{Date: "2026-05-01", ServiceName: "OpenAI API", ServiceTier: "", MetricName: "file_search_num_requests", Unit: "Calls", Quantity: "8"},
		{Date: "2026-05-01", ServiceName: "OpenAI API", ServiceTier: "", MetricName: "num_sessions", Unit: "Sessions", Quantity: "7"},
		{Date: "2026-05-01", ServiceName: "OpenAI API", ServiceTier: "", MetricName: "usage_bytes", Unit: "Bytes", Quantity: "1099511627776"},
		{Date: "2026-05-01", ServiceName: "claude-opus-4-6", ServiceTier: "priority", MetricName: "uncached_input_tokens", Unit: "Tokens", Quantity: "999"},
		{Date: "2026-05-01", ServiceName: "claude-opus-4-6", ServiceTier: "standard", MetricName: "web_search_requests", Unit: "Requests", Quantity: "5"},
		{Date: "2026-05-01", ServiceName: "gpt-4o", ServiceTier: "", MetricName: "num_model_requests", Unit: "Requests", Quantity: "10"},
		{Date: "2026-05-01", ServiceName: "gpt-4o", ServiceTier: "", MetricName: "web_search_num_requests", Unit: "Calls", Quantity: "15"},
		{Date: "2026-05-01", ServiceName: "gpt-image-1", ServiceTier: "", MetricName: "images", Unit: "Images", Quantity: "12"},
		{Date: "2026-05-01", ServiceName: "gpt-image-1", ServiceTier: "", MetricName: "num_model_requests", Unit: "Requests", Quantity: "3"},
		{Date: "2026-05-01", ServiceName: "tts-1", ServiceTier: "", MetricName: "characters", Unit: "Characters", Quantity: "5000"},
		{Date: "2026-05-01", ServiceName: "tts-1", ServiceTier: "", MetricName: "num_model_requests", Unit: "Requests", Quantity: "2"},
		{Date: "2026-05-01", ServiceName: "whisper-1", ServiceTier: "", MetricName: "num_model_requests", Unit: "Requests", Quantity: "4"},
		{Date: "2026-05-01", ServiceName: "whisper-1", ServiceTier: "", MetricName: "seconds", Unit: "Seconds", Quantity: "600"},
		{Date: "2026-05-02", ServiceName: "OpenAI API", ServiceTier: "", MetricName: "assistants api | file search", Unit: "Unknown", Quantity: "42"},
		{Date: "2026-05-02", ServiceName: "claude-opus-4-6", ServiceTier: "standard", MetricName: "uncached_input_tokens", Unit: "Tokens", Quantity: "42"},
		{Date: "2026-05-02", ServiceName: "claude-sonnet-4-5", ServiceTier: "flex_discount", MetricName: "output_tokens", Unit: "Tokens", Quantity: "123"},
	}
	if len(metricRows) != len(wantMetrics) {
		t.Fatalf("usage metrics = %+v, want %d rows (%+v)", metricRows, len(wantMetrics), wantMetrics)
	}
	for i, w := range wantMetrics {
		if metricRows[i] != w {
			t.Errorf("usage metric row %d = %+v, want %+v", i, metricRows[i], w)
		}
	}
	// MUTATION INTENT (Anthropic over-capture): capturing any referenced/enriched
	// standard|batch agg key as a usage metric MUST break this complete-set
	// assertion — the fixtures' large enriched token quantities (700000/800000
	// standard uncached, etc.) that DO join to cost rows must be ABSENT here. June
	// contributes ZERO usage-metric rows because every June Anthropic usage key is
	// cost-referenced AND the OpenAI Usage-API fixtures are deliberately May-ONLY
	// (so June stays usage-metric-empty and this Anthropic guard is preserved
	// intact). Over-capture would surface most visibly in the otherwise-empty
	// June; because the leak would be in a different table, it is invisible to the
	// money-invariance and FOCUS-isolation checks — this is the guard that catches
	// it. Scoped to Anthropic service_names so it stays a precise Anthropic guard.
	for _, r := range metricRows {
		if strings.HasPrefix(r.Date, "2026-06") && strings.HasPrefix(r.ServiceName, "claude-") {
			t.Errorf("June should contribute ZERO Anthropic usage metrics (all cost-referenced) but got: %+v", r)
		}
	}
	// ISOLATION: the usage-metric model-name services and units never appear in
	// the daily-cost or daily-token views (a separate table, separate query).
	for _, leaked := range []string{
		"claude-opus-4-6", "claude-sonnet-4-5", "web_search_requests",
		"assistants api | file search", "usage_bytes", "web_search_num_requests",
		"file_search_num_requests",
	} {
		if strings.Contains(daily, leaked) {
			t.Errorf("usage-metric-only token %q leaked into the daily-cost view", leaked)
		}
		if strings.Contains(tokens, leaked) {
			t.Errorf("usage-metric-only token %q leaked into the daily-token view", leaked)
		}
	}

	// --- unchanged re-sync: every period unchanged, rewrite short-circuited ---
	// Snapshot the cumulative fake request log BEFORE the re-sync so the
	// endpoint-coverage assertion below inspects only the entries THIS re-sync
	// appended: the cumulative log already holds both paths from the first sync,
	// so asserting against it could never fail (the bug this fix-up closes).
	logBefore := len(fakeLog.String())
	reOut := mustCLI("", "ingest", "--connector", "anthropic-cost", anthBase, "--since", "2026-05")
	for _, m := range expectedMonths {
		if !strings.Contains(reOut, "period "+m+": source content unchanged") {
			t.Errorf("re-sync did not report month %s unchanged:\n%s", m, reOut)
		}
	}
	// The unchanged re-sync must ITSELF fetch BOTH endpoints (the ContentHash
	// covers cost AND usage payloads, so a quantity-only restatement cannot be
	// missed). Assert the DELTA the re-sync appended — not the cumulative log.
	reSyncLog := fakeLog.String()[logBefore:]
	for _, path := range []string{"/v1/organizations/cost_report", "/v1/organizations/usage_report/messages"} {
		if !strings.Contains(reSyncLog, path) {
			t.Errorf("the unchanged re-sync did not itself fetch %s (both endpoints fetched):\n%s", path, reSyncLog)
		}
	}

	// --- --force is accepted and byte-identical content still reports unchanged ---
	forceOut := mustCLI("", "ingest", "--connector", "anthropic-cost", anthBase, "--since", "2026-05", "--force")
	for _, m := range expectedMonths {
		if !strings.Contains(forceOut, "period "+m+": source content unchanged") {
			t.Errorf("--force did not still short-circuit byte-identical content for %s:\n%s", m, forceOut)
		}
	}

	// --- --period targets one month ---
	periodOut := mustCLI("", "ingest", "--connector", "anthropic-cost", anthBase, "--period", "2026-06")
	if strings.Contains(periodOut, "2026-05") || strings.Contains(periodOut, "2026-07") {
		t.Errorf("--period 2026-06 touched other months:\n%s", periodOut)
	}
	if !strings.Contains(periodOut, "period 2026-06:") {
		t.Errorf("--period 2026-06 did not process 2026-06:\n%s", periodOut)
	}

	// --- money restatement: swap in the restated month, re-sync, show the delta ---
	// The restated dirs change only cost amounts; the usage files stay from the
	// fixture overlay, so the enrichment is unchanged and only money moves.
	copyTree(t, "../../testdata/anthropic-cost/restated", anthDir)
	copyTree(t, "../../testdata/openai-cost/restated", oaiDir)
	restatedAnth := mustCLI("", "ingest", "--connector", "anthropic-cost", anthBase, "--since", "2026-05")
	if !strings.Contains(restatedAnth, "period 2026-05: replaced (10 records; BilledCost 117.845678 → 115.5)") {
		t.Errorf("anthropic restatement delta missing/wrong:\n%s", restatedAnth)
	}
	if !strings.Contains(restatedAnth, "period 2026-06: source content unchanged") {
		t.Errorf("unchanged June should still short-circuit after May restatement:\n%s", restatedAnth)
	}
	restatedOai := mustCLI("", "ingest", "--connector", "openai-cost", oaiBase, "--period", "2026-05")
	if !strings.Contains(restatedOai, "period 2026-05: replaced (5 records; BilledCost 139.7067890123456789 → 113.75)") {
		t.Errorf("openai restatement delta missing/wrong (exact-decimal preservation):\n%s", restatedOai)
	}

	// --- quantity-only restatement (ContentHash-covers-usage proof) ---
	// Overlay ONLY a changed usage file (the cost stays the just-restated 115.5).
	// The re-sync must report `replaced` with UNCHANGED BilledCost totals — which
	// only happens if ContentHash covers the usage payloads (else it would
	// short-circuit as `unchanged` and the changed token count would be lost).
	beforeQty := storedConsumedQuantity(t, focus.DefaultTenant, "anthropic-cost",
		"anthropic/claude-opus-4-6/uncached_input_tokens/0-200k")
	copyTree(t, "../../testdata/anthropic-cost/restated-usage", anthDir)
	qtyOnly := mustCLI("", "ingest", "--connector", "anthropic-cost", anthBase, "--period", "2026-05")
	if !strings.Contains(qtyOnly, "period 2026-05: replaced (10 records; BilledCost 115.5 → 115.5)") {
		t.Errorf("quantity-only restatement should replace with UNCHANGED money:\n%s", qtyOnly)
	}
	afterQty := storedConsumedQuantity(t, focus.DefaultTenant, "anthropic-cost",
		"anthropic/claude-opus-4-6/uncached_input_tokens/0-200k")
	if !beforeQty.Equal(decimal.RequireFromString("1500000")) || !afterQty.Equal(decimal.RequireFromString("1400000")) {
		t.Errorf("quantity-only restatement did not update the stored token count: before=%s after=%s (want 1500000 → 1400000)", beforeQty, afterQty)
	}

	// --- orphan-correction supersede + idempotence (DEDICATED fixture) ---
	// restated-usage-orphan is restated-usage with ONLY the priority-tier uncached
	// orphan bumped 999→888; every joined/enriched quantity and all cost are
	// byte-identical, so this genuinely tests the orphan-supersede path (reusing
	// restated-usage would be a 999→999 no-op). Orphan quantities never feed cost,
	// so BilledCost stays 115.5→115.5, but the usage payload changed → `replaced`.
	priorityBefore, ok := findUsageMetric(t, "2026-05-01", "claude-opus-4-6", "priority", "uncached_input_tokens")
	if !ok || priorityBefore != "999" {
		t.Fatalf("priority orphan metric before correction = %q (ok=%t), want 999", priorityBefore, ok)
	}
	copyTree(t, "../../testdata/anthropic-cost/restated-usage-orphan", anthDir)
	orphanCorr := mustCLI("", "ingest", "--connector", "anthropic-cost", anthBase, "--period", "2026-05")
	if !strings.Contains(orphanCorr, "period 2026-05: replaced (10 records; BilledCost 115.5 → 115.5)") {
		t.Errorf("orphan-only correction should replace with UNCHANGED money:\n%s", orphanCorr)
	}
	// (a) SUPERSEDE: the priority metric moved 999→888; the untouched flex orphan
	// stays 123; cost is unchanged (asserted above via the 115.5→115.5 delta).
	if q, ok := findUsageMetric(t, "2026-05-01", "claude-opus-4-6", "priority", "uncached_input_tokens"); !ok || q != "888" {
		t.Errorf("SUPERSEDE: priority orphan metric = %q (ok=%t), want 888", q, ok)
	}
	if q, ok := findUsageMetric(t, "2026-05-02", "claude-sonnet-4-5", "flex_discount", "output_tokens"); !ok || q != "123" {
		t.Errorf("untouched flex orphan = %q (ok=%t), want 123 (supersede must not disturb other orphans)", q, ok)
	}
	// (b) IDEMPOTENCE: re-ingesting the SAME 888 fixture short-circuits
	// (`unchanged`) yet the usage rows still read 888 — the post-success write
	// fires even on the unchanged short-circuit, DELETE-then-INSERT replacing
	// (never accumulating to 1776).
	idem := mustCLI("", "ingest", "--connector", "anthropic-cost", anthBase, "--period", "2026-05")
	if !strings.Contains(idem, "period 2026-05: source content unchanged") {
		t.Errorf("second re-ingest of the same orphan fixture should be unchanged:\n%s", idem)
	}
	if q, ok := findUsageMetric(t, "2026-05-01", "claude-opus-4-6", "priority", "uncached_input_tokens"); !ok || q != "888" {
		t.Errorf("IDEMPOTENCE: priority orphan after re-sync = %q (ok=%t), want still 888 (not accumulated)", q, ok)
	}

	// --- tenant switch re-homes (azure/s3 parity) ---
	tenantOut := mustCLI("", "ingest", "--connector", "anthropic-cost", anthBase, "--since", "2026-05", "--tenant", "acme")
	if !strings.Contains(tenantOut, "replaced") {
		t.Errorf("tenant switch did not re-home (replace) the batches:\n%s", tenantOut)
	}
	if got := tenantDaysNonEmpty(t, "acme"); !got {
		t.Error("tenant acme has no AI cost rows after the re-home")
	}
	// The default tenant LOST the re-homed Anthropic rows (the s3/azure twins
	// assert the same lost-rows parity). Only anthropic-cost was re-homed, so
	// default keeps its cloud (Compute) and OpenAI rows — but its Claude API
	// rows must be gone, not duplicated across tenants.
	defaultAfter := dailyView(t)
	transcript.WriteString("\n# GET /api/v1/costs/daily (default tenant, after Anthropic re-home)\n" + defaultAfter + "\n")
	if strings.Contains(defaultAfter, "Claude API") {
		t.Errorf("default tenant still shows Claude API rows after re-homing them to acme:\n%s", defaultAfter)
	}
	if !strings.Contains(defaultAfter, "OpenAI API") {
		t.Errorf("default tenant lost OpenAI rows it should keep (only Anthropic was re-homed):\n%s", defaultAfter)
	}

	// --- negatives ---
	// negative runs a command expected to fail, and asserts the actionable
	// message appears in the captured stdout+stderr OR the returned error
	// (commands that fail before printing return the message via error, which
	// main() prints). It appends the combined text to the transcript.
	negative := func(label, want string, args ...string) {
		out, err := runCLI(args, "")
		combined := out
		if err != nil {
			combined += err.Error() + "\n"
		}
		transcript.WriteString("\n# negative: " + label + "\n" + combined)
		if err == nil || !strings.Contains(combined, want) {
			t.Errorf("%s: err=%v out=%q, want %q", label, err, out, want)
		}
	}

	missingDir := t.TempDir()
	negative("missing key file", "credentials init",
		"ingest", "--connector", "anthropic-cost", anthBase,
		"--key-file", filepath.Join(missingDir, "nope.key"), "--period", "2026-05")

	negative("missing credential", "credentials set no-such-slot",
		"ingest", "--connector", "anthropic-cost", anthBase,
		"--credential", "no-such-slot", "--period", "2026-05")

	// A slot holding a wrong key surfaces a per-period 401 (the credential
	// itself decrypts fine — the failure is from the vendor, not the store).
	mustCLI("sk-ant-admin01-DELIBERATELYWRONGKEY", "credentials", "set", "anthropic-bad")
	negative("wrong API key (401)", "rejected (HTTP 401)",
		"ingest", "--connector", "anthropic-cost", anthBase,
		"--credential", "anthropic-bad", "--period", "2026-05")

	// A usage-report fetch failure degrades ONLY that month to an actionable
	// per-period error (never a silently quantity-less ingest) while the other
	// months still ingest — failure isolation (ANT-12).
	anthFake.UsageFailMonth = "2026-05"
	failOut, failErr := runCLI([]string{"ingest", "--connector", "anthropic-cost", anthBase, "--since", "2026-05"}, "")
	transcript.WriteString("\n# negative: usage fetch failure isolates one period\n" + failOut)
	if failErr != nil {
		transcript.WriteString(failErr.Error() + "\n")
	}
	anthFake.UsageFailMonth = ""
	if failErr == nil {
		t.Error("usage-fetch failure did not fail the run")
	}
	if !strings.Contains(failOut, "period 2026-05: failed") || !strings.Contains(failOut, "HTTP 500") {
		t.Errorf("May should degrade actionably on a usage-fetch failure:\n%s", failOut)
	}
	if !strings.Contains(failOut, "period 2026-06:") {
		t.Errorf("June should still ingest while May degraded (failure isolation):\n%s", failOut)
	}

	// A world-readable key file is refused.
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	negative("world-readable key file", "chmod 600",
		"ingest", "--connector", "anthropic-cost", anthBase, "--period", "2026-05")
	if err := os.Chmod(keyPath, 0o600); err != nil {
		t.Fatalf("chmod back: %v", err)
	}

	// --- secrets-hygiene assertion over ALL captured output ---
	all := transcript.String() + "\n# fake request logs\n" + fakeLog.String()
	keyContents := readKeyFile(t, keyPath)
	for _, forbidden := range []string{
		fakeanthropic.AdminKey, fakeopenai.AdminKey,
		"sk-ant", "sk-admin", "DELIBERATELYWRONGKEY", keyContents,
	} {
		if forbidden != "" && strings.Contains(all, forbidden) {
			t.Errorf("captured output leaked forbidden material %q", firstLine(forbidden))
		}
	}

	t.Logf("\n===== OFFLINE E2E TRANSCRIPT =====\n%s\n%s", transcript.String(), "# fake request logs\n"+fakeLog.String())
}

// focus-csv fixtures. The restated-month assertion below is COUPLED to these
// two files: focus-1.4.csv's May rows total BilledCost 15 (10 + 5) and
// focus-1.4-restated.csv changes only May's first row to 8, totalling 13; the
// June rows are byte-identical between the two files. Keep the fixtures and the
// "15 → 13" expectation in sync.
const (
	fcsv10       = "../../testdata/focus-csv/focus-1.0.csv"
	fcsv11       = "../../testdata/focus-csv/focus-1.1.csv"
	fcsv12       = "../../testdata/focus-csv/focus-1.2.csv"
	fcsv13       = "../../testdata/focus-csv/focus-1.3.csv"
	fcsv14       = "../../testdata/focus-csv/focus-1.4.csv"
	fcsv14Restat = "../../testdata/focus-csv/focus-1.4-restated.csv"
	fcsvDupHdr   = "../../testdata/focus-csv/negative/duplicate-header.csv"
	fcsvUnkHdr   = "../../testdata/focus-csv/negative/unknown-header.csv"
	fcsvNull     = "../../testdata/focus-csv/negative/literal-null.csv"
	fcsvBadTS    = "../../testdata/focus-csv/negative/nonrfc3339-1.0.csv"
	fcsvLenient  = "../../testdata/focus-csv/lenient/lenient-1.4.csv"
)

// TestOfflineE2EFocusCSV is the hermetic, file-only end-to-end proof for the
// generic focus-csv importer: it ingests conformant 1.0/1.1/1.2/1.3/1.4 exports
// (plus the 1.0r2 alias) under distinct labels alongside AWS cloud sample data,
// shows them in the daily view with their BilledCost totals, re-imports unchanged,
// restates one month, exercises --period / --force / --tenant, and runs every
// negative — including the strict-parser boundary (a non-RFC3339-timestamp 1.0 file
// is still rejected, proving 1.0 acceptance did not relax the parser).
func TestOfflineE2EFocusCSV(t *testing.T) {
	t.Setenv("COSTROID_DATA_DIR", t.TempDir())

	var transcript strings.Builder
	cli := func(args ...string) (string, error) {
		out, err := runCLI(args, "")
		fmt.Fprintf(&transcript, "$ costroid %s\n%s", strings.Join(args, " "), out)
		if err != nil {
			fmt.Fprintf(&transcript, "  [exit: %v]\n", err)
		}
		return out, err
	}
	mustCLI := func(args ...string) string {
		out, err := cli(args...)
		if err != nil {
			t.Fatalf("costroid %s: %v", strings.Join(args, " "), err)
		}
		return out
	}

	// --- existing cloud data + three FOCUS CSV exports under distinct labels ---
	mustCLI("ingest", "--connector", "aws-focus", "--path", "../../testdata/aws-focus-1.2/sample-export.csv.gz")

	out12 := mustCLI("ingest", "--connector", "focus-csv", "--path", fcsv12, "--focus-version", "1.2", "--source-label", "aws-csv")
	for _, m := range []string{"2026-05", "2026-06"} {
		if !strings.Contains(out12, "period "+m+": ingested 2 record(s) as batch focus-csv/aws-csv/"+m) {
			t.Errorf("1.2 import missing per-month batch for %s:\n%s", m, out12)
		}
	}
	mustCLI("ingest", "--connector", "focus-csv", "--path", fcsv13, "--focus-version", "1.3", "--source-label", "gcp-csv")
	mustCLI("ingest", "--connector", "focus-csv", "--path", fcsv14, "--focus-version", "1.4", "--source-label", "azure-csv")

	// --- FOCUS 1.0 import (conformant; OCI is the real 1.0-only audience) ---
	// COUPLED to focus-1.0.csv: 2 records/month, BilledCost total 10 (May 1+2, June 3+4).
	out10 := mustCLI("ingest", "--connector", "focus-csv", "--path", fcsv10, "--focus-version", "1.0", "--source-label", "oci-csv")
	for _, m := range []string{"2026-05", "2026-06"} {
		if !strings.Contains(out10, "period "+m+": ingested 2 record(s) as batch focus-csv/oci-csv/"+m) {
			t.Errorf("1.0 import missing per-month batch for %s:\n%s", m, out10)
		}
	}

	// --- FOCUS 1.1 import (conformant; the 50-column superset) ---
	// COUPLED to focus-1.1.csv: 2 records/month, BilledCost total 20 (May 5+5, June 4+6).
	out11 := mustCLI("ingest", "--connector", "focus-csv", "--path", fcsv11, "--focus-version", "1.1", "--source-label", "cloudnine-csv")
	for _, m := range []string{"2026-05", "2026-06"} {
		if !strings.Contains(out11, "period "+m+": ingested 2 record(s) as batch focus-csv/cloudnine-csv/"+m) {
			t.Errorf("1.1 import missing per-month batch for %s:\n%s", m, out11)
		}
	}

	// --- daily view shows the focus-csv services alongside the AWS sample ---
	daily := dailyView(t)
	transcript.WriteString("\n# GET /api/v1/costs/daily (default tenant)\n" + daily + "\n")
	for _, svc := range []string{"AWS Lambda", "Datadog Pro", "Azure Virtual Machines", "Amazon Elastic Compute Cloud", "OCI Compute", "CloudNine Object Storage"} {
		if !strings.Contains(daily, svc) {
			t.Errorf("daily view missing service %q:\n%s", svc, daily)
		}
	}
	// BilledCost totals from the daily-cost view (coupled to the fixtures above).
	// Asserted BEFORE the 1.0r2 re-import below (which lands a second OCI batch under
	// a distinct label and would double the OCI total).
	if got := dailyServiceCosts(t, "OCI Compute", "OCI Marketplace App"); !got.Equal(decimal.RequireFromString("10")) {
		t.Errorf("1.0 fixture BilledCost total via daily view = %s, want 10", got)
	}
	if got := dailyServiceCosts(t, "CloudNine Object Storage"); !got.Equal(decimal.RequireFromString("20")) {
		t.Errorf("1.1 fixture BilledCost total via daily view = %s, want 20", got)
	}

	// --- FOCUS 1.0r2 import (Azure-declarable alias; canonicalizes to 1.0) ---
	// A "1.0r2" declaration imports under the 1.0 path end-to-end: the batches land,
	// proving canonicalVersion rewrote the value that flows into the Connector.
	out10r2 := mustCLI("ingest", "--connector", "focus-csv", "--path", fcsv10, "--focus-version", "1.0r2", "--source-label", "oci-r2-csv")
	for _, m := range []string{"2026-05", "2026-06"} {
		if !strings.Contains(out10r2, "period "+m+": ingested 2 record(s) as batch focus-csv/oci-r2-csv/"+m) {
			t.Errorf("1.0r2 import (canonicalized to 1.0) missing per-month batch for %s:\n%s", m, out10r2)
		}
	}

	// --- FOCUS 1.4 import WITH --lenient (zone-bearing UTC timestamp-format quirks) ---
	// COUPLED to lenient-1.4.csv: May 1 row (BilledCost 1), June 2 rows (BilledCost
	// 2 + 3) — the June count includes the boundary row whose BillingPeriodStart
	// "2026-05-31T20:00-05:00" is 2026-06-01T01:00Z in UTC, so it buckets into June,
	// not its local-wall-clock May. Both months' BillingPeriodStart values are
	// zone-bearing FORMAT quirks (no-seconds Z, space+"UTC", offset), so this import
	// is possible ONLY because --lenient normalizes both the Discover month-split and
	// the streaming reader. The without-flag rejection is asserted in the negatives.
	outLen := mustCLI("ingest", "--connector", "focus-csv", "--path", fcsvLenient, "--focus-version", "1.4", "--source-label", "lenient-csv", "--lenient")
	if !strings.Contains(outLen, "period 2026-05: ingested 1 record(s) as batch focus-csv/lenient-csv/2026-05") {
		t.Errorf("lenient import missing/wrong May batch:\n%s", outLen)
	}
	if !strings.Contains(outLen, "period 2026-06: ingested 2 record(s) as batch focus-csv/lenient-csv/2026-06") {
		t.Errorf("lenient import missing/wrong June batch (incl. the UTC-boundary row):\n%s", outLen)
	}
	// Money into the daily view via the fixture's two UNIQUE services (2 + 3 = 5),
	// which no earlier import contributes to (unlike the shared "OCI Compute").
	if got := dailyServiceCosts(t, "Boundary Crosser", "BigQuery Export"); !got.Equal(decimal.RequireFromString("5")) {
		t.Errorf("lenient fixture BilledCost total via daily view = %s, want 5", got)
	}

	// --- unchanged re-import short-circuits both months ---
	reOut := mustCLI("ingest", "--connector", "focus-csv", "--path", fcsv14, "--focus-version", "1.4", "--source-label", "azure-csv")
	for _, m := range []string{"2026-05", "2026-06"} {
		if !strings.Contains(reOut, "period "+m+": source content unchanged") {
			t.Errorf("unchanged re-import did not short-circuit %s:\n%s", m, reOut)
		}
	}

	// --- --force on byte-identical content still reports unchanged (no-op) ---
	forceOut := mustCLI("ingest", "--connector", "focus-csv", "--path", fcsv14, "--focus-version", "1.4", "--source-label", "azure-csv", "--force")
	if !strings.Contains(forceOut, "period 2026-05: source content unchanged") {
		t.Errorf("--force on identical content did not stay unchanged:\n%s", forceOut)
	}

	// --- --period targets one month ---
	periodOut := mustCLI("ingest", "--connector", "focus-csv", "--path", fcsv14, "--focus-version", "1.4", "--source-label", "azure-csv", "--period", "2026-06")
	if strings.Contains(periodOut, "2026-05") || !strings.Contains(periodOut, "period 2026-06:") {
		t.Errorf("--period 2026-06 touched the wrong months:\n%s", periodOut)
	}

	// --- period-absent error lists the discovered months ---
	_, err := cli("ingest", "--connector", "focus-csv", "--path", fcsv14, "--focus-version", "1.4", "--source-label", "azure-csv", "--period", "2026-09")
	if err == nil || !strings.Contains(err.Error(), "billing period 2026-09 not found in the export (discovered: 2026-05, 2026-06)") {
		t.Errorf("period-absent error = %v, want the discovered-months message", err)
	}

	// --- restated month: same label, only May changed → May replaced, June unchanged ---
	// COUPLED to the fixtures: May 15 → 13, June byte-identical (see the const block).
	restated := mustCLI("ingest", "--connector", "focus-csv", "--path", fcsv14Restat, "--focus-version", "1.4", "--source-label", "azure-csv")
	if !strings.Contains(restated, "period 2026-05: replaced (2 records; BilledCost 15 → 13)") {
		t.Errorf("restated May delta missing/wrong:\n%s", restated)
	}
	if !strings.Contains(restated, "period 2026-06: source content unchanged") {
		t.Errorf("unchanged June should still short-circuit after the May restatement:\n%s", restated)
	}

	// --- tenant switch homes rows under a distinct tenant ---
	mustCLI("ingest", "--connector", "focus-csv", "--path", fcsv14, "--focus-version", "1.4", "--source-label", "azure-acme", "--tenant", "acme")
	if !tenantDaysNonEmpty(t, "acme") {
		t.Error("tenant acme has no focus-csv rows after the tenant-scoped import")
	}

	// --- negatives ---
	negative := func(label, want string, args ...string) {
		out, err := runCLI(args, "")
		combined := out
		if err != nil {
			combined += err.Error() + "\n"
		}
		transcript.WriteString("\n# negative: " + label + "\n" + combined)
		if err == nil || !strings.Contains(combined, want) {
			t.Errorf("%s: err=%v out=%q, want %q", label, err, out, want)
		}
	}

	// Strict-parser boundary (the INVERSE of the pre-slice 1.0/1.1 rejections): a
	// spec-shaped 1.0 file whose ChargePeriodStart is a non-RFC3339 (space-separated)
	// timestamp passes the version + header + month gates but is REJECTED at the row
	// level — proving accepting 1.0 did NOT relax the shared strict parser.
	negative("non-RFC3339 timestamp (1.0) is row-numbered", "row 1",
		"ingest", "--connector", "focus-csv", "--path", fcsvBadTS, "--focus-version", "1.0")
	negative("non-RFC3339 timestamp (1.0) is the strict ISO-8601 rejection", "is not a valid ISO 8601 date/time",
		"ingest", "--connector", "focus-csv", "--path", fcsvBadTS, "--focus-version", "1.0")

	// --lenient is opt-in: the SAME lenient fixture is REJECTED without the flag.
	// Strict fails at Discover (analyze→monthOf on the May no-seconds
	// BillingPeriodStart), so match the Discover-time ISO-8601 substring (per the
	// reject-substring caveat, NOT a ChargePeriodStart-specific one).
	negative("lenient fixture rejected without --lenient", "is not a valid ISO 8601 date/time",
		"ingest", "--connector", "focus-csv", "--path", fcsvLenient, "--focus-version", "1.4")
	// The money-safety boundary holds under --lenient too: a genuinely zone-less
	// ChargePeriodStart (fcsvBadTS) is still rejected at the row level even WITH the
	// flag (its BillingPeriodStart is canonical, so Discover passes and the failure
	// is the row-level ChargePeriodStart ISO-8601 error).
	negative("zone-less still rejected with --lenient", "ChargePeriodStart",
		"ingest", "--connector", "focus-csv", "--path", fcsvBadTS, "--focus-version", "1.0", "--lenient")

	// The rest assert only actionable substrings (offending column, suggested
	// version, row number).
	negative("unknown non-x_ column", "MadeUpColumn",
		"ingest", "--connector", "focus-csv", "--path", fcsvUnkHdr, "--focus-version", "1.4")
	negative("mislabel hint (1.2 as 1.4)", "1.2 or 1.3",
		"ingest", "--connector", "focus-csv", "--path", fcsv12, "--focus-version", "1.4")
	negative("mislabel hint (1.3 as 1.2)", "1.3 (or 1.4)",
		"ingest", "--connector", "focus-csv", "--path", fcsv13, "--focus-version", "1.2")
	negative("mislabel hint (1.2 as 1.3)", "re-run with --focus-version 1.2",
		"ingest", "--connector", "focus-csv", "--path", fcsv12, "--focus-version", "1.3")
	negative("duplicate header", "duplicate header column(s) \"BilledCost\"",
		"ingest", "--connector", "focus-csv", "--path", fcsvDupHdr, "--focus-version", "1.4")
	negative("literal null cell (row-numbered)", "row 1",
		"ingest", "--connector", "focus-csv", "--path", fcsvNull, "--focus-version", "1.4")

	t.Logf("\n===== OFFLINE E2E FOCUS-CSV TRANSCRIPT =====\n%s", transcript.String())
}

// TestFocusCSVDefaultLabelCLI covers the default --source-label (the file's
// base name) end to end at the CLI.
func TestFocusCSVDefaultLabelCLI(t *testing.T) {
	t.Setenv("COSTROID_DATA_DIR", t.TempDir())
	out, err := runCLI([]string{"ingest", "--connector", "focus-csv", "--path", fcsv14, "--focus-version", "1.4"}, "")
	if err != nil {
		t.Fatalf("default-label ingest: %v\n%s", err, out)
	}
	if !strings.Contains(out, "batch focus-csv/focus-1.4.csv/2026-05") {
		t.Errorf("default label did not derive the base name:\n%s", out)
	}
}

// TestUsageMetricsWriteGatedOnCostIngestSuccess proves usage metrics are
// persisted ONLY after a period's cost ingest succeeds (write-gating): a month
// whose cost ingest FAILS (a bad billing currency at synthesis) persists ZERO
// usage_metrics rows even though its orphan usage was computed at discovery,
// while a sibling month that succeeds DOES persist them. This fails if
// ReplaceUsageBatch is called in the discovery/print loop instead of after
// ingest.Run.
func TestUsageMetricsWriteGatedOnCostIngestSuccess(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("COSTROID_DATA_DIR", dataDir)
	t.Setenv("COSTROID_CREDENTIALS_KEY_FILE", filepath.Join(t.TempDir(), "credentials.key"))

	anthDir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(anthDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	// 2026-05 cost carries an EMPTY currency → synthesize fails the whole month
	// (D23), so ingest.Run errors AFTER discovery already computed the orphan.
	write("2026-05.json", `[{"starting_at":"2026-05-01T00:00:00Z","ending_at":"2026-05-02T00:00:00Z",
	  "results":[{"amount":"100","currency":"","description":"bad","model":"claude-opus-4-6","cost_type":"session_usage"}]}]`)
	write("2026-05.usage.json", `[{"starting_at":"2026-05-01T00:00:00Z","ending_at":"2026-05-02T00:00:00Z",
	  "results":[{"model":"claude-opus-4-6","workspace_id":"wrkspc_alpha","context_window":"0-200k","inference_geo":"us","service_tier":"priority","uncached_input_tokens":777}]}]`)
	// 2026-06 is a clean, succeeding month with its own priority orphan.
	write("2026-06.json", `[{"starting_at":"2026-06-01T00:00:00Z","ending_at":"2026-06-02T00:00:00Z",
	  "results":[{"amount":"100","currency":"USD","description":"ok","model":"claude-opus-4-6","cost_type":"session_usage"}]}]`)
	write("2026-06.usage.json", `[{"starting_at":"2026-06-01T00:00:00Z","ending_at":"2026-06-02T00:00:00Z",
	  "results":[{"model":"claude-opus-4-6","workspace_id":"wrkspc_alpha","context_window":"0-200k","inference_geo":"us","service_tier":"priority","uncached_input_tokens":555}]}]`)

	srv := httptest.NewServer(fakeanthropic.New(anthDir))
	t.Cleanup(srv.Close)
	base := "--base-url=" + srv.URL

	if _, err := runCLI([]string{"credentials", "init"}, ""); err != nil {
		t.Fatalf("credentials init: %v", err)
	}
	if _, err := runCLI([]string{"credentials", "set", "anthropic-cost"}, fakeanthropic.AdminKey); err != nil {
		t.Fatalf("credentials set: %v", err)
	}

	// The failing month exits non-zero and reports the per-period failure.
	failOut, failErr := runCLI([]string{"ingest", "--connector", "anthropic-cost", base, "--period", "2026-05"}, "")
	if failErr == nil {
		t.Fatalf("2026-05 ingest should FAIL on the empty currency:\n%s", failOut)
	}
	if !strings.Contains(failOut, "period 2026-05: failed") {
		t.Errorf("2026-05 failure not reported:\n%s", failOut)
	}
	// The succeeding month ingests cleanly.
	if okOut, err := runCLI([]string{"ingest", "--connector", "anthropic-cost", base, "--period", "2026-06"}, ""); err != nil {
		t.Fatalf("2026-06 ingest should succeed: %v\n%s", err, okOut)
	}

	store, err := storage.Open(context.Background(), dataDir)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer func() { _ = store.Close() }()
	metrics, err := store.DailyUsageMetrics(context.Background(), focus.DefaultTenant, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyUsageMetrics: %v", err)
	}
	// Exactly the succeeded month's orphan (555); the failed month's 777 is ABSENT.
	if len(metrics) != 1 {
		t.Fatalf("usage metrics = %+v, want exactly the succeeded 2026-06 row (the failed month persists ZERO)", metrics)
	}
	m := metrics[0]
	if m.Date.Format(time.DateOnly) != "2026-06-01" || m.ServiceTier != "priority" || m.Quantity.String() != "555" {
		t.Errorf("usage metric = %+v, want the 2026-06 priority 555 row", m)
	}
	for _, r := range metrics {
		if r.Quantity.String() == "777" {
			t.Errorf("the failed cost period leaked usage metrics: %+v", r)
		}
	}
}

// TestUsageMetricsEmptyBatchClearsThroughDriver drives a month from HAVING a
// usage-metrics orphan to having ZERO through runIngest and proves the empty
// batch CLEARS the previously-written rows — the invariant behind the guard
// being `job.usageMetrics != nil` (fires on an empty, non-nil slice) rather than
// `len(job.usageMetrics) > 0` (which would leave the stale row). It reddens if
// the driver skips the write for an empty slice (Mutation A).
func TestUsageMetricsEmptyBatchClearsThroughDriver(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("COSTROID_DATA_DIR", dataDir)
	t.Setenv("COSTROID_CREDENTIALS_KEY_FILE", filepath.Join(t.TempDir(), "credentials.key"))

	anthDir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(anthDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	// 2026-05: a succeeding cost row plus one priority-tier usage orphan (999),
	// which is structurally cost-orphaned (D33) and surfaced as a usage metric.
	write("2026-05.json", `[{"starting_at":"2026-05-01T00:00:00Z","ending_at":"2026-05-02T00:00:00Z",
	  "results":[{"amount":"100","currency":"USD","description":"ok","model":"claude-opus-4-6","cost_type":"session_usage"}]}]`)
	write("2026-05.usage.json", `[{"starting_at":"2026-05-01T00:00:00Z","ending_at":"2026-05-02T00:00:00Z",
	  "results":[{"model":"claude-opus-4-6","workspace_id":"wrkspc_alpha","context_window":"0-200k","inference_geo":"us","service_tier":"priority","uncached_input_tokens":999}]}]`)

	srv := httptest.NewServer(fakeanthropic.New(anthDir))
	t.Cleanup(srv.Close)
	base := "--base-url=" + srv.URL

	if _, err := runCLI([]string{"credentials", "init"}, ""); err != nil {
		t.Fatalf("credentials init: %v", err)
	}
	if _, err := runCLI([]string{"credentials", "set", "anthropic-cost"}, fakeanthropic.AdminKey); err != nil {
		t.Fatalf("credentials set: %v", err)
	}

	// First ingest writes the priority orphan to usage_metrics.
	if out, err := runCLI([]string{"ingest", "--connector", "anthropic-cost", base, "--period", "2026-05"}, ""); err != nil {
		t.Fatalf("first 2026-05 ingest: %v\n%s", err, out)
	}
	if got := usageMetricCount(t); got != 1 {
		t.Fatalf("after first ingest: %d usage_metrics rows, want exactly 1 (the priority 999 orphan)", got)
	}
	if q, ok := findUsageMetric(t, "2026-05-01", "claude-opus-4-6", "priority", "uncached_input_tokens"); !ok || q != "999" {
		t.Fatalf("after first ingest: priority orphan = %q (ok=%v), want 999", q, ok)
	}

	// Overwrite ONLY the usage so the month now has ZERO orphans; the cost file is
	// untouched. Changing the usage bytes changes the ContentHash, so the cost
	// re-ingest is a `replaced` (not `unchanged`) and the connector's now-empty
	// usageMetrics slice reaches runIngest, whose empty ReplaceUsageBatch clears
	// the stale row.
	write("2026-05.usage.json", `[{"starting_at":"2026-05-01T00:00:00Z","ending_at":"2026-05-02T00:00:00Z","results":[]}]`)

	out, err := runCLI([]string{"ingest", "--connector", "anthropic-cost", base, "--period", "2026-05"}, "")
	if err != nil {
		t.Fatalf("re-ingest 2026-05: %v\n%s", err, out)
	}
	if !strings.Contains(out, "replaced") {
		t.Fatalf("re-ingest should be `replaced` (the changed usage bytes changed the ContentHash):\n%s", out)
	}
	// The empty batch cleared the previously-written rows for this source.
	if got := usageMetricCount(t); got != 0 {
		t.Fatalf("after the zero-orphan re-ingest: %d usage_metrics rows, want 0 (the empty batch must CLEAR the stale priority orphan)", got)
	}
}

// TestUsageMetricsWriteFiresOnUnchangedShortCircuit proves the usage write runs
// on the cost `unchanged` short-circuit (it sits BEFORE the outcome switch), so
// a month whose cost + ContentHash are stored but whose usage_metrics rows are
// missing self-heals on a no-op re-ingest. It reddens if the write is gated on
// `!result.Unchanged` (Mutation B).
func TestUsageMetricsWriteFiresOnUnchangedShortCircuit(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("COSTROID_DATA_DIR", dataDir)
	t.Setenv("COSTROID_CREDENTIALS_KEY_FILE", filepath.Join(t.TempDir(), "credentials.key"))

	anthDir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(anthDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	write("2026-05.json", `[{"starting_at":"2026-05-01T00:00:00Z","ending_at":"2026-05-02T00:00:00Z",
	  "results":[{"amount":"100","currency":"USD","description":"ok","model":"claude-opus-4-6","cost_type":"session_usage"}]}]`)
	write("2026-05.usage.json", `[{"starting_at":"2026-05-01T00:00:00Z","ending_at":"2026-05-02T00:00:00Z",
	  "results":[{"model":"claude-opus-4-6","workspace_id":"wrkspc_alpha","context_window":"0-200k","inference_geo":"us","service_tier":"priority","uncached_input_tokens":999}]}]`)

	srv := httptest.NewServer(fakeanthropic.New(anthDir))
	t.Cleanup(srv.Close)
	base := "--base-url=" + srv.URL

	if _, err := runCLI([]string{"credentials", "init"}, ""); err != nil {
		t.Fatalf("credentials init: %v", err)
	}
	if _, err := runCLI([]string{"credentials", "set", "anthropic-cost"}, fakeanthropic.AdminKey); err != nil {
		t.Fatalf("credentials set: %v", err)
	}

	// First ingest stores the cost batch, its ContentHash, and the priority 999
	// usage orphan.
	if out, err := runCLI([]string{"ingest", "--connector", "anthropic-cost", base, "--period", "2026-05"}, ""); err != nil {
		t.Fatalf("first 2026-05 ingest: %v\n%s", err, out)
	}
	if q, ok := findUsageMetric(t, "2026-05-01", "claude-opus-4-6", "priority", "uncached_input_tokens"); !ok || q != "999" {
		t.Fatalf("after first ingest: priority orphan = %q (ok=%v), want 999", q, ok)
	}

	// Simulate a prior ReplaceUsageBatch failure that left the cost committed and
	// its ContentHash stored but the usage_metrics rows absent: clear this month's
	// rows directly through the store (an empty batch on the same identity — the
	// per-month SourceIdentity is api.anthropic.com/<slot>/<YYYY-MM>, slot defaults
	// to the connector name). Open/close the store between CLI calls so only one
	// writer ever holds the data dir.
	{
		store, err := storage.Open(context.Background(), dataDir)
		if err != nil {
			t.Fatalf("opening store to clear usage metrics: %v", err)
		}
		batch := storage.UsageBatch{
			Connector:      "anthropic-cost",
			SourceIdentity: "api.anthropic.com/anthropic-cost/2026-05",
			TenantID:       focus.DefaultTenant,
		}
		if err := store.ReplaceUsageBatch(context.Background(), batch, nil); err != nil {
			_ = store.Close()
			t.Fatalf("clearing usage metrics: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("closing store after clear: %v", err)
		}
	}
	if _, ok := findUsageMetric(t, "2026-05-01", "claude-opus-4-6", "priority", "uncached_input_tokens"); ok {
		t.Fatalf("setup failed: the usage metric is still present after the direct clear (wrong SourceIdentity?)")
	}

	// Re-ingest the SAME month with IDENTICAL content: the cost short-circuits as
	// `unchanged`, but the usage write runs before the outcome switch, so it
	// re-writes the orphan even on the unchanged branch (self-heal).
	out, err := runCLI([]string{"ingest", "--connector", "anthropic-cost", base, "--period", "2026-05"}, "")
	if err != nil {
		t.Fatalf("unchanged re-ingest: %v\n%s", err, out)
	}
	if !strings.Contains(out, "source content unchanged") {
		t.Fatalf("re-ingest should short-circuit as `source content unchanged`:\n%s", out)
	}
	// The usage write fired on the Unchanged branch and restored the orphan.
	if q, ok := findUsageMetric(t, "2026-05-01", "claude-opus-4-6", "priority", "uncached_input_tokens"); !ok || q != "999" {
		t.Fatalf("usage metric = %q (ok=%v), want it RESTORED to 999 by the write firing on the Unchanged branch", q, ok)
	}
}

// TestOpenAIUsageOnlyRestatementSelfApplies (slice-11 mandated test #6) proves
// the deliberate COST-ONLY ContentHash design is correct for OpenAI usage: a
// month re-ingested with IDENTICAL cost bytes but CHANGED usage counts reports
// the cost batch `unchanged` (the usage payload is never hashed), yet the new
// usage_metrics values land because the driver's usage write is unconditional
// (fires even on the unchanged short-circuit). NOT a tautology — it asserts the
// stored num_model_requests moved 10 → 25.
func TestOpenAIUsageOnlyRestatementSelfApplies(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("COSTROID_DATA_DIR", dataDir)
	t.Setenv("COSTROID_CREDENTIALS_KEY_FILE", filepath.Join(t.TempDir(), "credentials.key"))

	oaiDir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(oaiDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	write("2026-05.json", `[{"object":"bucket","start_time":1777593600,"end_time":1777680000,
	  "results":[{"amount":{"value":10.0,"currency":"usd"},"line_item":"gpt-4o, input","quantity":1500000}]}]`)
	write("2026-05.usage.completions.json", `[{"start_time":1777593600,"results":[{"model":"gpt-4o","num_model_requests":10}]}]`)

	srv := httptest.NewServer(fakeopenai.New(oaiDir))
	t.Cleanup(srv.Close)
	base := "--base-url=" + srv.URL

	if _, err := runCLI([]string{"credentials", "init"}, ""); err != nil {
		t.Fatalf("credentials init: %v", err)
	}
	if _, err := runCLI([]string{"credentials", "set", "openai-cost"}, fakeopenai.AdminKey); err != nil {
		t.Fatalf("credentials set: %v", err)
	}

	if out, err := runCLI([]string{"ingest", "--connector", "openai-cost", base, "--period", "2026-05"}, ""); err != nil {
		t.Fatalf("first ingest: %v\n%s", err, out)
	}
	if q, ok := findUsageMetric(t, "2026-05-01", "gpt-4o", "", "num_model_requests"); !ok || q != "10" {
		t.Fatalf("after first ingest: num_model_requests = %q (ok=%v), want 10", q, ok)
	}

	// Change ONLY the usage count; the cost bytes are byte-identical.
	write("2026-05.usage.completions.json", `[{"start_time":1777593600,"results":[{"model":"gpt-4o","num_model_requests":25}]}]`)

	out, err := runCLI([]string{"ingest", "--connector", "openai-cost", base, "--period", "2026-05"}, "")
	if err != nil {
		t.Fatalf("usage-only re-ingest: %v\n%s", err, out)
	}
	if !strings.Contains(out, "source content unchanged") {
		t.Fatalf("cost is byte-identical → the cost batch must short-circuit `unchanged` (usage is never hashed):\n%s", out)
	}
	if q, ok := findUsageMetric(t, "2026-05-01", "gpt-4o", "", "num_model_requests"); !ok || q != "25" {
		t.Errorf("usage-only restatement did not self-apply: num_model_requests = %q (ok=%v), want 25 (the write fired on the Unchanged branch)", q, ok)
	}
}

// TestOpenAIEmptyUsageSuccessClearsPriorRows proves the clean-empty-month
// converse to TestOpenAIUsageFetchFailurePreservesPriorRows: when every usage
// endpoint succeeds but returns zero rows, and the cost rows are fully enriched
// (zero USG-3 orphans), UsageMetrics() is a non-nil empty slice. The driver must
// therefore write an empty batch and CLEAR stale rows rather than preserving them
// as it does for nil-on-usage-failure.
func TestOpenAIEmptyUsageSuccessClearsPriorRows(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("COSTROID_DATA_DIR", dataDir)
	t.Setenv("COSTROID_CREDENTIALS_KEY_FILE", filepath.Join(t.TempDir(), "credentials.key"))

	oaiDir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(oaiDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	write("2026-05.json", `[{"object":"bucket","start_time":1777593600,"end_time":1777680000,
	  "results":[{"amount":{"value":10.0,"currency":"usd"},"line_item":"gpt-4o, input","quantity":1500000}]}]`)
	write("2026-05.usage.completions.json", `[{"start_time":1777593600,"results":[{"model":"gpt-4o","num_model_requests":10}]}]`)

	srv := httptest.NewServer(fakeopenai.New(oaiDir))
	t.Cleanup(srv.Close)
	base := "--base-url=" + srv.URL

	if _, err := runCLI([]string{"credentials", "init"}, ""); err != nil {
		t.Fatalf("credentials init: %v", err)
	}
	if _, err := runCLI([]string{"credentials", "set", "openai-cost"}, fakeopenai.AdminKey); err != nil {
		t.Fatalf("credentials set: %v", err)
	}

	if out, err := runCLI([]string{"ingest", "--connector", "openai-cost", base, "--period", "2026-05"}, ""); err != nil {
		t.Fatalf("first ingest: %v\n%s", err, out)
	}
	if q, ok := findUsageMetric(t, "2026-05-01", "gpt-4o", "", "num_model_requests"); !ok || q != "10" {
		t.Fatalf("after first ingest: num_model_requests = %q (ok=%v), want 10", q, ok)
	}

	if err := os.Remove(filepath.Join(oaiDir, "2026-05.usage.completions.json")); err != nil {
		t.Fatalf("removing usage fixture: %v", err)
	}

	out, err := runCLI([]string{"ingest", "--connector", "openai-cost", base, "--period", "2026-05"}, "")
	if err != nil {
		t.Fatalf("empty-usage re-ingest: %v\n%s", err, out)
	}
	if !strings.Contains(out, "source content unchanged") {
		t.Fatalf("cost bytes are unchanged, so the cost batch should short-circuit as unchanged:\n%s", out)
	}
	if _, ok := findUsageMetric(t, "2026-05-01", "gpt-4o", "", "num_model_requests"); ok {
		t.Fatalf("stale num_model_requests row survived a successful empty usage fetch")
	}
	if got := usageMetricCount(t); got != 0 {
		t.Fatalf("after successful empty usage fetch: %d usage_metrics rows, want 0", got)
	}
}

// TestOpenAIUsageFetchFailurePreservesPriorRows (slice-11 mandated test, the
// usage-endpoint-FAILURE degrade — distinct from the per-FIELD degrade) proves a
// usage-endpoint 500 is orthogonal to cost: the month's COST still ingests, the
// prior usage_metrics rows SURVIVE (NOT overwritten to a USG-3-only slice), the
// anomaly notice prints, and the OTHER month proceeds. The May cost carries both
// an enriched token line AND an unknown-unit USG-3 orphan, so a WRONG USG-3-only
// overwrite would drop the completions Requests row — asserting it survives at 10
// proves the write was SKIPPED (UsageMetrics()==nil), not partially rewritten.
func TestOpenAIUsageFetchFailurePreservesPriorRows(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("COSTROID_DATA_DIR", dataDir)
	t.Setenv("COSTROID_CREDENTIALS_KEY_FILE", filepath.Join(t.TempDir(), "credentials.key"))

	oaiDir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(oaiDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	// May cost: an enriched token line (no USG-3) PLUS an unknown-unit orphan (USG-3).
	write("2026-05.json", `[{"object":"bucket","start_time":1777593600,"end_time":1777680000,
	  "results":[
	    {"amount":{"value":10.0,"currency":"usd"},"line_item":"gpt-4o, input","quantity":1500000},
	    {"amount":{"value":5.0,"currency":"usd"},"line_item":"assistants api | file search","quantity":42}
	  ]}]`)
	write("2026-05.usage.completions.json", `[{"start_time":1777593600,"results":[{"model":"gpt-4o","num_model_requests":10}]}]`)
	// June cost + usage, so failure isolation (the other month proceeds) is observable.
	write("2026-06.json", `[{"object":"bucket","start_time":1780272000,"end_time":1780358400,
	  "results":[{"amount":{"value":7.0,"currency":"usd"},"line_item":"gpt-4o, input","quantity":700000}]}]`)
	write("2026-06.usage.completions.json", `[{"start_time":1780272000,"results":[{"model":"gpt-4o","num_model_requests":20}]}]`)

	fake := fakeopenai.New(oaiDir)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	base := "--base-url=" + srv.URL

	if _, err := runCLI([]string{"credentials", "init"}, ""); err != nil {
		t.Fatalf("credentials init: %v", err)
	}
	if _, err := runCLI([]string{"credentials", "set", "openai-cost"}, fakeopenai.AdminKey); err != nil {
		t.Fatalf("credentials set: %v", err)
	}

	// Clean first ingest of both months: May and June get their usage rows.
	if out, err := runCLI([]string{"ingest", "--connector", "openai-cost", base, "--since", "2026-05"}, ""); err != nil {
		t.Fatalf("first ingest: %v\n%s", err, out)
	}
	if q, ok := findUsageMetric(t, "2026-05-01", "gpt-4o", "", "num_model_requests"); !ok || q != "10" {
		t.Fatalf("after first ingest: May num_model_requests = %q (ok=%v), want 10", q, ok)
	}

	// Now fail May's usage endpoints and re-ingest both months.
	fake.UsageFailMonth = "2026-05"
	out, err := runCLI([]string{"ingest", "--connector", "openai-cost", base, "--since", "2026-05"}, "")
	fake.UsageFailMonth = ""
	if err != nil {
		t.Fatalf("re-ingest with a usage failure should NOT fail the run (usage is orthogonal to cost): %v\n%s", err, out)
	}
	// (a) May's cost still ingests (byte-identical → unchanged) and prints the notice.
	if !strings.Contains(out, "period 2026-05: source content unchanged") {
		t.Errorf("May cost should still ingest despite the usage failure:\n%s", out)
	}
	if !strings.Contains(out, "usage endpoint") || !strings.Contains(out, "not refreshed") {
		t.Errorf("the usage-fetch-failure notice did not print (never silent):\n%s", out)
	}
	// (b) May's prior usage rows SURVIVE — NOT wiped, NOT overwritten to USG-3-only.
	if q, ok := findUsageMetric(t, "2026-05-01", "gpt-4o", "", "num_model_requests"); !ok || q != "10" {
		t.Errorf("May completions Requests row = %q (ok=%v), want it PRESERVED at 10 (write skipped, not overwritten to USG-3-only)", q, ok)
	}
	if q, ok := findUsageMetric(t, "2026-05-01", "OpenAI API", "", "assistants api | file search"); !ok || q != "42" {
		t.Errorf("May USG-3 orphan = %q (ok=%v), want it preserved at 42", q, ok)
	}
	// (c) The OTHER month proceeded normally.
	if !strings.Contains(out, "period 2026-06:") {
		t.Errorf("June should still ingest while May's usage degraded (failure isolation):\n%s", out)
	}
	if q, ok := findUsageMetric(t, "2026-06-01", "gpt-4o", "", "num_model_requests"); !ok || q != "20" {
		t.Errorf("June num_model_requests = %q (ok=%v), want 20 (its usage refreshed normally)", q, ok)
	}
}

// runCLI invokes run(args) with the given stdin, capturing everything written
// to stdout and stderr. It swaps the process streams, so it must not run in
// parallel with other output-producing code.
func runCLI(args []string, stdin string) (string, error) {
	origIn, origOut, origErr := os.Stdin, os.Stdout, os.Stderr

	inR, inW, _ := os.Pipe()
	os.Stdin = inR
	go func() {
		_, _ = io.WriteString(inW, stdin)
		_ = inW.Close()
	}()

	outR, outW, _ := os.Pipe()
	os.Stdout, os.Stderr = outW, outW
	captured := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, outR)
		captured <- buf.String()
	}()

	err := run(args)

	_ = outW.Close()
	os.Stdin, os.Stdout, os.Stderr = origIn, origOut, origErr
	_ = inR.Close()
	return <-captured, err
}

// aiRows opens the store and returns the enrichment-relevant projection of one
// tenant+connector's stored cost rows.
func aiRows(t *testing.T, tenant, connector string) []storage.AIRow {
	t.Helper()
	store, err := storage.Open(context.Background(), os.Getenv("COSTROID_DATA_DIR"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer func() { _ = store.Close() }()
	rows, err := store.EnrichedAIRows(context.Background(), tenant, connector)
	if err != nil {
		t.Fatalf("EnrichedAIRows(%s, %s): %v", tenant, connector, err)
	}
	return rows
}

// assertEnriched asserts the default-tenant row with the given minted SkuId
// carries the expected token count and "Tokens" unit.
func assertEnriched(t *testing.T, connector, skuID, wantQty string) {
	t.Helper()
	for _, r := range aiRows(t, focus.DefaultTenant, connector) {
		if r.SkuID == skuID {
			if !r.ConsumedQuantity.Valid || !r.ConsumedQuantity.Decimal.Equal(decimal.RequireFromString(wantQty)) || r.ConsumedUnit != "Tokens" {
				t.Errorf("%s %s: consumed=%v unit=%q, want %s Tokens", connector, skuID, r.ConsumedQuantity, r.ConsumedUnit, wantQty)
			}
			return
		}
	}
	t.Errorf("%s: no stored row with SkuId %q", connector, skuID)
}

// assertBilledTotal asserts a connector's default-tenant total BilledCost.
func assertBilledTotal(t *testing.T, connector, want string) {
	t.Helper()
	sum := decimal.Zero
	for _, r := range aiRows(t, focus.DefaultTenant, connector) {
		sum = sum.Add(r.BilledCost)
	}
	if !sum.Equal(decimal.RequireFromString(want)) {
		t.Errorf("%s grand-total BilledCost = %s, want %s (pre-enrichment fixture total)", connector, sum, want)
	}
}

// storedConsumedQuantity returns the stored token count for one minted SkuId.
func storedConsumedQuantity(t *testing.T, tenant, connector, skuID string) decimal.Decimal {
	t.Helper()
	for _, r := range aiRows(t, tenant, connector) {
		if r.SkuID == skuID {
			if !r.ConsumedQuantity.Valid {
				t.Fatalf("%s %s has a null consumed_quantity", connector, skuID)
			}
			return r.ConsumedQuantity.Decimal
		}
	}
	t.Fatalf("%s: no stored row with SkuId %q", connector, skuID)
	return decimal.Decimal{}
}

// TestOfflineE2EAllocation is the hermetic end-to-end proof for query-time cost
// allocation: it ingests the AWS FOCUS sample, serves the API in-process (never
// `costroid serve`, which blocks on signals), and asserts groupBy=allocation
// returns exact per-label money incl. Unallocated summing to the fixture grand
// total; that overwriting the rules file changes the grouping WITHOUT a restart
// (per-request read); that a missing rules file returns the exact 400 body; and
// that groupBy=service still works and is keyed by "key".
func TestOfflineE2EAllocation(t *testing.T) {
	t.Setenv("COSTROID_DATA_DIR", t.TempDir())
	var transcript strings.Builder

	// Seed the already-existing AWS FOCUS 1.2 sample (ingest opens and releases
	// the single-writer store before we open it below).
	if out, err := runCLI([]string{"ingest", "--connector", "aws-focus", "--path", "../../testdata/aws-focus-1.2/sample-export.csv.gz"}, ""); err != nil {
		t.Fatalf("ingest: %v\n%s", err, out)
	}

	rulesPath := filepath.Join(t.TempDir(), "allocation.json")
	writeRules := func(content string) {
		if err := os.WriteFile(rulesPath, []byte(content), 0o600); err != nil {
			t.Fatalf("writing rules: %v", err)
		}
	}
	// Rules v1: one rule AND-combines a service_provider_name condition with a
	// service_name starts_with; AWS Lambda gets its own rule; S3 is left for
	// Unallocated.
	writeRules(`{"dimensions":[{"name":"team","rules":[
		{"label":"compute","match":[
			{"dimension":"service_provider_name","operator":"equals","value":"AWS"},
			{"dimension":"service_name","operator":"starts_with","value":"Amazon Elastic Compute Cloud"}
		]},
		{"label":"serverless","match":[{"dimension":"service_name","operator":"equals","value":"AWS Lambda"}]}
	]}]}`)

	store, err := storage.Open(context.Background(), os.Getenv("COSTROID_DATA_DIR"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer func() { _ = store.Close() }()
	handler := api.NewHandler("e2e", fstest.MapFS{}, store, rulesPath)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	get := func(query string) (int, string) {
		t.Helper()
		resp, err := http.Get(srv.URL + "/api/v1/costs/daily" + query)
		if err != nil {
			t.Fatalf("GET %s: %v", query, err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	sumByKey := func(body string) map[string]decimal.Decimal {
		t.Helper()
		var resp struct {
			Days []struct {
				Services []struct {
					Key  string `json:"key"`
					Cost string `json:"cost"`
				} `json:"services"`
			} `json:"days"`
		}
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			t.Fatalf("decoding %q: %v", body, err)
		}
		m := map[string]decimal.Decimal{}
		for _, d := range resp.Days {
			for _, s := range d.Services {
				m[s.Key] = m[s.Key].Add(decimal.RequireFromString(s.Cost))
			}
		}
		return m
	}
	assertKeySums := func(got map[string]decimal.Decimal, want map[string]string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("label set = %v, want keys for %v", got, want)
		}
		total := decimal.Zero
		for k, w := range want {
			g, ok := got[k]
			if !ok {
				t.Errorf("missing label %q", k)
				continue
			}
			if g.String() != w {
				t.Errorf("label %q = %s, want %s", k, g.String(), w)
			}
			total = total.Add(g)
		}
		// The labels sum to the fixture grand total (COUPLED to
		// testdata/aws-focus-1.2: EC2 25.4016 + Lambda 1.3272 + S3 6.0375).
		if total.String() != "32.7663" {
			t.Errorf("label sum = %s, want fixture grand total 32.7663", total.String())
		}
	}
	assertResponseTotals := func(body string) {
		t.Helper()
		var resp struct {
			Total string `json:"total"`
			Days  []struct {
				Total    string `json:"total"`
				Services []struct {
					Cost string `json:"cost"`
				} `json:"services"`
			} `json:"days"`
		}
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			t.Fatalf("decoding totals from %q: %v", body, err)
		}
		grand := decimal.Zero
		for i, day := range resp.Days {
			wantDay := decimal.Zero
			for _, svc := range day.Services {
				wantDay = wantDay.Add(decimal.RequireFromString(svc.Cost))
			}
			if day.Total != wantDay.String() {
				t.Errorf("day %d response total = %s, want service sum %s", i+1, day.Total, wantDay)
			}
			grand = grand.Add(wantDay)
		}
		if resp.Total != grand.String() || resp.Total != "32.7663" {
			t.Errorf("response grand total = %s, want day sum and fixture total 32.7663", resp.Total)
		}
	}

	// --- allocation v1: exact money per label incl. Unallocated ---
	code, body := get("?groupBy=allocation")
	transcript.WriteString("# GET /api/v1/costs/daily?groupBy=allocation (rules v1)\n" + body + "\n")
	if code != http.StatusOK {
		t.Fatalf("allocation v1 status %d: %s", code, body)
	}
	// COUPLED to testdata/aws-focus-1.2 and rules v1 above.
	assertKeySums(sumByKey(body), map[string]string{
		"compute":     "25.4016",
		"serverless":  "1.3272",
		"Unallocated": "6.0375",
	})
	assertResponseTotals(body)

	// --- live reload: overwrite the rules, re-GET the SAME handler ---
	writeRules(`{"dimensions":[{"name":"team","rules":[
		{"label":"object-storage","match":[{"dimension":"service_name","operator":"equals","value":"Amazon Simple Storage Service"}]}
	]}]}`)
	code, body = get("?groupBy=allocation")
	transcript.WriteString("\n# GET /api/v1/costs/daily?groupBy=allocation (rules v2, live-reloaded, no restart)\n" + body + "\n")
	if code != http.StatusOK {
		t.Fatalf("allocation v2 status %d: %s", code, body)
	}
	// COUPLED to rules v2: only S3 matches; EC2+Lambda fall to Unallocated.
	assertKeySums(sumByKey(body), map[string]string{
		"object-storage": "6.0375",
		"Unallocated":    "26.7288",
	})

	// --- groupBy=service still works and is keyed by "key" ---
	code, body = get("?groupBy=service")
	if code != http.StatusOK {
		t.Fatalf("service status %d: %s", code, body)
	}
	if !strings.Contains(body, `"key":"Amazon Elastic Compute Cloud"`) {
		t.Errorf("service path not keyed by 'key': %s", body)
	}

	// --- missing rules file → exact 400 body (separate handler, same store) ---
	missing := filepath.Join(t.TempDir(), "nope.json")
	missSrv := httptest.NewServer(api.NewHandler("e2e", fstest.MapFS{}, store, missing))
	defer missSrv.Close()
	resp, err := http.Get(missSrv.URL + "/api/v1/costs/daily?groupBy=allocation")
	if err != nil {
		t.Fatalf("GET missing: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	mb, _ := io.ReadAll(resp.Body)
	transcript.WriteString("\n# GET ?groupBy=allocation against a missing rules file\n" + string(mb) + "\n")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing-file status %d: %s", resp.StatusCode, mb)
	}
	wantMiss := "allocation rules file not found: " + missing + " (create it, or start serve with --allocation-rules or set $COSTROID_ALLOCATION_RULES)"
	if got := strings.TrimSpace(string(mb)); got != wantMiss {
		t.Errorf("missing-file 400 body = %q, want %q", got, wantMiss)
	}

	t.Logf("\n===== OFFLINE E2E ALLOCATION TRANSCRIPT =====\n%s", transcript.String())
}

// TestOfflineE2EBusinessMetrics proves the first user-authored data-import
// path and its derived unit economics end to end. Every CLI import happens only
// after the prior handler and store are closed (DuckDB single-writer rule).
func TestOfflineE2EBusinessMetrics(t *testing.T) {
	t.Setenv("COSTROID_DATA_DIR", t.TempDir())
	if out, err := runCLI([]string{"ingest", "--connector", "aws-focus", "--path", "../../testdata/aws-focus-1.2/sample-export.csv.gz"}, ""); err != nil {
		t.Fatalf("AWS ingest: %v\n%s", err, out)
	}

	metricsPath := filepath.Join(t.TempDir(), "metrics.csv")
	writeCSV := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("writing metrics CSV: %v", err)
		}
	}
	writeCSV(metricsPath, "date,metric,quantity\n2026-05-01,requests,10\n2026-05-02,requests,20\n2026-05-08,requests,30\n")
	// No --source-label: the default basename is pinned by the output and by the
	// same-path re-import below replacing (rather than appending to) this data.
	out, err := runCLI([]string{"metrics", "import", "--path", metricsPath}, "")
	if err != nil {
		t.Fatalf("initial metrics import: %v\n%s", err, out)
	}
	for _, want := range []string{"3 business metric row(s)", "1 metric(s)", "2026-05-01 through 2026-05-08", `source label "metrics.csv"`} {
		if !strings.Contains(out, want) {
			t.Errorf("initial import output = %q, want %q", out, want)
		}
	}

	type liveAPI struct {
		store *storage.DuckDB
		srv   *httptest.Server
	}
	openAPI := func() *liveAPI {
		t.Helper()
		store, err := storage.Open(context.Background(), os.Getenv("COSTROID_DATA_DIR"))
		if err != nil {
			t.Fatalf("opening API store: %v", err)
		}
		return &liveAPI{store: store, srv: httptest.NewServer(api.NewHandler("e2e", fstest.MapFS{}, store, ""))}
	}
	closeAPI := func(live *liveAPI) {
		t.Helper()
		live.srv.Close()
		if err := live.store.Close(); err != nil {
			t.Fatalf("closing API store: %v", err)
		}
	}
	get := func(live *liveAPI, path string) (int, string) {
		t.Helper()
		resp, err := http.Get(live.srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	decodeEconomics := func(body string) api.UnitEconomics {
		t.Helper()
		var got api.UnitEconomics
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("decoding unit economics %q: %v", body, err)
		}
		return got
	}
	findDay := func(got api.UnitEconomics, date string) api.UnitEconomicsDay {
		t.Helper()
		for _, day := range got.Days {
			if day.Date.Format(time.DateOnly) == date {
				return day
			}
		}
		t.Fatalf("no day %s in %+v", date, got.Days)
		return api.UnitEconomicsDay{}
	}

	live := openAPI()
	code, listBody := get(live, "/api/v1/business-metrics")
	if code != http.StatusOK || strings.TrimSpace(listBody) != `{"metrics":[{"firstDay":"2026-05-01","lastDay":"2026-05-08","name":"requests"}]}` {
		t.Fatalf("list status=%d body=%s", code, listBody)
	}
	// D35 isolation, end to end: user-authored business metrics live in their own
	// table and must NEVER leak into the AI-usage endpoints. Here only cloud cost
	// and business metrics were imported (no AI usage), so both usage endpoints are
	// empty arrays — and in particular the "requests" business metric never appears.
	for _, path := range []string{"/api/v1/usage/tokens/daily", "/api/v1/usage/metrics/daily"} {
		code, body := get(live, path)
		if code != http.StatusOK || strings.TrimSpace(body) != `[]` {
			t.Fatalf("%s after business-metrics import: status=%d body=%s, want []", path, code, body)
		}
		if strings.Contains(body, "requests") {
			t.Errorf("business metric leaked into %s: %s", path, body)
		}
	}
	code, body := get(live, "/api/v1/unit-economics/daily?metric=requests&start=2026-05-01&end=2026-05-08")
	if code != http.StatusOK {
		t.Fatalf("unit economics status=%d body=%s", code, body)
	}
	got := decodeEconomics(body)
	// COUPLED to testdata/aws-focus-1.2: every 2026-05-01..07 cost day is
	// exactly 4.6809. COUPLED to the CSV above: days 1/2 carry quantities 10/20.
	if got.Period.CoveredDays != 2 || got.Period.Cost != "9.3618" || got.Period.Quantity != "30" || got.Period.UnitCost == nil || *got.Period.UnitCost != "0.31206" {
		t.Fatalf("initial period = %+v", got.Period)
	}
	day1, day2 := findDay(got, "2026-05-01"), findDay(got, "2026-05-02")
	if day1.Cost == nil || *day1.Cost != "4.6809" || day1.Quantity == nil || *day1.Quantity != "10" || day1.UnitCost == nil || *day1.UnitCost != "0.46809" {
		t.Errorf("day 1 = %+v", day1)
	}
	if day2.Cost == nil || *day2.Cost != "4.6809" || day2.Quantity == nil || *day2.Quantity != "20" || day2.UnitCost == nil || *day2.UnitCost != "0.234045" {
		t.Errorf("day 2 = %+v", day2)
	}
	costOnly := findDay(got, "2026-05-03")
	metricOnly := findDay(got, "2026-05-08")
	if costOnly.Cost == nil || *costOnly.Cost != "4.6809" || costOnly.Quantity != nil || costOnly.UnitCost != nil {
		t.Errorf("cost-only day = %+v", costOnly)
	}
	if metricOnly.Cost != nil || metricOnly.Quantity == nil || *metricOnly.Quantity != "30" || metricOnly.UnitCost != nil {
		t.Errorf("metric-only day = %+v", metricOnly)
	}
	closeAPI(live)

	// Same default label replaces the original three rows with this one row.
	writeCSV(metricsPath, "date,metric,quantity\n2026-05-01,requests,5\n")
	if out, err = runCLI([]string{"metrics", "import", "--path", metricsPath}, ""); err != nil {
		t.Fatalf("replacement import: %v\n%s", err, out)
	}
	live = openAPI()
	_, body = get(live, "/api/v1/unit-economics/daily?metric=requests&start=2026-05-01&end=2026-05-08")
	got = decodeEconomics(body)
	if got.Period.CoveredDays != 1 || got.Period.Cost != "4.6809" || got.Period.Quantity != "5" || got.Period.UnitCost == nil || *got.Period.UnitCost != "0.93618" || findDay(got, "2026-05-02").Quantity != nil {
		t.Fatalf("after replacement = %+v", got)
	}
	closeAPI(live)

	secondPath := filepath.Join(t.TempDir(), "second.csv")
	writeCSV(secondPath, "date,metric,quantity\n2026-05-01,requests,5\n")
	if out, err = runCLI([]string{"metrics", "import", "--path", secondPath, "--source-label", "secondary"}, ""); err != nil {
		t.Fatalf("second-label import: %v\n%s", err, out)
	}
	live = openAPI()
	_, body = get(live, "/api/v1/unit-economics/daily?metric=requests&start=2026-05-01&end=2026-05-01")
	got = decodeEconomics(body)
	// COUPLED to both CSV labels: 5 + 5 sums to 10 on the same day.
	if got.Period.Quantity != "10" || got.Period.UnitCost == nil || *got.Period.UnitCost != "0.46809" {
		t.Fatalf("cross-label sum = %+v", got.Period)
	}
	closeAPI(live)

	writeCSV(secondPath, "date,metric,quantity\n")
	out, err = runCLI([]string{"metrics", "import", "--path", secondPath, "--source-label", "secondary"}, "")
	if err != nil || !strings.Contains(out, `cleared business metrics for source label "secondary" (header-only import)`) {
		t.Fatalf("header-only clear = (%q, %v)", out, err)
	}
	live = openAPI()
	defer closeAPI(live)
	_, body = get(live, "/api/v1/unit-economics/daily?metric=requests&start=2026-05-01&end=2026-05-01")
	got = decodeEconomics(body)
	if got.Period.Quantity != "5" || got.Period.UnitCost == nil || *got.Period.UnitCost != "0.93618" {
		t.Fatalf("after header-only clear = %+v", got.Period)
	}
}

// anomalyRow mirrors one api.Anomaly for decoding the anomalies endpoint.
type anomalyRow struct {
	Date      string  `json:"date"`
	Scope     string  `json:"scope"`
	Key       *string `json:"key"`
	Direction string  `json:"direction"`
	Observed  string  `json:"observed"`
	Median    string  `json:"median"`
	Mad       string  `json:"mad"`
	ScaledMad string  `json:"scaledMad"`
	Threshold string  `json:"threshold"`
	Deviation string  `json:"deviation"`
}

func (a anomalyRow) equal(b anomalyRow) bool {
	keyEqual := (a.Key == nil) == (b.Key == nil) && (a.Key == nil || *a.Key == *b.Key)
	return keyEqual && a.Date == b.Date && a.Scope == b.Scope && a.Direction == b.Direction &&
		a.Observed == b.Observed && a.Median == b.Median && a.Mad == b.Mad &&
		a.ScaledMad == b.ScaledMad && a.Threshold == b.Threshold && a.Deviation == b.Deviation
}

func equalAnomalies(got, want []anomalyRow) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if !got[i].equal(want[i]) {
			return false
		}
	}
	return true
}

func strPtr(s string) *string { return &s }

// anomaliesView opens the store and GETs /api/v1/anomalies with the given raw
// query for the default tenant, returning the JSON body.
func anomaliesView(t *testing.T, query string) string {
	t.Helper()
	store, err := storage.Open(context.Background(), os.Getenv("COSTROID_DATA_DIR"))
	if err != nil {
		t.Fatalf("opening store for anomalies view: %v", err)
	}
	defer func() { _ = store.Close() }()

	handler := api.NewHandler("e2e", fstest.MapFS{}, store, "")
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/anomalies" + query)
	if err != nil {
		t.Fatalf("GET anomalies: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("anomalies HTTP %d: %s", resp.StatusCode, body)
	}
	return string(body)
}

// TestOfflineE2EAnomalies is the hermetic end-to-end proof for query-time
// anomaly detection: a real CLI ingest of the anomaly sample export, then
// in-process GETs of /api/v1/anomalies asserting the EXACT flagged dates,
// directions, and decimal-string statistics of the spike and dip (each flagging
// both the total and the single key), that mundane days are absent, that flags
// are range-independent, and that groupBy=provider composes.
func TestOfflineE2EAnomalies(t *testing.T) {
	t.Setenv("COSTROID_DATA_DIR", t.TempDir())
	var transcript strings.Builder

	if out, err := runCLI([]string{"ingest", "--connector", "aws-focus", "--path", "../../testdata/aws-focus-anomaly/sample-export.csv.gz"}, ""); err != nil {
		t.Fatalf("ingest: %v\n%s", err, out)
	}

	const ec2 = "Amazon Elastic Compute Cloud"
	// Hand-computed from the fixture (see testdata/aws-focus-anomaly/README.md).
	spikeTotal := anomalyRow{Date: "2026-06-15", Scope: "total", Direction: "increase", Observed: "200", Median: "100", Mad: "2", ScaledMad: "2.9652", Threshold: "8.8956", Deviation: "100"}
	dipTotal := anomalyRow{Date: "2026-06-17", Scope: "total", Direction: "decrease", Observed: "40", Median: "101", Mad: "2", ScaledMad: "2.9652", Threshold: "8.8956", Deviation: "61"}
	keyOf := func(a anomalyRow, key string) anomalyRow { a.Scope, a.Key = "key", strPtr(key); return a }

	full := anomaliesView(t, "")
	transcript.WriteString("# GET /api/v1/anomalies\n" + full + "\n")
	var resp struct {
		Currency   string `json:"currency"`
		Parameters struct {
			K                   string `json:"k"`
			ConsistencyConstant string `json:"consistencyConstant"`
			RelativeFloor       string `json:"relativeFloor"`
			GroupBy             string `json:"groupBy"`
			WindowDays          int    `json:"windowDays"`
			MinObservations     int    `json:"minObservations"`
		} `json:"parameters"`
		Anomalies []anomalyRow `json:"anomalies"`
	}
	if err := json.Unmarshal([]byte(full), &resp); err != nil {
		t.Fatalf("decode anomalies: %v (body %s)", err, full)
	}
	if resp.Currency != "USD" || resp.Parameters.K != "3" || resp.Parameters.ConsistencyConstant != "1.4826" ||
		resp.Parameters.WindowDays != 30 || resp.Parameters.MinObservations != 10 || resp.Parameters.RelativeFloor != "0.1" || resp.Parameters.GroupBy != "service" {
		t.Fatalf("currency/parameters = %s / %+v", resp.Currency, resp.Parameters)
	}
	// The spike and dip each flag the total AND the single service's key, ordered
	// date-asc then total-before-key (a live pin of the ordering rule).
	want := []anomalyRow{spikeTotal, keyOf(spikeTotal, ec2), dipTotal, keyOf(dipTotal, ec2)}
	if !equalAnomalies(resp.Anomalies, want) {
		t.Fatalf("anomalies = %s\nwant %+v", full, want)
	}
	// Mundane days never appear (day 11 is scored but within noise; 16/18 return
	// to normal after the spike/dip).
	for _, a := range resp.Anomalies {
		if a.Date == "2026-06-11" || a.Date == "2026-06-16" || a.Date == "2026-06-18" {
			t.Errorf("mundane day flagged: %+v", a)
		}
	}

	// Range independence: a one-day window around the spike reports the identical
	// spike flags.
	narrow := anomaliesView(t, "?start=2026-06-15&end=2026-06-15")
	var nresp struct {
		Anomalies []anomalyRow `json:"anomalies"`
	}
	if err := json.Unmarshal([]byte(narrow), &nresp); err != nil {
		t.Fatalf("decode narrow: %v", err)
	}
	if !equalAnomalies(nresp.Anomalies, []anomalyRow{spikeTotal, keyOf(spikeTotal, ec2)}) {
		t.Fatalf("narrow-window anomalies = %s, want the identical spike flags", narrow)
	}

	// groupBy=provider composes: PublisherName "AWS" keys everything under "AWS".
	prov := anomaliesView(t, "?groupBy=provider")
	var presp struct {
		Parameters struct {
			GroupBy string `json:"groupBy"`
		} `json:"parameters"`
		Anomalies []anomalyRow `json:"anomalies"`
	}
	if err := json.Unmarshal([]byte(prov), &presp); err != nil {
		t.Fatalf("decode provider: %v", err)
	}
	if presp.Parameters.GroupBy != "provider" {
		t.Errorf("provider groupBy echo = %q", presp.Parameters.GroupBy)
	}
	if !equalAnomalies(presp.Anomalies, []anomalyRow{spikeTotal, keyOf(spikeTotal, "AWS"), dipTotal, keyOf(dipTotal, "AWS")}) {
		t.Fatalf("provider anomalies = %s", prov)
	}

	t.Logf("\n===== OFFLINE E2E ANOMALIES TRANSCRIPT =====\n%s", transcript.String())
}

// dailyView opens the store and queries the daily-cost API for the default
// tenant, returning the JSON body.
func dailyView(t *testing.T) string {
	t.Helper()
	store, err := storage.Open(context.Background(), os.Getenv("COSTROID_DATA_DIR"))
	if err != nil {
		t.Fatalf("opening store for daily view: %v", err)
	}
	defer func() { _ = store.Close() }()

	handler := api.NewHandler("e2e", fstest.MapFS{}, store, "")
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/costs/daily")
	if err != nil {
		t.Fatalf("GET daily costs: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("daily costs HTTP %d: %s", resp.StatusCode, body)
	}
	return string(body)
}

// dailyServiceCosts sums the given services' BilledCost across every day in the
// default-tenant daily-cost view, decimal-exact (never float). Used to assert a
// fixture's total from a daily-view-exposed fact.
func dailyServiceCosts(t *testing.T, services ...string) decimal.Decimal {
	t.Helper()
	var resp struct {
		Days []struct {
			Services []struct {
				Key  string `json:"key"`
				Cost string `json:"cost"`
			} `json:"services"`
		} `json:"days"`
	}
	if err := json.Unmarshal([]byte(dailyView(t)), &resp); err != nil {
		t.Fatalf("decoding daily costs: %v", err)
	}
	want := map[string]bool{}
	for _, s := range services {
		want[s] = true
	}
	sum := decimal.Zero
	for _, d := range resp.Days {
		for _, s := range d.Services {
			if want[s.Key] {
				sum = sum.Add(decimal.RequireFromString(s.Cost))
			}
		}
	}
	return sum
}

// tokenRow mirrors one api.DailyTokenUsage item for decoding the token-usage
// endpoint's JSON body in the e2e.
type tokenRow struct {
	Date             string `json:"date"`
	ServiceName      string `json:"serviceName"`
	ConsumedUnit     string `json:"consumedUnit"`
	ConsumedQuantity string `json:"consumedQuantity"`
}

// tokensView opens the store and queries the token-usage API for the default
// tenant, returning the JSON body.
func tokensView(t *testing.T) string {
	t.Helper()
	store, err := storage.Open(context.Background(), os.Getenv("COSTROID_DATA_DIR"))
	if err != nil {
		t.Fatalf("opening store for token view: %v", err)
	}
	defer func() { _ = store.Close() }()

	handler := api.NewHandler("e2e", fstest.MapFS{}, store, "")
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/usage/tokens/daily")
	if err != nil {
		t.Fatalf("GET token usage: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token usage HTTP %d: %s", resp.StatusCode, body)
	}
	return string(body)
}

// usageMetricRow mirrors one api.DailyUsageMetric item for decoding the
// usage-metrics endpoint's JSON body in the e2e.
type usageMetricRow struct {
	Date        string `json:"date"`
	ServiceName string `json:"serviceName"`
	ServiceTier string `json:"serviceTier"`
	MetricName  string `json:"metricName"`
	Unit        string `json:"unit"`
	Quantity    string `json:"quantity"`
}

// usageMetricsView opens the store and queries the usage-metrics API for the
// default tenant, returning the JSON body.
func usageMetricsView(t *testing.T) string {
	t.Helper()
	store, err := storage.Open(context.Background(), os.Getenv("COSTROID_DATA_DIR"))
	if err != nil {
		t.Fatalf("opening store for usage-metrics view: %v", err)
	}
	defer func() { _ = store.Close() }()

	handler := api.NewHandler("e2e", fstest.MapFS{}, store, "")
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/usage/metrics/daily")
	if err != nil {
		t.Fatalf("GET usage metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("usage metrics HTTP %d: %s", resp.StatusCode, body)
	}
	return string(body)
}

// usageMetricCount opens the store and returns how many grouped default-tenant
// usage-metric rows exist across all dates (0 when the table has been cleared).
func usageMetricCount(t *testing.T) int {
	t.Helper()
	store, err := storage.Open(context.Background(), os.Getenv("COSTROID_DATA_DIR"))
	if err != nil {
		t.Fatalf("opening store for usage-metric count: %v", err)
	}
	defer func() { _ = store.Close() }()
	metrics, err := store.DailyUsageMetrics(context.Background(), focus.DefaultTenant, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyUsageMetrics: %v", err)
	}
	return len(metrics)
}

// findUsageMetric returns the quantity of the row matching (date, service, tier,
// metric) in the default-tenant usage-metrics view, or ("", false).
func findUsageMetric(t *testing.T, date, service, tier, metric string) (string, bool) {
	t.Helper()
	var rows []usageMetricRow
	if err := json.Unmarshal([]byte(usageMetricsView(t)), &rows); err != nil {
		t.Fatalf("decoding usage metrics: %v", err)
	}
	for _, r := range rows {
		if r.Date == date && r.ServiceName == service && r.ServiceTier == tier && r.MetricName == metric {
			return r.Quantity, true
		}
	}
	return "", false
}

// tenantDaysNonEmpty reports whether the given tenant has any stored cost
// days.
func tenantDaysNonEmpty(t *testing.T, tenant string) bool {
	t.Helper()
	store, err := storage.Open(context.Background(), os.Getenv("COSTROID_DATA_DIR"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer func() { _ = store.Close() }()
	daily, err := store.DailyCostsByService(context.Background(), tenant, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyCostsByService(%s): %v", tenant, err)
	}
	return len(daily.Days) > 0
}

func readKeyFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading key file: %v", err)
	}
	return strings.TrimSpace(string(b))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	if len(s) > 24 {
		return s[:24] + "…"
	}
	return s
}

// TestServeAuthWiringE2E proves that a bearer-configured handler ACTUALLY
// installs the auth middleware end to end (required test 11) — secure-by-default
// lives in serve/WithAuth, not in the auth-free NewHandler default. A data
// endpoint 401s without a token and 200s with the correct Authorization header;
// /healthz stays reachable without a token. Uses the in-process
// api.NewHandler(...) + httptest.NewServer pattern with an empty store.
func TestServeAuthWiringE2E(t *testing.T) {
	const token = "e2e-bearer-token"
	store, err := storage.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer func() { _ = store.Close() }()

	handler := api.NewHandler("e2e", fstest.MapFS{}, store, "", api.WithAuth(api.NewBearerAuth(token)))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	do := func(t *testing.T, path, authHeader string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if authHeader != "" {
			req.Header.Set("Authorization", authHeader)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	if code, _ := do(t, "/api/v1/costs/daily", ""); code != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/costs/daily without a token = %d, want 401", code)
	}
	code, body := do(t, "/api/v1/costs/daily", "Bearer "+token)
	if code != http.StatusOK {
		t.Fatalf("GET /api/v1/costs/daily with the token = %d, want 200\n%s", code, body)
	}
	if !strings.Contains(body, `"days"`) {
		t.Errorf("authenticated body = %q, want a daily-costs payload with a days array", body)
	}
	if code, _ := do(t, "/healthz", ""); code == http.StatusUnauthorized {
		t.Error("GET /healthz without a token = 401, want it reachable unauthenticated")
	}
}
