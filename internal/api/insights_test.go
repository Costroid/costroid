// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/allocation"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/storage"
)

// CostTotals satisfies CostStore for the shared fakeStore (existing test files
// stay untouched; Go allows methods on a type from any file in the package).
func (f *fakeStore) CostTotals(_ context.Context, _ string, _, _ time.Time) ([]storage.CostTotals, error) {
	return []storage.CostTotals{}, nil
}

// insightStore embeds fakeStore and overrides paths needed for multi-window,
// multi-tag, allocation, history, and cost-totals scenarios.
type insightStore struct {
	*fakeStore
	costTotals    []storage.CostTotals
	costTotalsErr error
	// serviceDaily maps "start|end" (DateOnly; empty side for zero) to costs.
	serviceDaily map[string]storage.DailyCosts
	// historyDaily is returned when start is zero (anomaly full-history fetch).
	historyDaily *storage.DailyCosts
	// tagDaily maps tag key to daily costs.
	tagDaily map[string]storage.DailyCosts
	// allocDaily when non-nil is returned from DailyCostsByAllocation.
	allocDaily *storage.DailyCosts
	// quantities maps metric name for any window.
	quantities map[string][]storage.DayQuantity
	// quantitiesByWindow maps "metric|start|end".
	quantitiesByWindow map[string][]storage.DayQuantity
}

func insightWindowKey(start, end time.Time) string {
	s, e := "", ""
	if !start.IsZero() {
		s = start.UTC().Format(time.DateOnly)
	}
	if !end.IsZero() {
		e = end.UTC().Format(time.DateOnly)
	}
	return s + "|" + e
}

func (s *insightStore) CostTotals(_ context.Context, _ string, _, _ time.Time) ([]storage.CostTotals, error) {
	if s.costTotalsErr != nil {
		return nil, s.costTotalsErr
	}
	if s.costTotals != nil {
		return append([]storage.CostTotals{}, s.costTotals...), nil
	}
	return []storage.CostTotals{}, nil
}

func (s *insightStore) DailyCostsByService(_ context.Context, tenant string, start, end time.Time, currency, provider string, groupBy ...storage.CostGroupBy) (storage.DailyCosts, error) {
	s.gotTenant, s.gotStart, s.gotEnd = tenant, start, end
	s.gotCurrency = currency
	s.gotGroupBy = groupByUnset
	if len(groupBy) > 0 {
		s.gotGroupBy = groupBy[0]
	}
	s.groupByLog = append(s.groupByLog, s.gotGroupBy)
	s.queryCount++
	s.queryLog = append(s.queryLog, struct {
		start, end time.Time
		currency   string
		provider   string
	}{start: start, end: end, currency: currency, provider: provider})
	if s.dailyErr != nil {
		return storage.DailyCosts{}, s.dailyErr
	}
	if start.IsZero() && s.historyDaily != nil {
		out := *s.historyDaily
		if currency != "" {
			out.Currency = currency
		}
		return out, nil
	}
	if s.serviceDaily != nil {
		if d, ok := s.serviceDaily[insightWindowKey(start, end)]; ok {
			out := d
			if currency != "" {
				out.Currency = currency
			}
			return out, nil
		}
	}
	out := s.daily
	if currency != "" {
		out.Currency = currency
	}
	return out, nil
}

func (s *insightStore) DailyCostsByTag(_ context.Context, tenant string, start, end time.Time, tagKey, currency, provider string) (storage.DailyCosts, error) {
	s.gotTenant, s.gotStart, s.gotEnd = tenant, start, end
	s.gotCurrency = currency
	s.tagKeyLog = append(s.tagKeyLog, tagKey)
	s.tagQueryCount++
	if s.dailyErr != nil {
		return storage.DailyCosts{}, s.dailyErr
	}
	if s.tagDaily != nil {
		if d, ok := s.tagDaily[tagKey]; ok {
			out := d
			if currency != "" {
				out.Currency = currency
			}
			return out, nil
		}
	}
	return storage.DailyCosts{Currency: currency, Days: nil}, nil
}

func (s *insightStore) DailyCostsByAllocation(_ context.Context, tenant string, start, end time.Time, dim allocation.Dimension, currency, provider string) (storage.DailyCosts, error) {
	s.gotTenant, s.gotStart, s.gotEnd = tenant, start, end
	s.gotCurrency = currency
	s.gotDimension = dim
	s.dimensionLog = append(s.dimensionLog, dim)
	s.allocQueryCount++
	if s.dailyErr != nil {
		return storage.DailyCosts{}, s.dailyErr
	}
	if s.allocDaily != nil {
		out := *s.allocDaily
		if currency != "" {
			out.Currency = currency
		}
		return out, nil
	}
	return storage.DailyCosts{Currency: currency}, nil
}

