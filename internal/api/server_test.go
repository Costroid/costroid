// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/storage"
)

// fakeStore records the query it received and returns canned costs.
type fakeStore struct {
	daily      storage.DailyCosts
	gotTenant  string
	gotStart   time.Time
	gotEnd     time.Time
	gotGroupBy storage.CostGroupBy
	queryCount int

	// token-usage query recording, kept separate from the cost fields.
	tokens          []storage.DailyTokenUsage
	gotTokenTenant  string
	gotTokenStart   time.Time
	gotTokenEnd     time.Time
	tokenQueryCount int

	// usage-metric query recording, kept separate from the cost/token fields.
	usage           []storage.DailyUsageMetric
	gotUsageTenant  string
	gotUsageStart   time.Time
	gotUsageEnd     time.Time
	usageQueryCount int
}

func (f *fakeStore) DailyCostsByService(_ context.Context, tenant string, start, end time.Time, groupBy ...storage.CostGroupBy) (storage.DailyCosts, error) {
	f.gotTenant, f.gotStart, f.gotEnd = tenant, start, end
	f.gotGroupBy = storage.GroupByService
	if len(groupBy) > 0 {
		f.gotGroupBy = groupBy[0]
	}
	f.queryCount++
	return f.daily, nil
}

func (f *fakeStore) DailyTokensByService(_ context.Context, tenant string, start, end time.Time) ([]storage.DailyTokenUsage, error) {
	f.gotTokenTenant, f.gotTokenStart, f.gotTokenEnd = tenant, start, end
	f.tokenQueryCount++
	return f.tokens, nil
}

func (f *fakeStore) DailyUsageMetrics(_ context.Context, tenant string, start, end time.Time) ([]storage.DailyUsageMetric, error) {
	f.gotUsageTenant, f.gotUsageStart, f.gotUsageEnd = tenant, start, end
	f.usageQueryCount++
	return f.usage, nil
}

func testStatic() fstest.MapFS {
	return fstest.MapFS{
		"index.html":    &fstest.MapFile{Data: []byte(`<!doctype html><div id="root"></div>`)},
		"assets/app.js": &fstest.MapFile{Data: []byte(`console.log("app")`)},
		".gitkeep":      &fstest.MapFile{Data: []byte("")},
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

// TestStaticHandler covers the slice-0 review fix: no directory listings,
// no dotfiles, SPA fallback for unknown extensionless GET paths, and API
// routes unaffected.
func TestStaticHandler(t *testing.T) {
	handler := NewHandler("0.1.0-test", testStatic(), &fakeStore{})

	tests := []struct {
		name         string
		method       string
		path         string
		wantStatus   int
		wantContains string
		wantExcludes string
		wantAllow    string
	}{
		{
			name:   "root serves the app",
			method: http.MethodGet, path: "/",
			wantStatus: http.StatusOK, wantContains: `<div id="root">`,
		},
		{
			name:   "existing asset is served",
			method: http.MethodGet, path: "/assets/app.js",
			wantStatus: http.StatusOK, wantContains: "console.log",
		},
		{
			name:   "directory path does not list contents",
			method: http.MethodGet, path: "/assets/",
			wantStatus: http.StatusOK, wantContains: `<div id="root">`, wantExcludes: "app.js",
		},
		{
			name:   "unknown extensionless path falls back to index.html",
			method: http.MethodGet, path: "/costs",
			wantStatus: http.StatusOK, wantContains: `<div id="root">`,
		},
		{
			name:   "dotfiles are never served",
			method: http.MethodGet, path: "/.gitkeep",
			wantStatus: http.StatusNotFound,
		},
		{
			name:   "missing asset with extension is a real 404",
			method: http.MethodGet, path: "/nope.js",
			wantStatus: http.StatusNotFound,
		},
		{
			name:   "unknown API path is a real 404, not the SPA fallback",
			method: http.MethodGet, path: "/api/v1/nope",
			wantStatus: http.StatusNotFound,
		},
		{
			name:   "percent-encoded slash cannot smuggle an API path past the exclusion",
			method: http.MethodGet, path: "/%2Fapi/v1/nope",
			wantStatus: http.StatusNotFound,
		},
		{
			name:   "non-GET request to a static path is rejected with Allow",
			method: http.MethodPost, path: "/costs",
			wantStatus: http.StatusMethodNotAllowed, wantAllow: "GET, HEAD",
		},
		{
			name:   "non-GET request to the root is rejected with Allow",
			method: http.MethodPost, path: "/",
			wantStatus: http.StatusMethodNotAllowed, wantAllow: "GET, HEAD",
		},
		{
			name:   "meta API route is unaffected",
			method: http.MethodGet, path: "/api/v1/meta",
			wantStatus: http.StatusOK, wantContains: `"version":"0.1.0-test"`,
		},
		{
			name:   "healthz is unaffected",
			method: http.MethodGet, path: "/healthz",
			wantStatus: http.StatusOK, wantContains: "ok",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
			if rec.Code != tt.wantStatus {
				t.Fatalf("%s %s = %d, want %d (body: %q)", tt.method, tt.path, rec.Code, tt.wantStatus, rec.Body)
			}
			body := rec.Body.String()
			if tt.wantContains != "" && !strings.Contains(body, tt.wantContains) {
				t.Errorf("%s %s body %q does not contain %q", tt.method, tt.path, body, tt.wantContains)
			}
			if tt.wantExcludes != "" && strings.Contains(body, tt.wantExcludes) {
				t.Errorf("%s %s body %q must not contain %q", tt.method, tt.path, body, tt.wantExcludes)
			}
			// RFC 9110 §15.5.6: 405 responses MUST carry an Allow header.
			if got := rec.Header().Get("Allow"); got != tt.wantAllow {
				t.Errorf("%s %s Allow header = %q, want %q", tt.method, tt.path, got, tt.wantAllow)
			}
		})
	}
}

func TestGetMeta(t *testing.T) {
	handler := NewHandler("1.2.3-test", testStatic(), &fakeStore{})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var meta Meta
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatalf("unmarshaling body %q: %v", rec.Body, err)
	}
	want := Meta{Name: "costroid", Version: "1.2.3-test", FocusVersion: "1.4"}
	if meta != want {
		t.Errorf("meta = %+v, want %+v", meta, want)
	}
}

