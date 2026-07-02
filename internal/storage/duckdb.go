// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/duckdb/duckdb-go/v2"
	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/focus"
)

// DatabaseFile is the DuckDB database file name inside the data directory.
const DatabaseFile = "costroid.duckdb"

// DuckDB is the embedded default Store implementation (decisions D5,
// D22), backed by DuckDB's official CGO driver.
type DuckDB struct {
	db *sql.DB
}

var _ Store = (*DuckDB)(nil)

// Open opens (creating if needed) the embedded database inside dataDir
// and applies pending schema migrations (decision D19).
//
// DuckDB allows a single read-write process per database file: opening a
// data directory that another Costroid process holds open fails with an
// actionable error.
func Open(ctx context.Context, dataDir string) (*DuckDB, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating data directory %s: %w", dataDir, err)
	}
	path := filepath.Join(dataDir, DatabaseFile)

	db, err := sql.Open("duckdb", path)
	if err == nil {
		err = db.PingContext(ctx)
	}
	if err != nil {
		if strings.Contains(err.Error(), "Could not set lock on file") {
			return nil, fmt.Errorf("the Costroid database in %s is in use by another process — "+
				"the embedded store allows a single process at a time, so stop the other "+
				"costroid process (e.g. `costroid serve`) before running this command", dataDir)
		}
		return nil, fmt.Errorf("opening DuckDB database %s: %w", path, err)
	}

	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrating database %s: %w", path, err)
	}
	return &DuckDB{db: db}, nil
}

// Close implements Store.
func (s *DuckDB) Close() error {
	return s.db.Close()
}

