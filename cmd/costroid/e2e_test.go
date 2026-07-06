// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/api"
	"github.com/Costroid/costroid/internal/devtools/fakeanthropic"
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
	// exactly the enumerated cost-orphaned classes and NOTHING else — the
	// Anthropic priority (999) and flex_discount (123) tier tokens, the
	// web_search_requests count (5, unit "Requests"), the standard-tier orphan
	// key no cost row referenced (delta uncached 42), and the OpenAI
	// recognized-but-unpriced line item (assistants api | file search 42, unit
	// "Unknown"). Ordering is day, serviceName, serviceTier, metricName, unit.
	metricsBody := usageMetricsView(t)
	transcript.WriteString("\n# GET /api/v1/usage/metrics/daily (default tenant)\n" + metricsBody + "\n")
	var metricRows []usageMetricRow
	if err := json.Unmarshal([]byte(metricsBody), &metricRows); err != nil {
		t.Fatalf("decoding usage metrics: %v (body: %s)", err, metricsBody)
	}
	wantMetrics := []usageMetricRow{
		{Date: "2026-05-01", ServiceName: "claude-opus-4-6", ServiceTier: "priority", MetricName: "uncached_input_tokens", Unit: "Tokens", Quantity: "999"},
		{Date: "2026-05-01", ServiceName: "claude-opus-4-6", ServiceTier: "standard", MetricName: "web_search_requests", Unit: "Requests", Quantity: "5"},
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
	// MUTATION INTENT: capturing any referenced/enriched standard|batch agg key
	// as a usage metric MUST break this complete-set assertion — the fixtures'
	// large enriched token quantities (700000/800000 standard uncached, etc.)
	// that DO join to cost rows must be ABSENT here. June contributes ZERO
	// usage-metric rows because every June usage key is cost-referenced; over-
	// capture would surface most visibly in the otherwise-empty June. Because the
	// leak would be in a different table, it is invisible to the money-invariance
	// and FOCUS-isolation checks — this is the ONLY guard that catches it.
	for _, r := range metricRows {
		if strings.HasPrefix(r.Date, "2026-06") {
			t.Errorf("June should contribute ZERO usage metrics (all cost-referenced) but got: %+v", r)
		}
	}
	// ISOLATION: the usage-metric model-name services and units never appear in
	// the daily-cost or daily-token views (a separate table, separate query).
	for _, leaked := range []string{"claude-opus-4-6", "claude-sonnet-4-5", "web_search_requests", "assistants api | file search"} {
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
	fcsv12       = "../../testdata/focus-csv/focus-1.2.csv"
	fcsv13       = "../../testdata/focus-csv/focus-1.3.csv"
	fcsv14       = "../../testdata/focus-csv/focus-1.4.csv"
	fcsv14Restat = "../../testdata/focus-csv/focus-1.4-restated.csv"
	fcsvDupHdr   = "../../testdata/focus-csv/negative/duplicate-header.csv"
	fcsvUnkHdr   = "../../testdata/focus-csv/negative/unknown-header.csv"
	fcsvNull     = "../../testdata/focus-csv/negative/literal-null.csv"
	fcsv10       = "../../testdata/focus-csv/focus-1.0.csv"
)

// TestOfflineE2EFocusCSV is the hermetic, file-only end-to-end proof for the
// generic focus-csv importer: it ingests conformant 1.2/1.3/1.4 exports under
// distinct labels alongside AWS cloud sample data, shows them in the daily
// view, re-imports unchanged, restates one month, exercises --period / --force
// / --tenant, and runs every negative (the 1.0/1.1 rejections asserted VERBATIM,
// the rest by actionable substrings).
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

	// --- daily view shows the focus-csv services alongside the AWS sample ---
	daily := dailyView(t)
	transcript.WriteString("\n# GET /api/v1/costs/daily (default tenant)\n" + daily + "\n")
	for _, svc := range []string{"AWS Lambda", "Datadog Pro", "Azure Virtual Machines", "Amazon Elastic Compute Cloud"} {
		if !strings.Contains(daily, svc) {
			t.Errorf("daily view missing service %q:\n%s", svc, daily)
		}
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

	// The 1.0 / 1.1 rejections are asserted VERBATIM (message-as-contract).
	want10 := "FOCUS 1.0 identifies entities via ProviderName/PublisherName " +
		"(replaced by ServiceProviderName/HostProviderName in 1.3, removed in 1.4); " +
		"no 1.0 → 1.4 transform is implemented — re-export as FOCUS 1.2 or later " +
		"(AWS Data Exports and Microsoft Cost Management both offer 1.2)."
	negative("focus-version 1.0", want10,
		"ingest", "--connector", "focus-csv", "--path", fcsv10, "--focus-version", "1.0")
	negative("focus-version 1.1", strings.ReplaceAll(want10, "1.0", "1.1"),
		"ingest", "--connector", "focus-csv", "--path", fcsv12, "--focus-version", "1.1")

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

// dailyView opens the store and queries the daily-cost API for the default
// tenant, returning the JSON body.
func dailyView(t *testing.T) string {
	t.Helper()
	store, err := storage.Open(context.Background(), os.Getenv("COSTROID_DATA_DIR"))
	if err != nil {
		t.Fatalf("opening store for daily view: %v", err)
	}
	defer func() { _ = store.Close() }()

	handler := api.NewHandler("e2e", fstest.MapFS{}, store)
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

	handler := api.NewHandler("e2e", fstest.MapFS{}, store)
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

	handler := api.NewHandler("e2e", fstest.MapFS{}, store)
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
