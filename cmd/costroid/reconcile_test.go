package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Costroid/costroid/internal/reconcile"
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
	if code != exitRejected {
		t.Fatalf("exit %d, want %d\n%s", code, exitRejected, stdout)
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
	if code != exitRejected {
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
	if code != exitUsage || !strings.Contains(stderr, "before") {
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
	if code != exitRejected {
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
		if code, _, _ := runReconcile(t, &replayDoer{t: t}, environment, append(base, extra...)...); code != exitUsage {
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

// reconcilePlatform answers as Costroid Platform does for reconcile --export,
// and as GitHub's OIDC token endpoint, and keeps every request it was sent.
type reconcilePlatform struct {
	server *httptest.Server
	// states holds, by version path, the contract GET /identity states there.
	states map[string]contract
	// quotas is the answer to GET /quotas.
	quotas string
	// answers answer each PUT /reconciliations in turn. Once they run out,
	// every row is stored.
	answers []turn

	mu   sync.Mutex
	seen []seenRequest
	puts [][]byte
}

// seenRequest is what one request carried: its path, its headers and its body.
type seenRequest struct {
	method, path string
	header       http.Header
	body         []byte
}

const reconcileQuotas = `{"as_of":"2026-11-21T09:40:03Z","quotas":[{"name":"reconciliations_per_day","limit":48,"used":3,"state":"ok","window_resets_at":"2026-11-22T00:00:00Z"},{"name":"max_reconciliation_lines","limit":500,"used":0,"state":"ok","window_resets_at":null},{"name":"max_reconciliation_bytes","limit":1048576,"used":0,"state":"ok","window_resets_at":null},{"name":"max_reconciliation_line_bytes","limit":16384,"used":0,"state":"ok","window_resets_at":null}]}`

func newReconcilePlatform(t *testing.T) *reconcilePlatform {
	t.Helper()
	stub := &reconcilePlatform{states: map[string]contract{contractV05.path(): contractV05}, quotas: reconcileQuotas}
	for _, version := range exportContracts {
		stub.states[version.path()] = version
	}
	stub.server = httptest.NewTLSServer(http.HandlerFunc(stub.serve))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *reconcilePlatform) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	s.seen = append(s.seen, seenRequest{r.Method, r.URL.Path, r.Header.Clone(), body})
	if r.URL.Path == "/oidc" {
		w.Write([]byte(`{"value":"` + testRunToken + `"}`))
		return
	}
	for prefix, stated := range s.states {
		rest, found := strings.CutPrefix(r.URL.Path, prefix+"/")
		if !found {
			continue
		}
		switch {
		case r.Method == http.MethodGet && rest == "identity":
			contract, _ := json.Marshal(stated)
			w.Write([]byte(`{"contract":` + string(contract) + `,"tenant":{"id":"` + testTenant + `","name":"Example Inc","region":"eu"}}`))
		case r.Method == http.MethodGet && rest == "quotas" && carriesRows(prefix):
			w.Write([]byte(s.quotas))
		case r.Method == http.MethodPut && rest == "reconciliations" && carriesRows(prefix):
			s.puts = append(s.puts, body)
			answer := turn{http.StatusOK, fmt.Sprintf(`{"stored":%d,"unchanged":0,"ignored":0}`, bytes.Count(body, []byte("\n")))}
			if len(s.puts) <= len(s.answers) {
				answer = s.answers[len(s.puts)-1]
			}
			w.WriteHeader(answer.status)
			w.Write([]byte(answer.body))
		default:
			http.Error(w, "", http.StatusNotFound)
		}
		return
	}
	http.Error(w, "", http.StatusNotFound)
}

// carriesRows reports a version path that serves reconciliation rows.
func carriesRows(prefix string) bool {
	return prefix == contractV05.path() || prefix == contractV06.path()
}

func (s *reconcilePlatform) sent() ([][]byte, []seenRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.puts), slices.Clone(s.seen)
}

// env is a scheduled job's environment that names the stub: the admin key,
// the platform and the integration token, with extra set over it.
func (s *reconcilePlatform) env(extra map[string]string) func(string) string {
	values := map[string]string{anthropicKeyVariable: sentinelKey, platformVariable: s.server.URL, platformTokenVariable: testIntegrationToken}
	maps.Copy(values, extra)
	return func(name string) string { return values[name] }
}

// githubJob is a GitHub Actions job with id-token: write.
func (s *reconcilePlatform) githubJob() map[string]string {
	return map[string]string{
		"GITHUB_ACTIONS":                 "true",
		"ACTIONS_ID_TOKEN_REQUEST_URL":   s.server.URL + "/oidc?api-version=2.0",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN": testRequestToken,
	}
}

// reconcileAndSend runs reconcile --export against the stub with the sample
// ledger and report, and returns the exit code, the output and the --out file.
func (s *reconcilePlatform) reconcileAndSend(t *testing.T, doer reconcile.Doer, getenv func(string) string, extra ...string) (int, string, string, string) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "reconciliation.jsonl")
	args := append([]string{"--rows", writeLedger(t, 100900), "--from", "2026-10-14", "--to", "2026-10-14", "--out", out, "--export"}, extra...)
	var stdout, stderr bytes.Buffer
	world := reconcileWorld{getenv: getenv, client: doer, now: fixedNow, platform: s.server.Client(), pace: testPace}
	code := reconcileThrough(context.Background(), args, world, &stdout, &stderr)
	for _, secret := range []string{testIntegrationToken, testRunToken, testRequestToken, sentinelKey} {
		if strings.Contains(stdout.String()+stderr.String(), secret) {
			t.Fatalf("the output holds a token: %s%s", stdout.String(), stderr.String())
		}
	}
	stored, err := os.ReadFile(out)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return code, stdout.String(), stderr.String(), string(stored)
}

const reconcileVersionLine = "costroid: Costroid Platform states integration/v0.5, so reconcile sends the rows by it\n"

// reconciliationRowSchema compiles integration/v0.5's ReconciliationRow.
func reconciliationRowSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	return contractSchema(t, contractV05, "ReconciliationRow")
}

