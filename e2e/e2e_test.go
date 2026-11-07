//go:build e2e

// Package e2e proves the whole Phase 0 path against one known figure: a recorded
// run, priced by the CLI, stored and priced again by the service in its container,
// read back through the per-PR view, printed in the comment and reconciled.
//
// Run it with make e2e. It needs Docker, and it builds the image.
package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/scm/scmtest"
)

const (
	// recordedTotal is the recorded run's list cost, hand-derived in
	// internal/run/run_test.go: 8 × 0.10 + 15605 × 0.01 + 3495 × 0.125 +
	// 4450 × 0.20 + 272 × 0.50, per million tokens. Claude Code's own
	// total_cost_usd in the recording is the same figure.
	recordedTotal = "0.001619725"
	recordedRows  = 19

	fixtures = "../internal/adapter/claudecode/testdata/2.1.316/subagent"
	session  = "00000000-0000-4000-8000-000000000002"

	owner      = "github.com:acme"
	repository = "github.com/acme/widgets"
	pull       = "7"
	head       = "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d"
	base       = "1111111111111111111111111111111111111111"
	merge      = "2222222222222222222222222222222222222222"
	runID      = "16873409921"
	credential = "apikey_01ExampleE2e"
	reportDay  = "2026-11-01"
	savedAt    = "2026-11-04T00:00:00Z"

	issuerHost = "token.actions.githubusercontent.com"

	serviceWait = 90 * time.Second
	// reportWait is longer than the service's five seconds between writes.
	reportWait = 30 * time.Second
)

// stack is one Compose project, torn down with the test.
type stack struct {
	t       *testing.T
	project string
	url     string
	env     []string
}

func compose(t *testing.T) *stack {
	t.Helper()
	port := freePort(t)
	s := &stack{
		t:       t,
		project: fmt.Sprintf("costroid-e2e-%d", os.Getpid()),
		url:     "http://127.0.0.1:" + strconv.Itoa(port),
		env: append(os.Environ(),
			"POSTGRES_PASSWORD=e2e-superuser", "COSTROID_OWNER_PASSWORD=e2e-owner", "COSTROID_APP_PASSWORD=e2e-app",
			"COSTROID_PSEUDONYM_KEY=ZTJlLXBzZXVkb255bS1rZXktb2YtdGhpcnR5LXR3by1ieXRlcw==",
			"COSTROID_OIDC_AUDIENCE=https://costroid.example", "COSTROID_BIND="+owner,
			"COSTROID_IMAGE=costroid-e2e:local", "COSTROID_VERSION=e2e", "COSTROID_PORT="+strconv.Itoa(port),
			"COSTROID_EGRESS_ALLOW=",
		),
	}
	t.Cleanup(s.down)
	if out, err := s.docker(nil, "up", "--detach", "--build", "--wait"); err != nil {
		t.Fatalf("compose up: %v\n%s", err, out)
	}
	s.awaitService()
	return s
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func (s *stack) docker(stdout io.Writer, args ...string) ([]byte, error) {
	command := exec.Command("docker", append([]string{"compose", "--project-name", s.project, "--file", "../deploy/compose.yaml"}, args...)...)
	command.Env = s.env
	var output bytes.Buffer
	command.Stderr = &output
	command.Stdout = &output
	if stdout != nil {
		command.Stdout = stdout
	}
	err := command.Run()
	return output.Bytes(), err
}

func (s *stack) down() {
	if s.t.Failed() {
		if out, err := s.docker(nil, "logs", "--no-color", "--tail", "40"); err == nil {
			s.t.Logf("service logs:\n%s", out)
		}
	}
	if out, err := s.docker(nil, "down", "--volumes", "--remove-orphans"); err != nil {
		s.t.Errorf("compose down: %v\n%s", err, out)
	}
}

func (s *stack) awaitService() {
	s.t.Helper()
	deadline := time.Now().Add(serviceWait)
	var last error
	for time.Now().Before(deadline) {
		response, err := http.Get(s.url + "/healthz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusNoContent {
				return
			}
			err = fmt.Errorf("status %d", response.StatusCode)
		}
		last = err
		time.Sleep(250 * time.Millisecond)
	}
	s.t.Fatalf("the service did not answer: %v", last)
}

