// Package oidctest is a stub OIDC issuer for tests: an RSA key, a JWKS endpoint
// and a signer. It imports nothing from package oidc, so oidc's own tests can
// use it.
package oidctest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// KeyID is the stub's one key id.
const KeyID = "key-1"

// Issuer is a running stub.
type Issuer struct {
	t      testing.TB
	Key    *rsa.PrivateKey
	Server *httptest.Server
	hits   atomic.Int64
}

// New starts a stub that closes with the test.
func New(t testing.TB) *Issuer {
	t.Helper()
	i := &Issuer{t: t, Key: NewKey(t)}
	i.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		i.hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA",
			"kid": KeyID,
			"n":   base64.RawURLEncoding.EncodeToString(i.Key.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(i.Key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(i.Server.Close)
	return i
}

// NewKey generates a 2048-bit key.
func NewKey(t testing.TB) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// URL is both the issuer and the JWKS address.
func (i *Issuer) URL() string {
	return i.Server.URL
}

// Hits counts JWKS fetches.
func (i *Issuer) Hits() int64 {
	return i.hits.Load()
}

// Token signs claims with the stub's key under its key id.
func (i *Issuer) Token(claims map[string]any) string {
	i.t.Helper()
	return Sign(i.t, i.Key, map[string]any{"alg": "RS256", "kid": KeyID}, claims)
}

// Sign builds a token with any header and any key, for the refusal tests.
func Sign(t testing.TB, key *rsa.PrivateKey, header, claims map[string]any) string {
	t.Helper()
	encode := func(v any) string {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(data)
	}
	input := encode(header) + "." + encode(claims)
	digest := sha256.Sum256([]byte(input))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(signature)
}