// reconcile --export sends what --out writes, byte for byte, as the body of
// PUT /reconciliations under integration/v0.5, and prints what the platform
// did with it. Each line is a ReconciliationRow. A GitHub job with id-token:
// write sends its run token beside the installation; any other job sends
// neither unless --installation-id names one.
func TestReconcileExportSendsTheRowsOutWrites(t *testing.T) {
	schema := reconciliationRowSchema(t)
	stub := newReconcilePlatform(t)
	doer := &replayDoer{t: t, usage: "usage.json", cost: "cost-at-list.json"}
	code, stdout, stderr, stored := stub.reconcileAndSend(t, doer, stub.env(stub.githubJob()), "--installation-id", testInstallation)
	if code != 0 || stderr != reconcileVersionLine {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	puts, seen := stub.sent()
	if len(puts) != 1 || string(puts[0]) != stored || stored == "" {
		t.Fatalf("%d bodies sent, want --out's rows once:\n%q\n%q", len(puts), puts, stored)
	}
	for _, line := range strings.Split(strings.TrimSuffix(stored, "\n"), "\n") {
		instance, err := jsonschema.UnmarshalJSON(strings.NewReader(line))
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(instance); err != nil {
			t.Errorf("a line is no ReconciliationRow: %v\n%s", err, line)
		}
	}
	put := seen[slices.IndexFunc(seen, func(r seenRequest) bool { return r.method == http.MethodPut })]
	for name, value := range map[string]string{
		"Authorization":            "Bearer " + testIntegrationToken,
		"Content-Type":             "application/x-ndjson",
		"Costroid-Installation-Id": testInstallation,
		"Costroid-Run-Token":       testRunToken,
	} {
		if got := put.header.Get(name); got != value {
			t.Errorf("%s: %q, want %q", name, got, value)
		}
	}
	if put.path != "/api/integration/v0.5/reconciliations" {
		t.Errorf("the body went to %s", put.path)
	}
	oidc := seen[slices.IndexFunc(seen, func(r seenRequest) bool { return r.path == "/oidc" })]
	if !strings.Contains(oidc.header.Get("Authorization"), testRequestToken) {
		t.Errorf("the run token was asked for without the request token: %v", oidc.header)
	}
	if want := "costroid: Costroid Platform took 1 row in 1 body: 1 stored, 0 unchanged, 0 ignored\n"; !strings.HasSuffix(stdout, want) {
		t.Errorf("stdout ends %q, want %q", stdout[max(0, len(stdout)-200):], want)
	}

	scheduled := newReconcilePlatform(t)
	code, _, stderr, _ = scheduled.reconcileAndSend(t, &replayDoer{t: t, usage: "usage.json", cost: "cost-at-list.json"}, scheduled.env(nil))
	puts, seen = scheduled.sent()
	if code != 0 || len(puts) != 1 {
		t.Fatalf("outside GitHub Actions: exit %d, %d bodies: %s", code, len(puts), stderr)
	}
	for _, request := range seen {
		if request.header.Get("Costroid-Run-Token") != "" || request.header.Get("Costroid-Installation-Id") != "" || request.path == "/oidc" {
			t.Errorf("outside GitHub Actions %s %s went with a run token or an installation", request.method, request.path)
		}
	}
}

// Bodies are cut at the limits GET /quotas states. A row past
// max_reconciliation_line_bytes fits no body, and a limit the answer does not
// state is never guessed: either way nothing is sent.
func TestReconcileExportCutsAtTheStatedLimits(t *testing.T) {
	for name, check := range map[string]struct {
		quotas, want string
	}{
		"a line past the limit": {strings.Replace(reconcileQuotas, `"limit":16384`, `"limit":100`, 1), "costroid: the row of anthropic apikey_01ExampleCi 2026-10-14 is "},
		"a limit not stated":    {strings.Replace(reconcileQuotas, `"max_reconciliation_bytes"`, `"max_batch_bytes"`, 1), "costroid: GET /quotas states no max_reconciliation_bytes, so no body can be cut to it, and nothing was sent\n"},
	} {
		t.Run(name, func(t *testing.T) {
			stub := newReconcilePlatform(t)
			stub.quotas = check.quotas
			code, _, stderr, _ := stub.reconcileAndSend(t, &replayDoer{t: t, usage: "usage.json", cost: "cost-at-list.json"}, stub.env(nil))
			if puts, _ := stub.sent(); code != exitFailure || len(puts) != 0 || !strings.Contains(stderr, check.want) {
				t.Errorf("exit %d, %d bodies, stderr %q", code, len(puts), stderr)
			}
		})
	}
}

// A 5xx sends the same body again, as export asks again for a verdict, until
// the platform takes it. One that never does fails the send.
func TestReconcileExportSendsAgainOnAServerError(t *testing.T) {
	stub := newReconcilePlatform(t)
	stub.answers = []turn{{http.StatusServiceUnavailable, ``}, {http.StatusBadGateway, `{"error":{"code":"UPSTREAM","message":"x"}}`}}
	code, stdout, stderr, stored := stub.reconcileAndSend(t, &replayDoer{t: t, usage: "usage.json", cost: "cost-at-list.json"}, stub.env(nil))
	puts, _ := stub.sent()
	if code != 0 || len(puts) != 3 {
		t.Fatalf("exit %d, %d sends: %s", code, len(puts), stderr)
	}
	for i, body := range puts {
		if string(body) != stored {
			t.Errorf("send %d differs from --out's rows:\n%s", i+1, body)
		}
	}
	if !strings.Contains(stdout, "costroid: Costroid Platform took 1 row in 1 body: 1 stored, 0 unchanged, 0 ignored\n") {
		t.Errorf("stdout %q", stdout)
	}

	down := newReconcilePlatform(t)
	down.answers = slices.Repeat([]turn{{http.StatusServiceUnavailable, ``}}, 100000)
	world := reconcileWorld{getenv: down.env(nil), client: &replayDoer{t: t, usage: "usage.json", cost: "cost-at-list.json"}, now: fixedNow, platform: down.server.Client(), pace: verdictPace{interval: time.Millisecond, limit: 50 * time.Millisecond}}
	out := filepath.Join(t.TempDir(), "reconciliation.jsonl")
	var stdoutDown, stderrDown bytes.Buffer
	code = reconcileThrough(context.Background(), []string{"--rows", writeLedger(t, 100900), "--from", "2026-10-14", "--to", "2026-10-14", "--out", out, "--export"}, world, &stdoutDown, &stderrDown)
	want := "costroid: Costroid Platform refused body 1 of 1: 503 with no refusal code, sent again every 0 s for 0 s. Nothing in the rows is wrong, and " + out + " is kept to send again\n"
	if puts, _ := down.sent(); code != exitFailure || len(puts) < 2 || !strings.HasSuffix(stderrDown.String(), want) {
		t.Errorf("exit %d, %d sends, stderr %q", code, len(puts), stderrDown.String())
	}
}

// The provider's admin key goes to the provider and nowhere else: no header
// and no body of any request to the platform, or to GitHub's token endpoint,
// holds it.
func TestReconcileExportNeverSendsTheAdminKey(t *testing.T) {
	stub := newReconcilePlatform(t)
	doer := &keyedDoer{replayDoer: replayDoer{t: t, usage: "usage.json", cost: "cost-at-list.json"}}
	code, _, stderr, _ := stub.reconcileAndSend(t, doer, stub.env(stub.githubJob()), "--installation-id", testInstallation)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if doer.keyed == 0 {
		t.Fatal("no provider request carried the admin key, so this test proves nothing")
	}
	_, seen := stub.sent()
	if len(seen) < 4 {
		t.Fatalf("the platform saw %d requests, want identity, quotas, the run token and the body", len(seen))
	}
	for _, request := range seen {
		for name, values := range request.header {
			for _, value := range values {
				if strings.Contains(value, sentinelKey) {
					t.Errorf("%s %s: the header %s holds the admin key", request.method, request.path, name)
				}
			}
		}
		if bytes.Contains(request.body, []byte(sentinelKey)) || strings.Contains(request.path, sentinelKey) {
			t.Errorf("%s %s: the request holds the admin key", request.method, request.path)
		}
	}
}

// keyedDoer answers as replayDoer does and counts the requests that carried
// the admin key.
type keyedDoer struct {
	replayDoer
	keyed int
}

func (d *keyedDoer) Do(request *http.Request) (*http.Response, error) {
	if request.Header.Get("x-api-key") == sentinelKey {
		d.keyed++
	}
	return d.replayDoer.Do(request)
}

// A platform that states an earlier version than integration/v0.5 carries no
// reconciliation rows, so nothing is sent, and the line names the version it
// states. The command exits as export does for a version it cannot send by.
func TestReconcileExportRefusesAPlatformBeforeV05(t *testing.T) {
	for name, states := range map[string]map[string]contract{
		"a platform of v0.4":                      {contractV04.path(): contractV04, contractV03.path(): contractV03},
		"a platform whose v0.5 path states v0.4":  {contractV05.path(): contractV04, contractV04.path(): contractV04},
		"a platform of v0.3 with no v0.4 or v0.5": {contractV03.path(): contractV03},
	} {
		t.Run(name, func(t *testing.T) {
			stub := newReconcilePlatform(t)
			stub.states = states
			code, _, stderr, stored := stub.reconcileAndSend(t, &replayDoer{t: t, usage: "usage.json", cost: "cost-at-list.json"}, stub.env(nil))
			newest := "0.4"
			if _, found := states[contractV04.path()]; !found {
				newest = "0.3"
			}
			want := "costroid: the latest version Costroid Platform states is integration/v" + newest + ", and reconcile --export sends by integration/v0.5 or later, the first that carries reconciliation rows, so nothing was sent\n"
			if puts, _ := stub.sent(); code != exitFailure || len(puts) != 0 || stderr != want {
				t.Errorf("exit %d, %d bodies, stderr %q, want %q", code, len(puts), stderr, want)
			}
			if stored == "" {
				t.Error("--out kept no rows")
			}
		})
	}

	none := newReconcilePlatform(t)
	none.states = map[string]contract{}
	code, _, stderr, _ := none.reconcileAndSend(t, &replayDoer{t: t, usage: "usage.json", cost: "cost-at-list.json"}, none.env(nil))
	if want := "costroid: Costroid Platform refused the identity request: 404 with no refusal code\n"; code != exitFailure || stderr != want {
		t.Errorf("a platform that states none: exit %d, stderr %q, want %q", code, stderr, want)
	}
}

// Each refusal of PUT /reconciliations follows the contract: 402, 429 and 409
// INSTALLATION_NOT_BOUND keep the rows and warn, and change no exit code; 413,
// 422 and any other refusal fail the send, and 422 names the line and its row.
func TestReconcileExportFollowsTheContractsRefusals(t *testing.T) {
	for name, check := range map[string]struct {
		answer turn
		retry  string
		code   int
		want   string
	}{
		"402": {turn{http.StatusPaymentRequired, `{"error":{"code":"PLAN_WRITES_REFUSED","message":"::error::forged"}}`}, "", 0,
			"costroid: warning: Costroid Platform refused body 1 of 1: 402 PLAN_WRITES_REFUSED. Nothing in the rows is wrong, and OUT is kept to send again\n"},
		"429": {turn{http.StatusTooManyRequests, `{"error":{"code":"QUOTA_RECONCILIATIONS_PER_DAY","message":"x"}}`}, "3600", 0,
			"costroid: warning: Costroid Platform refused body 1 of 1: 429 QUOTA_RECONCILIATIONS_PER_DAY, retry after 3600 s. Nothing in the rows is wrong, and OUT is kept to send again\n"},
		"409": {turn{http.StatusConflict, `{"error":{"code":"INSTALLATION_NOT_BOUND","message":"x"}}`}, "", 0,
			"costroid: warning: Costroid Platform refused body 1 of 1: 409 INSTALLATION_NOT_BOUND. The tenant holds no binding of github-owner:7204411: run costroid connect, as docs/connect.md shows, and OUT is kept to send again\n"},
		"413": {turn{http.StatusRequestEntityTooLarge, `{"error":{"code":"RECONCILIATION_TOO_LARGE","message":"x"}}`}, "", exitFailure,
			"costroid: Costroid Platform refused body 1 of 1: 413 RECONCILIATION_TOO_LARGE. The body was cut at the limits GET /quotas stated, which can only rise, and nothing in it was stored. OUT is kept to send again\n"},
		"422": {turn{http.StatusUnprocessableEntity, `{"error":{"code":"RECONCILIATION_INVALID","message":"::error::forged","line":1}}`}, "", exitFailure,
			"costroid: Costroid Platform refused body 1 of 1: 422 RECONCILIATION_INVALID at line 1, the row of anthropic apikey_01ExampleCi 2026-10-14. Nothing in the body was stored, and OUT is kept\n"},
		"403": {turn{http.StatusForbidden, `{"error":{"code":"AUTH_TOKEN_SCOPE","message":"x"}}`}, "", exitFailure,
			"costroid: Costroid Platform refused body 1 of 1: 403 AUTH_TOKEN_SCOPE\n"},
	} {
		t.Run(name, func(t *testing.T) {
			stub := newReconcilePlatform(t)
			stub.answers = []turn{check.answer}
			retry := check.retry
			stub.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if retry != "" {
					w.Header().Set("Retry-After", retry)
				}
				stub.serve(w, r)
			})
			out := filepath.Join(t.TempDir(), "reconciliation.jsonl")
			world := reconcileWorld{getenv: stub.env(nil), client: &replayDoer{t: t, usage: "usage.json", cost: "cost-at-list.json"}, now: fixedNow, platform: stub.server.Client(), pace: testPace}
			var stdout, stderr bytes.Buffer
			args := []string{"--rows", writeLedger(t, 100900), "--from", "2026-10-14", "--to", "2026-10-14", "--out", out, "--export", "--installation-id", testInstallation}
			code := reconcileThrough(context.Background(), args, world, &stdout, &stderr)
			want := reconcileVersionLine + strings.ReplaceAll(check.want, "OUT", out)
			if code != check.code || stderr.String() != want {
				t.Errorf("exit %d, stderr %q\nwant exit %d, %q", code, stderr.String(), check.code, want)
			}
			if puts, _ := stub.sent(); len(puts) != 1 {
				t.Errorf("%d sends, want 1", len(puts))
			}
			if _, err := os.Stat(out); err != nil {
				t.Errorf("--out was not kept: %v", err)
			}
		})
	}
}

