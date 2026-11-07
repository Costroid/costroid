package gateway

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// sealing is the tests' sealing key pair, drawn once.
var sealing, sealingPublic = func() (string, string) {
	private, public, err := NewSealingKey()
	if err != nil {
		panic(err)
	}
	return private, public
}()

func newRequest(t *testing.T) (Request, string) {
	t.Helper()
	request, key, err := NewRequest(sealingPublic, run)
	if err != nil {
		t.Fatal(err)
	}
	return request, key
}

func TestARequestOpensWithTheSealingKeyForItsOwnRun(t *testing.T) {
	request, key := newRequest(t)
	if strings.Contains(request.Sealed, key) || request.KeyID != KeyID(key) || request.SealingKey != sealingPublic {
		t.Fatalf("the request holds the key in the clear, the wrong id or the wrong public key: %+v", request)
	}
	opened, err := request.Open(sealing, run)
	if err != nil || opened != key {
		t.Fatalf("opened %q, %v", opened, err)
	}
}

func TestARequestDoesNotOpenForAnotherRunOrAnotherSealingKey(t *testing.T) {
	request, _ := newRequest(t)
	another, _, err := NewSealingKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := request.Open(another, run); err == nil || !strings.Contains(err.Error(), "COSTROID_GATEWAY_PUBLIC_KEY") {
		t.Errorf("a request opened with another sealing key, or said nothing of the variable: %v", err)
	}
	relabelled := request
	relabelled.SealingKey, _ = PublicKey(another)
	if _, err := relabelled.Open(another, run); err == nil {
		t.Error("a request relabelled with another public key opened with that key's private half")
	}
	other := run
	other.Attempt = "2"
	if _, err := request.Open(sealing, other); err == nil {
		t.Error("a request for attempt 1 opened in attempt 2")
	}
	moved := request
	moved.Binding = other
	if _, err := moved.Open(sealing, other); err == nil {
		t.Error("a request relabelled for another attempt opened: the seal is not bound to the run")
	}
}

// TestSealingKeyIsNotDerivedFromAdminKey holds the sealing key to a random
// draw: two draws differ, neither is the pair earlier versions derived from
// the admin key, and a request sealed to that derived pair is refused with a
// sentence that says to run keygen.
func TestSealingKeyIsNotDerivedFromAdminKey(t *testing.T) {
	private, public, err := NewSealingKey()
	if err != nil {
		t.Fatal(err)
	}
	if private == sealing || public == sealingPublic {
		t.Fatal("two draws gave the same sealing key")
	}
	if !strings.HasPrefix(private, SealingKeyPrefix) || strings.HasPrefix(public, SealingKeyPrefix) {
		t.Fatalf("the halves are not told apart: %.30s…, %s", private, public)
	}
	if got, err := PublicKey(private); err != nil || got != public {
		t.Fatalf("the private half gives the public half %s, %v, want %s", got, err, public)
	}
	derived := derivedPublicKey(admin)
	if derived == "" || derived == public || derived == sealingPublic {
		t.Fatalf("the sealing key is the one derived from the admin key: %s", derived)
	}
	request, _, err := NewRequest(derived, run)
	if err != nil {
		t.Fatal(err)
	}
	server := (&liteLLM{}).serve(t)
	mint := Mint{
		Client: NewClient(server.URL), AdminKey: admin, SealingKey: sealing, Run: run, Request: request,
		Budget: decimal.MustParse("0.01"), Lifetime: time.Hour, MaxParallel: 1, MaxRequestTokens: 1,
	}
	if _, err := mint.Register(context.Background()); !errors.Is(err, ErrDerivedSealingKey) || !strings.Contains(err.Error(), "costroid gateway keygen") {
		t.Fatalf("a request sealed to the derived key: %v", err)
	}
	legacy := `{"format":"costroid gateway request v1","repository":"github.com/acme/widgets","run_id":"42","run_attempt":"1","key_id":"x","sealed":"x"}`
	if _, err := ReadRequest(strings.NewReader(legacy)); !errors.Is(err, ErrDerivedSealingKey) {
		t.Fatalf("an earlier version's request: %v", err)
	}
}

