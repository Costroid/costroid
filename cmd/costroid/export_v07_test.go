package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// pullRequestRun is a pull_request run of the example rows, whose token names
// pull request 1423 in its ref.
func pullRequestRun(stub *platformStub) map[string]string {
	env := stub.githubEnv()
	env["GITHUB_EVENT_NAME"], env["GITHUB_REF"] = "pull_request", "refs/pull/1423/merge"
	return env
}

// otherPullRequest writes the example rows with rows 1 and 2 naming pull
// request 1424, beside the token's 1423.
func otherPullRequest(t *testing.T) string {
	t.Helper()
	return writeRows(t, func(row int, tags map[string]string) {
		if row == 1 || row == 2 {
			tags["costroid/pr"] = "1424"
		}
	})
}

// exportBy runs export against the stub and returns its exit and stderr
// whole, the version line included.
func exportBy(t *testing.T, stub *platformStub, env map[string]string, extra ...string) (int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	args := append([]string{"--platform-url", stub.server.URL, "--installation-id", testInstallation}, extra...)
	code := export(context.Background(), args, stub.server.Client(), func(name string) string { return env[name] }, testPace, stdio{stdout: &stdout, stderr: &stderr})
	if stdout.String() != "" {
		t.Errorf("export printed on stdout: %q", stdout.String())
	}
	return code, stderr.String()
}

// identitiesOf are the run identity and grade each row of the example rows
// goes with, from a run of the example, where the rows named by other are
// another pull request's, and the two windowed rows go as unverified.
func identitiesOf(other ...int) [][2]string {
	var want [][2]string
	for row := range 11 {
		switch {
		case row == 8 || row == 10 || slices.Contains(other, row):
			want = append(want, [2]string{"unverified", "unattributed"})
		default:
			want = append(want, [2]string{"token_verified", "exact"})
		}
	}
	return want
}

// TestExportSendsByVersion07WhenThePlatformStatesIt sends a pull_request
// run's rows to a platform that states integration/v0.7 beside every earlier
// version. Export reads GET /identity once, under v0.7, names v0.7, and sends
// the batch and asks its verdict under v0.7's path. The token names pull
// request 1423, so the two rows that name 1424 go as unverified, and a batch
// never holds a token_verified row of another pull request than the token's.
func TestExportSendsByVersion07WhenThePlatformStatesIt(t *testing.T) {
	stub := newPlatformStub(t, http.StatusAccepted, `{"batch_id":"903114-16873409921-1","status":"accepted"}`)
	code, stderr := exportBy(t, stub, pullRequestRun(stub), "--rows", otherPullRequest(t))
	if code != 0 || len(stub.exports) != 1 || !strings.HasPrefix(stderr, versionLine(contractV07)) {
		t.Fatalf("exit %d, %d exports: %s", code, len(stub.exports), stderr)
	}
	batch := contractV07.path() + "/exports/903114-16873409921-1"
	if path := stub.exports[0].URL.Path; path != batch || len(stub.identity) != 1 || len(stub.verdict) != 1 || stub.verdict[0] != batch {
		t.Errorf("the batch went to %s after %d identity answers, and its verdict was asked at %q; want %s", path, len(stub.identity), stub.verdict, batch)
	}
	if got := sentIdentities(t, stub.bodies[0]); !slices.Equal(got, identitiesOf(1, 2)) {
		t.Errorf("sent %v, want %v", got, identitiesOf(1, 2))
	}
	if line := "costroid: 7 of 11 rows name run 16873409921 of github.com/example/widgets and pull request 1423, so they went as token_verified and the rest as unverified\n"; !strings.Contains(stderr, line) {
		t.Errorf("stderr lacks %q:\n%s", line, stderr)
	}
	if len(stub.audience) != 1 || stub.audience[0] != rowsAudience+testTenant+"/rows" {
		t.Errorf("the run token's audience: %q", stub.audience)
	}

	t.Run("the contract's ingest vectors", func(t *testing.T) {
		for name, want := range map[string][]string{
			"IngestCase.rows-of-the-tokens-pull-request.json":                     {tokenVerified, tokenVerified},
			"IngestCase.a-stated-pull-request-under-pull-request-target.json":     {tokenVerified},
			"rejected/IngestCase.ROW_IDENTITY_MISMATCH.another-pull-request.json": {tokenVerified, unverified, unverified},
		} {
			stub := newPlatformStub(t, http.StatusAccepted, `{"status":"accepted"}`)
			run, rows := ingestCase(t, name)
			code, stderr := exportBy(t, stub, run.env(stub), "--rows", rows)
			if code != 0 || len(stub.exports) != 1 {
				t.Fatalf("%s: exit %d: %s", name, code, stderr)
			}
			sent := sentTags(t, stub.bodies[0])
			var got []string
			for _, tags := range sent {
				got = append(got, tags["costroid/run_identity"])
			}
			if !slices.Equal(got, want) {
				t.Errorf("%s: the rows went as %v, want %v", name, got, want)
			}
			if code := run.ingestRefusal(sent); code != "" {
				t.Errorf("%s: the batch export sent fails %s at a platform of integration/v0.7", name, code)
			}
		}
	})
}