func TestGetDailyCosts(t *testing.T) {
	store := &fakeStore{daily: storage.DailyCosts{
		Currency: "USD",
		Days: []storage.DayCosts{
			{
				Date: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
				Services: []storage.ServiceCost{
					{ServiceName: "AWS Lambda", Cost: dec(t, "0.1896")},
					{ServiceName: "Amazon Elastic Compute Cloud", Cost: dec(t, "3.6288")},
				},
			},
			{
				Date: time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC),
				Services: []storage.ServiceCost{
					{ServiceName: "AWS Lambda", Cost: dec(t, "0.1896")},
				},
			},
		},
	}}
	handler := NewHandler("0.1.0-test", testStatic(), store)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/costs/daily", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	if store.gotTenant != "default" {
		t.Errorf("queried tenant %q, want default", store.gotTenant)
	}
	if !store.gotStart.IsZero() || !store.gotEnd.IsZero() {
		t.Errorf("default range = [%s, %s], want unbounded (zero times)", store.gotStart, store.gotEnd)
	}
	if store.gotGroupBy != storage.GroupByService {
		t.Errorf("default groupBy = %v, want GroupByService", store.gotGroupBy)
	}

	var got DailyCosts
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Currency != "USD" {
		t.Errorf("currency = %q, want USD", got.Currency)
	}
	if got.Total != "4.008" {
		t.Errorf("period total = %q, want 4.008", got.Total)
	}
	if len(got.Days) != 2 {
		t.Fatalf("days = %+v, want 2", got.Days)
	}
	day0 := got.Days[0]
	if day0.Date.Format(time.DateOnly) != "2026-05-01" || day0.Total != "3.8184" {
		t.Errorf("day 0 = %s total %q, want 2026-05-01 total 3.8184", day0.Date, day0.Total)
	}
	if len(day0.Services) != 2 || day0.Services[0].ServiceName != "AWS Lambda" || day0.Services[0].Cost != "0.1896" {
		t.Errorf("day 0 services = %+v", day0.Services)
	}
	if got.Days[1].Total != "0.1896" {
		t.Errorf("day 1 total = %q, want 0.1896", got.Days[1].Total)
	}
}

func TestGetDailyCostsGroupByParam(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		wantStatus  int
		wantGroupBy storage.CostGroupBy
		wantQuery   bool
	}{
		{
			name:        "absent defaults to service",
			query:       "",
			wantStatus:  http.StatusOK,
			wantGroupBy: storage.GroupByService,
			wantQuery:   true,
		},
		{
			name:        "service propagates service",
			query:       "?groupBy=service",
			wantStatus:  http.StatusOK,
			wantGroupBy: storage.GroupByService,
			wantQuery:   true,
		},
		{
			name:        "provider propagates provider",
			query:       "?groupBy=provider",
			wantStatus:  http.StatusOK,
			wantGroupBy: storage.GroupByProvider,
			wantQuery:   true,
		},
		{
			name:       "bogus is rejected",
			query:      "?groupBy=bogus",
			wantStatus: http.StatusBadRequest,
			wantQuery:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{}
			handler := NewHandler("0.1.0-test", testStatic(), store)

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/costs/daily"+tt.query, nil))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tt.wantStatus, rec.Body)
			}
			if tt.wantQuery {
				if store.queryCount != 1 {
					t.Fatalf("store query count = %d, want 1", store.queryCount)
				}
				if store.gotGroupBy != tt.wantGroupBy {
					t.Errorf("groupBy = %v, want %v", store.gotGroupBy, tt.wantGroupBy)
				}
			} else if store.queryCount != 0 {
				t.Fatalf("store query count = %d, want 0", store.queryCount)
			}
		})
	}
}

