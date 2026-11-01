package store

import "context"

// SessionTenant reads the tenant setting on a pooled connection, outside any
// transaction. A leak from SET LOCAL's scope would show here.
func (s *Store) SessionTenant(ctx context.Context) (string, error) {
	var value string
	err := s.pool.QueryRow(ctx, `SELECT coalesce(current_setting('costroid.tenant_id', true), '')`).Scan(&value)
	return value, err
}