// TestATokenNamesAPullRequestOnlyOnAPullRequestRun sends the example rows by
// integration/v0.7 from a job whose ref is a pull request's merge ref under
// another event than pull_request, and with none. The token names no pull
// request there, so the rows of pull request 1424 are neither downgraded nor
// bound by the ref alone: they go as token_verified, each its own statement.
func TestATokenNamesAPullRequestOnlyOnAPullRequestRun(t *testing.T) {
	for _, event := range []string{"workflow_run", "push", ""} {
		t.Run("event "+event, func(t *testing.T) {
			stub := newPlatformStub(t, http.StatusAccepted, `{"batch_id":"903114-16873409921-1","status":"accepted"}`)
			env := pullRequestRun(stub)
			env["GITHUB_EVENT_NAME"] = event
			code, stderr := exportBy(t, stub, env, "--rows", otherPullRequest(t))
			if code != 0 || len(stub.exports) != 1 || !strings.HasPrefix(stderr, versionLine(contractV07)) {
				t.Fatalf("exit %d, %d exports: %s", code, len(stub.exports), stderr)
			}
			if got := sentIdentities(t, stub.bodies[0]); !slices.Equal(got, identitiesOf()) {
				t.Errorf("sent %v, want %v", got, identitiesOf())
			}
			if strings.Contains(stderr, "and pull request") {
				t.Errorf("export names a pull request from the ref alone: %s", stderr)
			}
		})
	}
}

// TestExportFallsBackFromVersion07 sends the same pull_request run's rows to
// a platform that does not mount integration/v0.7, whose path answers 404
// NOT_FOUND as the contract says. Export asks v0.7, then sends by the newest
// version the platform states, as before it knew v0.7: by v0.6 or by v0.4,
// whose rows are held to their run and repository alone, so the rows of
// pull request 1424 still go as token_verified there.
func TestExportFallsBackFromVersion07(t *testing.T) {
	v06 := speaksBefore06()
	v06[contractV06.path()] = contractV06
	for name, check := range map[string]struct {
		states map[string]contract
		want   contract
	}{
		"a platform of integration/v0.6": {v06, contractV06},
		"a platform of integration/v0.4": {speaksBefore06(), contractV04},
	} {
		t.Run(name, func(t *testing.T) {
			stub := newPlatformStub(t, http.StatusAccepted, `{"batch_id":"903114-16873409921-1","status":"accepted"}`)
			stub.states = check.states
			code, stderr := exportBy(t, stub, pullRequestRun(stub), "--rows", otherPullRequest(t))
			if code != 0 || len(stub.exports) != 1 || !strings.HasPrefix(stderr, versionLine(check.want)) {
				t.Fatalf("exit %d, %d exports: %s", code, len(stub.exports), stderr)
			}
			if path := stub.exports[0].URL.Path; path != check.want.path()+"/exports/903114-16873409921-1" {
				t.Errorf("the batch went to %s", path)
			}
			if got := sentIdentities(t, stub.bodies[0]); !slices.Equal(got, identitiesOf()) {
				t.Errorf("sent %v, want %v", got, identitiesOf())
			}
			if line := "costroid: 9 of 11 rows name run 16873409921 of github.com/example/widgets, so they went as token_verified and the rest as unverified\n"; !strings.Contains(stderr, line) {
				t.Errorf("stderr lacks %q:\n%s", line, stderr)
			}
		})
	}
}

