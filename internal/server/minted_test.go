package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/gateway"
	"github.com/Costroid/costroid/internal/store"
)

// providerKeyID is a provider's own key id, which every run on that key
// states alike.
const providerKeyID = "apikey_01SharedTeamKey"

// mintToken is the minting job's token: the fixtures' run, in the environment
// named, or in none when it is empty.
func (w *world) mintToken(environment string, change func(map[string]any)) string {
	claims := w.claims()
	if environment != "" {
		claims["environment"] = environment
	}
	if change != nil {
		change(claims)
	}
	return w.issuer.Token(claims)
}

// attest sends an attestation the way costroid gateway mint does.
func (w *world) attest(token string, attestation gateway.Attestation) error {
	return gateway.Attest(context.Background(), w.http.Client(), w.http.URL+"/v1/credentials", token, attestation)
}

// mintFor draws a key and attests it for the run and attempt named, from the
// gateway's environment, and returns its id.
func (w *world) mintFor(t *testing.T, run, attempt string) string {
	t.Helper()
	id := gateway.KeyID(gateway.NewKey())
	token := w.mintToken(gatewayEnvironment, func(claims map[string]any) {
		claims["run_id"], claims["run_attempt"] = run, attempt
	})
	binding := gateway.Binding{Repository: acmeRepo, Run: run, Attempt: attempt}
	if err := w.attest(token, gateway.AttestationFor(id, binding)); err != nil {
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
	return gateway.AttestationFor(keyID, gateway.Binding{Repository: acmeRepo, Run: acmeRun, Attempt: "1"})
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
	id := w.mintFor(t, acmeRun, "1")
	record := gradeOf(t, w, ledger, w.token(), stating(id))
	if got := record.Row.Tags[focus.TagAttributionGrade]; got != gradeExact {
		t.Fatalf("a key attested for this run reads %q", got)
	}
	if record.CredentialBasis != store.CredentialMinted {
		t.Fatalf("credential basis %q", record.CredentialBasis)
	}
	if err := w.attest(w.mintToken(gatewayEnvironment, nil), fixturesRun(id)); err != nil {
		t.Fatalf("the same attestation again was refused: %v", err)
	}
	stored := ledger.minted[tenantID(t, tenantA)][id]
	if stored.Environment != gatewayEnvironment || stored.RunAttempt != "1" || stored.Repository != acmeRepo {
		t.Fatalf("stored %+v", stored)
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
		"a key whose attestation came from outside the environment": {func(_ *testing.T, w *world) (string, string) {
			id := gateway.KeyID(gateway.NewKey())
			_ = w.attest(w.mintToken("", nil), fixturesRun(id))
			return w.token(), id
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

func TestAttestationNeedsTheEnvironment(t *testing.T) {
	cases := map[string]func(w *world) string{
		"a job in no environment":      func(w *world) string { return w.mintToken("", nil) },
		"a job in another environment": func(w *world) string { return w.mintToken("production", nil) },
		"an ingest key":                func(*world) string { return acmeKey },
		"a service that trusts no environment": func(w *world) string {
			w.withEnvironment("")
			return w.mintToken(gatewayEnvironment, nil)
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
	cases := map[string]func(a *gateway.Attestation){
		"another run":        func(a *gateway.Attestation) { a.RunID = "16873409999" },
		"another attempt":    func(a *gateway.Attestation) { a.RunAttempt = "2" },
		"another repository": func(a *gateway.Attestation) { a.Repository = "github.com/acme/gadgets" },
		"no attempt":         func(a *gateway.Attestation) { a.RunAttempt = "" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			w, ledger := memoryWorld(t)
			attestation := fixturesRun(gateway.KeyID(gateway.NewKey()))
			change(&attestation)
			refusedWith(t, w.attest(w.mintToken(gatewayEnvironment, nil), attestation), "403", codeJoinKey)
			if _, stored := ledger.minted[tenantID(t, tenantA)][attestation.KeyID]; stored {
				t.Fatal("a refused attestation was stored")
			}
		})
	}
	t.Run("a key already attested for another run", func(t *testing.T) {
		w, ledger := memoryWorld(t)
		id := w.mintFor(t, "16873409999", "1")
		refusedWith(t, w.attest(w.mintToken(gatewayEnvironment, nil), fixturesRun(id)), "409", codeJoinKey)
		if stored := ledger.minted[tenantID(t, tenantA)][id]; stored.RunID != "16873409999" {
			t.Fatalf("the first attestation was replaced: %+v", stored)
		}
	})
}

func TestAnAttestationIsOneObjectWithAKeyID(t *testing.T) {
	w, _ := memoryWorld(t)
	token := w.mintToken(gatewayEnvironment, nil)
	run := `"repository":"github.com/acme/widgets","run_id":"` + acmeRun + `","run_attempt":"1"`
	for name, body := range map[string]string{
		"not JSON":         "key",
		"an unknown field": `{"key_id":"` + gateway.KeyID("k") + `",` + run + `,"budget":"5"}`,
		"a short key id":   `{"key_id":"abc",` + run + `}`,
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
	sent, err := json.Marshal(gateway.Attestation{KeyID: "k", Repository: "r", RunID: "1", RunAttempt: "2"})
	if err != nil {
		t.Fatal(err)
	}
	var read attestation
	if err := json.Unmarshal(sent, &read); err != nil {
		t.Fatal(err)
	}
	if read != (attestation{KeyID: "k", Repository: "r", RunID: "1", RunAttempt: "2"}) {
		t.Fatalf("read %+v from %s", read, sent)
	}
}
