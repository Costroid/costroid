// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/storage"
)

// maxReportedRowErrors caps how many offending rows a failed ingest
// reports; the count of remaining ones is still stated.
const maxReportedRowErrors = 10

// Result reports a completed pipeline run.
type Result struct {
	// Batch identifies what was (or would have been) replaced.
	Batch storage.Batch
	// Records is the number of records stored for the batch.
	Records int
	// Unchanged is true when the source content matched the stored
	// batch and the store short-circuited to a no-op.
	Unchanged bool
	// Replaced is true when changed content replaced a previously
	// stored batch; PreviousBilledCost and NewBilledCost then carry the
	// store's batch totals before and after, for restatement visibility
	// (decision D26d).
	Replaced           bool
	PreviousBilledCost decimal.Decimal
	NewBilledCost      decimal.Decimal
}

// RowError is one offending row of an aborted ingest, with every
// transform or conformance error found on it.
type RowError struct {
	Row  int
	Errs []error
}

// RowErrors is the abort error of a failed transform/validation run: the
// ingest stored nothing, and the first offending rows say why.
type RowErrors struct {
	// Total is the total number of offending rows.
	Total int
	// First holds the first maxReportedRowErrors offending rows.
	First []RowError
}

func (e *RowErrors) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d row(s) failed FOCUS conformance; nothing was ingested", e.Total)
	for _, re := range e.First {
		for _, err := range re.Errs {
			fmt.Fprintf(&b, "\n  row %d: %v", re.Row, err)
		}
	}
	if rest := e.Total - len(e.First); rest > 0 {
		fmt.Fprintf(&b, "\n  ... and %d more row(s)", rest)
	}
	return b.String()
}

// Run executes the shared ingestion pipeline for one connector: read
// every raw record, apply the FOCUS version transform into the internal
// 1.4 model, validate conformance, and transactionally replace the
// connector's batch in the store (see Connector for the idempotency
// semantics). Validation failures abort the whole run — there are no
// partial loads — and report the offending rows by number. Every stored
// record carries the given tenant as x_TenantId (decision D15).
func Run(ctx context.Context, conn Connector, store storage.Store, tenant string) (Result, error) {
	if tenant == "" {
		return Result{}, errors.New("tenant must not be empty")
	}

	transform, err := focus.TransformTo14(conn.FOCUSVersion())
	if err != nil {
		return Result{}, fmt.Errorf("connector %s: %w", conn.Name(), err)
	}

	hash, err := conn.ContentHash(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("hashing source content: %w", err)
	}
	batch := storage.Batch{
		Connector:      conn.Name(),
		SourceIdentity: conn.SourceIdentity(),
		ContentHash:    hash,
		TenantID:       tenant,
	}

	reader, err := conn.Records(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("opening source records: %w", err)
	}
	defer func() { _ = reader.Close() }()

	rules := focus.DefaultRules()
	var (
		records   []focus.CostRecord
		rowErrors RowErrors
	)
	addRowErrors := func(row int, errs ...error) {
		rowErrors.Total++
		if len(rowErrors.First) < maxReportedRowErrors {
			rowErrors.First = append(rowErrors.First, RowError{Row: row, Errs: errs})
		}
	}

	for {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		row, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Result{}, fmt.Errorf("reading source records: %w", err)
		}

		normalized, err := transform(row.Record)
		if err != nil {
			addRowErrors(row.Number, err)
			continue
		}
		if violations := focus.Validate(normalized, rules); len(violations) > 0 {
			errs := make([]error, len(violations))
			for i, v := range violations {
				errs[i] = v
			}
			addRowErrors(row.Number, errs...)
			continue
		}
		rec, err := focus.ParseRecord(normalized)
		if err != nil {
			addRowErrors(row.Number, err)
			continue
		}
		if errs := storeScaleErrors(rec); len(errs) > 0 {
			addRowErrors(row.Number, errs...)
			continue
		}
		rec.XTenantID = tenant
		records = append(records, *rec)
	}

	if rowErrors.Total > 0 {
		return Result{}, &rowErrors
	}

	replaced, err := store.ReplaceIngestBatch(ctx, batch, records)
	if err != nil {
		return Result{}, fmt.Errorf("replacing ingest batch: %w", err)
	}
	return Result{
		Batch:              batch,
		Records:            replaced.RecordCount,
		Unchanged:          replaced.Unchanged,
		Replaced:           replaced.Replaced,
		PreviousBilledCost: replaced.PreviousBilledCost,
		NewBilledCost:      replaced.NewBilledCost,
	}, nil
}

// maxStoreIntegerAbs is the smallest magnitude the store cannot hold:
// DECIMAL(38,MaxDecimalScale) keeps 38−MaxDecimalScale integer digits,
// so any |value| ≥ 10^(38−MaxDecimalScale) would overflow the column.
var maxStoreIntegerAbs = decimal.New(1, 38-storage.MaxDecimalScale)

// storeScaleErrors reports monetary/quantity values that exceed the
// store's decimal capacity — more fractional digits than
// storage.MaxDecimalScale, or an integer part beyond the
// 38−MaxDecimalScale digits DECIMAL(38,MaxDecimalScale) keeps. Such rows
// abort the ingest with a row-numbered, column-named error — silently
// rounding in the store would violate the exactness invariant, and an
// overflowing insert would otherwise abort mid-transaction with a raw
// DuckDB error. Trailing zeros beyond the scale limit are fine: only
// values whose exactness would be lost fail.
func storeScaleErrors(rec *focus.CostRecord) []error {
	var errs []error
	check := func(col string, d decimal.Decimal) {
		if !d.Equal(d.Truncate(storage.MaxDecimalScale)) {
			errs = append(errs, fmt.Errorf(
				"column %s: value %s has more than %d fractional digits; the embedded store holds DECIMAL(38,%d) and never rounds silently",
				col, d, storage.MaxDecimalScale, storage.MaxDecimalScale))
		}
		if d.Abs().Cmp(maxStoreIntegerAbs) >= 0 {
			errs = append(errs, fmt.Errorf(
				"column %s: value %s has more than %d integer digits; the embedded store holds DECIMAL(38,%d) and never truncates silently",
				col, d, 38-storage.MaxDecimalScale, storage.MaxDecimalScale))
		}
	}
	checkNull := func(col string, d decimal.NullDecimal) {
		if d.Valid {
			check(col, d.Decimal)
		}
	}
	check("BilledCost", rec.BilledCost)
	check("EffectiveCost", rec.EffectiveCost)
	check("ListCost", rec.ListCost)
	check("ContractedCost", rec.ContractedCost)
	checkNull("PricingQuantity", rec.PricingQuantity)
	checkNull("ListUnitPrice", rec.ListUnitPrice)
	checkNull("ContractedUnitPrice", rec.ContractedUnitPrice)
	checkNull("ConsumedQuantity", rec.ConsumedQuantity)
	return errs
}
