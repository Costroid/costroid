// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/allocation"
	"github.com/Costroid/costroid/internal/storage"
)

// groupByUnset is an impossible CostGroupBy sentinel (not GroupByService,
// which is the iota zero value): the fake seeds it when the variadic groupBy
// arrives empty, so a test asserting gotGroupBy == GroupByService genuinely
// proves the handler passed "service" explicitly rather than merely defaulting.
const groupByUnset = storage.CostGroupBy(-1)

// fakeStore records the query it received and returns canned costs.
type fakeStore struct {
	daily                storage.DailyCosts
	dailyErr             error
	currencies           []string
	currenciesErr        error
	gotTenant            string
	gotStart             time.Time
	gotEnd               time.Time
	gotCurrency          string
	gotGroupBy           storage.CostGroupBy
	queryCount           int
	gotCurrenciesTenant  string
	gotCurrenciesStart   time.Time
	gotCurrenciesEnd     time.Time
	currenciesQueryCount int

	// dailyFn, when set, overrides daily/dailyErr for both service and
	// allocation queries so multi-window handlers can return different data
	// per [start,end].
	dailyFn func(tenant string, start, end time.Time, groupBy storage.CostGroupBy) (storage.DailyCosts, error)
	// dailyCurrencyFn additionally receives the selected currency and takes
	// precedence over dailyFn when set.
	dailyCurrencyFn func(tenant string, start, end time.Time, currency string, groupBy storage.CostGroupBy) (storage.DailyCosts, error)

	// queryLog records every DailyCostsByService/Allocation call.
	queryLog []struct {
		start, end time.Time
		currency   string
	}

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

	businessInfos          []storage.BusinessMetricInfo
	businessNamesErr       error
	gotBusinessNamesTenant string
	businessNamesCount     int
	businessQuantities     []storage.DayQuantity
	businessQuantitiesErr  error
	gotBusinessTenant      string
	gotBusinessMetric      string
	gotBusinessStart       time.Time
	gotBusinessEnd         time.Time
	businessQuantityCount  int

	// allocation query recording: the validated dimension the handler passed,
	// asserted per-parameter (rule content), plus an invocation count.
	gotDimension    allocation.Dimension
	dimensionLog    []allocation.Dimension
	allocQueryCount int
}

func (f *fakeStore) BillingCurrencies(_ context.Context, tenant string, start, end time.Time) ([]string, error) {
	f.gotCurrenciesTenant = tenant
	f.gotCurrenciesStart = start
	f.gotCurrenciesEnd = end
	f.currenciesQueryCount++
	if f.currenciesErr != nil {
		return nil, f.currenciesErr
	}
	if f.currencies != nil {
		return append([]string{}, f.currencies...), nil
	}
	if f.daily.Currency != "" {
		return []string{f.daily.Currency}, nil
	}
	return []string{}, nil
}

func (f *fakeStore) DailyCostsByAllocation(_ context.Context, tenant string, start, end time.Time, dim allocation.Dimension, currency string) (storage.DailyCosts, error) {
	f.gotTenant, f.gotStart, f.gotEnd = tenant, start, end
	f.gotCurrency = currency
	f.gotDimension = dim
	f.dimensionLog = append(f.dimensionLog, dim)
	f.allocQueryCount++
	f.queryLog = append(f.queryLog, struct {
		start, end time.Time
		currency   string
	}{start: start, end: end, currency: currency})
	if f.dailyCurrencyFn != nil {
		return f.dailyCurrencyFn(tenant, start, end, currency, storage.GroupByService)
	}
	if f.dailyFn != nil {
		return f.dailyFn(tenant, start, end, storage.GroupByService)
	}
	return f.daily, f.dailyErr
}

func (f *fakeStore) DailyCostsByService(_ context.Context, tenant string, start, end time.Time, currency string, groupBy ...storage.CostGroupBy) (storage.DailyCosts, error) {
	f.gotTenant, f.gotStart, f.gotEnd = tenant, start, end
	f.gotCurrency = currency
	f.gotGroupBy = groupByUnset
	if len(groupBy) > 0 {
		f.gotGroupBy = groupBy[0]
	}
	f.queryCount++
	f.queryLog = append(f.queryLog, struct {
		start, end time.Time
		currency   string
	}{start: start, end: end, currency: currency})
	if f.dailyCurrencyFn != nil {
		gb := groupByUnset
		if len(groupBy) > 0 {
			gb = groupBy[0]
		}
		return f.dailyCurrencyFn(tenant, start, end, currency, gb)
	}
	if f.dailyFn != nil {
		gb := groupByUnset
		if len(groupBy) > 0 {
			gb = groupBy[0]
		}
		return f.dailyFn(tenant, start, end, gb)
	}
	return f.daily, f.dailyErr
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

func (f *fakeStore) BusinessMetricNames(_ context.Context, tenant string) ([]storage.BusinessMetricInfo, error) {
	f.gotBusinessNamesTenant = tenant
	f.businessNamesCount++
	return f.businessInfos, f.businessNamesErr
}

func (f *fakeStore) DailyBusinessMetricQuantities(_ context.Context, tenant, metric string, start, end time.Time) ([]storage.DayQuantity, error) {
	f.gotBusinessTenant, f.gotBusinessMetric = tenant, metric
	f.gotBusinessStart, f.gotBusinessEnd = start, end
	f.businessQuantityCount++
	return f.businessQuantities, f.businessQuantitiesErr
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
	handler := NewHandler("0.1.0-test", testStatic(), &fakeStore{}, "")

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
	handler := NewHandler("1.2.3-test", testStatic(), &fakeStore{}, "")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var meta Meta
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatalf("unmarshaling body %q: %v", rec.Body, err)
	}
	want := Meta{Name: "costroid", Version: "1.2.3-test", FocusVersion: "1.4", Demo: false}
	if meta != want {
		t.Errorf("meta = %+v, want %+v", meta, want)
	}
}

func TestWithDemoMarksMetaOnlyWhenEnabled(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []HandlerOption
		want bool
	}{
		{name: "normal", want: false},
		{name: "demo", opts: []HandlerOption{WithDemo()}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			NewHandler("test", testStatic(), &fakeStore{}, "", tc.opts...).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil))
			var got Meta
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Demo != tc.want {
				t.Errorf("demo = %v, want %v", got.Demo, tc.want)
			}
		})
	}
}

