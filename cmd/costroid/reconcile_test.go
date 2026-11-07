package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	reconcileSamples = "../../internal/reconcile/testdata/documentation-samples/anthropic"
	sentinelKey      = "sk-ant-admin01-sentinel"
)

type replayDoer struct {
	t         *testing.T
	usage     string
	cost      string
	requested int
}

func (d *replayDoer) Do(request *http.Request) (*http.Response, error) {
	d.requested++
	file := d.usage
	if strings.HasSuffix(request.URL.Path, "/cost") {
		file = d.cost
	}
	data, err := os.ReadFile(filepath.Join(reconcileSamples, file))
	if err != nil {
		d.t.Fatal(err)
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(data))}, nil
}

func fixedNow() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC) }

func environment(name string) string {
	if name == anthropicKeyVariable {
		return sentinelKey
	}
	return ""
}

// writeLedger writes the four rows usage.json matches, with one count moved.
func writeLedger(t *testing.T, input int) string {
	t.Helper()
	line := `{"BillingCurrency":"USD","ChargeDescription":"claude-sonnet-5 tokens","ChargePeriodStart":"2026-10-14T09:31:02Z","ConsumedQuantity":"%d","ConsumedUnit":"Tokens","HostProviderName":"Anthropic","ListCost":"%s","ServiceProviderName":"Anthropic","SkuMeter":"%s","SkuPriceId":"anthropic:claude-sonnet-5:global:standard:all:%s","SubAccountId":"apikey_01ExampleCi","Tags":{"costroid/cost_basis":"measured","costroid/model":"claude-sonnet-5"},"x_CostroidRowId":"%s"}` + "\n"
	var rows strings.Builder
	for _, row := range []struct {
		tokens      int
		list, class string
	}{
		{input, "0.2", "input_uncached"},
		{20000, "0.05", "cache_write_5m"},
		{300000, "0.06", "cache_read"},
		{50000, "0.5", "output"},
	} {
		fmt.Fprintf(&rows, line, row.tokens, row.list, row.class, row.class, row.class)
	}
	path := filepath.Join(t.TempDir(), "rows.jsonl")
	if err := os.WriteFile(path, []byte(rows.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runReconcile(t *testing.T, doer *replayDoer, getenv func(string) string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := reconcileCommand(context.Background(), args, getenv, doer, fixedNow, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestReconcilePassesInsideTheTolerance(t *testing.T) {
	doer := &replayDoer{t: t, usage: "usage.json", cost: "cost-at-list.json"}
	code, stdout, stderr := runReconcile(t, doer, environment, "--rows", writeLedger(t, 100900), "--from", "2026-10-14", "--to", "2026-10-14")
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
	}
	for _, want := range []string{"TOKENS", "DOLLARS", "input_uncached", "100900", "0.90%", "within 1%", "within 10%"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table lacks %q:\n%s", want, stdout)
		}
	}
}

func TestReconcileExitsNonZeroOnATokensMiss(t *testing.T) {
	doer := &replayDoer{t: t, usage: "usage.json", cost: "cost-at-list.json"}
	code, stdout, _ := runReconcile(t, doer, environment, "--rows", writeLedger(t, 101100), "--from", "2026-10-14", "--to", "2026-10-14")
	if code != exitTokensMiss {
		t.Fatalf("exit %d, want %d\n%s", code, exitTokensMiss, stdout)
	}
	if !strings.Contains(stdout, "MISS") || !strings.Contains(stdout, "1.10%") {
		t.Errorf("table:\n%s", stdout)
	}
}

func TestReconcileExitsZeroWhileTheProviderIsStillReporting(t *testing.T) {
	doer := &replayDoer{t: t, usage: "usage-refreshing.json", cost: "cost-refreshing.json"}
	code, stdout, _ := runReconcile(t, doer, environment, "--rows", writeLedger(t, 900000), "--from", "2026-10-14", "--to", "2026-10-14")
	if code != 0 || !strings.Contains(stdout, "pending") {
		t.Errorf("exit %d\n%s", code, stdout)
	}
}

func TestReconcileNamesAMissingKeyAndCallsNoProvider(t *testing.T) {
	doer := &replayDoer{t: t, usage: "usage.json", cost: "cost-at-list.json"}
	unset := func(string) string { return "" }
	code, _, stderr := runReconcile(t, doer, unset, "--rows", writeLedger(t, 100000), "--from", "2026-10-14", "--to", "2026-10-14")
	if code != exitFailure || !strings.Contains(stderr, anthropicKeyVariable) {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
	if doer.requested != 0 {
		t.Errorf("%d requests went out", doer.requested)
	}
}

func TestReconcilePrintsNoKey(t *testing.T) {
	doer := &replayDoer{t: t, usage: "usage.json", cost: "cost-unexplained.json"}
	out := filepath.Join(t.TempDir(), "reconciliation.jsonl")
	_, stdout, stderr := runReconcile(t, doer, environment, "--rows", writeLedger(t, 100000), "--from", "2026-10-14", "--to", "2026-10-14", "--out", out)
	stored, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout+stderr+string(stored), sentinelKey) {
		t.Error("the key reached an output")
	}
}

func TestReconcileRerunKeepsOneRowPerKey(t *testing.T) {
	rows := writeLedger(t, 100000)
	out := filepath.Join(t.TempDir(), "reconciliation.jsonl")
	args := []string{"--rows", rows, "--from", "2026-10-14", "--to", "2026-10-14", "--out", out}
	runReconcile(t, &replayDoer{t: t, usage: "usage.json", cost: "cost-at-list.json"}, environment, args...)
	first, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	runReconcile(t, &replayDoer{t: t, usage: "usage.json", cost: "cost-at-list.json"}, environment, args...)
	second, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("the rerun moved the file:\n%s\n%s", first, second)
	}
	runReconcile(t, &replayDoer{t: t, usage: "usage.json", cost: "cost-contract-rate.json"}, environment, args...)
	third, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if lines := bytes.Count(third, []byte("\n")); lines != 1 || !bytes.Contains(third, []byte(`"provider":"0.7695"`)) {
		t.Errorf("the restated figure did not replace the row:\n%s", third)
	}
}

// A recorded run through the real writer, so the reader is held to real rows.
// Those rows are list-equivalent and name no credential, so the first call
// refuses and the second reads them under --credential.
func TestReconcileReadsTheRowsARunWrites(t *testing.T) {
	binary := replayClaude(t)
	out := filepath.Join(t.TempDir(), "rows.jsonl")
	if code, _, stderr := execute(t, "run", "--budget-usd", "5", "--out", out, "--", binary, "-p", "hello"); code != 0 {
		t.Fatalf("run exited %d: %s", code, stderr)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	day := recordedDay(t, data)
	client := emptyReport{refreshed: "2026-11-03T00:00:00Z"}
	later := func() time.Time { return time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC) }
	var stdout, stderr bytes.Buffer
	code := reconcileCommand(context.Background(), []string{"--rows", out, "--from", day, "--to", day}, environment, client, later, &stdout, &stderr)
	if code != exitFailure || !strings.Contains(stderr.String(), "--credential") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	stdout.Reset()
	code = reconcileCommand(context.Background(), []string{"--rows", out, "--from", day, "--to", day, "--credential", "apikey_01ExampleCi"}, environment, client, later, &stdout, &stderr)
	if code != exitTokensMiss {
		t.Fatalf("exit %d, want a miss against an empty report: %s", code, stderr.String())
	}
	for _, want := range []string{"cache_read", "15605", "MISS", "no API bill"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("table lacks %q:\n%s", want, stdout.String())
		}
	}
}

// emptyReport is a provider that has finished reporting and holds nothing.
type emptyReport struct {
	refreshed string
}

func (e emptyReport) Do(*http.Request) (*http.Response, error) {
	body := fmt.Sprintf(`{"data":[],"has_more":false,"next_page":null,"data_refreshed_at":%q}`, e.refreshed)
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func recordedDay(t *testing.T, rows []byte) string {
	t.Helper()
	_, rest, found := bytes.Cut(rows, []byte(`"ChargePeriodStart":"`))
	if !found {
		t.Fatal("no ChargePeriodStart in the rows")
	}
	return string(rest[:len("2006-01-02")])
}

func TestReconcileRefusesARangeThatRunsBackwards(t *testing.T) {
	doer := &replayDoer{t: t}
	code, _, stderr := runReconcile(t, doer, environment, "--rows", "rows.jsonl", "--from", "2026-10-14", "--to", "2026-10-13")
	if code != exitFailure || !strings.Contains(stderr, "before") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}

func TestReconcileIsOnTheHelpPage(t *testing.T) {
	code, stdout, _ := execute(t, "help", "reconcile")
	if code != 0 || !strings.Contains(stdout, "costroid reconcile --rows rows.jsonl --from D --to D") {
		t.Errorf("exit %d:\n%s", code, stdout)
	}
}

func saveReports(t *testing.T, usage, cost string) string {
	t.Helper()
	directory := t.TempDir()
	copyInto(t, filepath.Join(reconcileSamples, usage), filepath.Join(directory, "anthropic-usage.json"))
	copyInto(t, filepath.Join(reconcileSamples, cost), filepath.Join(directory, "anthropic-cost.json"))
	return directory
}

func TestSavedReportsReconcileWithNoKeyAndNoClient(t *testing.T) {
	refuses := &replayDoer{t: t, usage: "gone.json", cost: "gone.json"}
	code, stdout, stderr := runReconcile(t, refuses, func(string) string { return "" }, "--rows", writeLedger(t, 100900),
		"--from", "2026-10-14", "--to", "2026-10-14", "--credential", "apikey_01ExampleCi",
		"--reports", saveReports(t, "usage.json", "cost-at-list.json"), "--as-of", "2026-10-20T12:00:00Z")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if refuses.requested != 0 {
		t.Fatalf("%d requests reached the client", refuses.requested)
	}
	if !strings.Contains(stdout, "within 1%") {
		t.Fatalf("stdout: %s", stdout)
	}
}

func TestSavedReportsStillMissAMovedCount(t *testing.T) {
	code, stdout, _ := runReconcile(t, &replayDoer{t: t}, environment, "--rows", writeLedger(t, 101200),
		"--from", "2026-10-14", "--to", "2026-10-14", "--credential", "apikey_01ExampleCi",
		"--reports", saveReports(t, "usage.json", "cost-at-list.json"), "--as-of", "2026-10-20T12:00:00Z")
	if code != exitTokensMiss {
		t.Fatalf("exit %d: %s", code, stdout)
	}
}

func TestSavedReportsAreReadAsOfTheirOwnInstant(t *testing.T) {
	code, stdout, stderr := runReconcile(t, &replayDoer{t: t}, environment, "--rows", writeLedger(t, 100000),
		"--from", "2026-10-14", "--to", "2026-10-14", "--credential", "apikey_01ExampleCi",
		"--reports", saveReports(t, "usage.json", "cost-at-list.json"), "--as-of", "2026-10-14T12:00:00Z")
	if code != 0 || strings.Contains(stdout, "within 1%") || !strings.Contains(stdout, "pending") {
		t.Fatalf("exit %d: %s %s", code, stdout, stderr)
	}
}

func TestReportsAndAsOfGoTogether(t *testing.T) {
	rows := writeLedger(t, 100000)
	base := []string{"--rows", rows, "--from", "2026-10-14", "--to", "2026-10-14"}
	for _, extra := range [][]string{{"--reports", "x"}, {"--as-of", "2026-10-20T12:00:00Z"}, {"--reports", "x", "--as-of", "yesterday"}} {
		if code, _, _ := runReconcile(t, &replayDoer{t: t}, environment, append(base, extra...)...); code != exitFailure {
			t.Errorf("%v exited %d", extra, code)
		}
	}
}

func TestASavedReportAnswersOneRequestSoARangeCannotCountItTwice(t *testing.T) {
	reports := &savedReports{directory: saveReports(t, "usage.json", "cost-at-list.json")}
	first, err := http.NewRequest(http.MethodGet, "https://api.anthropic.com/v1/organizations/analytics/usage", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reports.Do(first); err != nil {
		t.Fatal(err)
	}
	if _, err := reports.Do(first); err == nil {
		t.Fatal("a second request read the file again")
	}
	other, err := http.NewRequest(http.MethodGet, "https://example.test/elsewhere", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reports.Do(other); err == nil {
		t.Fatal("an unknown path was answered")
	}
}
