package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Costroid/costroid/internal/decimal"
)

// ErrNotMinted means a spend names a key no attestation holds.
var ErrNotMinted = errors.New("store: the key is not attested as minted")

const (
	// keepSpend keeps the larger of a key's stored figure and the new one.
	keepSpend = `
INSERT INTO gateway_spend (tenant_id, key_sha256, usd)
VALUES (costroid_tenant(), $1, $2::numeric)
ON CONFLICT (tenant_id, key_sha256) DO UPDATE SET usd = excluded.usd, attested_at = now()
WHERE excluded.usd > gateway_spend.usd`

	// pullRequestSpend counts a pull request's budget-eligible rows whose
	// SubAccountId is a key attested as minted for the row's own repository,
	// run and attempt, and whose spend a closing job attested. It adds up the
	// spend of those keys, each key once.
	pullRequestSpend = `
WITH counted AS (
	SELECT m.key_sha256
	FROM focus_rows f
	JOIN minted_credentials m ON m.key_sha256 = f.document->>'SubAccountId'
		AND m.repo = f.repo AND m.workflow_run_id = f.workflow_run_id AND m.run_attempt = f.run_attempt
	JOIN gateway_spend g ON g.key_sha256 = m.key_sha256
	WHERE f.repo = $1 AND f.pr = $2 AND f.budget_eligible AND f.credential_basis = 'minted'
)
SELECT (SELECT count(*) FROM counted),
	(SELECT coalesce(sum(usd), 0) FROM gateway_spend WHERE key_sha256 IN (SELECT key_sha256 FROM counted))::text`
)

// AttestSpend keeps what the gateway counted on a minted key, in USD. The key
// must be attested as minted for the run named: a key attested for another
// repository, run or attempt answers ErrAttestedElsewhere, and a key never
// attested answers ErrNotMinted. A key's spend is the largest figure attested
// for it, because a gateway's count only grows, so a smaller one changes
// nothing.
func (s *Store) AttestSpend(ctx context.Context, tenant TenantID, key MintedCredential, usd decimal.Decimal) error {
	if usd.Sign() < 0 {
		return errors.New("store: a gateway's spend is never negative")
	}
	return s.inTenant(ctx, tenant, func(tx pgx.Tx) error {
		stored, err := readMinted(ctx, tx, []string{key.KeySHA256})
		if err != nil {
			return err
		}
		minted, found := stored[key.KeySHA256]
		switch {
		case !found:
			return ErrNotMinted
		case !minted.SameRun(key):
			return ErrAttestedElsewhere
		}
		if _, err := tx.Exec(ctx, keepSpend, key.KeySHA256, usd.String()); err != nil {
			return fmt.Errorf("store: keep the gateway's spend: %w", err)
		}
		return nil
	})
}

// GatewaySpend reads the pull request's measured spend. rows counts its
// budget-eligible rows whose credential is a key minted for the row's own run,
// with credential_basis minted, and whose spend a closing job attested. usd
// adds up the attested spend of those keys, each key once.
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
