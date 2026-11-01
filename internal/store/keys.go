package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// KeyPrefix starts every ingest key. A bearer without it is a CI token.
const KeyPrefix = "costroid_ik_"

const keySecretBytes = 32

// NewKey mints an ingest key. Only its digest is ever stored, so a key that is
// lost is revoked and replaced.
func NewKey() (string, error) {
	secret := make([]byte, keySecretBytes)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("store: mint ingest key: %w", err)
	}
	return KeyPrefix + base64.RawURLEncoding.EncodeToString(secret), nil
}

// KeyDigest is the SHA-256 of a key, which is also its public id.
func KeyDigest(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// AddKey stores a key's digest for the tenant holding an SCM owner, through
// the owner role's connection. An owner no tenant holds answers ErrNotFound.
func AddKey(ctx context.Context, ownerURL string, owner Binding, digest string) error {
	return asOwner(ctx, ownerURL, func(tx pgx.Tx) error {
		tenant, err := resolveTenant(ctx, tx, ownerKey(owner.Host, owner.Owner), ownerLookup)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, setTenant(tenant)); err != nil {
			return fmt.Errorf("store: set tenant: %w", err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO ingest_keys (key_sha256, tenant_id) VALUES ($1, $2)`, digest, tenant.text)
		if err != nil {
			return fmt.Errorf("store: add ingest key: %w", err)
		}
		return nil
	})
}

// RevokeKey stops a key at the next request, through the owner role's
// connection. A key already revoked keeps its first stamp, and a digest no
// tenant holds answers ErrNotFound.
func RevokeKey(ctx context.Context, ownerURL, digest string) error {
	return asOwner(ctx, ownerURL, func(tx pgx.Tx) error {
		tenant, err := resolveTenant(ctx, tx, digest, `SELECT tenant_id::text FROM ingest_keys WHERE key_sha256 = $1`)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, setTenant(tenant)); err != nil {
			return fmt.Errorf("store: set tenant: %w", err)
		}
		_, err = tx.Exec(ctx, `UPDATE ingest_keys SET revoked_at = now() WHERE key_sha256 = $1 AND revoked_at IS NULL`, digest)
		if err != nil {
			return fmt.Errorf("store: revoke ingest key: %w", err)
		}
		return nil
	})
}

func asOwner(ctx context.Context, ownerURL string, fn func(pgx.Tx) error) error {
	conn, err := pgx.Connect(ctx, ownerURL)
	if err != nil {
		return fmt.Errorf("store: connect as the owner: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	return pgx.BeginFunc(ctx, conn, fn)
}
