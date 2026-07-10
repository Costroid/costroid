// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/storage"
)

func TestUnitCostStringExactRounding(t *testing.T) {
	tests := []struct {
		name     string
		cost     string
		quantity string
		want     *string
	}{
		{name: "third", cost: "10", quantity: "3", want: stringPtr("3.333333333333333333")},
		{name: "positive half away", cost: "1", quantity: "2000000000000000000", want: stringPtr("0.000000000000000001")},
		{name: "negative half away", cost: "-1", quantity: "2000000000000000000", want: stringPtr("-0.000000000000000001")},
		// The quotient itself has 19 significant digits and cannot survive a
		// float64 round-trip; this guards the computation output, not just an operand.
		{name: "float fragile quotient", cost: "12345678901234567.89", quantity: "10", want: stringPtr("1234567890123456.789")},
		{name: "zero guard", cost: "1", quantity: "0", want: nil},
		{name: "trailing zero trimming", cost: "10", quantity: "2", want: stringPtr("5")},
		{name: "negative cost", cost: "-5", quantity: "2", want: stringPtr("-2.5")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := unitCostString(decimal.RequireFromString(tt.cost), decimal.RequireFromString(tt.quantity))
			if (got == nil) != (tt.want == nil) || got != nil && *got != *tt.want {
				t.Fatalf("unitCostString(%s, %s) = %v, want %v", tt.cost, tt.quantity, valueOf(got), valueOf(tt.want))
			}
		})
	}
}

func stringPtr(v string) *string { return &v }

func valueOf(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}

func TestMergeUnitEconomicsCoveredBinsOnly(t *testing.T) {
	day := func(n int) time.Time { return time.Date(2026, 5, n, 0, 0, 0, 0, time.UTC) }
	costs := storage.DailyCosts{Currency: "USD", Days: []storage.DayCosts{
		{Date: day(1), Services: []storage.ServiceCost{{ServiceName: "cost-only", Cost: decimal.RequireFromString("10")}}},
		{Date: day(2), Services: []storage.ServiceCost{{ServiceName: "credit", Cost: decimal.RequireFromString("-4")}}},
		{Date: day(3), Services: []storage.ServiceCost{{ServiceName: "zero", Cost: decimal.Zero}}},
		{Date: day(4), Services: []storage.ServiceCost{{ServiceName: "positive", Cost: decimal.RequireFromString("10")}}},
	}}
	quantities := []storage.DayQuantity{
		{Date: day(2), Quantity: decimal.RequireFromString("2")},
		{Date: day(3), Quantity: decimal.RequireFromString("4")},
		{Date: day(4), Quantity: decimal.RequireFromString("4")},
		{Date: day(5), Quantity: decimal.RequireFromString("7")},
	}

	got := mergeUnitEconomics("requests", costs, quantities)
	if got.Currency != "USD" || got.Metric != "requests" || len(got.Days) != 5 {
		t.Fatalf("response = %+v", got)
	}
	if got.Days[0].Cost == nil || *got.Days[0].Cost != "10" || got.Days[0].Quantity != nil || got.Days[0].UnitCost != nil {
		t.Errorf("cost-only day = %+v", got.Days[0])
	}
	if got.Days[1].UnitCost == nil || *got.Days[1].UnitCost != "-2" {
		t.Errorf("negative covered day = %+v", got.Days[1])
	}
	if got.Days[2].Cost == nil || *got.Days[2].Cost != "0" || got.Days[2].UnitCost == nil || *got.Days[2].UnitCost != "0" {
		t.Errorf("zero-cost covered day = %+v", got.Days[2])
	}
	if got.Days[3].UnitCost == nil || *got.Days[3].UnitCost != "2.5" {
		t.Errorf("positive covered day = %+v", got.Days[3])
	}
	if got.Days[4].Cost != nil || got.Days[4].Quantity == nil || *got.Days[4].Quantity != "7" || got.Days[4].UnitCost != nil {
		t.Errorf("metric-only day = %+v", got.Days[4])
	}
	if got.Period.CoveredDays != 3 || got.Period.Cost != "6" || got.Period.Quantity != "10" || got.Period.UnitCost == nil || *got.Period.UnitCost != "0.6" {
		t.Errorf("covered-only period = %+v", got.Period)
	}
}