// TestReadOnlyMiddleware is deliberately independent of generated routes: its
// permissive inner handler accepts every method, proving the 405 comes only
// from the outer read-only guard. Calling the inner handler without the guard
// proves POST would otherwise succeed.
func TestReadOnlyMiddleware(t *testing.T) {
	permissive := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		readOnly(permissive).ServeHTTP(rec, httptest.NewRequest(method, "/api/x", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/x = %d, want 405", method, rec.Code)
		}
	}

	for _, request := range []struct{ method, path string }{
		{http.MethodGet, "/api/x"},
		{http.MethodHead, "/api/x"},
		{http.MethodPost, "/healthz"},
		{http.MethodPost, "/"},
	} {
		rec := httptest.NewRecorder()
		readOnly(permissive).ServeHTTP(rec, httptest.NewRequest(request.method, request.path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s %s = %d, want 200", request.method, request.path, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	permissive.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/x", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("unguarded POST /api/x = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	NewHandler("test", testStatic(), &fakeStore{}, "", WithReadOnly()).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/x", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("WithReadOnly POST /api/x = %d, want 405", rec.Code)
	}
}

func TestGetDailyCostsSingleCurrencyResponseAddsCurrenciesOnly(t *testing.T) {
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
	handler := NewHandler("0.1.0-test", testStatic(), store, "")

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
	if store.gotCurrency != "USD" {
		t.Errorf("selected currency = %q, want USD", store.gotCurrency)
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
	if len(day0.Services) != 2 || day0.Services[0].Key != "AWS Lambda" || day0.Services[0].Cost != "0.1896" {
		t.Errorf("day 0 services = %+v", day0.Services)
	}
	if got.Days[1].Total != "0.1896" {
		t.Errorf("day 1 total = %q, want 0.1896", got.Days[1].Total)
	}

	var want DailyCosts
	if err := json.Unmarshal([]byte(`{"currencies":["USD"],"currency":"USD","days":[{"date":"2026-05-01","services":[{"cost":"0.1896","key":"AWS Lambda"},{"cost":"3.6288","key":"Amazon Elastic Compute Cloud"}],"total":"3.8184"},{"date":"2026-05-02","services":[{"cost":"0.1896","key":"AWS Lambda"}],"total":"0.1896"}],"total":"4.008"}`), &want); err != nil {
		t.Fatalf("decoding expected full response: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("full decoded response = %+v, want %+v", got, want)
	}
}

func TestGetDailyCostsMixedCurrenciesDefaultsToAlphabeticallyFirst(t *testing.T) {
	store := &fakeStore{
		currencies: []string{"EUR", "USD"},
		dailyCurrencyFn: func(_ string, _, _ time.Time, currency string, groupBy storage.CostGroupBy) (storage.DailyCosts, error) {
			if currency != "EUR" {
				return storage.DailyCosts{}, fmt.Errorf("selected currency = %q, want EUR", currency)
			}
			if groupBy != storage.GroupByService {
				return storage.DailyCosts{}, fmt.Errorf("groupBy = %v, want service", groupBy)
			}
			return dayCosts(t, "2026-05-01", "EUR", map[string]string{
				"Compute": "1.123456789012345678",
				"Storage": "2.000000000000000001",
			}), nil
		},
	}
	handler := NewHandler("0.1.0-test", testStatic(), store, "")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/costs/daily", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	var got DailyCosts
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Currencies, []string{"EUR", "USD"}) {
		t.Fatalf("currencies = %v, want [EUR USD]", got.Currencies)
	}
	if got.Currency != "EUR" || got.Total != "3.123456789012345679" {
		t.Fatalf("default series = %s %s, want exact EUR 3.123456789012345679", got.Total, got.Currency)
	}
	if len(got.Days) != 1 || got.Days[0].Total != "3.123456789012345679" {
		t.Fatalf("days = %+v, want exact EUR-only day", got.Days)
	}
}

func TestGetDailyCostsEmptyRangeCurrenciesNeverNull(t *testing.T) {
	store := &fakeStore{currencies: []string{}}
	handler := NewHandler("0.1.0-test", testStatic(), store, "")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/costs/daily", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	want := `{"currencies":[],"currency":"","days":[],"total":"0"}`
	if body := strings.TrimSpace(rec.Body.String()); body != want {
		t.Fatalf("empty response = %s, want %s", body, want)
	}
}

func TestGetDailyCostsCurrencyParam(t *testing.T) {
	newStore := func() *fakeStore {
		return &fakeStore{
			currencies: []string{"EUR", "USD"},
			dailyCurrencyFn: func(_ string, _, _ time.Time, currency string, _ storage.CostGroupBy) (storage.DailyCosts, error) {
				switch currency {
				case "USD":
					return dayCosts(t, "2026-05-01", "USD", map[string]string{
						"Compute": "30.987654321098765434",
					}), nil
				case "GBP":
					return storage.DailyCosts{Currency: "GBP", Days: []storage.DayCosts{}}, nil
				default:
					return storage.DailyCosts{}, fmt.Errorf("unexpected currency %q", currency)
				}
			},
		}
	}

	t.Run("present currency returns its exact series", func(t *testing.T) {
		store := newStore()
		rec := httptest.NewRecorder()
		NewHandler("test", testStatic(), store, "").ServeHTTP(rec,
			httptest.NewRequest(http.MethodGet, "/api/v1/costs/daily?currency=USD", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; body: %s", rec.Code, rec.Body)
		}
		var got DailyCosts
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Currency != "USD" || got.Total != "30.987654321098765434" || store.gotCurrency != "USD" {
			t.Fatalf("USD response/store selection = %+v / %q", got, store.gotCurrency)
		}
	})

	t.Run("absent currency is a zero series with an echo", func(t *testing.T) {
		store := newStore()
		rec := httptest.NewRecorder()
		NewHandler("test", testStatic(), store, "").ServeHTTP(rec,
			httptest.NewRequest(http.MethodGet, "/api/v1/costs/daily?currency=GBP", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; body: %s", rec.Code, rec.Body)
		}
		var got DailyCosts
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Currency != "GBP" || got.Total != "0" || len(got.Days) != 0 || !reflect.DeepEqual(got.Currencies, []string{"EUR", "USD"}) {
			t.Fatalf("GBP response = %+v, want echoed empty series and full currencies", got)
		}
	})

	for _, query := range []string{"?currency=usd", "?currency=USDX"} {
		t.Run("invalid "+query, func(t *testing.T) {
			store := newStore()
			rec := httptest.NewRecorder()
			NewHandler("test", testStatic(), store, "").ServeHTTP(rec,
				httptest.NewRequest(http.MethodGet, "/api/v1/costs/daily"+query, nil))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body)
			}
			if body := strings.TrimSpace(rec.Body.String()); body != "currency must be a three-letter uppercase code (for example, USD)" {
				t.Fatalf("body = %q", body)
			}
			if store.currenciesQueryCount != 0 || store.queryCount != 0 {
				t.Fatalf("invalid currency reached store: currency queries=%d daily queries=%d", store.currenciesQueryCount, store.queryCount)
			}
		})
	}
}

func TestGetDailyCostsBillingCurrenciesErrorIs500(t *testing.T) {
	for _, tt := range []struct {
		name string
		path string
	}{
		{name: "service", path: "/api/v1/costs/daily"},
		{name: "precedes allocation configuration", path: "/api/v1/costs/daily?groupBy=allocation"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{currenciesErr: errors.New("catalog unavailable")}
			rec := httptest.NewRecorder()
			NewHandler("test", testStatic(), store, "").ServeHTTP(rec,
				httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500; body: %s", rec.Code, rec.Body)
			}
			if body := strings.TrimSpace(rec.Body.String()); body != "querying daily costs: catalog unavailable" {
				t.Fatalf("body = %q", body)
			}
			if store.currenciesQueryCount != 1 || store.queryCount != 0 || store.allocQueryCount != 0 {
				t.Fatalf("query counts after BillingCurrencies failure = currencies:%d service:%d allocation:%d, want 1/0/0",
					store.currenciesQueryCount, store.queryCount, store.allocQueryCount)
			}
		})
	}
}

func TestGetDailyCostsAllocationUnconfiguredAfterHealthyCurrencyLookupIs400(t *testing.T) {
	store := &fakeStore{currencies: []string{"EUR", "USD"}}
	rec := httptest.NewRecorder()
	NewHandler("test", testStatic(), store, "").ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/v1/costs/daily?groupBy=allocation", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body)
	}
	want := "no allocation rules configured (start serve with --allocation-rules or set $COSTROID_ALLOCATION_RULES)"
	if body := strings.TrimSpace(rec.Body.String()); body != want {
		t.Fatalf("body = %q, want %q", body, want)
	}
	if store.currenciesQueryCount != 1 || store.allocQueryCount != 0 {
		t.Fatalf("currency/allocation query counts = %d/%d, want 1/0", store.currenciesQueryCount, store.allocQueryCount)
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
			handler := NewHandler("0.1.0-test", testStatic(), store, "")

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
	handler := NewHandler("0.1.0-test", testStatic(), store, "")

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
	if store.gotCurrenciesTenant != "default" ||
		store.gotCurrenciesStart.Format(time.DateOnly) != "2026-05-02" ||
		store.gotCurrenciesEnd.Format(time.DateOnly) != "2026-05-03" {
		t.Errorf("BillingCurrencies request = tenant %q [%s, %s], want default [2026-05-02, 2026-05-03]",
			store.gotCurrenciesTenant, store.gotCurrenciesStart, store.gotCurrenciesEnd)
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
	if body := strings.TrimSpace(rec.Body.String()); body != `{"currencies":[],"currency":"","days":[],"total":"0"}` {
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
	handler := NewHandler("0.1.0-test", testStatic(), store, "")

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
	handler := NewHandler("0.1.0-test", testStatic(), store, "")

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
	handler := NewHandler("0.1.0-test", testStatic(), store, "")

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
	handler := NewHandler("0.1.0-test", testStatic(), store, "")

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

// writeRules writes an allocation rules file into a temp dir and returns its
// path (never the developer's real config dir).
func writeRules(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "allocation.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("writing rules file: %v", err)
	}
	return p
}

// TestGetDailyCostsAllocation covers the happy path: the handler reads, parses,
// and validates the rules file per request, propagates the rule CONTENT to the
// store per-parameter (labels, operators, values — not merely "was called"),
// and renders the response keyed by "key" (the D41(d) rename).
func TestGetDailyCostsAllocation(t *testing.T) {
	store := &fakeStore{daily: storage.DailyCosts{
		Currency: "USD",
		Days: []storage.DayCosts{{
			Date: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
			Services: []storage.ServiceCost{
				{ServiceName: "platform", Cost: dec(t, "1.5")},
				{ServiceName: "Unallocated", Cost: dec(t, "0.25")},
			},
		}},
	}}
	rules := writeRules(t, `{"dimensions":[{"name":"team","rules":[
		{"label":"platform","match":[
			{"dimension":"service_name","operator":"starts_with","value":"Amazon EC2"},
			{"dimension":"tag:env","operator":"equals","value":"prod"}
		]},
		{"label":"data","match":[{"dimension":"service_category","operator":"one_of","values":["Analytics","Databases"]}]}
	]}]}`)
	handler := NewHandler("0.1.0-test", testStatic(), store, rules)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/costs/daily?groupBy=allocation", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}

	// Per-parameter assertion that the parsed rule content propagated.
	if store.allocQueryCount != 1 {
		t.Fatalf("allocation query count = %d, want 1", store.allocQueryCount)
	}
	d := store.gotDimension
	if d.Name != "team" || len(d.Rules) != 2 {
		t.Fatalf("dimension = %+v, want team with 2 rules", d)
	}
	if d.Rules[0].Label != "platform" || len(d.Rules[0].Match) != 2 {
		t.Fatalf("rule 0 = %+v, want platform with 2 conditions", d.Rules[0])
	}
	if c := d.Rules[0].Match[0]; c.Dimension != "service_name" || c.Operator != allocation.OpStartsWith || c.Value == nil || *c.Value != "Amazon EC2" {
		t.Errorf("rule 0 cond 0 = %+v, want service_name starts_with Amazon EC2", c)
	}
	if c := d.Rules[0].Match[1]; c.Dimension != "tag:env" || c.Operator != allocation.OpEquals || c.Value == nil || *c.Value != "prod" {
		t.Errorf("rule 0 cond 1 = %+v, want tag:env equals prod", c)
	}
	if c := d.Rules[1].Match[0]; c.Operator != allocation.OpOneOf || len(c.Values) != 2 || c.Values[0] != "Analytics" || c.Values[1] != "Databases" {
		t.Errorf("rule 1 cond 0 = %+v, want one_of [Analytics Databases]", c)
	}

	// Response is keyed by "key" and carries the fake's data verbatim.
	var got DailyCosts
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(got.Days) != 1 || len(got.Days[0].Services) != 2 {
		t.Fatalf("days = %+v, want 1 day with 2 keys", got.Days)
	}
	if got.Days[0].Services[0].Key != "platform" || got.Days[0].Services[0].Cost != "1.5" {
		t.Errorf("key 0 = %+v, want platform/1.5", got.Days[0].Services[0])
	}
	if got.Days[0].Services[1].Key != "Unallocated" || got.Days[0].Services[1].Cost != "0.25" {
		t.Errorf("key 1 = %+v, want Unallocated/0.25", got.Days[0].Services[1])
	}
	if !strings.Contains(rec.Body.String(), `"key":"platform"`) || strings.Contains(rec.Body.String(), `"serviceName"`) {
		t.Errorf("response body must be keyed by 'key', not 'serviceName': %s", rec.Body)
	}
}

// TestGetDailyCostsAllocationErrors covers the three degrade branches: the two
// 400s (exact bodies) and the 500 (prefix + offending-field substring). The
// healthy currency lookup precedes them, but none reaches cost aggregation.
func TestGetDailyCostsAllocationErrors(t *testing.T) {
	get := func(t *testing.T, rulesPath string) (*fakeStore, *httptest.ResponseRecorder) {
		t.Helper()
		store := &fakeStore{}
		handler := NewHandler("0.1.0-test", testStatic(), store, rulesPath)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/costs/daily?groupBy=allocation", nil))
		return store, rec
	}

	t.Run("unconfigured is 400", func(t *testing.T) {
		store, rec := get(t, "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body)
		}
		if body := strings.TrimSpace(rec.Body.String()); body != "no allocation rules configured (start serve with --allocation-rules or set $COSTROID_ALLOCATION_RULES)" {
			t.Errorf("body = %q", body)
		}
		if store.allocQueryCount != 0 {
			t.Error("store queried despite the unconfigured 400")
		}
	})

	t.Run("missing file is 400 naming the path", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "nope.json")
		store, rec := get(t, missing)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body)
		}
		want := "allocation rules file not found: " + missing + " (create it, or start serve with --allocation-rules or set $COSTROID_ALLOCATION_RULES)"
		if body := strings.TrimSpace(rec.Body.String()); body != want {
			t.Errorf("body = %q, want %q", body, want)
		}
		if store.allocQueryCount != 0 {
			t.Error("store queried despite the missing-file 400")
		}
	})

	t.Run("unreadable path is 500 from os.Open", func(t *testing.T) {
		regular := writeRules(t, `{"dimensions":[{"name":"team","rules":[]}]}`)
		unreadable := filepath.Join(regular, "child.json")
		store, rec := get(t, unreadable)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500; body: %s", rec.Code, rec.Body)
		}
		body := strings.TrimSpace(rec.Body.String())
		if !strings.HasPrefix(body, "loading allocation rules:") || !strings.Contains(body, unreadable) || !strings.Contains(body, "not a directory") {
			t.Errorf("body = %q, want load prefix, path, and ENOTDIR message", body)
		}
		if store.allocQueryCount != 0 {
			t.Error("store queried despite the unreadable-path 500")
		}
	})

	t.Run("invalid file is 500 with prefix and offending field", func(t *testing.T) {
		rules := writeRules(t, `{"dimensions":[{"name":"team","rules":[{"label":"x","match":[{"dimension":"service_name","operater":"equals","value":"y"}]}]}]}`)
		store, rec := get(t, rules)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500; body: %s", rec.Code, rec.Body)
		}
		body := rec.Body.String()
		if !strings.HasPrefix(strings.TrimSpace(body), "loading allocation rules:") {
			t.Errorf("body = %q, want the 'loading allocation rules:' prefix", body)
		}
		if !strings.Contains(body, "operater") {
			t.Errorf("body = %q, want it to name the offending field 'operater'", body)
		}
		if store.allocQueryCount != 0 {
			t.Error("store queried despite the invalid-file 500")
		}
	})
}

