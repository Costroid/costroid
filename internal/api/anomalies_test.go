// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/storage"
)

// anomalyFakeStore returns a fake whose daily series flags both the TOTAL and
// each of two keys on 2026-06-11 and 2026-06-12: ten flat baseline days
// (EC2 10, S3 5) then two spike days (EC2 100, S3 50). Day 11 and day 12 carry
// IDENTICAL flag statistics (median 15/10/5, mad 0), which the range-independence
// assertion relies on.
func anomalyFakeStore(t *testing.T) *fakeStore {
	t.Helper()
	day := func(n int, ec2, s3 string) storage.DayCosts {
		return storage.DayCosts{
			Date: time.Date(2026, 6, n, 0, 0, 0, 0, time.UTC),
			Services: []storage.ServiceCost{
				{ServiceName: "Amazon EC2", Cost: dec(t, ec2)},
				{ServiceName: "Amazon S3", Cost: dec(t, s3)},
			},
		}
	}
	var days []storage.DayCosts
	for n := 1; n <= 10; n++ {
		days = append(days, day(n, "10", "5"))
	}
	days = append(days, day(11, "100", "50"), day(12, "100", "50"))
	return &fakeStore{daily: storage.DailyCosts{Currency: "USD", Days: days}}
}

// TestGetAnomaliesContractPins name-checks every contract pin: the ordering rule,
// the full parameters echo, currency pass-through, key-present-only-on-key-scope,
// []-never-null, the full-history (zero-start) fetch, and range independence.
func TestGetAnomaliesContractPins(t *testing.T) {
	store := anomalyFakeStore(t)
	handler := NewHandler("test", testStatic(), store, "")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/anomalies", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	// Range-independence mechanism: scoring queries the store with a ZERO start
	// (full history) even without an explicit start.
	if !store.gotStart.IsZero() {
		t.Errorf("anomaly scoring queried start=%s, want zero (full history)", store.gotStart)
	}
	if store.gotGroupBy != storage.GroupByService {
		t.Errorf("default groupBy = %v, want GroupByService", store.gotGroupBy)
	}

	var got Anomalies
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// Currency pass-through.
	if got.Currency != "USD" {
		t.Errorf("currency = %q, want USD", got.Currency)
	}
	// Parameters echo — every value.
	p := got.Parameters
	if p.K != "3" || p.ConsistencyConstant != "1.4826" || p.WindowDays != 30 || p.MinObservations != 10 || p.RelativeFloor != "0.1" || p.GroupBy != "service" {
		t.Errorf("parameters echo = %+v", p)
	}

	// Ordering: date-asc, then total scope before key scope, then key-asc.
	type row struct{ date, scope, key string }
	want := []row{
		{"2026-06-11", "total", ""},
		{"2026-06-11", "key", "Amazon EC2"},
		{"2026-06-11", "key", "Amazon S3"},
		{"2026-06-12", "total", ""},
		{"2026-06-12", "key", "Amazon EC2"},
		{"2026-06-12", "key", "Amazon S3"},
	}
	if len(got.Anomalies) != len(want) {
		t.Fatalf("anomalies = %+v, want %d", got.Anomalies, len(want))
	}
	for i, w := range want {
		a := got.Anomalies[i]
		if a.Date.Format(time.DateOnly) != w.date || a.Scope != w.scope {
			t.Errorf("flag %d = date %s scope %s, want %s %s", i, a.Date.Format(time.DateOnly), a.Scope, w.date, w.scope)
		}
		// key present ONLY on scope=key.
		switch w.scope {
		case "total":
			if a.Key != nil {
				t.Errorf("flag %d (total) carries a key %q, want none", i, *a.Key)
			}
		case "key":
			if a.Key == nil || *a.Key != w.key {
				t.Errorf("flag %d (key) key = %v, want %q", i, a.Key, w.key)
			}
		}
		if a.Direction != "increase" {
			t.Errorf("flag %d direction = %q, want increase", i, a.Direction)
		}
	}
	// A total flag's exact statistics (hand-recomputable).
	total11 := got.Anomalies[0]
	if total11.Observed != "150" || total11.Median != "15" || total11.Mad != "0" || total11.ScaledMad != "0" || total11.Threshold != "0" || total11.Deviation != "135" {
		t.Errorf("day-11 total flag = %+v", total11)
	}

	// []-never-null on an empty store, currency "".
	emptyRec := httptest.NewRecorder()
	NewHandler("test", testStatic(), &fakeStore{}, "").ServeHTTP(emptyRec, httptest.NewRequest(http.MethodGet, "/api/v1/anomalies", nil))
	if body := strings.TrimSpace(emptyRec.Body.String()); !strings.Contains(body, `"anomalies":[]`) || !strings.Contains(body, `"currency":""`) {
		t.Errorf("empty store body = %s, want anomalies:[] and currency:\"\"", body)
	}

	// Range independence + filtering: a narrow window returns ONLY the day-12
	// flags, with statistics identical to the wide request's day-12 flags.
	store2 := anomalyFakeStore(t)
	narrowRec := httptest.NewRecorder()
	NewHandler("test", testStatic(), store2, "").ServeHTTP(narrowRec, httptest.NewRequest(http.MethodGet, "/api/v1/anomalies?start=2026-06-12&end=2026-06-12", nil))
	if !store2.gotStart.IsZero() {
		t.Errorf("narrow request still queried the store with a non-zero start %s", store2.gotStart)
	}
	var narrow Anomalies
	if err := json.Unmarshal(narrowRec.Body.Bytes(), &narrow); err != nil {
		t.Fatal(err)
	}
	if len(narrow.Anomalies) != 3 {
		t.Fatalf("narrow window anomalies = %+v, want 3 (day 12 only)", narrow.Anomalies)
	}
	wide12, n12 := got.Anomalies[3], narrow.Anomalies[0] // both the day-12 total flag
	if n12.Date.Format(time.DateOnly) != "2026-06-12" || n12.Scope != "total" ||
		n12.Observed != wide12.Observed || n12.Median != wide12.Median || n12.Threshold != wide12.Threshold || n12.Deviation != wide12.Deviation {
		t.Errorf("range dependence detected: narrow day-12 flag %+v != wide %+v", n12, wide12)
	}
}

