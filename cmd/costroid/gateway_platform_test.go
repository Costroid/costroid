package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/gateway"
	"github.com/Costroid/costroid/internal/gateway/gatewaytest"
)

// The values the contract's credential examples name: the key, the run it is
// for, its head, and the tenant the integration token's identity states.
const (
	exampleKeyID  = "f87edbb88470bc20b5b701ece0500b4bd75f061b5547255cb6518fd2eaa4e778"
	exampleHead   = "4b1e0c9d27f35a86e01c7d4f98b2a6e3c5d70f12"
	exampleSpend  = "0.412"
	statementPath = "/api/integration/v0.7/credentials/"
	// contractV07Digest is the SHA-256 of integration/v0.7's openapi.json as
	// published, which the copy in testdata must hold.
	contractV07Digest = "2d95df7d1db7086934585c31a87b6d10f75ab976de76afbb4ea992d1eeea83f2"
)

// exampleRun is the run the examples' key is for.
var exampleRun = gateway.Binding{Repository: "github.com/example/widgets", Run: gatewayRunID, Attempt: "1"}

// compacted is a JSON document without the whitespace between its tokens, as
// a sender writes it.
func compacted(t *testing.T, document []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := json.Compact(&out, document); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// writeKind names one of the two credential writes.
type writeKind string

const (
	attestationWrite writeKind = "attestation"
	spendWrite       writeKind = "spend"
)

// refusalExample is the contract's example body of a refusal of one write.
func refusalExample(code string, kind writeKind) string {
	name := "CredentialRefusal." + code
	if _, shared := contractExamples[name+".spend.json"]; shared && kind == spendWrite {
		return contractExamples[name+".spend.json"].body
	}
	return contractExamples[name+".json"].body
}

// sentWrite is one credential write the platform received.
type sentWrite struct {
	path   string
	header http.Header
	body   []byte
}

// statementPlatform answers as Costroid Platform on integration/v0.7 and as
// GitHub's OIDC token endpoint, over TLS. It checks each credential write in
// the contract's order, keeps the attestations and spends it took, and
// answers with the contract's examples. A token GitHub issued carries its
// audience, which the platform compares whole with the one it builds.
type statementPlatform struct {
	t      *testing.T
	server *httptest.Server
	// mounted serves integration/v0.7. A platform that does not mount it
	// answers 404 NOT_FOUND on every path, as the contract states.
	mounted bool
	// shut answers 404 NOT_FOUND on the credential paths alone.
	shut bool
	// unprotected issues tokens of a run on a branch no rule protects.
	unprotected bool
	// turns answer each credential write in turn, in place of the store, a
	// status and a body, and a Retry-After when retry is set.
	turns []statementTurn
	// redirect answers the request it names, the identity read, the
	// attestation or the spend, with 307 to the same path on this origin.
	redirect, elsewhere string

	bodies map[writeKind]*jsonschema.Schema
	stored map[writeKind]*jsonschema.Schema

	mu           sync.Mutex
	audiences    []string
	tokens       map[string]string
	writes       []sentWrite
	attestations map[string]gateway.KeyStatement
	spends       map[string]string
}

type statementTurn struct {
	status int
	body   string
	retry  string
}

func newStatementPlatform(t *testing.T) *statementPlatform {
	t.Helper()
	p := &statementPlatform{
		t: t, mounted: true,
		bodies: map[writeKind]*jsonschema.Schema{
			attestationWrite: contractSchema(t, contractV07, "CredentialAttestationBody"),
			spendWrite:       contractSchema(t, contractV07, "CredentialSpendBody"),
		},
		stored: map[writeKind]*jsonschema.Schema{
			attestationWrite: contractSchema(t, contractV07, "CredentialAttestation"),
			spendWrite:       contractSchema(t, contractV07, "CredentialSpend"),
		},
		tokens:       map[string]string{},
		attestations: map[string]gateway.KeyStatement{},
		spends:       map[string]string{},
	}
	p.server = httptest.NewTLSServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.server.Close)
	return p
}

// keyPath reads a credential write's key id and kind from its path.
var keyPath = regexp.MustCompile(`^` + regexp.QuoteMeta(statementPath) + `([^/]+)(/spend)?$`)

func (p *statementPlatform) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	if r.URL.Path == "/oidc" {
		audience := r.URL.Query().Get("audience")
		p.audiences = append(p.audiences, audience)
		sum := sha256.Sum256([]byte(audience))
		token := "eyJ" + hex.EncodeToString(sum[:8])
		p.tokens[token] = audience
		w.Write([]byte(`{"value":"` + token + `"}`))
		return
	}
	match := keyPath.FindStringSubmatch(r.URL.Path)
	writes := r.Method == http.MethodPut && match != nil
	asked := "identity"
	switch {
	case writes && match[2] == "":
		asked = string(attestationWrite)
	case writes:
		asked = string(spendWrite)
	}
	if p.redirect != "" && p.redirect == asked && (writes || r.URL.Path == "/api/integration/v0.7/identity") {
		w.Header().Set("Location", p.elsewhere+r.URL.Path)
		w.WriteHeader(http.StatusTemporaryRedirect)
		return
	}
	if writes && p.mounted {
		p.writes = append(p.writes, sentWrite{path: r.URL.Path, header: r.Header.Clone(), body: body})
	}
	switch {
	case !p.mounted || p.shut && writes:
		p.answer(w, http.StatusNotFound, contractExamples["Error.not-mounted.json"].body)
	case r.Method == http.MethodGet && r.URL.Path == "/api/integration/v0.7/identity" && r.Header.Get("Authorization") == "Bearer "+testIntegrationToken:
		w.Write([]byte(contractExamples["Identity.json"].body))
	case writes:
		kind := attestationWrite
		if match[2] != "" {
			kind = spendWrite
		}
		p.write(w, r, kind, match[1], body)
	default:
		p.answer(w, http.StatusNotFound, contractExamples["Error.not-mounted.json"].body)
	}
}

func (p *statementPlatform) answer(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write([]byte(body))
}

