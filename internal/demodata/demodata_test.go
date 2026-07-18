// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package demodata_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Costroid/costroid/internal/api"
	"github.com/Costroid/costroid/internal/demodata"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/storage"
)

var static = fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("demo")}}

type demoSyncSchedule []demodata.SyncScheduleSource

func (s demoSyncSchedule) SyncSchedule() []api.SyncScheduleSource {
	result := make([]api.SyncScheduleSource, len(s))
	for i, source := range s {
		result[i] = api.SyncScheduleSource(source)
	}
	return result
}

func seeded(t *testing.T, asOf time.Time, seed int64) (*storage.DuckDB, http.Handler) {
	t.Helper()
	store, err := storage.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := demodata.Seed(context.Background(), store, asOf, seed); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	var staticFS fs.FS = static
	return store, api.NewHandler("test", staticFS, store, "",
		api.WithReadOnly(), api.WithDemo(),
		api.WithSyncSchedule(demoSyncSchedule(demodata.SyncSchedule(asOf))))
}

func get(t *testing.T, handler http.Handler, path string) []byte {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.Bytes()
}

func TestSeedDeterministicClosedLoopAndExact(t *testing.T) {
	asOf := time.Date(2026, 7, 11, 18, 30, 0, 0, time.FixedZone("test", 3*60*60))
	_, first := seeded(t, asOf, demodata.DefaultSeed)
	_, second := seeded(t, asOf, demodata.DefaultSeed)

	firstDaily := get(t, first, "/api/v1/costs/daily")
	secondDaily := get(t, second, "/api/v1/costs/daily")
	firstHash := sha256.Sum256(firstDaily)
	secondHash := sha256.Sum256(secondDaily)
	if firstHash != secondHash {
		t.Fatalf("same (asOf, seed) hashes differ: %s != %s", hex.EncodeToString(firstHash[:]), hex.EncodeToString(secondHash[:]))
	}

	var costs api.DailyCosts
	if err := json.Unmarshal(firstDaily, &costs); err != nil {
		t.Fatal(err)
	}
	start, _ := demodata.Window(asOf)
	if len(costs.Days) < 180 {
		t.Fatalf("daily row count = %d, want at least 180", len(costs.Days))
	}
	foundExact := false
	for _, day := range costs.Days {
		if day.Date.Equal(start) {
			for _, service := range day.Services {
				if service.Key == "Amazon EC2" && service.Cost == demodata.ExactAmount {
					foundExact = true
				}
			}
		}
	}
	if !foundExact {
		t.Fatalf("exact amount %s did not round-trip on %s", demodata.ExactAmount, start.Format(time.DateOnly))
	}

	var anomalies api.Anomalies
	if err := json.Unmarshal(get(t, first, "/api/v1/anomalies?groupBy=service"), &anomalies); err != nil {
		t.Fatal(err)
	}
	flaggedDates := map[string]struct{}{}
	for _, anomaly := range anomalies.Anomalies {
		flaggedDates[anomaly.Date.Format(time.DateOnly)] = struct{}{}
	}
	wantSpike := demodata.SpikeDate(asOf).Format(time.DateOnly)
	if len(flaggedDates) != 1 {
		t.Fatalf("flagged dates = %v, want exactly one (%s); flags=%+v", flaggedDates, wantSpike, anomalies.Anomalies)
	}
	if _, ok := flaggedDates[wantSpike]; !ok {
		t.Fatalf("flagged date = %v, want injected spike %s", flaggedDates, wantSpike)
	}

	var economics api.UnitEconomics
	if err := json.Unmarshal(get(t, first, "/api/v1/unit-economics/daily?metric=requests%20served"), &economics); err != nil {
		t.Fatal(err)
	}
	if economics.Period.CoveredDays != len(costs.Days) || economics.Period.UnitCost == nil {
		t.Fatalf("unit economics not fully populated: %+v", economics.Period)
	}

	var usage []api.DailyUsageMetric
	if err := json.Unmarshal(get(t, first, "/api/v1/usage/metrics/daily"), &usage); err != nil {
		t.Fatal(err)
	}
	if len(usage) == 0 {
		t.Fatal("synthetic AI usage is empty")
	}
	for _, row := range usage {
		if row.ServiceName == "" || row.MetricName == "" || row.Unit == "" || row.Quantity == "" {
			t.Fatalf("usage row is not counts plus categorical dimensions: %+v", row)
		}
	}

	t.Logf("daily rows=%d sha256=%s", len(costs.Days), hex.EncodeToString(firstHash[:]))
	t.Logf("exact money=%s spike=%s anomaly flags=%d unique dates=%d", demodata.ExactAmount, wantSpike, len(anomalies.Anomalies), len(flaggedDates))
}

