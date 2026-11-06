package gateway

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
)

const admin = "sk-admin-for-tests"

var run = Binding{Repository: "github.com/acme/widgets", Run: "42", Attempt: "1"}

func newRequest(t *testing.T) (Request, string) {
	t.Helper()
	public, err := PublicKey(admin)
	if err != nil {
		t.Fatal(err)
	}
	request, key, err := NewRequest(public, run)
	if err != nil {
		t.Fatal(err)
	}
	return request, key
}

func TestARequestOpensWithTheAdminKeyForItsOwnRun(t *testing.T) {
	request, key := newRequest(t)
	if strings.Contains(request.Sealed, key) || request.KeyID != KeyID(key) {
		t.Fatalf("the request holds the key in the clear, or the wrong id: %+v", request)
	}
	opened, err := request.Open(admin, run)
	if err != nil || opened != key {
		t.Fatalf("opened %q, %v", opened, err)
	}
}

func TestARequestDoesNotOpenForAnotherRunOrAnotherAdminKey(t *testing.T) {
	request, _ := newRequest(t)
	if _, err := request.Open("sk-another-admin", run); err == nil {
		t.Error("a request opened with another admin key")
	}
	other := run
	other.Attempt = "2"
	if _, err := request.Open(admin, other); err == nil {
		t.Error("a request for attempt 1 opened in attempt 2")
	}
	moved := request
	moved.Binding = other
	if _, err := moved.Open(admin, other); err == nil {
		t.Error("a request relabelled for another attempt opened: the seal is not bound to the run")
	}
}

func TestTheKeyIDIsTheSHA256LiteLLMStores(t *testing.T) {
	// LiteLLM stored this hash for this key on 2026-11-06 (v1.108.1).
	if got := KeyID("sk-explorekeyone12345"); got != "927771f064075852535ac803d4b21b75e6f678e57d73e2e7d7dac38defe243c3" {
		t.Fatalf("key id %s", got)
	}
}

// liteLLM is a stand-in for the routes the package calls, holding one key.
type liteLLM struct {
	generated map[string]any
	budgets   map[string]map[string]any
	info      string
	store     atomic.Value
	deletes   atomic.Int64
}

func (l *liteLLM) serve(t *testing.T) *httptest.Server {
	l.store.Store(`{"status":"healthy","db":"connected"}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/key/generate":
			_ = json.NewDecoder(r.Body).Decode(&l.generated)
			w.Write([]byte(`{}`))
		case "/key/info":
			w.Write([]byte(l.info))
		case "/key/delete":
			if l.deletes.Add(1) > 1 {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":{"message":"No keys found","code":"404"}}`))
				return
			}
			w.Write([]byte(`{"deleted_keys":["x"]}`))
		case "/health/readiness":
			w.Write([]byte(l.store.Load().(string)))
		case "/team/info":
			http.NotFound(w, r)
		case "/user/info":
			w.Write([]byte(`{}`))
		case "/team/new", "/user/update":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			l.budgets[r.URL.Path] = body
			w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestMintRegistersTheKeyWithTheRunsBudgetExpiryAndMetadata(t *testing.T) {
	fake := &liteLLM{}
	server := fake.serve(t)
	request, key := newRequest(t)
	mint := Mint{
		Client: NewClient(server.URL), AdminKey: admin, Run: run, Request: request,
		Budget: decimal.MustParse("0.01"), Lifetime: 30 * time.Minute, MaxParallel: 1, MaxRequestTokens: 20000,
		PullRequest: "7", Head: "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d",
	}
	id, err := mint.Register(context.Background())
	if err != nil || id != KeyID(key) {
		t.Fatalf("id %s, %v", id, err)
	}
	got := fake.generated
	if got["key"] != key || got["max_budget"] != 0.01 || got["duration"] != "1800s" || got["key_alias"] != run.Alias() {
		t.Fatalf("generated %v", got)
	}
	metadata := got["metadata"].(map[string]any)
	for name, want := range map[string]string{MetaRun: "42", MetaPullRequest: "7", MetaRepository: run.Repository, MetaMaxRequestTokens: "20000"} {
		if metadata[name] != want {
			t.Errorf("metadata %s reads %v", name, metadata[name])
		}
	}
	if _, ok := got["team_id"]; ok {
		t.Error("a key with no cap was put in a team")
	}
}

