package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Costroid/costroid/internal/docstest"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/gateway"
	"github.com/Costroid/costroid/internal/store"
)

// providerKeyID is a provider's own key id, which every run on that key
// states alike.
const providerKeyID = "apikey_01SharedTeamKey"

const (
	// fixturesMintRun is the run that workflow_run started on the default
	// branch after the fixtures' run, which minted the fixtures' key and
	// attested it. mintRun is another such run, which a test attests a key of
	// its own from, and closeRun the one that attested a spend.
	fixturesMintRun = "16873410300"
	mintRun         = "16873410388"
	closeRun        = "16873410555"
	// otherHead is a head commit the fixtures' pull request never had.
	otherHead = "77aa3e1f0c9b5d2864e1f3a0b7c6d5e4f3a2b1c0"
)

// attestingRuns numbers the runs mintFor attests from, so each attestation
// comes from a run of its own.
var attestingRuns atomic.Int64

func init() { attestingRuns.Store(16873420000) }

// protectedToken is the token of a job that workflow_run started on the
// protected default branch, in the gateway's environment, with change applied
// to its claims. The claims are the ones recorded GitHub Actions runs
// 45109326900 and 45109345722 state for such a run.
func (w *world) protectedToken(run string, change func(map[string]any)) string {
	claims := w.claims()
	claims["run_id"], claims["run_attempt"] = run, "1"
	claims["event_name"], claims["ref"], claims["ref_protected"] = "workflow_run", "refs/heads/main", "true"
	claims["environment"] = gatewayEnvironment
	if change != nil {
		change(claims)
	}
	return w.issuer.Token(claims)
}

// mintToken is the token of the run that minted the fixtures' key.
func (w *world) mintToken() string {
	return w.protectedToken(mintRun, nil)
}

// pullRequestToken is the token of a job of a pull request's own run in the
// gateway's environment: the fixtures' run, or another run that
// pull_request started, with change applied to its claims.
func (w *world) pullRequestToken(change func(map[string]any)) string {
	claims := w.claims()
	claims["environment"] = gatewayEnvironment
	if change != nil {
		change(claims)
	}
	return w.issuer.Token(claims)
}

// attest sends an attestation the way costroid gateway mint does.
func (w *world) attest(token string, attestation gateway.Attestation) error {
	return gateway.Attest(context.Background(), w.http.Client(), w.http.URL+"/v1/credentials", token, attestation)
}

// mintFor draws a key and attests it for the run and attempt named, with the
// fixtures' head and the pull requests named, from a protected run of its
// own, and returns its id.
func (w *world) mintFor(t *testing.T, run, attempt string, pulls ...string) string {
	t.Helper()
	id := gateway.KeyID(gateway.NewKey())
	token := w.protectedToken(strconv.FormatInt(attestingRuns.Add(1), 10), nil)
	attestation := gateway.AttestationFor(id, gateway.Binding{Repository: acmeRepo, Run: run, Attempt: attempt}, acmeHead)
	attestation.PullRequests = pulls
	if err := w.attest(token, attestation); err != nil {
		t.Fatalf("attest: %v", err)
	}
	return id
}

// withEnvironment replaces the service with one that trusts the environment
// named.
func (w *world) withEnvironment(environment string) {
	config := w.server.Config
	config.GatewayEnvironment = environment
	w.server = New(config)
	w.http = httptest.NewServer(w.server.Handler())
	w.t.Cleanup(w.http.Close)
}

func fixturesRun(keyID string) gateway.Attestation {
	return gateway.AttestationFor(keyID, gateway.Binding{Repository: acmeRepo, Run: acmeRun, Attempt: "1"}, acmeHead)
}

// stating is a row for the fixtures' pull request that states a credential
// and claims exact on its own.
func stating(credential string) map[string]any {
	r := row("req-1", focus.Output, 100)
	delete(tags(r), focus.TagSessionID)
	r["SubAccountId"] = credential
	tags(r)[focus.TagAttributionGrade] = gradeExact
	tags(r)[focus.TagRunIdentity] = identityToken
	return r
}

