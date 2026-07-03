// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package storage

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
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

// TestManifestAttributionRoundTrip proves the attribution cache
// (migration 0004) persists exactly — including the TIMESTAMP
// round-trip of both times at microsecond precision — and that
// upserting replaces rather than duplicates.
func TestManifestAttributionRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = store.Close() }()

	empty, err := store.ManifestAttributions(ctx, "azure-focus")
	if err != nil {
		t.Fatalf("ManifestAttributions on empty store: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty store holds attributions: %+v", empty)
	}

	a := ManifestAttribution{
		Connector:     "azure-focus",
		ManifestKey:   "acct.blob.core.windows.net/exports/costroid-demo/20260601-20260630/run-a/manifest.json",
		ETag:          `"0xabc123"`,
		LastModified:  time.Date(2026, 6, 5, 9, 19, 2, 0, time.UTC),
		Size:          2048,
		BillingPeriod: "2026-06",
		SubmittedTime: time.Date(2026, 6, 5, 9, 19, 1, 901_396_000, time.UTC), // µs precision
		ExportName:    "costroid-demo",
	}
	if err := store.UpsertManifestAttribution(ctx, a); err != nil {
		t.Fatalf("UpsertManifestAttribution: %v", err)
	}
	got, err := store.ManifestAttributions(ctx, "azure-focus")
	if err != nil {
		t.Fatalf("ManifestAttributions: %v", err)
	}
	stored, ok := got[a.ManifestKey]
	if !ok || len(got) != 1 {
		t.Fatalf("ManifestAttributions = %+v, want exactly %s", got, a.ManifestKey)
	}
	if stored.ETag != a.ETag || !stored.LastModified.Equal(a.LastModified) || stored.Size != a.Size ||
		stored.BillingPeriod != a.BillingPeriod || !stored.SubmittedTime.Equal(a.SubmittedTime) ||
		stored.ExportName != a.ExportName {
		t.Fatalf("stored attribution = %+v, want %+v", stored, a)
	}

	// Upserting the same key replaces.
	a.ETag = `"0xdef456"`
	a.SubmittedTime = a.SubmittedTime.Add(time.Hour)
	if err := store.UpsertManifestAttribution(ctx, a); err != nil {
		t.Fatalf("second UpsertManifestAttribution: %v", err)
	}
	got, err = store.ManifestAttributions(ctx, "azure-focus")
	if err != nil {
		t.Fatalf("ManifestAttributions after upsert: %v", err)
	}
	stored = got[a.ManifestKey]
	if len(got) != 1 || stored.ETag != `"0xdef456"` || !stored.SubmittedTime.Equal(a.SubmittedTime) {
		t.Fatalf("after upsert = %+v, want the replaced attribution", got)
	}

	// Other connectors see nothing.
	other, err := store.ManifestAttributions(ctx, "aws-focus-s3")
	if err != nil {
		t.Fatalf("ManifestAttributions(other): %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("other connector sees %+v, want nothing", other)
	}
}

// TestSyncStatesJoinBatchTenant proves SyncStates reports each source's
// stored batch tenant (joined from ingest_batches, empty without a
// batch) — the tenant-aware tuple skip (slice-3 review fix-up) depends
// on it.
func TestSyncStatesJoinBatchTenant(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = store.Close() }()

	st := SyncState{
		Connector:            "aws-focus-s3",
		SourceIdentity:       "demo/exports/costroid-demo/2026-05",
		ManifestKey:          "exports/costroid-demo/metadata/BILLING_PERIOD=2026-05/costroid-demo-Manifest.json",
		ManifestETag:         `"9e107d9d372bb6826bd81d3542a419d6"`,
		ManifestLastModified: time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC),
		ManifestSize:         1451,
	}
	if err := store.UpsertSyncState(ctx, st); err != nil {
		t.Fatalf("UpsertSyncState: %v", err)
	}

	// No stored batch yet: the joined tenant is empty, which no
	// requested tenant equals — the caller falls through to the hash path.
	states, err := store.SyncStates(ctx, "aws-focus-s3")
	if err != nil {
		t.Fatalf("SyncStates: %v", err)
	}
	if got := states[st.SourceIdentity].TenantID; got != "" {
		t.Fatalf("TenantID without a batch = %q, want empty", got)
	}

	// With a stored batch, the batch's tenant is reported.
	batch := Batch{
		Connector:      "aws-focus-s3",
		SourceIdentity: st.SourceIdentity,
		ContentHash:    "sha256:aaaa",
		TenantID:       "acme",
	}
	if _, err := store.ReplaceIngestBatch(ctx, batch, []focus.CostRecord{
		testRecord(t, "AWS Lambda", day(1), "0.1264"),
	}); err != nil {
		t.Fatalf("ReplaceIngestBatch: %v", err)
	}
	states, err = store.SyncStates(ctx, "aws-focus-s3")
	if err != nil {
		t.Fatalf("SyncStates: %v", err)
	}
	if got := states[st.SourceIdentity].TenantID; got != "acme" {
		t.Fatalf("TenantID = %q, want acme", got)
	}
}

// TestHelperHoldStore is not a real test: it is the child half of
// TestOpenLockedByAnotherProcessIsActionable, re-executed as a separate
// process that opens the store and holds it until its stdin closes.
func TestHelperHoldStore(t *testing.T) {
	dir := os.Getenv("COSTROID_TEST_HOLD_STORE_DIR")
	if dir == "" {
		t.Skip("helper for the cross-process lock test only")
	}
	store, err := Open(context.Background(), dir)
	if err != nil {
		fmt.Println("HELPER_OPEN_ERROR:", err)
		return
	}
	fmt.Println("HELPER_READY")
	_, _ = io.Copy(io.Discard, os.Stdin) // hold the store until the parent closes stdin
	_ = store.Close()
}

// TestOpenLockedByAnotherProcessIsActionable proves the single-writer
// classification CROSS-PROCESS: duckdb-go v2 keeps a process-global
// instance cache, so a same-process double-open of the same file
// succeeds by design and proves nothing — only a second process is
// refused the file lock. The second Open must return the actionable
// in-use message, not the raw DuckDB error (slice-3 review fix-up).
func TestOpenLockedByAnotherProcessIsActionable(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperHoldStore$", "-test.v")
	cmd.Env = append(os.Environ(), "COSTROID_TEST_HOLD_STORE_DIR="+dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting helper process: %v", err)
	}
	defer func() {
		_ = stdin.Close() // releases the helper's io.Copy, letting it close the store and exit
		_ = cmd.Wait()
	}()

	scanner := bufio.NewScanner(stdout)
	ready := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "HELPER_OPEN_ERROR") {
			t.Fatalf("helper process failed to open the store: %s", line)
		}
		if strings.Contains(line, "HELPER_READY") {
			ready = true
			break
		}
	}
	if !ready {
		t.Fatalf("helper process never reported the store open (scan error: %v)", scanner.Err())
	}

	_, err = Open(ctx, dir)
	if err == nil {
		t.Fatal("Open succeeded while another process holds the store")
	}
	for _, part := range []string{"in use by another process", "single process at a time", "stop the other"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("cross-process Open error %q does not contain %q", err, part)
		}
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
