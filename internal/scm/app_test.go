package scm_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/scm"
)

// localSigner stands in for the KMS.
type localSigner struct {
	key *rsa.PrivateKey
}

func (s localSigner) Sign(_ context.Context, digest [sha256.Size]byte) ([]byte, error) {
	return rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, digest[:])
}

func generateSigner(t *testing.T) localSigner {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return localSigner{key}
}

type failingSigner struct{}

func (failingSigner) Sign(context.Context, [sha256.Size]byte) ([]byte, error) {
	return nil, errors.New("kms refused")
}

func TestAppJWTIsRS256OverTheInputAndLivesTenMinutes(t *testing.T) {
	signer := generateSigner(t)
	now := time.Unix(1_800_000_000, 0)
	token, err := scm.AppJWT(context.Background(), signer, "12345", now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token = %q", token)
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(header) != `{"alg":"RS256","typ":"JWT"}` {
		t.Errorf("header = %s", header)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&signer.key.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Error(err)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		IssuedAt  int64  `json:"iat"`
		ExpiresAt int64  `json:"exp"`
		Issuer    string `json:"iss"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.ExpiresAt-claims.IssuedAt != 600 || claims.Issuer != "12345" {
		t.Errorf("claims = %+v", claims)
	}
	if claims.IssuedAt > now.Unix() || claims.ExpiresAt <= now.Unix() {
		t.Errorf("the token is not live now: %+v", claims)
	}
}

func TestAppJWTCarriesTheSignersRefusal(t *testing.T) {
	if _, err := scm.AppJWT(context.Background(), failingSigner{}, "1", time.Now()); err == nil || !strings.Contains(err.Error(), "kms refused") {
		t.Errorf("err = %v", err)
	}
}

func TestInstallationTokensAreMintedWithMinimumPermissionsAndHeldUntilNearExpiry(t *testing.T) {
	clock := newClock()
	var mints atomic.Int64
	var granted map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Permissions map[string]string `json:"permissions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		granted = request.Permissions
		if r.URL.Path != "/app/installations/58/access_tokens" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
			t.Errorf("%s with %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		mints.Add(1)
		answer := map[string]string{"token": "installation-token", "expires_at": clock.now().Add(time.Hour).Format(time.RFC3339)}
		if err := json.NewEncoder(w).Encode(answer); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	tokens := &scm.AppTokens{API: server.URL, AppID: "1", Installation: 58, Signer: generateSigner(t), Now: clock.now}
	for range 3 {
		token, err := tokens.Token(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if token != "installation-token" {
			t.Errorf("token = %q", token)
		}
	}
	if mints.Load() != 1 {
		t.Errorf("%d mints", mints.Load())
	}
	want := map[string]string{"checks": "write", "pull_requests": "write", "contents": "read", "metadata": "read"}
	if len(granted) != len(want) {
		t.Errorf("permissions = %v", granted)
	}
	for scope, level := range want {
		if granted[scope] != level {
			t.Errorf("%s = %q", scope, granted[scope])
		}
	}
	clock.advance(56 * time.Minute)
	if _, err := tokens.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if mints.Load() != 2 {
		t.Errorf("%d mints after the margin", mints.Load())
	}
}

func TestInstallationTokensRefuseAPlainHTTPHost(t *testing.T) {
	tokens := &scm.AppTokens{API: "http://github.example", AppID: "1", Installation: 58, Signer: generateSigner(t), Now: time.Now}
	if _, err := tokens.Token(context.Background()); err == nil {
		t.Error("a signed token was sent over plain HTTP")
	}
}