func (s *insightStore) DailyBusinessMetricQuantities(_ context.Context, tenant, metric string, start, end time.Time) ([]storage.DayQuantity, error) {
	s.gotBusinessTenant, s.gotBusinessMetric = tenant, metric
	s.gotBusinessStart, s.gotBusinessEnd = start, end
	s.businessQuantityCount++
	if s.businessQuantitiesErr != nil {
		return nil, s.businessQuantitiesErr
	}
	if s.quantitiesByWindow != nil {
		if q, ok := s.quantitiesByWindow[metric+"|"+insightWindowKey(start, end)]; ok {
			return q, nil
		}
	}
	if s.quantities != nil {
		if q, ok := s.quantities[metric]; ok {
			return q, nil
		}
	}
	return append([]storage.DayQuantity{}, s.businessQuantities...), nil
}

func insightDay(t *testing.T, date string, costs map[string]string) storage.DayCosts {
	t.Helper()
	d, err := time.Parse(time.DateOnly, date)
	if err != nil {
		t.Fatalf("date: %v", err)
	}
	var svcs []storage.ServiceCost
	for k, v := range costs {
		svcs = append(svcs, storage.ServiceCost{ServiceName: k, Cost: dec(t, v)})
	}
	// stable order for tests that care about series construction
	return storage.DayCosts{Date: d, Services: svcs}
}

func getInsights(t *testing.T, store CostStore, rulesPath, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	NewHandler("test", testStatic(), store, rulesPath).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/v1/insights"+query, nil))
	return rec
}

func decodeInsights(t *testing.T, rec *httptest.ResponseRecorder) Insights {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got Insights
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	return got
}

func evidenceMap(ev []InsightEvidence) map[string]string {
	m := make(map[string]string, len(ev))
	for _, e := range ev {
		m[e.Name] = e.Value
	}
	return m
}

func findType(ins []Insight, typ string) (Insight, bool) {
	for _, i := range ins {
		if i.Type == typ {
			return i, true
		}
	}
	return Insight{}, false
}

func findTypeKey(ins []Insight, typ, key string) (Insight, bool) {
	for _, i := range ins {
		if i.Type == typ && i.Key != nil && *i.Key == key {
			return i, true
		}
	}
	return Insight{}, false
}

// --- Criterion 7: empty store ---

func TestGetInsightsEmptyStore(t *testing.T) {
	rec := getInsights(t, &fakeStore{}, "", "")
	got := decodeInsights(t, rec)
	if got.Currency != "" {
		t.Errorf("currency = %q, want \"\"", got.Currency)
	}
	if got.Currencies == nil || len(got.Currencies) != 0 {
		t.Errorf("currencies = %#v, want []", got.Currencies)
	}
	if got.Insights == nil || len(got.Insights) != 0 {
		t.Errorf("insights = %#v, want []", got.Insights)
	}
	// parameters still present
	if got.Parameters.DivisionScale != 18 || got.Parameters.K != "3" {
		t.Errorf("parameters = %+v", got.Parameters)
	}
	// raw JSON must not contain null arrays
	body := rec.Body.String()
	if strings.Contains(body, `"insights":null`) || strings.Contains(body, `"currencies":null`) {
		t.Errorf("null arrays in body: %s", body)
	}
}

// --- Criterion 2: currency semantics ---

func TestGetInsightsCurrencyDefaultAndExplicit(t *testing.T) {
	store := &insightStore{
		fakeStore: &fakeStore{currencies: []string{"EUR", "USD"}},
		serviceDaily: map[string]storage.DailyCosts{
			"2026-05-10|2026-05-20": {Currency: "EUR", Days: []storage.DayCosts{
				insightDay(t, "2026-05-15", map[string]string{"EC2": "10"}),
			}},
		},
		costTotals: []storage.CostTotals{
			// EUR billed 10 effective 10 (no commitment insight)
			{Currency: "EUR", Billed: dec(t, "10"), Effective: dec(t, "10")},
			// USD would change totals if leaked
			{Currency: "USD", Billed: dec(t, "999"), Effective: dec(t, "1")},
		},
	}
	// Default: alphabetically first = EUR
	got := decodeInsights(t, getInsights(t, store, "", "?start=2026-05-10&end=2026-05-20"))
	if got.Currency != "EUR" {
		t.Fatalf("default currency = %q, want EUR", got.Currency)
	}
	if len(got.Currencies) != 2 || got.Currencies[0] != "EUR" || got.Currencies[1] != "USD" {
		t.Fatalf("currencies = %v, want [EUR USD]", got.Currencies)
	}
	// Explicit USD
	store2 := &insightStore{
		fakeStore: &fakeStore{currencies: []string{"EUR", "USD"}},
		serviceDaily: map[string]storage.DailyCosts{
			"2026-05-10|2026-05-20": {Currency: "USD", Days: []storage.DayCosts{
				insightDay(t, "2026-05-15", map[string]string{"EC2": "5"}),
			}},
		},
		costTotals: []storage.CostTotals{
			{Currency: "EUR", Billed: dec(t, "10"), Effective: dec(t, "8")},
			// USD: billed 100, effective 40 => savings 60 (hand-computed)
			{Currency: "USD", Billed: dec(t, "100"), Effective: dec(t, "40")},
		},
	}
	gotUSD := decodeInsights(t, getInsights(t, store2, "", "?start=2026-05-10&end=2026-05-20&currency=USD"))
	if gotUSD.Currency != "USD" {
		t.Fatalf("explicit currency = %q, want USD", gotUSD.Currency)
	}
	// Single-currency teeth: commitment magnitude must be USD-only 60, not mixed.
	c, ok := findType(gotUSD.Insights, "commitment-realization")
	if !ok {
		t.Fatalf("missing commitment-realization: %+v", gotUSD.Insights)
	}
	if c.Magnitude != "60" {
		t.Fatalf("commitment magnitude = %s, want 60 (USD-only; EUR leak would change it)", c.Magnitude)
	}
	ev := evidenceMap(c.Evidence)
	if ev["billedTotal"] != "100" || ev["effectiveTotal"] != "40" {
		t.Fatalf("commitment evidence = %v, want billed 100 effective 40", ev)
	}
}

