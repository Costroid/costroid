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

const (
	credentialMigration = "0006_credential_basis.sql"
	protectedMigration  = "0010_protected_attestation.sql"
)

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

// migrationsBefore is the schema as it stood before the migration named: a
// later migration can depend on it.
func migrationsBefore(t *testing.T, first string) fstest.MapFS {
	t.Helper()
	names, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{}
	for _, name := range names {
		if name >= first {
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
	if err := store.MigrateFrom(ctx, db.OwnerURL, migrationsBefore(t, credentialMigration)); err != nil {
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

// minted is an attestation of key for run's first attempt, from a protected
// run of the default branch numbered after it, so each run's key has an
// attesting run of its own.
func minted(key, run string) store.MintedCredential {
	return store.MintedCredential{
		KeySHA256: key, Repository: "github.com/acme/widgets", RunID: run, RunAttempt: "1",
		HeadSHA:        "1d4e7a90c3b25f8866a0e4d17c92b35fa8e6d071",
		AttestingRunID: "9" + run, AttestingRunAttempt: "1", AttestingRef: "refs/heads/main",
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

// TestAnAttestingRunAttestsOnce holds an attesting run and attempt to one
// attestation. Any other from it is ErrAttestingRunUsed, and that refusal
// comes first where the key's own applies too. The database holds both rules
// and the run an attestation must name, and lets the service's role change
// no row.
func TestAnAttestingRunAttestsOnce(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	ctx := context.Background()
	a, _ := tenants(t, db)
	first := minted(rowID(11), "1")
	if err := s.Attest(ctx, a, first); err != nil {
		t.Fatal(err)
	}
	change := func(edit func(*store.MintedCredential)) store.MintedCredential {
		changed := first
		edit(&changed)
		return changed
	}
	for name, tc := range map[string]struct {
		minted store.MintedCredential
		want   error
	}{
		"another key":                       {change(func(m *store.MintedCredential) { m.KeySHA256 = rowID(12) }), store.ErrAttestingRunUsed},
		"the same key with another head":    {change(func(m *store.MintedCredential) { m.HeadSHA = rowID(13)[:40] }), store.ErrAttestingRunUsed},
		"the same key for another attempt":  {change(func(m *store.MintedCredential) { m.RunAttempt = "2" }), store.ErrAttestingRunUsed},
		"the same key from a later attempt": {change(func(m *store.MintedCredential) { m.AttestingRunAttempt = "2" }), store.ErrAttestedElsewhere},
	} {
		if err := s.Attest(ctx, a, tc.minted); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}

	conn := connect(t, db.AppURL)
	for name, statement := range map[string]struct{ sql, state string }{
		"a second attestation from the run": {`INSERT INTO credential_attestations (tenant_id, key_sha256, repo, workflow_run_id, run_attempt, head_sha, attesting_run_id, attesting_run_attempt, attesting_ref)
			VALUES (costroid_tenant(), '` + rowID(14) + `', 'github.com/acme/widgets', '3', '1', '` + rowID(15)[:40] + `', '91', '1', 'refs/heads/main')`, uniqueViolation},
		"a run that attests itself": {`INSERT INTO credential_attestations (tenant_id, key_sha256, repo, workflow_run_id, run_attempt, head_sha, attesting_run_id, attesting_run_attempt, attesting_ref)
			VALUES (costroid_tenant(), '` + rowID(16) + `', 'github.com/acme/widgets', '95', '1', '` + rowID(15)[:40] + `', '95', '1', 'refs/heads/main')`, checkViolation},
		"a tag's run": {`INSERT INTO credential_attestations (tenant_id, key_sha256, repo, workflow_run_id, run_attempt, head_sha, attesting_run_id, attesting_run_attempt, attesting_ref)
			VALUES (costroid_tenant(), '` + rowID(17) + `', 'github.com/acme/widgets', '3', '1', '` + rowID(15)[:40] + `', '96', '1', 'refs/tags/v1')`, checkViolation},
		"an update": {`UPDATE credential_attestations SET workflow_run_id = '4'`, insufficientPrivilege},
		"a delete":  {`DELETE FROM credential_attestations`, insufficientPrivilege},
	} {
		err := inTenant(t, conn, a, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, statement.sql)
			return err
		})
		if err == nil {
			t.Errorf("%s was taken", name)
			continue
		}
		requireSQLState(t, err, statement.state)
	}
}

// TestAttestationsAnEnvironmentSentReachNoReading stores a key, its exact row
// and its count as the service did when a job in the gateway's environment
// attested them, then migrates. The service's role can no longer read either
// table, and no attestation or count of them reaches a reading: the count
// lets no rule pass. The row keeps the grade it was stored with.
func TestAttestationsAnEnvironmentSentReachNoReading(t *testing.T) {
	db := storetest.Empty(t)
	ctx := context.Background()
	if err := store.MigrateFrom(ctx, db.OwnerURL, migrationsBefore(t, protectedMigration)); err != nil {
		t.Fatal(err)
	}
	a := db.Tenant(t, "acme", []store.Binding{{Host: "github.com", Owner: "acme"}})
	key := rowID(21)
	owner := connect(t, db.OwnerURL)
	if _, err := owner.Exec(ctx, `SELECT set_config('costroid.tenant_id', $1, false)`, a.String()); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO minted_credentials (tenant_id, key_sha256, repo, workflow_run_id, run_attempt, environment)
			VALUES (costroid_tenant(), '` + key + `', 'github.com/acme/widgets', '1', '1', 'costroid-gateway')`,
		`INSERT INTO gateway_spend (tenant_id, key_sha256, usd) VALUES (costroid_tenant(), '` + key + `', 0.01)`,
	} {
		if _, err := owner.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Migrate(ctx, db.OwnerURL); err != nil {
		t.Fatal(err)
	}
	s := open(t, db)
	if _, err := s.Ingest(ctx, a, batch(runRow(rowID(22), "0.01", key, "1", "1"))); err != nil {
		t.Fatal(err)
	}
	if rows, usd := gatewaySpend(t, s, a); rows != 0 || usd != "0" {
		t.Fatalf("an environment's count reached the reading: %d rows, USD %s", rows, usd)
	}
	if found, err := s.MintedCredentials(ctx, a, []string{key}); err != nil || len(found) != 0 {
		t.Fatalf("an environment's attestation was read: %+v, %v", found, err)
	}
	conn := connect(t, db.AppURL)
	for _, table := range []string{"minted_credentials", "gateway_spend"} {
		err := inTenant(t, conn, a, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `SELECT count(*) FROM `+table)
			return err
		})
		requireSQLState(t, err, insufficientPrivilege)
	}
}
