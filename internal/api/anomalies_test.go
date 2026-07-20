// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/focus"
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
	if store.currenciesQueryCount != 1 || !store.gotCurrenciesStart.IsZero() || !store.gotCurrenciesEnd.IsZero() || store.gotCurrency != "USD" {
		t.Errorf("currency selection = count %d range [%s,%s] selected %q, want one unbounded lookup and USD",
			store.currenciesQueryCount, store.gotCurrenciesStart, store.gotCurrenciesEnd, store.gotCurrency)
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
	if p.K != "3" || p.ConsistencyConstant != "1.4826" || p.WindowDays != 30 || p.MinObservations != 10 || p.RelativeFloor != "0.1" || p.GroupBy != "service" || p.TagKey != "" {
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

func TestGetAnomaliesCurrencySelectionThreadsMixedHistory(t *testing.T) {
	const guard = "stored records mix billing currencies (EUR, USD); currency conversion is not supported yet"
	store := &fakeStore{
		currencies: []string{"EUR", "USD"},
		dailyCurrencyFn: func(_ string, _, _ time.Time, currency string, _ storage.CostGroupBy) (storage.DailyCosts, error) {
			if currency == "" {
				return storage.DailyCosts{}, errors.New(guard)
			}
			return storage.DailyCosts{Currency: currency, Days: []storage.DayCosts{}}, nil
		},
	}
	rec := httptest.NewRecorder()
	NewHandler("test", testStatic(), store, "").ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/v1/anomalies?end=2026-06-30&currency=USD", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var got Anomalies
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode anomalies: %v", err)
	}
	if got.Currency != "USD" || got.Anomalies == nil || len(got.Anomalies) != 0 {
		t.Fatalf("anomalies = %+v, want non-nil empty USD result", got)
	}
	if store.currenciesQueryCount != 1 || !store.gotCurrenciesStart.IsZero() || store.gotCurrenciesEnd.Format(time.DateOnly) != "2026-06-30" ||
		store.gotCurrency != "USD" || len(store.queryLog) != 1 || store.queryLog[0].currency != "USD" {
		t.Fatalf("currency lookup/query = count %d range [%s,%s] selected %q log %+v",
			store.currenciesQueryCount, store.gotCurrenciesStart, store.gotCurrenciesEnd, store.gotCurrency, store.queryLog)
	}
}

func TestGetAnomaliesMixedHistoryDefaultsToFirstCurrency(t *testing.T) {
	const guard = "stored records mix billing currencies (EUR, USD); currency conversion is not supported yet"
	store := &fakeStore{
		currencies: []string{"EUR", "USD"},
		dailyCurrencyFn: func(_ string, _, _ time.Time, currency string, _ storage.CostGroupBy) (storage.DailyCosts, error) {
			if currency == "" {
				return storage.DailyCosts{}, errors.New(guard)
			}
			return storage.DailyCosts{Currency: currency, Days: []storage.DayCosts{}}, nil
		},
	}
	rec := httptest.NewRecorder()
	NewHandler("test", testStatic(), store, "").ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/v1/anomalies", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var got Anomalies
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode anomalies: %v", err)
	}
	if got.Currency != "EUR" || got.Anomalies == nil {
		t.Fatalf("defaulted anomalies = %+v, want EUR and non-nil anomalies", got)
	}
	if store.currenciesQueryCount != 1 || store.gotCurrency != "EUR" {
		t.Fatalf("currency lookup/query = %d/%q, want 1/EUR", store.currenciesQueryCount, store.gotCurrency)
	}
}