// dayCosts builds a single-day DailyCosts with the given key→cost map and currency.
func dayCosts(t *testing.T, date string, currency string, keys map[string]string) storage.DailyCosts {
	t.Helper()
	d, err := time.Parse(time.DateOnly, date)
	if err != nil {
		t.Fatal(err)
	}
	svcs := make([]storage.ServiceCost, 0, len(keys))
	// Stable order for readability (handler re-aggregates anyway).
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		svcs = append(svcs, storage.ServiceCost{ServiceName: k, Cost: dec(t, keys[k])})
	}
	return storage.DailyCosts{
		Currency: currency,
		Days:     []storage.DayCosts{{Date: d, Services: svcs}},
	}
}

// mergeDays concatenates DailyCosts windows (same currency required by caller).
func mergeDays(currency string, parts ...storage.DailyCosts) storage.DailyCosts {
	out := storage.DailyCosts{Currency: currency}
	for _, p := range parts {
		out.Days = append(out.Days, p.Days...)
	}
	return out
}

func TestGetCostsSummaryKeyTotalsSumToTotal(t *testing.T) {
	// 18-fractional-digit seed: keys must sum EXACTLY to total via decimal strings.
	store := &fakeStore{daily: mergeDays("USD",
		dayCosts(t, "2026-05-01", "USD", map[string]string{
			"Compute": "1.123456789012345678",
			"Storage": "2.876543210987654322",
		}),
		dayCosts(t, "2026-05-02", "USD", map[string]string{
			"Compute": "0.000000000000000001",
			"Network": "4",
		}),
	)}
	handler := NewHandler("0.1.0-test", testStatic(), store, "")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/costs/summary?start=2026-05-01&end=2026-05-02", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	var got CostsSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	sum := decimal.Zero
	for _, k := range got.Keys {
		sum = sum.Add(dec(t, k.Total))
	}
	if sum.String() != got.Total {
		t.Fatalf("keys sum %s != total %s", sum.String(), got.Total)
	}
	// Exact expected grand total.
	want := dec(t, "1.123456789012345678").
		Add(dec(t, "2.876543210987654322")).
		Add(dec(t, "0.000000000000000001")).
		Add(dec(t, "4"))
	if got.Total != want.String() {
		t.Fatalf("total = %q, want %q", got.Total, want.String())
	}
}