// What --export needs is read before any provider is asked, so a send that
// cannot happen costs no report and leaves no partial work.
func TestReconcileExportRefusesBeforeAskingTheProvider(t *testing.T) {
	stub := newReconcilePlatform(t)
	rows := writeLedger(t, 100900)
	out := filepath.Join(t.TempDir(), "reconciliation.jsonl")
	base := []string{"--rows", rows, "--from", "2026-10-14", "--to", "2026-10-14"}
	for name, check := range map[string]struct {
		args []string
		env  map[string]string
		want string
	}{
		"no --out":                         {[]string{"--export"}, nil, "--export sends the rows --out keeps, so it needs --out"},
		"an installation with no export":   {[]string{"--out", out, "--installation-id", testInstallation}, nil, "--installation-id names what --export sends beside the rows, so it goes with --export"},
		"an installation name":             {[]string{"--out", out, "--export", "--installation-id", "github-owner:example"}, nil, "--installation-id must be"},
		"no platform":                      {[]string{"--out", out, "--export"}, map[string]string{platformVariable: ""}, "--export sends to the Costroid Platform COSTROID_PLATFORM_URL names, and it is not set"},
		"plain http":                       {[]string{"--out", out, "--export"}, map[string]string{platformVariable: "http://app.costroid.com"}, "COSTROID_PLATFORM_URL must be an https origin"},
		"a provider key as the token":      {[]string{"--out", out, "--export"}, map[string]string{platformTokenVariable: "sk-ant-not-an-integration-token"}, "is not an integration token"},
		"a run token with no installation": {[]string{"--out", out, "--export"}, stub.githubJob(), "pass --installation-id"},
	} {
		t.Run(name, func(t *testing.T) {
			doer := &replayDoer{t: t, usage: "usage.json", cost: "cost-at-list.json"}
			var stdout, stderr bytes.Buffer
			world := reconcileWorld{getenv: stub.env(check.env), client: doer, now: fixedNow, platform: stub.server.Client(), pace: testPace}
			code := reconcileThrough(context.Background(), append(slices.Clone(base), check.args...), world, &stdout, &stderr)
			// The first three are the command line's. The rest are the
			// environment's, which the flags do not name.
			want := exitFailure
			if check.env == nil {
				want = exitUsage
			}
			if code != want || !strings.Contains(stderr.String(), check.want) || strings.Contains(stderr.String(), "sk-ant") {
				t.Errorf("exit %d, stderr %q, want %q", code, stderr.String(), check.want)
			}
			if doer.requested != 0 {
				t.Errorf("%d provider requests went out", doer.requested)
			}
		})
	}
	if _, seen := stub.sent(); len(seen) != 0 {
		t.Errorf("the platform saw %d requests", len(seen))
	}
}

