package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/gateway"
	"github.com/Costroid/costroid/internal/gateway/gatewaytest"
)

const (
	issuedToken     = "an-oidc-token-for-these-tests"
	requestToken    = "a-request-token-for-these-tests"
	ingestAudience  = "https://costroid.test"
	attestedKeyID   = "1d4e7a90c3b25f8866a0e4d17c92b35fa8e6d0711d4e7a90c3b25f8866a0e4d1"
	attestedRunID   = "16873409921"
	attestedAttempt = "2"
)

// attestWorld is GitHub's token endpoint and a ledger, both over TLS. The
// ledger takes any post under its credentials path and keeps the last.
type attestWorld struct {
	audience string
	bearer   string
	path     string
	raw      []byte
	body     gateway.Attestation
	ledger   *httptest.Server
	github   *httptest.Server
}

func newAttestWorld(t *testing.T) *attestWorld {
	t.Helper()
	w := &attestWorld{}
	w.github = httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+requestToken || r.URL.Query().Get("api-version") != "2.0" {
			rw.WriteHeader(http.StatusForbidden)
			return
		}
		w.audience = r.URL.Query().Get("audience")
		_ = json.NewEncoder(rw).Encode(map[string]string{"value": issuedToken})
	}))
	w.ledger = httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/ledger/v1/credentials") {
			rw.WriteHeader(http.StatusNotFound)
			return
		}
		w.path, w.bearer = r.URL.Path, r.Header.Get("Authorization")
		w.raw, _ = io.ReadAll(r.Body)
		_ = json.Unmarshal(w.raw, &w.body)
		rw.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(w.github.Close)
	t.Cleanup(w.ledger.Close)
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", w.github.URL+"/token?api-version=2.0")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", requestToken)
	t.Setenv(ingestAudienceVariable, ingestAudience)
	return w
}

func TestMintAttestsTheKeyToTheLedgerUnderTheJobsToken(t *testing.T) {
	w := newAttestWorld(t)
	t.Setenv(ingestURLVariable, w.ledger.URL+"/ledger/v1/rows")
	run := gateway.Binding{Repository: "github.com/acme/widgets", Run: attestedRunID, Attempt: attestedAttempt}
	var stderr bytes.Buffer
	if err := attestKey(context.Background(), w.ledger.Client(), attestedKeyID, run, &stderr); err != nil {
		t.Fatal(err)
	}
	if w.audience != ingestAudience || w.bearer != "Bearer "+issuedToken {
		t.Fatalf("audience %q, bearer %q", w.audience, w.bearer)
	}
	if w.path != "/ledger/v1/credentials" || w.body != gateway.AttestationFor(attestedKeyID, run) {
		t.Fatalf("the ledger read %+v at %s", w.body, w.path)
	}
	if strings.Contains(stderr.String(), issuedToken) {
		t.Fatal("mint printed the token")
	}
}

