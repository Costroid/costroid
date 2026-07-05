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

	"github.com/Costroid/costroid/internal/api"
	"github.com/Costroid/costroid/internal/devtools/fakeanthropic"
	"github.com/Costroid/costroid/internal/devtools/fakeopenai"
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
	mustCLI("", "ingest", "--connector", "openai-cost", oaiBase, "--since", "2026-05")

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

	// --- restatement: swap in the restated month, re-sync, show the delta ---
	copyTree(t, "../../testdata/anthropic-cost/restated", anthDir)
	copyTree(t, "../../testdata/openai-cost/restated", oaiDir)
	restatedAnth := mustCLI("", "ingest", "--connector", "anthropic-cost", anthBase, "--since", "2026-05")
	if !strings.Contains(restatedAnth, "period 2026-05: replaced (3 records; BilledCost 74.845678 → 72.5)") {
		t.Errorf("anthropic restatement delta missing/wrong:\n%s", restatedAnth)
	}
	if !strings.Contains(restatedAnth, "period 2026-06: source content unchanged") {
		t.Errorf("unchanged June should still short-circuit after May restatement:\n%s", restatedAnth)
	}
	restatedOai := mustCLI("", "ingest", "--connector", "openai-cost", oaiBase, "--period", "2026-05")
	if !strings.Contains(restatedOai, "period 2026-05: replaced (3 records; BilledCost 132.7067890123456789 → 106.75)") {
		t.Errorf("openai restatement delta missing/wrong (exact-decimal preservation):\n%s", restatedOai)
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
