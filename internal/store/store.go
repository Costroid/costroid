// Package store is the ledger: FOCUS rows in Postgres, one tenant at a time.
//
// Every query runs inside a transaction that names its tenant with SET LOCAL,
// and row level security does the filtering. No statement here carries a
// tenant predicate of its own.
package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound means a lookup named nothing.
var ErrNotFound = errors.New("store: not found")

// ErrUnsafeRole means the service was given a role that could read past row
// level security.
var ErrUnsafeRole = errors.New("store: unsafe application role")

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

const ownerLookup = `SELECT tenant_id::text FROM tenant_bindings WHERE owner_key = $1`

// TenantID is a validated tenant identifier. It is safe to place in SET LOCAL,
// which takes no bind parameters.
type TenantID struct {
	text string
}

// ParseTenantID accepts the canonical lower-case UUID form only.
func ParseTenantID(text string) (TenantID, error) {
	if !uuidPattern.MatchString(text) {
		return TenantID{}, fmt.Errorf("store: tenant id %q is not a canonical uuid", text)
	}
	return TenantID{text: text}, nil
}

// String returns the canonical form.
func (t TenantID) String() string {
	return t.text
}

// Store is the application role's handle on the ledger.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects as the application role and refuses a role that could read
// past row level security.
func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("store: open pool: %w", err)
	}
	s := &Store{pool: pool}
	if err := s.checkRole(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the pool.
func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) checkRole(ctx context.Context) error {
	var super, bypass bool
	var owned int
	err := s.pool.QueryRow(ctx, `
		SELECT r.rolsuper, r.rolbypassrls,
		       (SELECT count(*) FROM pg_class c
		        JOIN pg_namespace n ON n.oid = c.relnamespace
		        WHERE c.relowner = r.oid AND n.nspname = 'public')
		FROM pg_roles r WHERE r.rolname = current_user`).Scan(&super, &bypass, &owned)
	if err != nil {
		return fmt.Errorf("store: read the application role: %w", err)
	}
	if super || bypass || owned > 0 {
		return fmt.Errorf("%w: superuser %t, bypassrls %t, owns %d relations", ErrUnsafeRole, super, bypass, owned)
	}
	return nil
}

// inTenant runs fn in a transaction scoped to one tenant.
func (s *Store) inTenant(ctx context.Context, tenant TenantID, fn func(pgx.Tx) error) error {
	if tenant.text == "" {
		return errors.New("store: no tenant")
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, setTenant(tenant)); err != nil {
			return fmt.Errorf("store: set tenant: %w", err)
		}
		return fn(tx)
	})
}

func setTenant(tenant TenantID) string {
	return fmt.Sprintf(`SET LOCAL costroid.tenant_id = '%s'`, tenant.text)
}

// lookup reads a tenant by one exact key, before any tenant is set.
func (s *Store) lookup(ctx context.Context, key, query string) (TenantID, error) {
	var tenant TenantID
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		tenant, err = resolveTenant(ctx, tx, key, query)
		return err
	})
	return tenant, err
}

func resolveTenant(ctx context.Context, tx pgx.Tx, key, query string) (TenantID, error) {
	if _, err := tx.Exec(ctx, `SELECT set_config('costroid.lookup', $1, true)`, key); err != nil {
		return TenantID{}, fmt.Errorf("store: set lookup: %w", err)
	}
	var text string
	err := tx.QueryRow(ctx, query, key).Scan(&text)
	if errors.Is(err, pgx.ErrNoRows) {
		return TenantID{}, ErrNotFound
	}
	if err != nil {
		return TenantID{}, fmt.Errorf("store: resolve tenant: %w", err)
	}
	return ParseTenantID(text)
}

// TenantForOwner resolves an SCM owner, host and name, to its tenant.
func (s *Store) TenantForOwner(ctx context.Context, host, owner string) (TenantID, error) {
	return s.lookup(ctx, ownerKey(host, owner), ownerLookup)
}

// TenantForKey resolves the SHA-256 of an ingest key to its tenant.
func (s *Store) TenantForKey(ctx context.Context, keySHA256 string) (TenantID, error) {
	return s.lookup(ctx, keySHA256, `SELECT tenant_id::text FROM ingest_keys WHERE key_sha256 = $1 AND revoked_at IS NULL`)
}

func ownerKey(host, owner string) string {
	return strings.ToLower(host) + ":" + strings.ToLower(owner)
}