func TestGetDailyCostsDateParams(t *testing.T) {
	store := &fakeStore{}
	handler := NewHandler("0.1.0-test", testStatic(), store)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/costs/daily?start=2026-05-02&end=2026-05-03", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	if got := store.gotStart.Format(time.DateOnly); got != "2026-05-02" {
		t.Errorf("start = %s, want 2026-05-02", got)
	}
	if got := store.gotEnd.Format(time.DateOnly); got != "2026-05-03" {
		t.Errorf("end = %s, want 2026-05-03", got)
	}

	// The generated binding wrapper rejects invalid dates with 400
	// before the handler runs.
	before := store.queryCount
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/costs/daily?start=bogus", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid date status = %d, want 400", rec.Code)
	}
	if store.queryCount != before {
		t.Error("handler queried the store despite an invalid date param")
	}

	// Empty store: empty days array (not null), zero total, no currency.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/costs/daily", nil))
	if body := strings.TrimSpace(rec.Body.String()); body != `{"currency":"","days":[],"total":"0"}` {
		t.Errorf("empty store response = %s", body)
	}
}

// TestGetDailyTokens covers the token-usage endpoint: default-tenant scoping,
// exact decimal-string quantities (the float-hazard count survives), the store
// ordering rendered verbatim, and every field mapped.
func TestGetDailyTokens(t *testing.T) {
	// A 19-digit token count that float64 cannot represent exactly (> 2^53):
	// it must survive as its exact decimal string end to end (decisions D23/D25).
	const floatHazard = "1234567890125856789"
	store := &fakeStore{tokens: []storage.DailyTokenUsage{
		{
			Date:         time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
			ServiceName:  "Claude API",
			ConsumedUnit: "Tokens",
			Quantity:     dec(t, "4400000"),
		},
		{
			Date:         time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
			ServiceName:  "OpenAI API",
			ConsumedUnit: "Tokens",
			Quantity:     dec(t, floatHazard),
		},
	}}
	handler := NewHandler("0.1.0-test", testStatic(), store)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/usage/tokens/daily", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	// Tenant-scoped to the default tenant, exactly like the costs endpoint —
	// no tenant query param exists.
	if store.gotTokenTenant != "default" {
		t.Errorf("queried tenant %q, want default", store.gotTokenTenant)
	}
	if !store.gotTokenStart.IsZero() || !store.gotTokenEnd.IsZero() {
		t.Errorf("default range = [%s, %s], want unbounded (zero times)", store.gotTokenStart, store.gotTokenEnd)
	}

	var got []DailyTokenUsage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	want := []DailyTokenUsage{
		{Date: got[0].Date, ServiceName: "Claude API", ConsumedUnit: "Tokens", ConsumedQuantity: "4400000"},
		{Date: got[1].Date, ServiceName: "OpenAI API", ConsumedUnit: "Tokens", ConsumedQuantity: floatHazard},
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %+v, want %d", got, len(want))
	}
	for i := range want {
		if got[i].Date.Format(time.DateOnly) != "2026-05-01" {
			t.Errorf("row %d date = %s, want 2026-05-01", i, got[i].Date)
		}
		if got[i].ServiceName != want[i].ServiceName || got[i].ConsumedUnit != want[i].ConsumedUnit ||
			got[i].ConsumedQuantity != want[i].ConsumedQuantity {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// The float-hazard quantity is rendered as its EXACT decimal string in the
	// raw JSON (a float64 would have rounded it).
	if !strings.Contains(rec.Body.String(), `"consumedQuantity":"`+floatHazard+`"`) {
		t.Errorf("float-hazard quantity not exact in body: %s", rec.Body)
	}
}

// TestGetDailyTokensDateParams covers the date query params, the 400 on a
// malformed date (before the store is touched), and the empty-store response
// being `[]` (not null).
func TestGetDailyTokensDateParams(t *testing.T) {
	store := &fakeStore{}
	handler := NewHandler("0.1.0-test", testStatic(), store)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/usage/tokens/daily?start=2026-05-02&end=2026-05-03", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	if got := store.gotTokenStart.Format(time.DateOnly); got != "2026-05-02" {
		t.Errorf("start = %s, want 2026-05-02", got)
	}
	if got := store.gotTokenEnd.Format(time.DateOnly); got != "2026-05-03" {
		t.Errorf("end = %s, want 2026-05-03", got)
	}

	// The generated binding wrapper rejects invalid dates with 400 before the
	// handler runs — the store is never queried.
	before := store.tokenQueryCount
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/usage/tokens/daily?end=bogus", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid date status = %d, want 400", rec.Code)
	}
	if store.tokenQueryCount != before {
		t.Error("handler queried the store despite an invalid date param")
	}

	// Empty store: a JSON empty array, not null.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/usage/tokens/daily", nil))
	if body := strings.TrimSpace(rec.Body.String()); body != `[]` {
		t.Errorf("empty store response = %s, want []", body)
	}
}

