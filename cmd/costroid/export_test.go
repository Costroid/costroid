package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const (
	testIntegrationToken = "costroid_it_" + "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_AbCdE"
	testRunToken         = "eyJhbGciOiJSUzI1NiJ9.eyJydW5faWQiOiIxNjg3MzQwOTkyMSJ9.c2lnbmF0dXJl"
	testRequestToken     = "request-token"
	testTenant           = "org_01JEXAMPLE0001"
	testInstallation     = "github-owner:7204411"
)

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

	mu       sync.Mutex
	exports  []*http.Request
	bodies   [][]byte
	audience []string
	identity []string
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
	default:
		http.Error(w, "", http.StatusNotFound)
	}
}

// githubEnv is a GitHub Actions job with id-token: write, run 16873409921's
// first attempt in repository 903114.
func (s *platformStub) githubEnv() map[string]string {
	return map[string]string{
		"GITHUB_ACTIONS":                 "true",
		"GITHUB_REPOSITORY_ID":           "903114",
		"GITHUB_RUN_ID":                  "16873409921",
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
	code := export(context.Background(), args, s.server.Client(), func(name string) string { return env[name] }, &stdout, &stderr)
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
	want, err := os.ReadFile(exampleCSV)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(want)
	request, body := stub.exports[0], stub.bodies[0]
	if !bytes.Equal(body, want) {
		t.Error("the body is not the rows as the schemas' CSV example")
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
	if line := "costroid: batch 903114-16873409921-1, 11 rows, sha256 " + hex.EncodeToString(sum[:]) + ": accepted for ingest\n"; stdout != line {
		t.Errorf("stdout %q", stdout)
	}
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
	if !strings.HasSuffix(stdout, ": already held, so nothing changed\n") {
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
	code := export(context.Background(), args, stub.server.Client(), func(name string) string { return env[name] }, &stdout, &stderr)
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
	if _, err := parseExport([]string{"-h"}, new(bytes.Buffer)); err != flag.ErrHelp {
		t.Fatalf("-h: %v", err)
	}
	for _, name := range []string{"--rows", "--platform-url", "--installation-id", "--batch-id", platformTokenVariable} {
		if !strings.Contains(exportHelp, name) {
			t.Errorf("the help does not name %s", name)
		}
	}
}
