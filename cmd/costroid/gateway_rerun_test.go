package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Costroid/costroid/internal/gateway"
	"github.com/Costroid/costroid/internal/gateway/gatewaytest"
)

// rerunMintingRunID is the run workflow_run started on the in_progress event
// of the example run's second attempt: a run of its own, apart from the one
// that minted the first attempt's key.
const rerunMintingRunID = "16873410499"

// recordingLedger is a ledger that takes every attestation, as costroid serve
// takes a new one and answers the same one again, and keeps their bodies.
type recordingLedger struct {
	mu     sync.Mutex
	bodies []gateway.Attestation
}

// names the ledger to the job, under the platform's token endpoint, which
// issues a token for any audience.
func (l *recordingLedger) names(t *testing.T) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/credentials" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		data, _ := io.ReadAll(r.Body)
		var body gateway.Attestation
		_ = json.Unmarshal(data, &body)
		l.mu.Lock()
		l.bodies = append(l.bodies, body)
		l.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	t.Setenv(ingestURLVariable, server.URL+"/v1/rows")
	t.Setenv(ingestAudienceVariable, ingestAudience)
}

func (l *recordingLedger) taken() []gateway.Attestation {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]gateway.Attestation(nil), l.bodies...)
}

// platformAudiences are the audiences GitHub was asked for under the
// platform's tenant, apart from the ledger's.
func platformAudiences(audiences []string) []string {
	var platform []string
	for _, audience := range audiences {
		if strings.HasPrefix(audience, "https://app.costroid.com/tenants/") {
			platform = append(platform, audience)
		}
	}
	return platform
}

// TestAReRunsAttestationNamesItsAttempt mints the example run's first
// attempt's key, then re-runs the run: the agent's job of attempt 2 draws a
// key of its own, and a new run, which workflow_run started on attempt 2's
// in_progress event, mints it. That key is new, its alias and metadata name
// attempt 2, and the attestation to the ledger and the statement to Costroid
// Platform, body and audience, name attempt 2 of the same run. The first
// attempt's key stands as it was.
func TestAReRunsAttestationNamesItsAttempt(t *testing.T) {
	platform := newStatementPlatform(t)
	job, first := exampleJob(t)
	platform.names(t)
	ledger := &recordingLedger{}
	ledger.names(t)
	stub := gatewaytest.New(t, gatewayAdmin)
	if code, _, stderr := job.mintFor(t, stub); code != 0 {
		t.Fatalf("the first attempt's mint: exit %d: %s", code, stderr)
	}

	t.Setenv("GITHUB_EVENT_NAME", "pull_request")
	t.Setenv("GITHUB_RUN_ID", gatewayRunID)
	t.Setenv("GITHUB_RUN_ATTEMPT", "2")
	second := job.request(t)
	job.triggered(t, func(followed map[string]any) {
		followed["run_attempt"] = 2
		followed["head_sha"] = exampleHead
		followed["head_repository"] = map[string]any{"full_name": "example/widgets"}
	})
	t.Setenv("GITHUB_RUN_ID", rerunMintingRunID)
	code, stdout, stderr := job.mintFor(t, stub)
	id := gateway.KeyID(second)
	if code != 0 || strings.TrimSpace(stdout) != id || strings.Contains(stderr, "holds this run's key already") {
		t.Fatalf("the re-run's mint: exit %d, %q: %s", code, stdout, stderr)
	}
	if second == first {
		t.Fatal("the re-run drew the first attempt's key")
	}

	rerun := gateway.Binding{Repository: exampleRun.Repository, Run: exampleRun.Run, Attempt: "2"}
	held, ok := stub.Key(rerun.Alias())
	if !ok || held.Key != second || held.Metadata[gateway.MetaRun] != exampleRun.Run || held.Metadata[gateway.MetaAttempt] != "2" {
		t.Errorf("the re-run's key on the gateway: %+v, held %t", held, ok)
	}
	if kept, ok := stub.Key(exampleRun.Alias()); !ok || kept.Key != first {
		t.Errorf("the first attempt's key: %+v, held %t", kept, ok)
	}
	if generated, _ := stub.Registrations(); generated != 2 {
		t.Errorf("the gateway registered %d keys, want one for each attempt", generated)
	}

	attested := ledger.taken()
	want := gateway.AttestationFor(id, rerun, exampleHead)
	want.PullRequests = []string{"7"}
	if len(attested) != 2 || attested[1].KeyID != want.KeyID || attested[1].RunID != want.RunID || attested[1].RunAttempt != "2" || attested[1].HeadSHA != exampleHead {
		t.Errorf("the ledger took %+v, want %+v last", attested, want)
	}
	writes, audiences := platform.sent()
	audiences = platformAudiences(audiences)
	audience := "https://app.costroid.com/tenants/" + testTenant + "/credentials/" + id + "/" + exampleRun.Run + "/2"
	if len(audiences) != 2 || audiences[1] != audience {
		t.Errorf("the job's tokens were asked for %q, want %q last", audiences, audience)
	}
	body := `{"run_id":"` + exampleRun.Run + `","run_attempt":"2","head_sha":"` + exampleHead + `"}`
	if len(writes) != 2 || writes[1].path != statementPath+id || string(writes[1].body) != body {
		t.Errorf("the platform received %+v, want %s at %s%s last", writes, body, statementPath, id)
	}
}