func TestCloseDeletesTheKeyAndAKeyAlreadyGoneIsNoError(t *testing.T) {
	fake := &liteLLM{}
	client := NewClient(fake.serve(t).URL)
	if deleted, err := Close(context.Background(), client, admin, run); !deleted || err != nil {
		t.Fatalf("first close: %t, %v", deleted, err)
	}
	if deleted, err := Close(context.Background(), client, admin, run); deleted || err != nil {
		t.Fatalf("second close: %t, %v", deleted, err)
	}
}

func info(budget, run string) KeyInfo {
	expires := time.Date(2026, 11, 6, 21, 0, 0, 0, time.UTC)
	parallel := 1
	number := json.Number(budget)
	return KeyInfo{
		MaxBudget: &number, Expires: &expires, MaxParallel: &parallel,
		Metadata: map[string]any{MetaRun: run, MetaMaxRequestTokens: "20000"},
	}
}

func TestAKeyThatHoldsTheRunGrantsGatewayPrecheck(t *testing.T) {
	expect := Expect{Budget: decimal.MustParse("0.01"), Run: "42", Now: time.Date(2026, 11, 6, 20, 0, 0, 0, time.UTC)}
	grant := Inspect("sk-x", info("0.01", "42"), expect)
	if !grant.Holds() || grant.MaxParallel != 1 || grant.MaxRequestTokens != 20000 || grant.KeyID != KeyID("sk-x") {
		t.Fatalf("grant %+v", grant)
	}
	for name, broken := range map[string]KeyInfo{
		"a budget over the run's": info("0.02", "42"),
		"another run's key":       info("0.01", "41"),
		"no budget":               func() KeyInfo { k := info("0.01", "42"); k.MaxBudget = nil; return k }(),
		"no expiry":               func() KeyInfo { k := info("0.01", "42"); k.Expires = nil; return k }(),
		"no parallel limit":       func() KeyInfo { k := info("0.01", "42"); k.MaxParallel = nil; return k }(),
		"no largest request":      func() KeyInfo { k := info("0.01", "42"); delete(k.Metadata, MetaMaxRequestTokens); return k }(),
	} {
		if Inspect("sk-x", broken, expect).Holds() {
			t.Errorf("%s still grants gateway-precheck", name)
		}
	}
}

func TestAStoreThatFailsWhileTheAgentRunsIsReported(t *testing.T) {
	fake := &liteLLM{}
	session := Session{Client: NewClient(fake.serve(t).URL), Every: 10 * time.Millisecond}
	stop := session.Watch(context.Background())
	fake.store.Store(`{"status":"healthy","db":"disconnected"}`)
	time.Sleep(50 * time.Millisecond)
	fake.store.Store(`{"status":"healthy","db":"connected"}`)
	if reason := stop(); !strings.Contains(reason, `store read "disconnected"`) {
		t.Fatalf("reason %q", reason)
	}
	held := session.Watch(context.Background())
	if reason := held(); reason != "" {
		t.Fatalf("a connected store reads %q", reason)
	}
}

func TestTheAgentGetsTheGatewayInPlaceOfAnyProviderCredential(t *testing.T) {
	session := Session{Client: NewClient("http://gateway.test/"), Key: "sk-run"}
	env := session.Env([]string{"PATH=/bin", "ANTHROPIC_API_KEY=sk-ant-provider", "ANTHROPIC_AUTH_TOKEN=old", "ANTHROPIC_BASE_URL=https://api.anthropic.com"})
	want := []string{"PATH=/bin", "ANTHROPIC_BASE_URL=http://gateway.test", "ANTHROPIC_AUTH_TOKEN=sk-run"}
	if strings.Join(env, "\n") != strings.Join(want, "\n") {
		t.Fatalf("env %q", env)
	}
}

