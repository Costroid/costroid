// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package api

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

// reachHandler records whether the inner handler ran, so a test can prove a 401
// short-circuited before the protected handler.
type reachHandler struct{ reached bool }

type recordingMux struct {
	patterns []string
}

func (m *recordingMux) HandleFunc(pattern string, _ func(http.ResponseWriter, *http.Request)) {
	m.patterns = append(m.patterns, pattern)
}

func (m *recordingMux) ServeHTTP(http.ResponseWriter, *http.Request) {}

func (rh *reachHandler) next(status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		rh.reached = true
		w.WriteHeader(status)
	})
}

// withRecord attaches a fresh authnRecord to the request so a test can read the
// outcome the auth middleware records (the access log seeds this in production).
func withRecord(r *http.Request) (*http.Request, *authnRecord) {
	rec := &authnRecord{}
	return r.WithContext(context.WithValue(r.Context(), authnCtxKey{}, rec)), rec
}

// TestBearerAuth is the bearer functional contract (required test 1). It tests
// tokenOK/bearerOK directly and the middleware's 401 behavior. Constant-time is
// a property no black-box test can prove — it is REVIEW-enforced (§P1); this is
// NOT dressed up as a "constant-time" test.
func TestBearerAuth(t *testing.T) {
	const token = "s3cret-token-value"
	want := sha256.Sum256([]byte(token))

	// tokenOK: exact match, same-length-different, different-length.
	if !tokenOK(token, want) {
		t.Error("tokenOK rejected the exact configured token")
	}
	sameLenDiff := "S3CRET-TOKEN-VALUE" // same length, different bytes
	if len(sameLenDiff) != len(token) {
		t.Fatalf("test setup: %q is not the same length as the token", sameLenDiff)
	}
	if tokenOK(sameLenDiff, want) {
		t.Error("tokenOK accepted a same-length but different token")
	}
	if tokenOK(token+"-extra", want) {
		t.Error("tokenOK accepted a different-length token")
	}

	req := func(header string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		return r
	}
	parse := []struct {
		name   string
		header string
		want   bool
	}{
		{"exact", "Bearer " + token, true},
		{"scheme lowercase", "bearer " + token, true},
		{"scheme mixed case", "BeArEr " + token, true},
		{"wrong same-length token", "Bearer " + sameLenDiff, false},
		{"missing header", "", false},
		{"no scheme", token, false},
		{"empty token", "Bearer ", false},
		{"wrong scheme", "Basic " + token, false},
	}
	for _, tc := range parse {
		t.Run("bearerOK/"+tc.name, func(t *testing.T) {
			if got := bearerOK(req(tc.header), want); got != tc.want {
				t.Errorf("bearerOK(%q) = %v, want %v", tc.header, got, tc.want)
			}
		})
	}

	cfg := NewBearerAuth(token)
	t.Run("missing/malformed token → 401", func(t *testing.T) {
		inner := &reachHandler{}
		r, rec := withRecord(req(""))
		w := httptest.NewRecorder()
		cfg.middleware(inner.next(http.StatusOK)).ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", w.Code)
		}
		if got := w.Header().Get("WWW-Authenticate"); got != "Bearer" {
			t.Errorf("WWW-Authenticate = %q, want Bearer", got)
		}
		if !strings.Contains(w.Body.String(), `"error":"unauthorized"`) {
			t.Errorf("body = %q, want the unauthorized JSON body", w.Body.String())
		}
		if inner.reached {
			t.Error("inner handler ran despite a 401")
		}
		if rec.result != authnDenied {
			t.Errorf("record.result = %q, want denied", rec.result)
		}
	})
	t.Run("valid token reaches the inner handler", func(t *testing.T) {
		inner := &reachHandler{}
		r, rec := withRecord(req("Bearer " + token))
		r.Header.Set(recommendedIdentityHeader, "client-supplied")
		w := httptest.NewRecorder()
		var downstreamIdentity string
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			inner.reached = true
			downstreamIdentity = r.Header.Get(recommendedIdentityHeader)
			w.WriteHeader(http.StatusOK)
		})
		cfg.middleware(next).ServeHTTP(w, r)
		if w.Code != http.StatusOK || !inner.reached {
			t.Fatalf("status = %d reached = %v, want 200 and inner reached", w.Code, inner.reached)
		}
		if rec.result != authnOK {
			t.Errorf("record.result = %q, want ok", rec.result)
		}
		if downstreamIdentity != "" {
			t.Errorf("bearer-authenticated downstream saw client identity header %q", downstreamIdentity)
		}
	})
}