func TestSeedSyncStatusDeterministicAcrossFreshStores(t *testing.T) {
	asOf := time.Date(2026, 7, 11, 18, 30, 0, 0, time.FixedZone("test", 3*60*60))
	_, first := seeded(t, asOf, demodata.DefaultSeed)
	_, second := seeded(t, asOf, demodata.DefaultSeed)

	firstStatus := get(t, first, "/api/v1/sync/status")
	secondStatus := get(t, second, "/api/v1/sync/status")
	if !bytes.Equal(firstStatus, secondStatus) {
		t.Fatalf("same asOf sync status differs:\nfirst:  %s\nsecond: %s", firstStatus, secondStatus)
	}

	var status api.SyncStatusResponse
	if err := json.Unmarshal(firstStatus, &status); err != nil {
		t.Fatal(err)
	}
	if !status.Enabled {
		t.Fatal("synthetic sync status enabled = false, want true")
	}
	if len(status.Sources) != 4 {
		t.Fatalf("synthetic sync sources = %d, want 4: %+v", len(status.Sources), status.Sources)
	}

	day := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	wantNames := []string{"aws-prod", "azure-main", "openai-cost", "anthropic-cost"}
	wantConnectors := []string{"aws", "azure", "openai-cost", "anthropic-cost"}
	wantRecords := []int64{12_904, 8_231, 0, 1_455}
	wantFinished := []time.Time{
		day.Add(3*time.Hour + 47*time.Second),
		day.Add(3*time.Hour + 72*time.Second),
		day.Add(3*time.Hour + 5*time.Second),
		day.Add(3*time.Hour + 20*time.Second),
	}
	for i, source := range status.Sources {
		if source.Name != wantNames[i] || source.Connector != wantConnectors[i] || source.Tenant != focus.DefaultTenant {
			t.Errorf("source %d key = (%q, %q, %q), want (%q, %q, %q)", i, source.Name, source.Connector, source.Tenant, wantNames[i], wantConnectors[i], focus.DefaultTenant)
		}
		if source.Interval == nil || *source.Interval != "6h" {
			t.Errorf("source %s interval = %v, want 6h", source.Name, source.Interval)
		}
		if source.NextRunAt == nil || !source.NextRunAt.Equal(day.Add(9*time.Hour)) {
			t.Errorf("source %s nextRunAt = %v, want %s", source.Name, source.NextRunAt, day.Add(9*time.Hour))
		}
		if source.LastRun == nil {
			t.Errorf("source %s lastRun is nil", source.Name)
			continue
		}
		if source.LastRun.RecordsIngested != wantRecords[i] || !source.LastRun.FinishedAt.Equal(wantFinished[i]) {
			t.Errorf("source %s latest run = %+v, want records=%d finished=%s", source.Name, source.LastRun, wantRecords[i], wantFinished[i])
		}
	}

	failing := status.Sources[2]
	if failing.LastRun == nil || failing.LastRun.Outcome != api.Error {
		t.Fatalf("openai latest run = %+v, want error", failing.LastRun)
	}
	if failing.LastRun.Error == nil || *failing.LastRun.Error != "openai cost API request failed: 429 Too Many Requests" {
		t.Errorf("openai latest error = %v", failing.LastRun.Error)
	}
	wantLastSuccess := day.Add(-3*time.Hour + 30*time.Second)
	if failing.LastSuccessAt == nil || !failing.LastSuccessAt.Equal(wantLastSuccess) {
		t.Errorf("openai lastSuccessAt = %v, want %s", failing.LastSuccessAt, wantLastSuccess)
	}

	for _, i := range []int{0, 1, 3} {
		if status.Sources[i].LastRun == nil || status.Sources[i].LastRun.Outcome != api.Success {
			t.Errorf("source %s latest outcome = %+v, want success", status.Sources[i].Name, status.Sources[i].LastRun)
		}
	}

	t.Logf("sync status=%s", firstStatus)
}

