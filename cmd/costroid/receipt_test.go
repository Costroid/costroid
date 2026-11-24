package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/comment"
	receipts "github.com/Costroid/costroid/internal/receipt"
	"github.com/Costroid/costroid/internal/receipt/receipttest"
	"github.com/Costroid/costroid/internal/scm/scmtest"
)

const receiptBranch = "fix/retry-backoff"

var receiptDay = time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)

// receiptRepository is a git repository on the branch fix/retry-backoff,
// whose reflog's one entry was written on 2026-10-30, before the recorded
// sessions, and the locate that finds it.
func receiptRepository(t *testing.T) (string, locate) {
	t.Helper()
	root := gitRepository(t)
	t.Setenv("GIT_COMMITTER_DATE", "2026-10-30T00:00:00Z")
	runGit(t, root, "checkout", "--quiet", "-b", receiptBranch)
	return root, func() (repository, error) { return repository{root: root, dir: root}, nil }
}

// onAReceiptMachine makes the test's process a developer's machine: outside
// any CI job, in a repository on fix/retry-backoff, with a synthetic copy of
// a recorded Claude Code session on that branch under CLAUDE_CONFIG_DIR.
func onAReceiptMachine(t *testing.T) {
	t.Helper()
	root, _ := receiptRepository(t)
	config := t.TempDir()
	for name, value := range map[string]string{claudeConfigVariable: config, "CI": "", "GITHUB_ACTIONS": "", "GITLAB_CI": ""} {
		t.Setenv(name, value)
	}
	receipttest.Write(t, receipttest.OneRequest, config, root, "11111111-0000-4000-8000-000000000001",
		func(receipttest.Line) (string, string) { return root, receiptBranch }, nil)
	t.Chdir(root)
}

// machineEnvironment answers a getenv from values, and empty for any other name.
func machineEnvironment(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func runReceipt(t *testing.T, getenv func(string) string, find locate) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := printReceipt(nil, getenv, func() time.Time { return receiptDay }, find, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// TestAReceiptReadsNothingButCounts plants a marker in every field of two
// synthetic sessions that a person or a model wrote, a tool's input and
// output and a file's contents among them, and in the working directory of
// one, and runs costroid receipt: it counts both sessions' requests, and the
// marker and the repository's path are nowhere in what it prints or logs.
func TestAReceiptReadsNothingButCounts(t *testing.T) {
	const canary = "MARKER-c41b"
	root, find := receiptRepository(t)
	config := t.TempDir()
	inside := filepath.Join(root, canary)
	receipttest.Write(t, receipttest.OneRequest, config, inside, "11111111-0000-4000-8000-000000000001",
		func(receipttest.Line) (string, string) { return inside, receiptBranch }, receipttest.Plant(canary))
	receipttest.Write(t, receipttest.Subagent, config, root, "22222222-0000-4000-8000-000000000002",
		func(receipttest.Line) (string, string) { return root, receiptBranch }, receipttest.Plant(canary))
	code, stdout, stderr := runReceipt(t, machineEnvironment(map[string]string{claudeConfigVariable: config}), find)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for name, text := range map[string]string{"standard output": stdout, "standard error": stderr} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(canary)) || strings.Contains(text, root) {
			t.Errorf("%s holds what a session said, or the working directory:\n%s", name, text)
		}
	}
	stated, err := receipts.Parse(stdout)
	if err != nil {
		t.Fatal(err)
	}
	one, _, _ := receipttest.Requests(t, receipttest.OneRequest)
	four, _, _ := receipttest.Requests(t, receipttest.Subagent)
	var counted int64
	for _, session := range stated.Sessions {
		counted += session.Requests
	}
	if len(stated.Sessions) != 2 || counted != int64(one+four) || *stated.Since != "2026-10-30" {
		t.Errorf("the receipt counts %d requests in %d entries since %v, want %d in 2 since 2026-10-30", counted, len(stated.Sessions), stated.Since, one+four)
	}
	if !strings.Contains(stderr, "costroid: counted 5 requests of 2 Claude Code sessions on branch "+receiptBranch+", from 2026-10-30 00:00 UTC") {
		t.Errorf("standard error:\n%s", stderr)
	}
}