func TestMintWaitsForTheRequestArtifact(t *testing.T) {
	request, _ := newRequest(t)
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	file, _ := writer.Create(RequestFile)
	_ = json.NewEncoder(file).Encode(request)
	_ = writer.Close()
	var calls atomic.Int64
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gh-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/repos/acme/widgets/actions/runs/42/artifacts":
			if calls.Add(1) == 1 || r.URL.Query().Get("name") != "costroid-gateway-request-1" {
				w.Write([]byte(`{"artifacts":[]}`))
				return
			}
			w.Write([]byte(`{"artifacts":[{"archive_download_url":"` + server.URL + `/zip","expired":false}]}`))
		case "/zip":
			w.Write(archive.Bytes())
		}
	}))
	defer server.Close()
	artifacts := Artifacts{API: server.URL, Repository: "acme/widgets", Run: "42", Token: "gh-token", HTTP: server.Client(), Every: time.Millisecond}
	got, err := artifacts.Wait(context.Background(), "costroid-gateway-request-1")
	if err != nil || got != request {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestDailyCapsAreCarriedAsTheTeamsAndTheUsersBudgets(t *testing.T) {
	fake := &liteLLM{budgets: map[string]map[string]any{}}
	server := fake.serve(t)
	request, _ := newRequest(t)
	repository, author := decimal.MustParse("20"), decimal.MustParse("5")
	mint := Mint{
		Client: NewClient(server.URL), AdminKey: admin, Run: run, Request: request,
		Budget: decimal.MustParse("0.01"), Lifetime: time.Hour, MaxParallel: 1, MaxRequestTokens: 1,
		Author: "octocat", Caps: DailyCaps{Repository: &repository, Author: &author},
	}
	if _, err := mint.Register(context.Background()); err != nil {
		t.Fatal(err)
	}
	team, user := fake.budgets["/team/new"], fake.budgets["/user/update"]
	if team["team_id"] != "costroid:github.com/acme/widgets" || team["max_budget"] != 20.0 || team["budget_duration"] != "1d" {
		t.Errorf("a new team %v", team)
	}
	if user["user_id"] != "costroid:github.com/octocat" || user["max_budget"] != 5.0 || user["budget_duration"] != "1d" {
		t.Errorf("an existing user %v", user)
	}
	if fake.generated["team_id"] != team["team_id"] || fake.generated["user_id"] != user["user_id"] {
		t.Errorf("the key is not in the team and the user: %v", fake.generated)
	}
}

func TestSpendWaitsWhileTheGatewayStillStoresIt(t *testing.T) {
	var reads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reads.Add(1) < 3 {
			w.Write([]byte(`{"info":{"spend":0.0}}`))
			return
		}
		w.Write([]byte(`{"info":{"spend":0.018}}`))
	}))
	defer server.Close()
	session := Session{Client: NewClient(server.URL), Key: "sk-x", Poll: time.Millisecond, Settle: time.Second, Stable: time.Second}
	spend, err := session.Spend(context.Background(), decimal.MustParse("0.018"))
	if err != nil || spend != "0.018" {
		t.Fatalf("spend %q, %v", spend, err)
	}
}

func TestSpendStopsOnAFigureThatHasSettledElsewhere(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"info":{"spend":0.018000000000000002}}`))
	}))
	defer server.Close()
	session := Session{Client: NewClient(server.URL), Key: "sk-x", Poll: time.Millisecond, Settle: time.Minute, Stable: 20 * time.Millisecond}
	start := time.Now()
	spend, err := session.Spend(context.Background(), decimal.MustParse("0.018"))
	if err != nil || spend != "0.018000000000000002" || time.Since(start) > 10*time.Second {
		t.Fatalf("spend %q, %v, after %s", spend, err, time.Since(start))
	}
}