// TestGetDailyUnitEconomicsEmptyCurrencyWhenNoCostRows pins that when a metric
// has quantity rows but NO cost rows matched (empty DailyCosts.Currency), the
// response currency is exactly "" — never a fabricated default. It guards the
// costs.Currency passthrough in mergeUnitEconomics: a regression that defaulted
// the currency to (say) "USD" reddens here.
func TestGetDailyUnitEconomicsEmptyCurrencyWhenNoCostRows(t *testing.T) {
	store := &fakeStore{
		businessInfos:      []storage.BusinessMetricInfo{{Name: "requests"}},
		daily:              storage.DailyCosts{Currency: "", Days: nil},
		businessQuantities: []storage.DayQuantity{{Date: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), Quantity: decimal.RequireFromString("10")}},
	}
	handler := NewHandler("test", testStatic(), store, "")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/unit-economics/daily?metric=requests", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var got UnitEconomics
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Currency != "" {
		t.Errorf("currency = %q, want empty string (no cost rows matched)", got.Currency)
	}
	// The empty currency is carried verbatim in the JSON, never omitted or faked.
	if !strings.Contains(rec.Body.String(), `"currency":""`) {
		t.Errorf("body must carry an empty currency verbatim: %s", rec.Body)
	}
}

func TestGetBusinessMetrics(t *testing.T) {
	store := &fakeStore{businessInfos: []storage.BusinessMetricInfo{
		{Name: "active users", FirstDay: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), LastDay: time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC)},
		{Name: "requests", FirstDay: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), LastDay: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)},
	}}
	handler := NewHandler("test", testStatic(), store, "")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/business-metrics", nil))
	if rec.Code != http.StatusOK || store.gotBusinessNamesTenant != "default" || store.businessNamesCount != 1 {
		t.Fatalf("status=%d tenant=%q count=%d body=%s", rec.Code, store.gotBusinessNamesTenant, store.businessNamesCount, rec.Body)
	}
	var got BusinessMetrics
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Metrics) != 2 || got.Metrics[0].Name != "active users" || got.Metrics[0].FirstDay.Format(time.DateOnly) != "2026-05-01" || got.Metrics[1].LastDay.Format(time.DateOnly) != "2026-06-01" {
		t.Fatalf("metrics = %+v", got.Metrics)
	}

	empty := NewHandler("test", testStatic(), &fakeStore{}, "")
	rec = httptest.NewRecorder()
	empty.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/business-metrics", nil))
	if body := strings.TrimSpace(rec.Body.String()); body != `{"metrics":[]}` {
		t.Errorf("empty body = %s", body)
	}

	failing := NewHandler("test", testStatic(), &fakeStore{businessNamesErr: errors.New("names unavailable")}, "")
	rec = httptest.NewRecorder()
	failing.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/business-metrics", nil))
	if rec.Code != http.StatusInternalServerError || strings.TrimSpace(rec.Body.String()) != "querying business metric names: names unavailable" {
		t.Errorf("list error status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestGetDailyUnitEconomicsHappyAndRequestShape(t *testing.T) {
	day := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)
	store := &fakeStore{
		businessInfos: []storage.BusinessMetricInfo{{Name: "a b&c"}},
		daily: storage.DailyCosts{Currency: "USD", Days: []storage.DayCosts{{Date: day, Services: []storage.ServiceCost{
			{ServiceName: "one", Cost: decimal.RequireFromString("1.000000000000000001")},
			{ServiceName: "two", Cost: decimal.RequireFromString("2")},
		}}}},
		businessQuantities: []storage.DayQuantity{{Date: day, Quantity: decimal.RequireFromString("3")}},
	}
	handler := NewHandler("test", testStatic(), store, "")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/unit-economics/daily?metric=a%20b%26c&start=2026-05-02&end=2026-05-03", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if store.gotBusinessNamesTenant != "default" || store.gotTenant != "default" || store.gotBusinessTenant != "default" || store.gotBusinessMetric != "a b&c" {
		t.Errorf("tenant/metric shape: names=%q costs=%q quantities=%q metric=%q", store.gotBusinessNamesTenant, store.gotTenant, store.gotBusinessTenant, store.gotBusinessMetric)
	}
	if store.gotGroupBy != groupByUnset {
		t.Errorf("cost query groupBy = %v, want bare call", store.gotGroupBy)
	}
	for name, got := range map[string]time.Time{"cost start": store.gotStart, "cost end": store.gotEnd, "metric start": store.gotBusinessStart, "metric end": store.gotBusinessEnd} {
		want := "2026-05-02"
		if strings.Contains(name, "end") {
			want = "2026-05-03"
		}
		if got.Format(time.DateOnly) != want {
			t.Errorf("%s=%s want %s", name, got.Format(time.DateOnly), want)
		}
	}
	var got UnitEconomics
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Days) != 1 || got.Days[0].Cost == nil || *got.Days[0].Cost != "3.000000000000000001" || got.Days[0].Quantity == nil || *got.Days[0].Quantity != "3" || got.Days[0].UnitCost == nil || *got.Days[0].UnitCost != "1" {
		t.Fatalf("days = %+v", got.Days)
	}
	if got.Period.CoveredDays != 1 || got.Period.Cost != "3.000000000000000001" || got.Period.Quantity != "3" || got.Period.UnitCost == nil || *got.Period.UnitCost != "1" {
		t.Fatalf("period = %+v", got.Period)
	}
}

