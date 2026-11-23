package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var (
	// ErrAttestedElsewhere means a key is already attested, for another run or
	// by another attesting run. The first attestation stands.
	ErrAttestedElsewhere = errors.New("store: the key is attested already")
	// ErrAttestingRunUsed means the attesting run and attempt already
	// attested another key, or the same key otherwise.
	ErrAttestingRunUsed = errors.New("store: the attesting run has attested already")
)

// MintedCredential is a gateway key that a run of a protected branch attested
// as minted for another run: the run its rows come from. HeadSHA is the head
// commit GitHub's event named for that run, and the Attesting fields name the
// run whose token sent the attestation.
type MintedCredential struct {
	KeySHA256  string
	Repository string
	RunID      string
	RunAttempt string
	HeadSHA    string

	AttestingRunID      string
	AttestingRunAttempt string
	AttestingRef        string
}

// SameRun reports whether two attestations name one run.
func (m MintedCredential) SameRun(other MintedCredential) bool {
	return m.Repository == other.Repository && m.RunID == other.RunID && m.RunAttempt == other.RunAttempt
}

const (
	insertMinted = `
INSERT INTO credential_attestations (tenant_id, key_sha256, repo, workflow_run_id, run_attempt, head_sha,
	attesting_run_id, attesting_run_attempt, attesting_ref)
VALUES (costroid_tenant(), $1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT DO NOTHING`
	mintedColumns = `key_sha256, repo, workflow_run_id, run_attempt, head_sha, attesting_run_id, attesting_run_attempt, attesting_ref`
	selectMinted  = `SELECT ` + mintedColumns + ` FROM credential_attestations WHERE key_sha256 = ANY($1::text[])`
	selectSpoken  = `SELECT ` + mintedColumns + ` FROM credential_attestations
WHERE repo = $1 AND attesting_run_id = $2 AND attesting_run_attempt = $3`
)

// Attest stores an attestation. The same attestation again, every field the
// same, changes nothing. Any other from the same attesting run and attempt
// answers ErrAttestingRunUsed, and any other for a stored key
// ErrAttestedElsewhere. Where both hold, the attesting run's comes first,
// because it is about who is speaking. A unique index holds each rule, so two
// attestations that race end the same way.
func (s *Store) Attest(ctx context.Context, tenant TenantID, minted MintedCredential) error {
	return s.inTenant(ctx, tenant, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, insertMinted, minted.KeySHA256, minted.Repository, minted.RunID, minted.RunAttempt, minted.HeadSHA,
			minted.AttestingRunID, minted.AttestingRunAttempt, minted.AttestingRef)
		if err != nil {
			return fmt.Errorf("store: attest a credential: %w", err)
		}
		if tag.RowsAffected() == 1 {
			return nil
		}
		spoken, err := scanMinted(tx.Query(ctx, selectSpoken, minted.Repository, minted.AttestingRunID, minted.AttestingRunAttempt))
		if err != nil {
			return err
		}
		for _, stored := range spoken {
			if stored != minted {
				return ErrAttestingRunUsed
			}
		}
		stored, err := readMinted(ctx, tx, []string{minted.KeySHA256})
		if err != nil {
			return err
		}
		if stored[minted.KeySHA256] != minted {
			return ErrAttestedElsewhere
		}
		return nil
	})
}

// MintedCredentials reads the attestations of the keys named, by key.
func (s *Store) MintedCredentials(ctx context.Context, tenant TenantID, keys []string) (map[string]MintedCredential, error) {
	var found map[string]MintedCredential
	err := s.inTenant(ctx, tenant, func(tx pgx.Tx) error {
		var err error
		found, err = readMinted(ctx, tx, keys)
		return err
	})
	return found, err
}

func readMinted(ctx context.Context, tx pgx.Tx, keys []string) (map[string]MintedCredential, error) {
	list, err := scanMinted(tx.Query(ctx, selectMinted, keys))
	if err != nil {
		return nil, err
	}
	found := map[string]MintedCredential{}
	for _, m := range list {
		found[m.KeySHA256] = m
	}
	return found, nil
}

// scanMinted reads the attestations a query answered.
func scanMinted(rows pgx.Rows, err error) ([]MintedCredential, error) {
	if err != nil {
		return nil, fmt.Errorf("store: read minted credentials: %w", err)
	}
	defer rows.Close()
	var found []MintedCredential
	for rows.Next() {
		var m MintedCredential
		if err := rows.Scan(&m.KeySHA256, &m.Repository, &m.RunID, &m.RunAttempt, &m.HeadSHA,
			&m.AttestingRunID, &m.AttestingRunAttempt, &m.AttestingRef); err != nil {
			return nil, fmt.Errorf("store: read minted credentials: %w", err)
		}
		found = append(found, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read minted credentials: %w", err)
	}
	return found, nil
}
