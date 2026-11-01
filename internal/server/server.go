// Package server is the ingest service behind costroid serve: the result-stream
// and OTLP paths, pricing against the pinned catalog, and the per-PR view.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/oidc"
	"github.com/Costroid/costroid/internal/store"
)

const (
	// maxBodyBytes caps a request before and after decompression.
	maxBodyBytes = 32 << 20
	maxRowBytes  = 64 << 10
	// maxTextColumn is the row contract's limit on a text column.
	maxTextColumn = 512
)

// Ledger is what the service needs from the store.
type Ledger interface {
	TenantForOwner(ctx context.Context, host, owner string) (store.TenantID, error)
	TenantForKey(ctx context.Context, keySHA256 string) (store.TenantID, error)
	Ingest(ctx context.Context, tenant store.TenantID, batch store.Batch) (store.Receipt, error)
	PullRequest(ctx context.Context, tenant store.TenantID, repo, pr string) ([]store.Line, error)
}

// TokenVerifier checks a CI OIDC token.
type TokenVerifier interface {
	Verify(ctx context.Context, token string) (oidc.Identity, error)
}

// Config wires a Server.
type Config struct {
	Ledger   Ledger
	Verifier TokenVerifier
	Catalog  *catalog.Catalog
	// PseudonymKey is the master key the per-tenant HMAC keys derive from.
	PseudonymKey []byte
	Now          func() time.Time
	Logger       *slog.Logger
}

// Server handles ingest and the view.
type Server struct {
	Config
}

// New returns the service. Now and Logger default to the real clock and the
// default logger.
func New(config Config) *Server {
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &Server{Config: config}
}

// Handler returns the service's routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("POST /v1/rows", s.handleRows)
	mux.HandleFunc("POST /otlp/v1/traces", s.handleTraces)
	mux.HandleFunc("POST /otlp/v1/logs", s.handleLogs)
	mux.HandleFunc("POST /otlp/v1/metrics", s.handleMetrics)
	mux.HandleFunc("GET /v1/pulls/{pr}", s.handlePullRequest)
	return mux
}

// refusal is a typed answer. The code is the contract, the message is for
// people, and neither carries a value the sender supplied.
type refusal struct {
	status  int
	code    string
	message string
}

func (r *refusal) Error() string {
	return r.code + ": " + r.message
}

func refuse(status int, code, message string) *refusal {
	return &refusal{status: status, code: code, message: message}
}

// Refusal codes.
const (
	codeAuth         = "AUTH_TOKEN_INVALID"
	codeNoTenant     = "TENANT_NOT_BOUND"
	codeJoinKey      = "JOIN_KEY_MISMATCH"
	codeInvalidRow   = "ROW_INVALID"
	codeUnknownPrice = "PROVIDER_UNKNOWN"
	codeBodyTooLarge = "BODY_TOO_LARGE"
	codeMalformed    = "BODY_MALFORMED"
	codeWrongRepo    = "REPOSITORY_FORBIDDEN"
	codeBadQuery     = "QUERY_INVALID"
	codeEgress       = "EGRESS_REFUSED"
	codeInternal     = "INTERNAL"
)

const (
	contentTypeHeader = "Content-Type"
	jsonType          = "application/json"
)

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set(contentTypeHeader, jsonType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// fail answers a refusal, or a bare INTERNAL for anything unplanned, which is
// logged here and never sent.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var typed *refusal
	if !errors.As(err, &typed) {
		s.Logger.Error("request failed", "path", r.URL.Path, "error", err)
		typed = refuse(http.StatusInternalServerError, codeInternal, "The request could not be completed.")
	}
	writeJSON(w, typed.status, map[string]any{"error": map[string]string{"code": typed.code, "message": typed.message}})
}