func gradeOf(t *testing.T, w *world, ledger *memoryLedger, bearer string, r map[string]any) store.Record {
	t.Helper()
	w.rows(bearer, lines(t, r)).want(t, http.StatusOK)
	return ledger.only(t, tenantA)
}

// refusedWith holds a refused attestation to its status and typed code.
func refusedWith(t *testing.T, err error, status, code string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "answered "+status+": "+code) {
		t.Fatalf("got %v, want %s %s", err, status, code)
	}
}

func TestMintedKeyExact(t *testing.T) {
	w, ledger := memoryWorld(t)
	id := gateway.KeyID(gateway.NewKey())
	if err := w.attest(w.mintToken(), fixturesRun(id)); err != nil {
		t.Fatal(err)
	}
	record := gradeOf(t, w, ledger, w.token(), stating(id))
	if got := record.Row.Tags[focus.TagAttributionGrade]; got != gradeExact {
		t.Fatalf("a key attested for this run reads %q", got)
	}
	if record.CredentialBasis != store.CredentialMinted {
		t.Fatalf("credential basis %q", record.CredentialBasis)
	}
	if err := w.attest(w.mintToken(), fixturesRun(id)); err != nil {
		t.Fatalf("the same attestation again was refused: %v", err)
	}
	stored := ledger.minted[tenantID(t, tenantA)][id]
	want := store.MintedCredential{
		KeySHA256: id, Repository: acmeRepo, RunID: acmeRun, RunAttempt: "1", HeadSHA: acmeHead,
		AttestingRunID: mintRun, AttestingRunAttempt: "1", AttestingRef: "refs/heads/main",
	}
	if stored != want {
		t.Fatalf("stored %+v, want %+v", stored, want)
	}
}

func TestSharedKeyNeverExact(t *testing.T) {
	type setup func(t *testing.T, w *world) (bearer, credential string)
	cases := map[string]struct {
		arrange setup
		want    string
	}{
		"a team's key every run shares, never attested": {func(_ *testing.T, w *world) (string, string) {
			return w.token(), gateway.KeyID("sk-shared-team-key")
		}, gradeWindowed},
		"a provider key id": {func(_ *testing.T, w *world) (string, string) {
			return w.token(), providerKeyID
		}, gradeWindowed},
		"a key attested for another run": {func(t *testing.T, w *world) (string, string) {
			return w.token(), w.mintFor(t, "16873409999", "1")
		}, gradeWindowed},
		"a key attested for another attempt of this run": {func(t *testing.T, w *world) (string, string) {
			return w.token(), w.mintFor(t, acmeRun, "2")
		}, gradeWindowed},
		"a key attested for this run id in another repository": {func(t *testing.T, w *world) (string, string) {
			return w.tokenFor("acme", "gadgets"), w.mintFor(t, acmeRun, "1")
		}, gradeWindowed},
		"an attested key sent under an ingest key": {func(t *testing.T, w *world) (string, string) {
			return acmeKey, w.mintFor(t, acmeRun, "1")
		}, gradeNone},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w, ledger := memoryWorld(t)
			bearer, credential := tc.arrange(t, w)
			record := gradeOf(t, w, ledger, bearer, stating(credential))
			if got := record.Row.Tags[focus.TagAttributionGrade]; got != tc.want {
				t.Fatalf("a row claiming exact on a credential no attestation names for its run reads %q, want %q", got, tc.want)
			}
			if record.CredentialBasis != store.CredentialStated {
				t.Fatalf("credential basis %q", record.CredentialBasis)
			}
			if id := record.Row.SubAccountID; id == nil || *id != credential {
				t.Fatalf("SubAccountId %v, want the stated credential kept as a claim", id)
			}
		})
	}
}

// otherPullRequestRun is a second run a pull request starts beside the
// fixtures' run, from a workflow file of its own.
const otherPullRequestRun = "16873409950"

