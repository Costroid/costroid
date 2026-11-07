package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/gateway"
)

const (
	issuedToken     = "an-oidc-token-for-these-tests"
	requestToken    = "a-request-token-for-these-tests"
	ingestAudience  = "https://costroid.test"
	attestedKeyID   = "1d4e7a90c3b25f8866a0e4d17c92b35fa8e6d0711d4e7a90c3b25f8866a0e4d1"
	attestedRunID   = "16873409921"
	attestedAttempt = "2"
)

// attestWorld is GitHub's token endpoint and a ledger, both over TLS.
type attestWorld struct {
	audience string
	bearer   string
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
		if r.Method != http.MethodPost || r.URL.Path != "/ledger/v1/credentials" {
			rw.WriteHeader(http.StatusNotFound)
			return
		}
		w.bearer = r.Header.Get("Authorization")
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &w.body)
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
	if w.body != gateway.AttestationFor(attestedKeyID, run) {
		t.Fatalf("the ledger read %+v", w.body)
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