func TestGetCostsSummaryTotalEqualsDaily(t *testing.T) {
	store := &fakeStore{daily: dayCosts(t, "2026-05-01", "USD", map[string]string{
		"A": "10.123456789012345678",
		"B": "0.000000000000000001",
	})}
	handler := NewHandler("0.1.0-test", testStatic(), store, "")

	recSum := httptest.NewRecorder()
	handler.ServeHTTP(recSum, httptest.NewRequest(http.MethodGet, "/api/v1/costs/summary?start=2026-05-01&end=2026-05-01", nil))
	recDaily := httptest.NewRecorder()
	handler.ServeHTTP(recDaily, httptest.NewRequest(http.MethodGet, "/api/v1/costs/daily?start=2026-05-01&end=2026-05-01", nil))
	if recSum.Code != 200 || recDaily.Code != 200 {
		t.Fatalf("statuses summary=%d daily=%d", recSum.Code, recDaily.Code)
	}
	var sum CostsSummary
	var daily DailyCosts
	if err := json.Unmarshal(recSum.Body.Bytes(), &sum); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if err := json.Unmarshal(recDaily.Body.Bytes(), &daily); err != nil {
		t.Fatalf("decode daily: %v", err)
	}
	if sum.Total == "" {
		t.Fatal("summary total decoded empty; equality would be vacuous")
	}
	if sum.Total != daily.Total {
		t.Fatalf("summary total %q != daily total %q", sum.Total, daily.Total)
	}
}

