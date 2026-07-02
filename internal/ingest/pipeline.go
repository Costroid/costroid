// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

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
	return Result{Batch: batch, Records: replaced.RecordCount, Unchanged: replaced.Unchanged}, nil
}
