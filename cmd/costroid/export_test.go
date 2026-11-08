package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testIntegrationToken = "costroid_it_" + "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_AbCdE"
	testRunToken         = "eyJhbGciOiJSUzI1NiJ9.eyJydW5faWQiOiIxNjg3MzQwOTkyMSJ9.c2lnbmF0dXJl"
	testRequestToken     = "request-token"
	testTenant           = "org_01JEXAMPLE0001"
	testInstallation     = "github-owner:7204411"
	// testRun and testRepository are the run and repository the example
	// rows name.
	testRun        = "16873409921"
	testRepository = "example/widgets"
)

// testPace asks for a verdict at once and gives up after 5 s, so no test
// waits as a CI job would.
var testPace = verdictPace{interval: time.Millisecond, limit: 5 * time.Second}

// exampleRows and exampleCSV are focus-row/v0.3's examples, which the focus
// package's tests hold to the schemas' copy.
var (
	exampleRows = filepath.Join("..", "..", "internal", "focus", "testdata", "request-rows.jsonl")
	exampleCSV  = filepath.Join("..", "..", "internal", "focus", "testdata", "request-rows.csv")
)

// platformStub answers as Costroid Platform and as GitHub's OIDC token
// endpoint, and keeps what each was sent.
type platformStub struct {
	server *httptest.Server
	// status, body and header are the export's answer.
	status int
	body   string
	header http.Header
	// verdicts answer GET /exports/{batch_id} in turn, each a status and a
	// body. Once they run out, the batch has ingested.
	verdicts []turn

	mu       sync.Mutex
	exports  []*http.Request
	bodies   [][]byte
	audience []string
	identity []string
	asked    int
}

type turn struct {
	status int
	body   string
}

// ingested is the contract's example of a batch that ingested, with its rows
// and dropped entries counted.
func ingested(rows, dropped int) turn {
	return turn{http.StatusOK, `{"batch_id":"903114-16873409921-1","installation_id":"github-owner:7204411","status":"ingested","sha256":"3f7a9c10d2e84b6655a1c0f97e2d4b18c6a35f0e9d7b2c41a8e6f3051d9c7b24","focus_version":"1.4","received_at":"2026-10-14T09:40:03Z","rows":` + itoa(rows) + `,"dropped_tag_keys":` + itoa(dropped) + `,"failure":null}`}
}

func pending(status string) turn {
	return turn{http.StatusOK, `{"batch_id":"903114-16873409921-1","status":"` + status + `","rows":null,"dropped_tag_keys":null,"failure":null}`}
}

func failed(code string) turn {
	return turn{http.StatusOK, `{"batch_id":"903114-16873409921-1","status":"failed","rows":null,"dropped_tag_keys":null,"failure":{"code":` + quote(code) + `,"message":"::error::forged"}}`}
}

func itoa(n int) string {
	data, _ := json.Marshal(n)
	return string(data)
}

func quote(text string) string {
	data, _ := json.Marshal(text)
	return string(data)
}

func newPlatformStub(t *testing.T, status int, body string) *platformStub {
	t.Helper()
	stub := &platformStub{status: status, body: body, header: http.Header{}}
	stub.server = httptest.NewTLSServer(http.HandlerFunc(stub.serve))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *platformStub) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/oidc":
		if r.Header.Get("Authorization") != "Bearer "+testRequestToken {
			http.Error(w, "", http.StatusUnauthorized)
			return
		}
		s.audience = append(s.audience, r.URL.Query().Get("audience"))
		w.Write([]byte(`{"value":"` + testRunToken + `"}`))
	case r.Method == http.MethodGet && r.URL.Path == integrationPath+"/identity":
		s.identity = append(s.identity, r.Header.Get("Authorization"))
		w.Write([]byte(`{"tenant":{"id":"` + testTenant + `","name":"Example Inc","region":"eu"}}`))
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, integrationPath+"/exports/"):
		body := new(bytes.Buffer)
		body.ReadFrom(r.Body)
		s.exports = append(s.exports, r)
		s.bodies = append(s.bodies, body.Bytes())
		for name, values := range s.header {
			w.Header()[name] = values
		}
		w.WriteHeader(s.status)
		w.Write([]byte(s.body))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, integrationPath+"/exports/"):
		answer := ingested(11, 0)
		if s.asked < len(s.verdicts) {
			answer = s.verdicts[s.asked]
		}
		s.asked++
		w.WriteHeader(answer.status)
		w.Write([]byte(answer.body))
	default:
		http.Error(w, "", http.StatusNotFound)
	}
}