// A platform that states integration/v0.6 beside every earlier version gets
// the rows by v0.6: the identity, the limits and the body are asked for under
// v0.6's path alone, the line names v0.6, and each line of the body is a
// ReconciliationRow of v0.6.
func TestReconcileExportSendsByV06ToAPlatformThatStatesIt(t *testing.T) {
	schema := contractSchema(t, contractV06, "ReconciliationRow")
	stub := newReconcilePlatform(t)
	stub.states[contractV06.path()] = contractV06
	code, _, stderr, stored := stub.reconcileAndSend(t, &replayDoer{t: t, usage: "usage.json", cost: "cost-at-list.json"}, stub.env(stub.githubJob()), "--installation-id", testInstallation)
	if want := "costroid: Costroid Platform states integration/v0.6, so reconcile sends the rows by it\n"; code != 0 || stderr != want {
		t.Fatalf("exit %d, stderr %q, want %q", code, stderr, want)
	}
	puts, seen := stub.sent()
	if len(puts) != 1 || string(puts[0]) != stored || stored == "" {
		t.Fatalf("%d bodies sent, want --out's rows once:\n%q\n%q", len(puts), puts, stored)
	}
	var asked []string
	for _, request := range seen {
		if request.path != "/oidc" {
			asked = append(asked, request.method+" "+request.path)
		}
	}
	want := []string{"GET /api/integration/v0.6/identity", "GET /api/integration/v0.6/quotas", "PUT /api/integration/v0.6/reconciliations"}
	if !slices.Equal(asked, want) {
		t.Errorf("the platform was asked %q, want %q", asked, want)
	}
	for _, line := range strings.Split(strings.TrimSuffix(stored, "\n"), "\n") {
		instance, err := jsonschema.UnmarshalJSON(strings.NewReader(line))
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(instance); err != nil {
			t.Errorf("a line is no ReconciliationRow of v0.6: %v\n%s", err, line)
		}
	}
}
