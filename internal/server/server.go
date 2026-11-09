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
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/oidc"
	"github.com/Costroid/costroid/internal/scm"
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
	Attest(ctx context.Context, tenant store.TenantID, minted store.MintedCredential) error
	MintedCredentials(ctx context.Context, tenant store.TenantID, keys []string) (map[string]store.MintedCredential, error)
}

// SpendLedger keeps what costroid gateway close attests: the gateway's own
// count of a minted key's spend, in USD. The count is the gateway's, so it
// stands as a budget's evidence where the token counts on the rows cannot.
type SpendLedger interface {
	// AttestSpend records a key's USD spend. The key must be attested as
	// minted for the run named: store.ErrAttestedElsewhere and
	// store.ErrNotMinted say it is not. A key's spend is the largest figure
	// attested for it, because a gateway's count only grows.
	AttestSpend(ctx context.Context, tenant store.TenantID, key store.MintedCredential, usd decimal.Decimal) error
	// GatewaySpend reads the pull request's measured spend. Rows counts its
	// budget-eligible rows whose credential is a key minted for the row's own
	// run, with credential_basis minted, and whose spend a close job attested.
	// USD adds up the attested spend of those keys, each key once.
	GatewaySpend(ctx context.Context, tenant store.TenantID, repo, pr string) (rows int64, usd decimal.Decimal, err error)
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
	// GatewayEnvironment is the GitHub environment whose jobs may attest a
	// minted credential. Empty trusts none, and then no row reads exact.
	GatewayEnvironment string
	// Spend keeps the gateway's attested spend. Nil, the service refuses a
	// spend attestation and treats every token-verified spend as agent counts.
	Spend SpendLedger
	// App serves POST /webhooks/github. Nil, the route is not served.
	App    *GitHubApp
	Now    func() time.Time
	Logger *slog.Logger
}

// Server handles ingest and the view.
type Server struct {
	Config
	webhook *scm.Webhook
	queue   *scm.Queue
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
	server := &Server{Config: config}
	if config.App != nil {
		server.webhook, server.queue = newWebhook(config)
	}
	return server
}

// Handler returns the service's routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("POST /v1/rows", s.handleRows)
	mux.HandleFunc("POST /v1/credentials", s.handleCredentials)
	mux.HandleFunc("POST /v1/credentials/{key}/spend", s.handleSpend)
	mux.HandleFunc("POST /otlp/v1/traces", s.handleTraces)
	mux.HandleFunc("POST /otlp/v1/logs", s.handleLogs)
	mux.HandleFunc("POST /otlp/v1/metrics", s.handleMetrics)
	mux.HandleFunc("GET /v1/pulls/{pr}", s.handlePullRequest)
	mux.HandleFunc("POST /v1/pulls/{pr}/verdict", s.handleVerdict)
	if s.webhook != nil {
		mux.Handle("POST "+webhookPath, s.webhook)
	}
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
	codeAttestation  = "ATTESTATION_FORBIDDEN"
	codeInvalidRow   = "ROW_INVALID"
	codeUnknownPrice = "PROVIDER_UNKNOWN"
	codeBodyTooLarge = "BODY_TOO_LARGE"
	codeMalformed    = "BODY_MALFORMED"
	codeWrongRepo    = "REPOSITORY_FORBIDDEN"
	codeBadQuery     = "QUERY_INVALID"
	codeEgress       = "EGRESS_REFUSED"
	codeNoSpend      = "SPEND_NOT_KEPT"
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