// write judges one credential write in the contract's order of checks.
func (p *statementPlatform) write(w http.ResponseWriter, r *http.Request, kind writeKind, key string, body []byte) {
	if len(p.turns) > 0 {
		turn := p.turns[0]
		p.turns = p.turns[1:]
		if turn.retry != "" {
			w.Header().Set("Retry-After", turn.retry)
		}
		p.answer(w, turn.status, turn.body)
		return
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err == nil {
		err = p.bodies[kind].Validate(instance)
	}
	switch {
	case r.Header.Get("Authorization") != "Bearer "+testIntegrationToken:
		p.answer(w, http.StatusUnauthorized, `{"error":{"code":"AUTH_TOKEN_INVALID","message":"The token is absent, unknown, expired or revoked."}}`)
		return
	case err != nil || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(key) || r.Header.Get("Costroid-Run-Token") == "" ||
		r.Header.Get("Content-Type") != "application/json":
		p.answer(w, http.StatusBadRequest, refusalExample("TOOL_INPUT_INVALID", kind))
		return
	case r.Header.Get("Costroid-Installation-Id") != testInstallation:
		p.answer(w, http.StatusConflict, refusalExample("INSTALLATION_NOT_BOUND", kind))
		return
	}
	var stated struct {
		RunID      string `json:"run_id"`
		RunAttempt string `json:"run_attempt"`
		HeadSHA    string `json:"head_sha"`
		Spend      string `json:"spend"`
	}
	json.Unmarshal(body, &stated)
	run := gateway.Binding{Run: stated.RunID, Attempt: stated.RunAttempt}
	audience := "https://app.costroid.com/tenants/" + testTenant + "/credentials/" + key + "/" + stated.RunID + "/" + stated.RunAttempt
	if kind == spendWrite {
		audience += "/spend"
	}
	switch {
	case p.tokens[r.Header.Get("Costroid-Run-Token")] != audience:
		p.answer(w, http.StatusForbidden, refusalExample("RUN_TOKEN_INVALID", kind))
	case p.unprotected:
		p.answer(w, http.StatusForbidden, refusalExample("CREDENTIAL_RUN_UNPROTECTED", kind))
	case kind == attestationWrite:
		p.attest(w, key, gateway.KeyStatement{RunID: stated.RunID, RunAttempt: stated.RunAttempt, HeadSHA: stated.HeadSHA})
	default:
		p.spend(w, key, run, stated.Spend)
	}
}

// attest takes an attestation as the contract's store does: the first is
// stored, the same again is answered as the first was, and another for a key
// this attesting run holds is refused.
func (p *statementPlatform) attest(w http.ResponseWriter, key string, statement gateway.KeyStatement) {
	held, found := p.attestations[key]
	switch {
	case found && held != statement:
		p.answer(w, http.StatusConflict, refusalExample("CREDENTIAL_ATTESTING_RUN_USED", attestationWrite))
		return
	case found:
		p.stores(w, http.StatusOK, attestationWrite, key, "")
		return
	}
	p.attestations[key] = statement
	p.stores(w, http.StatusCreated, attestationWrite, key, "")
}

// spend takes a spend as the contract's store does: none for a key with no
// attestation for the run, the first figure stored, the same decimal string
// answered as the first was, and any other refused.
func (p *statementPlatform) spend(w http.ResponseWriter, key string, run gateway.Binding, figure string) {
	held, found := p.spends[key]
	switch attested, ok := p.attestations[key]; {
	case !ok || attested.RunID != run.Run || attested.RunAttempt != run.Attempt:
		p.answer(w, http.StatusConflict, refusalExample("CREDENTIAL_NOT_ATTESTED", spendWrite))
	case found && held != figure:
		p.answer(w, http.StatusConflict, refusalExample("CREDENTIAL_SPEND_HELD", spendWrite))
	case found:
		p.stores(w, http.StatusOK, spendWrite, key, figure)
	default:
		p.spends[key] = figure
		p.stores(w, http.StatusCreated, spendWrite, key, figure)
	}
}

// stores answers with the contract's example of the stored attestation or
// spend, for the key and the figure the write named.
func (p *statementPlatform) stores(w http.ResponseWriter, status int, kind writeKind, key, figure string) {
	answer := contractExamples["CredentialAttestation.json"].body
	if kind == spendWrite {
		answer = strings.Replace(contractExamples["CredentialSpend.json"].body, `"spend": "`+exampleSpend+`"`, `"spend": "`+figure+`"`, 1)
	}
	answer = strings.ReplaceAll(answer, exampleKeyID, key)
	instance, err := jsonschema.UnmarshalJSON(strings.NewReader(answer))
	if err == nil {
		err = p.stored[kind].Validate(instance)
	}
	if err != nil {
		p.t.Errorf("the platform answers what integration/v0.7 refuses: %v\n%s", err, answer)
	}
	p.answer(w, status, answer)
}

// sent returns the writes the platform received and the audiences GitHub was
// asked for.
func (p *statementPlatform) sent() ([]sentWrite, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]sentWrite(nil), p.writes...), append([]string(nil), p.audiences...)
}

// held puts an attestation of the example's run in the store, as a mint
// before the close would have.
func (p *statementPlatform) held(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.attestations[key] = gateway.KeyStatement{RunID: exampleRun.Run, RunAttempt: exampleRun.Attempt, HeadSHA: exampleHead}
}

// names the job the run that workflow_run started after the example's run,
// with the platform named, a quick pace, and the default transport trusting
// the platform's certificate, which the commands reach it through.
func (p *statementPlatform) names(t *testing.T) {
	t.Helper()
	for name, value := range map[string]string{
		platformVariable:                 p.server.URL,
		platformTokenVariable:            testIntegrationToken,
		platformInstallationVariable:     testInstallation,
		"ACTIONS_ID_TOKEN_REQUEST_URL":   p.server.URL + "/oidc?api-version=2.0",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN": requestToken,
	} {
		t.Setenv(name, value)
	}
	pace := statementPace
	statementPace = verdictPace{interval: time.Millisecond, limit: 2 * time.Second}
	trusted := http.DefaultTransport
	http.DefaultTransport = p.server.Client().Transport
	t.Cleanup(func() {
		statementPace = pace
		http.DefaultTransport = trusted
	})
}

// exampleJob is a job of the gateway recipe for the examples' run: its key
// sealed in that run, of example/widgets, and the job started by workflow_run
// after it, whose event names that run, its attempt and its head.
func exampleJob(t *testing.T) (gatewayJob, string) {
	t.Helper()
	job := newGatewayJob(t)
	t.Setenv("GITHUB_REPOSITORY", "example/widgets")
	key := job.request(t)
	job.triggered(t, func(followed map[string]any) {
		followed["head_sha"] = exampleHead
		followed["head_repository"] = map[string]any{"full_name": "example/widgets"}
	})
	return job, key
}

// mintFor mints the example run's key from the job's sealed request.
func (j gatewayJob) mintFor(t *testing.T, stub *gatewaytest.LiteLLM) (int, string, string) {
	t.Helper()
	return j.mint(t, stub, "--for-triggering-run", "--request", filepath.Join(j.dir, gateway.RequestFile))
}

// closeFor closes the example run's key, with the figure the gateway counted
// on it settled at spend, and returns what close printed on stderr and what
// it returned.
func closeFor(t *testing.T, key, spend string) (string, error) {
	t.Helper()
	run, err := closingRun(true)
	if err != nil || run.Repository != exampleRun.Repository || run.Run != exampleRun.Run {
		t.Fatalf("close reads the key of %+v, %v; want the example's run", run, err)
	}
	stub := gatewaytest.New(t, gatewayAdmin)
	stub.Add(gatewaytest.Key{Key: key, Alias: run.Alias()})
	stub.SetSpend(run.Alias(), spend)
	counter := gateway.Counter{
		Client: gateway.NewClient(stub.URL), AdminKey: gatewayAdmin, Run: run,
		Poll: time.Millisecond, Stable: 20 * time.Millisecond, Settle: 200 * time.Millisecond,
	}
	var stdout, stderr bytes.Buffer
	err = closeKey(context.Background(), counter, 0, &http.Client{Timeout: 10 * time.Second}, true, &stdout, &stderr)
	if _, held := stub.Key(run.Alias()); held || stdout.String() != "deleted\n" {
		t.Fatalf("the key was not deleted: %q, %s", stdout.String(), stderr.String())
	}
	return stderr.String(), err
}