// TestGetAnomaliesGroupByComposition pins that groupBy composes exactly like
// /costs/daily: provider and allocation route to the matching store method, an
// invalid value 400s without touching the store, and the allocation 400 (missing
// rules) is the costs handler's exact body.
func TestGetAnomaliesGroupByComposition(t *testing.T) {
	t.Run("provider routes to provider grouping", func(t *testing.T) {
		store := anomalyFakeStore(t)
		rec := httptest.NewRecorder()
		NewHandler("test", testStatic(), store, "").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/anomalies?groupBy=provider", nil))
		if rec.Code != http.StatusOK || store.gotGroupBy != storage.GroupByProvider {
			t.Fatalf("status=%d groupBy=%v body=%s", rec.Code, store.gotGroupBy, rec.Body)
		}
		var got Anomalies
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Parameters.GroupBy != "provider" {
			t.Errorf("parameters.groupBy = %q, want provider", got.Parameters.GroupBy)
		}
	})

	t.Run("invalid groupBy is 400 without touching the store", func(t *testing.T) {
		store := anomalyFakeStore(t)
		rec := httptest.NewRecorder()
		NewHandler("test", testStatic(), store, "").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/anomalies?groupBy=bogus", nil))
		if rec.Code != http.StatusBadRequest || store.queryCount != 0 || store.allocQueryCount != 0 {
			t.Fatalf("status=%d queryCount=%d allocCount=%d", rec.Code, store.queryCount, store.allocQueryCount)
		}
	})

	t.Run("allocation composes and echoes the rule content", func(t *testing.T) {
		store := anomalyFakeStore(t)
		rules := writeRules(t, `{"dimensions":[{"name":"team","rules":[{"label":"platform","match":[{"dimension":"service_name","operator":"equals","value":"Amazon EC2"}]}]}]}`)
		rec := httptest.NewRecorder()
		NewHandler("test", testStatic(), store, rules).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/anomalies?groupBy=allocation", nil))
		if rec.Code != http.StatusOK || store.allocQueryCount != 1 {
			t.Fatalf("status=%d allocCount=%d body=%s", rec.Code, store.allocQueryCount, rec.Body)
		}
		if !store.gotStart.IsZero() {
			t.Errorf("allocation scoring queried start=%s, want zero (full history)", store.gotStart)
		}
		if store.gotDimension.Name != "team" || len(store.gotDimension.Rules) != 1 {
			t.Errorf("dimension not propagated: %+v", store.gotDimension)
		}
		var got Anomalies
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Parameters.GroupBy != "allocation" {
			t.Errorf("parameters.groupBy = %q, want allocation", got.Parameters.GroupBy)
		}
	})

	t.Run("allocation unconfigured is the costs handler's exact 400", func(t *testing.T) {
		store := anomalyFakeStore(t)
		rec := httptest.NewRecorder()
		NewHandler("test", testStatic(), store, "").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/anomalies?groupBy=allocation", nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
		}
		if body := strings.TrimSpace(rec.Body.String()); body != "no allocation rules configured (start serve with --allocation-rules or set $COSTROID_ALLOCATION_RULES)" {
			t.Errorf("unconfigured body = %q", body)
		}
		if store.allocQueryCount != 0 {
			t.Error("store queried despite the unconfigured 400")
		}
	})
}