// createKey runs costroid keys create inside the container. The key stays in
// memory: nothing here logs it.
func (s *stack) createKey() string {
	s.t.Helper()
	var key bytes.Buffer
	out, err := s.docker(&key, "exec", "--no-TTY", "costroid", "/usr/local/bin/costroid", "keys", "create", "--owner", owner)
	if err != nil {
		s.t.Fatalf("keys create: %v\n%s", err, out)
	}
	return strings.TrimSpace(key.String())
}

func (s *stack) request(method, path, key string, body []byte) (int, []byte) {
	s.t.Helper()
	request, err := http.NewRequest(method, s.url+path, bytes.NewReader(body))
	if err != nil {
		s.t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/x-ndjson")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		s.t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		s.t.Fatal(err)
	}
	return response.StatusCode, data
}

// workspace is what a CI job holds: the costroid binary, a fake claude that
// replays the recording, the recording's session files and an event file.
type workspace struct {
	t          *testing.T
	directory  string
	binary     string
	agent      string
	attributes string
	rows       string
	env        []string
}

func newWorkspace(t *testing.T) *workspace {
	t.Helper()
	directory := t.TempDir()
	w := &workspace{
		t: t, directory: directory,
		binary:     filepath.Join(directory, "costroid"),
		agent:      filepath.Join(directory, "claude"),
		attributes: filepath.Join(directory, "attributes"),
		rows:       filepath.Join(directory, "rows.jsonl"),
	}
	build := exec.Command("go", "build", "-o", w.binary, "../cmd/costroid")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	stream, err := filepath.Abs(filepath.Join(fixtures, "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s' \"$OTEL_RESOURCE_ATTRIBUTES\" > " + w.attributes + "\nexec cat " + stream + "\n"
	w.write(w.agent, script, 0o755)
	config := filepath.Join(directory, "claude-config")
	project := filepath.Join(config, "projects", "-work")
	w.copy(filepath.Join(fixtures, "session.jsonl"), filepath.Join(project, session+".jsonl"))
	w.copy(filepath.Join(fixtures, "subagents", "agent-1.jsonl"), filepath.Join(project, session, "subagents", "agent-1.jsonl"))
	event := filepath.Join(directory, "event.json")
	w.write(event, fmt.Sprintf(`{"pull_request":{"number":%s,"head":{"sha":%q},"base":{"sha":%q,"ref":"main"}}}`, pull, head, base), 0o644)
	w.env = append(os.Environ(),
		"CLAUDE_CONFIG_DIR="+config, "GITHUB_ACTIONS=true", "GITHUB_SERVER_URL=https://github.com",
		"GITHUB_REPOSITORY=acme/widgets", "GITHUB_RUN_ID="+runID, "GITHUB_JOB=agent", "GITHUB_SHA="+merge,
		"GITHUB_EVENT_NAME=pull_request", "GITHUB_EVENT_PATH="+event, "COSTROID_INSTALLATION_ID=", "COSTROID_CREDENTIAL="+credential,
	)
	return w
}

func (w *workspace) write(path, content string, mode os.FileMode) {
	w.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		w.t.Fatal(err)
	}
}

func (w *workspace) copy(from, to string) {
	w.t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		w.t.Fatal(err)
	}
	w.write(to, string(data), 0o644)
}

// costroid runs the binary and returns its exit code, stdout and stderr.
func (w *workspace) costroid(env []string, args ...string) (int, string, string) {
	w.t.Helper()
	command := exec.Command(w.binary, args...)
	command.Env = env
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		w.t.Fatalf("run costroid: %v", err)
	}
	return command.ProcessState.ExitCode(), stdout.String(), stderr.String()
}

type view struct {
	Totals []struct {
		Currency  string `json:"currency"`
		CostBasis string `json:"cost_basis"`
		ListCost  string `json:"list_cost"`
	} `json:"totals"`
	Budget struct {
		Evidence bool `json:"evidence"`
	} `json:"budget"`
	Lines []struct {
		Rows             int64  `json:"rows"`
		RunIdentity      string `json:"run_identity"`
		AttributionGrade string `json:"attribution_grade"`
	} `json:"lines"`
}