// TestMintAttestsTheKeyToThePlatformUnderAnAudienceThatNamesTheKeyAndTheRun
// mints the key of the run a workflow_run event follows, with Costroid
// Platform named. After registering the key, mint reads the tenant under
// integration/v0.7, asks GitHub for its job's token under the audience that
// names the tenant, the key, the run the key is for and its attempt, never
// the minting run, and sends the contract's own attestation body, byte for
// byte, once. The platform compares the audience whole, as the contract does.
func TestMintAttestsTheKeyToThePlatformUnderAnAudienceThatNamesTheKeyAndTheRun(t *testing.T) {
	platform := newStatementPlatform(t)
	job, key := exampleJob(t)
	platform.names(t)
	code, stdout, stderr := job.mintFor(t, gatewaytest.New(t, gatewayAdmin))
	id := gateway.KeyID(key)
	if code != 0 || strings.TrimSpace(stdout) != id {
		t.Fatalf("exit %d, stdout %q: %s", code, stdout, stderr)
	}
	writes, audiences := platform.sent()
	if len(writes) != 1 || writes[0].path != statementPath+id {
		t.Fatalf("the platform received %d writes, want one to %s%s: %+v", len(writes), statementPath, id, writes)
	}
	var claims struct {
		Audience string `json:"aud"`
	}
	if err := json.Unmarshal(example(t, "CredentialRunTokenClaims.json"), &claims); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(claims.Audience, exampleKeyID, id, 1)
	if len(audiences) != 1 || audiences[0] != want || strings.Contains(audiences[0], mintingRunID) {
		t.Errorf("the job's token was asked for %q, want %q alone", audiences, want)
	}
	if body := writes[0].body; !bytes.Equal(body, compacted(t, example(t, "CredentialAttestationBody.json"))) {
		t.Errorf("the body is not the contract's CredentialAttestationBody: %s", body)
	}
	for name, value := range map[string]string{
		"Authorization":            "Bearer " + testIntegrationToken,
		"Costroid-Installation-Id": testInstallation,
		"Content-Type":             "application/json",
	} {
		if got := writes[0].header.Get(name); got != value {
			t.Errorf("%s: %q, want %q", name, got, value)
		}
	}
	if !strings.Contains(stderr, "attested to Costroid Platform by a run of refs/heads/main\n") {
		t.Errorf("stderr does not say the platform took the attestation:\n%s", stderr)
	}
	for _, token := range []string{testIntegrationToken, writes[0].header.Get("Costroid-Run-Token")} {
		if strings.Contains(stdout+stderr, token) {
			t.Fatalf("mint printed a token: %s%s", stdout, stderr)
		}
	}
}

// TestCloseStatesTheSettledFigureToThePlatformOnce closes the example run's
// key once its figure settled. close sends the contract's own spend body,
// byte for byte, under the spend's own audience, once. A closing job run
// again reads the same figure and is answered as the first was; another
// figure is refused, and the first stands. A figure that has not settled is
// never sent.
func TestCloseStatesTheSettledFigureToThePlatformOnce(t *testing.T) {
	platform := newStatementPlatform(t)
	_, key := exampleJob(t)
	platform.names(t)
	id := gateway.KeyID(key)
	platform.held(id)

	stderr, err := closeFor(t, key, exampleSpend)
	if err != nil {
		t.Fatalf("%v: %s", err, stderr)
	}
	writes, audiences := platform.sent()
	if len(writes) != 1 || writes[0].path != statementPath+id+"/spend" {
		t.Fatalf("the platform received %d writes, want one spend: %+v", len(writes), writes)
	}
	if body := writes[0].body; !bytes.Equal(body, compacted(t, example(t, "CredentialSpendBody.json"))) {
		t.Errorf("the body is not the contract's CredentialSpendBody: %s", body)
	}
	var claims struct {
		Audience string `json:"aud"`
	}
	if err := json.Unmarshal(example(t, "CredentialRunTokenClaims.spend.json"), &claims); err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(claims.Audience, exampleKeyID, id, 1); len(audiences) != 1 || audiences[0] != want {
		t.Errorf("the job's token was asked for %q, want %q alone", audiences, want)
	}
	if !strings.Contains(stderr, "attested the gateway's count of USD 0.412 to Costroid Platform by a run of refs/heads/main\n") {
		t.Errorf("stderr does not say the platform took the figure:\n%s", stderr)
	}

	t.Run("the closing job run again", func(t *testing.T) {
		stderr, err := closeFor(t, key, exampleSpend)
		if err != nil || !strings.Contains(stderr, "Costroid Platform held the gateway's count of USD 0.412 already, from a run of refs/heads/main, so nothing changed\n") {
			t.Fatalf("%v: %s", err, stderr)
		}
	})
	t.Run("another figure", func(t *testing.T) {
		stderr, err := closeFor(t, key, "0.5")
		want := "costroid: Costroid Platform refused the gateway's count: 409 CREDENTIAL_SPEND_HELD. The key holds another decimal string. Nothing was stored\n"
		if !errors.Is(err, errStatementRefused) || !strings.Contains(stderr, want) {
			t.Fatalf("%v: %s", err, stderr)
		}
		if platform.spends[id] != exampleSpend {
			t.Errorf("the platform holds USD %s, want the first figure", platform.spends[id])
		}
	})
	t.Run("a figure that has not settled", func(t *testing.T) {
		before, _ := platform.sent()
		run, _ := closingRun(true)
		stub, counter := settlingGateway(t, run, func(_ time.Duration, call int) string { return "0." + itoa(call) + "1" })
		stub.Add(gatewaytest.Key{Key: key, Alias: run.Alias()})
		var stdout, stderr bytes.Buffer
		if err := closeKey(context.Background(), counter, 0, &http.Client{Timeout: 10 * time.Second}, true, &stdout, &stderr); err != nil {
			t.Fatalf("%v: %s", err, stderr.String())
		}
		if after, _ := platform.sent(); len(after) != len(before) || !strings.Contains(stderr.String(), "may not have settled") {
			t.Fatalf("an unsettled figure reached the platform, %d writes: %s", len(after)-len(before), stderr.String())
		}
	})
}

