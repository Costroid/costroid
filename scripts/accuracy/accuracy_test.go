package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/focus"
)

// Every day these tests keep is synthetic: hand-written rows and provider
// reports in the shape the providers document. No provider was called. Each
// report says so in its x_synthetic field.
const (
	syntheticNote = "hand-written for a test; no provider was called"
	credential    = "apikey_01SyntheticTestKey"
	otherKey      = "apikey_01SyntheticOtherKey"
	model         = "claude-sonnet-5"
	adminKey      = "sk-ant-admin01-synthetic-sentinel-0123456789"
	firstDay      = "2026-11-10"
)

// costroid is the binary every test reconciles with, built once.
var costroid string

func TestMain(m *testing.M) {
	scratch, err := os.MkdirTemp("", "accuracy-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	costroid = filepath.Join(scratch, "costroid")
	if err := goBuild(context.Background(), costroid, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(scratch)
	os.Exit(code)
}

// sonnet's list prices per million tokens on catalog 0.1.0.
var listPerMillion = map[focus.TokenClass]string{
	focus.InputUncached: "2",
	focus.CacheWrite5m:  "2.50",
	focus.CacheRead:     "0.20",
	focus.Output:        "10",
}

// day is one synthetic day. The ledger holds the tokens below at list price,
// 0.81 USD. The provider reports outputReported output tokens and bills
// amountCents against listCents.
type day struct {
	date           string
	agentVersion   string
	outputReported int64
	amountCents    string
	listCents      string
	noReport       bool
}

func passingDay(date string) day {
	return day{date: date, agentVersion: "2.1.316", outputReported: 50000, amountCents: "81.000000", listCents: "81.000000"}
}

var ledgerTokens = map[focus.TokenClass]int64{
	focus.InputUncached: 100000,
	focus.CacheWrite5m:  20000,
	focus.CacheRead:     300000,
	focus.Output:        50000,
}

func (d day) rows(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	for _, class := range []focus.TokenClass{focus.InputUncached, focus.CacheWrite5m, focus.CacheRead, focus.Output} {
		tokens := ledgerTokens[class]
		row := map[string]any{
			"BillingCurrency":          "USD",
			"ChargeDescription":        model + " " + string(class) + " tokens",
			"ChargePeriodStart":        d.date + "T06:20:00Z",
			"ConsumedQuantity":         fmt.Sprint(tokens),
			"ConsumedUnit":             "Tokens",
			"HostProviderName":         "Anthropic",
			"ListCost":                 listCost(t, class, tokens),
			"RegionId":                 "global",
			"ServiceProviderName":      "Anthropic",
			"SkuMeter":                 class,
			"SkuPriceId":               "anthropic:" + model + ":global:standard:all:" + string(class),
			"SubAccountId":             credential,
			"x_CostroidCatalogVersion": "0.1.0",
			"x_CostroidRowId":          d.date + "-" + string(class),
			"x_CostroidServiceTier":    "standard",
			"Tags": map[string]string{
				focus.TagAgent:        "claude-code",
				focus.TagAgentVersion: d.agentVersion,
				focus.TagCostBasis:    string(focus.Measured),
				focus.TagModel:        model,
			},
		}
		line, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(append(line, '\n'))
	}
	return out.Bytes()
}

func listCost(t *testing.T, class focus.TokenClass, tokens int64) string {
	t.Helper()
	costs := map[focus.TokenClass]string{
		focus.InputUncached: "0.2",
		focus.CacheWrite5m:  "0.05",
		focus.CacheRead:     "0.06",
		focus.Output:        "0.5",
	}
	if ledgerTokens[class] != tokens || listPerMillion[class] == "" {
		t.Fatalf("no stated list cost for %d %s tokens", tokens, class)
	}
	return costs[class]
}

func (d day) next() string {
	at, err := time.Parse(dayLayout, d.date)
	if err != nil {
		panic(err)
	}
	return at.AddDate(0, 0, 1).Format(dayLayout)
}

func (d day) usageReport() []byte {
	return []byte(fmt.Sprintf(`{
  "x_synthetic": %q,
  "data": [
    {
      "starting_at": "%sT00:00:00Z",
      "ending_at": "%sT00:00:00Z",
      "group": {"api_key_id": %q, "model": %q, "service_tier": "standard", "inference_geo": "global", "speed": "standard"},
      "usage": {
        "uncached_input_tokens": 100000,
        "cache_creation": {"ephemeral_1h_input_tokens": 0, "ephemeral_5m_input_tokens": 20000},
        "cache_read_input_tokens": 300000,
        "output_tokens": %d
      }
    },
    {
      "starting_at": "%sT00:00:00Z",
      "ending_at": "%sT00:00:00Z",
      "group": {"api_key_id": %q, "model": %q, "service_tier": "standard", "inference_geo": "global", "speed": "standard"},
      "usage": {
        "uncached_input_tokens": 7,
        "cache_creation": {"ephemeral_1h_input_tokens": 0, "ephemeral_5m_input_tokens": 0},
        "cache_read_input_tokens": 0,
        "output_tokens": 7
      }
    }
  ],
  "has_more": false,
  "next_page": null,
  "data_refreshed_at": "%sT08:00:00Z"
}
`, syntheticNote, d.date, d.next(), credential, model, d.outputReported, d.date, d.next(), otherKey, model, d.next()))
}

func (d day) costReport() []byte {
	return []byte(fmt.Sprintf(`{
  "x_synthetic": %q,
  "data": [
    {
      "starting_at": "%sT00:00:00Z",
      "ending_at": "%sT00:00:00Z",
      "group": {"api_key_id": %q, "cost_type": "tokens"},
      "cost": {"currency": "USD", "amount": %q, "list_amount": %q}
    },
    {
      "starting_at": "%sT00:00:00Z",
      "ending_at": "%sT00:00:00Z",
      "group": {"api_key_id": %q, "cost_type": "tokens"},
      "cost": {"currency": "USD", "amount": "0.008400", "list_amount": "0.008400"}
    }
  ],
  "has_more": false,
  "next_page": null,
  "data_refreshed_at": "%sT08:00:00Z"
}
`, syntheticNote, d.date, d.next(), credential, d.amountCents, d.listCents, d.date, d.next(), otherKey, d.next()))
}

// savedAt is two days after the day, when the workflow saves its reports.
func (d day) savedAt() time.Time {
	at, err := time.Parse(dayLayout, d.date)
	if err != nil {
		panic(err)
	}
	return at.AddDate(0, 0, 2).Add(6*time.Hour + 31*time.Minute)
}

// provider answers the fetchers with the day's synthetic reports.
func (d day) provider(t *testing.T) doer {
	return doerFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("x-api-key") != adminKey {
			t.Errorf("the fetcher sent a key other than the admin key")
		}
		var body []byte
		switch request.URL.Path {
		case "/v1/organizations/analytics/usage":
			body = d.usageReport()
		case "/v1/organizations/analytics/cost":
			body = d.costReport()
		default:
			return nil, fmt.Errorf("no synthetic report for %s", request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
}

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(request *http.Request) (*http.Response, error) { return f(request) }

type result struct {
	code           int
	stdout, stderr string
}

func runAccuracy(t *testing.T, outside world, args ...string) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, outside, &stdout, &stderr)
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// keep saves one day through -save, as the workflow does.
func keep(t *testing.T, inputs string, d day) result {
	t.Helper()
	rows := filepath.Join(t.TempDir(), "rows.jsonl")
	if err := os.WriteFile(rows, d.rows(t), 0o644); err != nil {
		t.Fatal(err)
	}
	key := adminKey
	if d.noReport {
		key = ""
	}
	outside := world{
		getenv: func(name string) string {
			if name == "ANTHROPIC_ADMIN_KEY" {
				return key
			}
			return ""
		},
		client: d.provider(t),
		now:    d.savedAt,
	}
	got := runAccuracy(t, outside, "-save", filepath.Join(inputs, d.date), "-day", d.date, "-rows", rows)
	if got.code != 0 {
		t.Fatalf("-save exited %d: %s", got.code, got.stderr)
	}
	return got
}

func keepAll(t *testing.T, days ...day) string {
	t.Helper()
	inputs := t.TempDir()
	for _, d := range days {
		keep(t, inputs, d)
	}
	return inputs
}

func offline(t *testing.T) world {
	return world{
		getenv: func(string) string { return "" },
		client: doerFunc(func(request *http.Request) (*http.Response, error) {
			t.Errorf("a recomputation reached the network: %s", request.URL)
			return nil, fmt.Errorf("offline")
		}),
		now: time.Now,
	}
}

func writeAndCheck(t *testing.T, inputs string, days int) (string, result) {
	t.Helper()
	page := filepath.Join(t.TempDir(), "accuracy.md")
	if got := runAccuracy(t, offline(t), "-write", page, "-inputs", inputs, "-costroid", costroid); got.code != 0 {
		t.Fatalf("-write exited %d: %s", got.code, got.stderr)
	}
	return page, runAccuracy(t, offline(t), "-check", page, "-days", fmt.Sprint(days), "-inputs", inputs, "-costroid", costroid)
}

func dayLine(t *testing.T, page, date string) string {
	t.Helper()
	data, err := os.ReadFile(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(text, "| "+date+" |") {
			return text
		}
	}
	t.Fatalf("%s holds no line for %s:\n%s", page, date, data)
	return ""
}

func verdictOf(t *testing.T, text string) string {
	t.Helper()
	cells := strings.Split(text, " | ")
	if len(cells) != 11 {
		t.Fatalf("a day line has %d cells: %s", len(cells), text)
	}
	return cells[9]
}

func consecutiveDays(n int) []day {
	start, err := time.Parse(dayLayout, firstDay)
	if err != nil {
		panic(err)
	}
	days := make([]day, n)
	for i := range days {
		days[i] = passingDay(start.AddDate(0, 0, i).Format(dayLayout))
	}
	return days
}

func TestPassingDayPasses(t *testing.T) {
	inputs := keepAll(t, passingDay(firstDay))
	page, got := writeAndCheck(t, inputs, 1)
	if got.code != 0 {
		t.Fatalf("-check exited %d: %s", got.code, got.stderr)
	}
	text := dayLine(t, page, firstDay)
	if verdictOf(t, text) != "pass" {
		t.Errorf("verdict %q, want pass: %s", verdictOf(t, text), text)
	}
	for _, want := range []string{"output 50000 / 50000 (0.00%)", "0.81 / 0.81 (0.00%)", "claude-code 2.1.316", "| 0.1.0 | 0.81 USD |"} {
		if !strings.Contains(text, want) {
			t.Errorf("the line does not state %q: %s", want, text)
		}
	}
}

func TestTokenMissAboveOnePercentFailsTheDay(t *testing.T) {
	miss := passingDay(firstDay)
	miss.outputReported = 50600
	miss.amountCents, miss.listCents = "81.600000", "81.600000"
	page, got := writeAndCheck(t, keepAll(t, miss), 1)
	if got.code != exitRefused {
		t.Fatalf("-check exited %d on a token miss, want %d", got.code, exitRefused)
	}
	text := dayLine(t, page, firstDay)
	if verdictOf(t, text) != failTokens {
		t.Errorf("verdict %q, want %q: %s", verdictOf(t, text), failTokens, text)
	}
	if !strings.Contains(text, "output 50000 / 50600 (1.19%)") {
		t.Errorf("the line does not state the output miss: %s", text)
	}
	if !strings.Contains(got.stderr, firstDay+" did not pass") {
		t.Errorf("-check did not name the day: %s", got.stderr)
	}
}

func TestTokenGapAtOnePercentPasses(t *testing.T) {
	edge := passingDay(firstDay)
	edge.outputReported = 50505
	edge.amountCents, edge.listCents = "81.505000", "81.505000"
	page, got := writeAndCheck(t, keepAll(t, edge), 1)
	if got.code != 0 {
		t.Fatalf("-check exited %d at a gap of 505 in 50505 tokens: %s", got.code, got.stderr)
	}
	if text := dayLine(t, page, firstDay); verdictOf(t, text) != "pass" {
		t.Errorf("a gap of exactly 1%% did not pass: %s", text)
	}
}

func TestDollarMissWithANamedCausePasses(t *testing.T) {
	discounted := passingDay(firstDay)
	discounted.amountCents = "64.800000"
	page, got := writeAndCheck(t, keepAll(t, discounted), 1)
	if got.code != 0 {
		t.Fatalf("-check exited %d on a residual the contract rate explains: %s", got.code, got.stderr)
	}
	text := dayLine(t, page, firstDay)
	if verdictOf(t, text) != "pass after named causes" {
		t.Errorf("verdict %q: %s", verdictOf(t, text), text)
	}
	for _, want := range []string{"0.81 / 0.648 (25.00%)", "| -0.162 |", "| contract_rate -0.162 |"} {
		if !strings.Contains(text, want) {
			t.Errorf("the line does not state %q: %s", want, text)
		}
	}
}

func TestDollarMissWithNoCauseFailsTheDay(t *testing.T) {
	unexplained := passingDay(firstDay)
	unexplained.amountCents, unexplained.listCents = "64.800000", "64.800000"
	page, got := writeAndCheck(t, keepAll(t, unexplained), 1)
	if got.code != exitRefused {
		t.Fatalf("-check exited %d on an unexplained dollar miss, want %d", got.code, exitRefused)
	}
	if text := dayLine(t, page, firstDay); verdictOf(t, text) != failDollars {
		t.Errorf("verdict %q, want %q: %s", verdictOf(t, text), failDollars, text)
	}
}

func TestTamperedLineIsRefusedBecauseTheCheckRecomputes(t *testing.T) {
	days := consecutiveDays(14)
	days[6].outputReported = 52000
	days[6].amountCents, days[6].listCents = "83.000000", "83.000000"
	inputs := keepAll(t, days...)
	page, got := writeAndCheck(t, inputs, 14)
	if got.code != exitRefused {
		t.Fatalf("-check exited %d with a token miss in the run, want %d", got.code, exitRefused)
	}
	honest := dayLine(t, page, days[6].date)
	for name, forged := range map[string]string{
		"verdict": strings.Replace(honest, "| "+failTokens+" |", "| pass |", 1),
		"figure":  strings.Replace(honest, "output 50000 / 52000 (3.85%)", "output 50000 / 50000 (0.00%)", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if forged == honest {
				t.Fatalf("the forgery changed nothing: %s", honest)
			}
			data, err := os.ReadFile(page)
			if err != nil {
				t.Fatal(err)
			}
			tampered := filepath.Join(t.TempDir(), "accuracy.md")
			if err := os.WriteFile(tampered, []byte(strings.Replace(string(data), honest, forged, 1)), 0o644); err != nil {
				t.Fatal(err)
			}
			got := runAccuracy(t, offline(t), "-check", tampered, "-days", "14", "-inputs", inputs, "-costroid", costroid)
			if got.code != exitRefused {
				t.Fatalf("-check exited %d on a tampered line, want %d", got.code, exitRefused)
			}
			if !strings.Contains(got.stderr, "does not match its inputs") {
				t.Errorf("-check did not refuse the line: %s", got.stderr)
			}
		})
	}
}

func TestChangedInputsAreRefused(t *testing.T) {
	inputs := keepAll(t, passingDay(firstDay))
	page, got := writeAndCheck(t, inputs, 1)
	if got.code != 0 {
		t.Fatalf("-check exited %d: %s", got.code, got.stderr)
	}
	day := filepath.Join(inputs, firstDay, dayFile)
	data, err := os.ReadFile(day)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(day, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	got = runAccuracy(t, offline(t), "-check", page, "-days", "1", "-inputs", inputs, "-costroid", costroid)
	if got.code != exitRefused || !strings.Contains(got.stderr, "does not match its inputs") {
		t.Errorf("-check exited %d after an input changed by one byte: %s", got.code, got.stderr)
	}
}

func TestFourteenConsecutivePassingDaysPass(t *testing.T) {
	_, got := writeAndCheck(t, keepAll(t, consecutiveDays(14)...), 14)
	if got.code != 0 {
		t.Fatalf("-check exited %d: %s", got.code, got.stderr)
	}
	if !strings.Contains(got.stdout, "14 consecutive UTC days passed, 2026-11-10 to 2026-11-23") {
		t.Errorf("-check did not state the run: %s", got.stdout)
	}
}

func TestThirteenDaysAreNotFourteen(t *testing.T) {
	_, got := writeAndCheck(t, keepAll(t, consecutiveDays(13)...), 14)
	if got.code != exitRefused || !strings.Contains(got.stderr, "holds 13 days") {
		t.Errorf("-check exited %d on 13 days: %s", got.code, got.stderr)
	}
}

func TestAMissingDayBreaksTheRun(t *testing.T) {
	days := consecutiveDays(15)
	gapped := append(days[:7:7], days[8:]...)
	_, got := writeAndCheck(t, keepAll(t, gapped...), 14)
	if got.code != exitRefused || !strings.Contains(got.stderr, "not consecutive") {
		t.Errorf("-check exited %d over a missing day: %s", got.code, got.stderr)
	}
}

func TestDayWithNoProviderReportFails(t *testing.T) {
	unreported := passingDay(firstDay)
	unreported.noReport = true
	inputs := t.TempDir()
	saved := keep(t, inputs, unreported)
	if !strings.Contains(saved.stdout, "no provider report for 2026-11-10: ANTHROPIC_ADMIN_KEY is not set") {
		t.Errorf("-save did not say why: %s", saved.stdout)
	}
	page, got := writeAndCheck(t, inputs, 1)
	if got.code != exitRefused {
		t.Fatalf("-check exited %d on a day with no report, want %d", got.code, exitRefused)
	}
	text := dayLine(t, page, firstDay)
	if verdictOf(t, text) != failNoReport {
		t.Errorf("verdict %q, want %q: %s", verdictOf(t, text), failNoReport, text)
	}
	if !strings.Contains(text, "| 0.81 USD |") {
		t.Errorf("a day with no report hides its measured cost: %s", text)
	}
}

func TestSaveKeepsNeitherTheKeyNorAnotherKeysRecords(t *testing.T) {
	inputs := t.TempDir()
	saved := keep(t, inputs, passingDay(firstDay))
	if strings.Contains(saved.stdout+saved.stderr, adminKey) {
		t.Error("-save printed the key")
	}
	err := filepath.WalkDir(inputs, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(adminKey)) {
			t.Errorf("%s holds the key", path)
		}
		if bytes.Contains(data, []byte(otherKey)) {
			t.Errorf("%s keeps another key's records", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{dayFile, rowsFile, "reports/anthropic-usage.json", "reports/anthropic-cost.json"} {
		if _, err := os.Stat(filepath.Join(inputs, firstDay, name)); err != nil {
			t.Errorf("the day does not keep %s: %v", name, err)
		}
	}
}

func TestPageListsTheErrorPerAgentVersion(t *testing.T) {
	older := passingDay("2026-11-10")
	older.agentVersion = "2.1.300"
	older.outputReported = 50250
	older.amountCents, older.listCents = "81.250000", "81.250000"
	newer := passingDay("2026-11-11")
	newer.amountCents = "64.800000"
	page, got := writeAndCheck(t, keepAll(t, older, newer), 2)
	if got.code != 0 {
		t.Fatalf("-check exited %d: %s", got.code, got.stderr)
	}
	data, err := os.ReadFile(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"| claude-code 2.1.300 | 1 | 1 | 0.50% | 0.31% | 0.31% |",
		"| claude-code 2.1.316 | 1 | 1 | 0.00% | 25.00% | 0.00% |",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the page does not list %q:\n%s", want, data)
		}
	}
}

func TestCheckWithoutInputsCannotPass(t *testing.T) {
	page := filepath.Join(t.TempDir(), "accuracy.md")
	if err := os.WriteFile(page, render(nil), 0o644); err != nil {
		t.Fatal(err)
	}
	got := runAccuracy(t, offline(t), "-check", page, "-inputs", filepath.Join(t.TempDir(), "absent"), "-costroid", costroid)
	if got.code != exitFailure {
		t.Errorf("-check exited %d with no inputs, want %d: %s", got.code, exitFailure, got.stderr)
	}
}

func TestStrayEntryInTheInputsIsRefused(t *testing.T) {
	inputs := keepAll(t, passingDay(firstDay))
	if err := os.WriteFile(filepath.Join(inputs, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := runAccuracy(t, offline(t), "-write", filepath.Join(t.TempDir(), "accuracy.md"), "-inputs", inputs, "-costroid", costroid)
	if got.code != exitFailure || !strings.Contains(got.stderr, "is not a kept day") {
		t.Errorf("-write exited %d over a stray file: %s", got.code, got.stderr)
	}
}

func TestCommittedPageIsWrittenByThisCommand(t *testing.T) {
	data, err := os.ReadFile("../../docs/accuracy.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{header, "\n## By agent version\n\n", "\n## Days\n\n"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("docs/accuracy.md was edited by hand: it does not hold %q", want)
		}
	}
}
