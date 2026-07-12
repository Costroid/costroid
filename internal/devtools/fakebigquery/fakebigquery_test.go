// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package fakebigquery

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/ingest/gcpfocusbq"
)

const fixtureDir = "../../../testdata/gcp-focus-bq/fixture"

func runtimeKey(t *testing.T) (email string, priv *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return "fakebq-self@example.test", key
}

func mintJWT(t *testing.T, priv *rsa.PrivateKey, email, scope, audience string, lifetime time.Duration) string {
	t.Helper()
	now := time.Now().UTC().Unix()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"iss": email, "scope": scope, "aud": audience,
		"iat": now, "exp": now + int64(lifetime.Seconds()),
	})
	enc := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	signingInput := enc(header) + "." + enc(claims)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func postForm(t *testing.T, urlStr string, form url.Values) (*http.Response, string) {
	t.Helper()
	resp, err := http.PostForm(urlStr, form)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

func doJSON(t *testing.T, method, urlStr, token string, payload any) (*http.Response, string) {
	t.Helper()
	var rdr io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, urlStr, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

func authedToken(t *testing.T, srv *httptest.Server, email string, priv *rsa.PrivateKey) string {
	t.Helper()
	audience := srv.URL + "/token"
	assertion := mintJWT(t, priv, email, gcpfocusbq.BigQueryScope, audience, time.Hour)
	resp, body := postForm(t, audience, url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token status = %d body %s", resp.StatusCode, body)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(body), &tok); err != nil || tok.AccessToken == "" {
		t.Fatalf("token body = %s", body)
	}
	return tok.AccessToken
}

func TestTokenEndpointRejectionTeeth(t *testing.T) {
	email, priv := runtimeKey(t)
	h := New(fixtureDir)
	h.AllowServiceAccount(email, &priv.PublicKey)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	audience := srv.URL + "/token"

	t.Run("wrong_scope", func(t *testing.T) {
		assertion := mintJWT(t, priv, email, "https://www.googleapis.com/auth/cloud-platform", audience, time.Hour)
		resp, body := postForm(t, audience, url.Values{
			"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
			"assertion":  {assertion},
		})
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "JWT scope must equal") {
			t.Fatalf("status=%d body=%s", resp.StatusCode, body)
		}
	})

	t.Run("wrong_audience", func(t *testing.T) {
		assertion := mintJWT(t, priv, email, gcpfocusbq.BigQueryScope, "https://evil.example/token", time.Hour)
		resp, body := postForm(t, audience, url.Values{
			"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
			"assertion":  {assertion},
		})
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "JWT audience must equal the token URL") {
			t.Fatalf("status=%d body=%s", resp.StatusCode, body)
		}
	})

	t.Run("lifetime_over_one_hour", func(t *testing.T) {
		assertion := mintJWT(t, priv, email, gcpfocusbq.BigQueryScope, audience, 2*time.Hour)
		resp, body := postForm(t, audience, url.Values{
			"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
			"assertion":  {assertion},
		})
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "JWT lifetime") {
			t.Fatalf("status=%d body=%s", resp.StatusCode, body)
		}
	})

	t.Run("unknown_issuer", func(t *testing.T) {
		assertion := mintJWT(t, priv, "stranger@example.test", gcpfocusbq.BigQueryScope, audience, time.Hour)
		resp, body := postForm(t, audience, url.Values{
			"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
			"assertion":  {assertion},
		})
		if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "unknown service-account issuer") {
			t.Fatalf("status=%d body=%s", resp.StatusCode, body)
		}
	})

	t.Run("bad_signature", func(t *testing.T) {
		other, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		assertion := mintJWT(t, other, email, gcpfocusbq.BigQueryScope, audience, time.Hour)
		resp, body := postForm(t, audience, url.Values{
			"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
			"assertion":  {assertion},
		})
		if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "JWT signature invalid") {
			t.Fatalf("status=%d body=%s", resp.StatusCode, body)
		}
	})
}