func TestGetAnomaliesDefaultsToWindowFirstCurrency(t *testing.T) {
	const guard = "stored records mix billing currencies (AUD, USD); currency conversion is not supported yet"
	days := make([]storage.DayCosts, 0, 11)
	for n := 1; n <= 11; n++ {
		cost := "10"
		if n == 11 {
			cost = "100"
		}
		days = append(days, storage.DayCosts{
			Date: time.Date(2026, 6, n, 0, 0, 0, 0, time.UTC),
			Services: []storage.ServiceCost{
				{ServiceName: "Compute", Cost: dec(t, cost)},
			},
		})
	}
	store := &fakeStore{
		currenciesFn: func(_ string, start, _ time.Time, _ string) ([]string, error) {
			if start.IsZero() {
				return []string{"AUD", "USD"}, nil
			}
			return []string{"USD"}, nil
		},
		dailyCurrencyFn: func(_ string, _, _ time.Time, currency string, _ storage.CostGroupBy) (storage.DailyCosts, error) {
			if currency == "" {
				return storage.DailyCosts{}, errors.New(guard)
			}
			if currency == "USD" {
				return storage.DailyCosts{Currency: currency, Days: days}, nil
			}
			return storage.DailyCosts{Currency: currency}, nil
		},
	}
	rec := httptest.NewRecorder()
	NewHandler("test", testStatic(), store, "").ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/v1/anomalies?start=2026-06-01&end=2026-06-30", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var got Anomalies
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode anomalies: %v", err)
	}
	if got.Currency != "USD" {
		t.Errorf("currency = %q, want window-first USD", got.Currency)
	}
	if len(got.Anomalies) == 0 {
		t.Errorf("anomalies = %+v, want the June USD spike", got.Anomalies)
	} else if date := got.Anomalies[0].Date.Format(time.DateOnly); !strings.HasPrefix(date, "2026-06-") {
		t.Errorf("first anomaly date = %s, want a June 2026 date", date)
	}
	if store.gotCurrency != "USD" {
		t.Errorf("detection currency = %q, want USD", store.gotCurrency)
	}
	if store.currenciesQueryCount != 1 {
		t.Errorf("currency lookups = %d, want 1 for a non-empty window", store.currenciesQueryCount)
	}
}

func TestGetAnomaliesEmptyWindowFallsBackToFullHistoryCurrency(t *testing.T) {
	const guard = "stored records mix billing currencies (EUR, USD); currency conversion is not supported yet"
	store := &fakeStore{
		currenciesFn: func(_ string, start, _ time.Time, _ string) ([]string, error) {
			if start.IsZero() {
				return []string{"EUR", "USD"}, nil
			}
			return []string{}, nil
		},
		dailyCurrencyFn: func(_ string, _, _ time.Time, currency string, _ storage.CostGroupBy) (storage.DailyCosts, error) {
			if currency == "" {
				return storage.DailyCosts{}, errors.New(guard)
			}
			return storage.DailyCosts{Currency: currency}, nil
		},
	}
	rec := httptest.NewRecorder()
	NewHandler("test", testStatic(), store, "").ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/v1/anomalies?start=2026-09-01&end=2026-09-30", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var got Anomalies
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode anomalies: %v", err)
	}
	if got.Currency != "EUR" {
		t.Errorf("currency = %q, want full-history-first EUR", got.Currency)
	}
	if store.gotCurrency != "EUR" {
		t.Errorf("detection currency = %q, want EUR", store.gotCurrency)
	}
	if store.currenciesQueryCount != 2 {
		t.Errorf("currency lookups = %d, want 2 (window then full-history fallback)", store.currenciesQueryCount)
	}
	if !store.gotCurrenciesStart.IsZero() {
		t.Errorf("last currency lookup start = %s, want zero for full-history fallback", store.gotCurrenciesStart)
	}
}

func TestGetAnomaliesEmptyWindowFallbackErrorIs500(t *testing.T) {
	store := &fakeStore{
		currenciesFn: func(_ string, start, _ time.Time, _ string) ([]string, error) {
			if start.IsZero() {
				return nil, errors.New("fallback scan failed")
			}
			return []string{}, nil
		},
	}
	rec := httptest.NewRecorder()
	NewHandler("test", testStatic(), store, "").ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/v1/anomalies?start=2026-09-01&end=2026-09-30", nil))
	if rec.Code != http.StatusInternalServerError || strings.TrimSpace(rec.Body.String()) != "querying daily costs: fallback scan failed" {
		t.Errorf("status=%d body=%q, want 500 fallback error", rec.Code, rec.Body.String())
	}
	if store.currenciesQueryCount != 2 {
		t.Errorf("currency lookups = %d, want 2 (window then failed fallback)", store.currenciesQueryCount)
	}
}