// TestReceiptIsRefusedInsideCI runs costroid receipt where a CI system's
// variables are set, with sessions on the branch it would count: it refuses
// with exit 125 before it looks for a repository, and prints nothing on
// standard output. With none set, the same machine prints a receipt.
func TestReceiptIsRefusedInsideCI(t *testing.T) {
	_, find := receiptRepository(t)
	config := t.TempDir()
	root, _ := find()
	receipttest.Write(t, receipttest.OneRequest, config, root.root, "11111111-0000-4000-8000-000000000001",
		func(receipttest.Line) (string, string) { return root.root, receiptBranch }, nil)
	unreached := func() (repository, error) {
		t.Error("a CI job looked for a repository")
		return repository{}, errors.New("unreached")
	}
	for _, ci := range []map[string]string{{"GITHUB_ACTIONS": "true"}, {"GITLAB_CI": "true"}, {"CI": "true"}, {"CI": "1"}} {
		ci[claudeConfigVariable] = config
		code, stdout, stderr := runReceipt(t, machineEnvironment(ci), unreached)
		if code != 125 || stdout != "" || !strings.HasPrefix(stderr, "costroid: receipt refused: this is a CI job\n") {
			t.Errorf("%v: exit %d, stdout %q, stderr:\n%s", ci, code, stdout, stderr)
		}
	}
	code, stdout, stderr := runReceipt(t, machineEnvironment(map[string]string{claudeConfigVariable: config, "CI": "false"}), find)
	if code != 0 || !strings.HasPrefix(stdout, receipts.Marker+"\n") {
		t.Errorf("on a developer's machine: exit %d, stdout %q, stderr:\n%s", code, stdout, stderr)
	}
}

// TestAReceiptNeedsABranchAndASession runs costroid receipt on a detached
// head, and on a branch no session recorded: each exits 125 with nothing on
// standard output.
func TestAReceiptNeedsABranchAndASession(t *testing.T) {
	root, find := receiptRepository(t)
	config := t.TempDir()
	getenv := machineEnvironment(map[string]string{claudeConfigVariable: config})
	if code, stdout, stderr := runReceipt(t, getenv, find); code != 125 || stdout != "" || !strings.Contains(stderr, "costroid: no Claude Code session on branch "+receiptBranch+"\n") {
		t.Errorf("no session: exit %d, stdout %q, stderr:\n%s", code, stdout, stderr)
	}
	runGit(t, root, "checkout", "--quiet", "--detach")
	if code, stdout, stderr := runReceipt(t, getenv, find); code != 125 || stdout != "" || !strings.HasPrefix(stderr, "costroid: the working tree is on a detached head\n") {
		t.Errorf("a detached head: exit %d, stdout %q, stderr:\n%s", code, stdout, stderr)
	}
}

// refusingTransport fails every request and counts it.
type refusingTransport struct{ calls *atomic.Int64 }

func (r refusingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.calls.Add(1)
	return nil, errors.New("this test allows no request")
}

