package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrAttestedElsewhere means a key is already attested for another run.
var ErrAttestedElsewhere = errors.New("store: the key is attested for another run")

// MintedCredential is a gateway key the minting job attested for one run.
type MintedCredential struct {
	KeySHA256   string
	Repository  string
	RunID       string
	RunAttempt  string
	Environment string
}

// SameRun reports whether two attestations name one run.
func (m MintedCredential) SameRun(other MintedCredential) bool {
	return m.Repository == other.Repository && m.RunID == other.RunID && m.RunAttempt == other.RunAttempt
}

const (
	insertMinted = `
INSERT INTO minted_credentials (tenant_id, key_sha256, repo, workflow_run_id, run_attempt, environment)
VALUES (costroid_tenant(), $1, $2, $3, $4, $5)
ON CONFLICT (tenant_id, key_sha256) DO NOTHING`
	selectMinted = `
SELECT key_sha256, repo, workflow_run_id, run_attempt, environment
FROM minted_credentials WHERE key_sha256 = ANY($1::text[])`
)

// Attest stores an attestation. The same attestation again changes nothing,
// and one that names another run for a stored key answers ErrAttestedElsewhere.
func (s *Store) Attest(ctx context.Context, tenant TenantID, minted MintedCredential) error {
	return s.inTenant(ctx, tenant, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, insertMinted, minted.KeySHA256, minted.Repository, minted.RunID, minted.RunAttempt, minted.Environment)
		if err != nil {
			return fmt.Errorf("store: attest a credential: %w", err)
		}
		if tag.RowsAffected() == 1 {
			return nil
		}
		stored, err := readMinted(ctx, tx, []string{minted.KeySHA256})
		if err != nil {
			return err
		}
		if !stored[minted.KeySHA256].SameRun(minted) {
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
	rows, err := tx.Query(ctx, selectMinted, keys)
	if err != nil {
		return nil, fmt.Errorf("store: read minted credentials: %w", err)
	}
	found := map[string]MintedCredential{}
	for rows.Next() {
		var m MintedCredential
		if err := rows.Scan(&m.KeySHA256, &m.Repository, &m.RunID, &m.RunAttempt, &m.Environment); err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: read minted credentials: %w", err)
		}
		found[m.KeySHA256] = m
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read minted credentials: %w", err)
	}
	return found, nil
}
