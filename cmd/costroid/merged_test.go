package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Costroid/costroid/internal/scm/scmtest"
)

// The push of the tests: a merge queue's group of two pull requests, 1481 and
// 1482, whose commits groupCommit and tipCommit reached main in one push.
const (
	groupCommit = "3a7d0c4e91b25f68d0e3c7a1b9f4e2d6c8a05b13"
	tipCommit   = "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d"
	branchWork  = "5b0e2f7c1d9a84e36f2c0b1a7d4e9f3c8a6b2d10"
	otherMerge  = "c0e4a91d27b3f58e6a0d4c19b7e2f3a8d5c6b140"
	mergesPath  = "/api/integration/v0.5/merges/"
)

// contractCopy is a version's openapi.json of Costroid/costroid-schemas,
// copied: integration/v0.5 at 4cf76ee, integration/v0.6 at 338c5ec, and
// integration/v0.7 at 4f1bc81.
func contractCopy(version contract) string {
	return filepath.Join("testdata", "integration-v"+version.Version+".openapi.json")
}

// contractSchema compiles one schema of a version's copy of the contract.
func contractSchema(t *testing.T, version contract, name string) *jsonschema.Schema {
	t.Helper()
	document, err := jsonschema.UnmarshalJSON(strings.NewReader(readFile(t, contractCopy(version))))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("openapi.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("openapi.json#/components/schemas/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

// mergedVersionLine is the line merged prints on standard error for the
// version it sends by.
func mergedVersionLine(version contract) string {
	return "costroid: Costroid Platform states integration/v" + version.Version + ", so merged sends the merge facts by it\n"
}

var (
	groupMerged = time.Date(2026, 11, 21, 10, 7, 50, 0, time.UTC)
	tipMerged   = time.Date(2026, 11, 21, 10, 7, 52, 0, time.UTC)
)

// mergeStub answers as Costroid Platform, by integration/v0.5 or v0.6 where
// states says so, and as GitHub's OIDC token endpoint. A PUT it holds no
// answer for is graded as the version of its path grades it: token_verified
// when the run token is the test's and the fact's commit is the token's,
// which is the tip, and by v0.6 when the token's ref_protected is "true" as
// well. Each graded answer is held to that version's MergeFact.
type mergeStub struct {
	t      *testing.T
	server *httptest.Server
	states map[string]contract
	// facts are each version's MergeFact.
	facts map[contract]*jsonschema.Schema
	// unprotected sets the run token's ref_protected to "false", and ref is
	// the branch the token names.
	unprotected bool
	ref         string
	// answers, by merge id, answer each PUT of that id in turn in place of
	// the grading. A turn of status 0 drops the connection.
	answers map[string][]turn
	// held are the documents the stub stores, by merge id.
	held map[string]mergeDocument

	mu       sync.Mutex
	requests []string
	puts     []*http.Request
	bodies   [][]byte
	audience []string
}

func newMergeStub(t *testing.T) *mergeStub {
	t.Helper()
	states := speaksBefore06()
	states[contractV05.path()] = contractV05
	facts := map[contract]*jsonschema.Schema{}
	for _, version := range statementContracts {
		facts[version] = contractSchema(t, version, "MergeFact")
	}
	stub := &mergeStub{t: t, states: states, facts: facts, ref: "refs/heads/main", answers: map[string][]turn{}, held: map[string]mergeDocument{}}
	stub.server = httptest.NewTLSServer(http.HandlerFunc(stub.serve))
	t.Cleanup(stub.server.Close)
	return stub
}

// newV07MergeStub is a platform that states integration/v0.7 and v0.6 under
// their paths, beside every earlier version.
func newV07MergeStub(t *testing.T) *mergeStub {
	t.Helper()
	stub := newV06MergeStub(t)
	stub.states[contractV07.path()] = contractV07
	return stub
}

// newV06MergeStub is a platform that states integration/v0.6 under its path,
// beside every earlier version.
func newV06MergeStub(t *testing.T) *mergeStub {
	t.Helper()
	stub := newMergeStub(t)
	stub.states[contractV06.path()] = contractV06
	return stub
}

func (s *mergeStub) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r.Method+" "+r.URL.Path)
	var prefix, rest string
	for path := range s.states {
		if after, found := strings.CutPrefix(r.URL.Path, path+"/"); found {
			prefix, rest = path, "/"+after
		}
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/oidc" && r.Header.Get("Authorization") == "Bearer "+testRequestToken:
		s.audience = append(s.audience, r.URL.Query().Get("audience"))
		w.Write([]byte(`{"value":"` + testRunToken + `"}`))
	case prefix == "":
		http.Error(w, "", http.StatusNotFound)
	case r.Method == http.MethodGet && rest == "/identity":
		stated, _ := json.Marshal(s.states[prefix])
		w.Write([]byte(`{"contract":` + string(stated) + `,"tenant":{"id":"` + testTenant + `","name":"Example Inc","region":"eu"}}`))
	case r.Method == http.MethodPut && strings.HasPrefix(rest, "/merges/") && slices.ContainsFunc(statementContracts, func(c contract) bool { return c.path() == prefix }):
		body := new(bytes.Buffer)
		body.ReadFrom(r.Body)
		s.puts = append(s.puts, r)
		s.bodies = append(s.bodies, body.Bytes())
		version := s.states[prefix]
		s.answer(w, r, version, strings.TrimPrefix(rest, "/merges/"), body.Bytes())
	default:
		http.Error(w, "", http.StatusNotFound)
	}
}

func (s *mergeStub) answer(w http.ResponseWriter, r *http.Request, version contract, id string, body []byte) {
	if turns := s.answers[id]; len(turns) > 0 {
		s.answers[id] = turns[1:]
		if turns[0].status == 0 {
			panic(http.ErrAbortHandler)
		}
		w.WriteHeader(turns[0].status)
		w.Write([]byte(turns[0].body))
		return
	}
	var document mergeDocument
	if json.Unmarshal(body, &document) != nil {
		http.Error(w, `{"error":{"code":"MERGE_FACT_INVALID","message":"The body is not JSON."}}`, http.StatusUnprocessableEntity)
		return
	}
	held, seen := s.held[id]
	if seen && held != document {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":{"code":"MERGE_FACT_CONFLICT","message":"` + document.Repo + ` pull request already holds a different fact."}}`))
		return
	}
	grade := unverified
	if r.Header.Get("Costroid-Run-Token") == testRunToken && document.MergeCommitSHA == tipCommit && (version == contractV05 || !s.unprotected) {
		grade = tokenVerified
	}
	s.held[id] = document
	status := http.StatusCreated
	if seen {
		status = http.StatusOK
	}
	fact := map[string]any{
		"merge_id": id, "repo": document.Repo, "pull_request": document.PullRequest,
		"merged_at": document.MergedAt, "merge_commit_sha": document.MergeCommitSHA, "run_identity": grade,
	}
	if version != contractV05 {
		fact["ref"] = nil
		if grade == tokenVerified {
			fact["ref"] = s.ref
		}
	}
	stored, _ := json.Marshal(fact)
	instance, _ := jsonschema.UnmarshalJSON(bytes.NewReader(stored))
	if err := s.facts[version].Validate(instance); err != nil {
		s.t.Errorf("the stub answers what integration/v%s's MergeFact refuses: %v\n%s", version.Version, err, stored)
	}
	w.WriteHeader(status)
	w.Write(stored)
}