// TestReceiptNeedsNoNetwork runs costroid receipt with every way out of the
// process that Go's own clients take counted and refused: the default HTTP
// transport, a proxy the environment names, and the resolver. It prints the
// receipt, and nothing was asked of any of them.
func TestReceiptNeedsNoNetwork(t *testing.T) {
	root, find := receiptRepository(t)
	config := t.TempDir()
	receipttest.Write(t, receipttest.Subagent, config, root, "22222222-0000-4000-8000-000000000002",
		func(receipttest.Line) (string, string) { return root, receiptBranch }, nil)
	var calls atomic.Int64
	transport, client, resolver := http.DefaultTransport, http.DefaultClient.Transport, net.DefaultResolver
	t.Cleanup(func() {
		http.DefaultTransport, http.DefaultClient.Transport, net.DefaultResolver = transport, client, resolver
	})
	http.DefaultTransport = refusingTransport{&calls}
	http.DefaultClient.Transport = refusingTransport{&calls}
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		calls.Add(1)
		return nil, errors.New("this test allows no lookup")
	}}
	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	go func() {
		for {
			connection, err := proxy.Accept()
			if err != nil {
				return
			}
			calls.Add(1)
			connection.Close()
		}
	}()
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy"} {
		t.Setenv(name, "http://"+proxy.Addr().String())
	}
	code, stdout, stderr := runReceipt(t, machineEnvironment(map[string]string{claudeConfigVariable: config}), find)
	if code != 0 || !strings.HasPrefix(stdout, receipts.Marker) {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("costroid receipt reached for the network %d times", got)
	}
}

// receiptOnBranch is a receipt for fix/retry-backoff of two sessions, A and
// B, each one entry on Claude Sonnet 5.5. Every name and count is synthetic.
func receiptOnBranch() receipts.Receipt {
	tier := "standard"
	entry := func(session string, output string) receipts.Session {
		return receipts.Session{
			Agent: receipts.ClaudeCode, AgentVersion: "2.1.319", SessionID: session, Model: "claude-sonnet-5-5", ServiceTier: &tier,
			FirstDay: "2026-11-21", LastDay: "2026-11-21", Requests: 3, LargestInput: "1000",
			Tokens: receipts.Tokens{InputUncached: "0", CacheRead: "0", CacheWrite5m: "0", CacheWrite1h: "0", Output: output},
		}
	}
	return receipts.Receipt{
		Schema: receipts.Schema, Branch: receiptBranch, Written: "2026-11-22", Agents: []string{receipts.ClaudeCode},
		Sessions: []receipts.Session{
			entry("aaaaaaaa-0000-4000-8000-00000000000a", "100000"),
			entry("bbbbbbbb-0000-4000-8000-00000000000b", "200000"),
		},
	}
}

// TestASessionTheRunAlsoHoldsCountsOnce reads a receipt beside a rows file
// whose run row names session A: A is left out of the receipt's figure and
// counted as held, and B alone is priced, USD 2 for 200,000 output tokens at
// USD 10 per million. A receipt whose every session the rows hold adds
// nothing.
func TestASessionTheRunAlsoHoldsCountsOnce(t *testing.T) {
	rows := filepath.Join(t.TempDir(), "rows.jsonl")
	run := `{"BillingCurrency":"USD","ListCost":"0.4","Tags":{"costroid/agent":"claude-code","costroid/session_id":"aaaaaaaa-0000-4000-8000-00000000000a"}}` + "\n"
	if err := os.WriteFile(rows, []byte(run), 0o644); err != nil {
		t.Fatal(err)
	}
	stated := &comment.Receipt{}
	if err := priceReceipt(stated, receiptOnBranch(), runSessions(rows)); err != nil {
		t.Fatal(err)
	}
	if stated.Sessions != 1 || stated.Held != 1 || len(stated.Figures) != 1 || stated.Figures[0].Cost.String() != "2" || stated.Figures[0].Rows != 1 {
		t.Errorf("the receipt beside the run's rows: %+v", stated)
	}
	alone := &comment.Receipt{}
	if err := priceReceipt(alone, receiptOnBranch(), runSessions(filepath.Join(t.TempDir(), "absent.jsonl"))); err != nil || alone.Sessions != 2 || alone.Held != 0 || alone.Figures[0].Cost.String() != "3" {
		t.Errorf("the receipt with no rows file: %+v, %v", alone, err)
	}
	held := receiptOnBranch()
	held.Sessions = held.Sessions[:1]
	if err := priceReceipt(&comment.Receipt{}, held, runSessions(rows)); err == nil {
		t.Error("a receipt whose every session the run holds was priced")
	}
}

