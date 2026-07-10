// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"net"
	"net/http"
	"net/netip"
	"path"
	"strings"
)

// authKind discriminates the configured authentication mode. Exactly one mode
// is ever configured per process (serve enforces this fail-closed).
type authKind int

const (
	authBearer  authKind = iota + 1 // Authorization: Bearer <token>
	authForward                     // trusted-header / forward-auth
)

// AuthConfig is the resolved authentication configuration for the HTTP handler.
// Its fields are UNEXPORTED on purpose: a bearer token is only ever held as its
// SHA-256 digest (the raw token never becomes a stored field), and the forward-
// auth trust set is fixed at construction. Build one via NewBearerAuth or
// NewForwardAuth and install it with WithAuth.
type AuthConfig struct {
	kind authKind

	// tokenSHA256 is the SHA-256 digest of the configured bearer token. The
	// raw token is hashed in NewBearerAuth and never retained (§P1).
	tokenSHA256 [32]byte

	// headerName is the forward-auth trusted identity header (e.g.
	// X-WEBAUTH-USER). Enablement is by presence: a non-empty header name.
	headerName string
	// trustedProxies is the forward-auth trusted TCP-peer allowlist. The
	// identity header is honored ONLY when the real peer is in this set.
	trustedProxies []netip.Prefix
}

// NewBearerAuth returns a bearer-token AuthConfig. The token is hashed to its
// SHA-256 digest immediately; the raw token is not retained on the returned
// value (§P1). Callers pass the token in from a file or the environment, never
// from argv.
func NewBearerAuth(token string) AuthConfig {
	return AuthConfig{kind: authBearer, tokenSHA256: sha256.Sum256([]byte(token))}
}

// NewForwardAuth returns a trusted-header / forward-auth AuthConfig. headerName
// is the identity header a trusted reverse proxy sets (e.g. X-WEBAUTH-USER);
// trusted is the allowlist of proxy peer CIDRs. The header is honored only when
// the real TCP peer is in trusted (§P2, §P5).
func NewForwardAuth(headerName string, trusted []netip.Prefix) AuthConfig {
	return AuthConfig{kind: authForward, headerName: headerName, trustedProxies: trusted}
}

// gated reports whether a request path holds billing DATA that must be
// authenticated. ALL billing data lives under /api/; /healthz and the static
// SPA shell (/, assets) are always exempt.
//
// DENYLIST-AWARENESS INVARIANT: this /api/ prefix is the ONE gate. Any NEW
// data endpoint registered OUTSIDE /api/ (see api.gen.go's route table) MUST be
// added here and to the route-gate guard test, or it would ship
// unauthenticated. The path is path.Clean'd first so an encoded prefix such as
// /%2Fapi/... (which net/http decodes to //api/...) cannot dodge the gate.
func gated(urlPath string) bool {
	return strings.HasPrefix(path.Clean(urlPath), "/api/")
}

// middleware returns the auth middleware for this configuration. It gates only
// data endpoints (gated); exempt paths pass straight through. On a gated
// request it authenticates per mode and, on both the allow and deny paths,
// records the outcome into the access-log holder seeded by the outer
// middleware (nil-safe when the access log is not installed).
func (c AuthConfig) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !gated(r.URL.Path) {
			next.ServeHTTP(w, r) // /healthz + static shell: no credentials needed.
			return
		}
		rec := authnRecordFrom(r.Context())
		switch c.kind {
		case authBearer:
			if !bearerOK(r, c.tokenSHA256) {
				writeUnauthorized(w, rec, true)
				return
			}
			if rec != nil {
				rec.result = authnOK
			}
		case authForward:
			user, ok := c.forwardOK(r)
			if !ok {
				writeUnauthorized(w, rec, false)
				return
			}
			// Defense-in-depth: strip the trusted header so it never reaches
			// downstream handlers (they must not re-derive identity from it).
			r.Header.Del(c.headerName)
			if rec != nil {
				rec.result = authnOK
				rec.user = user
			}
		default:
			// Unconfigured AuthConfig: never installed by serve. Fail closed.
			writeUnauthorized(w, rec, false)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearerOK reports whether the request carries a valid bearer token. The scheme
// is case-insensitive; a missing/malformed Authorization header is not valid.
// The token comparison is constant-time (§P1) — never compare the raw token.
func bearerOK(r *http.Request, want [32]byte) bool {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return false
	}
	return tokenOK(token, want)
}

// tokenOK compares a presented token against the configured digest in constant
// time. Both sides are hashed to a fixed 32 bytes first so ConstantTimeCompare
// (which short-circuits on differing lengths) cannot leak the token length; the
// SHA-256 here only equalizes length and hides content — both inputs are
// already secrets, so no salt/KDF is needed (§P1). Never use ==/bytes.Equal/
// strings.EqualFold on the token or on the digests.
func tokenOK(presented string, want [32]byte) bool {
	p := sha256.Sum256([]byte(presented))
	return subtle.ConstantTimeCompare(p[:], want[:]) == 1
}

// forwardOK authenticates a forward-auth request: the identity header is
// honored ONLY when the real TCP peer is trusted (peerTrusted). An untrusted
// peer means the header is ignored entirely — the caller returns 401 without
// running the inner handler. This is the anti-spoof guardrail (§P2, §P5): the
// trust decision is derived from r.RemoteAddr, NEVER from a client-supplied
// header such as X-Forwarded-For.
func (c AuthConfig) forwardOK(r *http.Request) (string, bool) {
	if !peerTrusted(r, c.trustedProxies) {
		return "", false
	}
	user := r.Header.Get(c.headerName)
	if user == "" {
		return "", false
	}
	return user, true
}

// peerTrusted reports whether the request's real TCP peer is within the trusted
// proxy allowlist. Unmap is mandatory (§P2): Prefix.Contains is address-family
// strict, so an IPv4-mapped IPv6 peer (::ffff:127.0.0.1) must be unmapped to
// 127.0.0.1 or a 127.0.0.0/8 rule would never match it.
func peerTrusted(r *http.Request, cidrs []netip.Prefix) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, p := range cidrs {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// writeUnauthorized records the denial in the access-log holder (nil-safe) and
// writes a 401 with a small JSON body. It sets WWW-Authenticate: Bearer only
// for bearer mode. No stack traces, no timing branch on the secret, and never
// the token or Authorization header in the response.
func writeUnauthorized(w http.ResponseWriter, rec *authnRecord, bearer bool) {
	if rec != nil {
		rec.result = authnDenied
	}
	if bearer {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthorized"}` + "\n"))
}
