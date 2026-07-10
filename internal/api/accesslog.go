// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package api

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// authn result labels recorded per request.
const (
	authnOK       = "ok"       // gated path, credentials accepted
	authnDenied   = "denied"   // gated path, credentials rejected (401)
	authnDisabled = "disabled" // gated path, no auth configured (--no-auth)
	authnExempt   = "exempt"   // /healthz or static shell, never gated
)

// authnRecord is the seeded mutable holder the access log uses to learn a
// request's authentication outcome. Because Go request context flows DOWNWARD
// only, an inner middleware's context.WithValue is invisible to the outer
// access-log middleware after ServeHTTP returns. So the OUTER access-log
// middleware seeds a *authnRecord into the context before calling next, and the
// INNER auth middleware MUTATES the pointee (on both the allow and the deny
// paths). The access log reads it after ServeHTTP. This mirrors the status
// recorder: the outer layer owns a shared mutable object, the inner one fills
// it in.
type authnRecord struct {
	result string // "", "ok", or "denied"; "" means auth did not run
	user   string // forward-auth identity only; never a bearer token
}

type authnCtxKey struct{}

// authnRecordFrom returns the seeded authnRecord, or nil when the access-log
// middleware is not installed (e.g. the auth-only NewHandler wiring in tests).
// The auth middleware is written to tolerate a nil record.
func authnRecordFrom(ctx context.Context) *authnRecord {
	rec, _ := ctx.Value(authnCtxKey{}).(*authnRecord)
	return rec
}

// statusRecorder wraps a ResponseWriter to capture the status code. It is
// SEEDED with 200 (§P4): a handler that Writes without an explicit WriteHeader
// implicitly sends 200, which would otherwise log as 0.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// AccessLog returns the OUTERMOST access-log middleware: one structured JSON
// record per request written to out. authMode is the process's configured mode
// ("bearer", "forward-auth", or "disabled"), logged verbatim as auth_mode. It
// seeds the authnRecord the inner auth middleware fills in.
func AccessLog(out io.Writer, authMode string) func(http.Handler) http.Handler {
	logger := slog.New(slog.NewJSONHandler(out, nil))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &authnRecord{}
			ctx := context.WithValue(r.Context(), authnCtxKey{}, rec)
			sr := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sr, r.WithContext(ctx))

			authn := rec.result
			if authn == "" {
				// The auth middleware did not record an outcome: either the
				// path is exempt (never gated) or no auth is configured for
				// this gated path.
				if gated(r.URL.Path) {
					authn = authnDisabled
				} else {
					authn = authnExempt
				}
			}

			attrs := []slog.Attr{
				slog.String("method", r.Method),
				// CARDINAL-RULE- & CREDENTIAL-SAFE: only r.URL.Path may be
				// logged — never RawQuery/RequestURI/URL.String or any header.
				// The query string can carry secrets (e.g. ?token=…) and the
				// Authorization header carries the bearer token; neither is a
				// safe log field.
				slog.String("path", r.URL.Path),
				slog.Int("status", sr.status),
				slog.Int64("duration_ms", time.Since(start).Milliseconds()),
				slog.String("remote", remoteIP(r)),
				slog.String("authn", authn),
				slog.String("auth_mode", authMode),
			}
			if rec.user != "" { // forward-auth identity only; never for bearer
				attrs = append(attrs, slog.String("user", rec.user))
			}
			logger.LogAttrs(r.Context(), slog.LevelInfo, "http_request", attrs...)
		})
	}
}

// remoteIP returns the request peer's IP without its port, for the access log.
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
