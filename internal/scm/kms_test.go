package scm_test

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/scm"
	"github.com/Costroid/costroid/internal/scm/kmstest"
)

func TestAppJWTVerifiesWithPublicKey(t *testing.T) {
	stub := kmstest.New(t)
	kms, err := scm.KMSFromEnvironment(stub.Getenv, kmstest.SigningKey, stub.Server.Client(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	token, err := scm.AppJWT(context.Background(), kms.Signer(kmstest.SigningKey), "12345", now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token = %q", token)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&stub.Key(kmstest.SigningKey).PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatalf("the KMS key's public half does not verify the token: %v", err)
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
	if lifetime := time.Duration(claims.ExpiresAt-claims.IssuedAt) * time.Second; lifetime != 10*time.Minute || claims.Issuer != "12345" {
		t.Errorf("the token lives %s for %q", lifetime, claims.Issuer)
	}
	if claims.IssuedAt > now.Unix() || claims.ExpiresAt <= now.Unix() {
		t.Errorf("the token is not live now: %+v", claims)
	}
	calls := stub.Calls()
	if len(calls) != 1 || calls[0].Action != "Sign" {
		t.Fatalf("calls = %+v", calls)
	}
	if strings.Contains(string(calls[0].Body), token) || strings.Contains(string(calls[0].Body), parts[1]) {
		t.Error("KMS was sent the token rather than its digest")
	}
}

func TestKMSSignerCarriesKMSRefusal(t *testing.T) {
	stub := kmstest.New(t)
	stub.Fail["Sign"] = "DisabledException"
	kms, err := scm.KMSFromEnvironment(stub.Getenv, kmstest.SigningKey, stub.Server.Client(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = scm.AppJWT(context.Background(), kms.Signer(kmstest.SigningKey), "1", time.Now())
	if err == nil || !strings.Contains(err.Error(), "DisabledException") {
		t.Errorf("err = %v", err)
	}
}
