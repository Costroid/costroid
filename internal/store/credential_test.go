package store_test

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"

	"github.com/Costroid/costroid/internal/store"
	"github.com/Costroid/costroid/internal/store/storetest"
	"github.com/Costroid/costroid/migrations"
)

const credentialMigration = "0006_credential_basis.sql"

// insertExact writes an exact row naming no credential basis, as the service
// did before the rule.
const insertExact = `
INSERT INTO focus_rows (
	tenant_id, row_id, billing_currency, service_category, sku_meter, consumed_unit, consumed_quantity,
	list_cost, effective_cost, charge_period_start, charge_period_end, cost_basis, cost_source,
	attribution_grade, enforcement_grade, run_identity, document)
VALUES (costroid_tenant(), $1, 'USD', 'AI and Machine Learning', 'output', 'Tokens', 100, 1, 1,
	'2026-11-01T10:00:00Z', '2026-11-01T10:00:01Z', 'measured', 'agent_stream', 'exact', 'none',
	'token_verified', '{}')`

func migrationsWithout(t *testing.T, left string) fstest.MapFS {
	t.Helper()
	names, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{}
	for _, name := range names {
		if name == left {
			continue
		}
		data, err := fs.ReadFile(migrations.Files, name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = &fstest.MapFile{Data: data}
	}
	return files
}

func TestAnExactRowStoredBeforeTheMintedRuleKeepsItsGradeAndANewOneCannot(t *testing.T) {
	db := storetest.Empty(t)
	ctx := context.Background()
	if err := store.MigrateFrom(ctx, db.OwnerURL, migrationsWithout(t, credentialMigration)); err != nil {
		t.Fatal(err)
	}
	tenant := db.Tenant(t, "acme", []store.Binding{{Host: "github.com", Owner: "acme"}})
	conn, err := pgx.Connect(ctx, db.OwnerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, `SELECT set_config('costroid.tenant_id', $1, false)`, tenant.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, insertExact, rowID(1)); err != nil {
		t.Fatal(err)
	}

	if err := store.Migrate(ctx, db.OwnerURL); err != nil {
		t.Fatalf("the migration refused a row stored before it: %v", err)
	}
	var grade string
	if err := conn.QueryRow(ctx, `SELECT attribution_grade FROM focus_rows WHERE row_id = $1`, rowID(1)).Scan(&grade); err != nil {
		t.Fatal(err)
	}
	if grade != "exact" {
		t.Fatalf("a stored row was regraded to %q", grade)
	}
	_, err = conn.Exec(ctx, insertExact, rowID(2))
	requireSQLState(t, err, checkViolation)
}

func minted(key, run string) store.MintedCredential {
	return store.MintedCredential{
		KeySHA256: key, Repository: "github.com/acme/widgets", RunID: run, RunAttempt: "1", Environment: "costroid-gateway",
	}
}

func TestAnAttestationIsStoredOnceAndNeverMovedToAnotherRun(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	ctx := context.Background()
	a, b := tenants(t, db)
	key := rowID(1)
	if err := s.Attest(ctx, a, minted(key, "1")); err != nil {
		t.Fatal(err)
	}
	if err := s.Attest(ctx, a, minted(key, "1")); err != nil {
		t.Fatalf("the same attestation again: %v", err)
	}
	if err := s.Attest(ctx, a, minted(key, "2")); !errors.Is(err, store.ErrAttestedElsewhere) {
		t.Fatalf("another run for a stored key: %v", err)
	}
	found, err := s.MintedCredentials(ctx, a, []string{key, rowID(2)})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[key] != minted(key, "1") {
		t.Fatalf("%+v", found)
	}
	other, err := s.MintedCredentials(ctx, b, []string{key})
	if err != nil || len(other) != 0 {
		t.Fatalf("another tenant read the attestation: %+v, %v", other, err)
	}
	if err := s.Attest(ctx, b, minted(key, "2")); err != nil {
		t.Fatalf("another tenant's attestation of the same id: %v", err)
	}
}