// TestAPlatformThatDoesNotOfferThePathIsWarnedOfAndTheStepGoesOn mints and
// closes against a platform that speaks no integration/v0.7, and one whose
// integration paths are shut. Each answers 404 NOT_FOUND with the body the
// contract quotes, mint and close each warn in one line, and the step exits 0.
func TestAPlatformThatDoesNotOfferThePathIsWarnedOfAndTheStepGoesOn(t *testing.T) {
	for name, offer := range map[string]func(p *statementPlatform){
		"a platform of an earlier version": func(p *statementPlatform) { p.mounted = false },
		"a platform whose paths are shut":  func(p *statementPlatform) { p.shut = true },
	} {
		t.Run(name, func(t *testing.T) {
			platform := newStatementPlatform(t)
			offer(platform)
			job, key := exampleJob(t)
			platform.names(t)
			code, _, stderr := job.mintFor(t, gatewaytest.New(t, gatewayAdmin))
			if code != 0 || strings.Count(stderr, keyWrite.notOffered) != 1 || strings.Contains(stderr, "attested to Costroid Platform") {
				t.Errorf("mint: exit %d: %s", code, stderr)
			}
			stderr, err := closeFor(t, key, exampleSpend)
			if err != nil || strings.Count(stderr, countWrite.notOffered) != 1 || strings.Contains(stderr, "to Costroid Platform") {
				t.Errorf("close: %v: %s", err, stderr)
			}
			if writes, _ := platform.sent(); platform.shut != (len(writes) == 2) {
				t.Errorf("the platform received %d writes", len(writes))
			}
		})
	}
	for _, line := range []string{keyWrite.notOffered, countWrite.notOffered} {
		if strings.Count(line, "\n") != 1 || !strings.HasPrefix(line, "costroid: warning: ") {
			t.Errorf("the warning is not one line: %q", line)
		}
	}
}

// TestAnUnprotectedRunIsWarnedOfAtThePlatformToo mints and closes from a run
// whose token states that the default branch is not protected. The platform
// refuses both writes with CREDENTIAL_RUN_UNPROTECTED, and mint and close give
// the ledger's own warning, word for word, and go on: the rows read windowed.
func TestAnUnprotectedRunIsWarnedOfAtThePlatformToo(t *testing.T) {
	platform := newStatementPlatform(t)
	platform.unprotected = true
	job, key := exampleJob(t)
	platform.names(t)
	code, _, stderr := job.mintFor(t, gatewaytest.New(t, gatewayAdmin))
	if code != 0 || !strings.Contains(stderr, unprotectedBranchKey) || strings.Contains(stderr, "CREDENTIAL_RUN_UNPROTECTED") {
		t.Errorf("mint: exit %d: %s", code, stderr)
	}
	stderr, err := closeFor(t, key, exampleSpend)
	if err != nil || !strings.Contains(stderr, unprotectedBranchCount) || strings.Contains(stderr, "CREDENTIAL_RUN_UNPROTECTED") {
		t.Errorf("close: %v: %s", err, stderr)
	}
	if writes, _ := platform.sent(); len(writes) != 2 {
		t.Errorf("the platform received %d writes, want the attestation and the spend", len(writes))
	}
}

// TestEachRefusalOfAStatementNamesItsCodeAndTheContractsSentence answers each
// write with each refusal integration/v0.7 lists for it, in the contract's
// own example body. Each is printed with its code and the contract's sentence
// for it, nothing of the platform's own message reaches the log, and the step
// exits 1. CREDENTIAL_RUN_UNPROTECTED, INSTALLATION_NOT_BOUND and NOT_FOUND
// warn instead, as their own tests hold.
func TestEachRefusalOfAStatementNamesItsCodeAndTheContractsSentence(t *testing.T) {
	codes := refusalCodes(t)
	codes = append(codes, "AUTH_TOKEN_INVALID")
	status := map[string]int{
		"AUTH_TOKEN_INVALID": http.StatusUnauthorized, "AUTH_TOKEN_SCOPE": http.StatusForbidden, "TOOL_INPUT_INVALID": http.StatusBadRequest,
		"INSTALLATION_NOT_BOUND": http.StatusConflict, "RUN_TOKEN_INVALID": http.StatusForbidden, "RUN_TOKEN_OWNER_MISMATCH": http.StatusForbidden,
		"INSTALLATION_PROOF_KEYS_UNAVAILABLE": http.StatusServiceUnavailable, "CREDENTIAL_ATTESTING_RUN_USED": http.StatusConflict,
		"CREDENTIAL_ATTESTED_ELSEWHERE": http.StatusConflict, "CREDENTIAL_NOT_ATTESTED": http.StatusConflict, "CREDENTIAL_SPEND_HELD": http.StatusConflict,
	}
	platform := newStatementPlatform(t)
	_, key := exampleJob(t)
	platform.names(t)
	id := gateway.KeyID(key)
	for _, code := range codes {
		if code == gateway.CodeRunUnprotected || code == installationNotBound {
			continue
		}
		for _, write := range []struct {
			kind  writeKind
			write credentialWrite
			state func(k *keyPlatform) error
		}{
			{attestationWrite, keyWrite, func(k *keyPlatform) error { return k.stateKey(context.Background(), id, exampleRun, exampleHead) }},
			{spendWrite, countWrite, func(k *keyPlatform) error {
				return k.stateSpend(context.Background(), gateway.Count{KeyID: id, Spend: decimal.MustParse(exampleSpend)}, exampleRun)
			}},
		} {
			t.Run(code+" "+string(write.kind), func(t *testing.T) {
				body := refusalExample(code, write.kind)
				if code == "AUTH_TOKEN_INVALID" {
					body = `{"error":{"code":"AUTH_TOKEN_INVALID","message":"::error::The token is absent."}}`
				}
				if body == "" || status[code] == 0 {
					t.Fatalf("the contract lists %s, and the test holds no example of it", code)
				}
				platform.turns = []statementTurn{{status: status[code], body: body}}
				if code == "INSTALLATION_PROOF_KEYS_UNAVAILABLE" {
					platform.turns = append(platform.turns, platform.turns[0])
					platform.turns[0].retry, platform.turns[1].retry = "1", "60"
				}
				var stderr bytes.Buffer
				stating, err := platformFor(true, &http.Client{Timeout: 10 * time.Second}, write.write, &stderr)
				if err != nil {
					t.Fatal(err)
				}
				err = write.state(stating)
				sentence, known := write.write.sentences[code]
				want := "costroid: Costroid Platform refused " + write.write.what + ": " + itoa(status[code]) + " " + code + ". " + sentence + ". Nothing was stored\n"
				if !known || !errors.Is(err, errStatementRefused) || stderr.String() != want {
					t.Errorf("%v, stderr %q, want %q", err, stderr.String(), want)
				}
				var refused struct {
					Error struct {
						Message string `json:"message"`
					} `json:"error"`
				}
				if json.Unmarshal([]byte(body), &refused); refused.Error.Message != sentence+"." && strings.Contains(stderr.String(), refused.Error.Message) {
					t.Errorf("the platform's own message reached the log: %q", stderr.String())
				}
			})
		}
	}
	if len(platform.turns) != 0 {
		t.Errorf("%d answers were never asked for", len(platform.turns))
	}
}

