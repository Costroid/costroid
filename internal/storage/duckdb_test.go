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
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

const mixedCurrencyGuardError = "stored records mix billing currencies (EUR, USD); currency conversion is not supported yet"

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
	if _, err := store.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{}, ""); err != nil {
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

	got, err := store.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{}, "")
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
	ranged, err := store.DailyCostsByService(ctx, focus.DefaultTenant, day(2), day(2), "")
	if err != nil {
		t.Fatalf("DailyCostsByService(ranged): %v", err)
	}
	if len(ranged.Days) != 1 || !ranged.Days[0].Date.Equal(day(2)) {
		t.Errorf("ranged days = %+v, want only %s", ranged.Days, day(2))
	}

	// Records of another tenant are invisible (D15).
	other, err := store.DailyCostsByService(ctx, "someone-else", time.Time{}, time.Time{}, "")
	if err != nil {
		t.Fatalf("DailyCostsByService(other tenant): %v", err)
	}
	if len(other.Days) != 0 || other.Currency != "" {
		t.Errorf("other tenant sees %+v, want nothing", other)
	}
}

func TestBillingCurrenciesSortedSingleAndEmptyNonNil(t *testing.T) {
	eur := testRecord(t, "EUR service", day(2), "2")
	eur.BillingCurrency = "EUR"
	jpy := testRecord(t, "JPY service", day(3), "3")
	jpy.BillingCurrency = "JPY"
	otherTenant := testRecord(t, "GBP service", day(2), "4")
	otherTenant.BillingCurrency = "GBP"
	otherTenant.XTenantID = "acme"
	store := allocStore(t,
		testRecord(t, "USD service", day(1), "1"),
		eur,
		jpy,
		otherTenant,
	)

	got, err := store.BillingCurrencies(context.Background(), focus.DefaultTenant, day(1), day(2))
	if err != nil {
		t.Fatalf("BillingCurrencies(mixed range): %v", err)
	}
	if joined := strings.Join(got, ","); joined != "EUR,USD" {
		t.Fatalf("BillingCurrencies(mixed range) = %q, want sorted EUR,USD", joined)
	}

	single, err := store.BillingCurrencies(context.Background(), focus.DefaultTenant, day(2), day(2))
	if err != nil {
		t.Fatalf("BillingCurrencies(single range): %v", err)
	}
	if len(single) != 1 || single[0] != "EUR" {
		t.Fatalf("BillingCurrencies(single range) = %v, want [EUR]", single)
	}

	empty, err := store.BillingCurrencies(context.Background(), focus.DefaultTenant, day(4), day(4))
	if err != nil {
		t.Fatalf("BillingCurrencies(empty range): %v", err)
	}
	if empty == nil {
		t.Fatal("BillingCurrencies(empty range) returned nil, want a non-nil empty slice")
	}
	if len(empty) != 0 {
		t.Fatalf("BillingCurrencies(empty range) = %v, want []", empty)
	}
}

func TestDailyCostsByServiceCurrencyFilterExactExclusiveAndAbsentEcho(t *testing.T) {
	eurOne := testRecord(t, "shared service", day(1), "0.111111111111111111")
	eurOne.BillingCurrency = "EUR"
	eurTwo := testRecord(t, "shared service", day(1), "0.222222222222222222")
	eurTwo.BillingCurrency = "EUR"
	usd := testRecord(t, "shared service", day(1), "9.999999999999999999")
	store := allocStore(t, usd, eurTwo, eurOne)

	got, err := store.DailyCostsByService(context.Background(), focus.DefaultTenant, time.Time{}, time.Time{}, "EUR")
	if err != nil {
		t.Fatalf("DailyCostsByService(EUR): %v", err)
	}
	if got.Currency != "EUR" || len(got.Days) != 1 || !got.Days[0].Date.Equal(day(1)) ||
		len(got.Days[0].Services) != 1 || got.Days[0].Services[0].ServiceName != "shared service" {
		t.Fatalf("DailyCostsByService(EUR) shape = %+v, want one EUR day with only shared service", got)
	}
	if dayTotal := got.Days[0].Services[0].Cost.String(); dayTotal != "0.333333333333333333" {
		t.Fatalf("EUR day total = %s, want exact 0.333333333333333333 (USD leaked)", dayTotal)
	}

	absent, err := store.DailyCostsByService(context.Background(), focus.DefaultTenant, time.Time{}, time.Time{}, "GBP")
	if err != nil {
		t.Fatalf("DailyCostsByService(GBP): %v", err)
	}
	if absent.Currency != "GBP" || len(absent.Days) != 0 {
		t.Fatalf("DailyCostsByService(GBP) = %+v, want Currency GBP and no days", absent)
	}
}

