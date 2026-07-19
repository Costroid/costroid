// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"testing"

	"github.com/duckdb/duckdb-go/v2"
	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/focus"
)

// embeddedMigrationNames returns the sorted embedded migration file names
// — the versions an up-to-date store must have applied.
func embeddedMigrationNames(t *testing.T) []string {
	t.Helper()
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("listing embedded migrations: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

// TestMigration0002WidensDecimalsPreservingData proves the D25 widening
// on a REAL pre-0002 store: a database built by executing the embedded
// 0001 SQL directly (with its schema_migrations row recorded exactly as
// applyMigration would have) and holding known scale-12 values. Open must
// apply the pending migrations, preserve the stored values exactly, and
// afterwards ingest and read back an 18-fractional-digit value byte-exact
// through the production insert path — which is what catches a stale
// DECIMAL(38,12) cast in insertRecordSQL, since DuckDB's parameter-bound
// CAST silently rounds instead of erroring.
func TestMigration0002WidensDecimalsPreservingData(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// Build the pre-0002 store.
	db, err := sql.Open("duckdb", filepath.Join(dir, DatabaseFile))
	if err != nil {
		t.Fatalf("opening raw DuckDB: %v", err)
	}
	schema0001, err := migrationsFS.ReadFile("migrations/0001_create_cost_tables.sql")
	if err != nil {
		t.Fatalf("reading embedded 0001: %v", err)
	}
	for _, stmt := range []string{
		string(schema0001),
		`CREATE TABLE schema_migrations (
			version    VARCHAR PRIMARY KEY,
			applied_at TIMESTAMP NOT NULL
		)`,
		`INSERT INTO schema_migrations (version, applied_at) VALUES ('0001_create_cost_tables.sql', now())`,
		`INSERT INTO ingest_batches (connector, source_identity, content_hash, tenant_id, record_count, ingested_at)
		 VALUES ('aws-focus', 'seed.csv.gz', 'sha256:seed', 'default', 1, now())`,
		// A scale-12-era row with a full 12 fractional digits, inserted
		// the way the pre-0002 store did (explicit DECIMAL(38,12) cast).
		`INSERT INTO cost_records (
			x_tenant_id, batch_connector, batch_source_identity,
			billing_account_id, service_provider_name, invoice_issuer_name,
			billing_currency, billing_period_start, billing_period_end,
			charge_category, charge_period_start, charge_period_end,
			billed_cost, effective_cost, list_cost, contracted_cost,
			service_name, service_category
		) VALUES (
			'default', 'aws-focus', 'seed.csv.gz',
			'999999999999', 'AWS', 'Amazon Web Services, Inc.',
			'USD', TIMESTAMP '2026-05-01', TIMESTAMP '2026-06-01',
			'Usage', TIMESTAMP '2026-05-03', TIMESTAMP '2026-05-04',
			CAST('0.123456789012' AS DECIMAL(38,12)), CAST('0.123456789012' AS DECIMAL(38,12)),
			CAST('0.123456789012' AS DECIMAL(38,12)), CAST('0.123456789012' AS DECIMAL(38,12)),
			'SeedService', 'Compute'
		)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("building pre-0002 store: %v\nstatement: %s", err, stmt)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing pre-0002 store: %v", err)
	}

	// Open applies every pending migration (0002, ...).
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open on pre-0002 store: %v", err)
	}
	defer func() { _ = store.Close() }()
	if got, want := appliedMigrations(t, store), embeddedMigrationNames(t); !slices.Equal(got, want) {
		t.Fatalf("applied migrations = %v, want %v", got, want)
	}

	// The money columns now hold DECIMAL(38,18).
	var precision, scale int
	if err := store.db.QueryRowContext(ctx,
		`SELECT numeric_precision, numeric_scale FROM information_schema.columns
		 WHERE table_name = 'cost_records' AND column_name = 'billed_cost'`).Scan(&precision, &scale); err != nil {
		t.Fatalf("reading billed_cost column type: %v", err)
	}
	if precision != 38 || scale != MaxDecimalScale {
		t.Fatalf("billed_cost is DECIMAL(%d,%d), want DECIMAL(38,%d)", precision, scale, MaxDecimalScale)
	}

	// The pre-0002 value survived the widening exactly.
	var seeded duckdb.Decimal
	if err := store.db.QueryRowContext(ctx,
		`SELECT billed_cost FROM cost_records WHERE service_name = 'SeedService'`).Scan(&seeded); err != nil {
		t.Fatalf("reading seeded value: %v", err)
	}
	seededDec := decimal.NewFromBigInt(seeded.Value, -int32(seeded.Scale)) //nolint:gosec // DuckDB DECIMAL scale is at most 38.
	if got, want := seededDec.String(), "0.123456789012"; got != want {
		t.Fatalf("seeded billed_cost = %s after migration, want %s preserved exactly", got, want)
	}

	// An 18-fractional-digit value ingests through the production insert
	// path and reads back byte-exact.
	const precise = "0.123456789012345678"
	batch := Batch{Connector: "aws-focus", SourceIdentity: "precision.csv.gz", ContentHash: "sha256:precise", TenantID: focus.DefaultTenant}
	if _, err := store.ReplaceIngestBatch(ctx, batch, []focus.CostRecord{
		testRecord(t, "Amazon Elastic Compute Cloud", day(1), precise),
	}); err != nil {
		t.Fatalf("ingesting 18-digit value: %v", err)
	}
	daily, err := store.DailyCostsByService(ctx, focus.DefaultTenant, day(1), day(1), "", "")
	if err != nil {
		t.Fatalf("DailyCostsByService: %v", err)
	}
	if len(daily.Days) != 1 || len(daily.Days[0].Services) != 1 {
		t.Fatalf("daily = %+v, want exactly the ingested day/service", daily)
	}
	if got := daily.Days[0].Services[0].Cost.String(); got != precise {
		t.Fatalf("18-digit value read back as %q, want %q byte-exact (a stale insert cast silently rounds)", got, precise)
	}
}
