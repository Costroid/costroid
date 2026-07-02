// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package storage persists normalized FOCUS cost records behind a narrow
// interface (decision D5). The embedded DuckDB implementation is the
// default and only backend today; a scale-out backend (ClickHouse) will
// someday live behind the same interface.
package storage

import (
	"context"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/focus"
)

// Store is the storage interface (decision D5). It is deliberately sized
// to the current slice and grows per-slice — new query and lifecycle
// methods are added as features need them, never speculatively.
type Store interface {
	// ReplaceIngestBatch transactionally replaces the batch identified
	// by (batch.Connector, batch.SourceIdentity) with the given records:
	// prior records of that batch are deleted and the new ones inserted
	// in a single transaction, so re-ingesting a source never duplicates
	// and never leaves a partial load. When the stored batch already has
	// the same content hash and tenant, the store may short-circuit to a
	// no-op and report Unchanged.
	ReplaceIngestBatch(ctx context.Context, batch Batch, records []focus.CostRecord) (ReplaceResult, error)

	// DailyCostsByService returns, for one tenant, the total BilledCost
	// per UTC calendar day (of ChargePeriodStart) per ServiceName,
	// ordered days-ascending then service-name-ascending. A zero start
	// or end means unbounded on that side; a non-zero bound is an
	// inclusive calendar-day bound.
	DailyCostsByService(ctx context.Context, tenant string, start, end time.Time) (DailyCosts, error)

	// Close releases the underlying database. The embedded store is
	// single-writer (DuckDB): it must be closed before another process
	// can open the same data directory.
	Close() error
}

// Batch identifies and describes one ingest batch. The pair (Connector,
// SourceIdentity) is the replace key; ContentHash and TenantID are
// metadata recorded on the batch (see the ingest package for the pinned
// idempotency semantics).
type Batch struct {
	Connector      string
	SourceIdentity string
	ContentHash    string
	TenantID       string
}

// ReplaceResult reports what ReplaceIngestBatch did.
type ReplaceResult struct {
	// RecordCount is the number of records now stored for the batch.
	RecordCount int
	// Unchanged is true when the store short-circuited because the
	// already-stored batch has the same content hash and tenant.
	Unchanged bool
}

// DailyCosts is the result of DailyCostsByService.
type DailyCosts struct {
	// Currency is the single BillingCurrency of the matched records,
	// empty when no records matched. Mixed currencies are an error in
	// this slice (currency conversion is a later concern).
	Currency string
	// Days holds one entry per calendar day with data, ascending.
	Days []DayCosts
}

// DayCosts is one calendar day of per-service costs.
type DayCosts struct {
	// Date is the UTC midnight of the calendar day.
	Date time.Time
	// Services holds per-service BilledCost totals, service-name-ascending.
	Services []ServiceCost
}

// ServiceCost is the cost total of one service on one day.
type ServiceCost struct {
	ServiceName string
	Cost        decimal.Decimal
}
