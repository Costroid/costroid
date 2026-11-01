package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Costroid/costroid/internal/egress"
)

const addEgress = `
INSERT INTO egress_counters (day, host, outcome, count)
SELECT day::date, host, outcome, count
FROM unnest($1::text[], $2::text[], $3::text[], $4::bigint[]) AS t(day, host, outcome, count)
ON CONFLICT (day, host, outcome) DO UPDATE SET count = egress_counters.count + excluded.count`

// AddEgress adds to the deployment's egress counters, which belong to no
// tenant, so no tenant is set.
func (s *Store) AddEgress(ctx context.Context, counts []egress.Count) error {
	days, hosts, outcomes := make([]string, len(counts)), make([]string, len(counts)), make([]string, len(counts))
	values := make([]int64, len(counts))
	for i, count := range counts {
		days[i], hosts[i], outcomes[i], values[i] = count.Day, count.Host, count.Outcome, count.Count
	}
	if _, err := s.pool.Exec(ctx, addEgress, days, hosts, outcomes, values); err != nil {
		return fmt.Errorf("store: add egress counters: %w", err)
	}
	return nil
}

// Egress reads the counters, newest day first.
func (s *Store) Egress(ctx context.Context) ([]egress.Count, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT day::text, host, outcome, count FROM egress_counters
		ORDER BY day DESC, host, outcome`)
	if err != nil {
		return nil, fmt.Errorf("store: read egress counters: %w", err)
	}
	counts, err := pgx.CollectRows(rows, pgx.RowToStructByPos[egress.Count])
	if err != nil {
		return nil, fmt.Errorf("store: read egress counters: %w", err)
	}
	return counts, nil
}