// githubEnv is a GitHub Actions job with id-token: write, run 16873409921's
// first attempt in repository 903114, example/widgets.
func (s *platformStub) githubEnv() map[string]string {
	return map[string]string{
		"GITHUB_ACTIONS":                 "true",
		"GITHUB_REPOSITORY":              testRepository,
		"GITHUB_REPOSITORY_ID":           "903114",
		"GITHUB_RUN_ID":                  testRun,
		"GITHUB_RUN_ATTEMPT":             "1",
		"ACTIONS_ID_TOKEN_REQUEST_URL":   s.server.URL + "/oidc?api-version=2.0",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN": testRequestToken,
		platformTokenVariable:            testIntegrationToken,
	}
}

func (s *platformStub) export(t *testing.T, env map[string]string, extra ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	args := append([]string{"--rows", exampleRows, "--platform-url", s.server.URL, "--installation-id", testInstallation}, extra...)
	code := export(context.Background(), args, s.server.Client(), func(name string) string { return env[name] }, testPace, &stdout, &stderr)
	for _, secret := range []string{testIntegrationToken, testRunToken, testRequestToken} {
		if strings.Contains(stdout.String()+stderr.String(), secret) {
			t.Fatalf("the output holds a token: %s%s", stdout.String(), stderr.String())
		}
	}
	return code, stdout.String(), stderr.String()
}

func TestExportSendsTheContractHeaders(t *testing.T) {
	stub := newPlatformStub(t, http.StatusAccepted, `{"batch_id":"903114-16873409921-1","status":"accepted"}`)
	code, stdout, stderr := stub.export(t, stub.githubEnv())
	if code != 0 || len(stub.exports) != 1 {
		t.Fatalf("exit %d, %d exports: %s", code, len(stub.exports), stderr)
	}
	want := exampleWithWindowedUnverified(t)
	sum := sha256.Sum256(want)
	request, body := stub.exports[0], stub.bodies[0]
	if !bytes.Equal(body, want) {
		t.Errorf("the body is not the rows as the schemas' CSV example, its two windowed rows unverified:\n%s", body)
	}
	for name, value := range map[string]string{
		"Authorization":            "Bearer " + testIntegrationToken,
		"Costroid-Installation-Id": testInstallation,
		"Costroid-Content-SHA256":  hex.EncodeToString(sum[:]),
		"Costroid-Focus-Version":   "1.4",
		"Costroid-Run-Token":       testRunToken,
		"Content-Type":             "text/csv",
	} {
		if got := request.Header.Get(name); got != value {
			t.Errorf("%s: %q, want %q", name, got, value)
		}
	}
	if len(stub.audience) != 1 || stub.audience[0] != "https://app.costroid.com/tenants/"+testTenant+"/rows" {
		t.Errorf("the run token's audience: %q", stub.audience)
	}
	if len(stub.identity) != 1 || stub.identity[0] != "Bearer "+testIntegrationToken {
		t.Errorf("the identity request: %q", stub.identity)
	}
	wantOut := "costroid: batch 903114-16873409921-1, 11 rows, sha256 " + hex.EncodeToString(sum[:]) + ": accepted for ingest\n" +
		"costroid: 9 of 11 rows name run 16873409921 of github.com/example/widgets, so they went as token_verified and the rest as unverified\n" +
		"costroid: batch 903114-16873409921-1 ingested, 11 rows\n" +
		"costroid: pull request 1423 of github.com/example/widgets: " + stub.server.URL + "/#/pulls?repo=github.com/example/widgets&pr=1423\n"
	if stdout != wantOut {
		t.Errorf("stdout %q, want %q", stdout, wantOut)
	}
}

// exampleWithWindowedUnverified is the schemas' CSV example as export sends
// it from the run the rows name: byte for byte, but for the Tags cells of the
// two windowed rows, which read unverified and unattributed in their place.
func exampleWithWindowedUnverified(t *testing.T) []byte {
	t.Helper()
	example, err := os.ReadFile(exampleCSV)
	if err != nil {
		t.Fatal(err)
	}
	const windowedRow = `""costroid/attribution_grade"":""windowed"",""costroid/enforcement_grade"":""provider"",""costroid/run_identity"":""token_verified""`
	if n := bytes.Count(example, []byte(windowedRow)); n != 2 {
		t.Fatalf("the example holds %d windowed rows, want 2", n)
	}
	return bytes.ReplaceAll(example, []byte(windowedRow), []byte(`""costroid/attribution_grade"":""unattributed"",""costroid/enforcement_grade"":""provider"",""costroid/run_identity"":""unverified""`))
}

