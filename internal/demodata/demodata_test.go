// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package demodata_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Costroid/costroid/internal/api"
	"github.com/Costroid/costroid/internal/demodata"
	"github.com/Costroid/costroid/internal/storage"
)

var static = fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("demo")}}

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
	return store, api.NewHandler("test", staticFS, store, "", api.WithReadOnly(), api.WithDemo())
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
