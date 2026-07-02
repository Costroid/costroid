// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/focus"
)

func testRecord(t *testing.T, service string, day time.Time, cost string) focus.CostRecord {
	t.Helper()
	d, err := decimal.NewFromString(cost)
	if err != nil {
		t.Fatalf("bad test cost %q: %v", cost, err)
	}
	return focus.CostRecord{
		XTenantID:           focus.DefaultTenant,
		BillingAccountID:    "999999999999",
		ServiceProviderName: "AWS",
		HostProviderName:    "AWS",
		InvoiceIssuerName:   "Amazon Web Services, Inc.",
		BillingCurrency:     "USD",
		BillingPeriodStart:  time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		BillingPeriodEnd:    time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		ChargeCategory:      "Usage",
		ChargePeriodStart:   day,
		ChargePeriodEnd:     day.AddDate(0, 0, 1),
		BilledCost:          d,
		EffectiveCost:       d,
		ListCost:            d,
		ContractedCost:      d,
		ServiceName:         service,
		ServiceCategory:     "Compute",
		Tags:                map[string]any{"user:team": "platform", "user:opted-in": true},
	}
}

func day(d int) time.Time { return time.Date(2026, 5, d, 0, 0, 0, 0, time.UTC) }

func TestOpenAppliesMigrationsOnceAndReopenIsNoOp(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	versions := appliedMigrations(t, store)
	if len(versions) == 0 || versions[0] != "0001_create_cost_tables.sql" {
		t.Fatalf("applied migrations = %v, want 0001_create_cost_tables.sql first", versions)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopening an up-to-date store is a no-op: same recorded versions,
	// no error, tables usable.
	store, err = Open(ctx, dir)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer func() { _ = store.Close() }()
	reVersions := appliedMigrations(t, store)
	if len(reVersions) != len(versions) {
		t.Fatalf("after reopen applied migrations = %v, want %v", reVersions, versions)
	}
	if _, err := store.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{}); err != nil {
		t.Fatalf("querying after reopen: %v", err)
	}
}