// Outside GitHub Actions no run token exists, so the batch goes without one.
func TestExportOutsideGitHubSendsNoRunToken(t *testing.T) {
	stub := newPlatformStub(t, http.StatusOK, `{}`)
	code, stdout, stderr := stub.export(t, map[string]string{platformTokenVariable: testIntegrationToken}, "--batch-id", "nightly-2026-11-07")
	if code != 0 || len(stub.exports) != 1 || len(stub.identity) != 0 || len(stub.audience) != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if got := stub.exports[0].Header.Values("Costroid-Run-Token"); len(got) != 0 {
		t.Errorf("a run token went: %q", got)
	}
	if !strings.Contains(stdout, ": already held, so nothing changed\n") {
		t.Errorf("stdout %q", stdout)
	}
}

func TestExportKeepsRowsOn402And429(t *testing.T) {
	before, err := os.ReadFile(exampleRows)
	if err != nil {
		t.Fatal(err)
	}
	for _, answer := range []struct {
		status     int
		code, line string
	}{
		{http.StatusPaymentRequired, "PLAN_WRITES_REFUSED", "costroid: warning: Costroid Platform refused batch 903114-16873409921-1: 402 PLAN_WRITES_REFUSED. "},
		{http.StatusTooManyRequests, "QUOTA_EXPORTS_PER_DAY", "costroid: warning: Costroid Platform refused batch 903114-16873409921-1: 429 QUOTA_EXPORTS_PER_DAY, retry after 3600 s. "},
	} {
		t.Run(answer.code, func(t *testing.T) {
			stub := newPlatformStub(t, answer.status, `{"error":{"code":"`+answer.code+`","message":"Nothing was stored."}}`)
			if answer.status == http.StatusTooManyRequests {
				stub.header.Set("Retry-After", "3600")
			}
			code, stdout, stderr := stub.export(t, stub.githubEnv())
			if code != 0 || stdout != "" || len(stub.exports) != 1 {
				t.Fatalf("exit %d, stdout %q: %s", code, stdout, stderr)
			}
			if !strings.HasPrefix(stderr, answer.line) || !strings.HasSuffix(stderr, exampleRows+" is kept to send again\n") {
				t.Errorf("stderr %q", stderr)
			}
			after, err := os.ReadFile(exampleRows)
			if err != nil || !bytes.Equal(after, before) {
				t.Fatalf("the rows file changed: %v", err)
			}
		})
	}
}