func TestGetAnomaliesInvalidCurrencyIs400(t *testing.T) {
	store := anomalyFakeStore(t)
	rec := httptest.NewRecorder()
	NewHandler("test", testStatic(), store, "").ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/v1/anomalies?currency=eur", nil))
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

func TestGetAnomaliesProviderValidationAndStoreThreading(t *testing.T) {
	const selectedProvider = "Amazon Web Services"
	rules := writeRules(t, `{"dimensions":[{"name":"team","rules":[]}]}`)

	for _, tt := range []struct {
		name           string
		groupBy        string
		rulesPath      string
		wantGroupBy    storage.CostGroupBy
		wantService    int
		wantAllocation int
	}{
		{name: "service", wantGroupBy: storage.GroupByService, wantService: 1},
		{name: "provider grouping", groupBy: "&groupBy=provider", wantGroupBy: storage.GroupByProvider, wantService: 1},
		{name: "allocation", groupBy: "&groupBy=allocation", rulesPath: rules, wantAllocation: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{
				currenciesFn: func(_ string, start, _ time.Time, provider string) ([]string, error) {
					if provider != selectedProvider {
						return nil, errors.New("provider missing from currency lookup")
					}
					if !start.IsZero() {
						return []string{}, nil
					}
					return []string{"USD"}, nil
				},
				dailyCurrencyFn: func(_ string, _, _ time.Time, currency string, _ storage.CostGroupBy) (storage.DailyCosts, error) {
					return storage.DailyCosts{Currency: currency, Days: []storage.DayCosts{}}, nil
				},
			}
			rec := httptest.NewRecorder()
			NewHandler("test", testStatic(), store, tt.rulesPath).ServeHTTP(rec,
				httptest.NewRequest(http.MethodGet, "/api/v1/anomalies?start=2026-06-01&provider=Amazon%20Web%20Services"+tt.groupBy, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
			}
			if store.currenciesQueryCount != 2 || len(store.currenciesProviderLog) != 2 ||
				store.currenciesProviderLog[0] != selectedProvider || store.currenciesProviderLog[1] != selectedProvider {
				t.Fatalf("currency provider log = %v, want provider on window and fallback lookups", store.currenciesProviderLog)
			}
			if len(store.queryLog) != 1 || store.queryLog[0].currency != "USD" || store.queryLog[0].provider != selectedProvider {
				t.Fatalf("daily query log = %+v, want USD and provider %q", store.queryLog, selectedProvider)
			}
			if store.queryCount != tt.wantService || store.allocQueryCount != tt.wantAllocation {
				t.Fatalf("service/allocation queries = %d/%d, want %d/%d", store.queryCount, store.allocQueryCount, tt.wantService, tt.wantAllocation)
			}
			if tt.wantService == 1 && store.gotGroupBy != tt.wantGroupBy {
				t.Fatalf("groupBy = %v, want %v", store.gotGroupBy, tt.wantGroupBy)
			}
		})
	}

	for _, query := range []string{"?provider=", "?provider=" + strings.Repeat("p", focus.MaxFreeTextBytes+1)} {
		t.Run("invalid provider", func(t *testing.T) {
			store := &fakeStore{}
			rec := httptest.NewRecorder()
			NewHandler("test", testStatic(), store, "").ServeHTTP(rec,
				httptest.NewRequest(http.MethodGet, "/api/v1/anomalies"+query, nil))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body)
			}
			if body := strings.TrimSpace(rec.Body.String()); body != "provider must be a non-empty string of at most 8192 bytes" {
				t.Fatalf("body = %q", body)
			}
			if store.currenciesQueryCount != 0 || len(store.queryLog) != 0 {
				t.Fatalf("invalid provider reached store: currencies=%d daily=%d", store.currenciesQueryCount, len(store.queryLog))
			}
		})
	}
}