// mergeHost is GitHub with the group's two pull requests, a pull request
// still open on the tip, and one whose branch held groupCommit and that
// merged as another commit.
func mergeHost(t *testing.T) *scmtest.Stub {
	t.Helper()
	host := scmtest.New(t)
	host.Pulls = []scmtest.Pull{
		{Number: 1481, State: "closed", MergedAt: groupMerged, MergeCommitSHA: groupCommit},
		{Number: 1482, State: "closed", MergedAt: tipMerged, MergeCommitSHA: tipCommit},
		{Number: 1490, State: "open", MergeCommitSHA: tipCommit, Commits: []string{branchWork, tipCommit}},
		{Number: 1470, State: "closed", MergedAt: groupMerged, MergeCommitSHA: otherMerge, Commits: []string{groupCommit}},
	}
	return host
}

// pushEnv is a GitHub Actions job with id-token: write on a push of commits
// to example/widgets, repository 903114, whose tip is tipCommit.
func (s *mergeStub) pushEnv(t *testing.T, host *scmtest.Stub, commits ...string) map[string]string {
	t.Helper()
	listed := []map[string]string{}
	for _, commit := range commits {
		listed = append(listed, map[string]string{"id": commit})
	}
	event, _ := json.Marshal(map[string]any{"ref": "refs/heads/main", "after": tipCommit, "commits": listed})
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, event, 0o644); err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"GITHUB_ACTIONS":                 "true",
		"GITHUB_EVENT_NAME":              "push",
		"GITHUB_EVENT_PATH":              path,
		"GITHUB_REPOSITORY":              testRepository,
		"GITHUB_REPOSITORY_ID":           "903114",
		"GITHUB_RUN_ID":                  "16990415532",
		"GITHUB_RUN_ATTEMPT":             "1",
		"GITHUB_SHA":                     tipCommit,
		"GITHUB_REF":                     s.ref,
		"GITHUB_REF_PROTECTED":           strconv.FormatBool(!s.unprotected),
		"GITHUB_API_URL":                 host.Server.URL,
		"GITHUB_TOKEN":                   host.Token,
		"ACTIONS_ID_TOKEN_REQUEST_URL":   s.server.URL + "/oidc?api-version=2.0",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN": testRequestToken,
		platformTokenVariable:            testIntegrationToken,
	}
}

// merged runs merged against the stub, and fails the test on any output that
// holds a token.
func (s *mergeStub) merged(t *testing.T, env map[string]string, extra ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	args := append([]string{"--platform-url", s.server.URL, "--installation-id", testInstallation}, extra...)
	code := merged(context.Background(), args, s.server.Client(), func(name string) string { return env[name] }, testPace, &stdout, &stderr)
	for _, secret := range []string{testIntegrationToken, testRunToken, testRequestToken, env["GITHUB_TOKEN"]} {
		if secret != "" && strings.Contains(stdout.String()+stderr.String(), secret) {
			t.Fatalf("the output holds a token: %s%s", stdout.String(), stderr.String())
		}
	}
	return code, stdout.String(), stderr.String()
}

// mergeDocumentSchema is integration/v0.5's MergeDocument, compiled from the
// contract's copy.
func mergeDocumentSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	return contractSchema(t, contractV05, "MergeDocument")
}

// validMergeDocument holds a document merged sent or printed to the
// contract's MergeDocument.
func validMergeDocument(t *testing.T, schema *jsonschema.Schema, body []byte) {
	t.Helper()
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Errorf("the document breaks the contract's MergeDocument: %v\n%s", err, body)
	}
}