// Every other refusal fails the command and names the code as the platform
// sent it, and only a code: the platform's message never reaches the log.
func TestExportPrintsTheRefusalCodeAsSent(t *testing.T) {
	for body, line := range map[string]string{
		`{"error":{"code":"RUN_TOKEN_OWNER_MISMATCH","message":"::error::forged"}}`: "costroid: Costroid Platform refused batch 903114-16873409921-1: 403 RUN_TOKEN_OWNER_MISMATCH\n",
		`{"error":{"code":"run_token\n::error::forged","message":""}}`:              "costroid: Costroid Platform refused batch 903114-16873409921-1: 403 with no refusal code\n",
		`not json`: "costroid: Costroid Platform refused batch 903114-16873409921-1: 403 with no refusal code\n",
	} {
		stub := newPlatformStub(t, http.StatusForbidden, body)
		code, stdout, stderr := stub.export(t, stub.githubEnv())
		if code != exitRejected || stdout != "" || stderr != line {
			t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
	}
}

func TestExportBatchIDFromRun(t *testing.T) {
	stub := newPlatformStub(t, http.StatusAccepted, `{}`)
	env := stub.githubEnv()
	for _, attempt := range []string{"1", "2"} {
		env["GITHUB_RUN_ATTEMPT"] = attempt
		if code, _, stderr := stub.export(t, env); code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
	}
	if code, _, stderr := stub.export(t, env, "--batch-id", "chosen.by-the_sender"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var paths []string
	for _, request := range stub.exports {
		paths = append(paths, strings.TrimPrefix(request.URL.Path, integrationPath+"/exports/"))
	}
	if want := "903114-16873409921-1 903114-16873409921-2 chosen.by-the_sender"; strings.Join(paths, " ") != want {
		t.Errorf("batch ids %q, want %s", paths, want)
	}

	for name, change := range map[string][2]string{
		"outside GitHub Actions":  {"GITHUB_ACTIONS", ""},
		"an attempt not a number": {"GITHUB_RUN_ATTEMPT", "1 "},
		"no repository id":        {"GITHUB_REPOSITORY_ID", ""},
	} {
		t.Run(name, func(t *testing.T) {
			env := stub.githubEnv()
			env[change[0]] = change[1]
			sent := len(stub.exports)
			if code, _, stderr := stub.export(t, env); code != exitFailure || !strings.Contains(stderr, "--batch-id") {
				t.Errorf("exit %d: %s", code, stderr)
			}
			if len(stub.exports) != sent {
				t.Error("a batch went without its id")
			}
		})
	}
}

// A GitHub job that cannot name its run sends nothing.
func TestExportOnGitHubNeedsTheRunToken(t *testing.T) {
	stub := newPlatformStub(t, http.StatusAccepted, `{}`)
	env := stub.githubEnv()
	delete(env, "ACTIONS_ID_TOKEN_REQUEST_TOKEN")
	code, _, stderr := stub.export(t, env)
	if code != exitFailure || len(stub.exports) != 0 || !strings.Contains(stderr, "id-token: write") {
		t.Fatalf("exit %d, %d exports: %s", code, len(stub.exports), stderr)
	}
}

func TestExportWithNoRowsSendsNothing(t *testing.T) {
	stub := newPlatformStub(t, http.StatusAccepted, `{}`)
	empty := filepath.Join(t.TempDir(), "rows.jsonl")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	env := stub.githubEnv()
	args := []string{"--rows", empty, "--platform-url", stub.server.URL, "--installation-id", testInstallation}
	code := export(context.Background(), args, stub.server.Client(), func(name string) string { return env[name] }, testPace, &stdout, &stderr)
	if code != 0 || len(stub.exports) != 0 || stdout.String() != "costroid: "+empty+" holds no rows, so nothing was sent\n" {
		t.Fatalf("exit %d, %d exports: %s%s", code, len(stub.exports), stdout.String(), stderr.String())
	}
}

// These refusals come before any request, so the command's own wiring, with
// the process's environment and a real client, runs them.
func TestExportRefusesBeforeSending(t *testing.T) {
	t.Setenv(platformTokenVariable, "sk-ant-not-an-integration-token")
	for name, check := range map[string]struct {
		args []string
		want string
	}{
		"no rows flag":         {[]string{"--platform-url", "https://app.costroid.com", "--installation-id", testInstallation}, "export needs --rows"},
		"plain http":           {[]string{"--rows", exampleRows, "--platform-url", "http://app.costroid.com", "--installation-id", testInstallation}, "an https origin"},
		"a path on the origin": {[]string{"--rows", exampleRows, "--platform-url", "https://app.costroid.com/api/integration/v0.2", "--installation-id", testInstallation}, "an https origin"},
		"an installation name": {[]string{"--rows", exampleRows, "--platform-url", "https://app.costroid.com", "--installation-id", "github-owner:example"}, "--installation-id must be"},
		"a provider key":       {[]string{"--rows", exampleRows, "--platform-url", "https://app.costroid.com", "--installation-id", testInstallation}, "is not an integration token"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := exportCommand(context.Background(), check.args, stdio{stdout: &stdout, stderr: &stderr})
			if code != exitFailure || stdout.Len() != 0 || !strings.Contains(stderr.String(), check.want) || strings.Contains(stderr.String(), "sk-ant") {
				t.Fatalf("exit %d: %s%s", code, stdout.String(), stderr.String())
			}
		})
	}
}

// The help names every flag and variable the command reads.
func TestExportHelpNamesEveryFlag(t *testing.T) {
	if _, err := parseExport([]string{"-h"}, new(bytes.Buffer), new(bytes.Buffer)); err != flag.ErrHelp {
		t.Fatalf("-h: %v", err)
	}
	for _, name := range []string{"--rows", "--platform-url", "--installation-id", "--batch-id", "--format", "--out", "focus-csv", "vantage", "datadog", platformTokenVariable} {
		if !strings.Contains(exportHelp, name) {
			t.Errorf("the help does not name %s", name)
		}
	}
}

// sentIdentities reads the Tags cell of each row in a batch's CSV and returns
// each row's run identity and attribution grade.
func sentIdentities(t *testing.T, body []byte) [][2]string {
	t.Helper()
	records, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil || len(records) < 2 {
		t.Fatalf("the body is not a CSV of rows: %v", err)
	}
	column := slices.Index(records[0], "Tags")
	var identities [][2]string
	for _, record := range records[1:] {
		var tags map[string]string
		if err := json.Unmarshal([]byte(record[column]), &tags); err != nil {
			t.Fatal(err)
		}
		identities = append(identities, [2]string{tags["costroid/run_identity"], tags["costroid/attribution_grade"]})
	}
	return identities
}

// writeRows writes the example rows with each row's Tags changed by edit, and
// returns the file's path.
func writeRows(t *testing.T, edit func(row int, tags map[string]string)) string {
	t.Helper()
	data, err := os.ReadFile(exampleRows)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	for row, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var fields map[string]any
		if err := json.Unmarshal(line, &fields); err != nil {
			t.Fatal(err)
		}
		tags := map[string]string{}
		for key, value := range fields["Tags"].(map[string]any) {
			tags[key] = value.(string)
		}
		edit(row, tags)
		fields["Tags"] = tags
		encoded, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(append(encoded, '\n'))
	}
	path := filepath.Join(t.TempDir(), "rows.jsonl")
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestExportStampsOnlyItsRun sends rows as costroid run writes them, every one
// unverified, under a run token. Only a row naming this run and this
// repository, joined by its run and not by a time window, goes as
// token_verified. Without a run token no row does.
func TestExportStampsOnlyItsRun(t *testing.T) {
	rows := writeRows(t, func(row int, tags map[string]string) {
		tags["costroid/run_identity"], tags["costroid/attribution_grade"] = "unverified", "unattributed"
		switch row {
		case 1:
			tags["costroid/workflow_run_id"] = "16873409922"
		case 2:
			tags["costroid/repo"] = "github.com/example/gadgets"
		case 3:
			tags["costroid/attribution_grade"] = "windowed"
		}
	})
	want := [][2]string{{"token_verified", "unattributed"}}
	for range 3 {
		want = append(want, [2]string{"unverified", "unattributed"})
	}
	for range 7 {
		want = append(want, [2]string{"token_verified", "unattributed"})
	}

	stub := newPlatformStub(t, http.StatusAccepted, `{"status":"accepted"}`)
	code, stdout, stderr := stub.export(t, stub.githubEnv(), "--rows", rows)
	if code != 0 || len(stub.exports) != 1 || stub.exports[0].Header.Get("Costroid-Run-Token") != testRunToken {
		t.Fatalf("exit %d, %d exports: %s", code, len(stub.exports), stderr)
	}
	if got := sentIdentities(t, stub.bodies[0]); !slices.Equal(got, want) {
		t.Errorf("sent %v, want %v", got, want)
	}
	if line := "costroid: 8 of 11 rows name run 16873409921 of github.com/example/widgets, so they went as token_verified and the rest as unverified\n"; !strings.Contains(stdout, line) {
		t.Errorf("stdout %q", stdout)
	}

	// The same run's variables, outside GitHub Actions: no run token goes, so
	// no row claims one.
	outside := map[string]string{platformTokenVariable: testIntegrationToken, "GITHUB_RUN_ID": testRun, "GITHUB_REPOSITORY": testRepository}
	code, stdout, stderr = stub.export(t, outside, "--rows", rows, "--batch-id", "nightly")
	if code != 0 || len(stub.exports) != 2 || len(stub.exports[1].Header.Values("Costroid-Run-Token")) != 0 {
		t.Fatalf("exit %d, %d exports: %s", code, len(stub.exports), stderr)
	}
	for _, identity := range sentIdentities(t, stub.bodies[1]) {
		if identity != [2]string{"unverified", "unattributed"} {
			t.Errorf("without a run token a row went as %v", identity)
		}
	}
	if strings.Contains(stdout, "token_verified") {
		t.Errorf("stdout %q", stdout)
	}
}

// TestExportLeavesForkRowsUnverified sends a fork's rows from the follow-up
// workflow, whose run is not the run the rows name. The rows claim
// token_verified and exact, and every one goes as unverified and
// unattributed, so the batch cannot fail ROW_IDENTITY_MISMATCH.
func TestExportLeavesForkRowsUnverified(t *testing.T) {
	stub := newPlatformStub(t, http.StatusAccepted, `{"status":"accepted"}`)
	env := stub.githubEnv()
	env["GITHUB_RUN_ID"] = "16873409999"
	code, stdout, stderr := stub.export(t, env)
	if code != 0 || len(stub.exports) != 1 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	identities := sentIdentities(t, stub.bodies[0])
	if len(identities) != 11 {
		t.Fatalf("%d rows went", len(identities))
	}
	for _, identity := range identities {
		if identity != [2]string{"unverified", "unattributed"} {
			t.Errorf("a fork's row went as %v", identity)
		}
	}
	if line := "costroid: 0 of 11 rows name run 16873409999 of github.com/example/widgets, so they went as token_verified and the rest as unverified\n"; !strings.Contains(stdout, line) {
		t.Errorf("stdout %q", stdout)
	}
}

// TestExportReadsTheIngestVerdict waits on GET /exports/{batch_id} after the
// platform took a batch: an ingested batch prints its link, a failed one its
// code alone and exits 1, and no verdict within the wait warns and exits 0.
func TestExportReadsTheIngestVerdict(t *testing.T) {
	for name, check := range map[string]struct {
		verdicts []turn
		limit    time.Duration
		code     int
		stdout   []string
		stderr   string
	}{
		"failed after two polls": {
			verdicts: []turn{pending("accepted"), pending("ingesting"), failed("ROW_IDENTITY_MISMATCH")},
			code:     exitRejected,
			stderr:   "costroid: batch 903114-16873409921-1 failed on ingest: ROW_IDENTITY_MISMATCH. Costroid Platform stored none of its rows\n",
		},
		"a failure code outside the contract": {
			verdicts: []turn{failed("row_identity\n::error::forged")},
			code:     exitRejected,
			stderr:   "costroid: batch 903114-16873409921-1 failed on ingest: with no failure code. Costroid Platform stored none of its rows\n",
		},
		"ingested": {
			verdicts: []turn{pending("ingesting"), ingested(11, 2)},
			stdout: []string{
				"costroid: batch 903114-16873409921-1 ingested, 11 rows\n",
				"costroid: Costroid Platform dropped 2 Tags entries outside costroid/, and stored the rows without them\n",
				"costroid: pull request 1423 of github.com/example/widgets: ",
			},
		},
		"no verdict in the wait": {
			verdicts: slices.Repeat([]turn{pending("ingesting")}, 1000),
			limit:    50 * time.Millisecond,
			stderr:   "costroid: warning: batch 903114-16873409921-1 has no verdict after 0 s. The platform took batch 903114-16873409921-1, and states its verdict at GET /exports/903114-16873409921-1\n",
		},
		"a refused verdict request": {
			verdicts: []turn{{http.StatusForbidden, `{"error":{"code":"AUTH_TOKEN_SCOPE","message":"x"}}`}},
			stderr:   "costroid: warning: Costroid Platform refused the verdict request for batch 903114-16873409921-1: 403 AUTH_TOKEN_SCOPE. The platform took batch 903114-16873409921-1, and states its verdict at GET /exports/903114-16873409921-1\n",
		},
		"a server error is asked again": {
			verdicts: []turn{{http.StatusBadGateway, ``}, ingested(11, 0)},
			stdout:   []string{"costroid: batch 903114-16873409921-1 ingested, 11 rows\n"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			stub := newPlatformStub(t, http.StatusAccepted, `{"batch_id":"903114-16873409921-1","status":"accepted"}`)
			stub.verdicts = check.verdicts
			pace := testPace
			if check.limit != 0 {
				pace.limit = check.limit
			}
			var stdout, stderr bytes.Buffer
			env := stub.githubEnv()
			args := []string{"--rows", exampleRows, "--platform-url", stub.server.URL, "--installation-id", testInstallation}
			code := export(context.Background(), args, stub.server.Client(), func(name string) string { return env[name] }, pace, &stdout, &stderr)
			if code != check.code || stderr.String() != check.stderr {
				t.Fatalf("exit %d, stderr %q", code, stderr.String())
			}
			for _, line := range check.stdout {
				if !strings.Contains(stdout.String(), line) {
					t.Errorf("stdout %q lacks %q", stdout.String(), line)
				}
			}
			if strings.Contains(stdout.String()+stderr.String(), "::error::forged") {
				t.Error("the platform's message reached the log")
			}
			if stub.asked == 0 {
				t.Error("export never asked for the verdict")
			}
		})
	}
}

// A batch the send already settled is not asked for again.
func TestExportTakesTheVerdictInTheAnswer(t *testing.T) {
	stub := newPlatformStub(t, http.StatusOK, ingested(11, 0).body)
	code, stdout, stderr := stub.export(t, stub.githubEnv())
	if code != 0 || stub.asked != 0 || !strings.Contains(stdout, "ingested, 11 rows\n") {
		t.Fatalf("exit %d, asked %d times: %s%s", code, stub.asked, stdout, stderr)
	}
}

// TestExportWarnsWhenNotBound: a 409 INSTALLATION_NOT_BOUND took nothing
// because the tenant has not bound the installation, so export names costroid
// connect, keeps the rows and exits 0. Another 409 still fails.
func TestExportWarnsWhenNotBound(t *testing.T) {
	before, err := os.ReadFile(exampleRows)
	if err != nil {
		t.Fatal(err)
	}
	stub := newPlatformStub(t, http.StatusConflict, `{"error":{"code":"INSTALLATION_NOT_BOUND","message":"::error::forged"}}`)
	code, stdout, stderr := stub.export(t, stub.githubEnv())
	want := "costroid: warning: Costroid Platform refused batch 903114-16873409921-1: 409 INSTALLATION_NOT_BOUND. The tenant holds no binding of github-owner:7204411: run costroid connect, as docs/connect.md shows, and " + exampleRows + " is kept to send again\n"
	if code != 0 || stdout != "" || stderr != want || stub.asked != 0 {
		t.Fatalf("exit %d, asked %d, stdout %q, stderr %q", code, stub.asked, stdout, stderr)
	}
	if after, err := os.ReadFile(exampleRows); err != nil || !bytes.Equal(after, before) {
		t.Fatalf("the rows file changed: %v", err)
	}

	busy := newPlatformStub(t, http.StatusConflict, `{"error":{"code":"EXPORT_BATCH_BUSY","message":""}}`)
	if code, _, stderr := busy.export(t, busy.githubEnv()); code != exitRejected || strings.Contains(stderr, "costroid connect") {
		t.Errorf("EXPORT_BATCH_BUSY: exit %d, stderr %q", code, stderr)
	}
}

// TestExportAcceptsLoopbackHTTP reaches a platform on plain http at
// 127.0.0.1, as the end-to-end harness serves it, and refuses plain http to
// any other host, localhost included.
func TestExportAcceptsLoopbackHTTP(t *testing.T) {
	stub := &platformStub{status: http.StatusAccepted, body: `{"status":"accepted"}`, header: http.Header{}}
	stub.server = httptest.NewServer(http.HandlerFunc(stub.serve))
	t.Cleanup(stub.server.Close)
	if !strings.HasPrefix(stub.server.URL, "http://127.0.0.1:") {
		t.Fatalf("the stub listens at %s", stub.server.URL)
	}
	code, stdout, stderr := stub.export(t, map[string]string{platformTokenVariable: testIntegrationToken}, "--batch-id", "e2e-1")
	if code != 0 || len(stub.exports) != 1 || !strings.Contains(stdout, "ingested, 11 rows") {
		t.Fatalf("exit %d, %d exports: %s%s", code, len(stub.exports), stdout, stderr)
	}
	if origin, err := platformOrigin("http://[::1]:8787"); err != nil || origin != "http://[::1]:8787" {
		t.Errorf("[::1]: %q, %v", origin, err)
	}
	for _, address := range []string{"http://example.com", "http://localhost:8787", "http://10.0.0.1:8787", "http://127.0.0.1.example.com", "ftp://127.0.0.1"} {
		if origin, err := platformOrigin(address); err == nil {
			t.Errorf("%s was taken as %s", address, origin)
		}
	}
}

// runExport runs costroid export with no platform token in the environment,
// so a test that reaches the platform fails.
func runExport(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv(platformTokenVariable, "")
	var stdout, stderr bytes.Buffer
	code := exportCommand(context.Background(), args, stdio{stdout: &stdout, stderr: &stderr})
	return code, stdout.String(), stderr.String()
}

// TestExportFormatWritesAFileAndSendsNothing writes each format to a file and
// to standard output, the same bytes both ways, and sends nothing: no platform
// flag is given and none is taken.
func TestExportFormatWritesAFileAndSendsNothing(t *testing.T) {
	for _, format := range []string{"focus-csv", "vantage", "datadog"} {
		t.Run(format, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "costs.csv")
			code, stdout, stderr := runExport(t, "--rows", exampleRows, "--format", format, "--out", path)
			if code != 0 || stdout != "costroid: wrote 11 rows as "+format+" to "+path+"\n" {
				t.Fatalf("exit %d: %s%s", code, stdout, stderr)
			}
			file, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			code, streamed, _ := runExport(t, "--rows", exampleRows, "--format", format)
			if code != 0 || streamed != string(file) {
				t.Fatalf("exit %d, standard output differs from the file", code)
			}
		})
	}
	want, err := os.ReadFile(exampleCSV)
	if err != nil {
		t.Fatal(err)
	}
	if _, stdout, _ := runExport(t, "--rows", exampleRows, "--format", "focus-csv"); stdout != string(want) {
		t.Error("focus-csv is not the CSV the platform takes")
	}
}