// receiptReport points a report at a stub whose pull request 7 the author
// octocat opened from fix/retry-backoff of the same repository, and asks for
// receipts as the action's input does.
func receiptReport(t *testing.T) *scmtest.Stub {
	t.Helper()
	stub := reportEnvironment(t, pullRequestEvent())
	stub.Author, stub.HeadBranch, stub.HeadRepository = "octocat", receiptBranch, "example/widgets"
	t.Setenv(receiptsVariable, "true")
	t.Setenv("GITHUB_OUTPUT", filepath.Join(t.TempDir(), "outputs"))
	return stub
}

func postReceipt(t *testing.T, stub *scmtest.Stub, id int64, login string, stated receipts.Receipt) {
	t.Helper()
	body, err := stated.Body()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 22, 8, 30, 0, 0, time.UTC)
	stub.Comments = append(stub.Comments, scmtest.Comment{ID: id, Body: body, Login: login, CreatedAt: at, UpdatedAt: at})
}

// reportOutputs runs the report and answers the check it posted, the comment
// and the step's outputs.
func reportOutputs(t *testing.T, stub *scmtest.Stub) (scmtest.Check, string, string) {
	t.Helper()
	code, _, stderr := execute(t, "report", "--rows", realRows, "--budget-usd", "5")
	if code != 0 || len(stub.Checks) != 1 {
		t.Fatalf("exit %d, %d checks: %s", code, len(stub.Checks), stderr)
	}
	var body string
	for _, posted := range stub.Comments {
		if strings.HasPrefix(posted.Body, comment.Marker) {
			body = posted.Body
		}
	}
	outputs, err := os.ReadFile(os.Getenv("GITHUB_OUTPUT"))
	if err != nil {
		t.Fatal(err)
	}
	return stub.Checks[0], body, string(outputs)
}

// TestAReportStatesTheAuthorsReceiptApartFromTheRun posts the author's receipt
// and a stranger's on a pull request and runs the report: the comment states
// the author's on its own line, priced at the catalog, the check run's object
// is result/0.13 with the receipt under receipts, and the title, the
// conclusion and every output but result are the bytes the same report
// writes with no receipt. Where costroid.yml turns receipts off, the receipt
// is refused.
func TestAReportStatesTheAuthorsReceiptApartFromTheRun(t *testing.T) {
	plain := reportEnvironment(t, pullRequestEvent())
	t.Setenv("GITHUB_OUTPUT", filepath.Join(t.TempDir(), "outputs"))
	before, _, beforeOutputs := reportOutputs(t, plain)

	stub := receiptReport(t)
	stranger := receiptOnBranch()
	stranger.Sessions[1].Tokens.Output = "900000000"
	postReceipt(t, stub, 41, "mallory", stranger)
	postReceipt(t, stub, 40, "octocat", receiptOnBranch())
	check, body, outputs := reportOutputs(t, stub)
	line := "Developer-machine sessions, unchecked: USD 3.00 at list (List-equivalent, not billed), 2 sessions of Claude Code on 2026-11-21 (UTC), as `octocat` stated them in [a comment](https://github.com/example/widgets/pull/7#issuecomment-40) on 2026-11-22. No budget reads this figure, and it is not added to the run's."
	if !strings.Contains(body, line) || !strings.Contains(check.Summary, line) {
		t.Errorf("the comment or the check lacks the receipt's line:\n%s", body)
	}
	for _, want := range []string{`"schema": "costroid.result/0.13"`, `"receipts": [`, `"id": 40,`, `"list_cost": "3",`} {
		if !strings.Contains(check.Text, want) {
			t.Errorf("the check's object lacks %s:\n%s", want, check.Text)
		}
	}
	if check.Title != before.Title || check.Conclusion != before.Conclusion || withoutResult(outputs) != withoutResult(beforeOutputs) {
		t.Errorf("the receipt moved the title, the conclusion or an output:\n%q\n%q\n%s\n%s", check.Title, before.Title, outputs, beforeOutputs)
	}

	off := receiptReport(t)
	off.Files["costroid.yml"] = "comment:\n  receipts: false\n"
	postReceipt(t, off, 40, "octocat", receiptOnBranch())
	refused, body, _ := reportOutputs(t, off)
	if !strings.Contains(body, "No developer-machine figure (receipts_off):") || !strings.Contains(refused.Text, `"reason": "receipts_off"`) || strings.Contains(body, "USD 3.00") {
		t.Errorf("receipts off: the comment reads\n%s", body)
	}
}

