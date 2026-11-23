package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/gateway"
	"github.com/Costroid/costroid/internal/gateway/gatewaytest"
	"github.com/Costroid/costroid/internal/oidc"
	"github.com/Costroid/costroid/internal/oidc/oidctest"
	"github.com/Costroid/costroid/internal/server"
	"github.com/Costroid/costroid/internal/store"
)

// memoryLedger keeps what costroid serve stores for one tenant, so a test can
// read what the service took.
type memoryLedger struct {
	mu      sync.Mutex
	records []store.Record
	minted  map[string]store.MintedCredential
	spent   []store.AttestedSpend
}

var acmeTenant, _ = store.ParseTenantID("aaaaaaaa-0000-4000-8000-000000000001")

func (l *memoryLedger) TenantForOwner(_ context.Context, host, owner string) (store.TenantID, error) {
	if host != "github.com" || owner != "acme" {
		return store.TenantID{}, store.ErrNotFound
	}
	return acmeTenant, nil
}

func (l *memoryLedger) TenantForKey(context.Context, string) (store.TenantID, error) {
	return store.TenantID{}, store.ErrNotFound
}

func (l *memoryLedger) Ingest(_ context.Context, _ store.TenantID, batch store.Batch) (store.Receipt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, batch.Records...)
	return store.Receipt{Inserted: len(batch.Records)}, nil
}

func (l *memoryLedger) PullRequest(context.Context, store.TenantID, string, string) ([]store.Line, error) {
	return nil, nil
}

func (l *memoryLedger) Attest(_ context.Context, _ store.TenantID, minted store.MintedCredential) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.minted[minted.KeySHA256] = minted
	return nil
}

func (l *memoryLedger) MintedCredentials(_ context.Context, _ store.TenantID, keys []string) (map[string]store.MintedCredential, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	found := map[string]store.MintedCredential{}
	for _, key := range keys {
		if minted, ok := l.minted[key]; ok {
			found[key] = minted
		}
	}
	return found, nil
}

func (l *memoryLedger) AttestSpend(_ context.Context, _ store.TenantID, attested store.AttestedSpend) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.spent = append(l.spent, attested)
	return nil
}

func (l *memoryLedger) GatewaySpend(context.Context, store.TenantID, string, string) (int64, decimal.Decimal, error) {
	return 0, decimal.Decimal{}, nil
}

// serveWorld is costroid serve's handler over TLS on a memory ledger, a CI
// issuer the test signs for, and GitHub's ID token endpoint, which answers the
// token of a job of the pull request's own run in the gateway's environment,
// as the earlier LiteLLM recipe's mint and close jobs were. The command's own
// client trusts the servers' test certificate.
type serveWorld struct {
	ledger *memoryLedger
	issuer *oidctest.Issuer
	serve  *httptest.Server
	// jobToken is the token the endpoint answers: a job of the pull
	// request's own run in the gateway's environment, unless a test sets
	// another.
	jobToken func() string
}

func newServeWorld(t *testing.T) *serveWorld {
	t.Helper()
	pinned, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	w := &serveWorld{ledger: &memoryLedger{minted: map[string]store.MintedCredential{}}, issuer: oidctest.New(t)}
	w.jobToken = func() string { return w.token("costroid-gateway") }
	verifier := oidc.NewVerifier(ingestAudience, w.issuer.Server.Client(), nil,
		oidc.Issuer{Kind: oidc.GitHubActions, URL: w.issuer.URL(), JWKSURL: w.issuer.URL() + "/keys", Host: "github.com"})
	w.serve = httptest.NewTLSServer(server.New(server.Config{
		Ledger: w.ledger, Verifier: verifier, Catalog: pinned, PseudonymKey: bytes.Repeat([]byte("k"), 32),
		GatewayEnvironment: "costroid-gateway", Spend: w.ledger, Logger: slog.New(slog.DiscardHandler),
	}).Handler())
	t.Cleanup(w.serve.Close)
	tokens := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(rw).Encode(map[string]string{"value": w.jobToken()})
	}))
	t.Cleanup(tokens.Close)
	trusted := http.DefaultTransport
	http.DefaultTransport = w.serve.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = trusted })
	t.Setenv(ingestURLVariable, w.serve.URL+"/v1/rows")
	t.Setenv(ingestAudienceVariable, ingestAudience)
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", tokens.URL+"/token?api-version=2.0")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", requestToken)
	return w
}

// token is a token of a job of the pull request's own run, gatewayRunID, in
// the environment named, or in none when it is empty.
func (w *serveWorld) token(environment string) string {
	return w.issuer.Token(w.claims(environment))
}

