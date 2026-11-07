package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/Costroid/costroid/internal/store"
)

// attestation is what costroid gateway mint sends once it has registered a
// run's key: the key's id, the value rows carry in SubAccountId, and the run
// the key was minted for.
type attestation struct {
	KeyID      string `json:"key_id"`
	Repository string `json:"repository"`
	RunID      string `json:"run_id"`
	RunAttempt string `json:"run_attempt"`
}

// gatewayKeyID is the form of a gateway key's id: the SHA-256 of the key in hex.
var gatewayKeyID = regexp.MustCompile(`^[0-9a-f]{64}$`)

func attestationRefused(message string) *refusal {
	return refuse(http.StatusForbidden, codeAttestation, message)
}

// handleCredentials stores a minted-credential attestation. The token must
// come from a job in the gateway's environment, whose protection rules the
// repository's owner sets, and must name the run the attestation names. A
// sender cannot attest for a run it is not.
func (s *Server) handleCredentials(w http.ResponseWriter, r *http.Request) {
	who, err := s.authenticate(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	minted, err := s.readAttestation(w, r, who)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	err = s.Ledger.Attest(r.Context(), who.tenant, minted)
	if errors.Is(err, store.ErrAttestedElsewhere) {
		s.fail(w, r, refuse(http.StatusConflict, codeJoinKey, "The key is attested for another run."))
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) readAttestation(w http.ResponseWriter, r *http.Request, who caller) (store.MintedCredential, error) {
	id := who.identity
	switch {
	case id == nil:
		return store.MintedCredential{}, attestationRefused("An attestation needs a CI token.")
	case s.GatewayEnvironment == "" || id.Environment != s.GatewayEnvironment:
		return store.MintedCredential{}, attestationRefused("The token's job did not run in the gateway's environment.")
	}
	body, err := readBody(w, r)
	if err != nil {
		return store.MintedCredential{}, err
	}
	var stated attestation
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stated); err != nil || !gatewayKeyID.MatchString(stated.KeyID) {
		return store.MintedCredential{}, refuse(http.StatusBadRequest, codeMalformed, "The body is not an attestation with a 64-character key_id.")
	}
	for _, pair := range []struct{ key, stated, token string }{
		{"repository", strings.ToLower(stated.Repository), id.Repository},
		{"run", stated.RunID, id.RunID},
		{"attempt", stated.RunAttempt, id.RunAttempt},
	} {
		if pair.token == "" || pair.stated != pair.token {
			return store.MintedCredential{}, refuse(http.StatusForbidden, codeJoinKey, "The attestation's "+pair.key+" disagrees with the CI token.")
		}
	}
	return store.MintedCredential{
		KeySHA256: stated.KeyID, Repository: id.Repository, RunID: id.RunID, RunAttempt: id.RunAttempt,
		Environment: id.Environment,
	}, nil
}

// provenCredentials returns the credentials the batch states that were
// attested as minted for the token's run. A sender with no token, or a token
// naming no pull request, proves none.
func (s *Server) provenCredentials(ctx context.Context, who caller, charges []charge) (map[string]bool, error) {
	proven := map[string]bool{}
	id := who.identity
	if id == nil || id.PullRequest == "" {
		return proven, nil
	}
	seen := map[string]bool{}
	var keys []string
	for i := range charges {
		credential := charges[i].credential
		if seen[credential] || !charges[i].namesCredential() || !gatewayKeyID.MatchString(credential) {
			continue
		}
		seen[credential] = true
		keys = append(keys, credential)
	}
	if len(keys) == 0 {
		return proven, nil
	}
	attested, err := s.Ledger.MintedCredentials(ctx, who.tenant, keys)
	if err != nil {
		return nil, fmt.Errorf("server: read minted credentials: %w", err)
	}
	run := store.MintedCredential{Repository: id.Repository, RunID: id.RunID, RunAttempt: id.RunAttempt}
	for key, minted := range attested {
		proven[key] = id.RunAttempt != "" && minted.SameRun(run)
	}
	return proven, nil
}