func TestThePathFromARecordedRunToTheCommentHoldsOneFigure(t *testing.T) {
	services := compose(t)
	key := services.createKey()
	job := newWorkspace(t)

	runAgent(t, job)
	sendRows(t, services, key, job)
	readView(t, services, key)
	postReport(t, job)
	reconcileRows(t, job)
	refuseAnIssuerOffTheList(t, services)
}

func runAgent(t *testing.T, job *workspace) {
	t.Helper()
	code, stdout, stderr := job.costroid(job.env, "run", "--budget-usd", "5", "--out", job.rows, "--", job.agent, "-p", "Fix the failing test.")
	if code != 0 {
		t.Fatalf("costroid run exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, `"type":"result"`) {
		t.Error("the agent's stream did not pass through")
	}
	if want := "USD " + recordedTotal + " at list"; !strings.Contains(stderr, want) {
		t.Errorf("costroid run: want %q in:\n%s", want, stderr)
	}
	attributes, err := os.ReadFile(job.attributes)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"vcs.change.id=" + pull, "vcs.ref.head.revision=" + head, "vcs.ref.base.revision=" + base, "cicd.pipeline.run.id=" + runID} {
		if !strings.Contains(string(attributes), want) {
			t.Errorf("the agent's OTEL_RESOURCE_ATTRIBUTES lack %q: %s", want, attributes)
		}
	}
	rows, err := os.ReadFile(job.rows)
	if err != nil {
		t.Fatal(err)
	}
	var first struct {
		SubAccountID string            `json:"SubAccountId"`
		Tags         map[string]string `json:"Tags"`
	}
	line, _, _ := bytes.Cut(rows, []byte("\n"))
	if err := json.Unmarshal(line, &first); err != nil {
		t.Fatal(err)
	}
	if first.SubAccountID != credential {
		t.Errorf("SubAccountId reads %q", first.SubAccountID)
	}
	for tag, want := range map[string]string{
		"costroid/repo": repository, "costroid/pr": pull, "costroid/commit_sha": head, "costroid/workflow_run_id": runID,
		"costroid/run_identity": "unverified", "costroid/attribution_grade": "unattributed",
	} {
		if first.Tags[tag] != want {
			t.Errorf("%s reads %q, want %q", tag, first.Tags[tag], want)
		}
	}
}

