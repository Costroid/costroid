package server

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/egress"
	"github.com/Costroid/costroid/internal/scm/kmstest"
	"github.com/Costroid/costroid/internal/scm/scmtest"
	"github.com/Costroid/costroid/internal/store/storetest"
)

const testAppID = "424242"

// appFront is GitHub as the App sees it: it mints installation 58's token
// only for a JWT the KMS key signed, and answers the rest from the stub.
type appFront struct {
	server *httptest.Server
	kms    *kmstest.KMS
	minted atomic.Int64
}

func newAppFront(t testing.TB, stub *scmtest.Stub) *appFront {
	t.Helper()
	front := &appFront{kms: kmstest.New(t)}
	public := &front.kms.Key(kmstest.SigningKey).PublicKey
	front.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/app/") {
			stub.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/app/installations/58/access_tokens" || !signedByTheApp(r.Header.Get("Authorization"), public) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		front.minted.Add(1)
		answer := map[string]string{"token": stub.Token, "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)}
		if err := json.NewEncoder(w).Encode(answer); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(front.server.Close)
	return front
}

// signedByTheApp reports whether a bearer is an RS256 JWT the key signed, for
// the App's id.
func signedByTheApp(authorization string, public *rsa.PublicKey) bool {
	token, found := strings.CutPrefix(authorization, "Bearer ")
	parts := strings.Split(token, ".")
	if !found || len(parts) != 3 {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(public, crypto.SHA256, digest[:], signature) != nil {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Issuer string `json:"iss"`
	}
	return err == nil && json.Unmarshal(payload, &claims) == nil && claims.Issuer == testAppID
}

// appEnvironment is serve's environment with the App's id and KMS key set,
// and the stub's AWS variables.
func (f *appFront) appEnvironment(name string) string {
	switch name {
	case envAppID:
		return testAppID
	case envAppKey:
		return kmstest.SigningKey
	}
	return f.kms.Getenv(name)
}

// hosts are serve's own hosts for the App, through an egress gate that allows
// the loopback stubs.
func (f *appFront) hosts(t testing.TB) RepositoryHosts {
	t.Helper()
	gate := egress.New([]string{"127.0.0.1"}, failingCounters())
	hosts, err := appKeyHosts(f.appEnvironment, f.server.URL, gate, slog.New(slog.DiscardHandler))
	if err != nil || hosts == nil {
		t.Fatalf("hosts %v, %v", hosts, err)
	}
	return hosts
}

func TestServeHoldsNoAppKeyWithoutBothVariables(t *testing.T) {
	gate := egress.New(nil, failingCounters())
	logger := slog.New(slog.DiscardHandler)
	hosts, err := appKeyHosts(func(string) string { return "" }, "https://api.github.com", gate, logger)
	if hosts != nil || err != nil {
		t.Errorf("neither set: %v, %v", hosts, err)
	}
	for name, values := range map[string]map[string]string{
		"the id alone":       {envAppID: testAppID},
		"the key alone":      {envAppKey: kmstest.SigningKey},
		"an id that is text": {envAppID: "my-app", envAppKey: kmstest.SigningKey},
		"no AWS credentials": {envAppID: testAppID, envAppKey: kmstest.SigningKey, "AWS_REGION": kmstest.Region},
		"no region":          {envAppID: testAppID, envAppKey: kmstest.SigningKey, "AWS_ACCESS_KEY_ID": "a", "AWS_SECRET_ACCESS_KEY": "b"},
	} {
		hosts, err := appKeyHosts(func(name string) string { return values[name] }, "https://api.github.com", gate, logger)
		if hosts != nil || err == nil {
			t.Errorf("%s: %v, %v", name, hosts, err)
		}
	}
}

func TestServeSaysTheApproveButtonPostsAndWhichHostsTheListMisses(t *testing.T) {
	front := newAppFront(t, scmtest.New(t))
	var logged bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logged, nil))
	hosts, err := appKeyHosts(front.appEnvironment, "https://api.github.com", egress.New(nil, failingCounters()), logger)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{`"host":"api.github.com"`, `"host":"127.0.0.1"`} {
		if !strings.Contains(logged.String(), `"msg":"egress allowlist does not cover the GitHub App's calls",`+host) {
			t.Errorf("the log does not name %s:\n%s", host, logged.String())
		}
	}
	logged.Reset()
	if app := githubApp([]byte(webhookSecretText), hosts, nil, logger); app == nil || app.Hosts == nil {
		t.Fatal("the App's hosts were dropped")
	}
	line := `"msg":"webhook route served","path":"/webhooks/github","approve_button":"posts as the GitHub App, signed by its KMS key"`
	if !strings.Contains(logged.String(), line) {
		t.Errorf("%s", logged.String())
	}
	for _, secret := range []string{kmstest.SecretAccessKey, kmstest.SigningKey, webhookSecretText} {
		if strings.Contains(logged.String(), secret) {
			t.Errorf("the log holds %q", secret)
		}
	}
}

// TestServePostsAsTheAppWhenBothVariablesAreSet runs serve with the App's id
// and KMS key set, and reads at start that the approve button posts.
func TestServePostsAsTheAppWhenBothVariablesAreSet(t *testing.T) {
	db := storetest.New(t)
	front := newAppFront(t, scmtest.New(t))
	t.Setenv(envDatabase, db.AppURL)
	t.Setenv(envMigration, db.OwnerURL)
	t.Setenv(envAudience, audience)
	t.Setenv(envPseudonym, base64.StdEncoding.EncodeToString(make([]byte, minPseudonymKeyBytes)))
	t.Setenv(envWebhook, webhookSecretText)
	for _, name := range []string{envAppID, envAppKey, "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_REGION", "AWS_ENDPOINT_URL_KMS"} {
		t.Setenv(name, front.appEnvironment(name))
	}
	_, log := startServe(t)
	if !strings.Contains(log.String(), `"approve_button":"posts as the GitHub App, signed by its KMS key"`) {
		t.Errorf("serve does not say the button posts:\n%s", log.String())
	}
	for _, secret := range []string{kmstest.SecretAccessKey, kmstest.SigningKey} {
		if strings.Contains(log.String(), secret) {
			t.Errorf("serve logged %q", secret)
		}
	}
}