func TestDailyCostsByServiceGroupsByProviderExactly(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = store.Close() }()

	withProvider := func(r focus.CostRecord, provider string) focus.CostRecord {
		r.ServiceProviderName = provider
		r.HostProviderName = provider
		r.InvoiceIssuerName = provider
		return r
	}

	batch := Batch{
		Connector:      "multi-source",
		SourceIdentity: "provider-exactness",
		ContentHash:    "sha256:provider",
		TenantID:       focus.DefaultTenant,
	}
	records := []focus.CostRecord{
		withProvider(testRecord(t, "GPT-4o input tokens", day(1), "0.111111111111111111"), "OpenAI"),
		withProvider(testRecord(t, "GPT-4o output tokens", day(1), "0.222222222222222222"), "OpenAI"),
		withProvider(testRecord(t, "Amazon Elastic Compute Cloud", day(1), "1.000000000000000001"), "Amazon Web Services"),
	}
	if _, err := store.ReplaceIngestBatch(ctx, batch, records); err != nil {
		t.Fatalf("ReplaceIngestBatch: %v", err)
	}

	noArg, err := store.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{}, "")
	if err != nil {
		t.Fatalf("DailyCostsByService(no arg): %v", err)
	}
	wantService := DailyCosts{Currency: "USD", Days: []DayCosts{
		{Date: day(1), Services: []ServiceCost{
			{ServiceName: "Amazon Elastic Compute Cloud", Cost: dec(t, "1.000000000000000001")},
			{ServiceName: "GPT-4o input tokens", Cost: dec(t, "0.111111111111111111")},
			{ServiceName: "GPT-4o output tokens", Cost: dec(t, "0.222222222222222222")},
		}},
	}}
	assertDailyCosts(t, noArg, wantService)

	byService, err := store.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{}, "", GroupByService)
	if err != nil {
		t.Fatalf("DailyCostsByService(GroupByService): %v", err)
	}
	assertDailyCosts(t, byService, wantService)

	byProvider, err := store.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{}, "", GroupByProvider)
	if err != nil {
		t.Fatalf("DailyCostsByService(GroupByProvider): %v", err)
	}
	wantProvider := DailyCosts{Currency: "USD", Days: []DayCosts{
		{Date: day(1), Services: []ServiceCost{
			{ServiceName: "Amazon Web Services", Cost: dec(t, "1.000000000000000001")},
			{ServiceName: "OpenAI", Cost: dec(t, "0.333333333333333333")},
		}},
	}}
	assertDailyCosts(t, byProvider, wantProvider)
	if got := byProvider.Days[0].Services[1].Cost.String(); got != "0.333333333333333333" {
		t.Fatalf("OpenAI provider sum = %s, want exact 0.333333333333333333", got)
	}

	eur := withProvider(testRecord(t, "Azure Functions", day(1), "0.50"), "Microsoft")
	eur.BillingCurrency = "EUR"
	if _, err := store.ReplaceIngestBatch(ctx, Batch{
		Connector:      "azure-focus",
		SourceIdentity: "provider-currency-mix",
		ContentHash:    "sha256:eur",
		TenantID:       focus.DefaultTenant,
	}, []focus.CostRecord{eur}); err != nil {
		t.Fatalf("ReplaceIngestBatch(EUR): %v", err)
	}
	if _, err := store.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{}, "", GroupByProvider); err == nil || err.Error() != mixedCurrencyGuardError {
		t.Fatalf("DailyCostsByService(GroupByProvider) currency mix error = %v, want %q", err, mixedCurrencyGuardError)
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
	got, err := store.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{}, "")
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
	got, err = store.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{}, "")
	if err != nil {
		t.Fatalf("DailyCostsByService: %v", err)
	}
	if len(got.Days) != 2 {
		t.Fatalf("after second batch, days = %+v, want 2 days", got.Days)
	}
}

