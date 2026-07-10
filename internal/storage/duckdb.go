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

	"github.com/Costroid/costroid/internal/allocation"
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
func (s *DuckDB) DailyCostsByService(ctx context.Context, tenant string, start, end time.Time, groupBy ...CostGroupBy) (DailyCosts, error) {
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

	selectedGroup := GroupByService
	if len(groupBy) > 0 {
		selectedGroup = groupBy[0]
	}
	var groupColumn string
	switch selectedGroup {
	case GroupByService:
		groupColumn = "service_name"
	case GroupByProvider:
		groupColumn = "service_provider_name"
	default:
		groupColumn = "service_name"
	}

	// Deterministic ordering: days ascending, then grouping key ascending.
	rows, err := s.db.QueryContext(ctx,
		`SELECT CAST(charge_period_start AS DATE) AS day, `+groupColumn+` AS cost_group, SUM(billed_cost)
		 FROM cost_records `+where+`
		 GROUP BY day, cost_group
		 ORDER BY day ASC, cost_group ASC`, args...)
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

// allocationColumns maps each validated column dimension to its HARDCODED
// cost_records column literal — the injection boundary for column operands: no
// caller-supplied string ever reaches SQL as a column name. Its key set is
// asserted SET-EQUAL to allocation.ColumnDimensions() by a storage test, so the
// two closed sets can never drift. Tag dimensions ("tag:<key>") are handled
// separately (the bare key is bound into json_extract_string), not through this
// map.
var allocationColumns = map[string]string{
	"billing_account_id":    "billing_account_id",
	"sub_account_id":        "sub_account_id",
	"sub_account_name":      "sub_account_name",
	"service_provider_name": "service_provider_name",
	"service_name":          "service_name",
	"service_category":      "service_category",
	"service_subcategory":   "service_subcategory",
	"charge_category":       "charge_category",
	"charge_description":    "charge_description",
	"region_id":             "region_id",
	"region_name":           "region_name",
	"resource_id":           "resource_id",
	"resource_name":         "resource_name",
	"resource_type":         "resource_type",
	"sku_id":                "sku_id",
}

// allocationOperand returns the SQL operand for a condition dimension and any
// bound args it carries. A column dimension resolves through the hardcoded
// allocationColumns map (an unknown name is an error, never a fallback column);
// a tag dimension compiles to json_extract_string(tags, ?) with the bare key
// bound (keys containing ':' or '.' extract literally — verified).
func allocationOperand(dimension string) (sql string, args []any, err error) {
	if key, ok := allocation.TagKey(dimension); ok {
		return "json_extract_string(tags, ?)", []any{key}, nil
	}
	col, ok := allocationColumns[dimension]
	if !ok {
		return "", nil, fmt.Errorf("unknown allocation dimension %q", dimension)
	}
	return col, nil, nil
}

// allocationCondition compiles one validated condition into a boolean SQL
// fragment plus its bound args. Every rule-supplied string (match value, tag
// key) is a parameter — never interpolated. It stays memory-safe on a
// value-less operator by binding "" rather than dereferencing a nil pointer.
func allocationCondition(c allocation.Condition) (string, []any, error) {
	operand, args, err := allocationOperand(c.Dimension)
	if err != nil {
		return "", nil, err
	}
	_, isTag := allocation.TagKey(c.Dimension)

	val := ""
	if c.Value != nil {
		val = *c.Value
	}

	switch c.Operator {
	case allocation.OpEquals:
		return operand + " = ?", append(args, val), nil
	case allocation.OpContains:
		// contains() takes a literal needle — a bound '%' matches literally, so
		// there is no escaping to get wrong (deliberately not LIKE).
		return "contains(" + operand + ", ?)", append(args, val), nil
	case allocation.OpStartsWith:
		return "starts_with(" + operand + ", ?)", append(args, val), nil
	case allocation.OpOneOf:
		placeholders := make([]string, len(c.Values))
		for i, v := range c.Values {
			placeholders[i] = "?"
			args = append(args, v)
		}
		return operand + " IN (" + strings.Join(placeholders, ", ") + ")", args, nil
	case allocation.OpExists:
		if isTag {
			// Present with a non-null JSON value: a tag stored as JSON null
			// extracts SQL NULL and does NOT satisfy exists.
			return operand + " IS NOT NULL", args, nil
		}
		return "(" + operand + " IS NOT NULL AND " + operand + " <> '')", nil, nil
	default:
		return "", nil, fmt.Errorf("unknown allocation operator %q", c.Operator)
	}
}

// compileAllocationCase builds the grouping-key expression: a CASE mapping each
// rule (top-down, first-match-wins) to its bound label, with an ELSE binding
// the reserved Unallocated label. With zero rules a bare CASE has no WHEN arms
// (a DuckDB parser error), so the degenerate form is CAST(? AS VARCHAR) binding
// Unallocated — everything then groups under Unallocated. Args are returned in
// SQL-text order for positional binding.
func compileAllocationCase(dim allocation.Dimension) (string, []any, error) {
	if len(dim.Rules) == 0 {
		return "CAST(? AS VARCHAR)", []any{allocation.UnallocatedLabel}, nil
	}
	var b strings.Builder
	var args []any
	b.WriteString("CASE")
	for _, rule := range dim.Rules {
		b.WriteString(" WHEN ")
		for i, cond := range rule.Match {
			if i > 0 {
				b.WriteString(" AND ")
			}
			sql, condArgs, err := allocationCondition(cond)
			if err != nil {
				return "", nil, err
			}
			b.WriteString(sql)
			args = append(args, condArgs...)
		}
		b.WriteString(" THEN ?")
		args = append(args, rule.Label)
	}
	b.WriteString(" ELSE ? END")
	args = append(args, allocation.UnallocatedLabel)
	return b.String(), args, nil
}

// DailyCostsByAllocation implements Store. It is the query-time cost-allocation
// sibling of DailyCostsByService (decision D18b): the grouping key is a per-row
// allocation LABEL from dim's ordered, first-match-wins rules, with unmatched
// cost landing in allocation.UnallocatedLabel. Every rule-supplied string is a
// BOUND parameter (never interpolated); its aggregation, tenant scoping, single
// -currency guard, decimal exactness, and ordering mirror DailyCostsByService.
func (s *DuckDB) DailyCostsByAllocation(ctx context.Context, tenant string, start, end time.Time, dim allocation.Dimension) (DailyCosts, error) {
	where := "WHERE x_tenant_id = ?"
	whereArgs := []any{tenant}
	if !start.IsZero() {
		where += " AND CAST(charge_period_start AS DATE) >= CAST(? AS DATE)"
		whereArgs = append(whereArgs, start.UTC().Format(time.DateOnly))
	}
	if !end.IsZero() {
		where += " AND CAST(charge_period_start AS DATE) <= CAST(? AS DATE)"
		whereArgs = append(whereArgs, end.UTC().Format(time.DateOnly))
	}

	var result DailyCosts

	// Single-currency guard, duplicated inline (deliberately NOT shared with
	// DailyCostsByService, whose body stays byte-identical): mixed currencies
	// cannot be summed without conversion. The message is byte-identical to the
	// sibling's.
	currencies, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT billing_currency FROM cost_records `+where+` ORDER BY billing_currency`, whereArgs...)
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

	caseExpr, caseArgs, err := compileAllocationCase(dim)
	if err != nil {
		return DailyCosts{}, fmt.Errorf("compiling allocation rules: %w", err)
	}

	// Placeholders bind POSITIONALLY in SQL-text order: the CASE (in the SELECT)
	// precedes the WHERE, so CASE args go first, then the where args. GROUP BY /
	// ORDER BY reference the CASE's ALIAS — textually repeating the
	// placeholder-bearing CASE there is a binder error.
	args := make([]any, 0, len(caseArgs)+len(whereArgs))
	args = append(args, caseArgs...)
	args = append(args, whereArgs...)

	rows, err := s.db.QueryContext(ctx,
		`SELECT CAST(charge_period_start AS DATE) AS day, `+caseExpr+` AS cost_group, SUM(billed_cost)
		 FROM cost_records `+where+`
		 GROUP BY day, cost_group
		 ORDER BY day ASC, cost_group ASC`, args...)
	if err != nil {
		return DailyCosts{}, fmt.Errorf("querying daily costs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			day   time.Time
			label string
			sum   duckdb.Decimal
		)
		// DuckDB DECIMAL columns scan as duckdb.Decimal — scanning into float64
		// would silently lose precision (decisions D23, D25).
		if err := rows.Scan(&day, &label, &sum); err != nil {
			return DailyCosts{}, fmt.Errorf("scanning daily cost row: %w", err)
		}
		cost := decimal.NewFromBigInt(sum.Value, -int32(sum.Scale)) //nolint:gosec // DuckDB DECIMAL scale is at most 38.
		day = day.UTC()

		if n := len(result.Days); n == 0 || !result.Days[n-1].Date.Equal(day) {
			result.Days = append(result.Days, DayCosts{Date: day})
		}
		last := &result.Days[len(result.Days)-1]
		last.Services = append(last.Services, ServiceCost{ServiceName: label, Cost: cost})
	}
	if err := rows.Err(); err != nil {
		return DailyCosts{}, fmt.Errorf("querying daily costs: %w", err)
	}
	return result, nil
}

// DailyTokensByService returns, for one tenant, the total ConsumedQuantity
// per UTC calendar day (of ChargePeriodStart) per (ServiceName,
// ConsumedUnit), ordered day-ascending then service-name-ascending then
// consumed-unit-ascending. It is scoped to token usage: only rows whose
// ConsumedUnit is "Tokens" (the FOCUS 1.4 UnitFormat token unit, decision
// D33) and whose consumed_quantity is non-NULL contribute — so a money-only
// row never surfaces with a fabricated or zero quantity, and non-token FOCUS
// usage (cloud Hrs/GB-Mo/… quantities) is excluded from this token view. A
// zero start or end means unbounded on that side; a non-zero bound is an
// inclusive calendar-day bound. Aggregation is by ChargePeriod, like
// DailyCostsByService (decision D26c).
func (s *DuckDB) DailyTokensByService(ctx context.Context, tenant string, start, end time.Time) ([]DailyTokenUsage, error) {
	where := "WHERE x_tenant_id = ? AND consumed_quantity IS NOT NULL AND consumed_unit = 'Tokens'"
	args := []any{tenant}
	if !start.IsZero() {
		where += " AND CAST(charge_period_start AS DATE) >= CAST(? AS DATE)"
		args = append(args, start.UTC().Format(time.DateOnly))
	}
	if !end.IsZero() {
		where += " AND CAST(charge_period_start AS DATE) <= CAST(? AS DATE)"
		args = append(args, end.UTC().Format(time.DateOnly))
	}

	// Deterministic ordering: day ascending, then service name, then unit.
	rows, err := s.db.QueryContext(ctx,
		`SELECT CAST(charge_period_start AS DATE) AS day, service_name, consumed_unit,
			SUM(consumed_quantity)
		 FROM cost_records `+where+`
		 GROUP BY day, service_name, consumed_unit
		 ORDER BY day ASC, service_name ASC, consumed_unit ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("querying daily token usage: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []DailyTokenUsage
	for rows.Next() {
		var (
			day     time.Time
			service string
			unit    string
			sum     duckdb.Decimal
		)
		// DuckDB DECIMAL columns scan as duckdb.Decimal — scanning into
		// float64 would silently lose precision (decisions D23, D25).
		if err := rows.Scan(&day, &service, &unit, &sum); err != nil {
			return nil, fmt.Errorf("scanning daily token usage row: %w", err)
		}
		out = append(out, DailyTokenUsage{
			Date:         day.UTC(),
			ServiceName:  service,
			ConsumedUnit: unit,
			Quantity:     decimal.NewFromBigInt(sum.Value, -int32(sum.Scale)), //nolint:gosec // DuckDB DECIMAL scale is at most 38.
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("querying daily token usage: %w", err)
	}
	return out, nil
}

// insertUsageMetricSQL binds the quantity as a string and casts it to the
// column's DECIMAL scale inside DuckDB, exactly like insertRecordSQL, so a
// value stays exact and is never silently rounded (decision D25). service_name
// and service_tier bind as plain strings — "" is a valid non-null value, so the
// tier-less OpenAI rows store "" and never SQL NULL.
var insertUsageMetricSQL = fmt.Sprintf(`INSERT INTO usage_metrics (
	x_tenant_id, connector, source_identity, charge_period_start,
	service_name, service_tier, metric_name, unit, quantity
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, CAST(? AS DECIMAL(38,%[1]d)))`, MaxDecimalScale)

// ReplaceUsageBatch implements Store.
func (s *DuckDB) ReplaceUsageBatch(ctx context.Context, batch UsageBatch, metrics []Metric) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning usage-metrics replace transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Delete-then-insert in one tx: a corrected month wholly supersedes its
	// prior usage rows (decision D26a), and an empty batch clears them.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM usage_metrics WHERE connector = ? AND source_identity = ?`,
		batch.Connector, batch.SourceIdentity); err != nil {
		return fmt.Errorf("deleting prior usage metrics: %w", err)
	}

	stmt, err := tx.PrepareContext(ctx, insertUsageMetricSQL)
	if err != nil {
		return fmt.Errorf("preparing usage-metric insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for i := range metrics {
		m := &metrics[i]
		if _, err := stmt.ExecContext(ctx,
			batch.TenantID, batch.Connector, batch.SourceIdentity, m.ChargePeriodStart.UTC(),
			m.ServiceName, m.ServiceTier, m.MetricName, m.Unit, m.Quantity.String()); err != nil {
			return fmt.Errorf("inserting usage metric %d: %w", i+1, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing usage-metrics replace transaction: %w", err)
	}
	return nil
}

// DailyUsageMetrics implements Store. It clones DailyTokensByService's query and
// scan discipline: exact DECIMAL sums via duckdb.Decimal (never float64), plain
// Go-string scans of the NOT NULL categorical columns (a stored NULL tier would
// error at scan — the migration forbids it), inclusive date bounds bound only
// when non-zero, and a fully-deterministic ORDER BY. The GROUP BY carries BOTH
// metric_name AND unit so different units never merge and different metric names
// within one unit never merge.
func (s *DuckDB) DailyUsageMetrics(ctx context.Context, tenant string, start, end time.Time) ([]DailyUsageMetric, error) {
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

	rows, err := s.db.QueryContext(ctx,
		`SELECT CAST(charge_period_start AS DATE) AS day, service_name, service_tier,
			metric_name, unit, SUM(quantity)
		 FROM usage_metrics `+where+`
		 GROUP BY day, service_name, service_tier, metric_name, unit
		 ORDER BY day ASC, service_name ASC, service_tier ASC, metric_name ASC, unit ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("querying daily usage metrics: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []DailyUsageMetric
	for rows.Next() {
		var (
			day                         time.Time
			service, tier, metric, unit string
			sum                         duckdb.Decimal
		)
		// DECIMAL scans as duckdb.Decimal — a float64 scan would silently lose
		// precision (decisions D23, D25).
		if err := rows.Scan(&day, &service, &tier, &metric, &unit, &sum); err != nil {
			return nil, fmt.Errorf("scanning daily usage metric row: %w", err)
		}
		out = append(out, DailyUsageMetric{
			Date:        day.UTC(),
			ServiceName: service,
			ServiceTier: tier,
			MetricName:  metric,
			Unit:        unit,
			Quantity:    decimal.NewFromBigInt(sum.Value, -int32(sum.Scale)), //nolint:gosec // DuckDB DECIMAL scale is at most 38.
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("querying daily usage metrics: %w", err)
	}
	return out, nil
}

var insertBusinessMetricSQL = fmt.Sprintf(`INSERT INTO business_metrics (
	x_tenant_id, source_label, metric_day, metric_name, quantity
) VALUES (?, ?, ?, ?, CAST(? AS DECIMAL(38,%[1]d)))`, MaxDecimalScale)

// ReplaceBusinessMetricsBatch implements Store. The delete and all inserts are
// atomic, and every value is bound data rather than SQL text.
func (s *DuckDB) ReplaceBusinessMetricsBatch(ctx context.Context, tenant, sourceLabel string, rows []BusinessMetricRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning business-metrics replace transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM business_metrics WHERE x_tenant_id = ? AND source_label = ?`,
		tenant, sourceLabel); err != nil {
		return fmt.Errorf("deleting prior business metrics: %w", err)
	}

	stmt, err := tx.PrepareContext(ctx, insertBusinessMetricSQL)
	if err != nil {
		return fmt.Errorf("preparing business-metric insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for i := range rows {
		row := &rows[i]
		if _, err := stmt.ExecContext(ctx, tenant, sourceLabel, row.MetricDay.UTC(), row.MetricName, row.Quantity.String()); err != nil {
			return fmt.Errorf("inserting business metric %d: %w", i+1, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing business-metrics replace transaction: %w", err)
	}
	return nil
}

// BusinessMetricNames implements Store.
func (s *DuckDB) BusinessMetricNames(ctx context.Context, tenant string) ([]BusinessMetricInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT metric_name, MIN(CAST(metric_day AS DATE)), MAX(CAST(metric_day AS DATE))
		 FROM business_metrics
		 WHERE x_tenant_id = ?
		 GROUP BY metric_name
		 ORDER BY metric_name ASC`, tenant)
	if err != nil {
		return nil, fmt.Errorf("querying business metric names: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []BusinessMetricInfo
	for rows.Next() {
		var info BusinessMetricInfo
		if err := rows.Scan(&info.Name, &info.FirstDay, &info.LastDay); err != nil {
			return nil, fmt.Errorf("scanning business metric name: %w", err)
		}
		info.FirstDay = info.FirstDay.UTC()
		info.LastDay = info.LastDay.UTC()
		out = append(out, info)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("querying business metric names: %w", err)
	}
	return out, nil
}

// DailyBusinessMetricQuantities implements Store. Same-day quantities from
// different source labels deliberately sum: the labels are complementary
// user-authored sources for the same named business measure.
func (s *DuckDB) DailyBusinessMetricQuantities(ctx context.Context, tenant, metric string, start, end time.Time) ([]DayQuantity, error) {
	where := "WHERE x_tenant_id = ? AND metric_name = ?"
	args := []any{tenant, metric}
	if !start.IsZero() {
		where += " AND CAST(metric_day AS DATE) >= CAST(? AS DATE)"
		args = append(args, start.UTC().Format(time.DateOnly))
	}
	if !end.IsZero() {
		where += " AND CAST(metric_day AS DATE) <= CAST(? AS DATE)"
		args = append(args, end.UTC().Format(time.DateOnly))
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT CAST(metric_day AS DATE), SUM(quantity)
		 FROM business_metrics `+where+`
		 GROUP BY CAST(metric_day AS DATE)
		 ORDER BY CAST(metric_day AS DATE) ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("querying daily business metric quantities: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []DayQuantity
	for rows.Next() {
		var (
			day time.Time
			sum duckdb.Decimal
		)
		if err := rows.Scan(&day, &sum); err != nil {
			return nil, fmt.Errorf("scanning daily business metric quantity: %w", err)
		}
		out = append(out, DayQuantity{
			Date:     day.UTC(),
			Quantity: decimal.NewFromBigInt(sum.Value, -int32(sum.Scale)), //nolint:gosec // DuckDB DECIMAL scale is at most 38.
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("querying daily business metric quantities: %w", err)
	}
	return out, nil
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