func TestGetCostsSummaryPrecedingWindowArithmetic(t *testing.T) {
	// Pin month-boundary case: start=2026-03-01, end=2026-03-31
	// duration = 30 days; prevEnd = 2026-02-28; prevStart = 2026-01-29.
	curStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	curEnd := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	wantPrevEnd := time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)
	wantPrevStart := time.Date(2026, 1, 29, 0, 0, 0, 0, time.UTC)

	store := &fakeStore{
		currencies: []string{"USD"},
		dailyFn: func(_ string, start, end time.Time, _ storage.CostGroupBy) (storage.DailyCosts, error) {
			if start.Equal(curStart) && end.Equal(curEnd) {
				return dayCosts(t, "2026-03-15", "USD", map[string]string{"A": "100"}), nil
			}
			if start.Equal(wantPrevStart) && end.Equal(wantPrevEnd) {
				return dayCosts(t, "2026-02-01", "USD", map[string]string{"A": "40"}), nil
			}
			return storage.DailyCosts{}, fmt.Errorf("unexpected range [%s,%s]", start, end)
		},
	}
	handler := NewHandler("0.1.0-test", testStatic(), store, "")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/costs/summary?start=2026-03-01&end=2026-03-31", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var got CostsSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.PreviousTotal == nil || *got.PreviousTotal != "40" {
		t.Fatalf("previousTotal = %v, want 40", got.PreviousTotal)
	}
	if got.PreviousStart == nil || got.PreviousStart.Format(time.DateOnly) != "2026-01-29" {
		t.Fatalf("previousStart = %v, want 2026-01-29", got.PreviousStart)
	}
	if got.PreviousEnd == nil || got.PreviousEnd.Format(time.DateOnly) != "2026-02-28" {
		t.Fatalf("previousEnd = %v, want 2026-02-28", got.PreviousEnd)
	}
	// Also pin the query log ranges.
	if len(store.queryLog) != 2 {
		t.Fatalf("query count = %d, want 2", len(store.queryLog))
	}
	if !store.queryLog[1].start.Equal(wantPrevStart) || !store.queryLog[1].end.Equal(wantPrevEnd) {
		t.Fatalf("prev query = [%s,%s], want [%s,%s]",
			store.queryLog[1].start, store.queryLog[1].end, wantPrevStart, wantPrevEnd)
	}
}

func TestGetCostsSummaryUnboundedOmitsPrevious(t *testing.T) {
	store := &fakeStore{daily: dayCosts(t, "2026-05-01", "USD", map[string]string{"A": "1"})}
	handler := NewHandler("0.1.0-test", testStatic(), store, "")
	rec := httptest.NewRecorder()
	// Unbounded: no start/end → previous fields ABSENT.
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/costs/summary", nil))
	if rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"previousTotal", "previousStart", "previousEnd"} {
		if _, ok := raw[key]; ok {
			t.Errorf("unbounded response has %s = %v; want field ABSENT", key, raw[key])
		}
	}
	keys, _ := raw["keys"].([]any)
	if len(keys) != 1 {
		t.Fatalf("keys = %v", keys)
	}
	k0 := keys[0].(map[string]any)
	if _, ok := k0["previousTotal"]; ok {
		t.Error("keys[0].previousTotal present; want absent")
	}
	if _, ok := k0["delta"]; ok {
		t.Error("keys[0].delta present; want absent")
	}
}

func TestGetCostsSummaryEmptyPrecedingWindowOmitsPrevious(t *testing.T) {
	// Bounded range whose preceding window returns zero rows (demo FULL shape).
	store := &fakeStore{
		currencies: []string{"USD"},
		dailyFn: func(_ string, start, end time.Time, _ storage.CostGroupBy) (storage.DailyCosts, error) {
			// Current window has data; previous is empty.
			if start.Format(time.DateOnly) == "2026-01-12" {
				return dayCosts(t, "2026-01-12", "USD", map[string]string{"A": "10"}), nil
			}
			return storage.DailyCosts{Currency: "", Days: nil}, nil
		},
	}
	handler := NewHandler("0.1.0-test", testStatic(), store, "")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/costs/summary?start=2026-01-12&end=2026-07-11", nil))
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var raw map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	for _, key := range []string{"previousTotal", "previousStart", "previousEnd"} {
		if _, ok := raw[key]; ok {
			t.Errorf("empty-prev response has %s; want ABSENT", key)
		}
	}
	// Never previousTotal "0".
	if body := rec.Body.String(); strings.Contains(body, `"previousTotal"`) {
		t.Errorf("body still contains previousTotal: %s", body)
	}
	assertKeysWithoutPreviousFields(t, raw)
}