// TestAKeyAttestedFromAPullRequestsOwnRunReadsWindowed sends the attestation
// from each run a pull request can edit, or that no protected branch holds,
// with the job in the gateway's environment. Each is refused, nothing is
// stored, and the run's row on the key reads windowed.
func TestAKeyAttestedFromAPullRequestsOwnRunReadsWindowed(t *testing.T) {
	cases := map[string]struct {
		bearer func(w *world) string
		code   string
	}{
		"the pull request's own run, attesting itself": {func(w *world) string {
			return w.pullRequestToken(nil)
		}, codeRunUnprotected},
		"another run the pull request started, attesting the agent's run": {func(w *world) string {
			return w.pullRequestToken(func(claims map[string]any) { claims["run_id"] = otherPullRequestRun })
		}, codeRunUnprotected},
		"a push to a branch, run by its own workflow file": {func(w *world) string {
			return w.pullRequestToken(func(claims map[string]any) {
				claims["run_id"], claims["event_name"], claims["ref"], claims["ref_protected"] = otherPullRequestRun, "push", "refs/heads/feature", "false"
			})
		}, codeRunUnprotected},
		"a workflow_run run of a default branch no rule protects": {func(w *world) string {
			return w.protectedToken(mintRun, func(claims map[string]any) { claims["ref_protected"] = "false" })
		}, codeRunUnprotected},
		"a workflow_run run that states no ref_protected": {func(w *world) string {
			return w.protectedToken(mintRun, func(claims map[string]any) { delete(claims, "ref_protected") })
		}, codeRunUnprotected},
		"a workflow_run run of a tag": {func(w *world) string {
			return w.protectedToken(mintRun, func(claims map[string]any) { claims["ref"] = "refs/tags/v1" })
		}, codeRunUnprotected},
		"a protected workflow_run run outside the gateway's environment": {func(w *world) string {
			return w.protectedToken(mintRun, func(claims map[string]any) { delete(claims, "environment") })
		}, codeAttestation},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w, ledger := memoryWorld(t)
			id := gateway.KeyID(gateway.NewKey())
			if err := w.attest(tc.bearer(w), fixturesRun(id)); err == nil || !strings.Contains(err.Error(), "answered 403: "+tc.code) {
				t.Errorf("got %v, want 403 %s", err, tc.code)
			}
			if _, stored := ledger.minted[tenantID(t, tenantA)][id]; stored {
				t.Error("a refused attestation was stored")
			}
			record := gradeOf(t, w, ledger, w.token(), stating(id))
			if got := record.Row.Tags[focus.TagAttributionGrade]; got != gradeWindowed || record.CredentialBasis != store.CredentialStated {
				t.Fatalf("a key attested from a run a pull request can edit reads %q on basis %q", got, record.CredentialBasis)
			}
		})
	}
}

// TestRefProtectedIsComparedAsTheStringTrue signs a protected run's token with
// every other form ref_protected could take. GitHub states the claim as the
// JSON string "true" (recorded GitHub Actions runs 45109326900 and
// 45109345722), and only that form attests a key. Every other form still
// verifies as a token, and is refused as a run no protected branch holds.
func TestRefProtectedIsComparedAsTheStringTrue(t *testing.T) {
	for name, value := range map[string]any{
		"the string true":  "true",
		"a boolean":        true,
		"capitalised":      "True",
		"upper case":       "TRUE",
		"the string 1":     "1",
		"the number 1":     1,
		"padded":           " true",
		"null":             nil,
		"a list":           []string{"true"},
		"an object":        map[string]string{"value": "true"},
		"the string false": "false",
	} {
		t.Run(name, func(t *testing.T) {
			w, ledger := memoryWorld(t)
			id := gateway.KeyID(gateway.NewKey())
			token := w.protectedToken(mintRun, func(claims map[string]any) { claims["ref_protected"] = value })
			err := w.attest(token, fixturesRun(id))
			if value == "true" {
				if err != nil {
					t.Fatalf("the string true was refused: %v", err)
				}
				return
			}
			refusedWith(t, err, "403", codeRunUnprotected)
			if _, stored := ledger.minted[tenantID(t, tenantA)][id]; stored {
				t.Fatal("a refused attestation was stored")
			}
		})
	}
}