// TestMergeDocumentSchemaRefusesTheContractsRejectedDocuments holds the
// compiled schema to the published contract's own examples, so a schema
// that read nothing cannot pass the documents merged sends.
func TestMergeDocumentSchemaRefusesTheContractsRejectedDocuments(t *testing.T) {
	schema := mergeDocumentSchema(t)
	for _, body := range []string{
		`{"repo":"https://github.com/example/widgets","pull_request":1482,"merged_at":"2026-11-21T10:07:52Z","merge_commit_sha":"9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d"}`,
		`{"repo":"github.com/example/widgets","pull_request":"1482","merged_at":"2026-11-21T10:07:52Z","merge_commit_sha":"9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d"}`,
		`{"repo":"github.com/example/widgets","pull_request":1482,"merged_at":"2026-11-21T10:07:52.418Z","merge_commit_sha":"9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d"}`,
		`{"repo":"github.com/example/widgets","pull_request":1482,"merged_at":"2026-11-21T10:07:52Z","merge_commit_sha":"9f2c6e1"}`,
		`{"repo":"github.com/example/widgets","pull_request":1482,"merged_at":"2026-11-21T10:07:52Z","merge_commit_sha":"9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d","head_sha":"5b0e2f7c1d9a84e36f2c0b1a7d4e9f3c8a6b2d10"}`,
	} {
		instance, err := jsonschema.UnmarshalJSON(strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if schema.Validate(instance) == nil {
			t.Errorf("the schema takes a document the contract rejects: %s", body)
		}
	}
}

// mergeDocumentOf is the document merged sends for a pull request of the
// group.
func mergeDocumentOf(number int, merged time.Time, commit string) mergeDocument {
	return mergeDocument{Repo: "github.com/" + testRepository, PullRequest: number, MergedAt: merged.Format(mergedInstant), MergeCommitSHA: commit}
}

// TestMergedSendsOneFactPerMergedPullRequest sends a merge queue's push and
// holds each request to the contract: one PUT per pull request the push's
// commits closed, under the merge id of the repository id and the number,
// each body a valid MergeDocument, each with the integration token, the
// installation and the run token of the rows audience. An open pull request
// and one that merged as another commit get none. --json prints one object
// per fact with its status and run_identity.
func TestMergedSendsOneFactPerMergedPullRequest(t *testing.T) {
	schema := mergeDocumentSchema(t)
	stub, host := newMergeStub(t), mergeHost(t)
	code, stdout, stderr := stub.merged(t, stub.pushEnv(t, host, groupCommit, tipCommit))
	if code != 0 || len(stub.puts) != 2 {
		t.Fatalf("exit %d, %d facts sent: %s%s", code, len(stub.puts), stdout, stderr)
	}
	want := []struct {
		id       string
		document mergeDocument
	}{
		{"903114-1481", mergeDocumentOf(1481, groupMerged, groupCommit)},
		{"903114-1482", mergeDocumentOf(1482, tipMerged, tipCommit)},
	}
	for i, fact := range want {
		request, body := stub.puts[i], stub.bodies[i]
		if request.URL.Path != mergesPath+fact.id {
			t.Errorf("fact %d went to %s, want %s", i, request.URL.Path, mergesPath+fact.id)
		}
		validMergeDocument(t, schema, body)
		var sent mergeDocument
		if err := json.Unmarshal(body, &sent); err != nil || sent != fact.document {
			t.Errorf("fact %d is %s, want %+v", i, body, fact.document)
		}
		for name, value := range map[string]string{
			"Authorization":            "Bearer " + testIntegrationToken,
			"Content-Type":             "application/json",
			"Costroid-Installation-Id": testInstallation,
			"Costroid-Run-Token":       testRunToken,
		} {
			if got := request.Header.Get(name); got != value {
				t.Errorf("fact %d, %s: %q, want %q", i, name, got, value)
			}
		}
	}
	if len(stub.audience) != 1 || stub.audience[0] != rowsAudience+testTenant+"/rows" {
		t.Errorf("the run token's audience: %q", stub.audience)
	}
	for _, line := range []string{
		"costroid: merge 903114-1481, pull request 1481 of github.com/example/widgets: stored as unverified\n",
		"costroid: merge 903114-1482, pull request 1482 of github.com/example/widgets: stored as token_verified\n",
	} {
		if !strings.Contains(stderr, line) {
			t.Errorf("stderr lacks %q:\n%s", line, stderr)
		}
	}
	if !strings.HasPrefix(stderr, mergedVersionLine(contractV05)) || stdout != "" {
		t.Errorf("stdout %q, stderr %q", stdout, stderr)
	}

	stub = newMergeStub(t)
	code, stdout, _ = stub.merged(t, stub.pushEnv(t, host, groupCommit, tipCommit), "--json")
	var printed []mergedAnswer
	for _, line := range strings.Split(strings.TrimSuffix(stdout, "\n"), "\n") {
		var answer mergedAnswer
		if err := json.Unmarshal([]byte(line), &answer); err != nil {
			t.Fatalf("--json printed %q: %v", line, err)
		}
		printed = append(printed, answer)
	}
	wantJSON := []mergedAnswer{
		{MergeID: "903114-1481", PullRequest: 1481, Status: http.StatusCreated, RunIdentity: unverified},
		{MergeID: "903114-1482", PullRequest: 1482, Status: http.StatusCreated, RunIdentity: tokenVerified},
	}
	if code != 0 || !slices.Equal(printed, wantJSON) {
		t.Errorf("--json: exit %d, printed %+v, want %+v", code, printed, wantJSON)
	}
}

// TestMergedSendsNothingForACommitThatClosedNone holds a push that closed no
// pull request, and a run on another event, to one line, exit 0, and no
// request to the platform.
func TestMergedSendsNothingForACommitThatClosedNone(t *testing.T) {
	stub, host := newMergeStub(t), scmtest.New(t)
	host.Pulls = []scmtest.Pull{{Number: 1490, State: "open", MergeCommitSHA: tipCommit, Commits: []string{tipCommit}}}
	code, stdout, stderr := stub.merged(t, stub.pushEnv(t, host, tipCommit))
	if want := "costroid: the push to " + tipCommit + " closed no pull request, so no merge fact was sent\n"; code != 0 || stderr != want || stdout != "" {
		t.Errorf("exit %d, stdout %q, stderr %q, want %q", code, stdout, stderr, want)
	}
	if len(stub.requests) != 0 {
		t.Errorf("the platform was asked %q", stub.requests)
	}

	env := stub.pushEnv(t, mergeHost(t), groupCommit, tipCommit)
	env["GITHUB_EVENT_NAME"] = "pull_request"
	code, stdout, stderr = stub.merged(t, env)
	if want := "costroid: merged sends the merge facts of a push, and this run is on pull_request, so nothing was sent\n"; code != 0 || stdout != "" || stderr != want {
		t.Errorf("a pull_request run: exit %d, stdout %q, stderr %q, want %q", code, stdout, stderr, want)
	}
	if len(stub.requests) != 0 {
		t.Errorf("the platform was asked %q", stub.requests)
	}
}

// TestMergedSaysWhyAFactIsUnverified holds the two causes the contract gives
// to their lines: a fact of the group whose commit is not the tip the run
// token names, and a job without id-token: write, whose facts go without a
// run token. A token_verified fact gets no such line.
func TestMergedSaysWhyAFactIsUnverified(t *testing.T) {
	stub, host := newMergeStub(t), mergeHost(t)
	code, stdout, stderr := stub.merged(t, stub.pushEnv(t, host, groupCommit, tipCommit))
	notTip := "costroid: merge 903114-1481 reads unverified because its commit " + groupCommit + " is not the tip of the push, " + tipCommit + ", which the run token names, as in a merge queue's group\n"
	if code != 0 || stdout != "" || !strings.Contains(stderr, notTip) {
		t.Errorf("exit %d, stdout %q, stderr lacks %q:\n%s", code, stdout, notTip, stderr)
	}
	if strings.Contains(stderr, "merge 903114-1482 reads unverified") {
		t.Errorf("the token_verified fact is said to be unverified:\n%s", stderr)
	}

	stub = newMergeStub(t)
	env := stub.pushEnv(t, host, tipCommit)
	delete(env, "ACTIONS_ID_TOKEN_REQUEST_URL")
	delete(env, "ACTIONS_ID_TOKEN_REQUEST_TOKEN")
	code, stdout, stderr = stub.merged(t, env)
	want := "costroid: merge 903114-1482, pull request 1482 of github.com/example/widgets: stored as unverified\n" +
		"costroid: merge 903114-1482 reads unverified because no run token went with it: give the job id-token: write, so the run token names the commit\n"
	if code != 0 || stdout != "" || stderr != mergedVersionLine(contractV05)+want {
		t.Errorf("without id-token: exit %d, stdout %q, stderr %q, want %q", code, stdout, stderr, want)
	}
	if len(stub.puts) != 1 || stub.puts[0].Header.Get("Costroid-Run-Token") != "" || len(stub.audience) != 0 {
		t.Errorf("without id-token: %d facts, run token %q, audiences %q", len(stub.puts), stub.puts[0].Header.Get("Costroid-Run-Token"), stub.audience)
	}
}

// TestMergedDoesNotSendAConflictAgain holds 409 MERGE_FACT_CONFLICT to one
// request, a line that names the pull request, and exit 1, while the push's
// other fact is still sent.
func TestMergedDoesNotSendAConflictAgain(t *testing.T) {
	stub, host := newMergeStub(t), mergeHost(t)
	stub.held["903114-1482"] = mergeDocumentOf(1482, tipMerged.Add(-time.Hour), tipCommit)
	code, stdout, stderr := stub.merged(t, stub.pushEnv(t, host, groupCommit, tipCommit))
	want := "costroid: Costroid Platform refused merge 903114-1482: 409 MERGE_FACT_CONFLICT. Pull request 1482 of github.com/example/widgets already holds a different fact, and a pull request merges once, so merged does not send it again\n"
	if code != exitRejected || stdout != "" || !strings.HasPrefix(stderr, mergedVersionLine(contractV05)) || !strings.HasSuffix(stderr, want) {
		t.Errorf("exit %d, stdout %q, stderr %q, want it to end %q", code, stdout, stderr, want)
	}
	var paths []string
	for _, request := range stub.puts {
		paths = append(paths, request.URL.Path)
	}
	if !slices.Equal(paths, []string{mergesPath + "903114-1481", mergesPath + "903114-1482"}) {
		t.Errorf("sent %q", paths)
	}
	if !strings.Contains(stderr, "merge 903114-1481, pull request 1481 of github.com/example/widgets: stored as unverified\n") {
		t.Errorf("the other fact was not stored:\n%s", stderr)
	}
}

// TestMergedRefusesAPlatformBeforeV05 sends nothing, and mints no run token,
// to a platform that serves no integration/v0.5, or whose v0.5 path states
// another version.
func TestMergedRefusesAPlatformBeforeV05(t *testing.T) {
	for name, check := range map[string]struct {
		states map[string]contract
		want   string
	}{
		"a platform of integration/v0.4": {
			speaksBefore06(),
			"costroid: Costroid Platform does not serve integration/v0.5, and a merge fact needs integration/v0.5 or later, so nothing was sent\n",
		},
		"a v0.5 path that states v0.4": {
			map[string]contract{contractV05.path(): contractV04},
			"costroid: the identity answer of Costroid Platform under /api/integration/v0.5 does not state integration/v0.5\n" + seeTroubleshooting,
		},
	} {
		t.Run(name, func(t *testing.T) {
			stub := newMergeStub(t)
			stub.states = check.states
			code, stdout, stderr := stub.merged(t, stub.pushEnv(t, mergeHost(t), groupCommit, tipCommit))
			if code != exitFailure || stdout != "" || stderr != check.want {
				t.Errorf("exit %d, stdout %q, stderr %q, want %q", code, stdout, stderr, check.want)
			}
			if len(stub.puts) != 0 || len(stub.audience) != 0 {
				t.Errorf("%d facts sent, run token audiences %q", len(stub.puts), stub.audience)
			}
		})
	}
}

// TestMergedNeverPrintsTheToken runs merged through every kind of answer, from
// a platform whose refusals echo every token, and from a GitHub that refuses.
// The stub's merged fails the test on any output that holds a token.
func TestMergedNeverPrintsTheToken(t *testing.T) {
	echo := testIntegrationToken + " " + testRunToken + " " + testRequestToken + " stub-token"
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusInternalServerError} {
		for _, code := range []string{"RUN_TOKEN_INVALID", "INSTALLATION_NOT_BOUND", mergeFactConflict, "NOT_A_MERGE_CODE"} {
			stub := newMergeStub(t)
			stub.answers["903114-1482"] = []turn{{status, `{"error":{"code":"` + code + `","message":"` + echo + `"}}`}}
			exit, stdout, stderr := stub.merged(t, stub.pushEnv(t, mergeHost(t), tipCommit))
			if exit == exitFailure || len(stub.puts) == 0 {
				t.Errorf("%d %s: exit %d, %d facts sent: %s%s", status, code, exit, len(stub.puts), stdout, stderr)
			}
		}
	}
	stub := newMergeStub(t)
	code, _, _ := stub.merged(t, stub.pushEnv(t, mergeHost(t), tipCommit), "--json")
	if code != 0 {
		t.Errorf("--json: exit %d", code)
	}
	host := mergeHost(t)
	host.Fail["GET /commits/"] = http.StatusForbidden
	stub = newMergeStub(t)
	if code, _, stderr := stub.merged(t, stub.pushEnv(t, host, tipCommit)); code != exitFailure || !strings.Contains(stderr, "403") {
		t.Errorf("a refused GitHub: exit %d, stderr %q", code, stderr)
	}
}