// heldLine is what mint says when the gateway holds the run's key already,
// as this mint registers it.
const heldLine = "the gateway holds this run's key already, as this mint registers it, so mint registers nothing and states the key again\n"

// TestMintStatesAgainWhenTheGatewayHoldsItsKey mints the example run's key
// while the ledger cannot be reached: the key is registered, the platform
// takes the statement, and the step fails. The mint job run again finds the
// key on the gateway as it registers it, says so first, registers nothing,
// sets no budget, and states the key again: the ledger takes it, and the
// platform answers that it held it already.
func TestMintStatesAgainWhenTheGatewayHoldsItsKey(t *testing.T) {
	platform := newStatementPlatform(t)
	job, key := exampleJob(t)
	platform.names(t)
	unreachable := httptest.NewTLSServer(http.NotFoundHandler())
	unreachable.Close()
	t.Setenv(ingestURLVariable, unreachable.URL+"/v1/rows")
	t.Setenv(ingestAudienceVariable, ingestAudience)
	stub := gatewaytest.New(t, gatewayAdmin)
	if code, _, stderr := job.mintFor(t, stub); code != exitFailure || strings.Contains(stderr, heldLine) {
		t.Fatalf("a mint whose ledger cannot be reached: exit %d: %s", code, stderr)
	}
	generated, budgets := stub.Registrations()
	if generated != 1 {
		t.Fatalf("the gateway registered %d keys", generated)
	}

	ledger := &recordingLedger{}
	ledger.names(t)
	t.Setenv("GITHUB_RUN_ATTEMPT", "2")
	code, stdout, stderr := job.mintFor(t, stub)
	id := gateway.KeyID(key)
	if code != 0 || strings.TrimSpace(stdout) != id {
		t.Fatalf("the mint job run again: exit %d, %q: %s", code, stdout, stderr)
	}
	held, attested := strings.Index(stderr, heldLine), strings.Index(stderr, "attested to the ledger\n")
	if held < 0 || attested < held || !strings.Contains(stderr, "Costroid Platform held this attestation already, from a run of refs/heads/main, so nothing changed\n") {
		t.Errorf("the mint job run again said:\n%s", stderr)
	}
	if again, budgetsAgain := stub.Registrations(); again != generated || budgetsAgain != budgets {
		t.Errorf("the mint job run again registered %d keys and set %d budgets, after %d and %d", again, budgetsAgain, generated, budgets)
	}
	want := gateway.AttestationFor(id, exampleRun, exampleHead)
	if taken := ledger.taken(); len(taken) != 1 || taken[0].KeyID != want.KeyID || taken[0].RunID != want.RunID || taken[0].RunAttempt != want.RunAttempt || taken[0].HeadSHA != want.HeadSHA {
		t.Errorf("the ledger took %+v, want %+v", taken, want)
	}
	writes, _ := platform.sent()
	if len(writes) != 2 || string(writes[1].body) != string(writes[0].body) || writes[1].path != writes[0].path {
		t.Errorf("the platform received %+v, want the same statement twice", writes)
	}
}