func TestSeedSyncRunHistoryExact(t *testing.T) {
	ctx := context.Background()
	asOf := time.Date(2026, 7, 11, 18, 30, 0, 0, time.FixedZone("test", 3*60*60))
	dir := t.TempDir()
	store, err := storage.Open(ctx, dir)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	if err := demodata.Seed(ctx, store, asOf, demodata.DefaultSeed); err != nil {
		_ = store.Close()
		t.Fatalf("Seed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("closing seeded store: %v", err)
	}

	db, err := sql.Open("duckdb", filepath.Join(dir, storage.DatabaseFile))
	if err != nil {
		t.Fatalf("opening seeded database for history assertion: %v", err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, `SELECT source_name, connector, tenant_id,
		started_at, finished_at, outcome, error, periods_processed,
		periods_skipped, records_ingested
		FROM sync_runs
		ORDER BY CASE source_name
			WHEN 'aws-prod' THEN 1 WHEN 'azure-main' THEN 2
			WHEN 'openai-cost' THEN 3 WHEN 'anthropic-cost' THEN 4 END,
		started_at`)
	if err != nil {
		t.Fatalf("querying seeded sync_runs: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var got []storage.SyncRun
	for rows.Next() {
		var run storage.SyncRun
		if err := rows.Scan(
			&run.SourceName, &run.Connector, &run.TenantID,
			&run.StartedAt, &run.FinishedAt, &run.Outcome, &run.Error,
			&run.PeriodsProcessed, &run.PeriodsSkipped, &run.RecordsIngested,
		); err != nil {
			t.Fatalf("scanning seeded sync run: %v", err)
		}
		run.StartedAt = run.StartedAt.UTC()
		run.FinishedAt = run.FinishedAt.UTC()
		got = append(got, run)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading seeded sync runs: %v", err)
	}

	day := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	want := []storage.SyncRun{
		{SourceName: "aws-prod", Connector: "aws", TenantID: focus.DefaultTenant, StartedAt: day.Add(3 * time.Hour), FinishedAt: day.Add(3*time.Hour + 47*time.Second), Outcome: "success", PeriodsProcessed: 1, PeriodsSkipped: 5, RecordsIngested: 12_904},
		{SourceName: "azure-main", Connector: "azure", TenantID: focus.DefaultTenant, StartedAt: day.Add(3 * time.Hour), FinishedAt: day.Add(3*time.Hour + 72*time.Second), Outcome: "success", PeriodsProcessed: 1, PeriodsSkipped: 5, RecordsIngested: 8_231},
		{SourceName: "openai-cost", Connector: "openai-cost", TenantID: focus.DefaultTenant, StartedAt: day.Add(-3 * time.Hour), FinishedAt: day.Add(-3*time.Hour + 30*time.Second), Outcome: "success", PeriodsProcessed: 1, PeriodsSkipped: 5, RecordsIngested: 1_200},
		{SourceName: "openai-cost", Connector: "openai-cost", TenantID: focus.DefaultTenant, StartedAt: day.Add(3 * time.Hour), FinishedAt: day.Add(3*time.Hour + 5*time.Second), Outcome: "error", Error: "openai cost API request failed: 429 Too Many Requests"},
		{SourceName: "anthropic-cost", Connector: "anthropic-cost", TenantID: focus.DefaultTenant, StartedAt: day.Add(3 * time.Hour), FinishedAt: day.Add(3*time.Hour + 20*time.Second), Outcome: "success", PeriodsProcessed: 1, PeriodsSkipped: 5, RecordsIngested: 1_455},
	}
	if len(got) != len(want) {
		t.Fatalf("seeded sync run count = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("seeded sync run %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestSeedPopulatesTokenUsage pins the Step-0 fix: the AI services' FOCUS cost
// rows carry ConsumedUnit="Tokens" and a positive ConsumedQuantity, so the
// token-usage view (/api/v1/usage/tokens/daily) returns a non-empty series
// keyed by service. The quantities are Cardinal-safe: integer counts and
// categorical dimensions only, never prompt or response content.
func TestSeedPopulatesTokenUsage(t *testing.T) {
	asOf := time.Date(2026, 7, 11, 18, 30, 0, 0, time.FixedZone("test", 3*60*60))
	_, handler := seeded(t, asOf, demodata.DefaultSeed)

	var tokens []api.DailyTokenUsage
	if err := json.Unmarshal(get(t, handler, "/api/v1/usage/tokens/daily"), &tokens); err != nil {
		t.Fatal(err)
	}
	if len(tokens) == 0 {
		t.Fatal("synthetic token usage is empty; the AI cost rows carry no ConsumedQuantity")
	}

	allDigits := func(s string) bool {
		if s == "" {
			return false
		}
		for _, r := range s {
			if r < '0' || r > '9' {
				return false
			}
		}
		return true
	}

	services := map[string]struct{}{}
	nonZero := false
	for _, row := range tokens {
		if row.ConsumedUnit != "Tokens" {
			t.Fatalf("token row unit = %q, want Tokens: %+v", row.ConsumedUnit, row)
		}
		if row.ServiceName == "" {
			t.Fatalf("token row missing ServiceName: %+v", row)
		}
		if !allDigits(row.ConsumedQuantity) {
			t.Fatalf("token quantity is not a non-negative integer count: %+v", row)
		}
		if row.ConsumedQuantity != "0" {
			nonZero = true
		}
		services[row.ServiceName] = struct{}{}
	}
	if !nonZero {
		t.Fatal("every synthetic token quantity is zero")
	}
	for _, want := range []string{"GPT-5", "Claude"} {
		if _, ok := services[want]; !ok {
			t.Fatalf("token series missing AI service %q; have %v", want, services)
		}
	}
	t.Logf("token rows=%d services=%v", len(tokens), services)
}
