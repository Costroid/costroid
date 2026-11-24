package store

import "context"

// CountOverrides is the override query, for the benchmark to show its plan.
const CountOverrides = countOverrides

// SessionTenant reads the tenant setting on a pooled connection, outside any
// transaction. A leak from SET LOCAL's scope would show here.
func (s *Store) SessionTenant(ctx context.Context) (string, error) {
	var value string
	err := s.pool.QueryRow(ctx, `SELECT coalesce(current_setting('costroid.tenant_id', true), '')`).Scan(&value)
	return value, err
}

// SetBeforeInsert has fn run in each batch's transaction after the stored
// statements are read and before the rows are inserted.
func (s *Store) SetBeforeInsert(fn func()) {
	s.beforeInsert = fn
}