func TestSyncRunRecordingAndRetention(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	base := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)

	success := SyncRun{
		SourceName: "healthy", Connector: "focus-csv", TenantID: "default",
		StartedAt: base, FinishedAt: base.Add(time.Minute), Outcome: "success",
		PeriodsProcessed: 2, PeriodsSkipped: 1, RecordsIngested: 7,
	}
	if err := store.RecordSyncRun(ctx, success); err != nil {
		t.Fatalf("RecordSyncRun(success): %v", err)
	}
	longError := strings.Repeat("x", 1200)
	if err := store.RecordSyncRun(ctx, SyncRun{
		SourceName: "failed", Connector: "focus-csv", TenantID: "default",
		StartedAt: base, FinishedAt: base.Add(time.Second), Outcome: "error", Error: longError,
	}); err != nil {
		t.Fatalf("RecordSyncRun(error): %v", err)
	}
	if err := store.RecordSyncRun(ctx, SyncRun{
		SourceName: "mixed", Connector: "focus-csv", TenantID: "default",
		StartedAt: base, FinishedAt: base.Add(2 * time.Second), Outcome: "partial", Error: "one period failed",
		PeriodsProcessed: 1, RecordsIngested: 2,
	}); err != nil {
		t.Fatalf("RecordSyncRun(partial): %v", err)
	}

	statuses, err := store.SyncStatuses(ctx)
	if err != nil {
		t.Fatalf("SyncStatuses: %v", err)
	}
	byName := map[string]SyncStatus{}
	for _, status := range statuses {
		byName[status.Latest.SourceName] = status
	}
	got := byName["healthy"]
	if got.Latest.Outcome != "success" || got.Latest.PeriodsProcessed != 2 || got.Latest.PeriodsSkipped != 1 || got.Latest.RecordsIngested != 7 {
		t.Fatalf("healthy status = %+v", got)
	}
	if got.LastSuccessAt == nil || !got.LastSuccessAt.Equal(success.FinishedAt) {
		t.Fatalf("healthy last success = %v, want %s", got.LastSuccessAt, success.FinishedAt)
	}
	if got := byName["failed"].Latest.Error; len(got) != 1000 || got != longError[:1000] {
		t.Fatalf("failed error bytes = %d, want exactly 1000", len(got))
	}
	if got := byName["mixed"].Latest; got.Outcome != "partial" || got.Error != "one period failed" {
		t.Fatalf("mixed status = %+v", got)
	}

	for i := 0; i < 51; i++ {
		started := base.Add(time.Duration(i) * time.Minute)
		if err := store.RecordSyncRun(ctx, SyncRun{
			SourceName: "retained", Connector: "aws-focus", TenantID: "acme",
			StartedAt: started, FinishedAt: started.Add(time.Second), Outcome: "success",
		}); err != nil {
			t.Fatalf("RecordSyncRun(retained %d): %v", i, err)
		}
	}
	var count int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sync_runs WHERE source_name = 'retained' AND tenant_id = 'acme'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 50 {
		t.Fatalf("retained rows = %d, want 50 after the 51st insert", count)
	}
}

// TestSyncRunErrorTruncationRuneSafe pins that the 1000-byte error bound
// never splits a multi-byte UTF-8 rune: DuckDB rejects invalid UTF-8, and a
// rejected insert would silently drop exactly the failed-run record the
// sync_runs table exists to keep.
func TestSyncRunErrorTruncationRuneSafe(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	base := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)

	// 999 ASCII bytes, then a 3-byte rune straddling the 1000-byte cut.
	boundary := strings.Repeat("x", 999) + strings.Repeat("€", 100)
	if err := store.RecordSyncRun(ctx, SyncRun{
		SourceName: "multibyte", Connector: "focus-csv", TenantID: "default",
		StartedAt: base, FinishedAt: base.Add(time.Second), Outcome: "error", Error: boundary,
	}); err != nil {
		t.Fatalf("RecordSyncRun with multibyte error near the boundary: %v", err)
	}
	statuses, err := store.SyncStatuses(ctx)
	if err != nil {
		t.Fatalf("SyncStatuses: %v", err)
	}
	var got *SyncRun
	for i := range statuses {
		if statuses[i].Latest.SourceName == "multibyte" {
			got = &statuses[i].Latest
		}
	}
	if got == nil {
		t.Fatal("multibyte run row was not recorded")
	}
	if len(got.Error) == 0 || len(got.Error) > 1000 {
		t.Fatalf("stored error length = %d, want 1..1000 bytes", len(got.Error))
	}
	if !utf8.ValidString(got.Error) {
		t.Fatalf("stored error is not valid UTF-8: %q", got.Error[len(got.Error)-8:])
	}
	if !strings.HasPrefix(got.Error, strings.Repeat("x", 999)) {
		t.Fatal("stored error lost its leading content")
	}
}