// refusalCodes are the codes integration/v0.7's CredentialRefusalCode lists.
func refusalCodes(t *testing.T) []string {
	t.Helper()
	var contract struct {
		Components struct {
			Schemas struct {
				Codes struct {
					Enum []string `json:"enum"`
				} `json:"CredentialRefusalCode"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal([]byte(readFile(t, contractCopy(contractV07))), &contract); err != nil {
		t.Fatal(err)
	}
	if len(contract.Components.Schemas.Codes.Enum) < 10 {
		t.Fatalf("the contract lists %d refusals of a credential write", len(contract.Components.Schemas.Codes.Enum))
	}
	return contract.Components.Schemas.Codes.Enum
}

// TestAStatementIsSentAgainOnlyWithinTheBound answers a write 503
// INSTALLATION_PROOF_KEYS_UNAVAILABLE with the contract's Retry-After of 60
// s, on a clock where a second lasts 10 ms. Under the bound of 150 s the
// write goes again under the same token once Retry-After has passed, and
// stores; a platform still answering 503 gets the write three times in all,
// and is then named with its code and sentence, and the step exits 1. A bound
// no longer than Retry-After sends the write once and never again. A platform
// that cannot be reached at all is no refusal, and the step exits 125.
func TestAStatementIsSentAgainOnlyWithinTheBound(t *testing.T) {
	platform := newStatementPlatform(t)
	job, _ := exampleJob(t)
	platform.names(t)
	second := retryAfterSecond
	retryAfterSecond = 10 * time.Millisecond
	t.Cleanup(func() { retryAfterSecond = second })
	statementPace = verdictPace{interval: time.Millisecond, limit: 150 * retryAfterSecond}
	unavailable := statementTurn{status: http.StatusServiceUnavailable, body: refusalExample("INSTALLATION_PROOF_KEYS_UNAVAILABLE", attestationWrite), retry: "60"}
	platform.turns = []statementTurn{unavailable}
	started := time.Now()
	code, _, stderr := job.mintFor(t, gatewaytest.New(t, gatewayAdmin))
	writes, audiences := platform.sent()
	if code != 0 || len(writes) != 2 || len(audiences) != 1 || !strings.Contains(stderr, "attested to Costroid Platform") {
		t.Fatalf("exit %d, %d writes under %d tokens: %s", code, len(writes), len(audiences), stderr)
	}
	if writes[0].header.Get("Costroid-Run-Token") != writes[1].header.Get("Costroid-Run-Token") || time.Since(started) < 60*retryAfterSecond {
		t.Errorf("the write went again under another token, or before Retry-After")
	}

	want := "costroid: Costroid Platform refused the key's attestation: 503 INSTALLATION_PROOF_KEYS_UNAVAILABLE. GitHub's key set cannot be read. Retry inside the run token's window. Nothing was stored\n"
	for name, check := range map[string]struct {
		limit time.Duration
		sent  int
	}{
		"the bound of 150 s":       {150 * retryAfterSecond, 3},
		"a bound of Retry-After's": {60 * retryAfterSecond, 1},
	} {
		statementPace.limit = check.limit
		before, _ := platform.sent()
		platform.turns = []statementTurn{unavailable, unavailable, unavailable, unavailable}
		code, _, stderr = job.mintFor(t, gatewaytest.New(t, gatewayAdmin))
		after, _ := platform.sent()
		if code != exitRejected || !strings.Contains(stderr, want) || len(after)-len(before) != check.sent {
			t.Errorf("%s: exit %d, the write sent %d times, want %d: %s", name, code, len(after)-len(before), check.sent, stderr)
		}
	}
	platform.turns = nil

	platform.server.Close()
	code, _, stderr = job.mintFor(t, gatewaytest.New(t, gatewayAdmin))
	if code != exitFailure || !strings.Contains(stderr, keyWrite.unstated) {
		t.Errorf("a platform that cannot be reached: exit %d: %s", code, stderr)
	}
}

// TestTheStatementsWaitTheirOwnBound holds the two credential writes to a
// bound of 150 s of their own, every 2 s or after Retry-After, and the other
// senders to export's 60 s.
func TestTheStatementsWaitTheirOwnBound(t *testing.T) {
	if want := (verdictPace{interval: 2 * time.Second, limit: 150 * time.Second, every: 10 * time.Second}); statementPace != want || retryAfterSecond != time.Second {
		t.Errorf("the statements wait %+v, a second of Retry-After is %s; want %+v and 1s", statementPace, retryAfterSecond, want)
	}
	if ingestPace.limit != 60*time.Second {
		t.Errorf("the other senders wait %s, want 60s", ingestPace.limit)
	}
}

// TestAnUnboundInstallationIsWarnedOfAndTheStepGoesOn mints and closes
// against a platform that holds no binding of the installation. It is a state
// of the account and not a fault of the run: mint and close warn, name
// costroid connect and the page, and exit 0.
func TestAnUnboundInstallationIsWarnedOfAndTheStepGoesOn(t *testing.T) {
	platform := newStatementPlatform(t)
	job, key := exampleJob(t)
	platform.names(t)
	line := func(what string) string {
		return "costroid: warning: Costroid Platform refused " + what + ": 409 INSTALLATION_NOT_BOUND. The tenant holds no binding of " + testInstallation +
			", so the rows of the run the key serves read windowed there until it is bound: run costroid connect, as https://docs.costroid.com/connect shows\n"
	}
	platform.turns = []statementTurn{{status: http.StatusConflict, body: refusalExample("INSTALLATION_NOT_BOUND", attestationWrite)}}
	code, stdout, stderr := job.mintFor(t, gatewaytest.New(t, gatewayAdmin))
	if code != 0 || strings.TrimSpace(stdout) != gateway.KeyID(key) || !strings.Contains(stderr, line(keyWrite.what)) {
		t.Errorf("mint: exit %d: %s", code, stderr)
	}
	platform.turns = []statementTurn{{status: http.StatusConflict, body: refusalExample("INSTALLATION_NOT_BOUND", spendWrite)}}
	if stderr, err := closeFor(t, key, exampleSpend); err != nil || !strings.Contains(stderr, line(countWrite.what)) {
		t.Errorf("close: %v: %s", err, stderr)
	}
	if strings.Contains(stderr, "github-owner:7204411.\"") {
		t.Errorf("the platform's own message reached the log: %s", stderr)
	}
}

// refusingLedger is a ledger of the team's own that answers every statement
// with one status and body, over TLS.
func refusingLedger(t *testing.T, status int, body string) string {
	t.Helper()
	ledger := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(ledger.Close)
	return ledger.URL + "/ledger/v1/rows"
}

// TestAnUnprotectedRunIsWarnedOfOnceWhenTheLedgerRefusesItToo mints and
// closes with a ledger and Costroid Platform named, where both refuse the
// run because its branch is not protected. The warning is one line for both.
func TestAnUnprotectedRunIsWarnedOfOnceWhenTheLedgerRefusesItToo(t *testing.T) {
	platform := newStatementPlatform(t)
	platform.unprotected = true
	job, key := exampleJob(t)
	platform.names(t)
	t.Setenv(ingestURLVariable, refusingLedger(t, http.StatusForbidden, `{"error":{"code":"CREDENTIAL_RUN_UNPROTECTED"}}`))
	code, _, stderr := job.mintFor(t, gatewaytest.New(t, gatewayAdmin))
	if code != 0 || strings.Count(stderr, unprotectedBranchKey) != 1 {
		t.Errorf("mint: exit %d: %s", code, stderr)
	}
	if stderr, err := closeFor(t, key, exampleSpend); err != nil || strings.Count(stderr, unprotectedBranchCount) != 1 {
		t.Errorf("close: %v: %s", err, stderr)
	}
	if writes, _ := platform.sent(); len(writes) != 2 {
		t.Errorf("the platform received %d writes, want both statements", len(writes))
	}
}

// TestALedgerThatRefusesAStatementExitsOne mints and closes with a ledger of
// the team's own that took each statement and refused it: the step names the
// refusal and exits 1, as for Costroid Platform's refusal. A ledger that
// cannot be reached exits 125, and so does the step when the platform could
// not be reached and the ledger refused.
func TestALedgerThatRefusesAStatementExitsOne(t *testing.T) {
	platform := newStatementPlatform(t)
	job, key := exampleJob(t)
	platform.names(t)
	t.Setenv(ingestURLVariable, refusingLedger(t, http.StatusConflict, `{"error":{"code":"CREDENTIAL_ATTESTED_ELSEWHERE"}}`))
	code, _, stderr := job.mintFor(t, gatewaytest.New(t, gatewayAdmin))
	if code != exitRejected || !strings.Contains(stderr, "costroid: the key is registered and not attested, so the run's rows cannot read exact: gateway: the ledger's attestation answered 409: CREDENTIAL_ATTESTED_ELSEWHERE\n") ||
		!strings.Contains(stderr, "attested to Costroid Platform") {
		t.Errorf("mint: exit %d: %s", code, stderr)
	}
	stderr, err := closeFor(t, key, exampleSpend)
	if !refusedByLedger(err) || !strings.Contains(err.Error(), "the ledger's spend attestation answered 409: CREDENTIAL_ATTESTED_ELSEWHERE") ||
		!strings.Contains(stderr, "attested the gateway's count of USD 0.412 to Costroid Platform") {
		t.Errorf("close: %v: %s", err, stderr)
	}

	unreachable := httptest.NewTLSServer(http.NotFoundHandler())
	unreachable.Close()
	t.Setenv(ingestURLVariable, unreachable.URL+"/v1/rows")
	if code, _, stderr := job.mintFor(t, gatewaytest.New(t, gatewayAdmin)); code != exitFailure {
		t.Errorf("a ledger that cannot be reached: exit %d: %s", code, stderr)
	}

	t.Setenv(ingestURLVariable, refusingLedger(t, http.StatusConflict, `{"error":{"code":"CREDENTIAL_ATTESTED_ELSEWHERE"}}`))
	t.Setenv(platformVariable, unreachable.URL)
	if code, _, stderr := job.mintFor(t, gatewaytest.New(t, gatewayAdmin)); code != exitFailure || !strings.Contains(stderr, "409: CREDENTIAL_ATTESTED_ELSEWHERE") {
		t.Errorf("a ledger that refused and a platform that cannot be reached: exit %d: %s", code, stderr)
	}
}

// TestAStatementNamesTheAttemptItIsFor mints and closes the key of the
// example run's second attempt. The audience of each write and its body name
// attempt 2, the attempt the key is for, so a token minted for one attempt
// fits no other.
func TestAStatementNamesTheAttemptItIsFor(t *testing.T) {
	platform := newStatementPlatform(t)
	job := newGatewayJob(t)
	t.Setenv("GITHUB_REPOSITORY", "example/widgets")
	t.Setenv("GITHUB_RUN_ATTEMPT", "2")
	key := job.request(t)
	job.triggered(t, func(followed map[string]any) {
		followed["run_attempt"] = 2
		followed["head_sha"] = exampleHead
		followed["head_repository"] = map[string]any{"full_name": "example/widgets"}
	})
	platform.names(t)
	if code, _, stderr := job.mintFor(t, gatewaytest.New(t, gatewayAdmin)); code != 0 {
		t.Fatalf("mint: exit %d: %s", code, stderr)
	}
	if stderr, err := closeFor(t, key, exampleSpend); err != nil {
		t.Fatalf("close: %v: %s", err, stderr)
	}
	id := gateway.KeyID(key)
	writes, audiences := platform.sent()
	want := "https://app.costroid.com/tenants/" + testTenant + "/credentials/" + id + "/" + exampleRun.Run + "/2"
	if len(audiences) != 2 || audiences[0] != want || audiences[1] != want+"/spend" {
		t.Errorf("the job's tokens were asked for %q, want %q and its /spend", audiences, want)
	}
	bodies := []string{
		`{"run_id":"16873409921","run_attempt":"2","head_sha":"` + exampleHead + `"}`,
		`{"run_id":"16873409921","run_attempt":"2","currency":"USD","spend":"0.412"}`,
	}
	if len(writes) != 2 || string(writes[0].body) != bodies[0] || string(writes[1].body) != bodies[1] {
		t.Fatalf("the platform received %+v, want %q", writes, bodies)
	}
}

// TestNoRequestFollowsARedirect answers the identity read, the attestation
// and the spend, each in turn, with 307 to another origin. The commands
// follow none, so the other origin sees no request, and neither the
// integration token nor a run token: the step names the answer and exits 1.
func TestNoRequestFollowsARedirect(t *testing.T) {
	var seen atomic.Int64
	elsewhere := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { seen.Add(1) }))
	t.Cleanup(elsewhere.Close)
	for _, asked := range []string{"identity", string(attestationWrite), string(spendWrite)} {
		t.Run(asked, func(t *testing.T) {
			platform := newStatementPlatform(t)
			platform.redirect, platform.elsewhere = asked, elsewhere.URL
			job, key := exampleJob(t)
			platform.names(t)
			platform.held(gateway.KeyID(key))
			what := "the identity request"
			if asked == string(attestationWrite) {
				what = keyWrite.what
			}
			code, _, stderr := job.mintFor(t, gatewaytest.New(t, gatewayAdmin))
			if want := "costroid: Costroid Platform refused " + what + ": 307 with no refusal code\n"; asked != string(spendWrite) && (code != exitRejected || !strings.Contains(stderr, want)) {
				t.Errorf("mint: exit %d, want 1 and %q: %s", code, want, stderr)
			}
			if asked == string(spendWrite) {
				stderr, err := closeFor(t, key, exampleSpend)
				if want := "costroid: Costroid Platform refused " + countWrite.what + ": 307 with no refusal code\n"; !errors.Is(err, errStatementRefused) || !strings.Contains(stderr, want) {
					t.Errorf("close: %v, want %q: %s", err, want, stderr)
				}
			}
			if n := seen.Load(); n != 0 {
				t.Fatalf("the other origin received %d requests", n)
			}
		})
	}
}

// TestOnlyTheContractsAnswerForAPathNotOfferedWarns answers each write 404
// with another body than the contract's NOT_FOUND: a code of another kind,
// and a page with no code, as a proxy in front of the platform gives. Neither
// is the platform's statement that it does not offer the write, so each is a
// refusal named as it came, and the step exits 1.
func TestOnlyTheContractsAnswerForAPathNotOfferedWarns(t *testing.T) {
	platform := newStatementPlatform(t)
	_, key := exampleJob(t)
	platform.names(t)
	id := gateway.KeyID(key)
	for name, check := range map[string]struct {
		body, line string
	}{
		"a code of another kind": {`{"error":{"code":"MERGE_FACT_NOT_FOUND","message":"No such route."}}`, "404 MERGE_FACT_NOT_FOUND\n"},
		"a page with no code":    {`<html><body>Not Found</body></html>`, "404 with no refusal code\n"},
	} {
		t.Run(name, func(t *testing.T) {
			platform.turns = []statementTurn{{status: http.StatusNotFound, body: check.body}}
			var stderr bytes.Buffer
			stating, err := platformFor(true, &http.Client{Timeout: 10 * time.Second}, keyWrite, &stderr)
			if err != nil {
				t.Fatal(err)
			}
			err = stating.stateKey(context.Background(), id, exampleRun, exampleHead)
			if want := "costroid: Costroid Platform refused " + keyWrite.what + ": " + check.line; !errors.Is(err, errStatementRefused) || stderr.String() != want {
				t.Errorf("%v, stderr %q, want %q", err, stderr.String(), want)
			}
		})
	}
}

// TestAStatementRefusedByThePlatformFailsTheStep runs mint and close as the
// recipe does, against a platform that holds the key for another run: the
// command exits 1 and names the code, and the key stays registered for the
// run, or is deleted, as without a platform.
func TestAStatementRefusedByThePlatformFailsTheStep(t *testing.T) {
	platform := newStatementPlatform(t)
	job, key := exampleJob(t)
	platform.names(t)
	platform.turns = []statementTurn{{status: http.StatusConflict, body: refusalExample("CREDENTIAL_ATTESTED_ELSEWHERE", attestationWrite)}}
	stub := gatewaytest.New(t, gatewayAdmin)
	code, stdout, stderr := job.mintFor(t, stub)
	if _, registered := stub.Key(exampleRun.Alias()); code != exitRejected || !registered || strings.TrimSpace(stdout) != gateway.KeyID(key) ||
		!strings.Contains(stderr, "409 CREDENTIAL_ATTESTED_ELSEWHERE. The key is held by another attesting run. The first one stands. Nothing was stored\n") {
		t.Errorf("exit %d, registered %t, stdout %q: %s", code, registered, stdout, stderr)
	}
	if strings.Contains(stderr, "The key is attested for run") {
		t.Errorf("the platform's own message reached the log: %s", stderr)
	}
}

// TestMintStatesNothingToThePlatformFromItsOwnRun mints in the agent's own
// workflow, without --for-triggering-run, with Costroid Platform named. The
// platform takes no statement from a pull request's own run, so mint sends
// it nothing, asks GitHub for no token, and warns.
func TestMintStatesNothingToThePlatformFromItsOwnRun(t *testing.T) {
	platform := newStatementPlatform(t)
	job := newGatewayJob(t)
	job.request(t)
	platform.names(t)
	code, _, stderr := job.mint(t, gatewaytest.New(t, gatewayAdmin), "--request", filepath.Join(job.dir, gateway.RequestFile))
	if writes, audiences := platform.sent(); code != 0 || len(writes) != 0 || len(audiences) != 0 || !strings.Contains(stderr, keyWrite.workflowRun) {
		t.Errorf("exit %d, %d writes, audiences %q: %s", code, len(writes), audiences, stderr)
	}
}

// TestTheSendersHoldTheContractsOwnExamples holds the copy of integration/v0.7
// to the published file's SHA-256, and each example the tests send and read
// to its own. The attestation and the spend of the examples' key, run, head
// and figure are the contract's bodies, byte for byte, under the audiences
// its claims name; the platform's answers are its stored examples, which the
// sender reads. Each rejected vector a sender could produce is not: a body
// of another form, a key id of another form, an audience of another write,
// path or prefix, a later attempt's restatement of an earlier batch, a
// token_verified row of another pull request, and a row from a developer's
// machine.
func TestTheSendersHoldTheContractsOwnExamples(t *testing.T) {
	if sum := sha256.Sum256([]byte(readFile(t, contractCopy(contractV07)))); hex.EncodeToString(sum[:]) != contractV07Digest {
		t.Fatalf("testdata holds another integration/v0.7 than the published file")
	}
	for name := range contractExamples {
		example(t, name)
	}

	key, err := gateway.NewKeyStatement(exampleKeyID, exampleRun, exampleHead)
	if err != nil {
		t.Fatal(err)
	}
	spend, err := gateway.NewSpendStatement(gateway.Count{KeyID: exampleKeyID, Spend: decimal.MustParse(exampleSpend)}, exampleRun)
	if err != nil {
		t.Fatal(err)
	}
	for name, sent := range map[string]any{"CredentialAttestationBody.json": key, "CredentialSpendBody.json": spend} {
		if data, _ := json.Marshal(sent); !bytes.Equal(data, compacted(t, example(t, name))) {
			t.Errorf("the sender writes %s where the contract's %s is %s", data, name, compacted(t, example(t, name)))
		}
	}
	audiences := map[writeKind]string{
		attestationWrite: gateway.KeyAudience(testTenant, exampleKeyID, exampleRun),
		spendWrite:       gateway.SpendAudience(testTenant, exampleKeyID, exampleRun),
	}
	for kind, name := range map[writeKind]string{attestationWrite: "CredentialRunTokenClaims.json", spendWrite: "CredentialRunTokenClaims.spend.json"} {
		var claims struct {
			Audience string `json:"aud"`
		}
		if err := json.Unmarshal(example(t, name), &claims); err != nil || claims.Audience != audiences[kind] {
			t.Errorf("the sender asks for %q where %s names %q", audiences[kind], name, claims.Audience)
		}
	}
	var keyID string
	if err := json.Unmarshal(example(t, "KeyId.json"), &keyID); err != nil || keyID != exampleKeyID {
		t.Fatalf("KeyId.json names %q", keyID)
	}

	t.Run("the examples on the wire", func(t *testing.T) {
		platform := newStatementPlatform(t)
		platform.names(t)
		var stderr bytes.Buffer
		stating, err := platformFor(true, &http.Client{Timeout: 10 * time.Second}, keyWrite, &stderr)
		if err != nil {
			t.Fatal(err)
		}
		if err := stating.stateKey(context.Background(), exampleKeyID, exampleRun, exampleHead); err != nil {
			t.Fatal(err)
		}
		if err := stating.stateSpend(context.Background(), gateway.Count{KeyID: exampleKeyID, Spend: decimal.MustParse(exampleSpend)}, exampleRun); err != nil {
			t.Fatal(err)
		}
		writes, asked := platform.sent()
		if len(writes) != 2 || writes[0].path != statementPath+exampleKeyID || writes[1].path != statementPath+exampleKeyID+"/spend" {
			t.Fatalf("the platform received %+v", writes)
		}
		if !bytes.Equal(writes[0].body, compacted(t, example(t, "CredentialAttestationBody.json"))) || !bytes.Equal(writes[1].body, compacted(t, example(t, "CredentialSpendBody.json"))) {
			t.Errorf("the bodies are not the contract's: %s, %s", writes[0].body, writes[1].body)
		}
		if len(asked) != 2 || asked[0] != audiences[attestationWrite] || asked[1] != audiences[spendWrite] {
			t.Errorf("the job's tokens were asked for %q", asked)
		}
		if want := "attested to Costroid Platform by a run of refs/heads/main\nattested the gateway's count of USD 0.412 to Costroid Platform by a run of refs/heads/main\n"; !strings.HasSuffix(stderr.String(), want) {
			t.Errorf("stderr %q, want it to end %q", stderr.String(), want)
		}
	})

	t.Run("rejected bodies", func(t *testing.T) {
		attestation, spending := contractSchema(t, contractV07, "CredentialAttestationBody"), contractSchema(t, contractV07, "CredentialSpendBody")
		for name, schema := range map[string]*jsonschema.Schema{
			"rejected/CredentialAttestationBody.TOOL_INPUT_INVALID.pull-request-key.json":        attestation,
			"rejected/CredentialAttestationBody.TOOL_INPUT_INVALID.run-attempt-as-a-number.json": attestation,
			"rejected/CredentialAttestationBody.TOOL_INPUT_INVALID.short-head.json":              attestation,
			"rejected/CredentialSpendBody.TOOL_INPUT_INVALID.another-currency.json":              spending,
			"rejected/CredentialSpendBody.TOOL_INPUT_INVALID.exponent.json":                      spending,
			"rejected/CredentialSpendBody.TOOL_INPUT_INVALID.leading-zero.json":                  spending,
			"rejected/CredentialSpendBody.TOOL_INPUT_INVALID.negative.json":                      spending,
			"rejected/CredentialSpendBody.TOOL_INPUT_INVALID.spend-as-a-number.json":             spending,
			"rejected/CredentialSpendBody.TOOL_INPUT_INVALID.thirteen-places.json":               spending,
		} {
			vector := example(t, name)
			instance, _ := jsonschema.UnmarshalJSON(bytes.NewReader(vector))
			if schema.Validate(instance) == nil {
				t.Fatalf("the contract's schema takes %s, so it holds nothing", name)
			}
			var stated struct {
				RunID      string          `json:"run_id"`
				RunAttempt json.RawMessage `json:"run_attempt"`
				HeadSHA    string          `json:"head_sha"`
				Spend      json.RawMessage `json:"spend"`
			}
			if err := json.Unmarshal(vector, &stated); err != nil {
				t.Fatal(err)
			}
			var attempt string
			if json.Unmarshal(stated.RunAttempt, &attempt) != nil {
				attempt = string(stated.RunAttempt)
			}
			run := gateway.Binding{Repository: exampleRun.Repository, Run: stated.RunID, Attempt: attempt}
			var sent any
			var err error
			if schema == attestation {
				sent, err = gateway.NewKeyStatement(exampleKeyID, run, stated.HeadSHA)
			} else {
				var figure string
				if json.Unmarshal(stated.Spend, &figure) != nil {
					figure = string(stated.Spend)
				}
				amount, parsed := decimal.ParseNumber(figure)
				if parsed != nil {
					continue
				}
				sent, err = gateway.NewSpendStatement(gateway.Count{KeyID: exampleKeyID, Spend: amount}, run)
			}
			if err != nil {
				continue
			}
			data, _ := json.Marshal(sent)
			instance, _ = jsonschema.UnmarshalJSON(bytes.NewReader(data))
			if bytes.Equal(data, compacted(t, vector)) || schema.Validate(instance) != nil {
				t.Errorf("from %s the sender writes %s, which the contract refuses", name, data)
			}
		}
	})

	t.Run("rejected key ids and audiences", func(t *testing.T) {
		for _, name := range []string{"rejected/KeyId.TOOL_INPUT_INVALID.session-id.json", "rejected/KeyId.TOOL_INPUT_INVALID.upper-case.json"} {
			var id string
			if err := json.Unmarshal(example(t, name), &id); err != nil {
				t.Fatal(err)
			}
			if _, err := gateway.NewKeyStatement(id, exampleRun, exampleHead); err == nil {
				t.Errorf("the sender states a key of the id %s", name)
			}
			if _, err := gateway.NewSpendStatement(gateway.Count{KeyID: id, Spend: decimal.MustParse(exampleSpend)}, exampleRun); err == nil {
				t.Errorf("the sender states a spend of the id %s", name)
			}
		}
		for _, name := range []string{"rejected/CredentialRunTokenClaims.RUN_TOKEN_INVALID.audience-by-prefix.json", "rejected/CredentialRunTokenClaims.RUN_TOKEN_INVALID.rows-audience.json"} {
			var claims struct {
				Audience string `json:"aud"`
			}
			if err := json.Unmarshal(example(t, name), &claims); err != nil {
				t.Fatal(err)
			}
			if claims.Audience == audiences[attestationWrite] || claims.Audience == audiences[spendWrite] {
				t.Errorf("the sender asks for the audience of %s", name)
			}
		}
		for _, name := range []string{"rejected/CredentialCase.RUN_TOKEN_INVALID.attestation-audience-on-the-spend-path.json", "rejected/CredentialCase.RUN_TOKEN_INVALID.spend-audience-on-the-attestation-path.json"} {
			var vector struct {
				Write writeKind `json:"write"`
				KeyID string    `json:"key_id"`
				Body  struct {
					RunID      string `json:"run_id"`
					RunAttempt string `json:"run_attempt"`
				} `json:"body"`
				Claims struct {
					Audience string `json:"aud"`
				} `json:"claims"`
			}
			if err := json.Unmarshal(example(t, name), &vector); err != nil {
				t.Fatal(err)
			}
			run := gateway.Binding{Run: vector.Body.RunID, Attempt: vector.Body.RunAttempt}
			asked := gateway.KeyAudience(testTenant, vector.KeyID, run)
			if vector.Write == spendWrite {
				asked = gateway.SpendAudience(testTenant, vector.KeyID, run)
			}
			if asked == vector.Claims.Audience {
				t.Errorf("the sender asks for the audience of %s", name)
			}
		}
	})

	t.Run("a row from a developer's machine", func(t *testing.T) {
		var row map[string]any
		if err := json.Unmarshal(example(t, "rejected/FocusRow.ROW_ORIGIN_REFUSED.developer-machine.json"), &row); err != nil {
			t.Fatal(err)
		}
		line, _ := json.Marshal(row)
		rows := filepath.Join(t.TempDir(), "rows.jsonl")
		if err := os.WriteFile(rows, append(line, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		stub := newPlatformStub(t, http.StatusAccepted, `{"status":"accepted"}`)
		code, _, stderr := stub.export(t, stub.githubEnv(), "--rows", rows)
		if code != exitFailure || len(stub.exports) != 0 || len(stub.identity) != 0 || !strings.Contains(stderr, "costroid/origin") {
			t.Errorf("exit %d, %d batches sent: %s", code, len(stub.exports), stderr)
		}
	})
}