func TestGetDailyUnitEconomicsErrorsAndKnownOutsideRange(t *testing.T) {
	get := func(store *fakeStore, query string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		NewHandler("test", testStatic(), store, "").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/unit-economics/daily"+query, nil))
		return rec
	}

	// metric is a required query param. A WHOLLY ABSENT metric is rejected by the
	// generated binding wrapper (its own message) before the handler runs; a
	// present-but-empty "?metric=" binds "" and is rejected by the handler's own
	// non-empty guard. Both are 400 and neither touches the store.
	for _, tc := range []struct {
		query string
		body  string
	}{
		{"", "Query argument metric is required, but not found"},
		{"?metric=", "metric query parameter is required and must be non-empty"},
	} {
		store := &fakeStore{}
		rec := get(store, tc.query)
		if rec.Code != http.StatusBadRequest || strings.TrimSpace(rec.Body.String()) != tc.body || store.businessNamesCount != 0 {
			t.Errorf("missing metric %q: status=%d body=%q names=%d", tc.query, rec.Code, rec.Body.String(), store.businessNamesCount)
		}
	}

	unknown := &fakeStore{businessInfos: []storage.BusinessMetricInfo{{Name: "known"}}}
	rec := get(unknown, "?metric=missing")
	if rec.Code != http.StatusNotFound || strings.TrimSpace(rec.Body.String()) != `unknown business metric "missing"; list available metrics at /api/v1/business-metrics` || unknown.queryCount != 0 || unknown.businessQuantityCount != 0 {
		t.Errorf("unknown: status=%d body=%q", rec.Code, rec.Body.String())
	}

	invalid := &fakeStore{}
	rec = get(invalid, "?metric=known&start=bogus")
	const invalidBody = `Invalid format for parameter start: parsing time "bogus" as "2006-01-02": cannot parse "bogus" as "2006"`
	if rec.Code != http.StatusBadRequest || strings.TrimSpace(rec.Body.String()) != invalidBody || invalid.businessNamesCount != 0 {
		t.Errorf("invalid date: status=%d body=%q", rec.Code, rec.Body.String())
	}

	namesFail := &fakeStore{businessNamesErr: errors.New("names failed")}
	rec = get(namesFail, "?metric=known")
	if rec.Code != http.StatusInternalServerError || strings.TrimSpace(rec.Body.String()) != "querying business metric names: names failed" {
		t.Errorf("names error: status=%d body=%q", rec.Code, rec.Body.String())
	}

	costFail := &fakeStore{businessInfos: []storage.BusinessMetricInfo{{Name: "known"}}, dailyErr: errors.New("stored records mix billing currencies (EUR, USD); currency conversion is not supported yet")}
	rec = get(costFail, "?metric=known")
	if rec.Code != http.StatusInternalServerError || strings.TrimSpace(rec.Body.String()) != "querying daily costs: stored records mix billing currencies (EUR, USD); currency conversion is not supported yet" {
		t.Errorf("cost error: status=%d body=%q", rec.Code, rec.Body.String())
	}

	quantityFail := &fakeStore{businessInfos: []storage.BusinessMetricInfo{{Name: "known"}}, businessQuantitiesErr: errors.New("quantity failed")}
	rec = get(quantityFail, "?metric=known")
	if rec.Code != http.StatusInternalServerError || strings.TrimSpace(rec.Body.String()) != "querying daily business metric quantities: quantity failed" {
		t.Errorf("quantity error: status=%d body=%q", rec.Code, rec.Body.String())
	}

	// Metric existence is independent of the requested range: the known name
	// returns cost-only days and a present zero-covered period, not a 404.
	known := &fakeStore{
		businessInfos: []storage.BusinessMetricInfo{{Name: "known", FirstDay: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), LastDay: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}},
		daily:         storage.DailyCosts{Currency: "USD", Days: []storage.DayCosts{{Date: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), Services: []storage.ServiceCost{{ServiceName: "cost", Cost: decimal.RequireFromString("1")}}}}},
	}
	rec = get(known, "?metric=known&start=2026-05-01&end=2026-05-01")
	if rec.Code != http.StatusOK {
		t.Fatalf("known outside range status=%d body=%s", rec.Code, rec.Body)
	}
	var got UnitEconomics
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Days) != 1 || got.Days[0].Cost == nil || *got.Days[0].Cost != "1" || got.Days[0].Quantity != nil || got.Days[0].UnitCost != nil || got.Period.CoveredDays != 0 || got.Period.Cost != "0" || got.Period.Quantity != "0" || got.Period.UnitCost != nil {
		t.Fatalf("known outside range = %+v", got)
	}
}
