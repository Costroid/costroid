// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package storage persists normalized FOCUS cost records behind a narrow
// interface (decision D5). The embedded DuckDB implementation is the
// default and only backend today; a scale-out backend (ClickHouse) will
// someday live behind the same interface.
//
// # Decimal capacity trade-off (decision D25)
//
// Money and quantity columns are DECIMAL(38,18): migration 0002 widened
// the original DECIMAL(38,12), trading integer capacity for fractional
// precision — 26 integer digits down to 20 — because FOCUS providers
// emit 16-fractional-digit unit prices while >20-integer-digit money or
// quantity values do not occur in practice. Values that exceed either
// bound are rejected at ingest with a row-numbered, column-named error;
// the store never rounds or truncates silently. A pre-existing stored
// value whose integer part would not fit the widened type makes
// migration 0002 itself fail — loudly and transactionally, leaving the
// store on the prior schema version — so an upgrade can abort but can
// never corrupt data.
package storage

import (
	"context"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/focus"
)

// MaxDecimalScale is the fractional-digit capacity of the store's
// monetary and quantity columns (DECIMAL(38,18), decision D25). Values
// with more significant fractional digits must be rejected at ingest
// time — the store never rounds silently (exactness invariant).
const MaxDecimalScale = 18

// CostGroupBy selects the FOCUS dimension used by DailyCostsByService.
type CostGroupBy int

