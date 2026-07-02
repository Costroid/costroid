// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package ingest defines Costroid's stable connector contract (decision
// D16) and the shared ingestion pipeline every connector's data flows
// through: read → version transform → validate → replace batch. The
// pipeline — not the individual connector — owns normalization,
// conformance validation, and idempotency, so a connector only has to
// describe its source and yield its raw records.
package ingest

import (
	"context"

	"github.com/Costroid/costroid/internal/focus"
)

// Connector is the stable interface every cost-data source implements
// (decision D16). It is the ecosystem seam: community connectors
// implement exactly this and plug into the shared pipeline without
// touching the core.
//
// A connector describes one logical source of FOCUS data (a file today;
// a bucket prefix, billing API, ... in later slices) and streams that
// source's raw records. It performs no normalization, no validation, and
// no storage.
//
// # Idempotency semantics (pinned)
//
// The pipeline replaces stored data keyed by (Name, SourceIdentity) —
// the replace key. Consequences:
//
//   - Re-ingesting the same source — whether or not its content changed
//     — REPLACES its prior batch in one transaction and never
//     duplicates rows.
//   - ContentHash is recorded on the batch as metadata for change
//     detection only; it is NOT part of the replace key. When the stored
//     batch already carries the same hash, the pipeline may short-circuit
//     the rewrite as a no-op.
//   - Overlapping billing periods ingested under DIFFERENT source
//     identities are stored side by side and can double-count. Detecting
//     and superseding restated data across sources is exactly the FOCUS
//     correction/supersede machinery (D16, "correction-aware") deferred
//     to a later slice — connectors must not try to emulate it.
//
// Incremental fetch state ("only fetch what changed since last run") is
// likewise a later slice; the contract leaves room for it but defines
// none of it yet.
type Connector interface {
	// Name is the connector's unique registry name, e.g. "aws-focus".
	// It is the first half of the replace key and must be stable across
	// releases.
	Name() string

	// FOCUSVersion is the FOCUS specification version of the data this
	// source produces (e.g. focus.V1_2). The pipeline uses it to pick
	// the version transform into the internal 1.4 model (decision D4).
	FOCUSVersion() focus.Version

	// SourceIdentity is the stable identity of the logical source, the
	// second half of the replace key. Two runs that read the same
	// logical source must return the same identity; two different
	// sources must not collide. For file connectors this defaults to
	// the file's base name.
	SourceIdentity() string

	// ContentHash returns a digest (e.g. "sha256:<hex>") of the raw
	// source content, recorded on the ingest batch for change
	// detection. It must not consume the reader returned by Records.
	ContentHash(ctx context.Context) (string, error)

	// Records opens the source and returns a streaming reader over its
	// raw records, in the column shape of the connector's declared
	// FOCUS version. The caller closes the reader.
	Records(ctx context.Context) (RecordReader, error)
}

// RecordReader streams raw source rows.
type RecordReader interface {
	// Next returns the next row, or io.EOF after the last one. Any
	// other error aborts the ingest.
	Next() (Row, error)

	// Close releases the underlying source.
	Close() error
}

// Row is one raw source row.
type Row struct {
	// Number is the 1-based data-row number within the source (header
	// rows excluded), used to report actionable validation errors.
	Number int

	// Record holds the row's raw column values.
	Record focus.RawRecord
}