// TestGetDailyUsageMetrics covers the usage-metrics endpoint: default-tenant
// scoping, every field mapped (including serviceTier="" and unit="Unknown"), an
// exact decimal-string quantity beyond float64 range, and the store ordering
// rendered verbatim.
func TestGetDailyUsageMetrics(t *testing.T) {
	// A 19-digit quantity float64 cannot represent exactly (> 2^53).
	const floatHazard = "1234567890125856789"
	store := &fakeStore{usage: []storage.DailyUsageMetric{
		{
			Date:        time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
			ServiceName: "claude-opus-4-6",
			ServiceTier: "priority",
			MetricName:  "uncached_input_tokens",
			Unit:        "Tokens",
			Quantity:    dec(t, floatHazard),
		},
		{
			Date:        time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC),
			ServiceName: "OpenAI API",
			ServiceTier: "", // OpenAI has no tier concept
			MetricName:  "assistants api | file search",
			Unit:        "Unknown",
			Quantity:    dec(t, "42"),
		},
	}}
	handler := NewHandler("0.1.0-test", testStatic(), store)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/usage/metrics/daily", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	if store.gotUsageTenant != "default" {
		t.Errorf("queried tenant %q, want default", store.gotUsageTenant)
	}
	if !store.gotUsageStart.IsZero() || !store.gotUsageEnd.IsZero() {
		t.Errorf("default range = [%s, %s], want unbounded (zero times)", store.gotUsageStart, store.gotUsageEnd)
	}

	var got []DailyUsageMetric
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	want := []DailyUsageMetric{
		{Date: got[0].Date, ServiceName: "claude-opus-4-6", ServiceTier: "priority", MetricName: "uncached_input_tokens", Unit: "Tokens", Quantity: floatHazard},
		{Date: got[1].Date, ServiceName: "OpenAI API", ServiceTier: "", MetricName: "assistants api | file search", Unit: "Unknown", Quantity: "42"},
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %+v, want %d", got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// serviceTier="" is present in the JSON (a required field, not omitted), and
	// the float-hazard quantity is the exact decimal string.
	if !strings.Contains(rec.Body.String(), `"serviceTier":""`) {
		t.Errorf("empty serviceTier not emitted in body: %s", rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"quantity":"`+floatHazard+`"`) {
		t.Errorf("float-hazard quantity not exact in body: %s", rec.Body)
	}
}

// TestGetDailyUsageMetricsDateParams covers the date query params, the 400 on a
// malformed date (before the store is touched), and the empty-store response
// being `[]` (not null).
func TestGetDailyUsageMetricsDateParams(t *testing.T) {
	store := &fakeStore{}
	handler := NewHandler("0.1.0-test", testStatic(), store)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/usage/metrics/daily?start=2026-05-02&end=2026-05-03", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	if got := store.gotUsageStart.Format(time.DateOnly); got != "2026-05-02" {
		t.Errorf("start = %s, want 2026-05-02", got)
	}
	if got := store.gotUsageEnd.Format(time.DateOnly); got != "2026-05-03" {
		t.Errorf("end = %s, want 2026-05-03", got)
	}

	// The generated binding wrapper rejects invalid dates with 400 before the
	// handler runs — the store is never queried.
	before := store.usageQueryCount
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/usage/metrics/daily?start=bogus", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid date status = %d, want 400", rec.Code)
	}
	if store.usageQueryCount != before {
		t.Error("handler queried the store despite an invalid date param")
	}

	// Empty store: a JSON empty array, not null.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/usage/metrics/daily", nil))
	if body := strings.TrimSpace(rec.Body.String()); body != `[]` {
		t.Errorf("empty store response = %s, want []", body)
	}
}