const (
	// GroupByService groups daily costs by FOCUS ServiceName.
	GroupByService CostGroupBy = iota
	// GroupByProvider groups daily costs by FOCUS ServiceProviderName.
	GroupByProvider
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
	// per UTC calendar day (of ChargePeriodStart) per grouping key:
	// ServiceName by default, or ServiceProviderName when GroupByProvider
	// is passed. Results are ordered days-ascending then key-ascending. A
	// zero start or end means unbounded on that side; a non-zero bound is
	// an inclusive calendar-day bound.
	//
	// Time-series aggregation is by ChargePeriod, not BillingPeriod
	// (decision D26c): FOCUS correction rows (ChargeClass="Correction")
	// are delivered in a LATER open billing period but keep the ORIGINAL
	// incurred timeframe in ChargePeriodStart/End, so ingesting a
	// correction retroactively adjusts the corrected days. Cost history
	// legitimately changes when providers issue corrections — this is
	// intended, documented, and tested behavior.
	DailyCostsByService(ctx context.Context, tenant string, start, end time.Time, groupBy ...CostGroupBy) (DailyCosts, error)

	// DailyTokensByService returns, for one tenant, the total
	// ConsumedQuantity per UTC calendar day (of ChargePeriodStart) per
	// (ServiceName, ConsumedUnit), ordered day-ascending then
	// service-name-ascending then consumed-unit-ascending. It is scoped to
	// token usage: only rows whose ConsumedUnit is "Tokens" (the FOCUS 1.4
	// UnitFormat token unit, decision D33) and whose ConsumedQuantity is
	// non-null contribute, so a money-only row never surfaces with a
	// fabricated or zero quantity. Its twin DailyCostsByService is on this
	// interface; this token query belongs beside it. Aggregation is by
	// ChargePeriod (decision D26c), like DailyCostsByService.
	DailyTokensByService(ctx context.Context, tenant string, start, end time.Time) ([]DailyTokenUsage, error)

	// ReplaceUsageBatch transactionally replaces the usage_metrics rows of
	// the batch identified by (batch.Connector, batch.SourceIdentity): prior
	// rows of that batch are deleted and the given metrics inserted in one
	// transaction. So a re-delivered or corrected month WHOLLY SUPERSEDES its
	// prior usage rows (decision D26a) and re-syncing unchanged content yields
	// identical rows (idempotent). It is called only after that period's cost
	// ingest succeeds and with the SAME identity as the cost batch, and it is
	// called even when metrics is empty (so a month whose orphans vanished
	// clears its stale rows). Quantities go through the same scale-bound CAST
	// as the cost insert (decision D25) — never silently rounded. usage_metrics
	// is a separate table from cost_records: this never touches any BilledCost
	// or token total.
	ReplaceUsageBatch(ctx context.Context, batch UsageBatch, metrics []Metric) error

	// DailyUsageMetrics returns, for one tenant, the summed quantity per UTC
	// calendar day (of ChargePeriodStart) per (ServiceName, ServiceTier,
	// MetricName, Unit), ordered by a fully-deterministic key. Different units
	// never merge (Tokens vs Requests vs Unknown) AND different metric names
	// within one unit never merge — the GROUP BY is a two-dimension guard;
	// grouping (not erroring) is correct because these are counts, not money. A
	// zero start or end means unbounded on that side; a non-zero bound is an
	// inclusive calendar-day bound. It reads FROM usage_metrics only, so it is
	// isolated from DailyCostsByService/DailyTokensByService (which read
	// cost_records). ServiceTier is the stored "" when the vendor has no tier —
	// never null.
	DailyUsageMetrics(ctx context.Context, tenant string, start, end time.Time) ([]DailyUsageMetric, error)

	// SyncStates returns one connector's stored sync tuples, keyed by
	// source identity (see SyncState).
	SyncStates(ctx context.Context, connector string) (map[string]SyncState, error)

	// UpsertSyncState records a source's sync tuple, replacing any
	// stored one. Callers upsert after EVERY successful sync outcome —
	// fresh ingest, replace, unchanged short-circuit, and forced runs —
	// otherwise a single touched-but-identical delivery would leave a
	// stale tuple and permanently defeat the skip.
	UpsertSyncState(ctx context.Context, state SyncState) error

	// ManifestAttributions returns one connector's cached manifest
	// attributions, keyed by manifest key (see ManifestAttribution).
	ManifestAttributions(ctx context.Context, connector string) (map[string]ManifestAttribution, error)

	// UpsertManifestAttribution caches one manifest blob's attribution,
	// replacing any stored one for the same (connector, manifest key).
	UpsertManifestAttribution(ctx context.Context, attr ManifestAttribution) error

	// PutCredential stores (or replaces) one encrypted credential slot by
	// name (decision D32). It persists only the opaque nonce and ciphertext
	// — never the encryption key or any plaintext. Replacing a slot keeps
	// its original CreatedAt.
	PutCredential(ctx context.Context, cred Credential) error

	// GetCredential returns one credential slot's stored nonce and
	// ciphertext by name; found is false when no such slot exists.
	GetCredential(ctx context.Context, name string) (cred Credential, found bool, err error)

	// ListCredentials returns every slot's name and timestamps (never any
	// secret material), name-ascending.
	ListCredentials(ctx context.Context) ([]CredentialInfo, error)

	// DeleteCredential removes one credential slot by name; deleted is
	// false when no such slot existed.
	DeleteCredential(ctx context.Context, name string) (deleted bool, err error)

	// Close releases the underlying database. The embedded store is
	// single-writer (DuckDB): it must be closed before another process
	// can open the same data directory.
	Close() error
}

// SyncState is one source's incremental-sync tuple (decision D16,
// "incremental"): the S3 listing metadata of the source's partition-level
// manifest as of the last successful sync. A billing period whose listed
// (key, ETag, LastModified, size) tuple equals the stored one is skipped
// without fetching anything — zero GetObject calls. LastModified is the
// load-bearing change signal (S3 ETags are NOT content digests under
// SSE-KMS or multipart upload); the key, ETag, and size corroborate.
// Local-file connectors keep no sync state: reading a local file has no
// fetch cost to save, and the pipeline's content-hash short-circuit
// already makes their re-ingest a no-op.
type SyncState struct {
	Connector            string
	SourceIdentity       string
	ManifestKey          string
	ManifestETag         string
	ManifestLastModified time.Time
	ManifestSize         int64

	// TenantID is the tenant recorded on the source's stored ingest
	// batch (empty when no batch is stored). It is not a column of the
	// sync tuple — SyncStates joins it from ingest_batches — and exists
	// so callers can make the tuple skip tenant-aware: a sync targeting
	// a different tenant must NOT skip an unchanged source, because the
	// stored records would stay homed under the old tenant. Such sources
	// fall through to the content-hash path, whose tenant-sensitive
	// short-circuit re-homes the batch (see ReplaceIngestBatch).
	TenantID string
}

