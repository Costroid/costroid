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
	if err != nil {
		return nil, openError(err, dataDir, path)
	}
	if err := db.PingContext(ctx); err != nil {
		// The failed pool still holds the DuckDB instance (and its file
		// lock) until closed.
		_ = db.Close()
		return nil, openError(err, dataDir, path)
	}

	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrating database %s: %w", path, err)
	}
	return &DuckDB{db: db}, nil
}

// openError classifies embedded-database open failures into actionable
// messages. The single-writer lock refusal must be classified on BOTH
// open paths: duckdb-go v2 implements database/sql's DriverContext, so
// sql.Open itself opens the database file (and takes — or is refused —
// its lock) rather than deferring to the first use; PingContext only
// covers whatever failure sql.Open did not surface.
func openError(err error, dataDir, path string) error {
	if strings.Contains(err.Error(), "Could not set lock on file") {
		return fmt.Errorf("the Costroid database in %s is in use by another process — "+
			"the embedded store allows a single process at a time, so stop the other "+
			"costroid process (e.g. `costroid serve`) before running this command", dataDir)
	}
	return fmt.Errorf("opening DuckDB database %s: %w", path, err)
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
	result := ReplaceResult{RecordCount: len(records)}
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
		// Changed content replaces a stored batch: report the cost delta
		// (decision D26d) from what the store actually held.
		result.Replaced = true
		result.PreviousBilledCost, err = batchBilledCost(ctx, tx, batch)
		if err != nil {
			return ReplaceResult{}, fmt.Errorf("totaling prior batch cost: %w", err)
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

	// Total the new content from what was just stored, so the reported
	// delta reflects the store, not the connector.
	if result.NewBilledCost, err = batchBilledCost(ctx, tx, batch); err != nil {
		return ReplaceResult{}, fmt.Errorf("totaling new batch cost: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return ReplaceResult{}, fmt.Errorf("committing replace transaction: %w", err)
	}
	return result, nil
}

// batchBilledCostSQL totals one batch's stored BilledCost. The zero
// literal's cast scale is bound to MaxDecimalScale like insertRecordSQL,
// so the store's decimal scale has a single source of truth.
var batchBilledCostSQL = fmt.Sprintf(
	`SELECT COALESCE(SUM(billed_cost), CAST(0 AS DECIMAL(38,%d)))
	 FROM cost_records WHERE batch_connector = ? AND batch_source_identity = ?`, MaxDecimalScale)

// batchBilledCost totals the stored BilledCost of one batch inside tx.
func batchBilledCost(ctx context.Context, tx *sql.Tx, batch Batch) (decimal.Decimal, error) {
	var sum duckdb.Decimal
	if err := tx.QueryRowContext(ctx, batchBilledCostSQL,
		batch.Connector, batch.SourceIdentity).Scan(&sum); err != nil {
		return decimal.Decimal{}, err
	}
	return decimal.NewFromBigInt(sum.Value, -int32(sum.Scale)), nil //nolint:gosec // DuckDB DECIMAL scale is at most 38.
}

// insertRecordSQL binds monetary/quantity parameters as strings and casts
// them to DECIMAL inside DuckDB, so values stay exact end to end. The
// cast scale is bound to MaxDecimalScale because DuckDB's parameter-bound
// CAST silently ROUNDS to the target scale instead of erroring — a cast
// narrower than the columns would corrupt values the pipeline already
// accepted as exact (decision D25).
var insertRecordSQL = fmt.Sprintf(`INSERT INTO cost_records (
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
	CAST(? AS DECIMAL(38,%[1]d)), CAST(? AS DECIMAL(38,%[1]d)), CAST(? AS DECIMAL(38,%[1]d)), CAST(? AS DECIMAL(38,%[1]d)),
	?, CAST(? AS DECIMAL(38,%[1]d)), ?, CAST(? AS DECIMAL(38,%[1]d)), CAST(? AS DECIMAL(38,%[1]d)),
	CAST(? AS DECIMAL(38,%[1]d)), ?,
	?, ?, ?,
	?, ?, ?,
	?, ?, ?,
	?, ?, ?,
	?
)`, MaxDecimalScale)

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

// EnrichedAIRows returns the enrichment-relevant projection of one
// tenant+connector's stored cost records (see AIRow), ordered
// deterministically. Decimal columns are cast to text in SQL and rebuilt
// exactly, so no precision is lost and NULLs stay NULL. This is a store-level
// verification helper (decision D33), not a product query surface — it is on
// the concrete store only, never the Store interface.
func (s *DuckDB) EnrichedAIRows(ctx context.Context, tenant, connector string) ([]AIRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT charge_description, sku_id, sku_price_id, sku_meter,
			CAST(consumed_quantity AS VARCHAR), consumed_unit,
			CAST(pricing_quantity AS VARCHAR), pricing_unit,
			CAST(billed_cost AS VARCHAR)
		 FROM cost_records
		 WHERE x_tenant_id = ? AND batch_connector = ?
		 ORDER BY charge_period_start ASC, sku_id ASC, billed_cost ASC`, tenant, connector)
	if err != nil {
		return nil, fmt.Errorf("querying AI cost rows: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []AIRow
	for rows.Next() {
		var (
			desc, skuID, skuPriceID, skuMeter sql.NullString
			consumedQty, consumedUnit         sql.NullString
			pricingQty, pricingUnit           sql.NullString
			billed                            sql.NullString
		)
		if err := rows.Scan(&desc, &skuID, &skuPriceID, &skuMeter,
			&consumedQty, &consumedUnit, &pricingQty, &pricingUnit, &billed); err != nil {
			return nil, fmt.Errorf("scanning AI cost row: %w", err)
		}
		r := AIRow{
			ChargeDescription: desc.String,
			SkuID:             skuID.String,
			SkuPriceID:        skuPriceID.String,
			SkuMeter:          skuMeter.String,
			ConsumedUnit:      consumedUnit.String,
			PricingUnit:       pricingUnit.String,
		}
		if r.ConsumedQuantity, err = parseNullText(consumedQty); err != nil {
			return nil, fmt.Errorf("parsing consumed_quantity: %w", err)
		}
		if r.PricingQuantity, err = parseNullText(pricingQty); err != nil {
			return nil, fmt.Errorf("parsing pricing_quantity: %w", err)
		}
		if billed.Valid {
			if r.BilledCost, err = decimal.NewFromString(billed.String); err != nil {
				return nil, fmt.Errorf("parsing billed_cost: %w", err)
			}
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("querying AI cost rows: %w", err)
	}
	return out, nil
}

// parseNullText rebuilds a nullable decimal from a nullable text column.
func parseNullText(v sql.NullString) (decimal.NullDecimal, error) {
	if !v.Valid {
		return decimal.NullDecimal{}, nil
	}
	d, err := decimal.NewFromString(v.String)
	if err != nil {
		return decimal.NullDecimal{}, err
	}
	return decimal.NullDecimal{Decimal: d, Valid: true}, nil
}

// SyncStates implements Store. TenantID is joined from the source's
// stored ingest batch (see SyncState.TenantID) — it is not a column of
// the sync tuple itself.
func (s *DuckDB) SyncStates(ctx context.Context, connector string) (map[string]SyncState, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT s.source_identity, s.manifest_key, s.manifest_etag, s.manifest_last_modified,
			s.manifest_size, COALESCE(b.tenant_id, '')
		 FROM sync_state s
		 LEFT JOIN ingest_batches b
		   ON b.connector = s.connector AND b.source_identity = s.source_identity
		 WHERE s.connector = ?`, connector)
	if err != nil {
		return nil, fmt.Errorf("querying sync state: %w", err)
	}
	defer func() { _ = rows.Close() }()

	states := map[string]SyncState{}
	for rows.Next() {
		st := SyncState{Connector: connector}
		if err := rows.Scan(&st.SourceIdentity, &st.ManifestKey, &st.ManifestETag,
			&st.ManifestLastModified, &st.ManifestSize, &st.TenantID); err != nil {
			return nil, fmt.Errorf("scanning sync state: %w", err)
		}
		st.ManifestLastModified = st.ManifestLastModified.UTC()
		states[st.SourceIdentity] = st
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("querying sync state: %w", err)
	}
	return states, nil
}

// UpsertSyncState implements Store.
func (s *DuckDB) UpsertSyncState(ctx context.Context, state SyncState) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO sync_state (connector, source_identity, manifest_key, manifest_etag,
			manifest_last_modified, manifest_size, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (connector, source_identity) DO UPDATE SET
			manifest_key = excluded.manifest_key,
			manifest_etag = excluded.manifest_etag,
			manifest_last_modified = excluded.manifest_last_modified,
			manifest_size = excluded.manifest_size,
			updated_at = excluded.updated_at`,
		state.Connector, state.SourceIdentity, state.ManifestKey, state.ManifestETag,
		state.ManifestLastModified.UTC(), state.ManifestSize, time.Now().UTC()); err != nil {
		return fmt.Errorf("upserting sync state: %w", err)
	}
	return nil
}

// ManifestAttributions implements Store.
func (s *DuckDB) ManifestAttributions(ctx context.Context, connector string) (map[string]ManifestAttribution, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT manifest_key, etag, last_modified, size_bytes, billing_period, submitted_time, export_name
		 FROM manifest_attributions WHERE connector = ?`, connector)
	if err != nil {
		return nil, fmt.Errorf("querying manifest attributions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	attrs := map[string]ManifestAttribution{}
	for rows.Next() {
		a := ManifestAttribution{Connector: connector}
		if err := rows.Scan(&a.ManifestKey, &a.ETag, &a.LastModified, &a.Size,
			&a.BillingPeriod, &a.SubmittedTime, &a.ExportName); err != nil {
			return nil, fmt.Errorf("scanning manifest attribution: %w", err)
		}
		a.LastModified = a.LastModified.UTC()
		// TIMESTAMP holds microseconds, so a submitted time with finer
		// fractional digits (Azure emits seven) rounds trips truncated;
		// current-run selection compares runs whose submission times
		// differ by far more than that.
		a.SubmittedTime = a.SubmittedTime.UTC()
		attrs[a.ManifestKey] = a
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("querying manifest attributions: %w", err)
	}
	return attrs, nil
}

// UpsertManifestAttribution implements Store.
func (s *DuckDB) UpsertManifestAttribution(ctx context.Context, attr ManifestAttribution) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO manifest_attributions (connector, manifest_key, etag, last_modified,
			size_bytes, billing_period, submitted_time, export_name, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (connector, manifest_key) DO UPDATE SET
			etag = excluded.etag,
			last_modified = excluded.last_modified,
			size_bytes = excluded.size_bytes,
			billing_period = excluded.billing_period,
			submitted_time = excluded.submitted_time,
			export_name = excluded.export_name,
			updated_at = excluded.updated_at`,
		attr.Connector, attr.ManifestKey, attr.ETag, attr.LastModified.UTC(),
		attr.Size, attr.BillingPeriod, attr.SubmittedTime.UTC(), attr.ExportName, time.Now().UTC()); err != nil {
		return fmt.Errorf("upserting manifest attribution: %w", err)
	}
	return nil
}

// PutCredential implements Store. A replace keeps the slot's original
// created_at; only nonce, ciphertext, and updated_at change.
func (s *DuckDB) PutCredential(ctx context.Context, cred Credential) error {
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO credentials (name, nonce, ciphertext, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (name) DO UPDATE SET
			nonce = excluded.nonce,
			ciphertext = excluded.ciphertext,
			updated_at = excluded.updated_at`,
		cred.Name, cred.Nonce, cred.Ciphertext, now, now); err != nil {
		return fmt.Errorf("storing credential: %w", err)
	}
	return nil
}

// GetCredential implements Store.
func (s *DuckDB) GetCredential(ctx context.Context, name string) (Credential, bool, error) {
	cred := Credential{Name: name}
	err := s.db.QueryRowContext(ctx,
		`SELECT nonce, ciphertext, created_at, updated_at FROM credentials WHERE name = ?`, name).
		Scan(&cred.Nonce, &cred.Ciphertext, &cred.CreatedAt, &cred.UpdatedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Credential{}, false, nil
	case err != nil:
		return Credential{}, false, fmt.Errorf("reading credential: %w", err)
	}
	cred.CreatedAt = cred.CreatedAt.UTC()
	cred.UpdatedAt = cred.UpdatedAt.UTC()
	return cred, true, nil
}

// ListCredentials implements Store. It returns names and timestamps only —
// never nonce or ciphertext.
func (s *DuckDB) ListCredentials(ctx context.Context) ([]CredentialInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT name, created_at, updated_at FROM credentials ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("listing credentials: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []CredentialInfo
	for rows.Next() {
		var info CredentialInfo
		if err := rows.Scan(&info.Name, &info.CreatedAt, &info.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning credential: %w", err)
		}
		info.CreatedAt = info.CreatedAt.UTC()
		info.UpdatedAt = info.UpdatedAt.UTC()
		out = append(out, info)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing credentials: %w", err)
	}
	return out, nil
}

// DeleteCredential implements Store.
func (s *DuckDB) DeleteCredential(ctx context.Context, name string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM credentials WHERE name = ?`, name)
	if err != nil {
		return false, fmt.Errorf("deleting credential: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("deleting credential: %w", err)
	}
	return n > 0, nil
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
func tagsJSON(tags map[string]any) any {
	if len(tags) == 0 {
		return nil
	}
	b, err := json.Marshal(tags)
	if err != nil {
		// Unreachable: Tags values are JSON scalars (string, bool,
		// json.Number, nil) per focus.ParseTags, which always marshal.
		return nil
	}
	return string(b)
}