// assertKeysWithoutPreviousFields pins that per-key previousTotal AND delta are
// absent, independently of the top-level fields — a regression that split the
// shared prevDefined flag and kept emitting per-key deltas must fail here.
func assertKeysWithoutPreviousFields(t *testing.T, raw map[string]any) {
	t.Helper()
	keys, ok := raw["keys"].([]any)
	if !ok || len(keys) == 0 {
		t.Fatalf("keys missing or empty in response: %v", raw)
	}
	for i, k := range keys {
		entry, ok := k.(map[string]any)
		if !ok {
			t.Fatalf("keys[%d] is not an object: %v", i, k)
		}
		for _, field := range []string{"previousTotal", "delta"} {
			if _, present := entry[field]; present {
				t.Errorf("keys[%d] has %s; want ABSENT", i, field)
			}
		}
	}
}

func TestGetCostsSummaryCurrencyFiltersBothWindowsAndEchoes(t *testing.T) {
	store := &fakeStore{
		currencies: []string{"EUR", "USD"},
		dailyCurrencyFn: func(_ string, start, _ time.Time, currency string, _ storage.CostGroupBy) (storage.DailyCosts, error) {
			if currency != "USD" {
				return storage.DailyCosts{}, fmt.Errorf("currency = %q, want USD", currency)
			}
			if start.Format(time.DateOnly) == "2026-06-01" {
				return dayCosts(t, "2026-06-01", currency, map[string]string{"A": "10"}), nil
			}
			return dayCosts(t, "2026-05-15", currency, map[string]string{"A": "4"}), nil
		},
	}
	rec := httptest.NewRecorder()
	NewHandler("test", testStatic(), store, "").ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/costs/summary?start=2026-06-01&end=2026-06-30&currency=USD", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var got CostsSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if got.Currency != "USD" || !reflect.DeepEqual(got.Currencies, []string{"EUR", "USD"}) {
		t.Fatalf("currency response = %q / %v, want USD / [EUR USD]", got.Currency, got.Currencies)
	}
	if len(got.Keys) != 1 || got.PreviousTotal == nil {
		t.Fatalf("summary comparison is vacuous: %+v", got)
	}
	if len(store.queryLog) != 2 {
		t.Fatalf("cost query count = %d, want current and preceding", len(store.queryLog))
	}
	for i, query := range store.queryLog {
		if query.currency != "USD" {
			t.Errorf("query %d currency = %q, want USD", i, query.currency)
		}
	}
}

func TestGetCostsSummaryCurrenciesPopulatedAndEmptyNonNil(t *testing.T) {
	t.Run("populated and default selects first", func(t *testing.T) {
		store := &fakeStore{
			currencies: []string{"EUR", "USD"},
			dailyCurrencyFn: func(_ string, _, _ time.Time, currency string, _ storage.CostGroupBy) (storage.DailyCosts, error) {
				return dayCosts(t, "2026-05-01", currency, map[string]string{"A": "1"}), nil
			},
		}
		rec := httptest.NewRecorder()
		NewHandler("test", testStatic(), store, "").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/costs/summary", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
		}
		var got CostsSummary
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode summary: %v", err)
		}
		if got.Currency != "EUR" || !reflect.DeepEqual(got.Currencies, []string{"EUR", "USD"}) || len(got.Keys) != 1 {
			t.Fatalf("summary = %+v, want non-vacuous EUR summary with [EUR USD]", got)
		}
	})

	t.Run("empty range returns non-nil empty list", func(t *testing.T) {
		store := &fakeStore{currencies: []string{}}
		rec := httptest.NewRecorder()
		NewHandler("test", testStatic(), store, "").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/costs/summary", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
		}
		var got CostsSummary
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode summary: %v", err)
		}
		if got.Currencies == nil || len(got.Currencies) != 0 || got.Keys == nil || len(got.Keys) != 0 || got.Currency != "" || got.Total != "0" {
			t.Fatalf("empty summary = %+v, want currencies/keys non-nil empty and total 0", got)
		}
		if !strings.Contains(rec.Body.String(), `"currencies":[]`) {
			t.Fatalf("empty response does not encode currencies as []: %s", rec.Body)
		}
	})
}

func TestGetCostsSummaryInvalidCurrencyIs400(t *testing.T) {
	store := &fakeStore{currencies: []string{"USD"}}
	rec := httptest.NewRecorder()
	NewHandler("test", testStatic(), store, "").ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/v1/costs/summary?currency=usd", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", rec.Code, rec.Body)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "currency must be a three-letter uppercase code (for example, USD)" {
		t.Fatalf("body=%q", body)
	}
	if store.currenciesQueryCount != 0 || store.queryCount != 0 || store.allocQueryCount != 0 {
		t.Fatalf("invalid currency reached store: currencies=%d service=%d allocation=%d",
			store.currenciesQueryCount, store.queryCount, store.allocQueryCount)
	}
}

func TestGetCostsSummaryCrossWindowPerCurrency(t *testing.T) {
	for _, tc := range []struct {
		name         string
		previous     storage.DailyCosts
		wantPrevious bool
	}{
		{
			name:         "selected currency rows emit comparison",
			previous:     dayCosts(t, "2026-05-15", "USD", map[string]string{"A": "4"}),
			wantPrevious: true,
		},
		{
			name:         "zero selected currency rows omit comparison",
			previous:     storage.DailyCosts{Currency: "USD", Days: []storage.DayCosts{}},
			wantPrevious: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{
				currencies: []string{"EUR", "USD"},
				dailyCurrencyFn: func(_ string, start, _ time.Time, currency string, _ storage.CostGroupBy) (storage.DailyCosts, error) {
					if currency != "USD" {
						return storage.DailyCosts{}, fmt.Errorf("currency = %q, want USD", currency)
					}
					if start.Format(time.DateOnly) == "2026-06-01" {
						return dayCosts(t, "2026-06-15", currency, map[string]string{"A": "10"}), nil
					}
					return tc.previous, nil
				},
			}
			rec := httptest.NewRecorder()
			NewHandler("test", testStatic(), store, "").ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
				"/api/v1/costs/summary?start=2026-06-01&end=2026-06-30&currency=USD", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
			}
			var got CostsSummary
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode summary: %v", err)
			}
			if got.Currency != "USD" || len(got.Keys) != 1 {
				t.Fatalf("summary is vacuous or wrong currency: %+v", got)
			}
			key := got.Keys[0]
			if tc.wantPrevious {
				if got.PreviousTotal == nil || *got.PreviousTotal != "4" || got.PreviousStart == nil || got.PreviousEnd == nil ||
					key.PreviousTotal == nil || *key.PreviousTotal != "4" || key.Delta == nil || *key.Delta != "6" {
					t.Fatalf("defined comparison = %+v / key %+v", got, key)
				}
			} else if got.PreviousTotal != nil || got.PreviousStart != nil || got.PreviousEnd != nil || key.PreviousTotal != nil || key.Delta != nil {
				t.Fatalf("zero-row preceding currency leaked comparison: %+v / key %+v", got, key)
			}
		})
	}
}