// ManifestAttribution is one manifest blob's cached attribution
// (migration 0004): the blob's listed change-detection tuple (ETag,
// LastModified, size) mapped to the billing period and run submission
// time its body declares. Export manifests are immutable once written —
// a refresh writes a new run folder — so once a manifest has been
// fetched its attribution is permanent: a listed manifest whose tuple
// matches the cached row needs no fetch at all. That keeps an unchanged
// re-sync at zero Get Blob calls even when superseded runs' manifests
// remain listed forever (Azure's CreateNewReport mode). A cache row
// whose manifest disappears from the listing goes stale harmlessly.
type ManifestAttribution struct {
	Connector string
	// ManifestKey uniquely identifies the manifest blob within the
	// connector, e.g. "<account-host>/<container>/<blob path>".
	ManifestKey  string
	ETag         string
	LastModified time.Time
	// Size is the blob's listed Content-Length in bytes.
	Size int64
	// BillingPeriod is the "YYYY-MM" the manifest's run covers, derived
	// from the manifest body's run start date.
	BillingPeriod string
	// SubmittedTime is when the manifest's export run was submitted; the
	// run with the latest SubmittedTime is a period's current one.
	SubmittedTime time.Time
	// ExportName is the export the manifest belongs to
	// (exportConfig.exportName). Two different exports delivering under
	// one prefix would silently replace each other's data per period, so
	// discovery refuses such periods — which requires knowing each
	// manifest's export without re-fetching it.
	ExportName string
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

// Credential is one encrypted credential slot at rest (decision D32,
// migration 0005): the slot name, the AES-256-GCM nonce and ciphertext,
// and its timestamps. The struct never holds plaintext or the encryption
// key — decryption happens in the credentials package, which owns the key
// file. The credential NAME is the GCM additional authenticated data, so a
// ciphertext moved to a different name fails to decrypt.
type Credential struct {
	Name       string
	Nonce      []byte
	Ciphertext []byte
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// CredentialInfo is the non-secret metadata of one credential slot, all
// that `credentials list` may reveal (decision D32).
type CredentialInfo struct {
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ReplaceResult reports what ReplaceIngestBatch did.
type ReplaceResult struct {
	// RecordCount is the number of records now stored for the batch.
	RecordCount int
	// Unchanged is true when the store short-circuited because the
	// already-stored batch has the same content hash and tenant.
	Unchanged bool
	// Replaced is true when a previously stored batch with different
	// content was replaced (false on a first ingest and on an Unchanged
	// short-circuit). Restatement visibility (decision D26d) hangs off
	// this: a replaced period's cost delta is user-facing information.
	Replaced bool
	// PreviousBilledCost is the replaced batch's total BilledCost before
	// the replace; meaningful only when Replaced is true.
	PreviousBilledCost decimal.Decimal
	// NewBilledCost is the batch's total BilledCost now stored.
	NewBilledCost decimal.Decimal
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

// DayCosts is one calendar day of costs by grouping key.
type DayCosts struct {
	// Date is the UTC midnight of the calendar day.
	Date time.Time
	// Services holds BilledCost totals by grouping key, key-ascending.
	Services []ServiceCost
}

// ServiceCost is the cost total of one grouping key on one day.
type ServiceCost struct {
	// ServiceName carries the grouping key: FOCUS ServiceName by default,
	// or FOCUS ServiceProviderName when GroupByProvider is requested.
	ServiceName string
	Cost        decimal.Decimal
}

// DailyTokenUsage is one row of DailyTokensByService: the total
// ConsumedQuantity of one service's one unit on one UTC calendar day.
// Quantity is an exact decimal (never float64, decisions D23/D25).
type DailyTokenUsage struct {
	// Date is the UTC midnight of the calendar day.
	Date time.Time
	// ServiceName is the FOCUS ServiceName.
	ServiceName string
	// ConsumedUnit is the FOCUS ConsumedUnit (e.g. "Tokens").
	ConsumedUnit string
	// Quantity is the summed ConsumedQuantity, exact.
	Quantity decimal.Decimal
}

// UsageBatch identifies the usage_metrics rows one AI-vendor month replaces:
// the (Connector, SourceIdentity) pair is the per-month replace key and
// TenantID homes the rows (decision D15). It is the SAME identity as that
// month's cost batch, so a month's usage metrics and its cost records supersede
// together. ContentHash is not needed here — the caller only writes after the
// cost ingest already decided the month changed.
type UsageBatch struct {
	Connector      string
	SourceIdentity string
	TenantID       string
}

// Metric is one cost-orphaned usage-metric row an AI-vendor connector surfaces
// for persistence — the neutral currency between connector and store, mirroring
// how focus.CostRecord is shared. Cardinal Rule (decision D7): every field is
// count or categorical metadata (the UTC usage day, service/model name, service
// tier, a metric name, a unit, an exact quantity) — no content, no
// content-derived field. Connector, SourceIdentity, and tenant are NOT on this
// struct; ReplaceUsageBatch binds them from its UsageBatch descriptor.
type Metric struct {
	// ChargePeriodStart is the UTC midnight of the usage-bucket day.
	ChargePeriodStart time.Time
	// ServiceName is the FOCUS ServiceName / model identity.
	ServiceName string
	// ServiceTier is the vendor service tier, or "" when the vendor has no
	// tier concept (OpenAI) — bound as "" (never SQL NULL) so the NOT NULL
	// column and DailyUsageMetrics's string scan hold.
	ServiceTier string
	// MetricName is the frozen metric identifier: a token_type, the literal
	// "web_search_requests", an opaque OpenAI cost line_item (USG-3), one of the
	// OpenAI usage-endpoint field names "num_model_requests" / "images" /
	// "characters" / "seconds" / "num_sessions" / "usage_bytes", or the
	// source-qualified OpenAI search-call metrics "web_search_num_requests" /
	// "file_search_num_requests" (endpoint-disambiguated because usage_metrics
	// has no endpoint dimension).
	MetricName string
	// Unit is the frozen unit vocabulary: "Tokens", "Requests", "Unknown",
	// "Images", "Characters", "Seconds", "Sessions", "Bytes", or "Calls".
	// (unit is a plain string column — this vocabulary is the canonical source;
	// migration 0006's own "USG-1..USG-3" comment is intentionally left stale,
	// migrations being frozen.)
	Unit string
	// Quantity is the exact metric quantity (never float64).
	Quantity decimal.Decimal
}

// DailyUsageMetric is one row of DailyUsageMetrics: the summed quantity of one
// (ServiceName, ServiceTier, MetricName, Unit) on one UTC calendar day.
// Quantity is an exact decimal (never float64). ServiceTier is the stored ""
// when the vendor has no tier, never null.
type DailyUsageMetric struct {
	// Date is the UTC midnight of the calendar day.
	Date        time.Time
	ServiceName string
	ServiceTier string
	MetricName  string
	Unit        string
	Quantity    decimal.Decimal
}

// AIRow is the enrichment-relevant projection of one stored cost record,
// returned by DuckDB.EnrichedAIRows for verifying token-quantity enrichment
// (decision D33) landed without disturbing money. It is not a query surface —
// no API endpoint consumes it — only a store-level assertion helper.
type AIRow struct {
	ChargeDescription string
	SkuID             string
	SkuPriceID        string
	SkuMeter          string
	ConsumedQuantity  decimal.NullDecimal
	ConsumedUnit      string
	PricingQuantity   decimal.NullDecimal
	PricingUnit       string
	BilledCost        decimal.Decimal
}