func TestAttestationNeedsTheEnvironment(t *testing.T) {
	cases := map[string]func(w *world) string{
		"a job in no environment": func(w *world) string {
			return w.protectedToken(mintRun, func(claims map[string]any) { delete(claims, "environment") })
		},
		"a job in another environment": func(w *world) string {
			return w.protectedToken(mintRun, func(claims map[string]any) { claims["environment"] = "production" })
		},
		"an ingest key": func(*world) string { return acmeKey },
		"a service that trusts no environment": func(w *world) string {
			w.withEnvironment("")
			return w.mintToken()
		},
	}
	for name, bearer := range cases {
		t.Run(name, func(t *testing.T) {
			w, ledger := memoryWorld(t)
			id := gateway.KeyID(gateway.NewKey())
			refusedWith(t, w.attest(bearer(w), fixturesRun(id)), "403", codeAttestation)
			if _, stored := ledger.minted[tenantID(t, tenantA)][id]; stored {
				t.Fatal("a refused attestation was stored")
			}
		})
	}
}

func TestAttestationForAnotherRunRefused(t *testing.T) {
	cases := map[string]struct {
		change func(a *gateway.Attestation)
		status string
		code   string
	}{
		"another repository":          {func(a *gateway.Attestation) { a.Repository = "github.com/acme/gadgets" }, "403", codeJoinKey},
		"the attesting run itself":    {func(a *gateway.Attestation) { a.RunID = mintRun }, "403", codeJoinKey},
		"no attempt":                  {func(a *gateway.Attestation) { a.RunAttempt = "" }, "400", codeMalformed},
		"no head":                     {func(a *gateway.Attestation) { a.HeadSHA = "" }, "400", codeMalformed},
		"a head that is not a commit": {func(a *gateway.Attestation) { a.HeadSHA = "main" }, "400", codeMalformed},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w, ledger := memoryWorld(t)
			attestation := fixturesRun(gateway.KeyID(gateway.NewKey()))
			tc.change(&attestation)
			refusedWith(t, w.attest(w.mintToken(), attestation), tc.status, tc.code)
			if _, stored := ledger.minted[tenantID(t, tenantA)][attestation.KeyID]; stored {
				t.Fatal("a refused attestation was stored")
			}
		})
	}
}

// TestASecondAttestationFromOneAttestingRunIsRefused holds an attesting run
// and attempt to one attestation, and a key to its first. The same
// attestation again is answered as the first was. Where both refusals apply,
// the attesting run's comes first. It runs on the memory ledger and on
// Postgres, whose unique indexes hold the rule.
func TestASecondAttestationFromOneAttestingRunIsRefused(t *testing.T) {
	t.Run("in memory", func(t *testing.T) {
		w, _ := memoryWorld(t)
		secondAttestationFromOneAttestingRunIsRefused(t, w)
	})
	t.Run("on Postgres", func(t *testing.T) {
		secondAttestationFromOneAttestingRunIsRefused(t, postgresWorld(t).world)
	})
}

func secondAttestationFromOneAttestingRunIsRefused(t *testing.T, w *world) {
	first, second := gateway.KeyID(gateway.NewKey()), gateway.KeyID(gateway.NewKey())
	if err := w.attest(w.mintToken(), fixturesRun(first)); err != nil {
		t.Fatal(err)
	}
	if err := w.attest(w.mintToken(), fixturesRun(first)); err != nil {
		t.Fatalf("the same attestation again: %v", err)
	}
	change := func(key string, edit func(*gateway.Attestation)) gateway.Attestation {
		attestation := fixturesRun(key)
		edit(&attestation)
		return attestation
	}
	for name, attestation := range map[string]gateway.Attestation{
		"another key":                      fixturesRun(second),
		"another key for another run":      change(second, func(a *gateway.Attestation) { a.RunID = "16873409999" }),
		"the same key with another head":   change(first, func(a *gateway.Attestation) { a.HeadSHA = otherHead }),
		"the same key for another attempt": change(first, func(a *gateway.Attestation) { a.RunAttempt = "2" }),
	} {
		t.Run(name, func(t *testing.T) {
			refusedWith(t, w.attest(w.mintToken(), attestation), "409", codeAttestingRunUsed)
		})
	}
	secondAttempt := w.protectedToken(mintRun, func(claims map[string]any) { claims["run_attempt"] = "2" })
	refusedWith(t, w.attest(secondAttempt, change(first, func(a *gateway.Attestation) { a.HeadSHA = otherHead })), "409", codeAttestedElsewhere)
	refusedWith(t, w.attest(w.protectedToken(closeRun, nil), fixturesRun(first)), "409", codeAttestedElsewhere)
	if err := w.attest(w.mintToken(), fixturesRun(first)); err != nil {
		t.Fatalf("the first attestation no longer stands: %v", err)
	}
	if err := w.attest(secondAttempt, fixturesRun(second)); err != nil {
		t.Fatalf("another attempt of the attesting run is a speaker of its own: %v", err)
	}
}