func TestGetCostsSummaryEmptyCurrentSkipsMixedPreceding(t *testing.T) {
	const guard = "stored records mix billing currencies (EUR, USD); currency conversion is not supported yet"
	store := &fakeStore{
		currencies: []string{},
		dailyCurrencyFn: func(_ string, start, _ time.Time, _ string, _ storage.CostGroupBy) (storage.DailyCosts, error) {
			if start.Format(time.DateOnly) == "2026-06-01" {
				return storage.DailyCosts{Currency: "", Days: []storage.DayCosts{}}, nil
			}
			return storage.DailyCosts{}, errors.New(guard)
		},
	}
	rec := httptest.NewRecorder()
	NewHandler("test", testStatic(), store, "").ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/costs/summary?start=2026-06-01&end=2026-06-30", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var got CostsSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if got.Currency != "" || got.Currencies == nil || len(got.Currencies) != 0 || got.Keys == nil || len(got.Keys) != 0 || got.Total != "0" ||
		got.PreviousTotal != nil || got.PreviousStart != nil || got.PreviousEnd != nil {
		t.Fatalf("empty-current response = %+v", got)
	}
	if len(store.queryLog) != 1 {
		t.Fatalf("cost queries = %d, want current only (mixed preceding must be skipped)", len(store.queryLog))
	}
}

func TestGetCostsSummaryValidAbsentCurrencyEchoesEmptySeries(t *testing.T) {
	store := &fakeStore{
		currencies: []string{"EUR", "USD"},
		dailyCurrencyFn: func(_ string, start, _ time.Time, currency string, _ storage.CostGroupBy) (storage.DailyCosts, error) {
			if start.Format(time.DateOnly) == "2026-06-01" {
				return storage.DailyCosts{Currency: currency, Days: []storage.DayCosts{}}, nil
			}
			// This selected currency exists in the preceding window. The empty
			// current window still has no keys to compare, so this query must never run.
			return dayCosts(t, "2026-05-15", currency, map[string]string{"A": "9"}), nil
		},
	}
	rec := httptest.NewRecorder()
	NewHandler("test", testStatic(), store, "").ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/v1/costs/summary?start=2026-06-01&end=2026-06-30&currency=JPY", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var got CostsSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if got.Currency != "JPY" || !reflect.DeepEqual(got.Currencies, []string{"EUR", "USD"}) || got.Keys == nil || len(got.Keys) != 0 || got.Total != "0" ||
		got.PreviousTotal != nil || got.PreviousStart != nil || got.PreviousEnd != nil {
		t.Fatalf("valid-absent response = %+v", got)
	}
	if len(store.queryLog) != 1 || store.queryLog[0].currency != "JPY" {
		t.Fatalf("store queries = %+v, want only the bounded current-window JPY query", store.queryLog)
	}
}

func TestNonDailyEndpointsDefaultToFirstCurrencyOnMixedStore(t *testing.T) {
	const guard = "stored records mix billing currencies (EUR, USD); currency conversion is not supported yet"
	tests := []struct {
		name string
		path string
	}{
		{name: "summary", path: "/api/v1/costs/summary"},
		{name: "anomalies", path: "/api/v1/anomalies"},
		{name: "unit economics", path: "/api/v1/unit-economics/daily?metric=requests"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{
				currencies:    []string{"EUR", "USD"},
				businessInfos: []storage.BusinessMetricInfo{{Name: "requests"}},
				dailyCurrencyFn: func(_ string, _, _ time.Time, currency string, _ storage.CostGroupBy) (storage.DailyCosts, error) {
					if currency == "" {
						return storage.DailyCosts{}, errors.New(guard)
					}
					return storage.DailyCosts{Currency: currency, Days: []storage.DayCosts{}}, nil
				},
			}
			rec := httptest.NewRecorder()
			NewHandler("test", testStatic(), store, "").ServeHTTP(rec,
				httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
			}
			var raw map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if len(raw) == 0 || raw["currency"] != "EUR" {
				t.Fatalf("response = %v, want non-vacuous EUR response", raw)
			}
			if store.currenciesQueryCount != 1 || store.gotCurrency != "EUR" {
				t.Fatalf("currency lookup/query = %d/%q, want 1/EUR", store.currenciesQueryCount, store.gotCurrency)
			}
		})
	}
}

func TestGetCostsSummaryNewKeyDeltaEqualsTotal(t *testing.T) {
	store := &fakeStore{
		currencies: []string{"USD"},
		dailyFn: func(_ string, start, end time.Time, _ storage.CostGroupBy) (storage.DailyCosts, error) {
			if start.Format(time.DateOnly) == "2026-06-01" {
				// Current: A (continuing) + B (new)
				return dayCosts(t, "2026-06-15", "USD", map[string]string{
					"A": "10.5",
					"B": "3.25",
				}), nil
			}
			// Previous: only A
			return dayCosts(t, "2026-05-01", "USD", map[string]string{"A": "4"}), nil
		},
	}
	handler := NewHandler("0.1.0-test", testStatic(), store, "")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/costs/summary?start=2026-06-01&end=2026-06-30", nil))
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var got CostsSummary
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	byKey := map[string]CostSummaryKey{}
	for _, k := range got.Keys {
		byKey[k.Key] = k
	}
	b := byKey["B"]
	if b.PreviousTotal != nil {
		t.Errorf("new key B has previousTotal=%v; want ABSENT", b.PreviousTotal)
	}
	if b.Delta == nil || *b.Delta != b.Total {
		t.Errorf("new key B delta=%v total=%s; want delta==total", b.Delta, b.Total)
	}
	a := byKey["A"]
	if a.PreviousTotal == nil || *a.PreviousTotal != "4" {
		t.Errorf("sibling A previousTotal=%v, want 4", a.PreviousTotal)
	}
	if a.Delta == nil || *a.Delta != "6.5" {
		t.Errorf("sibling A delta=%v, want 6.5", a.Delta)
	}
}

