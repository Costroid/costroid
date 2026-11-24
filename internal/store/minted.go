package store

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

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
// commit GitHub's event named for that run, and PullRequests the pull requests
// the event lists for it. The Attesting fields name the run whose token sent
// the attestation.
type MintedCredential struct {
	KeySHA256    string
	Repository   string
	RunID        string
	RunAttempt   string
	HeadSHA      string
	PullRequests PullRequests

	AttestingRunID      string
	AttestingRunAttempt string
	AttestingRef        string
}

// PullRequests is a set of pull request numbers in one form, so that two
// statements of the same set compare equal: ascending, each once, separated by
// a space. Empty names none.
type PullRequests string

// PullRequestsOf is the set of the numbers given, in any order.
func PullRequestsOf(numbers []string) PullRequests {
	sorted := slices.Clone(numbers)
	slices.SortFunc(sorted, func(a, b string) int { return cmp.Or(cmp.Compare(len(a), len(b)), cmp.Compare(a, b)) })
	return PullRequests(strings.Join(slices.Compact(sorted), " "))
}

// Numbers lists the set, none when it is empty.
func (p PullRequests) Numbers() []string {
	return strings.Fields(string(p))
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
	insertPulls = `
INSERT INTO credential_pulls (tenant_id, key_sha256, pr)
SELECT costroid_tenant(), $1, unnest($2::text[])`
	mintedColumns = `key_sha256, repo, workflow_run_id, run_attempt, head_sha,
	ARRAY(SELECT p.pr FROM credential_pulls p WHERE p.key_sha256 = credential_attestations.key_sha256),
	attesting_run_id, attesting_run_attempt, attesting_ref`
	selectMinted = `SELECT ` + mintedColumns + ` FROM credential_attestations WHERE key_sha256 = ANY($1::text[])`
	selectSpoken = `SELECT ` + mintedColumns + ` FROM credential_attestations
WHERE repo = $1 AND attesting_run_id = $2 AND attesting_run_attempt = $3`
)

// Attest stores an attestation, and the pull requests it names with it in the
// same transaction. The same attestation again, every field and the same pull
// requests, changes nothing. Any other from the same attesting run and attempt
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
			return keepPulls(ctx, tx, minted)
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

// keepPulls writes the pull requests a new attestation names.
func keepPulls(ctx context.Context, tx pgx.Tx, minted MintedCredential) error {
	numbers := minted.PullRequests.Numbers()
	if len(numbers) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, insertPulls, minted.KeySHA256, numbers); err != nil {
		return fmt.Errorf("store: keep an attestation's pull requests: %w", err)
	}
	return nil
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
		var pulls []string
		if err := rows.Scan(&m.KeySHA256, &m.Repository, &m.RunID, &m.RunAttempt, &m.HeadSHA, &pulls,
			&m.AttestingRunID, &m.AttestingRunAttempt, &m.AttestingRef); err != nil {
			return nil, fmt.Errorf("store: read minted credentials: %w", err)
		}
		m.PullRequests = PullRequestsOf(pulls)
		found = append(found, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read minted credentials: %w", err)
	}
	return found, nil
}
