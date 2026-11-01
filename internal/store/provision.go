package store

import (
	"context"
	"crypto/rand"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Binding names one SCM owner a tenant holds.
type Binding struct {
	Host  string
	Owner string
}

// Tenant is one provisioned tenant.
type Tenant struct {
	ID   TenantID
	Name string
	// Bindings are the SCM owners whose CI tokens resolve to this tenant.
	Bindings []Binding
	// KeySHA256 lists the SHA-256 of each ingest key.
	KeySHA256 []string
}

// Provision writes a tenant through the owner's connection, which the service
// holds only to migrate and to bind. A binding another tenant holds fails it.
func Provision(ctx context.Context, ownerURL string, tenant Tenant) error {
	conn, err := pgx.Connect(ctx, ownerURL)
	if err != nil {
		return fmt.Errorf("store: connect to provision: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, setTenant(tenant.ID)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, $2)`, tenant.ID.text, tenant.Name); err != nil {
			return err
		}
		for _, binding := range tenant.Bindings {
			if _, err := tx.Exec(ctx, `INSERT INTO tenant_bindings (owner_key, tenant_id) VALUES ($1, $2)`, ownerKey(binding.Host, binding.Owner), tenant.ID.text); err != nil {
				return err
			}
		}
		for _, sum := range tenant.KeySHA256 {
			if _, err := tx.Exec(ctx, `INSERT INTO ingest_keys (key_sha256, tenant_id) VALUES ($1, $2)`, sum, tenant.ID.text); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("store: provision %s: %w", tenant.Name, err)
	}
	return nil
}

// NewTenantID mints a random version 4 UUID.
func NewTenantID() (TenantID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return TenantID{}, fmt.Errorf("store: mint tenant id: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return ParseTenantID(fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]))
}