func TestGetCostsSummaryDeltaExactness(t *testing.T) {
	// 18-digit total − previousTotal asserted as exact string.
	cur := "10.123456789012345678"
	prev := "3.000000000000000001"
	wantDelta := dec(t, cur).Sub(dec(t, prev)).String()
	store := &fakeStore{
		currencies: []string{"USD"},
		dailyFn: func(_ string, start, end time.Time, _ storage.CostGroupBy) (storage.DailyCosts, error) {
			if start.Format(time.DateOnly) == "2026-06-01" {
				return dayCosts(t, "2026-06-01", "USD", map[string]string{"X": cur}), nil
			}
			return dayCosts(t, "2026-05-01", "USD", map[string]string{"X": prev}), nil
		},
	}
	handler := NewHandler("0.1.0-test", testStatic(), store, "")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/costs/summary?start=2026-06-01&end=2026-06-30", nil))
	var got CostsSummary
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Keys) != 1 || got.Keys[0].Delta == nil || *got.Keys[0].Delta != wantDelta {
		t.Fatalf("delta = %v, want %s (body %s)", got.Keys, wantDelta, rec.Body)
	}
}

func TestGetCostsSummaryOrderingTotalDescKeyAsc(t *testing.T) {
	// Tie on total between B and A → key asc (A before B); C highest first.
	store := &fakeStore{daily: dayCosts(t, "2026-05-01", "USD", map[string]string{
		"C": "30",
		"B": "10",
		"A": "10",
	})}
	handler := NewHandler("0.1.0-test", testStatic(), store, "")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/costs/summary", nil))
	var got CostsSummary
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Keys) != 3 {
		t.Fatalf("keys = %+v", got.Keys)
	}
	if got.Keys[0].Key != "C" || got.Keys[1].Key != "A" || got.Keys[2].Key != "B" {
		t.Fatalf("order = %s,%s,%s want C,A,B", got.Keys[0].Key, got.Keys[1].Key, got.Keys[2].Key)
	}
}

func TestGetCostsSummaryEmptyCurrent(t *testing.T) {
	store := &fakeStore{} // empty daily
	handler := NewHandler("0.1.0-test", testStatic(), store, "")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/costs/summary", nil))
	if body := strings.TrimSpace(rec.Body.String()); body != `{"currencies":[],"currency":"","keys":[],"total":"0"}` {
		t.Errorf("empty current = %s", body)
	}
}

func TestGetCostsSummaryAllocationRulesLoadedOncePerRequest(t *testing.T) {
	t.Run("both windows share one resolved dimension", func(t *testing.T) {
		rules := writeRules(t, `{"dimensions":[{"name":"team","rules":[{"label":"platform","match":[{"dimension":"service_name","operator":"equals","value":"Amazon EC2"}]}]}]}`)
		store := &fakeStore{currencies: []string{"USD"}}
		store.dailyCurrencyFn = func(_ string, start, _ time.Time, currency string, _ storage.CostGroupBy) (storage.DailyCosts, error) {
			if len(store.dimensionLog) == 1 {
				// A mid-request file replacement must not affect the preceding
				// window: the handler resolved the dimension before either query.
				if err := os.WriteFile(rules, []byte(`{"dimensions":[{"name":"owner","rules":[{"label":"data","match":[{"dimension":"service_name","operator":"equals","value":"Amazon S3"}]}]}]}`), 0o600); err != nil {
					t.Fatalf("replacing allocation rules: %v", err)
				}
			}
			if start.Format(time.DateOnly) == "2026-06-01" {
				return dayCosts(t, "2026-06-15", currency, map[string]string{"platform": "10"}), nil
			}
			return dayCosts(t, "2026-05-15", currency, map[string]string{"platform": "4"}), nil
		}

		rec := httptest.NewRecorder()
		NewHandler("test", testStatic(), store, rules).ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
			"/api/v1/costs/summary?start=2026-06-01&end=2026-06-30&groupBy=allocation", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
		}
		var got CostsSummary
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode summary: %v", err)
		}
		if got.PreviousTotal == nil || len(got.Keys) != 1 {
			t.Fatalf("comparison is vacuous: %+v", got)
		}
		if store.allocQueryCount != 2 || store.queryCount != 0 || len(store.dimensionLog) != 2 {
			t.Fatalf("allocation/service/dimension counts = %d/%d/%d, want 2/0/2",
				store.allocQueryCount, store.queryCount, len(store.dimensionLog))
		}
		for i, dim := range store.dimensionLog {
			if dim.Name != "team" || len(dim.Rules) != 1 || dim.Rules[0].Label != "platform" {
				t.Errorf("query %d dimension = %+v, want original team/platform rules", i, dim)
			}
		}
	})

	t.Run("non-allocation grouping never reads rules", func(t *testing.T) {
		malformed := writeRules(t, `{"dimensions":[{"name":"team","rules":[{"label":"x","match":[{"dimension":"service_name","operater":"equals","value":"y"}]}]}]}`)
		store := &fakeStore{daily: dayCosts(t, "2026-05-01", "USD", map[string]string{"provider": "1"})}
		rec := httptest.NewRecorder()
		NewHandler("test", testStatic(), store, malformed).ServeHTTP(rec,
			httptest.NewRequest(http.MethodGet, "/api/v1/costs/summary?groupBy=provider", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("provider status=%d body=%s", rec.Code, rec.Body)
		}
		if store.allocQueryCount != 0 || store.queryCount != 1 || store.gotGroupBy != storage.GroupByProvider {
			t.Fatalf("allocation/service/groupBy = %d/%d/%v, want 0/1/provider",
				store.allocQueryCount, store.queryCount, store.gotGroupBy)
		}
	})
}

func TestGetCostsSummaryAllocationErrors(t *testing.T) {
	t.Run("unconfigured is 400", func(t *testing.T) {
		store := &fakeStore{}
		handler := NewHandler("0.1.0-test", testStatic(), store, "")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/costs/summary?groupBy=allocation", nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
		}
		if store.allocQueryCount != 0 {
			t.Error("store queried despite unconfigured 400")
		}
	})
	t.Run("malformed is 500", func(t *testing.T) {
		rules := writeRules(t, `{"dimensions":[{"name":"team","rules":[{"label":"x","match":[{"dimension":"service_name","operater":"equals","value":"y"}]}]}]}`)
		store := &fakeStore{}
		handler := NewHandler("0.1.0-test", testStatic(), store, rules)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/costs/summary?groupBy=allocation", nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
		}
		if !strings.HasPrefix(strings.TrimSpace(rec.Body.String()), "loading allocation rules:") {
			t.Errorf("body = %s", rec.Body)
		}
	})
}