// attemptPlatform is a platform of integration/v0.7 that keeps the attempt
// each batch's token_verified rows came from, and refuses a restatement from
// any other attempt with EXPORT_BATCH_RUN_BOUND, as the contract does. The
// token GitHub issues names the attempt the test sets.
type attemptPlatform struct {
	server  *httptest.Server
	attempt string

	mu      sync.Mutex
	batches map[string]string
	sent    []string
}

func newAttemptPlatform(t *testing.T) *attemptPlatform {
	t.Helper()
	p := &attemptPlatform{batches: map[string]string{}}
	p.server = httptest.NewTLSServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.server.Close)
	return p
}

func (p *attemptPlatform) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	batch, put := strings.CutPrefix(r.URL.Path, contractV07.path()+"/exports/")
	switch {
	case r.URL.Path == "/oidc":
		w.Write([]byte(`{"value":"run-token-of-attempt-` + p.attempt + `"}`))
	case r.URL.Path == contractV07.path()+"/identity":
		stated, _ := json.Marshal(contractV07)
		w.Write([]byte(`{"contract":` + string(stated) + `,"tenant":{"id":"` + testTenant + `"}}`))
	case put && r.Method == http.MethodPut:
		attempt := strings.TrimPrefix(r.Header.Get("Costroid-Run-Token"), "run-token-of-attempt-")
		p.sent = append(p.sent, batch)
		if bound, held := p.batches[batch]; held && bound != attempt {
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"error":{"code":"EXPORT_BATCH_RUN_BOUND","message":"This batch belongs to attempt ` + bound + ` of its run."}}`))
			return
		}
		p.batches[batch] = attempt
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"status":"accepted"}`))
	case put:
		w.Write([]byte(ingested(11, 0).body))
	default:
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(contractExamples["Error.not-mounted.json"].body))
	}
}

// TestALaterAttemptSendsABatchOfItsOwn sends one run's rows from its first
// attempt and again from its second, against a platform that binds a batch
// to its attempt. The second attempt sends a batch of its own, never the
// first attempt's, so both stand, as the contract's restatement vector of a
// later attempt requires. A job that names one batch id for both attempts
// meets EXPORT_BATCH_RUN_BOUND, keeps its rows, says the batch belongs to the
// attempt that sent it, and says to drop --batch-id on GitHub Actions.
func TestALaterAttemptSendsABatchOfItsOwn(t *testing.T) {
	var vector struct {
		BatchID string `json:"batch_id"`
		Run     struct {
			Repository string `json:"repository"`
			RunID      string `json:"run_id"`
			RunAttempt string `json:"run_attempt"`
		} `json:"run"`
		Refusal string `json:"refusal"`
	}
	if err := json.Unmarshal(example(t, "rejected/RestatementCase.EXPORT_BATCH_RUN_BOUND.a-later-attempt.json"), &vector); err != nil {
		t.Fatal(err)
	}
	platform := newAttemptPlatform(t)
	env := map[string]string{
		"GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY": vector.Run.Repository, "GITHUB_REPOSITORY_ID": "903114",
		"GITHUB_RUN_ID": vector.Run.RunID, "ACTIONS_ID_TOKEN_REQUEST_URL": platform.server.URL + "/oidc?api-version=2.0",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN": testRequestToken, platformTokenVariable: testIntegrationToken,
	}
	send := func(attempt string, extra ...string) (int, string) {
		t.Helper()
		platform.attempt, env["GITHUB_RUN_ATTEMPT"] = attempt, attempt
		var stdout, stderr bytes.Buffer
		args := append([]string{"--rows", exampleRows, "--platform-url", platform.server.URL, "--installation-id", testInstallation}, extra...)
		code := export(context.Background(), args, platform.server.Client(), func(name string) string { return env[name] }, testPace, stdio{stdout: &stdout, stderr: &stderr})
		return code, stdout.String() + stderr.String()
	}
	for _, attempt := range []string{"1", vector.Run.RunAttempt} {
		if code, said := send(attempt); code != 0 || strings.Contains(said, exportBatchRunBound) {
			t.Fatalf("attempt %s: exit %d: %s", attempt, code, said)
		}
	}
	want := []string{"903114-16873409921-1", "903114-16873409921-2"}
	if !slices.Equal(platform.sent, want) || platform.sent[1] == vector.BatchID || len(platform.batches) != 2 {
		t.Errorf("the attempts sent %q, want %q, each attempt's batch its own", platform.sent, want)
	}

	rows, err := os.ReadFile(exampleRows)
	if err != nil {
		t.Fatal(err)
	}
	code, said := send(vector.Run.RunAttempt, "--batch-id", vector.BatchID)
	line := "costroid: Costroid Platform refused batch " + vector.BatchID + ": 409 " + vector.Refusal + ". The batch holds token_verified rows, so it belongs to the run and the attempt that sent it, and only that attempt restates it. Nothing was stored, and " + exampleRows + " is kept. On GitHub Actions export names a batch for each attempt itself, so drop --batch-id there\n"
	if code != exitRejected || !strings.HasSuffix(said, line) || strings.Contains(said, "belongs to attempt") {
		t.Errorf("a later attempt that names the first attempt's batch: exit %d: %s", code, said)
	}
	if after, err := os.ReadFile(exampleRows); err != nil || !bytes.Equal(after, rows) {
		t.Errorf("the rows file changed: %v", err)
	}
}