// Without --format, export sends to the platform as before, and --out alone
// is refused, so no file is written by mistake.
func TestExportWithoutFormatStillSends(t *testing.T) {
	if code, _, stderr := runExport(t, "--rows", exampleRows, "--out", filepath.Join(t.TempDir(), "costs.csv")); code != exitFailure || !strings.Contains(stderr, "--out names the file --format writes") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if code, _, stderr := runExport(t, "--rows", exampleRows); code != exitFailure || !strings.Contains(stderr, "export needs --rows, --platform-url and --installation-id") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

func TestExportFormatRefusesBeforeWriting(t *testing.T) {
	for name, check := range map[string]struct {
		args []string
		want string
	}{
		"an unknown format": {[]string{"--rows", exampleRows, "--format", "parquet"}, `--format is "parquet", and export writes focus-csv, vantage, datadog`},
		"no rows":           {[]string{"--format", "vantage"}, "export --format needs --rows"},
		"a platform flag":   {[]string{"--rows", exampleRows, "--format", "vantage", "--platform-url", "https://app.costroid.com"}, "writes a file and sends nothing"},
		"a batch id":        {[]string{"--rows", exampleRows, "--format", "datadog", "--batch-id", "nightly"}, "writes a file and sends nothing"},
	} {
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr := runExport(t, check.args...)
			if code != exitFailure || stdout != "" || !strings.Contains(stderr, check.want) {
				t.Fatalf("exit %d: %s%s", code, stdout, stderr)
			}
		})
	}
}