// TestMintRefusesASecondKeyForOneRun holds the example run's key on the
// gateway otherwise than mint registers it: with another budget, another
// parallel-request limit, another attempt in its metadata, a metadata value
// mint does not set, a team or a user where mint sets none, a budget window,
// an expiry past the key's lifetime, or under another alias, as a key every
// run shares. It also holds another key under the run's alias. Mint refuses
// each before it registers, sets a budget or states anything, and the key
// the gateway holds is left as it was.
func TestMintRefusesASecondKeyForOneRun(t *testing.T) {
	platform := newStatementPlatform(t)
	job, _ := exampleJob(t)
	platform.names(t)
	ledger := &recordingLedger{}
	ledger.names(t)
	minted := gatewaytest.New(t, gatewayAdmin)
	if code, _, stderr := job.mintFor(t, minted); code != 0 {
		t.Fatalf("mint: exit %d: %s", code, stderr)
	}
	registered, ok := minted.Key(exampleRun.Alias())
	if !ok {
		t.Fatal("the key was not registered")
	}
	stated := len(ledger.taken())

	otherwise := func(change func(*gatewaytest.Key)) gatewaytest.Key {
		held := registered
		held.Metadata = map[string]string{}
		for name, value := range registered.Metadata {
			held.Metadata[name] = value
		}
		change(&held)
		return held
	}
	for name, tc := range map[string]struct {
		held gatewaytest.Key
		want string
	}{
		"another budget":                  {otherwise(func(k *gatewaytest.Key) { k.MaxBudget = "50" }), "holds the request's key already"},
		"another parallel-request limit":  {otherwise(func(k *gatewaytest.Key) { k.MaxParallel = 2 }), "holds the request's key already"},
		"another attempt in its metadata": {otherwise(func(k *gatewaytest.Key) { k.Metadata[gateway.MetaAttempt] = "2" }), "holds the request's key already"},
		"a budget window":                 {otherwise(func(k *gatewaytest.Key) { k.BudgetDuration = "1m" }), "holds the request's key already"},
		"an expiry past the lifetime":     {otherwise(func(k *gatewaytest.Key) { k.Duration = "48h" }), "holds the request's key already"},
		"no expiry":                       {otherwise(func(k *gatewaytest.Key) { k.Duration = "" }), "holds the request's key already"},
		"a key every run shares":          {otherwise(func(k *gatewaytest.Key) { k.Alias = "shared" }), "holds the request's key already"},
		"a team where mint sets none":     {otherwise(func(k *gatewaytest.Key) { k.Team = "costroid:github.com/example/widgets" }), "holds the request's key already"},
		"a user where mint sets none":     {otherwise(func(k *gatewaytest.Key) { k.User = "costroid-minter" }), "holds the request's key already"},
		"metadata mint does not set":      {otherwise(func(k *gatewaytest.Key) { k.Metadata["costroid_other"] = "1" }), "holds the request's key already"},
		"another key under the run's alias": {
			otherwise(func(k *gatewaytest.Key) { k.Key = "sk-another-key-of-sixteen-or-more" }),
			"holds another key for run " + exampleRun.Run + ", attempt 1, already",
		},
	} {
		t.Run(name, func(t *testing.T) {
			stub := gatewaytest.New(t, gatewayAdmin)
			stub.Add(tc.held)
			writes, _ := platform.sent()
			code, stdout, stderr := job.mintFor(t, stub)
			if code != exitFailure || stdout != "" || !strings.Contains(stderr, tc.want) || strings.Contains(stderr, heldLine) {
				t.Errorf("exit %d, %q: %s", code, stdout, stderr)
			}
			if generated, budgets := stub.Registrations(); generated != 0 || budgets != 0 {
				t.Errorf("mint registered %d keys and set %d budgets", generated, budgets)
			}
			if after, ok := stub.Key(tc.held.Alias); !ok || after.Key != tc.held.Key || after.MaxBudget != tc.held.MaxBudget {
				t.Errorf("the key the gateway held is now %+v", after)
			}
			if after, _ := platform.sent(); len(after) != len(writes) || len(ledger.taken()) != stated {
				t.Error("mint stated a key it refused")
			}
		})
	}
}