// TestMergedDryRunSendsNothing prints each fact's merge id and document, each
// a valid MergeDocument, and reaches no platform, with or without the
// platform's flags and token.
func TestMergedDryRunSendsNothing(t *testing.T) {
	schema := mergeDocumentSchema(t)
	stub, host := newMergeStub(t), mergeHost(t)
	env := stub.pushEnv(t, host, groupCommit, tipCommit)
	delete(env, platformTokenVariable)
	var stdout, stderr bytes.Buffer
	code := merged(context.Background(), []string{"--dry-run"}, stub.server.Client(), func(name string) string { return env[name] }, testPace, &stdout, &stderr)
	if code != 0 || stderr.String() != "costroid: --dry-run printed 2 merge facts and sent none\n" {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	var ids []string
	for _, line := range strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n") {
		var printed struct {
			MergeID  string          `json:"merge_id"`
			Document json.RawMessage `json:"document"`
		}
		if err := json.Unmarshal([]byte(line), &printed); err != nil {
			t.Fatalf("--dry-run printed %q: %v", line, err)
		}
		validMergeDocument(t, schema, printed.Document)
		ids = append(ids, printed.MergeID)
	}
	if !slices.Equal(ids, []string{"903114-1481", "903114-1482"}) {
		t.Errorf("--dry-run printed %q", ids)
	}
	code, _, _ = stub.merged(t, stub.pushEnv(t, host, groupCommit, tipCommit), "--dry-run")
	if code != 0 || len(stub.requests) != 0 {
		t.Errorf("with the platform's flags: exit %d, the platform was asked %q", code, stub.requests)
	}
}