func TestJobsQueryRejectionTeeth(t *testing.T) {
	email, priv := runtimeKey(t)
	h := New(fixtureDir)
	h.AllowServiceAccount(email, &priv.PublicKey)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	token := authedToken(t, srv, email, priv)
	queryURL := srv.URL + "/projects/job-project/queries"

	cols := gcpfocusbq.PinnedColumnNames()
	for i, c := range cols {
		cols[i] = "`" + c + "`"
	}
	goodCols := strings.Join(cols, ", ")
	goodQuery := "SELECT " + goodCols + " FROM `p.d.t` WHERE BillingPeriodStart >= TIMESTAMP('2026-05-01T00:00:00Z') AND BillingPeriodStart < TIMESTAMP('2026-06-01T00:00:00Z')"

	t.Run("missing_location", func(t *testing.T) {
		resp, body := doJSON(t, http.MethodPost, queryURL, token, map[string]any{
			"query": goodQuery, "useLegacySql": false,
			"formatOptions": map[string]any{"useInt64Timestamp": true},
			"maxResults":    100,
		})
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "location and formatOptions.useInt64Timestamp=true are required") {
			t.Fatalf("status=%d body=%s", resp.StatusCode, body)
		}
	})

	t.Run("missing_formatOptions", func(t *testing.T) {
		resp, body := doJSON(t, http.MethodPost, queryURL, token, map[string]any{
			"query": goodQuery, "location": "EU", "useLegacySql": false, "maxResults": 100,
		})
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "location and formatOptions.useInt64Timestamp=true are required") {
			t.Fatalf("status=%d body=%s", resp.StatusCode, body)
		}
	})

	t.Run("select_star", func(t *testing.T) {
		resp, body := doJSON(t, http.MethodPost, queryURL, token, map[string]any{
			"query": "SELECT * FROM `p.d.t`", "location": "EU", "useLegacySql": false,
			"formatOptions": map[string]any{"useInt64Timestamp": true},
		})
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "SELECT star is forbidden") {
			t.Fatalf("status=%d body=%s", resp.StatusCode, body)
		}
	})

	t.Run("legacy_sql", func(t *testing.T) {
		resp, body := doJSON(t, http.MethodPost, queryURL, token, map[string]any{
			"query": goodQuery, "location": "EU", "useLegacySql": true,
			"formatOptions": map[string]any{"useInt64Timestamp": true},
		})
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "useLegacySql must be present and false") {
			t.Fatalf("status=%d body=%s", resp.StatusCode, body)
		}
	})

	t.Run("non_pinned_select_set", func(t *testing.T) {
		resp, body := doJSON(t, http.MethodPost, queryURL, token, map[string]any{
			"query":    "SELECT `BilledCost`, `ServiceName` FROM `p.d.t` WHERE BillingPeriodStart >= TIMESTAMP('2026-05-01T00:00:00Z') AND BillingPeriodStart < TIMESTAMP('2026-06-01T00:00:00Z')",
			"location": "EU", "useLegacySql": false,
			"formatOptions": map[string]any{"useInt64Timestamp": true},
		})
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "per-period SELECT columns must set-equal the pinned schema") {
			t.Fatalf("status=%d body=%s", resp.StatusCode, body)
		}
	})
}

func TestPageSizeEnforcedRegardlessOfMaxResults(t *testing.T) {
	email, priv := runtimeKey(t)
	h := New(fixtureDir)
	h.PageSize = 1
	h.DelayedMonth = "" // serve June immediately so we exercise page(), not the delayed job
	h.AllowServiceAccount(email, &priv.PublicKey)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	token := authedToken(t, srv, email, priv)

	cols := gcpfocusbq.PinnedColumnNames()
	for i, c := range cols {
		cols[i] = "`" + c + "`"
	}
	query := "SELECT " + strings.Join(cols, ", ") +
		" FROM `p.d.t` WHERE BillingPeriodStart >= TIMESTAMP('2026-05-01T00:00:00Z')" +
		" AND BillingPeriodStart < TIMESTAMP('2026-06-01T00:00:00Z')"
	resp, body := doJSON(t, http.MethodPost, srv.URL+"/projects/job-project/queries", token, map[string]any{
		"query": query, "location": "EU", "useLegacySql": false,
		"maxResults":    1000, // client asks for everything; fake still pages at PageSize
		"formatOptions": map[string]any{"useInt64Timestamp": true},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	var page struct {
		Rows      []json.RawMessage `json:"rows"`
		PageToken string            `json:"pageToken"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 1 {
		t.Fatalf("page rows = %d, want 1 (PageSize), body=%s", len(page.Rows), body)
	}
	if page.PageToken == "" {
		t.Fatalf("expected a pageToken when PageSize=1 and May has 2 rows; body=%s", body)
	}
}

func TestUnauthorizedBearerRejected(t *testing.T) {
	h := New(fixtureDir)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	resp, body := doJSON(t, http.MethodGet,
		srv.URL+"/projects/p/datasets/d/tables/t", "not-a-real-token", nil)
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "invalid bearer token") {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
}

func TestSchemaOrderExactAndCellKindsAcceptFixture(t *testing.T) {
	// Loading the real fixture through months() exercises order-exact schema
	// check and per-cell NUMERIC/TIMESTAMP kind validation.
	h := New(fixtureDir)
	months, err := h.months()
	if err != nil {
		t.Fatalf("months(): %v", err)
	}
	if len(months["2026-05"]) != 2 || len(months["2026-06"]) != 3 {
		t.Fatalf("months = %#v", months)
	}

	// A reordered schema must fail (order-exact, not set-equal).
	dir := t.TempDir()
	src, err := os.ReadFile(filepath.Join(fixtureDir, "2026-05.json"))
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	if err := json.Unmarshal(src, &env); err != nil {
		t.Fatal(err)
	}
	schema := env["schema"].(map[string]any)
	fields := schema["fields"].([]any)
	fields[0], fields[1] = fields[1], fields[0]
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2026-05.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	h2 := New(dir)
	if _, err := h2.months(); err == nil || !strings.Contains(err.Error(), "order-exact") {
		t.Fatalf("reordered schema error = %v", err)
	}
}