// vectorRun is the run token of one of the contract's ingest vectors.
type vectorRun struct {
	Repository   string `json:"repository"`
	RepositoryID string `json:"repository_id"`
	RunID        string `json:"run_id"`
	RunAttempt   string `json:"run_attempt"`
	Ref          string `json:"ref"`
	EventName    string `json:"event_name"`
}

// env is the GitHub Actions job whose token the vector's run token is.
func (r vectorRun) env(stub *platformStub) map[string]string {
	env := stub.githubEnv()
	for name, value := range map[string]string{
		"GITHUB_REPOSITORY": r.Repository, "GITHUB_REPOSITORY_ID": r.RepositoryID, "GITHUB_RUN_ID": r.RunID,
		"GITHUB_RUN_ATTEMPT": r.RunAttempt, "GITHUB_REF": r.Ref, "GITHUB_EVENT_NAME": r.EventName,
	} {
		env[name] = value
	}
	return env
}

// ingestRefusal is the code a platform of integration/v0.7 fails a batch on
// for its rows' identities, as its row checks read them under this run
// token, or empty.
func (r vectorRun) ingestRefusal(rows []map[string]string) string {
	match := pullRef.FindStringSubmatch(r.Ref)
	for _, tags := range rows {
		switch {
		case tags["costroid/run_identity"] != tokenVerified:
		case tags["costroid/workflow_run_id"] != r.RunID, tags["costroid/repo"] != "github.com/"+strings.ToLower(r.Repository),
			r.EventName == "pull_request" && match != nil && tags["costroid/pr"] != match[1]:
			return "ROW_IDENTITY_MISMATCH"
		}
	}
	return ""
}

// ingestCase reads one of the contract's ingest vectors as a run token and a
// rows file: the first example row, once for each of the vector's rows, with
// that row's Tags.
func ingestCase(t *testing.T, name string) (vectorRun, string) {
	t.Helper()
	var vector struct {
		Run  vectorRun           `json:"run"`
		Rows []map[string]string `json:"rows"`
	}
	if err := json.Unmarshal(example(t, name), &vector); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exampleRows)
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := bytes.Cut(data, []byte("\n"))
	var out bytes.Buffer
	for _, tags := range vector.Rows {
		var row map[string]any
		if err := json.Unmarshal(first, &row); err != nil {
			t.Fatal(err)
		}
		row["Tags"] = tags
		line, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(append(line, '\n'))
	}
	path := filepath.Join(t.TempDir(), "rows.jsonl")
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return vector.Run, path
}

// sentTags reads the Tags cell of each row in a batch's CSV.
func sentTags(t *testing.T, body []byte) []map[string]string {
	t.Helper()
	records, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil || len(records) < 2 {
		t.Fatalf("the body is not a CSV of rows: %v", err)
	}
	column := slices.Index(records[0], "Tags")
	var rows []map[string]string
	for _, record := range records[1:] {
		var tags map[string]string
		if err := json.Unmarshal([]byte(record[column]), &tags); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, tags)
	}
	return rows
}