// ReplaceIngestBatch implements Store.
func (s *DuckDB) ReplaceIngestBatch(ctx context.Context, batch Batch, records []focus.CostRecord) (ReplaceResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReplaceResult{}, fmt.Errorf("beginning replace transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Unchanged-content short-circuit: same replace key, same content
	// hash, same tenant — nothing to rewrite.
	var storedHash, storedTenant string
	var storedCount int
	err = tx.QueryRowContext(ctx,
		`SELECT content_hash, tenant_id, record_count FROM ingest_batches
		 WHERE connector = ? AND source_identity = ?`,
		batch.Connector, batch.SourceIdentity).Scan(&storedHash, &storedTenant, &storedCount)
	switch {
	case err == nil:
		if storedHash == batch.ContentHash && storedTenant == batch.TenantID {
			return ReplaceResult{RecordCount: storedCount, Unchanged: true}, nil
		}
	case errors.Is(err, sql.ErrNoRows):
		// First ingest of this batch.
	default:
		return ReplaceResult{}, fmt.Errorf("looking up ingest batch: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM cost_records WHERE batch_connector = ? AND batch_source_identity = ?`,
		batch.Connector, batch.SourceIdentity); err != nil {
		return ReplaceResult{}, fmt.Errorf("deleting prior batch records: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM ingest_batches WHERE connector = ? AND source_identity = ?`,
		batch.Connector, batch.SourceIdentity); err != nil {
		return ReplaceResult{}, fmt.Errorf("deleting prior batch: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO ingest_batches (connector, source_identity, content_hash, tenant_id, record_count, ingested_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		batch.Connector, batch.SourceIdentity, batch.ContentHash, batch.TenantID,
		len(records), time.Now().UTC()); err != nil {
		return ReplaceResult{}, fmt.Errorf("inserting ingest batch: %w", err)
	}

	stmt, err := tx.PrepareContext(ctx, insertRecordSQL)
	if err != nil {
		return ReplaceResult{}, fmt.Errorf("preparing record insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for i := range records {
		if _, err := stmt.ExecContext(ctx, insertRecordArgs(batch, &records[i])...); err != nil {
			return ReplaceResult{}, fmt.Errorf("inserting record %d: %w", i+1, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return ReplaceResult{}, fmt.Errorf("committing replace transaction: %w", err)
	}
	return ReplaceResult{RecordCount: len(records)}, nil
}

// insertRecordSQL binds monetary/quantity parameters as strings and casts
// them to DECIMAL inside DuckDB, so values stay exact end to end.
const insertRecordSQL = `INSERT INTO cost_records (
	x_tenant_id, batch_connector, batch_source_identity,
	billing_account_id, billing_account_name, billing_account_type,
	sub_account_id, sub_account_name, sub_account_type,
	service_provider_name, host_provider_name, invoice_issuer_name,
	billing_currency, billing_period_start, billing_period_end, invoice_id,
	charge_category, charge_class, charge_description, charge_frequency,
	charge_period_start, charge_period_end,
	billed_cost, effective_cost, list_cost, contracted_cost,
	pricing_category, pricing_quantity, pricing_unit, list_unit_price, contracted_unit_price,
	consumed_quantity, consumed_unit,
	service_name, service_category, service_subcategory,
	sku_id, sku_price_id, sku_meter,
	resource_id, resource_name, resource_type,
	region_id, region_name, availability_zone,
	tags
) VALUES (
	?, ?, ?,
	?, ?, ?,
	?, ?, ?,
	?, ?, ?,
	?, ?, ?, ?,
	?, ?, ?, ?,
	?, ?,
	CAST(? AS DECIMAL(38,12)), CAST(? AS DECIMAL(38,12)), CAST(? AS DECIMAL(38,12)), CAST(? AS DECIMAL(38,12)),
	?, CAST(? AS DECIMAL(38,12)), ?, CAST(? AS DECIMAL(38,12)), CAST(? AS DECIMAL(38,12)),
	CAST(? AS DECIMAL(38,12)), ?,
	?, ?, ?,
	?, ?, ?,
	?, ?, ?,
	?, ?, ?,
	?
)`

func insertRecordArgs(batch Batch, r *focus.CostRecord) []any {
	return []any{
		r.XTenantID, batch.Connector, batch.SourceIdentity,
		r.BillingAccountID, nullString(r.BillingAccountName), nullString(r.BillingAccountType),
		nullString(r.SubAccountID), nullString(r.SubAccountName), nullString(r.SubAccountType),
		r.ServiceProviderName, nullString(r.HostProviderName), r.InvoiceIssuerName,
		r.BillingCurrency, r.BillingPeriodStart.UTC(), r.BillingPeriodEnd.UTC(), nullString(r.InvoiceID),
		r.ChargeCategory, nullString(r.ChargeClass), nullString(r.ChargeDescription), nullString(r.ChargeFrequency),
		r.ChargePeriodStart.UTC(), r.ChargePeriodEnd.UTC(),
		r.BilledCost.String(), r.EffectiveCost.String(), r.ListCost.String(), r.ContractedCost.String(),
		nullString(r.PricingCategory), nullDecimal(r.PricingQuantity), nullString(r.PricingUnit),
		nullDecimal(r.ListUnitPrice), nullDecimal(r.ContractedUnitPrice),
		nullDecimal(r.ConsumedQuantity), nullString(r.ConsumedUnit),
		r.ServiceName, r.ServiceCategory, nullString(r.ServiceSubcategory),
		nullString(r.SkuID), nullString(r.SkuPriceID), nullString(r.SkuMeter),
		nullString(r.ResourceID), nullString(r.ResourceName), nullString(r.ResourceType),
		nullString(r.RegionID), nullString(r.RegionName), nullString(r.AvailabilityZone),
		tagsJSON(r.Tags),
	}
}

// DailyCostsByService implements Store.
func (s *DuckDB) DailyCostsByService(ctx context.Context, tenant string, start, end time.Time) (DailyCosts, error) {
	where := "WHERE x_tenant_id = ?"
	args := []any{tenant}
	if !start.IsZero() {
		where += " AND CAST(charge_period_start AS DATE) >= CAST(? AS DATE)"
		args = append(args, start.UTC().Format(time.DateOnly))
	}
	if !end.IsZero() {
		where += " AND CAST(charge_period_start AS DATE) <= CAST(? AS DATE)"
		args = append(args, end.UTC().Format(time.DateOnly))
	}

	var result DailyCosts

	// Single-currency guard for this slice: mixed currencies cannot be
	// summed without conversion, which is a later concern.
	currencies, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT billing_currency FROM cost_records `+where+` ORDER BY billing_currency`, args...)
	if err != nil {
		return DailyCosts{}, fmt.Errorf("querying billing currencies: %w", err)
	}
	defer func() { _ = currencies.Close() }()
	for currencies.Next() {
		var c string
		if err := currencies.Scan(&c); err != nil {
			return DailyCosts{}, fmt.Errorf("scanning billing currency: %w", err)
		}
		if result.Currency != "" && result.Currency != c {
			return DailyCosts{}, fmt.Errorf("stored records mix billing currencies (%s, %s); currency conversion is not supported yet", result.Currency, c)
		}
		result.Currency = c
	}
	if err := currencies.Err(); err != nil {
		return DailyCosts{}, fmt.Errorf("querying billing currencies: %w", err)
	}

	// Deterministic ordering: days ascending, then service name ascending.
	rows, err := s.db.QueryContext(ctx,
		`SELECT CAST(charge_period_start AS DATE) AS day, service_name, SUM(billed_cost)
		 FROM cost_records `+where+`
		 GROUP BY day, service_name
		 ORDER BY day ASC, service_name ASC`, args...)
	if err != nil {
		return DailyCosts{}, fmt.Errorf("querying daily costs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			day     time.Time
			service string
			sum     duckdb.Decimal
		)
		// DuckDB DECIMAL columns scan as duckdb.Decimal — scanning into
		// float64 would silently lose precision.
		if err := rows.Scan(&day, &service, &sum); err != nil {
			return DailyCosts{}, fmt.Errorf("scanning daily cost row: %w", err)
		}
		cost := decimal.NewFromBigInt(sum.Value, -int32(sum.Scale)) //nolint:gosec // DuckDB DECIMAL scale is at most 38.
		day = day.UTC()

		if n := len(result.Days); n == 0 || !result.Days[n-1].Date.Equal(day) {
			result.Days = append(result.Days, DayCosts{Date: day})
		}
		last := &result.Days[len(result.Days)-1]
		last.Services = append(last.Services, ServiceCost{ServiceName: service, Cost: cost})
	}
	if err := rows.Err(); err != nil {
		return DailyCosts{}, fmt.Errorf("querying daily costs: %w", err)
	}
	return result, nil
}

// nullString maps the model's "" (null) convention to SQL NULL.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullDecimal binds a nullable decimal as an exact string (or SQL NULL).
func nullDecimal(d decimal.NullDecimal) any {
	if !d.Valid {
		return nil
	}
	return d.Decimal.String()
}

// tagsJSON serializes the Tags map as canonical JSON text (or SQL NULL).
func tagsJSON(tags map[string]string) any {
	if len(tags) == 0 {
		return nil
	}
	b, err := json.Marshal(tags)
	if err != nil {
		// Unreachable: map[string]string always marshals.
		return nil
	}
	return string(b)
}
