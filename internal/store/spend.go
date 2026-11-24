package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Costroid/costroid/internal/decimal"
)

var (
	// ErrNotMinted means a spend names a key no attestation holds for the run
	// it names.
	ErrNotMinted = errors.New("store: the key is not attested as minted for the run")
	// ErrSpendHeld means the key holds a spend another write sent.
	ErrSpendHeld = errors.New("store: the key holds a spend already")
)

// AttestedSpend is what the gateway counted on a key, in USD, as the decimal
// string a run of a protected branch sent once the run the key was minted for
// had ended. Key names that run; the Attesting fields name the run whose
// token sent the figure.
type AttestedSpend struct {
	Key   MintedCredential
	Spend string

	AttestingRunID      string
	AttestingRunAttempt string
}

const (
	insertSpend = `
INSERT INTO credential_spend (tenant_id, key_sha256, spend, attesting_repo, attesting_run_id, attesting_run_attempt)
VALUES (costroid_tenant(), $1, $2, $3, $4, $5)
ON CONFLICT DO NOTHING`
	selectSpentBy = `
SELECT key_sha256, spend FROM credential_spend
WHERE attesting_repo = $1 AND attesting_run_id = $2 AND attesting_run_attempt = $3`

	// pullRequestSpend reads a pull request's gateway figure. counted holds
	// the keys its budget-eligible rows name, each attested as minted for the
	// row's own repository, run and attempt, with a spend a protected run
	// attested: one entry per row. named holds every key an attesting run
	// named the pull request for, whether a row of it arrived or not. The
	// figure adds up the spend of the keys of both, each key once. The rows it
	// stands behind are counted's, and none while a key in named holds no
	// spend: a figure that leaves out a run's count stands behind no row.
	pullRequestSpend = `
WITH counted AS (
	SELECT m.key_sha256
	FROM focus_rows f
	JOIN credential_attestations m ON m.key_sha256 = f.document->>'SubAccountId'
		AND m.repo = f.repo AND m.workflow_run_id = f.workflow_run_id AND m.run_attempt = f.run_attempt
	JOIN credential_spend g ON g.key_sha256 = m.key_sha256
	WHERE f.repo = $1 AND f.pr = $2 AND f.budget_eligible AND f.credential_basis = 'minted'
), named AS (
	SELECT m.key_sha256
	FROM credential_pulls p
	JOIN credential_attestations m ON m.key_sha256 = p.key_sha256
	WHERE m.repo = $1 AND p.pr = $2
), uncounted AS (
	SELECT FROM named n WHERE NOT EXISTS (SELECT FROM credential_spend g WHERE g.key_sha256 = n.key_sha256)
)
SELECT (SELECT count(*) FROM counted WHERE NOT EXISTS (SELECT FROM uncounted)),
	(SELECT coalesce(sum(spend::numeric), 0) FROM credential_spend
		WHERE key_sha256 IN (SELECT key_sha256 FROM counted UNION SELECT key_sha256 FROM named))::text`
)

// AttestSpend keeps what the gateway counted on a minted key. The key must be
// attested as minted for the run named, or the write answers ErrNotMinted. A
// key holds one figure and an attesting run and attempt writes one: the same
// figure again from the same attesting run, the same string, changes nothing,
// any other from that run answers ErrAttestingRunUsed, and any other for a key
// that holds a figure ErrSpendHeld. A figure is never replaced, so the larger
// of two is never kept.
func (s *Store) AttestSpend(ctx context.Context, tenant TenantID, attested AttestedSpend) error {
	return s.inTenant(ctx, tenant, func(tx pgx.Tx) error {
		stored, err := readMinted(ctx, tx, []string{attested.Key.KeySHA256})
		if err != nil {
			return err
		}
		if minted, found := stored[attested.Key.KeySHA256]; !found || !minted.SameRun(attested.Key) {
			return ErrNotMinted
		}
		tag, err := tx.Exec(ctx, insertSpend, attested.Key.KeySHA256, attested.Spend, attested.Key.Repository,
			attested.AttestingRunID, attested.AttestingRunAttempt)
		if err != nil {
			return fmt.Errorf("store: keep the gateway's spend: %w", err)
		}
		if tag.RowsAffected() == 1 {
			return nil
		}
		var key, spend string
		err = tx.QueryRow(ctx, selectSpentBy, attested.Key.Repository, attested.AttestingRunID, attested.AttestingRunAttempt).Scan(&key, &spend)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return ErrSpendHeld
		case err != nil:
			return fmt.Errorf("store: read the gateway's spend: %w", err)
		case key != attested.Key.KeySHA256 || spend != attested.Spend:
			return ErrAttestingRunUsed
		}
		return nil
	})
}

// GatewaySpend reads the pull request's measured spend. usd adds up the spend
// a protected run attested for every key the pull request's budget-eligible
// rows name, minted for the row's own run, with credential_basis minted, and
// for every key an attesting run named the pull request for, whether a row of
// it arrived or not: each key once. rows counts the eligible rows on the first
// kind of key, and is 0 while a key of the second kind holds no spend, so a
// figure that leaves out a run's count stands behind no row.
func (s *Store) GatewaySpend(ctx context.Context, tenant TenantID, repo, pr string) (rows int64, usd decimal.Decimal, err error) {
	err = s.inTenant(ctx, tenant, func(tx pgx.Tx) error {
		var total string
		if err := tx.QueryRow(ctx, pullRequestSpend, repo, pr).Scan(&rows, &total); err != nil {
			return fmt.Errorf("store: read the gateway's spend: %w", err)
		}
		parsed, err := decimal.Parse(total)
		if err != nil {
			return fmt.Errorf("store: read the gateway's spend: %w", err)
		}
		usd = parsed
		return nil
	})
	return rows, usd, err
}
