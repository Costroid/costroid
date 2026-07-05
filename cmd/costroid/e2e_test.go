// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package main

import (
	"bytes"
	"context"
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

	// --- unchanged re-sync: every period unchanged, rewrite short-circuited ---
	reOut := mustCLI("", "ingest", "--connector", "anthropic-cost", anthBase, "--since", "2026-05")
	for _, m := range expectedMonths {
		if !strings.Contains(reOut, "period "+m+": source content unchanged") {
			t.Errorf("re-sync did not report month %s unchanged:\n%s", m, reOut)
		}
	}
	// The unchanged re-sync still fetched BOTH endpoints (the ContentHash covers
	// cost AND usage payloads, so a quantity-only restatement cannot be missed).
	for _, path := range []string{"/v1/organizations/cost_report", "/v1/organizations/usage_report/messages"} {
		if !strings.Contains(fakeLog.String(), path) {
			t.Errorf("expected the fake to have served %s (both endpoints fetched)", path)
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