func TestThePrivateHalfIsNeverSealedToAsAPublicKey(t *testing.T) {
	_, _, err := NewRequest(sealing, run)
	if err == nil || !strings.Contains(err.Error(), "keygen again") || strings.Contains(err.Error(), sealing) {
		t.Fatalf("a private half in the public variable: %v", err)
	}
	for _, broken := range []string{"", "not-a-key", SealingKeyPrefix + "!!", SealingKeyPrefix + "AAAA"} {
		if _, err := PublicKey(broken); err == nil {
			t.Errorf("PublicKey(%q) was accepted", broken)
		}
	}
}

func TestGatewayURLMustBeHTTPS(t *testing.T) {
	for _, address := range []string{"https://gateway.example", "https://gateway.example:8443/proxy", "http://localhost:4000", "http://127.0.0.1:4000"} {
		if err := CheckURL(address); err != nil {
			t.Errorf("%s was refused: %v", address, err)
		}
	}
	for _, address := range []string{"http://gateway.example", "http://user:secret@gateway.example", "http://127.0.0.2:4000", "http://localhost.example", "ftp://gateway.example", "gateway.example", ""} {
		err := CheckURL(address)
		if err == nil {
			t.Errorf("%s was accepted", address)
		} else if strings.Contains(err.Error(), "secret") {
			t.Errorf("the refusal printed the URL's password: %v", err)
		}
	}
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	plain := NewClient(strings.Replace(server.URL, "127.0.0.1", "gateway.test", 1))
	plain.HTTP = server.Client()
	if _, err := plain.Delete(context.Background(), admin, run.Alias()); err == nil || !strings.Contains(err.Error(), "https") {
		t.Errorf("a call over plain http to a host that is not loopback: %v", err)
	}
	if _, err := plain.Readiness(context.Background()); err == nil {
		t.Error("a readiness probe over plain http to a host that is not loopback was made")
	}
	session := Session{Client: plain, Key: "sk-x", Wait: time.Hour, Poll: time.Hour}
	if _, err := session.Await(context.Background()); err == nil {
		t.Error("Await waited on a URL it must refuse")
	}
	if calls.Load() != 0 {
		t.Errorf("%d calls reached a server over plain http", calls.Load())
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
		Client: NewClient(server.URL), AdminKey: admin, SealingKey: sealing, Run: run, Request: request,
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
	parallel := 1
	number := json.Number(budget)
	return KeyInfo{
		MaxBudget: &number, Expires: json.RawMessage(`"2026-11-06T21:00:00.320000+00:00"`), MaxParallel: &parallel,
		Metadata: map[string]any{MetaRun: run, MetaMaxRequestTokens: "20000"},
	}
}

// expiring is a key that holds the run but for its expiry, as LiteLLM wrote it.
func expiring(expires string) KeyInfo {
	k := info("0.01", "42")
	k.Expires = json.RawMessage(expires)
	return k
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
		"an expiry passed":        expiring(`"2026-11-06T19:00:00Z"`),
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
		Client: NewClient(server.URL), AdminKey: admin, SealingKey: sealing, Run: run, Request: request,
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

func spendServer(t *testing.T, figures ...string) *httptest.Server {
	t.Helper()
	var reads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := min(int(reads.Add(1)), len(figures)) - 1
		w.Write([]byte(`{"info":{"spend":` + figures[n] + `}}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestSpendWaitsWhileTheGatewayStillStoresIt(t *testing.T) {
	server := spendServer(t, "0.0", "0.0", "0.018")
	session := Session{Client: NewClient(server.URL), Key: "sk-x", Poll: time.Millisecond, Settle: time.Second, Stable: time.Second}
	spend, err := session.Spend(context.Background(), decimal.MustParse("0.018"))
	if err != nil || spend.String() != "0.018" {
		t.Fatalf("spend %s, %v", spend, err)
	}
}

func TestSpendStopsOnAFigureThatHasSettledElsewhere(t *testing.T) {
	server := spendServer(t, "0.0175")
	session := Session{Client: NewClient(server.URL), Key: "sk-x", Poll: time.Millisecond, Settle: time.Minute, Stable: 20 * time.Millisecond}
	start := time.Now()
	spend, err := session.Spend(context.Background(), decimal.MustParse("0.018"))
	if err != nil || spend.String() != "0.0175" || time.Since(start) > 10*time.Second {
		t.Fatalf("spend %s, %v, after %s", spend, err, time.Since(start))
	}
}

// TestSpendWithinFloatTolerance takes LiteLLM's float sum as the rows' figure
// when it is within float rounding, reads its exponent form, and still tells a
// real gap from rounding.
func TestSpendWithinFloatTolerance(t *testing.T) {
	total := decimal.MustParse("0.018")
	for _, figure := range []string{"0.018000000000000002", "0.017999999999999999", "1.8e-2", "0.018"} {
		server := spendServer(t, figure)
		session := Session{Client: NewClient(server.URL), Key: "sk-x", Poll: time.Millisecond, Settle: time.Minute, Stable: time.Minute}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		spend, err := session.Spend(ctx, total)
		cancel()
		if err != nil || !SameSpend(spend, total) {
			t.Errorf("%s: spend %s, %v: a figure within float rounding waited out the settle", figure, spend, err)
		}
	}
	small, err := decimal.ParseNumber("1.2e-05")
	if err != nil || small.String() != "0.000012" || !SameSpend(small, decimal.MustParse("0.000012")) {
		t.Errorf("1.2e-05 reads %s, %v", small, err)
	}
	for _, apart := range []string{"0.018000001", "0.017999999", "0.0181", "0"} {
		if SameSpend(decimal.MustParse(apart), total) {
			t.Errorf("%s counts as the same figure as %s", apart, total)
		}
	}
	if !SameSpend(decimal.Decimal{}, decimal.Decimal{}) {
		t.Error("nothing counted for nothing spent is a gap")
	}
	server := spendServer(t, `"a lot"`)
	session := Session{Client: NewClient(server.URL), Key: "sk-x", Poll: time.Millisecond, Settle: time.Second, Stable: time.Second}
	if _, err := session.Spend(context.Background(), total); err == nil {
		t.Error("a spend that is no number was read")
	}
}

// TestExpiryForms reads LiteLLM's own form, observed on 2026-11-07 from
// v1.108.1, and the other forms an expiry can take. A form without a zone is
// a problem that names the field, never a failure of the whole answer.
func TestExpiryForms(t *testing.T) {
	expect := Expect{Budget: decimal.MustParse("0.01"), Run: "42", Now: time.Date(2026, 11, 6, 20, 0, 0, 0, time.UTC)}
	for _, form := range []string{`"2026-11-06T21:00:00.320000+00:00"`, `"2026-11-06T21:00:00Z"`, `"2026-11-06T23:00:00+02:30"`} {
		if grant := Inspect("sk-x", expiring(form), expect); !grant.Holds() {
			t.Errorf("%s: %v", form, grant.Problems)
		}
	}
	for form, want := range map[string]string{
		`"2026-11-06T21:00:00"`: `the key's expires field reads "2026-11-06T21:00:00", which is not an RFC 3339 time with a zone offset`,
		`1794000000`:            "the key's expires field is not a string",
		`null`:                  "the key has no expiry ahead of now",
		``:                      "the key has no expiry ahead of now",
	} {
		grant := Inspect("sk-x", expiring(form), expect)
		if len(grant.Problems) != 1 || !strings.Contains(grant.Problems[0], want) {
			t.Errorf("%s: problems %q, want %q", form, grant.Problems, want)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"info":{"spend":0.0,"expires":"2026-11-06T21:00:00"}}`))
	}))
	defer server.Close()
	if _, err := NewClient(server.URL).Info(context.Background(), "sk-x", ""); err != nil {
		t.Fatalf("an expiry without a zone failed the whole key read: %v", err)
	}
}
