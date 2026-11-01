package server

import (
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Costroid/costroid/internal/oidc"
	"github.com/Costroid/costroid/internal/store"
)

const pseudonymInfo = "costroid user pseudonym v1"

// caller is who a request proved to be. A nil identity means the tenant is
// known and the run is not.
type caller struct {
	tenant   store.TenantID
	identity *oidc.Identity
}

func (s *Server) authenticate(r *http.Request) (caller, error) {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return caller{}, invalidToken()
	}
	if strings.HasPrefix(token, store.KeyPrefix) {
		return s.authenticateKey(r.Context(), token)
	}
	return s.authenticateToken(r.Context(), token)
}

func invalidToken() *refusal {
	return refuse(http.StatusUnauthorized, codeAuth, "The token is absent, expired or not valid for this service.")
}

func (s *Server) authenticateKey(ctx context.Context, key string) (caller, error) {
	tenant, err := s.Ledger.TenantForKey(ctx, store.KeyDigest(key))
	if errors.Is(err, store.ErrNotFound) {
		return caller{}, invalidToken()
	}
	if err != nil {
		return caller{}, err
	}
	return caller{tenant: tenant}, nil
}

func (s *Server) authenticateToken(ctx context.Context, token string) (caller, error) {
	identity, err := s.Verifier.Verify(ctx, token)
	if err != nil {
		s.Logger.Info("token refused", "reason", refusalReason(err))
		return caller{}, invalidToken()
	}
	tenant, err := s.Ledger.TenantForOwner(ctx, identity.Host, identity.Owner)
	if errors.Is(err, store.ErrNotFound) {
		return caller{}, refuse(http.StatusForbidden, codeNoTenant, "No tenant holds this repository's owner.")
	}
	if err != nil {
		return caller{}, err
	}
	return caller{tenant: tenant, identity: &identity}, nil
}

// refusalReason names a verification failure for the log, and never echoes
// the token or anything inside it.
func refusalReason(err error) string {
	for _, known := range []error{
		oidc.ErrMalformed, oidc.ErrAlgorithm, oidc.ErrIssuer, oidc.ErrKeyUnavailable, oidc.ErrSignature,
		oidc.ErrAudience, oidc.ErrExpired, oidc.ErrNotYetValid, oidc.ErrClaims,
	} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return "oidc: unknown"
}

// pseudonym replaces an address with a per-tenant HMAC. Two tenants get
// unrelated values for one address, and the key is never stored.
func (s *Server) pseudonym(tenant store.TenantID, email string) (string, error) {
	key, err := hkdf.Key(sha256.New, s.PseudonymKey, []byte(tenant.String()), pseudonymInfo, sha256.Size)
	if err != nil {
		return "", fmt.Errorf("server: derive pseudonym key: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(strings.ToLower(strings.TrimSpace(email))))
	return "hmac:" + hex.EncodeToString(mac.Sum(nil)), nil
}
