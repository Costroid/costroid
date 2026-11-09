package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Deliveries keeps the webhook delivery ids the service has taken, so every
// instance refuses a replayed delivery, and a restart forgets none. The ids
// belong to the deployment, so no tenant is set.
type Deliveries struct {
	store     *Store
	retention time.Duration
}

// Deliveries returns the delivery ids held for retention after each is taken.
func (s *Store) Deliveries(retention time.Duration) Deliveries {
	return Deliveries{store: s, retention: retention}
}

// Claim reports true the first time an id is taken within the retention. It
// prunes the ids older than the retention first, in the same transaction.
func (d Deliveries) Claim(ctx context.Context, delivery string, now time.Time) (bool, error) {
	var first bool
	err := pgx.BeginFunc(ctx, d.store.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM app_deliveries WHERE claimed_at < $1`, now.Add(-d.retention)); err != nil {
			return err
		}
		taken, err := tx.Exec(ctx, `
			INSERT INTO app_deliveries (delivery, claimed_at) VALUES ($1, $2)
			ON CONFLICT (delivery) DO NOTHING`, delivery, now)
		first = taken.RowsAffected() == 1
		return err
	})
	if err != nil {
		return false, fmt.Errorf("store: claim delivery: %w", err)
	}
	return first, nil
}

// Release forgets an id, so a redelivery of it is processed.
func (d Deliveries) Release(ctx context.Context, delivery string) error {
	if _, err := d.store.pool.Exec(ctx, `DELETE FROM app_deliveries WHERE delivery = $1`, delivery); err != nil {
		return fmt.Errorf("store: release delivery: %w", err)
	}
	return nil
}