// TestMergedSendsAgainOnAServerError sends the same document again after a
// 5xx and after a dropped connection, and stops at a refusal below 500.
func TestMergedSendsAgainOnAServerError(t *testing.T) {
	stub := newMergeStub(t)
	stub.answers["903114-1482"] = []turn{{http.StatusServiceUnavailable, ""}, {0, ""}, {http.StatusBadGateway, ""}}
	code, stdout, stderr := stub.merged(t, stub.pushEnv(t, mergeHost(t), tipCommit))
	if code != 0 || len(stub.puts) != 4 || !strings.Contains(stderr, "stored as token_verified") {
		t.Fatalf("exit %d, %d sends: %s%s", code, len(stub.puts), stdout, stderr)
	}
	for _, body := range stub.bodies[1:] {
		if !bytes.Equal(body, stub.bodies[0]) {
			t.Errorf("a later send differs: %s, %s", stub.bodies[0], body)
		}
	}

	stub = newMergeStub(t)
	stub.answers["903114-1482"] = []turn{{http.StatusUnprocessableEntity, `{"error":{"code":"MERGE_FACT_INVALID","message":"x"}}`}}
	if code, _, _ := stub.merged(t, stub.pushEnv(t, mergeHost(t), tipCommit)); code != exitRejected || len(stub.puts) != 1 {
		t.Errorf("a 422 was sent %d times, exit %d", len(stub.puts), code)
	}

	stub = newMergeStub(t)
	stub.answers["903114-1482"] = slices.Repeat([]turn{{http.StatusInternalServerError, ""}}, 1000)
	pace := verdictPace{interval: time.Millisecond, limit: 50 * time.Millisecond}
	env := stub.pushEnv(t, mergeHost(t), tipCommit)
	var out, errs bytes.Buffer
	code = merged(context.Background(), []string{"--platform-url", stub.server.URL, "--installation-id", testInstallation}, stub.server.Client(), func(name string) string { return env[name] }, pace, &out, &errs)
	if want := mergedVersionLine(contractV05) + "costroid: Costroid Platform refused merge 903114-1482: 500 with no refusal code\n"; code != exitRejected || errs.String() != want {
		t.Errorf("a platform that keeps failing: exit %d, stderr %q, want %q", code, errs.String(), want)
	}
}

// TestMergedStatesEachRefusal holds each refusal of integration/v0.5 to its
// line and its exit code, in export's scheme: 402 and 409
// INSTALLATION_NOT_BOUND warn and exit 0, every other refusal exits 1.
func TestMergedStatesEachRefusal(t *testing.T) {
	refused := "costroid: Costroid Platform refused merge 903114-1482: "
	for _, check := range []struct {
		status int
		code   string
		exit   int
		line   string
	}{
		{402, "PLAN_WRITES_REFUSED", 0, "costroid: warning: Costroid Platform refused merge 903114-1482: 402 PLAN_WRITES_REFUSED. Nothing in the merge fact is wrong, and nothing keeps it: run this job again once the plan allows writes"},
		{409, "INSTALLATION_NOT_BOUND", 0, "costroid: warning: Costroid Platform refused merge 903114-1482: 409 INSTALLATION_NOT_BOUND. The tenant holds no binding of github-owner:7204411: run costroid connect, as docs/connect.md shows, then run this job again"},
		{401, "AUTH_TOKEN_INVALID", 1, refused + "401 AUTH_TOKEN_INVALID. The token is absent, unknown, expired or revoked. Nothing was stored"},
		{403, "AUTH_TOKEN_SCOPE", 1, refused + "403 AUTH_TOKEN_SCOPE. The token lacks merges:write. Nothing was stored"},
		{403, "RUN_TOKEN_INVALID", 1, refused + "403 RUN_TOKEN_INVALID. The run token fails verification. Nothing was stored"},
		{403, "RUN_TOKEN_OWNER_MISMATCH", 1, refused + "403 RUN_TOKEN_OWNER_MISMATCH. The run token names another owner than the installation. Nothing was stored"},
		{422, "MERGE_FACT_IDENTITY_MISMATCH", 1, refused + "422 MERGE_FACT_IDENTITY_MISMATCH. A verified run token names another repository than repo. Nothing was stored"},
		{422, "MERGE_FACT_INVALID", 1, refused + "422 MERGE_FACT_INVALID. A key is missing, unknown or of the wrong form. Nothing was stored"},
		{400, "", 1, refused + "400 with no refusal code"},
	} {
		t.Run(check.code, func(t *testing.T) {
			stub := newMergeStub(t)
			stub.answers["903114-1482"] = []turn{{check.status, `{"error":{"code":"` + check.code + `","message":"x"}}`}}
			code, stdout, stderr := stub.merged(t, stub.pushEnv(t, mergeHost(t), tipCommit))
			if code != check.exit || stdout != "" || stderr != mergedVersionLine(contractV05)+check.line+"\n" || len(stub.puts) != 1 {
				t.Errorf("exit %d, %d sends, stdout %q, stderr %q, want %q", code, len(stub.puts), stdout, stderr, check.line)
			}
		})
	}
}

// TestMergedNamesAKeySetThatStaysUnavailable answers each send of a fact 503
// INSTALLATION_PROOF_KEYS_UNAVAILABLE, the refusal integration/v0.7 lists for
// GitHub's key set. merged sends the fact again until its pace ends, then
// names the code and the contract's sentence for it, and exits 1.
func TestMergedNamesAKeySetThatStaysUnavailable(t *testing.T) {
	stub := newV07MergeStub(t)
	stub.answers["903114-1482"] = slices.Repeat([]turn{{http.StatusServiceUnavailable, `{"error":{"code":"INSTALLATION_PROOF_KEYS_UNAVAILABLE","message":"x"}}`}}, 1000)
	var stdout, stderr bytes.Buffer
	args := []string{"--platform-url", stub.server.URL, "--installation-id", testInstallation}
	env := stub.pushEnv(t, mergeHost(t), tipCommit)
	code := merged(context.Background(), args, stub.server.Client(), func(name string) string { return env[name] }, verdictPace{interval: time.Millisecond, limit: 50 * time.Millisecond}, &stdout, &stderr)
	want := "costroid: Costroid Platform refused merge 903114-1482: 503 INSTALLATION_PROOF_KEYS_UNAVAILABLE. GitHub's key set cannot be read. Retry inside the run token's window. Nothing was stored\n"
	// merged can end at its pace's limit while the stub still serves the
	// last send, so the count is read under the stub's lock.
	stub.mu.Lock()
	sends := len(stub.puts)
	stub.mu.Unlock()
	if code != exitRejected || stderr.String() != mergedVersionLine(contractV07)+want || sends < 2 {
		t.Errorf("exit %d, %d sends, stderr %q, want %q", code, sends, stderr.String(), want)
	}
}