func TestGetAnomaliesBillingCurrenciesErrorIs500(t *testing.T) {
	store := &fakeStore{currenciesErr: errors.New("currency scan failed")}
	rec := httptest.NewRecorder()
	NewHandler("test", testStatic(), store, "").ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/v1/anomalies", nil))
	if rec.Code != http.StatusInternalServerError || strings.TrimSpace(rec.Body.String()) != "querying daily costs: currency scan failed" {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if store.currenciesQueryCount != 1 || store.queryCount != 0 || store.allocQueryCount != 0 {
		t.Fatalf("currency failure query counts = %d/%d/%d, want 1/0/0",
			store.currenciesQueryCount, store.queryCount, store.allocQueryCount)
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

	for _, tt := range []struct {
		name        string
		groupBy     string
		wantGroupBy storage.CostGroupBy
	}{
		{name: "subaccount routes and echoes verbatim", groupBy: "subaccount", wantGroupBy: storage.GroupBySubaccount},
		{name: "region routes and echoes verbatim", groupBy: "region", wantGroupBy: storage.GroupByRegion},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := anomalyFakeStore(t)
			rec := httptest.NewRecorder()
			NewHandler("test", testStatic(), store, "").ServeHTTP(rec,
				httptest.NewRequest(http.MethodGet, "/api/v1/anomalies?groupBy="+tt.groupBy, nil))
			if rec.Code != http.StatusOK || store.gotGroupBy != tt.wantGroupBy {
				t.Fatalf("status=%d groupBy=%v body=%s", rec.Code, store.gotGroupBy, rec.Body)
			}
			var got Anomalies
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Parameters.GroupBy != tt.groupBy {
				t.Errorf("parameters.groupBy = %q, want %q", got.Parameters.GroupBy, tt.groupBy)
			}
		})
	}

	t.Run("invalid groupBy is 400 without touching the store", func(t *testing.T) {
		store := anomalyFakeStore(t)
		rec := httptest.NewRecorder()
		NewHandler("test", testStatic(), store, "").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/anomalies?groupBy=bogus", nil))
		if rec.Code != http.StatusBadRequest || store.currenciesQueryCount != 0 || store.queryCount != 0 || store.allocQueryCount != 0 {
			t.Fatalf("status=%d currencyCount=%d queryCount=%d allocCount=%d", rec.Code, store.currenciesQueryCount, store.queryCount, store.allocQueryCount)
		}
		if body := strings.TrimSpace(rec.Body.String()); body != "invalid groupBy value" {
			t.Fatalf("body = %q, want invalid groupBy value", body)
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

func TestGetAnomaliesTagGroupingRoutesKeyAndEchoesGrouping(t *testing.T) {
	store := anomalyFakeStore(t)
	rec := httptest.NewRecorder()
	NewHandler("test", testStatic(), store, "").ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/v1/anomalies?groupBy=tag&tagKey=cost%20center", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	if store.tagQueryCount != 1 || store.queryCount != 0 || !reflect.DeepEqual(store.tagKeyLog, []string{"cost center"}) {
		t.Fatalf("tag/service calls = %d/%d keys=%v, want 1/0 [cost center]", store.tagQueryCount, store.queryCount, store.tagKeyLog)
	}
	var got Anomalies
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Parameters.GroupBy != "tag" {
		t.Fatalf("parameters.groupBy = %q, want tag", got.Parameters.GroupBy)
	}
	if got.Parameters.TagKey != "cost center" {
		t.Fatalf("parameters.tagKey = %q, want cost center", got.Parameters.TagKey)
	}
}
