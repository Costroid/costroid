// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package alert

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/credentials"
	"github.com/Costroid/costroid/internal/storage"
)

type capturedRequest struct {
	method      string
	contentType string
	auth        string
	body        []byte
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

// testPoster mirrors the production poster but with a short backoff so the
// bounded-retry path costs no meaningful wall-clock; the tests never block on a
// real sleep.
func testPoster(client *http.Client) httpPoster {
	return httpPoster{client: client, timeout: 2 * time.Second, backoff: time.Millisecond, retries: maxRetries}
}

func failingRun() storage.SyncRun {
	return storage.SyncRun{
		SourceName: "aws-prod", Connector: "aws-focus-s3", TenantID: "default",
		Outcome: "error", Error: "429 Too Many Requests",
		StartedAt:        time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC),
		FinishedAt:       time.Date(2026, 7, 17, 0, 1, 0, 0, time.UTC),
		PeriodsProcessed: 2, PeriodsSkipped: 1, RecordsIngested: 5,
	}
}

func TestWebhookChannelPostsWhitelistedPayloadWithAuth(t *testing.T) {
	capCh := make(chan capturedRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capCh <- capturedRequest{r.Method, r.Header.Get("Content-Type"), r.Header.Get("Authorization"), body}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	secret := credentials.NewSecret("super-secret-token")
	channel, err := NewWebhookChannel("ops", srv.URL, &secret)
	if err != nil {
		t.Fatalf("NewWebhookChannel: %v", err)
	}
	if err := channel.Send(context.Background(), buildMessage(failingRun(), KindFailing)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := <-capCh

	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.method)
	}
	if got.contentType != "application/json" {
		t.Errorf("content-type = %q", got.contentType)
	}
	if got.auth != "Bearer super-secret-token" {
		t.Errorf("authorization = %q", got.auth)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(got.body, &fields); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(fields) != len(whitelistKeys) {
		t.Fatalf("payload keys = %v, want exactly %v", mapKeys(fields), whitelistKeys)
	}
	var decoded Message
	if err := json.Unmarshal(got.body, &decoded); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	if decoded.Kind != KindFailing || decoded.Source != "aws-prod" || decoded.Outcome != "error" || decoded.Error != "429 Too Many Requests" {
		t.Errorf("decoded = %+v", decoded)
	}
	// The bearer token is a header, never the body.
	if strings.Contains(string(got.body), "super-secret-token") || strings.Contains(string(got.body), "Bearer") {
		t.Errorf("payload leaked a credential: %s", got.body)
	}
}

func TestWebhookChannelNoAuthOmitsAuthorization(t *testing.T) {
	capCh := make(chan capturedRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capCh <- capturedRequest{r.Method, r.Header.Get("Content-Type"), r.Header.Get("Authorization"), body}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	channel, err := NewWebhookChannel("ops", srv.URL, nil)
	if err != nil {
		t.Fatalf("NewWebhookChannel: %v", err)
	}
	if err := channel.Send(context.Background(), buildMessage(failingRun(), KindFailing)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := <-capCh; got.auth != "" {
		t.Errorf("authorization header set without a configured secret: %q", got.auth)
	}
}

func TestSlackChannelPostsTextShape(t *testing.T) {
	capCh := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q", ct)
		}
		body, _ := io.ReadAll(r.Body)
		capCh <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// The Slack incoming-webhook URL is itself the secret.
	channel := NewSlackChannel("slack", credentials.NewSecret(srv.URL))
	run := storage.SyncRun{SourceName: "openai-cost", Connector: "openai-cost", TenantID: "default", Outcome: "error", Error: "429 Too Many Requests"}
	if err := channel.Send(context.Background(), buildMessage(run, KindFailing)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	body := <-capCh

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(fields) != 1 {
		t.Fatalf("slack payload keys = %v, want only \"text\"", mapKeys(fields))
	}
	var decoded struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode text: %v", err)
	}
	if !strings.Contains(decoded.Text, "openai-cost") || !strings.Contains(decoded.Text, "error") {
		t.Errorf("slack text missing source or outcome: %q", decoded.Text)
	}
}

func TestWebhookChannelRetriesOnceOn5xx(t *testing.T) {
	var mu sync.Mutex
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		n := hits
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	channel := &webhookChannel{name: "ops", endpoint: mustURL(t, srv.URL), poster: testPoster(srv.Client())}
	if err := channel.Send(context.Background(), buildMessage(failingRun(), KindFailing)); err != nil {
		t.Fatalf("Send should succeed on the retry: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 2 {
		t.Fatalf("hits = %d, want 2 (one retry after a 5xx)", hits)
	}
}

func TestWebhookChannelNon2xxDoesNotRetryAndHidesSecret(t *testing.T) {
	var mu sync.Mutex
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	secret := credentials.NewSecret("super-secret-token")
	channel := &webhookChannel{name: "ops", endpoint: mustURL(t, srv.URL), auth: &secret, poster: testPoster(srv.Client())}
	err := channel.Send(context.Background(), buildMessage(failingRun(), KindFailing))
	if err == nil {
		t.Fatal("a 4xx response should return an error")
	}
	if strings.Contains(err.Error(), "super-secret-token") || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("error leaked a secret or the endpoint: %v", err)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("error should name the status code: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 1 {
		t.Fatalf("a 4xx must not retry: hits = %d", hits)
	}
}

func TestWebhookChannelAbandonsHungEndpointAtTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer srv.Close()
	defer close(block)

	channel := &webhookChannel{
		name:     "ops",
		endpoint: mustURL(t, srv.URL),
		poster:   httpPoster{client: srv.Client(), timeout: 100 * time.Millisecond, backoff: time.Millisecond, retries: maxRetries},
	}
	done := make(chan error, 1)
	go func() { done <- channel.Send(context.Background(), buildMessage(failingRun(), KindFailing)) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a hung endpoint should return an error at the per-send timeout")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Send did not honor the per-send timeout")
	}
}

func TestValidateWebhookEndpoint(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"https ok", "https://hooks.example.com/services/x", false},
		{"https with query preserved-ok", "https://hooks.example.com/x?token=abc", false},
		{"loopback http ok", "http://127.0.0.1:9000/hook", false},
		{"localhost http ok", "http://localhost/hook", false},
		{"http non-loopback rejected", "http://example.com/hook", true},
		{"userinfo rejected", "https://user:pass@example.com/hook", true},
		{"fragment rejected", "https://example.com/hook#frag", true},
		{"non-http scheme rejected", "ftp://example.com/hook", true},
		{"missing host rejected", "https:///hook", true},
		{"unparseable rejected", "://nope", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateWebhookEndpoint(tc.raw); (err != nil) != tc.wantErr {
				t.Fatalf("ValidateWebhookEndpoint(%q) err = %v, wantErr = %v", tc.raw, err, tc.wantErr)
			}
		})
	}
	// The path and query string are preserved verbatim (unlike the connectors'
	// validateEndpoint, which mutates the path and rejects a query).
	u, err := validateWebhookEndpoint("https://hooks.example.com/services/a/b?x=1&y=2")
	if err != nil {
		t.Fatalf("valid endpoint rejected: %v", err)
	}
	if u.Path != "/services/a/b" || u.RawQuery != "x=1&y=2" {
		t.Fatalf("path/query not preserved: path=%q query=%q", u.Path, u.RawQuery)
	}
}

func TestRedactTransportErrorDropsURL(t *testing.T) {
	secretURL := "https://hooks.slack.com/services/T000/B000/SECRETTOKEN"
	uerr := &url.Error{Op: "Post", URL: secretURL, Err: io.EOF}
	redacted := redactTransportError(uerr)
	if strings.Contains(redacted.Error(), secretURL) || strings.Contains(redacted.Error(), "SECRETTOKEN") {
		t.Fatalf("redactTransportError leaked the URL: %v", redacted)
	}
}