// withoutResult is the step's outputs but the result object.
func withoutResult(outputs string) string {
	var kept []string
	for line := range strings.Lines(outputs) {
		if !strings.HasPrefix(line, "result=") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "")
}

// originRows is the recorded rows file with its second row tagged
// costroid/origin, as a job could tag its own rows.
func originRows(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(realRows)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(data), "\n")
	tagged := strings.Replace(lines[1], `"Tags":{`, `"Tags":{"costroid/origin":"developer_machine",`, 1)
	if tagged == lines[1] {
		t.Fatal("the fixture's second row has no Tags")
	}
	lines[1] = tagged
	rows := filepath.Join(t.TempDir(), "costroid-rows.jsonl")
	if err := os.WriteFile(rows, []byte(strings.Join(lines, "")), 0o644); err != nil {
		t.Fatal(err)
	}
	return rows
}

// TestARowsFileThatStatesAnOriginIsRefused hands each reader of a rows file
// a job wrote the recorded rows with one row tagged costroid/origin: the
// report and the fork's follow-up post a neutral check with no figure for
// invalid_row on line 2, and the line that names the tag; costroid policy
// refuses the file and exits 125. No reader leaves the row out and reads
// the rest.
func TestARowsFileThatStatesAnOriginIsRefused(t *testing.T) {
	rows := originRows(t)
	checks := map[string]func() *scmtest.Stub{
		"report": func() *scmtest.Stub { return reportEnvironment(t, pullRequestEvent()) },
		"follow-up": func() *scmtest.Stub {
			stub := followUpEnvironment(t, "stranger/widgets")
			stub.Commits[agentWorkflow] = []time.Time{time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
			return stub
		},
	}
	for command, environment := range checks {
		stub := environment()
		code, _, stderr := execute(t, command, "--rows", rows)
		if code != 0 || len(stub.Checks) != 1 {
			t.Fatalf("%s: exit %d, %d checks: %s", command, code, len(stub.Checks), stderr)
		}
		check := stub.Checks[0]
		if check.Conclusion != "neutral" || !strings.HasPrefix(check.Title, "No figure (invalid_row): line 2 of the rows file") ||
			!strings.Contains(check.Summary, "That row states `costroid/origin`") || !strings.Contains(check.Text, `"reason": "invalid_row"`) || !strings.Contains(check.Text, `"rows": 0`) {
			t.Errorf("%s posted %s, %q:\n%s\n%s", command, check.Conclusion, check.Title, check.Summary, check.Text)
		}
	}
	code, stdout, stderr := execute(t, "policy", "--rows", rows)
	if code != 125 || !strings.Contains(stderr, "costroid/origin") {
		t.Errorf("costroid policy: exit %d, stdout %q, stderr:\n%s", code, stdout, stderr)
	}
}

// withOrigin copies a rows file with its first row tagged costroid/origin.
func withOrigin(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tagged := strings.Replace(string(data), `"Tags":{`, `"Tags":{"costroid/origin":"developer_machine",`, 1)
	if tagged == string(data) {
		t.Fatalf("%s has no Tags to tag", path)
	}
	out := filepath.Join(t.TempDir(), "rows.jsonl")
	if err := os.WriteFile(out, []byte(tagged), 0o644); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestNoCommandSendsARowFromADevelopersMachine gives each command that sends
// or writes a rows file a job wrote that file with one row tagged
// costroid/origin: export to Costroid Platform, export --format, reconcile
// and emit-otlp each exit 125 with the line that names the tag, and the
// service each would reach received no request, and no file was written.
// The same files untagged go through, so each refusal is the tag's.
func TestNoCommandSendsARowFromADevelopersMachine(t *testing.T) {
	refused := "costroid: rows line 1: the row states costroid/origin"
	t.Run("export", func(t *testing.T) {
		stub := newPlatformStub(t, http.StatusAccepted, `{}`)
		env := stub.githubEnv()
		send := func(rows string) (int, string) {
			var stdout, stderr bytes.Buffer
			args := []string{"--rows", rows, "--platform-url", stub.server.URL, "--installation-id", testInstallation}
			code := export(context.Background(), args, stub.server.Client(), func(name string) string { return env[name] }, testPace, &stdout, &stderr)
			return code, stdout.String() + stderr.String()
		}
		if code, said := send(withOrigin(t, exampleRows)); code != exitFailure || !strings.HasPrefix(said, refused) || stub.timesAsked() != 0 {
			t.Errorf("exit %d, %d requests: %s", code, stub.timesAsked(), said)
		}
		if code, said := send(exampleRows); code != 0 || stub.timesAsked() == 0 {
			t.Errorf("the untagged rows: exit %d, %d requests: %s", code, stub.timesAsked(), said)
		}
	})
	t.Run("export --format", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "costs.csv")
		code, stdout, stderr := execute(t, "export", "--rows", withOrigin(t, requestRows), "--format", "focus-csv", "--out", out)
		if _, err := os.Stat(out); code != exitFailure || stdout != "" || !strings.HasPrefix(stderr, refused) || err == nil {
			t.Errorf("exit %d, the file written %t, stdout %q, stderr %q", code, err == nil, stdout, stderr)
		}
		if code, _, stderr := execute(t, "export", "--rows", requestRows, "--format", "focus-csv", "--out", out); code != 0 {
			t.Errorf("the untagged rows: exit %d: %s", code, stderr)
		}
	})
	t.Run("reconcile", func(t *testing.T) {
		doer := &replayDoer{t: t, usage: "usage.json", cost: "cost-at-list.json"}
		code, stdout, stderr := runReconcile(t, doer, environment, "--rows", withOrigin(t, writeLedger(t, 100900)), "--from", "2026-10-14", "--to", "2026-10-14")
		if code != exitFailure || stdout != "" || !strings.HasPrefix(stderr, refused) || doer.requested != 0 {
			t.Errorf("exit %d, %d requests, stdout %q, stderr %q", code, doer.requested, stdout, stderr)
		}
		if code, _, stderr := runReconcile(t, doer, environment, "--rows", writeLedger(t, 100900), "--from", "2026-10-14", "--to", "2026-10-14"); code != 0 || doer.requested == 0 {
			t.Errorf("the untagged rows: exit %d, %d requests: %s", code, doer.requested, stderr)
		}
	})
	t.Run("emit-otlp", func(t *testing.T) {
		var asked atomic.Int64
		collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			asked.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		defer collector.Close()
		getenv := func(name string) string {
			return map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": collector.URL + "/v1/traces"}[name]
		}
		emit := func(rows string) (int, string) {
			var stdout, stderr bytes.Buffer
			code := emitOTLP(context.Background(), []string{"--rows", rows}, getenv, stdio{stdin: strings.NewReader(""), stdout: &stdout, stderr: &stderr})
			return code, stdout.String() + stderr.String()
		}
		if code, said := emit(withOrigin(t, requestRows)); code != exitFailure || !strings.HasPrefix(said, refused) || asked.Load() != 0 {
			t.Errorf("exit %d, %d requests: %s", code, asked.Load(), said)
		}
		if code, said := emit(requestRows); code != 0 || asked.Load() == 0 {
			t.Errorf("the untagged rows: exit %d, %d requests: %s", code, asked.Load(), said)
		}
	})
}