// TestARowWhoseHeadIsNotTheAttestedHeadReadsWindowed attests a key with the
// head GitHub's event named for the run. A row that states that head reads
// exact; a row that states another head, or none, reads windowed on the same
// minted key.
func TestARowWhoseHeadIsNotTheAttestedHeadReadsWindowed(t *testing.T) {
	for name, tc := range map[string]struct {
		head  string
		grade string
	}{
		"the attested head": {acmeHead, gradeExact},
		"another head":      {otherHead, gradeWindowed},
		"no head":           {"", gradeWindowed},
	} {
		t.Run(name, func(t *testing.T) {
			w, ledger := memoryWorld(t)
			id := w.mintFor(t, acmeRun, "1")
			r := stating(id)
			if tc.head == "" {
				delete(tags(r), store.TagCommitSHA)
			} else {
				tags(r)[store.TagCommitSHA] = tc.head
			}
			record := gradeOf(t, w, ledger, w.token(), r)
			if got := record.Row.Tags[focus.TagAttributionGrade]; got != tc.grade || record.CredentialBasis != store.CredentialMinted {
				t.Fatalf("a row stating head %q reads %q on basis %q, want %q", tc.head, got, record.CredentialBasis, tc.grade)
			}
		})
	}
}

func TestAnAttestationIsOneObjectWithAKeyID(t *testing.T) {
	w, _ := memoryWorld(t)
	token := w.mintToken()
	run := `"repository":"github.com/acme/widgets","run_id":"` + acmeRun + `","run_attempt":"1","head_sha":"` + acmeHead + `"`
	key := `{"key_id":"` + gateway.KeyID("k") + `",` + run
	for name, body := range map[string]string{
		"not JSON":                            "key",
		"an unknown field":                    key + `,"budget":"5"}`,
		"a short key id":                      `{"key_id":"abc",` + run + `}`,
		"pull requests that are not a list":   key + `,"pull_requests":"1423"}`,
		"a pull request as a JSON number":     key + `,"pull_requests":[1423]}`,
		"a pull request that is not a number": key + `,"pull_requests":["#1423"]}`,
		"pull request 0":                      key + `,"pull_requests":["0"]}`,
		"an empty pull request":               key + `,"pull_requests":["1423",""]}`,
	} {
		t.Run(name, func(t *testing.T) {
			got := w.send(http.MethodPost, "/v1/credentials", token, "application/json", []byte(body)).want(t, http.StatusBadRequest)
			if got.code(t) != codeMalformed {
				t.Fatalf("code %s", got.code(t))
			}
		})
	}
}

func TestTheAttestationMintSendsIsTheOneTheServiceReads(t *testing.T) {
	sent, err := json.Marshal(gateway.Attestation{KeyID: "k", Repository: "r", RunID: "1", RunAttempt: "2", HeadSHA: "h", PullRequests: []string{"7", "9"}})
	if err != nil {
		t.Fatal(err)
	}
	var read attestation
	decoder := json.NewDecoder(bytes.NewReader(sent))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&read); err != nil {
		t.Fatal(err)
	}
	if want := (attestation{KeyID: "k", Repository: "r", RunID: "1", RunAttempt: "2", HeadSHA: "h", PullRequests: []string{"7", "9"}}); !reflect.DeepEqual(read, want) {
		t.Fatalf("read %+v from %s", read, sent)
	}
}