// unprotectedRunToken is the token of a job of the gateway recipe's run, in
// the gateway's environment, on a default branch that no rule protects:
// ref_protected "false", as recorded GitHub Actions run 45109004242 states.
func (w *serveWorld) unprotectedRunToken() string {
	claims := w.claims("costroid-gateway")
	claims["run_id"], claims["event_name"], claims["ref"], claims["ref_protected"] = mintingRunID, "workflow_run", "refs/heads/main", "false"
	return w.issuer.Token(claims)
}

// claims are the claims of a job of the pull request's own run.
func (w *serveWorld) claims(environment string) map[string]any {
	claims := map[string]any{
		"iss": w.issuer.URL(), "aud": ingestAudience, "exp": time.Now().Add(5 * time.Minute).Unix(),
		"repository": "acme/widgets", "repository_owner": "acme", "run_id": gatewayRunID, "run_attempt": "1",
		"sha": mergeSHA, "actor": gatewayAuthor, "event_name": "pull_request", "ref": "refs/pull/7/merge",
	}
	if environment != "" {
		claims["environment"] = environment
	}
	return claims
}

// ingest posts a rows file under the agent's job's token, as the ingest job does.
func (w *serveWorld) ingest(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, w.serve.URL+"/v1/rows", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+w.token(""))
	request.Header.Set("Content-Type", "application/x-ndjson")
	response, err := w.serve.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if answer, _ := io.ReadAll(response.Body); response.StatusCode != http.StatusOK {
		t.Fatalf("ingest answered %d: %s", response.StatusCode, answer)
	}
}

// TestTheOldRecipeWarnsAndItsRowsReadWindowed mints and closes a run's key
// from jobs of the pull request's own run, as the LiteLLM recipe did before
// its mint and close jobs moved to recipes/github-gateway.yml, against costroid
// serve. serve refuses both attestations, and stores neither. mint and close
// warn, say what to do, and exit 0: the key is registered and deleted, and
// the run's rows read windowed. Every other failure of an attestation, with
// --for-triggering-run or without it, still fails the step.
func TestTheOldRecipeWarnsAndItsRowsReadWindowed(t *testing.T) {
	job := newGatewayJob(t)
	key := job.request(t)
	id := gateway.KeyID(key)
	world := newServeWorld(t)
	stub := gatewaytest.New(t, gatewayAdmin)

	code, stdout, stderr := job.mint(t, stub, "--request", filepath.Join(job.dir, gateway.RequestFile))
	if code != 0 || strings.TrimSpace(stdout) != id || !strings.Contains(stderr, unprotectedKey) {
		t.Fatalf("mint in the pull request's own run: exit %d, %q, %s", code, stdout, stderr)
	}
	if _, registered := stub.Key(gatewayAlias); !registered {
		t.Fatal("the key was not registered")
	}

	// The agent's job: replayClaude clears the CI variables, and the job sets
	// them again, with the ledger named once more.
	claude := replayClaude(t)
	newGatewayJob(t)
	t.Setenv(ingestURLVariable, world.serve.URL+"/v1/rows")
	rows := filepath.Join(t.TempDir(), "rows.jsonl")
	if code, _, stderr := execute(t, "run", "--budget-usd", "5", "--out", rows, "--credential", id, "--", claude, "-p", "Fix the failing test."); code != 0 {
		t.Fatalf("run exit %d: %s", code, stderr)
	}
	world.ingest(t, rows)

	stub.SetSpend(gatewayAlias, "0.018")
	counter := gateway.Counter{
		Client: gateway.NewClient(stub.URL), AdminKey: gatewayAdmin, Run: gateway.Binding{Repository: "github.com/acme/widgets", Run: gatewayRunID, Attempt: "1"},
		Poll: time.Millisecond, Stable: 50 * time.Millisecond, Settle: 200 * time.Millisecond,
	}
	var closed, warned bytes.Buffer
	if err := closeKey(context.Background(), counter, 0, world.serve.Client(), false, &closed, &warned); err != nil || closed.String() != "deleted\n" || !strings.Contains(warned.String(), unprotectedCount) {
		t.Fatalf("close in the pull request's own run: %v, %q, %s", err, closed.String(), warned.String())
	}

	if len(world.ledger.minted) != 0 || len(world.ledger.spent) != 0 {
		t.Fatalf("serve stored an attestation from the pull request's own run: %v %v", world.ledger.minted, world.ledger.spent)
	}
	if len(world.ledger.records) == 0 {
		t.Fatal("serve stored no rows")
	}
	for _, record := range world.ledger.records {
		if got := record.Row.Tags[focus.TagAttributionGrade]; got != "windowed" || record.Row.SubAccountID == nil || *record.Row.SubAccountID != id {
			t.Errorf("a row on the key reads %q, with SubAccountId %v", got, record.Row.SubAccountID)
		}
	}

	t.Run("every other failure fails the step", func(t *testing.T) {
		for name, answer := range map[string]struct {
			status int
			body   string
		}{
			"a key attested for another run": {http.StatusConflict, `{"error":{"code":"CREDENTIAL_ATTESTED_ELSEWHERE"}}`},
			"a token the ledger cannot read": {http.StatusUnauthorized, `{"error":{"code":"AUTH_TOKEN_INVALID"}}`},
			"a job outside the environment":  {http.StatusForbidden, `{"error":{"code":"ATTESTATION_FORBIDDEN"}}`},
			"a ledger that answers no code":  {http.StatusBadGateway, `bad gateway`},
		} {
			ledger := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
				rw.WriteHeader(answer.status)
				_, _ = rw.Write([]byte(answer.body))
			}))
			t.Setenv(ingestURLVariable, ledger.URL+"/v1/rows")
			for _, triggering := range []bool{false, true} {
				if err := attestKey(context.Background(), ledger.Client(), gateway.AttestationFor(id, counter.Run, headCommit), triggering, io.Discard); err == nil {
					t.Errorf("%s, for the triggering run %t: mint went on", name, triggering)
				}
			}
			ledger.Close()
		}
		unreachable := httptest.NewTLSServer(http.NotFoundHandler())
		unreachable.Close()
		t.Setenv(ingestURLVariable, unreachable.URL+"/v1/rows")
		if err := attestKey(context.Background(), unreachable.Client(), gateway.AttestationFor(id, counter.Run, headCommit), false, io.Discard); err == nil {
			t.Error("a ledger that cannot be reached: mint went on")
		}
	})
}