// A Datadog export with a row in EUR exits 1, names the row and writes no
// file.
func TestExportDatadogRefusalWritesNothing(t *testing.T) {
	rows := writeRows(t, func(int, map[string]string) {})
	data, err := os.ReadFile(rows)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	lines[6] = bytes.Replace(lines[6], []byte(`"BillingCurrency":"USD"`), []byte(`"BillingCurrency":"EUR"`), 1)
	if err := os.WriteFile(rows, append(bytes.Join(lines, []byte("\n")), '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "costs.csv")
	code, stdout, stderr := runExport(t, "--rows", rows, "--format", "datadog", "--out", path)
	if code != exitRejected || stdout != "" || !strings.HasPrefix(stderr, "costroid: datadog refuses row 7 (x_CostroidRowId ") ||
		!strings.Contains(stderr, ") in EUR: Datadog takes USD only, and export never converts an amount. Nothing was written\n") {
		t.Fatalf("exit %d: %s%s", code, stdout, stderr)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a file was written: %v", err)
	}
}

// Datadog's file has a column for a tag only when every row carries it, and
// the command names the tags it left out.
func TestExportDatadogNamesTheTagsLeftOut(t *testing.T) {
	code, _, stderr := runExport(t, "--rows", exampleRows, "--format", "datadog", "--out", filepath.Join(t.TempDir(), "costs.csv"))
	if code != 0 || stderr != "costroid: warning: some rows lack the tags costroid/assumed_ttl, so the datadog file has no column for them\n" {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

// Rows past one Vantage file go to numbered files beside --out, and standard
// output, which cannot keep files apart, is refused.
func TestExportVantageNumbersItsFiles(t *testing.T) {
	data, err := os.ReadFile(exampleRows)
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := bytes.Cut(data, []byte("\n"))
	rows := filepath.Join(t.TempDir(), "rows.jsonl")
	if err := os.WriteFile(rows, bytes.Repeat(append(first, '\n'), 10001), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	code, stdout, stderr := runExport(t, "--rows", rows, "--format", "vantage", "--out", filepath.Join(dir, "costs.csv"))
	if code != 0 || !strings.Contains(stdout, "costs-1.csv") || !strings.Contains(stdout, "costs-2.csv") {
		t.Fatalf("exit %d: %s%s", code, stdout, stderr)
	}
	written, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, path := range written {
		records, err := csv.NewReader(bytesFile(t, path)).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		total += len(records) - 1
	}
	if len(written) < 2 || total != 10001 {
		t.Fatalf("%d files hold %d rows", len(written), total)
	}
	code, stdout, stderr = runExport(t, "--rows", rows, "--format", "vantage")
	if code != exitRejected || stdout != "" || !strings.Contains(stderr, "vantage files, so name them with --out. Nothing was written") {
		t.Fatalf("exit %d: %q %s", code, stdout, stderr)
	}
}

func bytesFile(t *testing.T, path string) *bytes.Reader {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(data)
}

func TestExportFormatOfNoRowsWritesNothing(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "rows.jsonl")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runExport(t, "--rows", empty, "--format", "focus-csv")
	if code != 0 || stdout != "" || stderr != "costroid: "+empty+" holds no rows, so nothing was written\n" {
		t.Fatalf("exit %d: %q %q", code, stdout, stderr)
	}
}

// The example of docs/export.md's Files for another tool runs, on the example
// rows, into a file of its own.
func TestExportDocsFormatExampleRuns(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("..", "..", "docs", "export.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, section, found := strings.Cut(string(page), "## Files for another tool\n")
	_, block, _ := strings.Cut(section, "```sh\n")
	command, _, _ := strings.Cut(block, "\n```")
	fields := strings.Fields(command)
	if !found || len(fields) < 2 || fields[0] != "costroid" || fields[1] != "export" || !slices.Contains(fields, "--format") {
		t.Fatalf("the section's example is %q", command)
	}
	out := filepath.Join(t.TempDir(), "costs.csv")
	args := slices.Clone(fields[2:])
	for i, arg := range args {
		switch arg {
		case "rows.jsonl":
			args[i] = exampleRows
		case "costs.csv":
			args[i] = out
		}
	}
	if code, stdout, stderr := runExport(t, args...); code != 0 || stdout != "costroid: wrote 11 rows as vantage to "+out+"\n" {
		t.Fatalf("%v: exit %d: %s%s", args, code, stdout, stderr)
	}
}