// statedClaim finds each value a page states a claim at, in "`claim` as
// `value`".
func statedClaim(t *testing.T, page, claim string) []string {
	t.Helper()
	text := docstest.Page(t, "../..", page)
	found := regexp.MustCompile("`"+regexp.QuoteMeta(claim)+"` as `([^`]*)`").FindAllStringSubmatch(text, -1)
	if len(found) == 0 {
		t.Fatalf("%s states no value of %s", page, claim)
	}
	values := make([]string, len(found))
	for i, match := range found {
		values[i] = match[1]
	}
	return values
}

// TestTheExactRuleOnTheGatewayPageIsTheServersRule holds the claim value the
// gateway page and the service page state for the exact grade to the one the
// service compares, and the event they name to the one it reads.
func TestTheExactRuleOnTheGatewayPageIsTheServersRule(t *testing.T) {
	for _, page := range []string{"docs/gateway.md", "docs/serve.md"} {
		for _, value := range statedClaim(t, page, "ref_protected") {
			if value != protectedRefClaim {
				t.Errorf("%s states ref_protected as %s, and the service compares %s", page, value, protectedRefClaim)
			}
		}
		if !strings.Contains(docstest.Page(t, "../..", page), "a run that `"+eventWorkflowRun+"` started") {
			t.Errorf("%s does not name the event %s the service reads", page, eventWorkflowRun)
		}
	}
}

// TestOnlyAWorkflowRunTokenAttests signs, for each event other than
// workflow_run that can run on a protected default branch, a token that
// passes every other check: ref refs/heads/main, ref_protected the string
// "true", a run attempt, and the job in the gateway's environment.
// pull_request_target among them runs on the default branch with the
// default branch's ref, though a pull request starts it. Each is refused on
// the attestation path and on the spend path, stores nothing, and a row of
// the run it names reads windowed.
func TestOnlyAWorkflowRunTokenAttests(t *testing.T) {
	for _, event := range []string{"pull_request_target", "push", "workflow_dispatch", "schedule", "merge_group"} {
		t.Run(event, func(t *testing.T) {
			w := spendWorld(t, true)
			ledger := w.server.Config.Ledger.(*spendLedger)
			token := w.protectedToken(mintRun, func(claims map[string]any) { claims["event_name"] = event })
			id := gateway.KeyID(gateway.NewKey())
			refusedWith(t, w.attest(token, fixturesRun(id)), "403", codeRunUnprotected)
			if got := w.attestSpend(token, gateway.KeyID(fixturesKey), fixturesSpend("0.001")); got.status != http.StatusForbidden || got.code(t) != codeRunUnprotected {
				t.Fatalf("the spend path: %d %s", got.status, got.body)
			}
			if _, stored := ledger.minted[tenantID(t, tenantA)][id]; stored || len(ledger.spend[tenantID(t, tenantA)]) != 0 {
				t.Fatal("a refused attestation was stored")
			}
			record := gradeOf(t, w, ledger.memoryLedger, w.token(), stating(id))
			if got := record.Row.Tags[focus.TagAttributionGrade]; got != gradeWindowed {
				t.Fatalf("a row on a key a %s run attested reads %q", event, got)
			}
		})
	}
}

// TestAnAttestingTokenNamesItsRunAttempt signs a protected workflow_run
// token that states no run attempt. The rule of one attestation per
// attesting run and attempt needs the attempt, so the token is refused on
// both paths and stores nothing.
func TestAnAttestingTokenNamesItsRunAttempt(t *testing.T) {
	w := spendWorld(t, true)
	ledger := w.server.Config.Ledger.(*spendLedger)
	token := w.protectedToken(mintRun, func(claims map[string]any) { delete(claims, "run_attempt") })
	id := gateway.KeyID(gateway.NewKey())
	refusedWith(t, w.attest(token, fixturesRun(id)), "403", codeRunUnprotected)
	if got := w.attestSpend(token, gateway.KeyID(fixturesKey), fixturesSpend("0.001")); got.status != http.StatusForbidden || got.code(t) != codeRunUnprotected {
		t.Fatalf("the spend path: %d %s", got.status, got.body)
	}
	if _, stored := ledger.minted[tenantID(t, tenantA)][id]; stored || len(ledger.spend[tenantID(t, tenantA)]) != 0 {
		t.Fatal("a refused attestation was stored")
	}
}