func sendRows(t *testing.T, services *stack, key string, job *workspace) {
	t.Helper()
	rows, err := os.ReadFile(job.rows)
	if err != nil {
		t.Fatal(err)
	}
	status, body := services.request(http.MethodPost, "/v1/rows", key, rows)
	if status != http.StatusOK {
		t.Fatalf("POST /v1/rows: %d %s", status, body)
	}
	var answer struct {
		Accepted int `json:"accepted"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		t.Fatal(err)
	}
	if answer.Accepted != recordedRows {
		t.Errorf("accepted %d rows, want %d: %s", answer.Accepted, recordedRows, body)
	}
}

func readView(t *testing.T, services *stack, key string) {
	t.Helper()
	status, body := services.request(http.MethodGet, "/v1/pulls/"+pull+"?repo="+repository, key, nil)
	if status != http.StatusOK {
		t.Fatalf("GET the per-PR view: %d %s", status, body)
	}
	var answer view
	if err := json.Unmarshal(body, &answer); err != nil {
		t.Fatal(err)
	}
	if len(answer.Totals) != 1 || answer.Totals[0].ListCost != recordedTotal || answer.Totals[0].Currency != "USD" || answer.Totals[0].CostBasis != "list_equivalent" {
		t.Errorf("per-PR view: want USD %s list_equivalent, got %+v", recordedTotal, answer.Totals)
	}
	if answer.Budget.Evidence {
		t.Error("an ingest key's rows reached a budget")
	}
	rows := int64(0)
	for _, line := range answer.Lines {
		rows += line.Rows
		if line.RunIdentity != "unverified" || line.AttributionGrade != "unattributed" {
			t.Errorf("an ingest key's line reads %+v", line)
		}
	}
	if rows != recordedRows {
		t.Errorf("the view holds %d rows", rows)
	}
}

func postReport(t *testing.T, job *workspace) {
	t.Helper()
	github := scmtest.New(t)
	github.Branches["main"] = base
	env := append(job.env, "GITHUB_TOKEN="+github.Token, "GITHUB_API_URL="+github.Server.URL)
	code, _, stderr := job.costroid(env, "report", "--rows", job.rows, "--budget-usd", "5")
	if code != 0 {
		t.Fatalf("costroid report exit %d: %s", code, stderr)
	}
	if len(github.Checks) != 1 || len(github.Comments) != 1 {
		t.Fatalf("%d checks, %d comments: %s", len(github.Checks), len(github.Comments), stderr)
	}
	if github.Checks[0].HeadSHA != head {
		t.Errorf("the check sits on %s, not the head", github.Checks[0].HeadSHA)
	}
	body := github.Comments[0].Body
	if want := "USD " + recordedTotal; !strings.Contains(body, want) {
		t.Errorf("comment: want %q in:\n%s", want, body)
	}
	if want := "Unverified: the agent's job wrote these figures."; !strings.Contains(body, want) {
		t.Errorf("comment: no label in:\n%s", body)
	}
}

// issuerToken names GitHub's issuer and is not signed by it. The service reads
// the issuer before it checks a signature, so the list stops the token first.
func issuerToken() string {
	encode := base64.RawURLEncoding.EncodeToString
	return encode([]byte(`{"alg":"RS256","kid":"e2e"}`)) + "." +
		encode([]byte(`{"iss":"https://`+issuerHost+`"}`)) + "." + encode([]byte("unsigned"))
}

// refuseAnIssuerOffTheList walks the stack's empty allowlist: the service names
// the issuer at start, refuses its token by type and reports the attempt.
func refuseAnIssuerOffTheList(t *testing.T, services *stack) {
	t.Helper()
	logs, err := services.docker(nil, "logs", "--no-color", "costroid")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(logs, []byte("egress allowlist does not cover this issuer")) || !bytes.Contains(logs, []byte(issuerHost)) {
		t.Errorf("the service did not name the issuer at start:\n%s", logs)
	}
	status, body := services.request(http.MethodPost, "/v1/rows", issuerToken(), nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("a token off the list: %d %s", status, body)
	}
	for _, want := range []string{"EGRESS_REFUSED", issuerHost, "--egress-allow"} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("the refusal lacks %q: %s", want, body)
		}
	}
	services.awaitRefusal()
}

func (s *stack) awaitRefusal() {
	s.t.Helper()
	deadline := time.Now().Add(reportWait)
	var last []byte
	for time.Now().Before(deadline) {
		var report bytes.Buffer
		if out, err := s.docker(&report, "exec", "--no-TTY", "costroid", "/usr/local/bin/costroid", "egress", "report", "--json"); err != nil {
			s.t.Fatalf("egress report: %v\n%s", err, out)
		}
		var answer struct {
			Counters []struct {
				Host    string `json:"host"`
				Outcome string `json:"outcome"`
				Count   int64  `json:"count"`
			} `json:"counters"`
		}
		if err := json.Unmarshal(report.Bytes(), &answer); err != nil {
			s.t.Fatalf("egress report: %v: %s", err, report.Bytes())
		}
		for _, counter := range answer.Counters {
			if counter.Host == issuerHost && counter.Outcome == "refused" && counter.Count == 1 {
				return
			}
		}
		last = report.Bytes()
		time.Sleep(time.Second)
	}
	s.t.Errorf("the report never showed the refusal: %s", last)
}

func reconcileRows(t *testing.T, job *workspace) {
	t.Helper()
	code, stdout, stderr := job.costroid(job.env, "reconcile", "--rows", job.rows, "--from", reportDay, "--to", reportDay,
		"--reports", "testdata/saved-reports", "--as-of", savedAt)
	if code != 0 {
		t.Fatalf("costroid reconcile exit %d:\n%s\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, credential) || !strings.Contains(stdout, "within 1%") {
		t.Errorf("reconcile: no matched row for %s:\n%s", credential, stdout)
	}
}