// TestForwardAuth is the forward-auth contract (required test 2): trusted peer +
// header authenticates and records the identity; an untrusted peer with the SAME
// request is a spoof → 401 with the inner handler NOT run; a trusted peer with
// no header → 401. The trusted vs untrusted requests are byte-identical except
// RemoteAddr, so the peer check is provably the only variable.
func TestForwardAuth(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	const header = "X-WEBAUTH-USER"
	cfg := NewForwardAuth(header, trusted)

	// build is the ONE request shape; only RemoteAddr differs between the
	// trusted and spoof cases.
	build := func(remote string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
		r.RemoteAddr = remote
		r.Header.Set(header, "alice")
		return r
	}

	t.Run("trusted peer + header authenticates, records identity, strips header", func(t *testing.T) {
		inner := &reachHandler{}
		var downstreamHeader string
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			inner.reached = true
			downstreamHeader = r.Header.Get(header) // must be stripped (defense-in-depth)
			w.WriteHeader(http.StatusOK)
		})
		r, rec := withRecord(build("127.0.0.1:40000"))
		w := httptest.NewRecorder()
		cfg.middleware(next).ServeHTTP(w, r)
		if w.Code != http.StatusOK || !inner.reached {
			t.Fatalf("status = %d reached = %v, want 200 and inner reached", w.Code, inner.reached)
		}
		if rec.result != authnOK || rec.user != "alice" {
			t.Errorf("record = %+v, want ok/alice", rec)
		}
		if downstreamHeader != "" {
			t.Errorf("downstream saw the trusted header %q; it must be stripped before ServeHTTP", downstreamHeader)
		}
	})

	t.Run("untrusted peer + header is a spoof: 401, header ignored, inner not run", func(t *testing.T) {
		inner := &reachHandler{}
		next := inner.next(http.StatusOK)
		// BYTE-IDENTICAL to the trusted request except RemoteAddr.
		r, rec := withRecord(build("203.0.113.9:40000"))
		w := httptest.NewRecorder()
		cfg.middleware(next).ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 for an untrusted (spoofing) peer", w.Code)
		}
		if inner.reached {
			t.Error("inner handler ran for an untrusted peer — the spoofed identity header was trusted")
		}
		if rec.result != authnDenied || rec.user != "" {
			t.Errorf("record = %+v, want denied and no user", rec)
		}
	})

	t.Run("trusted peer + missing header → 401", func(t *testing.T) {
		inner := &reachHandler{}
		r := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
		r.RemoteAddr = "127.0.0.1:40000" // trusted peer, but no identity header
		w := httptest.NewRecorder()
		cfg.middleware(inner.next(http.StatusOK)).ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized || inner.reached {
			t.Fatalf("status = %d reached = %v, want 401 and inner not run", w.Code, inner.reached)
		}
	})

	t.Run("IPv4-mapped IPv6 loopback peer is trusted (Unmap is load-bearing)", func(t *testing.T) {
		inner := &reachHandler{}
		r := build("[::ffff:127.0.0.1]:40000")
		w := httptest.NewRecorder()
		cfg.middleware(inner.next(http.StatusOK)).ServeHTTP(w, r)
		if w.Code != http.StatusOK || !inner.reached {
			t.Fatalf("status = %d reached = %v, want 200 — Unmap must map ::ffff:127.0.0.1 into 127.0.0.0/8", w.Code, inner.reached)
		}
	})
}

// TestExemptPaths proves /healthz and the static shell are NEVER gated, under
// BOTH modes, without credentials (required test 3). It asserts != 401 (not ==
// 200), because the in-process fstest.MapFS 404s some static paths — a == 200
// assertion would fail for the wrong reason. A gated /api/ path is asserted to
// still 401, so the exemption is proven non-vacuous.
func TestExemptPaths(t *testing.T) {
	modes := map[string]AuthConfig{
		"bearer":  NewBearerAuth("tok"),
		"forward": NewForwardAuth("X-WEBAUTH-USER", []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}),
	}
	exempt := []string{"/healthz", "/", "/assets/app.js", "/index.html"}
	for modeName, cfg := range modes {
		handler := NewHandler("test", testStatic(), &fakeStore{}, "", WithAuth(cfg))
		for _, p := range exempt {
			t.Run(modeName+" exempt "+p, func(t *testing.T) {
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
				if w.Code == http.StatusUnauthorized {
					t.Fatalf("%s under %s mode returned 401 without creds; exempt paths must never be gated", p, modeName)
				}
			})
		}
		t.Run(modeName+" gated /api still 401 without creds", func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("/api/v1/meta under %s mode = %d without creds, want 401 (proves the gate is active)", modeName, w.Code)
			}
		})
	}
}

// TestRouteGateGuard records the routes the generated scaffolding actually
// registers plus the static root, and asserts that every data route is under
// /api/ while the only non-/api/ routes are the exempt allowlist. The exact
// method set is a codegen-drift detector, not a security guard: gated is based
// only on path prefixes and readOnly applies independently of route lookup.
func TestRouteGateGuard(t *testing.T) {
	exempt := map[string]bool{"/healthz": true, "/": true}
	wantMethods := map[string]string{
		"/":                            "",
		"/healthz":                     http.MethodGet,
		"/api/v1/anomalies":            http.MethodGet,
		"/api/v1/business-metrics":     http.MethodGet,
		"/api/v1/costs/daily":          http.MethodGet,
		"/api/v1/costs/summary":        http.MethodGet,
		"/api/v1/insights":             http.MethodGet,
		"/api/v1/meta":                 http.MethodGet,
		"/api/v1/query":                http.MethodPost,
		"/api/v1/sync/status":          http.MethodGet,
		"/api/v1/unit-economics/daily": http.MethodGet,
		"/api/v1/usage/metrics/daily":  http.MethodGet,
		"/api/v1/usage/tokens/daily":   http.MethodGet,
	}
	mux := &recordingMux{}
	_ = HandlerWithOptions(NewServer("test", nil, ""), StdHTTPServerOptions{BaseRouter: mux})
	methods := map[string]string{"/": ""} // NewHandler's static route is outside generated scaffolding.
	for _, pattern := range mux.patterns {
		method, route, ok := strings.Cut(pattern, " ")
		if !ok {
			t.Fatalf("generated route pattern %q has no method prefix", pattern)
		}
		methods[route] = method
	}
	if !reflect.DeepEqual(methods, wantMethods) {
		t.Fatalf("registered method set = %v, want %v", methods, wantMethods)
	}
	for r := range methods {
		isGated := gated(r)
		if exempt[r] {
			if isGated {
				t.Errorf("exempt route %q is gated — it must be reachable without credentials", r)
			}
			continue
		}
		if !isGated {
			t.Errorf("data route %q is NOT gated — it would ship unauthenticated; add it under /api/, to the gate, or to the exempt allowlist", r)
		}
	}
}