// TestAPIReadDuringIngestWriteTransaction pins the in-process scheduler
// architecture: API reads use the same open DuckDB while an ingest-style write
// transaction is held open on a separate pooled connection.
func TestAPIReadDuringIngestWriteTransaction(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	batch := Batch{Connector: "aws-focus", SourceIdentity: "overlap", ContentHash: "one", TenantID: focus.DefaultTenant}
	record := testRecord(t, "Compute", day(1), "1")
	if _, err := store.ReplaceIngestBatch(ctx, batch, []focus.CostRecord{record}); err != nil {
		t.Fatal(err)
	}

	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM cost_records WHERE batch_connector = ? AND batch_source_identity = ?`,
		batch.Connector, batch.SourceIdentity); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, insertRecordSQL, insertRecordArgs(batch, &record)...); err != nil {
		t.Fatal(err)
	}

	readDone := make(chan error, 1)
	go func() {
		currencies, err := store.BillingCurrencies(ctx, focus.DefaultTenant, time.Time{}, time.Time{})
		if err == nil && !slices.Equal(currencies, []string{"USD"}) {
			err = fmt.Errorf("currencies = %v, want [USD]", currencies)
		}
		if err == nil {
			_, err = store.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{}, "USD")
		}
		readDone <- err
	}()
	if err := <-readDone; err != nil {
		t.Fatalf("API-style read while write transaction was open: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("committing held write: %v", err)
	}
}

// TestDailyTokensByService proves the token-usage query: money-only
// (null-quantity) rows are skipped, non-token usage units are excluded (the
// endpoint is token-scoped), ordering is deterministic (day, then service,
// then unit), a float-hazard token count round-trips EXACTLY (no float64),
// same day/service/unit rows sum, and results are tenant-scoped.
func TestDailyTokensByService(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Item 9: DailyTokensByService lives on the Store INTERFACE (its twin
	// DailyCostsByService does), so a Store consumer can query token usage
	// without a concrete-type assertion. Routing the query below through a
	// Store-typed value makes this test fail to COMPILE if the method is ever
	// dropped from the interface.
	var tokenStore Store = store

	// withUsage decorates a base cost record with a consumed unit and quantity;
	// an empty qty leaves ConsumedQuantity NULL (a money-only row).
	withUsage := func(r focus.CostRecord, unit, qty string) focus.CostRecord {
		r.ConsumedUnit = unit
		if qty != "" {
			d, err := decimal.NewFromString(qty)
			if err != nil {
				t.Fatalf("bad usage qty %q: %v", qty, err)
			}
			r.ConsumedQuantity = decimal.NullDecimal{Decimal: d, Valid: true}
		}
		return r
	}

	// A 19-digit token count float64 cannot represent exactly (> 2^53).
	const floatHazard = "1234567890123456789"
	records := []focus.CostRecord{
		// day 1 — enriched OpenAI rows; same day/service/unit, so they SUM. The
		// sum stays exact: floatHazard + 1500000 = 1234567890124956789.
		withUsage(testRecord(t, "OpenAI API", day(1), "12.5"), "Tokens", floatHazard),
		withUsage(testRecord(t, "OpenAI API", day(1), "0.5"), "Tokens", "1500000"),
		// day 1 — an enriched Claude row (sorts before OpenAI).
		withUsage(testRecord(t, "Claude API", day(1), "1"), "Tokens", "4400000"),
		// day 1 — a Tokens-unit row with a NULL quantity (money-only): excluded
		// by the consumed_quantity IS NOT NULL predicate specifically.
		withUsage(testRecord(t, "Ghost Token API", day(1), "3.25"), "Tokens", ""),
		// day 1 — a non-token FOCUS usage row (cloud Hrs): excluded by the token
		// unit scope, proving the endpoint is not a general usage view.
		withUsage(testRecord(t, "Amazon Elastic Compute Cloud", day(1), "7.0"), "Hrs", "36"),
		// day 2 — an enriched Claude row.
		withUsage(testRecord(t, "Claude API", day(2), "2"), "Tokens", "3000000"),
	}
	batch := Batch{Connector: "ai-mix", SourceIdentity: "src", ContentHash: "sha256:aaaa", TenantID: focus.DefaultTenant}
	if _, err := store.ReplaceIngestBatch(ctx, batch, records); err != nil {
		t.Fatalf("ReplaceIngestBatch: %v", err)
	}

	// A different tenant's enriched token row must be invisible to the default
	// tenant (D15 tenant scoping).
	acme := withUsage(testRecord(t, "Claude API", day(1), "9"), "Tokens", "999")
	acme.XTenantID = "acme"
	acmeBatch := Batch{Connector: "ai-mix", SourceIdentity: "src-acme", ContentHash: "sha256:bbbb", TenantID: "acme"}
	if _, err := store.ReplaceIngestBatch(ctx, acmeBatch, []focus.CostRecord{acme}); err != nil {
		t.Fatalf("ReplaceIngestBatch(acme): %v", err)
	}

	got, err := tokenStore.DailyTokensByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyTokensByService: %v", err)
	}
	want := []DailyTokenUsage{
		{Date: day(1), ServiceName: "Claude API", ConsumedUnit: "Tokens", Quantity: dec(t, "4400000")},
		{Date: day(1), ServiceName: "OpenAI API", ConsumedUnit: "Tokens", Quantity: dec(t, "1234567890124956789")},
		{Date: day(2), ServiceName: "Claude API", ConsumedUnit: "Tokens", Quantity: dec(t, "3000000")},
	}
	assertDailyTokens(t, got, want)
	// The null-quantity money-only row and the non-token (Hrs) usage row are
	// both absent, and the summed float-hazard is EXACT to the digit (a float64
	// sum would have rounded it) — assert on the string form.
	for _, r := range got {
		if r.ServiceName == "Ghost Token API" {
			t.Errorf("null-quantity money-only row surfaced in token usage: %+v", r)
		}
		if r.ServiceName == "Amazon Elastic Compute Cloud" || r.ConsumedUnit != "Tokens" {
			t.Errorf("non-token usage row surfaced in token usage: %+v", r)
		}
	}
	if got[1].Quantity.String() != "1234567890124956789" {
		t.Errorf("float-hazard sum = %s, want 1234567890124956789 (exact, no float64)", got[1].Quantity)
	}

	// Range bounds are inclusive calendar days.
	ranged, err := store.DailyTokensByService(ctx, focus.DefaultTenant, day(2), day(2))
	if err != nil {
		t.Fatalf("DailyTokensByService(ranged): %v", err)
	}
	assertDailyTokens(t, ranged, []DailyTokenUsage{
		{Date: day(2), ServiceName: "Claude API", ConsumedUnit: "Tokens", Quantity: dec(t, "3000000")},
	})

	// The acme tenant sees ONLY its own row; a nonexistent tenant sees nothing.
	acmeGot, err := store.DailyTokensByService(ctx, "acme", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyTokensByService(acme): %v", err)
	}
	assertDailyTokens(t, acmeGot, []DailyTokenUsage{
		{Date: day(1), ServiceName: "Claude API", ConsumedUnit: "Tokens", Quantity: dec(t, "999")},
	})
	none, err := store.DailyTokensByService(ctx, "nobody", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyTokensByService(nobody): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("nonexistent tenant sees %+v, want nothing", none)
	}
}

func assertDailyTokens(t *testing.T, got, want []DailyTokenUsage) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("token usage rows = %+v, want %d rows", got, len(want))
	}
	for i, w := range want {
		g := got[i]
		if !g.Date.Equal(w.Date) || g.ServiceName != w.ServiceName || g.ConsumedUnit != w.ConsumedUnit {
			t.Errorf("row %d = {%s %s %s}, want {%s %s %s}", i,
				g.Date.Format(time.DateOnly), g.ServiceName, g.ConsumedUnit,
				w.Date.Format(time.DateOnly), w.ServiceName, w.ConsumedUnit)
		}
		if !g.Quantity.Equal(w.Quantity) {
			t.Errorf("row %d quantity = %s, want %s", i, g.Quantity, w.Quantity)
		}
	}
}

// TestDailyUsageMetrics proves the usage_metrics query (migration 0006): the
// two-dimension (metric_name, unit) GROUP-BY guard, a >2^53 float-hazard sum
// staying exact, the service_tier="" round-trip, isolation from BOTH FOCUS
// queries, tenant scoping, deterministic ordering, empty→nil, and
// ReplaceUsageBatch idempotence + per-source_identity supersede. Dropping EITHER
// metric_name OR unit from the GROUP BY must fail this test.
func TestDailyUsageMetrics(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Route the whole test through a Store-typed value: dropping either new
	// method from the interface makes this fail to COMPILE (interface-drift
	// guard, like TestDailyTokensByService).
	var s Store = store

	metric := func(day int, service, tier, name, unit, qty string) Metric {
		return Metric{
			ChargePeriodStart: time.Date(2026, 5, day, 0, 0, 0, 0, time.UTC),
			ServiceName:       service, ServiceTier: tier, MetricName: name, Unit: unit,
			Quantity: dec(t, qty),
		}
	}

	// A 19-digit quantity float64 cannot represent exactly (> 2^53); two rows
	// identical in all five GROUP-BY dims must SUM to the exact 19-digit total.
	const floatHazard = "1234567890123456789"
	batch := UsageBatch{Connector: "anthropic-cost", SourceIdentity: "api.anthropic.com/anthropic-cost/2026-05", TenantID: focus.DefaultTenant}
	metrics := []Metric{
		// day 1 — same (day, service, tier, metric, unit): SUM to floatHazard+1500000.
		metric(1, "claude-opus-4-6", "priority", "uncached_input_tokens", "Tokens", floatHazard),
		metric(1, "claude-opus-4-6", "priority", "uncached_input_tokens", "Tokens", "1500000"),
		// day 1 — same (day, service, tier, unit) but a DIFFERENT metric_name: must
		// stay SEPARATE (collapses to 1122 if metric_name is dropped from GROUP BY).
		metric(1, "claude-sonnet-4-5", "standard", "uncached_input_tokens", "Tokens", "999"),
		metric(1, "claude-sonnet-4-5", "standard", "output_tokens", "Tokens", "123"),
		// day 1 — a Requests-unit row sharing ALL FOUR other dims (day, service, tier,
		// metric_name) with the output_tokens Tokens row above: it must stay SEPARATE
		// (Tokens vs Requests never merge). Because only `unit` differs, dropping `unit`
		// from the GROUP BY would MERGE the two into 130 (123+7) — a wrong SUM, not just
		// a binder error — so this is the semantic guard that `unit` is a GROUP-BY key.
		metric(1, "claude-sonnet-4-5", "standard", "output_tokens", "Requests", "7"),
		// day 1 — a further non-Tokens unit (web-search counts) round-tripping through
		// the store, distinct in metric_name from every other row.
		metric(1, "claude-sonnet-4-5", "standard", "web_search_requests", "Requests", "5"),
		// day 2 — an OpenAI-shaped row: service_tier="" round-trips to "".
		metric(2, "OpenAI API", "", "assistants api | file search", "Unknown", "42"),
	}
	if err := s.ReplaceUsageBatch(ctx, batch, metrics); err != nil {
		t.Fatalf("ReplaceUsageBatch: %v", err)
	}

	// A different tenant's metric under a DIFFERENT source_identity is invisible to
	// the default tenant (D15). (A same-(connector, source_identity) write from
	// another tenant would re-home by design — ReplaceUsageBatch deletes without a
	// tenant clause, D26a — so this uses a distinct source_identity, the scoping the
	// store actually guarantees.)
	acme := metric(1, "claude-opus-4-6", "priority", "uncached_input_tokens", "Tokens", "777")
	if err := s.ReplaceUsageBatch(ctx, UsageBatch{Connector: "anthropic-cost", SourceIdentity: "acme-src", TenantID: "acme"}, []Metric{acme}); err != nil {
		t.Fatalf("ReplaceUsageBatch(acme): %v", err)
	}

	// Seed a cost_records row with the SAME token unit/quantity so the isolation
	// assertion below is real: a usage_metrics row must NOT appear in either FOCUS
	// query, and this cost row must NOT appear in the usage-metrics query.
	costWithTokens := testRecord(t, "OpenAI API", day(1), "12.5")
	costWithTokens.ConsumedUnit = "Tokens"
	costWithTokens.ConsumedQuantity = decimal.NullDecimal{Decimal: dec(t, "555"), Valid: true}
	if _, err := store.ReplaceIngestBatch(ctx, Batch{Connector: "openai-cost", SourceIdentity: "cost-src", ContentHash: "sha256:c", TenantID: focus.DefaultTenant}, []focus.CostRecord{costWithTokens}); err != nil {
		t.Fatalf("ReplaceIngestBatch(cost): %v", err)
	}

	got, err := s.DailyUsageMetrics(ctx, focus.DefaultTenant, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyUsageMetrics: %v", err)
	}
	want := []DailyUsageMetric{
		// day 1, ordered by service, tier, metric, unit (byte order): "OpenAI"<"claude";
		// within a shared metric_name, "Requests"<"Tokens".
		{Date: day(1), ServiceName: "claude-opus-4-6", ServiceTier: "priority", MetricName: "uncached_input_tokens", Unit: "Tokens", Quantity: dec(t, "1234567890124956789")},
		{Date: day(1), ServiceName: "claude-sonnet-4-5", ServiceTier: "standard", MetricName: "output_tokens", Unit: "Requests", Quantity: dec(t, "7")},
		{Date: day(1), ServiceName: "claude-sonnet-4-5", ServiceTier: "standard", MetricName: "output_tokens", Unit: "Tokens", Quantity: dec(t, "123")},
		{Date: day(1), ServiceName: "claude-sonnet-4-5", ServiceTier: "standard", MetricName: "uncached_input_tokens", Unit: "Tokens", Quantity: dec(t, "999")},
		{Date: day(1), ServiceName: "claude-sonnet-4-5", ServiceTier: "standard", MetricName: "web_search_requests", Unit: "Requests", Quantity: dec(t, "5")},
		{Date: day(2), ServiceName: "OpenAI API", ServiceTier: "", MetricName: "assistants api | file search", Unit: "Unknown", Quantity: dec(t, "42")},
	}
	assertDailyUsageMetrics(t, got, want)
	// The float-hazard sum is EXACT to the digit (a float64 sum would round it).
	if got[0].Quantity.String() != "1234567890124956789" {
		t.Errorf("float-hazard sum = %s, want 1234567890124956789 (exact, no float64)", got[0].Quantity)
	}

	// ISOLATION: usage_metrics rows appear in NEITHER FOCUS query, and the cost
	// row's token quantity appears ONLY in the token query — never in usage.
	tokens, err := store.DailyTokensByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyTokensByService: %v", err)
	}
	if len(tokens) != 1 || tokens[0].Quantity.String() != "555" {
		t.Errorf("token query = %+v, want only the one cost row (555); usage_metrics must not leak in", tokens)
	}
	costs, err := store.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{}, "")
	if err != nil {
		t.Fatalf("DailyCostsByService: %v", err)
	}
	for _, d := range costs.Days {
		for _, svc := range d.Services {
			if svc.ServiceName == "claude-opus-4-6" || svc.ServiceName == "claude-sonnet-4-5" {
				t.Errorf("usage-metric service %q leaked into the cost view", svc.ServiceName)
			}
		}
	}

	// Range bounds are inclusive calendar days.
	ranged, err := s.DailyUsageMetrics(ctx, focus.DefaultTenant, day(2), day(2))
	if err != nil {
		t.Fatalf("DailyUsageMetrics(ranged): %v", err)
	}
	assertDailyUsageMetrics(t, ranged, []DailyUsageMetric{
		{Date: day(2), ServiceName: "OpenAI API", ServiceTier: "", MetricName: "assistants api | file search", Unit: "Unknown", Quantity: dec(t, "42")},
	})

	// Tenant scoping: acme sees ONLY its own row; a nonexistent tenant sees nothing.
	acmeGot, err := s.DailyUsageMetrics(ctx, "acme", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyUsageMetrics(acme): %v", err)
	}
	assertDailyUsageMetrics(t, acmeGot, []DailyUsageMetric{
		{Date: day(1), ServiceName: "claude-opus-4-6", ServiceTier: "priority", MetricName: "uncached_input_tokens", Unit: "Tokens", Quantity: dec(t, "777")},
	})
	none, err := s.DailyUsageMetrics(ctx, "nobody", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyUsageMetrics(nobody): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("nonexistent tenant sees %+v, want nothing", none)
	}

	// IDEMPOTENCE: re-writing the SAME batch yields identical rows (not doubled).
	if err := s.ReplaceUsageBatch(ctx, batch, metrics); err != nil {
		t.Fatalf("idempotent ReplaceUsageBatch: %v", err)
	}
	again, err := s.DailyUsageMetrics(ctx, focus.DefaultTenant, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyUsageMetrics after re-write: %v", err)
	}
	assertDailyUsageMetrics(t, again, want)

	// SUPERSEDE: a CHANGED batch under the SAME (connector, source_identity)
	// REPLACES — not accumulates. Bump the priority row 1500000→2500000 (new sum
	// floatHazard+2500000) and drop the OpenAI day-2 row entirely.
	changed := []Metric{
		metric(1, "claude-opus-4-6", "priority", "uncached_input_tokens", "Tokens", floatHazard),
		metric(1, "claude-opus-4-6", "priority", "uncached_input_tokens", "Tokens", "2500000"),
	}
	if err := s.ReplaceUsageBatch(ctx, batch, changed); err != nil {
		t.Fatalf("supersede ReplaceUsageBatch: %v", err)
	}
	superseded, err := s.DailyUsageMetrics(ctx, focus.DefaultTenant, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyUsageMetrics after supersede: %v", err)
	}
	assertDailyUsageMetrics(t, superseded, []DailyUsageMetric{
		{Date: day(1), ServiceName: "claude-opus-4-6", ServiceTier: "priority", MetricName: "uncached_input_tokens", Unit: "Tokens", Quantity: dec(t, "1234567890125956789")},
	})

	// EMPTY batch clears the source's rows (a month whose orphans vanished).
	if err := s.ReplaceUsageBatch(ctx, batch, nil); err != nil {
		t.Fatalf("empty ReplaceUsageBatch: %v", err)
	}
	cleared, err := s.DailyUsageMetrics(ctx, focus.DefaultTenant, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyUsageMetrics after empty: %v", err)
	}
	if len(cleared) != 0 {
		t.Errorf("empty batch did not clear the source's rows: %+v", cleared)
	}
}

func TestDailyUsageMetricsSearchCallsSourceQualified(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = store.Close() }()

	var s Store = store
	batch := UsageBatch{Connector: "openai-cost", SourceIdentity: "api.openai.com/openai-cost/2026-05", TenantID: focus.DefaultTenant}
	metrics := []Metric{
		{
			ChargePeriodStart: day(1),
			ServiceName:       "OpenAI API",
			ServiceTier:       "",
			MetricName:        "web_search_num_requests",
			Unit:              "Calls",
			Quantity:          dec(t, "15"),
		},
		{
			ChargePeriodStart: day(1),
			ServiceName:       "OpenAI API",
			ServiceTier:       "",
			MetricName:        "file_search_num_requests",
			Unit:              "Calls",
			Quantity:          dec(t, "8"),
		},
	}
	if err := s.ReplaceUsageBatch(ctx, batch, metrics); err != nil {
		t.Fatalf("ReplaceUsageBatch: %v", err)
	}

	got, err := s.DailyUsageMetrics(ctx, focus.DefaultTenant, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyUsageMetrics: %v", err)
	}
	assertDailyUsageMetrics(t, got, []DailyUsageMetric{
		{Date: day(1), ServiceName: "OpenAI API", ServiceTier: "", MetricName: "file_search_num_requests", Unit: "Calls", Quantity: dec(t, "8")},
		{Date: day(1), ServiceName: "OpenAI API", ServiceTier: "", MetricName: "web_search_num_requests", Unit: "Calls", Quantity: dec(t, "15")},
	})
}

func assertDailyUsageMetrics(t *testing.T, got, want []DailyUsageMetric) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("usage metric rows = %+v, want %d rows (%+v)", got, len(want), want)
	}
	for i, w := range want {
		g := got[i]
		if !g.Date.Equal(w.Date) || g.ServiceName != w.ServiceName || g.ServiceTier != w.ServiceTier ||
			g.MetricName != w.MetricName || g.Unit != w.Unit {
			t.Errorf("row %d = {%s %s %s %s %s}, want {%s %s %s %s %s}", i,
				g.Date.Format(time.DateOnly), g.ServiceName, g.ServiceTier, g.MetricName, g.Unit,
				w.Date.Format(time.DateOnly), w.ServiceName, w.ServiceTier, w.MetricName, w.Unit)
		}
		if !g.Quantity.Equal(w.Quantity) {
			t.Errorf("row %d quantity = %s, want %s", i, g.Quantity, w.Quantity)
		}
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