// TestTheNewRecipeOnAnUnprotectedBranchWarnsAndItsRowsReadWindowed runs the
// gateway recipe's mint and close, with --for-triggering-run, where GitHub's
// token states the default branch is not protected, as on a private
// repository on GitHub Free. serve refuses both attestations and stores
// neither. mint and close warn that the branch is not protected, say to
// protect it, and exit 0: the key is registered and deleted, and the pull
// request's rows read windowed, the true reading there.
func TestTheNewRecipeOnAnUnprotectedBranchWarnsAndItsRowsReadWindowed(t *testing.T) {
	job := newGatewayJob(t)
	key := job.request(t)
	id := gateway.KeyID(key)
	world := newServeWorld(t)
	world.jobToken = world.unprotectedRunToken
	stub := gatewaytest.New(t, gatewayAdmin)

	job.triggered(t, nil)
	code, stdout, stderr := job.mint(t, stub, "--for-triggering-run", "--request", filepath.Join(job.dir, gateway.RequestFile))
	if code != 0 || strings.TrimSpace(stdout) != id || !strings.Contains(stderr, unprotectedBranchKey) {
		t.Fatalf("mint on an unprotected default branch: exit %d, %q, %s", code, stdout, stderr)
	}
	if _, registered := stub.Key(gatewayAlias); !registered {
		t.Fatal("the key was not registered for the pull request's run")
	}
	run, err := closingRun(true)
	if err != nil {
		t.Fatal(err)
	}

	// The agent's job of the pull request's run: replayClaude clears the CI
	// variables, and the job sets them again, with the ledger named.
	claude := replayClaude(t)
	newGatewayJob(t)
	t.Setenv(ingestURLVariable, world.serve.URL+"/v1/rows")
	rows := filepath.Join(t.TempDir(), "rows.jsonl")
	if code, _, stderr := execute(t, "run", "--budget-usd", "5", "--out", rows, "--credential", id, "--", claude, "-p", "Fix the failing test."); code != 0 {
		t.Fatalf("run exit %d: %s", code, stderr)
	}
	world.ingest(t, rows)

	stub.SetSpend(gatewayAlias, "0.018")
	counter := gateway.Counter{
		Client: gateway.NewClient(stub.URL), AdminKey: gatewayAdmin, Run: run,
		Poll: time.Millisecond, Stable: 50 * time.Millisecond, Settle: 200 * time.Millisecond,
	}
	var closed, warned bytes.Buffer
	if err := closeKey(context.Background(), counter, 0, world.serve.Client(), true, &closed, &warned); err != nil || closed.String() != "deleted\n" || !strings.Contains(warned.String(), unprotectedBranchCount) {
		t.Fatalf("close on an unprotected default branch: %v, %q, %s", err, closed.String(), warned.String())
	}
	t.Logf("mint said: %s", strings.TrimSpace(stderr))
	t.Logf("close said: %s", strings.TrimSpace(warned.String()))

	if len(world.ledger.minted) != 0 || len(world.ledger.spent) != 0 {
		t.Fatalf("serve stored an attestation from an unprotected branch: %v %v", world.ledger.minted, world.ledger.spent)
	}
	if len(world.ledger.records) == 0 {
		t.Fatal("serve stored no rows")
	}
	for _, record := range world.ledger.records {
		if got := record.Row.Tags[focus.TagAttributionGrade]; got != "windowed" {
			t.Errorf("a row on the key reads %q", got)
		}
	}
}
