// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

// decodeLogLine decodes the single JSON access-log record from buf.
func decodeLogLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	line := bytes.TrimSpace(buf.Bytes())
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		t.Fatalf("access-log line is not JSON: %v\n%s", err, line)
	}
	return m
}

// assertNumericDuration checks duration_ms is present and decodes as a JSON
// number. Its MAGNITUDE is never asserted — a fast in-process request logs 0.
func assertNumericDuration(t *testing.T, m map[string]any) {
	t.Helper()
	v, ok := m["duration_ms"]
	if !ok {
		t.Error("duration_ms missing from the access-log record")
		return
	}
	if _, ok := v.(float64); !ok { // JSON numbers decode to float64
		t.Errorf("duration_ms = %v (%T), want a JSON number", v, v)
	}
}

// assertNoLeak fails if the log text contains any secret or the Authorization
// header name.
func assertNoLeak(t *testing.T, logText string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if s != "" && strings.Contains(logText, s) {
			t.Errorf("access log leaked a secret %q:\n%s", s, logText)
		}
	}
	if strings.Contains(strings.ToLower(logText), "authorization") {
		t.Errorf("access log contains the Authorization header:\n%s", logText)
	}
}

// TestAccessLog is the access-log contract (required test 4): the CONCRETE authn
// value is logged (denied on a 401, ok on an authenticated request, plus user
// for forward-auth); the token, the Authorization header, and a ?token=SECRET
// query value are NEVER present; duration_ms is present and numeric (magnitude
// un-asserted). It wires accessLog(auth(mux)) exactly as serve does.
func TestAccessLog(t *testing.T) {
	const token = "super-secret-token-value"
	const querySecret = "SUPERSECRETQUERYVALUE"
	const gatedPath = "/api/v1/costs/daily?token=" + querySecret
	cfg := NewBearerAuth(token)

	bearerHandler := func(buf *bytes.Buffer) http.Handler {
		return AccessLog(buf, "bearer")(NewHandler("test", testStatic(), &fakeStore{}, "", WithAuth(cfg)))
	}

	t.Run("denied request logs authn=denied, leaks no secret", func(t *testing.T) {
		var buf bytes.Buffer
		req := httptest.NewRequest(http.MethodGet, gatedPath, nil)
		req.Header.Set("Authorization", "Bearer definitely-wrong-token")
		w := httptest.NewRecorder()
		bearerHandler(&buf).ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", w.Code)
		}
		m := decodeLogLine(t, &buf)
		if m["authn"] != "denied" {
			t.Errorf("authn = %v, want denied", m["authn"])
		}
		if m["path"] != "/api/v1/costs/daily" {
			t.Errorf("path = %v, want /api/v1/costs/daily (the query string must not be logged)", m["path"])
		}
		if m["auth_mode"] != "bearer" {
			t.Errorf("auth_mode = %v, want bearer", m["auth_mode"])
		}
		assertNoLeak(t, buf.String(), querySecret, "definitely-wrong-token")
		assertNumericDuration(t, m)
	})

	t.Run("authenticated request logs authn=ok, leaks neither the bearer token nor ?token", func(t *testing.T) {
		var buf bytes.Buffer
		req := httptest.NewRequest(http.MethodGet, gatedPath, nil)
		req.Header.Set("Authorization", "Bearer "+token) // carries a real token AND ?token=SECRET
		w := httptest.NewRecorder()
		bearerHandler(&buf).ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		m := decodeLogLine(t, &buf)
		if m["authn"] != "ok" {
			t.Errorf("authn = %v, want ok", m["authn"])
		}
		assertNoLeak(t, buf.String(), token, querySecret)
		assertNumericDuration(t, m)
	})

	t.Run("forward-auth authenticated request logs authn=ok and user", func(t *testing.T) {
		fwd := NewForwardAuth("X-WEBAUTH-USER", []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")})
		var buf bytes.Buffer
		h := AccessLog(&buf, "forward-auth")(NewHandler("test", testStatic(), &fakeStore{}, "", WithAuth(fwd)))
		req := httptest.NewRequest(http.MethodGet, "/api/v1/costs/daily", nil)
		req.RemoteAddr = "127.0.0.1:5555"
		req.Header.Set("X-WEBAUTH-USER", "alice")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		m := decodeLogLine(t, &buf)
		if m["authn"] != "ok" || m["user"] != "alice" || m["auth_mode"] != "forward-auth" {
			t.Errorf("log = %v, want authn=ok user=alice auth_mode=forward-auth", m)
		}
		assertNumericDuration(t, m)
	})

	t.Run("exempt path logs authn=exempt", func(t *testing.T) {
		var buf bytes.Buffer
		h := AccessLog(&buf, "bearer")(NewHandler("test", testStatic(), &fakeStore{}, "", WithAuth(cfg)))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		m := decodeLogLine(t, &buf)
		if m["authn"] != "exempt" {
			t.Errorf("authn = %v for /healthz, want exempt", m["authn"])
		}
	})

	t.Run("no auth configured logs authn=disabled for a gated path", func(t *testing.T) {
		var buf bytes.Buffer
		// No WithAuth: the auth-free NewHandler default, wrapped only by the log.
		h := AccessLog(&buf, "disabled")(NewHandler("test", testStatic(), &fakeStore{}, ""))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil))
		m := decodeLogLine(t, &buf)
		if m["authn"] != "disabled" || m["auth_mode"] != "disabled" {
			t.Errorf("log = %v, want authn=disabled auth_mode=disabled", m)
		}
	})
}