func TestGetInsightsCurrencyMalformed400(t *testing.T) {
	rec := getInsights(t, &fakeStore{currencies: []string{"USD"}}, "", "?currency=usd")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
}

func TestGetInsightsCurrencyFullHistoryFallback(t *testing.T) {
	// Window has no currencies; full history has USD.
	store := &insightStore{
		fakeStore: &fakeStore{
			currenciesFn: func(_ string, start, end time.Time, _ string) ([]string, error) {
				if start.IsZero() {
					return []string{"USD"}, nil
				}
				return []string{}, nil
			},
		},
		historyDaily: &storage.DailyCosts{Currency: "USD", Days: nil},
	}
	got := decodeInsights(t, getInsights(t, store, "", "?start=2026-07-01&end=2026-07-02"))
	if got.Currency != "USD" {
		t.Fatalf("fallback currency = %q, want USD", got.Currency)
	}
	// currencies lists in-window only (empty)
	if len(got.Currencies) != 0 {
		t.Fatalf("in-window currencies = %v, want []", got.Currencies)
	}
}

// --- Criterion 9: parameters parity with anomalies ---

func TestGetInsightsParametersMatchAnomalies(t *testing.T) {
	store := anomalyFakeStore(t)
	// anomalies
	aRec := httptest.NewRecorder()
	NewHandler("test", testStatic(), store, "").ServeHTTP(aRec, httptest.NewRequest(http.MethodGet, "/api/v1/anomalies", nil))
	var a Anomalies
	if err := json.Unmarshal(aRec.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	// insights (empty enough to still emit parameters)
	iRec := getInsights(t, &fakeStore{currencies: []string{"USD"}, daily: store.daily}, "", "")
	// Use a store that has currency so we don't early-return before params... params always present.
	got := decodeInsights(t, iRec)
	p, ap := got.Parameters, a.Parameters
	if p.K != ap.K || p.ConsistencyConstant != ap.ConsistencyConstant ||
		p.WindowDays != ap.WindowDays || p.MinObservations != ap.MinObservations ||
		p.RelativeFloor != ap.RelativeFloor {
		t.Fatalf("insights params %+v != anomalies %+v", p, ap)
	}
	if p.DivisionScale != 18 {
		t.Fatalf("divisionScale = %d, want 18", p.DivisionScale)
	}
}

// --- Six insight types + suppressions ---

func TestGetInsightsTopMoverExact(t *testing.T) {
	// Current window May 10-20: EC2 40, S3 5
	// Previous May 0-9 effectively: prevEnd=May 9, prevStart=May 9-(10 days)=Apr 29
	// We key previous as "2026-04-29|2026-05-09"
	// Previous: EC2 10, S3 20 => deltas EC2 +30, S3 -15
	cur := storage.DailyCosts{Currency: "USD", Days: []storage.DayCosts{
		insightDay(t, "2026-05-15", map[string]string{"EC2": "40", "S3": "5"}),
	}}
	prev := storage.DailyCosts{Currency: "USD", Days: []storage.DayCosts{
		insightDay(t, "2026-05-01", map[string]string{"EC2": "10", "S3": "20"}),
	}}
	store := &insightStore{
		fakeStore: &fakeStore{currencies: []string{"USD"}},
		serviceDaily: map[string]storage.DailyCosts{
			"2026-05-10|2026-05-20": cur,
			"2026-04-29|2026-05-09": prev,
		},
		historyDaily: &storage.DailyCosts{Currency: "USD"},
	}
	got := decodeInsights(t, getInsights(t, store, "", "?start=2026-05-10&end=2026-05-20&currency=USD"))
	inc, ok := findTypeKey(got.Insights, "top-mover", "EC2")
	if !ok {
		t.Fatalf("missing EC2 increase: %+v", got.Insights)
	}
	if inc.Magnitude != "30" {
		t.Fatalf("increase mag = %s, want 30", inc.Magnitude)
	}
	ev := evidenceMap(inc.Evidence)
	if ev["total"] != "40" || ev["previousTotal"] != "10" || ev["delta"] != "30" {
		t.Fatalf("increase evidence = %v", ev)
	}
	dec, ok := findTypeKey(got.Insights, "top-mover", "S3")
	if !ok {
		t.Fatalf("missing S3 decrease: %+v", got.Insights)
	}
	if dec.Magnitude != "15" {
		t.Fatalf("decrease mag = %s, want 15", dec.Magnitude)
	}
	if evidenceMap(dec.Evidence)["delta"] != "-15" {
		t.Fatalf("decrease delta = %v", evidenceMap(dec.Evidence))
	}
}

func TestGetInsightsTopMoverSuppressedPreviousEmpty(t *testing.T) {
	// Anti-vacuity: current would produce movers if previous had days.
	cur := storage.DailyCosts{Currency: "USD", Days: []storage.DayCosts{
		insightDay(t, "2026-05-15", map[string]string{"EC2": "100"}),
	}}
	store := &insightStore{
		fakeStore: &fakeStore{currencies: []string{"USD"}},
		serviceDaily: map[string]storage.DailyCosts{
			"2026-05-10|2026-05-20": cur,
			"2026-04-29|2026-05-09": {Currency: "USD", Days: nil}, // empty previous
		},
		historyDaily: &storage.DailyCosts{Currency: "USD"},
	}
	got := decodeInsights(t, getInsights(t, store, "", "?start=2026-05-10&end=2026-05-20"))
	if _, ok := findType(got.Insights, "top-mover"); ok {
		t.Fatalf("top-mover present with empty previous: %+v", got.Insights)
	}
}

func TestGetInsightsTopMoverAbsentWithoutBothBounds(t *testing.T) {
	store := &insightStore{
		fakeStore: &fakeStore{
			currencies: []string{"USD"},
			daily: storage.DailyCosts{Currency: "USD", Days: []storage.DayCosts{
				insightDay(t, "2026-05-15", map[string]string{"EC2": "100"}),
			}},
		},
		historyDaily: &storage.DailyCosts{Currency: "USD"},
	}
	// only start
	got := decodeInsights(t, getInsights(t, store, "", "?start=2026-05-10"))
	if _, ok := findType(got.Insights, "top-mover"); ok {
		t.Fatal("top-mover with only start")
	}
	if _, ok := findType(got.Insights, "unit-cost-drift"); ok {
		t.Fatal("unit-cost-drift with only start")
	}
}

func TestGetInsightsUntaggedSpend(t *testing.T) {
	// env: untagged 25, tagged 75 => window 100; team: untagged 10
	// largest untagged = env 25; share = 0.25
	store := &insightStore{
		fakeStore: &fakeStore{
			currencies: []string{"USD"},
			tagKeys:    []string{"env", "team"},
		},
		serviceDaily: map[string]storage.DailyCosts{
			"|": {Currency: "USD"},
		},
		historyDaily: &storage.DailyCosts{Currency: "USD"},
		tagDaily: map[string]storage.DailyCosts{
			"env": {Currency: "USD", Days: []storage.DayCosts{
				insightDay(t, "2026-05-01", map[string]string{"(untagged)": "25", "prod": "75"}),
			}},
			"team": {Currency: "USD", Days: []storage.DayCosts{
				insightDay(t, "2026-05-01", map[string]string{"(untagged)": "10", "platform": "90"}),
			}},
		},
	}
	got := decodeInsights(t, getInsights(t, store, "", "?currency=USD"))
	u, ok := findType(got.Insights, "untagged-spend")
	if !ok {
		t.Fatalf("missing untagged-spend: %+v", got.Insights)
	}
	if u.Key == nil || *u.Key != "env" || u.Magnitude != "25" {
		t.Fatalf("untagged = %+v, want env/25", u)
	}
	ev := evidenceMap(u.Evidence)
	wantShare := dec(t, "25").DivRound(dec(t, "100"), storage.MaxDecimalScale).String()
	if ev["untaggedTotal"] != "25" || ev["windowTotal"] != "100" || ev["share"] != wantShare {
		t.Fatalf("evidence = %v, want share %s", ev, wantShare)
	}
	if u.Link.GroupBy == nil || *u.Link.GroupBy != "tag" || u.Link.TagKey == nil || *u.Link.TagKey != "env" {
		t.Fatalf("link = %+v", u.Link)
	}
	if !strings.Contains(u.Body, "25%") {
		t.Fatalf("body missing 25%%: %s", u.Body)
	}
}

func TestGetInsightsUntaggedSuppressedWhenZero(t *testing.T) {
	store := &insightStore{
		fakeStore: &fakeStore{
			currencies: []string{"USD"},
			tagKeys:    []string{"env"},
		},
		historyDaily: &storage.DailyCosts{Currency: "USD"},
		tagDaily: map[string]storage.DailyCosts{
			"env": {Currency: "USD", Days: []storage.DayCosts{
				insightDay(t, "2026-05-01", map[string]string{"prod": "100"}), // no untagged
			}},
		},
	}
	got := decodeInsights(t, getInsights(t, store, "", "?currency=USD"))
	if _, ok := findType(got.Insights, "untagged-spend"); ok {
		t.Fatal("untagged present when zero")
	}
}

func TestGetInsightsUnallocatedSpend(t *testing.T) {
	rules := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(rules, []byte(`{"dimensions":[{"name":"team","rules":[{"label":"Platform","match":[{"dimension":"service_name","operator":"equals","value":"EC2"}]}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	alloc := storage.DailyCosts{Currency: "USD", Days: []storage.DayCosts{
		insightDay(t, "2026-05-01", map[string]string{"Platform": "70", "Unallocated": "30"}),
	}}
	store := &insightStore{
		fakeStore:    &fakeStore{currencies: []string{"USD"}},
		historyDaily: &storage.DailyCosts{Currency: "USD"},
		allocDaily:   &alloc,
	}
	got := decodeInsights(t, getInsights(t, store, rules, "?currency=USD"))
	u, ok := findType(got.Insights, "unallocated-spend")
	if !ok {
		t.Fatalf("missing unallocated: %+v", got.Insights)
	}
	if u.Magnitude != "30" || u.Key == nil || *u.Key != "Unallocated" {
		t.Fatalf("unallocated = %+v", u)
	}
	if u.Dimension != nil {
		t.Fatalf("dimension should be omitted, got %v", u.Dimension)
	}
	ev := evidenceMap(u.Evidence)
	wantShare := dec(t, "30").DivRound(dec(t, "100"), storage.MaxDecimalScale).String()
	if ev["unallocatedTotal"] != "30" || ev["windowTotal"] != "100" || ev["share"] != wantShare {
		t.Fatalf("evidence = %v want share %s", ev, wantShare)
	}
}

func TestGetInsightsUnallocatedSuppressedNoRulesPath(t *testing.T) {
	alloc := storage.DailyCosts{Currency: "USD", Days: []storage.DayCosts{
		insightDay(t, "2026-05-01", map[string]string{"Unallocated": "30"}),
	}}
	store := &insightStore{
		fakeStore:    &fakeStore{currencies: []string{"USD"}},
		historyDaily: &storage.DailyCosts{Currency: "USD"},
		allocDaily:   &alloc, // would produce if rules loaded
	}
	got := decodeInsights(t, getInsights(t, store, "", "?currency=USD"))
	if _, ok := findType(got.Insights, "unallocated-spend"); ok {
		t.Fatal("unallocated present with empty rules path")
	}
	if store.allocQueryCount != 0 {
		t.Fatalf("alloc queries = %d, want 0", store.allocQueryCount)
	}
}

func TestGetInsightsUnallocatedSuppressedMissingRulesFile(t *testing.T) {
	alloc := storage.DailyCosts{Currency: "USD", Days: []storage.DayCosts{
		insightDay(t, "2026-05-01", map[string]string{"Unallocated": "30"}),
	}}
	store := &insightStore{
		fakeStore:    &fakeStore{currencies: []string{"USD"}},
		historyDaily: &storage.DailyCosts{Currency: "USD"},
		allocDaily:   &alloc,
	}
	missing := filepath.Join(t.TempDir(), "nope.json")
	rec := getInsights(t, store, missing, "?currency=USD")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200 on missing rules", rec.Code)
	}
	got := decodeInsights(t, rec)
	if _, ok := findType(got.Insights, "unallocated-spend"); ok {
		t.Fatal("unallocated present with missing rules file")
	}
}

func TestGetInsightsUnallocatedSuppressedMalformedRules(t *testing.T) {
	rules := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(rules, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	alloc := storage.DailyCosts{Currency: "USD", Days: []storage.DayCosts{
		insightDay(t, "2026-05-01", map[string]string{"Unallocated": "30"}),
	}}
	store := &insightStore{
		fakeStore:    &fakeStore{currencies: []string{"USD"}},
		historyDaily: &storage.DailyCosts{Currency: "USD"},
		allocDaily:   &alloc,
	}
	rec := getInsights(t, store, rules, "?currency=USD")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200 on malformed rules", rec.Code)
	}
	got := decodeInsights(t, rec)
	if _, ok := findType(got.Insights, "unallocated-spend"); ok {
		t.Fatal("unallocated present with malformed rules")
	}
}

func TestGetInsightsAnomalyDigest(t *testing.T) {
	// Same spike series as anomalyFakeStore: flags on day 11/12.
	base := anomalyFakeStore(t)
	store := &insightStore{
		fakeStore:    &fakeStore{currencies: []string{"USD"}},
		historyDaily: &base.daily,
		serviceDaily: map[string]storage.DailyCosts{
			"2026-06-11|2026-06-12": base.daily, // not used for scoring
		},
	}
	got := decodeInsights(t, getInsights(t, store, "", "?start=2026-06-11&end=2026-06-12&currency=USD"))
	a, ok := findType(got.Insights, "anomaly-digest")
	if !ok {
		t.Fatalf("missing anomaly-digest: %+v", got.Insights)
	}
	// Largest |obs-med| among window flags: total day 11 or 12, deviation 135.
	if a.Magnitude != "135" {
		t.Fatalf("magnitude = %s, want 135", a.Magnitude)
	}
	ev := evidenceMap(a.Evidence)
	if ev["flagCount"] != "6" { // 2 days * (total + EC2 + S3)
		t.Fatalf("flagCount = %s, want 6", ev["flagCount"])
	}
	if ev["observed"] != "150" || ev["median"] != "15" || ev["deviation"] != "135" {
		t.Fatalf("evidence = %v", ev)
	}
	// total-scope: no key field in evidence, no dimension/key on insight
	if _, has := ev["key"]; has {
		t.Fatalf("total-scope evidence must omit key: %v", ev)
	}
	if a.Key != nil || a.Dimension != nil {
		t.Fatalf("total-scope must omit key/dimension: %+v", a)
	}
}

func TestGetInsightsAnomalySuppressedWhenNone(t *testing.T) {
	// Flat series: no flags.
	flat := storage.DailyCosts{Currency: "USD"}
	for n := 1; n <= 12; n++ {
		flat.Days = append(flat.Days, storage.DayCosts{
			Date:     time.Date(2026, 6, n, 0, 0, 0, 0, time.UTC),
			Services: []storage.ServiceCost{{ServiceName: "EC2", Cost: dec(t, "10")}},
		})
	}
	store := &insightStore{
		fakeStore:    &fakeStore{currencies: []string{"USD"}},
		historyDaily: &flat,
	}
	got := decodeInsights(t, getInsights(t, store, "", "?currency=USD"))
	if _, ok := findType(got.Insights, "anomaly-digest"); ok {
		t.Fatal("anomaly-digest present on flat series")
	}
}

func TestGetInsightsUnitCostDriftParity(t *testing.T) {
	// Current May 10-20: cost 10 on day 15, qty 3 => unitCost = 10/3 at scale 18
	// Previous Apr 29 - May 9: cost 5 on day 1, qty 2 => unitCost = 5/2 = 2.5
	// drift = 10/3 - 2.5; costOfDrift = |10 - 2.5*3| = |10-7.5| = 2.5
	curDate := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
	prevDate := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	curCosts := storage.DailyCosts{Currency: "USD", Days: []storage.DayCosts{
		{Date: curDate, Services: []storage.ServiceCost{{ServiceName: "EC2", Cost: dec(t, "10")}}},
	}}
	prevCosts := storage.DailyCosts{Currency: "USD", Days: []storage.DayCosts{
		{Date: prevDate, Services: []storage.ServiceCost{{ServiceName: "EC2", Cost: dec(t, "5")}}},
	}}
	store := &insightStore{
		fakeStore: &fakeStore{
			currencies: []string{"USD"},
			businessInfos: []storage.BusinessMetricInfo{
				{Name: "requests", FirstDay: prevDate, LastDay: curDate},
			},
		},
		serviceDaily: map[string]storage.DailyCosts{
			"2026-05-10|2026-05-20": curCosts,
			"2026-04-29|2026-05-09": prevCosts,
		},
		historyDaily: &storage.DailyCosts{Currency: "USD"},
		quantitiesByWindow: map[string][]storage.DayQuantity{
			"requests|2026-05-10|2026-05-20": {{Date: curDate, Quantity: dec(t, "3")}},
			"requests|2026-04-29|2026-05-09": {{Date: prevDate, Quantity: dec(t, "2")}},
		},
	}
	// Insights
	got := decodeInsights(t, getInsights(t, store, "", "?start=2026-05-10&end=2026-05-20&currency=USD"))
	u, ok := findType(got.Insights, "unit-cost-drift")
	if !ok {
		t.Fatalf("missing unit-cost-drift: %+v", got.Insights)
	}
	ev := evidenceMap(u.Evidence)
	wantUnit := dec(t, "10").DivRound(dec(t, "3"), storage.MaxDecimalScale).String()
	if ev["currentUnitCost"] != wantUnit {
		t.Fatalf("currentUnitCost = %s, want %s", ev["currentUnitCost"], wantUnit)
	}
	// Anti-vacuity: non-trivial 18-decimal string
	if !strings.Contains(wantUnit, ".") || len(strings.TrimRight(strings.Split(wantUnit, ".")[1], "0")) < 2 {
		t.Fatalf("unit cost not non-trivial enough: %s", wantUnit)
	}
	if u.Magnitude != "2.5" || ev["costOfDrift"] != "2.5" {
		t.Fatalf("magnitude/costOfDrift = %s / %s, want 2.5", u.Magnitude, ev["costOfDrift"])
	}

	// Parity with unit-economics endpoint period unitCost
	ueRec := httptest.NewRecorder()
	// Unit economics uses the same store interface; point quantities at current window via businessQuantities
	// when quantitiesByWindow is used, DailyBusinessMetricQuantities on insightStore handles it.
	// But GetDailyUnitEconomics goes through DailyBusinessMetricQuantities - good.
	// Costs: DailyCostsByService for start/end - our serviceDaily has the key.
	NewHandler("test", testStatic(), store, "").ServeHTTP(ueRec,
		httptest.NewRequest(http.MethodGet, "/api/v1/unit-economics/daily?metric=requests&start=2026-05-10&end=2026-05-20&currency=USD", nil))
	if ueRec.Code != http.StatusOK {
		t.Fatalf("unit-economics status=%d body=%s", ueRec.Code, ueRec.Body.String())
	}
	var ue UnitEconomics
	if err := json.Unmarshal(ueRec.Body.Bytes(), &ue); err != nil {
		t.Fatal(err)
	}
	if ue.Period.UnitCost == nil {
		t.Fatal("unit-economics period unitCost is nil")
	}
	if ev["currentUnitCost"] != *ue.Period.UnitCost {
		t.Fatalf("parity fail: insight currentUnitCost %q != unit-economics period unitCost %q",
			ev["currentUnitCost"], *ue.Period.UnitCost)
	}
}

func TestGetInsightsCommitmentRealization(t *testing.T) {
	store := &insightStore{
		fakeStore:    &fakeStore{currencies: []string{"USD"}},
		historyDaily: &storage.DailyCosts{Currency: "USD"},
		costTotals: []storage.CostTotals{
			{Currency: "USD", Billed: dec(t, "100.5"), Effective: dec(t, "80.25")},
		},
	}
	got := decodeInsights(t, getInsights(t, store, "", "?currency=USD"))
	c, ok := findType(got.Insights, "commitment-realization")
	if !ok {
		t.Fatalf("missing commitment: %+v", got.Insights)
	}
	// savings = 100.5 - 80.25 = 20.25
	if c.Magnitude != "20.25" {
		t.Fatalf("magnitude = %s, want 20.25", c.Magnitude)
	}
	ev := evidenceMap(c.Evidence)
	if ev["billedTotal"] != "100.5" || ev["effectiveTotal"] != "80.25" || ev["savings"] != "20.25" {
		t.Fatalf("evidence = %v", ev)
	}
	wantRatio := dec(t, "20.25").DivRound(dec(t, "100.5"), storage.MaxDecimalScale).String()
	if ev["ratio"] != wantRatio {
		t.Fatalf("ratio = %s, want %s", ev["ratio"], wantRatio)
	}
	if !strings.Contains(c.Body, "commitment discounts and amortization reduced effective cost below billed cost") {
		t.Fatalf("body: %s", c.Body)
	}
}

func TestGetInsightsCommitmentSuppressedWhenEqual(t *testing.T) {
	store := &insightStore{
		fakeStore:    &fakeStore{currencies: []string{"USD"}},
		historyDaily: &storage.DailyCosts{Currency: "USD"},
		costTotals: []storage.CostTotals{
			{Currency: "USD", Billed: dec(t, "50"), Effective: dec(t, "50")},
		},
	}
	got := decodeInsights(t, getInsights(t, store, "", "?currency=USD"))
	if _, ok := findType(got.Insights, "commitment-realization"); ok {
		t.Fatal("commitment present when billed == effective")
	}
}

// --- Criterion 6 + 8: ranking and structured links ---

func TestGetInsightsRankingAndLinks(t *testing.T) {
	// Seed >= 3 types with known magnitudes:
	// commitment 50, untagged 40, top-mover increase 30
	cur := storage.DailyCosts{Currency: "USD", Days: []storage.DayCosts{
		insightDay(t, "2026-05-15", map[string]string{"EC2": "40", "S3": "10"}),
	}}
	prev := storage.DailyCosts{Currency: "USD", Days: []storage.DayCosts{
		insightDay(t, "2026-05-01", map[string]string{"EC2": "10", "S3": "10"}),
	}}
	store := &insightStore{
		fakeStore: &fakeStore{
			currencies: []string{"USD"},
			tagKeys:    []string{"env"},
		},
		serviceDaily: map[string]storage.DailyCosts{
			"2026-05-10|2026-05-20": cur,
			"2026-04-29|2026-05-09": prev,
		},
		historyDaily: &storage.DailyCosts{Currency: "USD"},
		tagDaily: map[string]storage.DailyCosts{
			"env": {Currency: "USD", Days: []storage.DayCosts{
				insightDay(t, "2026-05-15", map[string]string{"(untagged)": "40", "prod": "10"}),
			}},
		},
		costTotals: []storage.CostTotals{
			{Currency: "USD", Billed: dec(t, "100"), Effective: dec(t, "50")},
		},
	}
	got := decodeInsights(t, getInsights(t, store, "", "?start=2026-05-10&end=2026-05-20&currency=USD"))
	if len(got.Insights) < 3 {
		t.Fatalf("insights = %d, want >= 3: %+v", len(got.Insights), insightSummary(got.Insights))
	}
	// Order: commitment 50, untagged 40, top-mover EC2 30, ...
	if got.Insights[0].Type != "commitment-realization" || got.Insights[0].Magnitude != "50" {
		t.Fatalf("rank0 = %s/%s, want commitment/50", got.Insights[0].Type, got.Insights[0].Magnitude)
	}
	if got.Insights[1].Type != "untagged-spend" || got.Insights[1].Magnitude != "40" {
		t.Fatalf("rank1 = %s/%s, want untagged/40", got.Insights[1].Type, got.Insights[1].Magnitude)
	}
	if got.Insights[2].Type != "top-mover" || got.Insights[2].Magnitude != "30" {
		t.Fatalf("rank2 = %s/%s, want top-mover/30", got.Insights[2].Type, got.Insights[2].Magnitude)
	}

	// Link structure (criterion 8)
	views := map[string]bool{"overview": true, "costs": true, "tokens": true, "usage": true, "unit-economics": true, "sources": true}
	groupBys := map[string]bool{"service": true, "provider": true, "allocation": true, "subaccount": true, "region": true, "tag": true}
	for _, ins := range got.Insights {
		l := ins.Link
		if l.View != nil && !views[*l.View] {
			t.Errorf("view %q not in VIEWS", *l.View)
		}
		if l.GroupBy != nil && !groupBys[*l.GroupBy] {
			t.Errorf("groupBy %q invalid", *l.GroupBy)
		}
		if l.GroupBy != nil && *l.GroupBy == "tag" {
			if l.TagKey == nil || *l.TagKey == "" {
				t.Errorf("tag groupBy without tagKey on %s", ins.Type)
			}
		} else if l.TagKey != nil {
			t.Errorf("tagKey present without groupBy=tag on %s", ins.Type)
		}
		// no # and no assembled URL
		for _, field := range []*string{l.View, l.Start, l.End, l.GroupBy, l.TagKey, l.Currency, l.Provider, l.Metric} {
			if field != nil && (strings.Contains(*field, "#") || strings.Contains(*field, "://") || strings.HasPrefix(*field, "/api")) {
				t.Errorf("link field looks like URL/hash: %q on %s", *field, ins.Type)
			}
		}
	}
}

func insightSummary(ins []Insight) []string {
	out := make([]string, len(ins))
	for i, x := range ins {
		k := ""
		if x.Key != nil {
			k = *x.Key
		}
		out[i] = x.Type + ":" + k + ":" + x.Magnitude
	}
	return out
}

func TestGetInsightsEvidenceNeverNull(t *testing.T) {
	store := &insightStore{
		fakeStore:    &fakeStore{currencies: []string{"USD"}},
		historyDaily: &storage.DailyCosts{Currency: "USD"},
		costTotals: []storage.CostTotals{
			{Currency: "USD", Billed: dec(t, "10"), Effective: dec(t, "5")},
		},
	}
	rec := getInsights(t, store, "", "?currency=USD")
	body := rec.Body.String()
	if strings.Contains(body, `"evidence":null`) {
		t.Fatalf("null evidence: %s", body)
	}
	got := decodeInsights(t, rec)
	for _, ins := range got.Insights {
		if ins.Evidence == nil {
			t.Fatalf("nil evidence on %s", ins.Type)
		}
	}
}

// Ensure decimal import used when needed for hand computations in comments.
var _ = decimal.Zero
var _ = focus.DefaultTenant
