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

	"github.com/Costroid/costroid/internal/oidc"
	"github.com/Costroid/costroid/internal/store"
	"github.com/Costroid/costroid/internal/vcs"
)

// attestation is what costroid gateway mint sends once it has registered a
// run's key: the key's id, the value rows carry in SubAccountId, the run the
// key was minted for, and that run's head commit as GitHub's event names it.
type attestation struct {
	KeyID      string `json:"key_id"`
	Repository string `json:"repository"`
	RunID      string `json:"run_id"`
	RunAttempt string `json:"run_attempt"`
	HeadSHA    string `json:"head_sha"`
}

// The refusals of an attestation and of a spend, as the integration contract
// names them.
const (
	codeRunUnprotected    = "CREDENTIAL_RUN_UNPROTECTED"
	codeAttestedElsewhere = "CREDENTIAL_ATTESTED_ELSEWHERE"
	codeAttestingRunUsed  = "CREDENTIAL_ATTESTING_RUN_USED"
	codeNotAttested       = "CREDENTIAL_NOT_ATTESTED"
	codeSpendHeld         = "CREDENTIAL_SPEND_HELD"
)

const (
	// eventWorkflowRun is the event of a run that GitHub starts from the
	// default branch's workflow file after another run.
	eventWorkflowRun = "workflow_run"
	branchRef        = "refs/heads/"
	// protectedRefClaim is ref_protected as a token of a protected branch's
	// run holds it: the JSON string "true", which recorded GitHub Actions runs
	// 45109326900 and 45109345722 state. The comparison is on the claim's JSON
	// text, so a boolean, another case or any other value is not it.
	protectedRefClaim = `"true"`
	// unprotected ends each refusal of a token that is not such a run's.
	unprotected = " A key is attested by a workflow_run run of a protected branch."
)

var (
	// gatewayKeyID is the form of a gateway key's id: the SHA-256 of the key in hex.
	gatewayKeyID = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// runAttempt is GitHub's run attempt, a count from 1.
	runAttempt = regexp.MustCompile(`^[1-9][0-9]{0,9}$`)
)

func attestationRefused(message string) *refusal {
	return refuse(http.StatusForbidden, codeAttestation, message)
}

func unprotectedRun(message string) *refusal {
	return refuse(http.StatusForbidden, codeRunUnprotected, message+unprotected)
}

// attestingRun holds the token an attestation or a spend arrives under to a
// run a pull request cannot edit: one that workflow_run started on a
// protected branch, whose job ran in the gateway's environment. Such a run
// executes the protected branch's workflow file, so what it attests is not
// the word of whoever can push a branch. A job of a pull request's own run
// is refused whatever environment it reached.
func (s *Server) attestingRun(id *oidc.Identity) error {
	switch {
	case id == nil:
		return attestationRefused("An attestation needs a CI token.")
	case id.Event != eventWorkflowRun:
		return unprotectedRun("The token's run was not started by workflow_run.")
	case !strings.HasPrefix(id.Ref, branchRef):
		return unprotectedRun("The token's ref is not a branch.")
	case id.RefProtected != protectedRefClaim:
		return unprotectedRun(`The token does not state ref_protected as the string "true".`)
	case !runAttempt.MatchString(id.RunAttempt):
		return unprotectedRun("The token names no run attempt.")
	case s.GatewayEnvironment == "" || id.Environment != s.GatewayEnvironment:
		return attestationRefused("The token's job did not run in the gateway's environment.")
	}
	return nil
}

// namedRun holds the repository and the run an attestation or a spend names
// to the token it arrived under: the repository is the token's, and the run
// is another, the one workflow_run followed.
func namedRun(stated attestation, id *oidc.Identity) error {
	if strings.ToLower(stated.Repository) != id.Repository {
		return refuse(http.StatusForbidden, codeJoinKey, "The attestation's repository disagrees with the CI token.")
	}
	if stated.RunID == id.RunID {
		return refuse(http.StatusForbidden, codeJoinKey, "The attestation names the run that sends it. A key is attested for the run that workflow_run followed.")
	}
	return nil
}

// handleCredentials stores a minted-credential attestation from a run of a
// protected branch, for the run its key was minted for. A sender cannot
// attest from a run a pull request can edit.
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
	switch {
	case errors.Is(err, store.ErrAttestingRunUsed):
		s.fail(w, r, refuse(http.StatusConflict, codeAttestingRunUsed,
			fmt.Sprintf("Run %s, attempt %s, has attested a key already.", minted.AttestingRunID, minted.AttestingRunAttempt)))
	case errors.Is(err, store.ErrAttestedElsewhere):
		s.fail(w, r, refuse(http.StatusConflict, codeAttestedElsewhere, "The key is attested already, and the first attestation stands."))
	case err != nil:
		s.fail(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) readAttestation(w http.ResponseWriter, r *http.Request, who caller) (store.MintedCredential, error) {
	id := who.identity
	if err := s.attestingRun(id); err != nil {
		return store.MintedCredential{}, err
	}
	body, err := readBody(w, r)
	if err != nil {
		return store.MintedCredential{}, err
	}
	var stated attestation
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stated); err != nil || !gatewayKeyID.MatchString(stated.KeyID) || !runTag.MatchString(stated.RunID) ||
		!runAttempt.MatchString(stated.RunAttempt) || !vcs.IsCommit(stated.HeadSHA) {
		return store.MintedCredential{}, refuse(http.StatusBadRequest, codeMalformed,
			"The body is not an attestation with a 64-character key_id, a run_id, a run_attempt and a head_sha.")
	}
	if err := namedRun(stated, id); err != nil {
		return store.MintedCredential{}, err
	}
	return store.MintedCredential{
		KeySHA256: stated.KeyID, Repository: id.Repository, RunID: stated.RunID, RunAttempt: stated.RunAttempt, HeadSHA: stated.HeadSHA,
		AttestingRunID: id.RunID, AttestingRunAttempt: id.RunAttempt, AttestingRef: id.Ref,
	}, nil
}

// provenCredentials returns the attestations of the credentials the batch
// states that were minted for the token's run, by key. A sender with no
// token, or a token naming no pull request, proves none.
func (s *Server) provenCredentials(ctx context.Context, who caller, charges []charge) (map[string]store.MintedCredential, error) {
	proven := map[string]store.MintedCredential{}
	id := who.identity
	if id == nil || id.PullRequest == "" || id.RunAttempt == "" {
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
		if minted.SameRun(run) {
			proven[key] = minted
		}
	}
	return proven, nil
}