func TestMintWithNoLedgerAttestsNothing(t *testing.T) {
	w := newAttestWorld(t)
	t.Setenv(ingestURLVariable, "")
	if err := attestKey(context.Background(), w.ledger.Client(), attestedKeyID, gateway.Binding{}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if w.bearer != "" || w.audience != "" {
		t.Fatal("mint called out with no ledger named")
	}
}

func TestMintRefusesToAttestOverPlainHTTPOrWithoutTheIDTokenPermission(t *testing.T) {
	w := newAttestWorld(t)
	t.Setenv(ingestURLVariable, "http://ledger.example/v1/rows")
	err := attestKey(context.Background(), w.ledger.Client(), attestedKeyID, gateway.Binding{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("plain http: %v", err)
	}
	t.Setenv(ingestURLVariable, w.ledger.URL+"/v1/rows")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	err = attestKey(context.Background(), w.ledger.Client(), attestedKeyID, gateway.Binding{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "id-token") {
		t.Fatalf("no id-token permission: %v", err)
	}
}

const (
	gatewayAdmin  = "sk-gateway-admin-for-these-tests"
	gatewayRunID  = "16873409921"
	gatewayAuthor = "octocat"
	gatewayAlias  = "costroid:github.com/acme/widgets:" + gatewayRunID + ":1"
)

// gatewayJob is one GitHub Actions job of the gateway recipe, with a sealing
// key pair of its own and nothing else from the environment it runs in.
type gatewayJob struct {
	dir     string
	sealing string
	public  string
}

func newGatewayJob(t *testing.T) gatewayJob {
	t.Helper()
	job := gatewayJob{dir: t.TempDir()}
	var err error
	if job.sealing, job.public, err = gateway.NewSealingKey(); err != nil {
		t.Fatal(err)
	}
	event := filepath.Join(job.dir, "event.json")
	body := `{"pull_request":{"number":7,"head":{"sha":"` + headCommit + `"},"base":{"sha":"` + baseCommit + `"},"user":{"login":"` + gatewayAuthor + `"}}}`
	if err := os.WriteFile(event, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"GITHUB_ACTIONS": "true", "GITHUB_SERVER_URL": "https://github.com", "GITHUB_REPOSITORY": "acme/widgets",
		"GITHUB_RUN_ID": gatewayRunID, "GITHUB_RUN_ATTEMPT": "1", "GITHUB_EVENT_NAME": "pull_request", "GITHUB_EVENT_PATH": event,
		"GITHUB_TOKEN": "", "GITHUB_API_URL": "", ingestURLVariable: "", gatewayURLVar: "",
		adminKeyVariable: "", sealingKeyVariable: "", publicKeyVariable: "",
	} {
		t.Setenv(name, value)
	}
	return job
}

// request runs the agent's step and returns the key it drew.
func (j gatewayJob) request(t *testing.T) string {
	t.Helper()
	t.Setenv(publicKeyVariable, j.public)
	code, stdout, stderr := execute(t, "gateway", "request", "--out", j.dir)
	if code != 0 {
		t.Fatalf("gateway request exit %d: %s", code, stderr)
	}
	key, err := os.ReadFile(filepath.Join(j.dir, keyFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout) != gateway.KeyID(string(key)) || strings.Contains(stdout+stderr, string(key)) {
		t.Fatalf("gateway request printed %q, %q", stdout, stderr)
	}
	return string(key)
}

// mint runs the minting step against the stub with the flags given.
func (j gatewayJob) mint(t *testing.T, stub *gatewaytest.LiteLLM, flags ...string) (int, string, string) {
	t.Helper()
	t.Setenv(adminKeyVariable, gatewayAdmin)
	t.Setenv(sealingKeyVariable, j.sealing+"\n")
	args := append([]string{"gateway", "mint", "--url", stub.URL, "--budget-usd", "5", "--lifetime-minutes", "30", "--max-request-tokens", "1128000"}, flags...)
	code, stdout, stderr := execute(t, args...)
	for _, secret := range []string{gatewayAdmin, j.sealing} {
		if strings.Contains(stdout+stderr, secret) {
			t.Fatalf("gateway mint printed a secret: %s%s", stdout, stderr)
		}
	}
	return code, stdout, stderr
}

func TestGatewayKeygenWritesThePrivateHalfAloneAndPrintsThePublicHalf(t *testing.T) {
	newGatewayJob(t)
	out := filepath.Join(t.TempDir(), "sealing-key")
	code, stdout, stderr := execute(t, "gateway", "keygen", "--out", out)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	private := strings.TrimSpace(string(data))
	if public, err := gateway.PublicKey(private); err != nil || strings.TrimSpace(stdout) != public {
		t.Fatalf("printed %q, the private half gives %q, %v", stdout, public, err)
	}
	if strings.Contains(stdout+stderr, private) {
		t.Fatal("keygen printed the private half")
	}
	if info, err := os.Stat(out); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the private half's file: %v, %v", info.Mode(), err)
	}
	t.Setenv(sealingKeyVariable, private)
	if code, again, _ := execute(t, "gateway", "public-key"); code != 0 || again != stdout {
		t.Fatalf("public-key printed %q, keygen %q", again, stdout)
	}
	if code, _, stderr := execute(t, "gateway", "keygen", "--out", out); code != exitFailure || !strings.Contains(stderr, "exists") {
		t.Fatalf("keygen over an existing file: exit %d, %s", code, stderr)
	}
	if code, _, stderr := execute(t, "gateway", "keygen"); code != exitFailure || !strings.Contains(stderr, "--out") {
		t.Fatalf("keygen with nowhere to write: exit %d, %s", code, stderr)
	}
}

func TestGatewayPublicKeyWithOnlyTheAdminKeySaysToRunKeygen(t *testing.T) {
	newGatewayJob(t)
	t.Setenv(adminKeyVariable, gatewayAdmin)
	code, stdout, stderr := execute(t, "gateway", "public-key")
	if code != exitFailure || stdout != "" || !strings.Contains(stderr, "costroid gateway keygen") || strings.Contains(stderr, gatewayAdmin) {
		t.Fatalf("exit %d, %q, %q", code, stdout, stderr)
	}
}

func TestGatewayRequestWritesTheKeyForThisUserAndTheSealedRequest(t *testing.T) {
	job := newGatewayJob(t)
	key := job.request(t)
	if info, err := os.Stat(filepath.Join(job.dir, keyFileName)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the key's file: %v, %v", info.Mode(), err)
	}
	file, err := os.Open(filepath.Join(job.dir, gateway.RequestFile))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	request, err := gateway.ReadRequest(file)
	if err != nil {
		t.Fatal(err)
	}
	run := gateway.Binding{Repository: "github.com/acme/widgets", Run: gatewayRunID, Attempt: "1"}
	if opened, err := request.Open(job.sealing, run); err != nil || opened != key {
		t.Fatalf("the request opened to %q, %v", opened, err)
	}
}

func TestGatewayRequestRefusesWhatItCannotSealFor(t *testing.T) {
	job := newGatewayJob(t)
	if code, _, stderr := execute(t, "gateway", "request", "--out", job.dir); code != exitFailure || !strings.Contains(stderr, "keygen printed") {
		t.Errorf("no public key: exit %d, %s", code, stderr)
	}
	t.Setenv(publicKeyVariable, job.sealing)
	if code, _, stderr := execute(t, "gateway", "request", "--out", job.dir); code != exitFailure || strings.Contains(stderr, job.sealing) || !strings.Contains(stderr, "private half") {
		t.Errorf("the private half as the public key: exit %d, %s", code, stderr)
	}
	t.Setenv(publicKeyVariable, job.public)
	t.Setenv("GITHUB_ACTIONS", "")
	if code, _, stderr := execute(t, "gateway", "request", "--out", job.dir); code != exitFailure || !strings.Contains(stderr, "GitHub Actions") {
		t.Errorf("outside GitHub Actions: exit %d, %s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(job.dir, keyFileName)); err == nil {
		t.Error("a refused request left a key behind")
	}
}

func TestGatewayMintRegistersTheKeyWithEveryFlagAndTheEventsAuthor(t *testing.T) {
	job := newGatewayJob(t)
	key := job.request(t)
	stub := gatewaytest.New(t, gatewayAdmin)
	code, stdout, stderr := job.mint(t, stub, "--max-parallel-requests", "2", "--repository-daily-cap-usd", "20", "--author-daily-cap-usd", "5.5",
		"--request", filepath.Join(job.dir, gateway.RequestFile))
	if code != 0 || strings.TrimSpace(stdout) != gateway.KeyID(key) {
		t.Fatalf("exit %d, %q, %s", code, stdout, stderr)
	}
	got, ok := stub.Key(gatewayAlias)
	if !ok || got.Key != key || got.MaxBudget != "5" || got.Duration != "1800s" || got.MaxParallel != 2 {
		t.Fatalf("registered %+v", got)
	}
	for name, want := range map[string]string{
		gateway.MetaPullRequest: "7", gateway.MetaHead: headCommit, gateway.MetaRun: gatewayRunID,
		gateway.MetaAttempt: "1", gateway.MetaMaxRequestTokens: "1128000", gateway.MetaRepository: "github.com/acme/widgets",
	} {
		if got.Metadata[name] != want {
			t.Errorf("metadata %s reads %q, want %q", name, got.Metadata[name], want)
		}
	}
	team, _ := stub.Budget("team", got.Team)
	user, _ := stub.Budget("user", got.User)
	if got.Team != "costroid:github.com/acme/widgets" || team.MaxBudget != "20" || team.Duration != "1d" {
		t.Errorf("team %q holds %+v", got.Team, team)
	}
	if got.User != "costroid:github.com/"+gatewayAuthor || user.MaxBudget != "5.5" || user.Duration != "1d" {
		t.Errorf("user %q holds %+v", got.User, user)
	}
	if stub.Refused() != 0 {
		t.Errorf("the stub refused %d calls", stub.Refused())
	}
}

func TestGatewayMintRefusesFlagsItCannotHoldTheKeyTo(t *testing.T) {
	job := newGatewayJob(t)
	job.request(t)
	stub := gatewaytest.New(t, gatewayAdmin)
	request := filepath.Join(job.dir, gateway.RequestFile)
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"no budget":             {[]string{"--url", stub.URL, "--lifetime-minutes", "30", "--max-request-tokens", "1"}, "--budget-usd"},
		"no lifetime":           {[]string{"--url", stub.URL, "--budget-usd", "5", "--max-request-tokens", "1"}, "--lifetime-minutes"},
		"no largest request":    {[]string{"--url", stub.URL, "--budget-usd", "5", "--lifetime-minutes", "30"}, "--max-request-tokens"},
		"no URL":                {[]string{"--budget-usd", "5", "--lifetime-minutes", "30", "--max-request-tokens", "1"}, "--url"},
		"plain http":            {[]string{"--url", "http://gateway.example", "--budget-usd", "5", "--lifetime-minutes", "30", "--max-request-tokens", "1"}, "https"},
		"a budget with a sign":  {[]string{"--url", stub.URL, "--budget-usd", "-5", "--lifetime-minutes", "30", "--max-request-tokens", "1"}, "--budget-usd"},
		"a zero repository cap": {[]string{"--url", stub.URL, "--budget-usd", "5", "--lifetime-minutes", "30", "--max-request-tokens", "1", "--repository-daily-cap-usd", "0"}, "--repository-daily-cap-usd must be a decimal above 0"},
		"an author cap of text": {[]string{"--url", stub.URL, "--budget-usd", "5", "--lifetime-minutes", "30", "--max-request-tokens", "1", "--author-daily-cap-usd", "five"}, "--author-daily-cap-usd must be a decimal above 0"},
		"an unknown flag":       {[]string{"--budget", "5"}, "flag provided but not defined"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(adminKeyVariable, gatewayAdmin)
			t.Setenv(sealingKeyVariable, job.sealing)
			code, _, stderr := execute(t, append([]string{"gateway", "mint", "--request", request}, c.args...)...)
			if code != exitFailure || !strings.Contains(stderr, c.want) {
				t.Fatalf("exit %d, %s", code, stderr)
			}
		})
	}
	if _, ok := stub.Key(gatewayAlias); ok {
		t.Error("a refused mint registered a key")
	}
	if code, stdout, stderr := execute(t, "gateway", "mint", "-h"); code != 0 || stdout != gatewayUsage || stderr != "" {
		t.Errorf("mint -h: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestGatewayMintNamesTheSecretItLacks(t *testing.T) {
	job := newGatewayJob(t)
	job.request(t)
	stub := gatewaytest.New(t, gatewayAdmin)
	flags := []string{"gateway", "mint", "--url", stub.URL, "--budget-usd", "5", "--lifetime-minutes", "30", "--max-request-tokens", "1", "--request", filepath.Join(job.dir, gateway.RequestFile)}
	if code, _, stderr := execute(t, flags...); code != exitFailure || !strings.Contains(stderr, adminKeyVariable) {
		t.Errorf("no admin key: exit %d, %s", code, stderr)
	}
	t.Setenv(adminKeyVariable, gatewayAdmin)
	if code, _, stderr := execute(t, flags...); code != exitFailure || !strings.Contains(stderr, sealingKeyVariable+" is not set: run costroid gateway keygen") {
		t.Errorf("no sealing key: exit %d, %s", code, stderr)
	}
	other, _, _ := gateway.NewSealingKey()
	t.Setenv(sealingKeyVariable, other)
	if code, _, stderr := execute(t, flags...); code != exitFailure || !strings.Contains(stderr, publicKeyVariable) || strings.Contains(stderr, other) {
		t.Errorf("another sealing key: exit %d, %s", code, stderr)
	}
	if _, ok := stub.Key(gatewayAlias); ok {
		t.Error("a refused mint registered a key")
	}
}

func TestGatewayMintRefusesAnEarlierVersionsRequestAndSaysToRunKeygen(t *testing.T) {
	job := newGatewayJob(t)
	stub := gatewaytest.New(t, gatewayAdmin)
	legacy := filepath.Join(job.dir, "legacy.json")
	body := `{"format":"costroid gateway request v1","repository":"github.com/acme/widgets","run_id":"` + gatewayRunID + `","run_attempt":"1","key_id":"x","sealed":"x"}`
	if err := os.WriteFile(legacy, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := job.mint(t, stub, "--request", legacy)
	if code != exitFailure || !strings.Contains(stderr, "derived from the admin key") || !strings.Contains(stderr, "costroid gateway keygen") {
		t.Fatalf("exit %d, %s", code, stderr)
	}
}

// artifactServer is GitHub's artifact API for one run, holding the request.
func artifactServer(t *testing.T, request []byte) *httptest.Server {
	t.Helper()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	file, _ := writer.Create(gateway.RequestFile)
	file.Write(request)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gh-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/repos/acme/widgets/actions/runs/" + gatewayRunID + "/artifacts":
			w.Write([]byte(`{"artifacts":[{"archive_download_url":"` + server.URL + `/zip","expired":false}]}`))
		case "/zip":
			w.Write(archive.Bytes())
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestGatewayMintReadsTheRequestFromThisRunsArtifact(t *testing.T) {
	job := newGatewayJob(t)
	key := job.request(t)
	data, err := os.ReadFile(filepath.Join(job.dir, gateway.RequestFile))
	if err != nil {
		t.Fatal(err)
	}
	stub := gatewaytest.New(t, gatewayAdmin)
	if code, _, stderr := job.mint(t, stub); code != exitFailure || !strings.Contains(stderr, "GITHUB_TOKEN") {
		t.Fatalf("no token: exit %d, %s", code, stderr)
	}
	t.Setenv("GITHUB_API_URL", artifactServer(t, data).URL)
	t.Setenv("GITHUB_TOKEN", "gh-token")
	code, stdout, stderr := job.mint(t, stub)
	if code != 0 || strings.TrimSpace(stdout) != gateway.KeyID(key) {
		t.Fatalf("exit %d, %q, %s", code, stdout, stderr)
	}
	if got, ok := stub.Key(gatewayAlias); !ok || got.Key != key {
		t.Fatalf("registered %+v", got)
	}
	if code, _, stderr := job.mint(t, stub, "--request", filepath.Join(job.dir, "absent.json")); code != exitFailure || !strings.Contains(stderr, "absent.json") {
		t.Fatalf("a request file that is not there: exit %d, %s", code, stderr)
	}
}

func TestGatewayEventAuthorIsThePullRequestsOrNothing(t *testing.T) {
	dir := t.TempDir()
	for body, want := range map[string]string{
		`{"pull_request":{"user":{"login":"octocat"}}}`: "octocat",
		`{"ref":"refs/heads/main"}`:                     "",
		`not json`:                                      "",
	} {
		path := filepath.Join(dir, "event.json")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := eventAuthor(path); got != want {
			t.Errorf("%s: author %q, want %q", body, got, want)
		}
	}
	if got := eventAuthor(filepath.Join(dir, "absent.json")); got != "" {
		t.Errorf("no event file: author %q", got)
	}
}

func TestGatewayCloseDeletesTheKeyAndASecondCloseFindsItAbsent(t *testing.T) {
	newGatewayJob(t)
	stub := gatewaytest.New(t, gatewayAdmin)
	stub.Add(gatewaytest.Key{Key: "sk-a-run-key-of-sixteen-or-more", Alias: gatewayAlias})
	t.Setenv(adminKeyVariable, gatewayAdmin)
	t.Setenv(gatewayURLVar, stub.URL)
	code, stdout, stderr := execute(t, "gateway", "close")
	if code != 0 || stdout != "deleted\n" {
		t.Fatalf("first close: exit %d, %q, %s", code, stdout, stderr)
	}
	code, stdout, stderr = execute(t, "gateway", "close", "--retry-after", "1ms")
	if code != 0 || stdout != "absent\n" || !strings.Contains(stderr, "late mint") {
		t.Fatalf("second close: exit %d, %q, %s", code, stdout, stderr)
	}
	if strings.Contains(stdout+stderr, gatewayAdmin) {
		t.Fatal("close printed the admin key")
	}
}

// TestGatewayCloseDeletesAKeyMintedAfterItFirstLooked is the late mint: the
// key is registered between close's first delete and its retry.
func TestGatewayCloseDeletesAKeyMintedAfterItFirstLooked(t *testing.T) {
	newGatewayJob(t)
	stub := gatewaytest.New(t, gatewayAdmin)
	stub.BeforeDelete = func(call int) {
		if call == 2 {
			stub.Add(gatewaytest.Key{Key: "sk-a-run-key-of-sixteen-or-more", Alias: gatewayAlias})
		}
	}
	t.Setenv(adminKeyVariable, gatewayAdmin)
	code, stdout, stderr := execute(t, "gateway", "close", "--url", stub.URL, "--retry-after", "1ms")
	if code != 0 || stdout != "deleted\n" {
		t.Fatalf("exit %d, %q, %s", code, stdout, stderr)
	}
	if _, ok := stub.Key(gatewayAlias); ok {
		t.Fatal("the late key is still registered")
	}
}

// TestCloseAttestsSpend closes a run's key with a ledger named. Close reads
// what the gateway counted on the key before it deletes it, waits while the
// figure still moves, and attests the settled figure under the job's token,
// rounded up to the ledger's 12 fractional digits.
func TestCloseAttestsSpend(t *testing.T) {
	newGatewayJob(t)
	w := newAttestWorld(t)
	t.Setenv(ingestURLVariable, w.ledger.URL+"/ledger/v1/rows")
	ctx := context.Background()
	run := gateway.Binding{Repository: "github.com/acme/widgets", Run: gatewayRunID, Attempt: "1"}
	key := "sk-a-run-key-of-sixteen-or-more"
	opened := func(t *testing.T, spend string) (*gatewaytest.LiteLLM, gateway.Counter) {
		t.Helper()
		stub := gatewaytest.New(t, gatewayAdmin)
		stub.Add(gatewaytest.Key{Key: key, Alias: gatewayAlias})
		stub.SetSpend(gatewayAlias, spend)
		return stub, gateway.Counter{
			Client: gateway.NewClient(stub.URL), AdminKey: gatewayAdmin, Run: run,
			Poll: time.Millisecond, Stable: 100 * time.Millisecond, Settle: 10 * time.Second,
		}
	}

	stub, counter := opened(t, "0.012")
	stub.BeforeList = func(call int) {
		if call == 3 {
			stub.SetSpend(gatewayAlias, "0.018000000000000002")
		}
	}
	var stdout, stderr bytes.Buffer
	if err := closeKey(ctx, counter, 0, w.ledger.Client(), &stdout, &stderr); err != nil {
		t.Fatalf("%v: %s", err, stderr.String())
	}
	if _, ok := stub.Key(gatewayAlias); ok || stdout.String() != "deleted\n" {
		t.Fatalf("the key was not deleted: %q", stdout.String())
	}
	var body gateway.SpendAttestation
	if err := json.Unmarshal(w.raw, &body); err != nil {
		t.Fatal(err)
	}
	want := gateway.SpendAttestation{Repository: run.Repository, RunID: run.Run, RunAttempt: run.Attempt, Currency: "USD", Spend: "0.018000000001"}
	if w.path != "/ledger/v1/credentials/"+gateway.KeyID(key)+"/spend" || body != want {
		t.Fatalf("the ledger read %+v at %s, want %+v", body, w.path, want)
	}
	if w.audience != ingestAudience || w.bearer != "Bearer "+issuedToken {
		t.Fatalf("audience %q, bearer %q", w.audience, w.bearer)
	}
	if strings.Contains(stdout.String()+stderr.String(), issuedToken) || strings.Contains(stdout.String()+stderr.String(), key) {
		t.Fatal("close printed the token or the key")
	}

	t.Run("no ledger named reads and posts nothing", func(t *testing.T) {
		t.Setenv(ingestURLVariable, "")
		w.path = ""
		stub, counter := opened(t, "0.5")
		listed := 0
		stub.BeforeList = func(int) { listed++ }
		var stdout bytes.Buffer
		if err := closeKey(ctx, counter, 0, w.ledger.Client(), &stdout, io.Discard); err != nil || stdout.String() != "deleted\n" {
			t.Fatalf("%v, %q", err, stdout.String())
		}
		if listed != 0 || w.path != "" {
			t.Fatalf("listed %d times, posted to %q", listed, w.path)
		}
	})
	t.Run("a spend it cannot read still deletes the key and fails the step", func(t *testing.T) {
		w.path = ""
		stub, counter := opened(t, "-1")
		var stdout bytes.Buffer
		err := closeKey(ctx, counter, 0, w.ledger.Client(), &stdout, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "not read before the delete") || stdout.String() != "deleted\n" {
			t.Fatalf("%v, %q", err, stdout.String())
		}
		if _, ok := stub.Key(gatewayAlias); ok || w.path != "" {
			t.Fatalf("the key is registered, or a figure was posted to %q", w.path)
		}
	})
	t.Run("a ledger over plain http fails the step after the delete", func(t *testing.T) {
		stub, counter := opened(t, "0.5")
		t.Setenv(ingestURLVariable, "http://ledger.example/v1/rows")
		err := closeKey(ctx, counter, 0, w.ledger.Client(), io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "https") {
			t.Fatalf("plain http: %v", err)
		}
		if _, ok := stub.Key(gatewayAlias); ok {
			t.Fatal("the key is still registered")
		}
	})
}

// TestCloseAttestsASettledSpend closes a key whose store holds 0 for longer
// than Stable before the batch with the run's spend lands, as LiteLLM's does
// for up to 15 seconds. Close attests the figure the batch wrote. A figure
// still moving when the wait ends is attested as the last one read, with a
// warning that names it and says it may not have settled.
func TestCloseAttestsASettledSpend(t *testing.T) {
	newGatewayJob(t)
	w := newAttestWorld(t)
	t.Setenv(ingestURLVariable, w.ledger.URL+"/ledger/v1/rows")
	run := gateway.Binding{Repository: "github.com/acme/widgets", Run: gatewayRunID, Attempt: "1"}
	for _, c := range []struct {
		name    string
		written func(since time.Duration, call int) string
		want    string
	}{
		{"the batch lands after a quiet spell", func(since time.Duration, _ int) string {
			if since < 300*time.Millisecond {
				return "0"
			}
			return "0.018"
		}, "0.018"},
		{"a figure still moving when the wait ends", func(_ time.Duration, call int) string {
			return fmt.Sprintf("0.%d1", call)
		}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			w.raw = nil
			stub := gatewaytest.New(t, gatewayAdmin)
			stub.Add(gatewaytest.Key{Key: "sk-a-run-key-of-sixteen-or-more", Alias: gatewayAlias})
			var mu sync.Mutex
			var start time.Time
			written := map[string]bool{}
			stub.BeforeList = func(call int) {
				mu.Lock()
				defer mu.Unlock()
				if call == 1 {
					start = time.Now()
				}
				figure := c.written(time.Since(start), call)
				written[figure] = true
				stub.SetSpend(gatewayAlias, figure)
			}
			counter := gateway.Counter{
				Client: gateway.NewClient(stub.URL), AdminKey: gatewayAdmin, Run: run,
				Poll: 5 * time.Millisecond, Stable: 100 * time.Millisecond, Settle: time.Second,
			}
			var stdout, stderr bytes.Buffer
			if err := closeKey(context.Background(), counter, 0, w.ledger.Client(), &stdout, &stderr); err != nil {
				t.Fatalf("%v: %s", err, stderr.String())
			}
			var body gateway.SpendAttestation
			if err := json.Unmarshal(w.raw, &body); err != nil {
				t.Fatalf("the ledger read no attestation: %v", err)
			}
			warned := strings.Contains(stderr.String(), "may not have settled")
			if c.want != "" {
				if body.Spend != c.want || warned {
					t.Errorf("attested USD %s, want the settled USD %s, with no warning: %s", body.Spend, c.want, stderr.String())
				}
				return
			}
			mu.Lock()
			read := written[body.Spend]
			mu.Unlock()
			if !read || !warned || !strings.Contains(stderr.String(), "its last figure, USD "+body.Spend+",") {
				t.Errorf("attested USD %s, which the store wrote: %t; want it named in a warning that it may not have settled: %s", body.Spend, read, stderr.String())
			}
		})
	}
}

func TestGatewayCloseRefusesWhatItCannotCloseSafely(t *testing.T) {
	newGatewayJob(t)
	stub := gatewaytest.New(t, gatewayAdmin)
	if code, _, stderr := execute(t, "gateway", "close"); code != exitFailure || !strings.Contains(stderr, "--url") {
		t.Errorf("no URL: exit %d, %s", code, stderr)
	}
	t.Setenv(adminKeyVariable, gatewayAdmin)
	if code, _, stderr := execute(t, "gateway", "close", "--url", "http://gateway.example"); code != exitFailure || !strings.Contains(stderr, "https") {
		t.Errorf("plain http: exit %d, %s", code, stderr)
	}
	t.Setenv(adminKeyVariable, "sk-not-the-admin-key")
	code, _, stderr := execute(t, "gateway", "close", "--url", stub.URL)
	if code != exitFailure || !strings.Contains(stderr, "401") || strings.Contains(stderr, "sk-not-the-admin-key") {
		t.Errorf("a wrong admin key: exit %d, %s", code, stderr)
	}
	t.Setenv(adminKeyVariable, "")
	if code, _, stderr := execute(t, "gateway", "close", "--url", stub.URL); code != exitFailure || !strings.Contains(stderr, adminKeyVariable) {
		t.Errorf("no admin key: exit %d, %s", code, stderr)
	}
	t.Setenv(adminKeyVariable, gatewayAdmin)
	t.Setenv("GITHUB_RUN_ATTEMPT", "")
	if code, _, stderr := execute(t, "gateway", "close", "--url", stub.URL); code != exitFailure || !strings.Contains(stderr, "GITHUB_RUN_ATTEMPT") {
		t.Errorf("no attempt: exit %d, %s", code, stderr)
	}
}

func TestGatewayUsage(t *testing.T) {
	if code, _, stderr := execute(t, "gateway"); code != exitFailure || !strings.Contains(stderr, "costroid gateway keygen") {
		t.Errorf("no subcommand: exit %d, %s", code, stderr)
	}
	if code, stdout, _ := execute(t, "gateway", "help"); code != 0 || !strings.Contains(stdout, "https") {
		t.Errorf("help: exit %d, %s", code, stdout)
	}
	if code, _, stderr := execute(t, "gateway", "open"); code != exitFailure || !strings.Contains(stderr, `unknown gateway command "open"`) {
		t.Errorf("an unknown subcommand: exit %d, %s", code, stderr)
	}
}