// mergeRefusalCodes are the refusal codes a version lists for a merge fact,
// with AUTH_TOKEN_INVALID, which each answers for a bearer it does not know.
func mergeRefusalCodes(t *testing.T, version contract) []string {
	t.Helper()
	var contractDocument struct {
		Components struct {
			Schemas struct {
				MergeRefusalCode struct {
					Enum []string `json:"enum"`
				} `json:"MergeRefusalCode"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal([]byte(readFile(t, contractCopy(version))), &contractDocument); err != nil {
		t.Fatal(err)
	}
	return append(contractDocument.Components.Schemas.MergeRefusalCode.Enum, "AUTH_TOKEN_INVALID")
}

// TestMergedNamesEveryRefusalTheContractLists holds merged's sentences to the
// refusal codes integration/v0.7, the version it sends by, lists for a merge
// fact: each has a line of its own, but MERGE_FACT_NOT_FOUND, which answers
// GET alone and merged never asks. merged names no code that no version it
// sends by lists: PLAN_WRITES_REFUSED is v0.6's and v0.5's, not v0.7's.
func TestMergedNamesEveryRefusalTheContractLists(t *testing.T) {
	listed := mergeRefusalCodes(t, statementContracts[0])
	if statementContracts[0] != contractV07 || len(listed) < 9 {
		t.Fatalf("integration/v%s lists %v", statementContracts[0].Version, listed)
	}
	own := []string{"PLAN_WRITES_REFUSED", installationNotBound, mergeFactConflict}
	for _, code := range listed {
		if code == "MERGE_FACT_NOT_FOUND" {
			continue
		}
		if _, known := mergeRefusals[code]; known == slices.Contains(own, code) {
			t.Errorf("%s: a sentence %t, a line of its own %t", code, known, slices.Contains(own, code))
		}
	}
	var every []string
	for _, version := range statementContracts {
		every = append(every, mergeRefusalCodes(t, version)...)
	}
	for _, code := range slices.Concat(slices.Collect(maps.Keys(mergeRefusals)), own) {
		if !slices.Contains(every, code) {
			t.Errorf("merged names %s, which no version it sends by lists", code)
		}
	}
}

// TestMergedOutsideAGitHubPushSendsNothing holds GitLab CI and a run outside
// any CI system to a line and exit 125, with no request anywhere.
func TestMergedOutsideAGitHubPushSendsNothing(t *testing.T) {
	stub := newMergeStub(t)
	for name, check := range map[string]struct {
		env  map[string]string
		want string
	}{
		"GitLab CI": {map[string]string{"GITLAB_CI": "true", "CI_COMMIT_SHA": tipCommit, platformTokenVariable: testIntegrationToken},
			"costroid: merged reads the pull requests a commit closed from GitHub alone, so on GitLab CI it sends nothing\n"},
		"no CI": {map[string]string{platformTokenVariable: testIntegrationToken},
			"costroid: merged runs in a GitHub Actions job on a push, and this is no GitHub Actions run\n"},
	} {
		code, stdout, stderr := stub.merged(t, check.env)
		if code != exitFailure || stdout != "" || stderr != check.want+seeTroubleshooting {
			t.Errorf("%s: exit %d, stdout %q, stderr %q, want %q", name, code, stdout, stderr, check.want)
		}
	}
	if len(stub.requests) != 0 {
		t.Errorf("the platform was asked %q", stub.requests)
	}
}

// TestMergedDocsMatchTheCommand runs docs/export.md's command lines through
// merged's flags, with the job's variables filled in, and holds its job to the
// permissions and the variables the command reads.
func TestMergedDocsMatchTheCommand(t *testing.T) {
	page := readFile(t, filepath.Join(repositoryRoot, "docs", "export.md"))
	lines := regexp.MustCompile(`(?m)costroid merged( [^\n]*)?$`).FindAllStringSubmatch(page, -1)
	if len(lines) < 2 {
		t.Fatalf("docs/export.md runs costroid merged %d times", len(lines))
	}
	filled := strings.NewReplacer(`"`, "", "$COSTROID_PLATFORM_URL", "https://app.costroid.com", "$GITHUB_REPOSITORY_OWNER_ID", "7204411")
	for _, line := range lines {
		if _, err := parseMerged(strings.Fields(filled.Replace(line[1])), io.Discard, io.Discard); err != nil {
			t.Errorf("docs/export.md runs %q, which merged refuses: %v", line[0], err)
		}
	}
	for _, want := range []string{
		"    permissions:\n      pull-requests: read\n      id-token: write\n",
		"GITHUB_TOKEN: ${{ github.token }}", platformTokenVariable + ": ${{ secrets." + platformTokenVariable + " }}",
		"identity:read", "merges:write", "--json", "--dry-run",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("docs/export.md does not hold %q", want)
		}
	}
}

// sentRequests are the requests the stub took, the run token's aside.
func (s *mergeStub) sentRequests() []string {
	return slices.DeleteFunc(slices.Clone(s.requests), func(request string) bool { return request == "GET /oidc" })
}

// TestMergedSendsByV06ToAPlatformThatStatesIt sends a merge queue's push to a
// platform that states integration/v0.6 beside every earlier version. merged
// reads GET /identity under v0.7, then v0.6, names v0.6 on standard error,
// and sends each fact to v0.6's path, each body a valid v0.6 MergeDocument.
func TestMergedSendsByV06ToAPlatformThatStatesIt(t *testing.T) {
	schema := contractSchema(t, contractV06, "MergeDocument")
	stub := newV06MergeStub(t)
	code, stdout, stderr := stub.merged(t, stub.pushEnv(t, mergeHost(t), groupCommit, tipCommit))
	if code != 0 || stdout != "" || !strings.HasPrefix(stderr, mergedVersionLine(contractV06)) {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	want := []string{
		"GET /api/integration/v0.7/identity",
		"GET /api/integration/v0.6/identity",
		"PUT /api/integration/v0.6/merges/903114-1481",
		"PUT /api/integration/v0.6/merges/903114-1482",
	}
	if got := stub.sentRequests(); !slices.Equal(got, want) {
		t.Errorf("the platform was asked %q, want %q", got, want)
	}
	for _, body := range stub.bodies {
		validMergeDocument(t, schema, body)
	}
	for _, line := range []string{
		"costroid: merge 903114-1481, pull request 1481 of github.com/example/widgets: stored as unverified\n",
		"costroid: merge 903114-1482, pull request 1482 of github.com/example/widgets: stored as token_verified, from a push to the protected branch main\n",
	} {
		if !strings.Contains(stderr, line) {
			t.Errorf("stderr lacks %q:\n%s", line, stderr)
		}
	}
}

// TestMergedSendsByV07ToAPlatformThatStatesIt sends a merge queue's push to a
// platform that states integration/v0.7 beside every earlier version. merged
// reads GET /identity under v0.7 alone, names v0.7, and sends each fact to
// v0.7's path, the same document v0.6 takes, each a valid v0.7 MergeDocument.
// The answers are read as v0.6's are: the fact of the tip names its branch.
func TestMergedSendsByV07ToAPlatformThatStatesIt(t *testing.T) {
	schema := contractSchema(t, contractV07, "MergeDocument")
	stub := newV07MergeStub(t)
	code, stdout, stderr := stub.merged(t, stub.pushEnv(t, mergeHost(t), groupCommit, tipCommit))
	if code != 0 || stdout != "" || !strings.HasPrefix(stderr, mergedVersionLine(contractV07)) {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	want := []string{
		"GET /api/integration/v0.7/identity",
		"PUT /api/integration/v0.7/merges/903114-1481",
		"PUT /api/integration/v0.7/merges/903114-1482",
	}
	if got := stub.sentRequests(); !slices.Equal(got, want) {
		t.Errorf("the platform was asked %q, want %q", got, want)
	}
	v06 := newV06MergeStub(t)
	v06.merged(t, v06.pushEnv(t, mergeHost(t), groupCommit, tipCommit))
	for i, body := range stub.bodies {
		validMergeDocument(t, schema, body)
		if i >= len(v06.bodies) || !bytes.Equal(body, v06.bodies[i]) {
			t.Errorf("by v0.7 merged sent %s, and by v0.6 another document", body)
		}
	}
	if line := "costroid: merge 903114-1482, pull request 1482 of github.com/example/widgets: stored as token_verified, from a push to the protected branch main\n"; !strings.Contains(stderr, line) {
		t.Errorf("stderr lacks %q:\n%s", line, stderr)
	}
}

// TestMergedSendsByV05ToAPlatformThatStatesV05 sends to a platform that serves
// no integration/v0.6. merged asks v0.7 and v0.6 first, sends by v0.5 and says so, and
// reads the answers as v0.5 has them: no branch is named, and the job's
// GITHUB_REF_PROTECTED, which v0.5 does not grade by, names no cause.
func TestMergedSendsByV05ToAPlatformThatStatesV05(t *testing.T) {
	schema := mergeDocumentSchema(t)
	stub := newMergeStub(t)
	env := stub.pushEnv(t, mergeHost(t), groupCommit, tipCommit)
	env["GITHUB_REF_PROTECTED"] = "false"
	code, stdout, stderr := stub.merged(t, env)
	if code != 0 || stdout != "" || !strings.HasPrefix(stderr, mergedVersionLine(contractV05)) {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	want := []string{
		"GET /api/integration/v0.7/identity",
		"GET /api/integration/v0.6/identity",
		"GET /api/integration/v0.5/identity",
		"PUT /api/integration/v0.5/merges/903114-1481",
		"PUT /api/integration/v0.5/merges/903114-1482",
	}
	if got := stub.sentRequests(); !slices.Equal(got, want) {
		t.Errorf("the platform was asked %q, want %q", got, want)
	}
	for _, body := range stub.bodies {
		validMergeDocument(t, schema, body)
	}
	if line := "costroid: merge 903114-1482, pull request 1482 of github.com/example/widgets: stored as token_verified\n"; !strings.Contains(stderr, line) {
		t.Errorf("stderr lacks %q:\n%s", line, stderr)
	}
	if strings.Contains(stderr, "protected") {
		t.Errorf("a v0.5 answer is read for a branch:\n%s", stderr)
	}

	stub = newMergeStub(t)
	code, stdout, _ = stub.merged(t, stub.pushEnv(t, mergeHost(t), tipCommit), "--json")
	if want := `{"merge_id":"903114-1482","pull_request":1482,"status":201,"run_identity":"token_verified"}` + "\n"; code != 0 || stdout != want {
		t.Errorf("--json: exit %d, printed %q, want %q", code, stdout, want)
	}
}

// TestMergedNamesARefusalOfAPlatformThatServesV06Alone holds a platform that
// serves integration/v0.6 alone and refuses the token to its refusal. The 404
// under v0.5 that follows does not say the platform serves no version.
func TestMergedNamesARefusalOfAPlatformThatServesV06Alone(t *testing.T) {
	stub := newMergeStub(t)
	stub.states = map[string]contract{contractV06.path(): contractV06}
	stub.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == contractV06.path()+"/identity" {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":{"code":"AUTH_TOKEN_SCOPE","message":"x"}}`))
			return
		}
		stub.serve(w, r)
	})
	code, stdout, stderr := stub.merged(t, stub.pushEnv(t, mergeHost(t), tipCommit))
	if want := "costroid: Costroid Platform refused the identity request: 403 AUTH_TOKEN_SCOPE\n"; code != exitRejected || stdout != "" || stderr != want {
		t.Errorf("exit %d, stdout %q, stderr %q, want %q", code, stdout, stderr, want)
	}
	if len(stub.puts) != 0 {
		t.Errorf("%d facts sent", len(stub.puts))
	}
}

// TestMergedNamesTheBranchOfACheckedFact holds a token_verified fact by
// integration/v0.6 to a line that names its branch, whole when the name holds
// a slash, and to its full ref under --json. An unverified fact names none,
// and a ref no branch can hold, such as one that carries a workflow command,
// names none either.
func TestMergedNamesTheBranchOfACheckedFact(t *testing.T) {
	stub := newV06MergeStub(t)
	stub.ref = "refs/heads/release/2026.11"
	env := stub.pushEnv(t, mergeHost(t), groupCommit, tipCommit)
	code, stdout, stderr := stub.merged(t, env)
	want := mergedVersionLine(contractV06) +
		"costroid: merge 903114-1481, pull request 1481 of github.com/example/widgets: stored as unverified\n" +
		"costroid: merge 903114-1481 reads unverified because its commit " + groupCommit + " is not the tip of the push, " + tipCommit + ", which the run token names, as in a merge queue's group\n" +
		"costroid: merge 903114-1482, pull request 1482 of github.com/example/widgets: stored as token_verified, from a push to the protected branch release/2026.11\n"
	if code != 0 || stdout != "" || stderr != want {
		t.Errorf("exit %d, stdout %q, stderr %q, want %q", code, stdout, stderr, want)
	}
	code, _, stderr = stub.merged(t, env)
	if line := "costroid: merge 903114-1482, pull request 1482 of github.com/example/widgets: already held as token_verified, from a push to the protected branch release/2026.11, so nothing changed\n"; code != 0 || !strings.Contains(stderr, line) {
		t.Errorf("sent again: exit %d, stderr lacks %q:\n%s", code, line, stderr)
	}

	stub = newV06MergeStub(t)
	stub.ref = "refs/heads/release/2026.11"
	code, stdout, _ = stub.merged(t, stub.pushEnv(t, mergeHost(t), groupCommit, tipCommit), "--json")
	wantJSON := `{"merge_id":"903114-1481","pull_request":1481,"status":201,"run_identity":"unverified"}` + "\n" +
		`{"merge_id":"903114-1482","pull_request":1482,"status":201,"run_identity":"token_verified","ref":"refs/heads/release/2026.11"}` + "\n"
	if code != 0 || stdout != wantJSON {
		t.Errorf("--json: exit %d, printed %q, want %q", code, stdout, wantJSON)
	}

	for _, ref := range []string{`"refs/heads/::error::forged"`, `"refs/heads/main\n::error::forged"`, `"refs/tags/v1"`, `7`} {
		stub = newV06MergeStub(t)
		stub.answers["903114-1482"] = []turn{{http.StatusCreated, `{"merge_id":"903114-1482","repo":"github.com/example/widgets","pull_request":1482,"merged_at":"2026-11-21T10:07:52Z","merge_commit_sha":"` + tipCommit + `","run_identity":"token_verified","ref":` + ref + `}`}}
		code, stdout, _ = stub.merged(t, stub.pushEnv(t, mergeHost(t), tipCommit), "--json")
		want := `{"merge_id":"903114-1482","pull_request":1482,"status":201,"run_identity":"token_verified"}` + "\n"
		if code != 0 || stdout != want {
			t.Errorf("a ref of %s: exit %d, printed %q, want %q", ref, code, stdout, want)
		}
	}
}

// TestMergedSaysAnUnprotectedBranchIsWhyAFactIsUnchecked holds a push to a
// branch that is not protected, by integration/v0.6, to a line on each fact
// that names that cause beside any other. The job's GITHUB_REF_PROTECTED is
// how merged can tell: without it no such line is printed, and either way the
// run token goes with each fact.
func TestMergedSaysAnUnprotectedBranchIsWhyAFactIsUnchecked(t *testing.T) {
	unprotected := func(id string) string {
		return "costroid: merge " + id + " reads unverified because the push was to a branch that is not protected, and a merge fact reads token_verified only from a protected branch\n"
	}
	stub := newV06MergeStub(t)
	stub.unprotected = true
	code, stdout, stderr := stub.merged(t, stub.pushEnv(t, mergeHost(t), groupCommit, tipCommit))
	want := "costroid: merge 903114-1481, pull request 1481 of github.com/example/widgets: stored as unverified\n" +
		"costroid: merge 903114-1481 reads unverified because its commit " + groupCommit + " is not the tip of the push, " + tipCommit + ", which the run token names, as in a merge queue's group\n" +
		unprotected("903114-1481") +
		"costroid: merge 903114-1482, pull request 1482 of github.com/example/widgets: stored as unverified\n" +
		unprotected("903114-1482")
	if code != 0 || stdout != "" || stderr != mergedVersionLine(contractV06)+want {
		t.Errorf("exit %d, stdout %q, stderr %q, want %q", code, stdout, stderr, want)
	}
	for i, request := range stub.puts {
		if request.Header.Get("Costroid-Run-Token") != testRunToken {
			t.Errorf("fact %d went without the run token", i)
		}
	}

	stub = newV06MergeStub(t)
	stub.unprotected = true
	env := stub.pushEnv(t, mergeHost(t), tipCommit)
	delete(env, "GITHUB_REF_PROTECTED")
	code, stdout, stderr = stub.merged(t, env)
	if want := mergedVersionLine(contractV06) + "costroid: merge 903114-1482, pull request 1482 of github.com/example/widgets: stored as unverified\n"; code != 0 || stdout != "" || stderr != want {
		t.Errorf("without GITHUB_REF_PROTECTED: exit %d, stdout %q, stderr %q, want %q", code, stdout, stderr, want)
	}
	if len(stub.puts) != 1 || stub.puts[0].Header.Get("Costroid-Run-Token") != testRunToken {
		t.Errorf("without GITHUB_REF_PROTECTED: %d facts, the run token decided by it", len(stub.puts))
	}
}

// TestMergedWritesTheRepositoryInLowerCase runs merged in a repository whose
// name GitHub states with capitals. The document's repo is lower case, the
// whole string with its host, and it is a valid integration/v0.6
// MergeDocument, whose repo admits no upper-case letter.
func TestMergedWritesTheRepositoryInLowerCase(t *testing.T) {
	schema := contractSchema(t, contractV06, "MergeDocument")
	stub := newV06MergeStub(t)
	env := stub.pushEnv(t, mergeHost(t), tipCommit)
	env["GITHUB_REPOSITORY"] = "Example/Widgets"
	env["GITHUB_SERVER_URL"] = "https://GitHub.com"
	code, stdout, stderr := stub.merged(t, env)
	if code != 0 || len(stub.bodies) != 1 {
		t.Fatalf("exit %d, %d facts sent: %s%s", code, len(stub.bodies), stdout, stderr)
	}
	var sent mergeDocument
	if err := json.Unmarshal(stub.bodies[0], &sent); err != nil || sent.Repo != "github.com/example/widgets" {
		t.Errorf("the document is %s, want repo github.com/example/widgets", stub.bodies[0])
	}
	validMergeDocument(t, schema, stub.bodies[0])
	upper, _ := jsonschema.UnmarshalJSON(strings.NewReader(strings.Replace(string(stub.bodies[0]), "github.com/example/widgets", "github.com/Example/Widgets", 1)))
	if schema.Validate(upper) == nil {
		t.Error("integration/v0.6's MergeDocument takes a repo with capitals, so this test proves nothing")
	}
}