func appliedMigrations(t *testing.T, store *DuckDB) []string {
	t.Helper()
	rows, err := store.db.Query(`SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("reading schema_migrations: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var versions []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scanning version: %v", err)
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading schema_migrations: %v", err)
	}
	return versions
}

func TestReplaceIngestBatchAndDailyCosts(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = store.Close() }()

	batch := Batch{
		Connector:      "aws-focus",
		SourceIdentity: "sample-export.csv.gz",
		ContentHash:    "sha256:aaaa",
		TenantID:       focus.DefaultTenant,
	}
	records := []focus.CostRecord{
		testRecord(t, "Amazon Elastic Compute Cloud", day(2), "2.4192"),
		testRecord(t, "Amazon Elastic Compute Cloud", day(1), "1.2096"),
		testRecord(t, "AWS Lambda", day(1), "0.1264"),
		testRecord(t, "Amazon Elastic Compute Cloud", day(1), "2.4192"),
	}
	res, err := store.ReplaceIngestBatch(ctx, batch, records)
	if err != nil {
		t.Fatalf("ReplaceIngestBatch: %v", err)
	}
	if res.Unchanged || res.RecordCount != 4 {
		t.Fatalf("ReplaceIngestBatch = %+v, want 4 records, not unchanged", res)
	}

	got, err := store.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyCostsByService: %v", err)
	}
	if got.Currency != "USD" {
		t.Errorf("Currency = %q, want USD", got.Currency)
	}
	// Deterministic ordering: days ascending, then service names ascending;
	// same-service same-day records are summed exactly.
	want := DailyCosts{Currency: "USD", Days: []DayCosts{
		{Date: day(1), Services: []ServiceCost{
			{ServiceName: "AWS Lambda", Cost: dec(t, "0.1264")},
			{ServiceName: "Amazon Elastic Compute Cloud", Cost: dec(t, "3.6288")},
		}},
		{Date: day(2), Services: []ServiceCost{
			{ServiceName: "Amazon Elastic Compute Cloud", Cost: dec(t, "2.4192")},
		}},
	}}
	assertDailyCosts(t, got, want)

	// Range bounds are inclusive calendar days.
	ranged, err := store.DailyCostsByService(ctx, focus.DefaultTenant, day(2), day(2))
	if err != nil {
		t.Fatalf("DailyCostsByService(ranged): %v", err)
	}
	if len(ranged.Days) != 1 || !ranged.Days[0].Date.Equal(day(2)) {
		t.Errorf("ranged days = %+v, want only %s", ranged.Days, day(2))
	}

	// Records of another tenant are invisible (D15).
	other, err := store.DailyCostsByService(ctx, "someone-else", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyCostsByService(other tenant): %v", err)
	}
	if len(other.Days) != 0 || other.Currency != "" {
		t.Errorf("other tenant sees %+v, want nothing", other)
	}
}

func TestReplaceIngestBatchIdempotency(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = store.Close() }()

	batch := Batch{Connector: "aws-focus", SourceIdentity: "export.csv.gz", ContentHash: "sha256:aaaa", TenantID: focus.DefaultTenant}
	records := []focus.CostRecord{
		testRecord(t, "AWS Lambda", day(1), "0.1264"),
		testRecord(t, "AWS Lambda", day(2), "0.0632"),
	}
	first, err := store.ReplaceIngestBatch(ctx, batch, records)
	if err != nil {
		t.Fatalf("first ReplaceIngestBatch: %v", err)
	}
	if first.Replaced || first.NewBilledCost.String() != "0.1896" {
		t.Fatalf("first ReplaceIngestBatch = %+v, want fresh (not replaced) with new total 0.1896", first)
	}

	// Same key, same content hash: short-circuits to a no-op.
	res, err := store.ReplaceIngestBatch(ctx, batch, records)
	if err != nil {
		t.Fatalf("unchanged ReplaceIngestBatch: %v", err)
	}
	if !res.Unchanged || res.Replaced || res.RecordCount != 2 {
		t.Fatalf("unchanged ReplaceIngestBatch = %+v, want unchanged with 2 records", res)
	}

	// Same key, changed content: replaces — never duplicates — and
	// reports the batch's BilledCost totals before → after (D26d).
	changed := batch
	changed.ContentHash = "sha256:bbbb"
	res, err = store.ReplaceIngestBatch(ctx, changed, records[:1])
	if err != nil {
		t.Fatalf("changed ReplaceIngestBatch: %v", err)
	}
	if res.Unchanged || res.RecordCount != 1 {
		t.Fatalf("changed ReplaceIngestBatch = %+v, want replace with 1 record", res)
	}
	if !res.Replaced || res.PreviousBilledCost.String() != "0.1896" || res.NewBilledCost.String() != "0.1264" {
		t.Fatalf("changed ReplaceIngestBatch = %+v, want replaced with BilledCost 0.1896 → 0.1264", res)
	}
	got, err := store.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyCostsByService: %v", err)
	}
	want := DailyCosts{Currency: "USD", Days: []DayCosts{
		{Date: day(1), Services: []ServiceCost{{ServiceName: "AWS Lambda", Cost: dec(t, "0.1264")}}},
	}}
	assertDailyCosts(t, got, want)

	// A different source identity is a different batch: stored side by
	// side (overlap handling is the correction machinery of a later slice).
	batch2 := Batch{Connector: "aws-focus", SourceIdentity: "export-2.csv.gz", ContentHash: "sha256:cccc", TenantID: focus.DefaultTenant}
	if _, err := store.ReplaceIngestBatch(ctx, batch2, records[1:]); err != nil {
		t.Fatalf("second batch ReplaceIngestBatch: %v", err)
	}
	got, err = store.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyCostsByService: %v", err)
	}
	if len(got.Days) != 2 {
		t.Fatalf("after second batch, days = %+v, want 2 days", got.Days)
	}
}

// TestSyncStateRoundTrip proves the sync-state tuple (migration 0003)
// persists exactly — including the TIMESTAMP round-trip of LastModified —
// and that upserting replaces rather than duplicates.
func TestSyncStateRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = store.Close() }()

	empty, err := store.SyncStates(ctx, "aws-focus-s3")
	if err != nil {
		t.Fatalf("SyncStates on empty store: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty store holds sync states: %+v", empty)
	}

	st := SyncState{
		Connector:            "aws-focus-s3",
		SourceIdentity:       "demo/exports/costroid-demo/2026-05",
		ManifestKey:          "exports/costroid-demo/metadata/BILLING_PERIOD=2026-05/costroid-demo-Manifest.json",
		ManifestETag:         `"9e107d9d372bb6826bd81d3542a419d6"`,
		ManifestLastModified: time.Date(2026, 7, 2, 12, 0, 0, 123_000_000, time.UTC), // ms precision, as S3 lists
		ManifestSize:         1451,
	}
	if err := store.UpsertSyncState(ctx, st); err != nil {
		t.Fatalf("UpsertSyncState: %v", err)
	}
	got, err := store.SyncStates(ctx, "aws-focus-s3")
	if err != nil {
		t.Fatalf("SyncStates: %v", err)
	}
	stored, ok := got[st.SourceIdentity]
	if !ok || len(got) != 1 {
		t.Fatalf("SyncStates = %+v, want exactly %s", got, st.SourceIdentity)
	}
	if stored.ManifestKey != st.ManifestKey || stored.ManifestETag != st.ManifestETag ||
		!stored.ManifestLastModified.Equal(st.ManifestLastModified) || stored.ManifestSize != st.ManifestSize {
		t.Fatalf("stored tuple = %+v, want %+v", stored, st)
	}

	// Upserting the same identity replaces the tuple.
	st.ManifestETag = `"new"`
	st.ManifestLastModified = st.ManifestLastModified.Add(time.Hour)
	st.ManifestSize = 999
	if err := store.UpsertSyncState(ctx, st); err != nil {
		t.Fatalf("second UpsertSyncState: %v", err)
	}
	got, err = store.SyncStates(ctx, "aws-focus-s3")
	if err != nil {
		t.Fatalf("SyncStates after upsert: %v", err)
	}
	stored = got[st.SourceIdentity]
	if len(got) != 1 || stored.ManifestETag != `"new"` ||
		!stored.ManifestLastModified.Equal(st.ManifestLastModified) || stored.ManifestSize != 999 {
		t.Fatalf("after upsert SyncStates = %+v, want the replaced tuple", got)
	}

	// Other connectors see nothing.
	other, err := store.SyncStates(ctx, "aws-focus")
	if err != nil {
		t.Fatalf("SyncStates(other): %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("other connector sees %+v, want nothing", other)
	}
}

func dec(t *testing.T, s string) decimal.Decimal {
	t.Helper()
	d, err := decimal.NewFromString(s)
	if err != nil {
		t.Fatalf("bad decimal %q: %v", s, err)
	}
	return d
}

func assertDailyCosts(t *testing.T, got, want DailyCosts) {
	t.Helper()
	if got.Currency != want.Currency {
		t.Errorf("Currency = %q, want %q", got.Currency, want.Currency)
	}
	if len(got.Days) != len(want.Days) {
		t.Fatalf("Days = %+v, want %d days", got.Days, len(want.Days))
	}
	for i, wantDay := range want.Days {
		gotDay := got.Days[i]
		if !gotDay.Date.Equal(wantDay.Date) {
			t.Errorf("day %d date = %s, want %s", i, gotDay.Date, wantDay.Date)
		}
		if len(gotDay.Services) != len(wantDay.Services) {
			t.Fatalf("day %d services = %+v, want %d services", i, gotDay.Services, len(wantDay.Services))
		}
		for j, wantSvc := range wantDay.Services {
			gotSvc := gotDay.Services[j]
			if gotSvc.ServiceName != wantSvc.ServiceName {
				t.Errorf("day %d service %d = %q, want %q", i, j, gotSvc.ServiceName, wantSvc.ServiceName)
			}
			if !gotSvc.Cost.Equal(wantSvc.Cost) {
				t.Errorf("day %d service %s cost = %s, want %s", i, wantSvc.ServiceName, gotSvc.Cost, wantSvc.Cost)
			}
		}
	}
}
