// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package api

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/allocation"
	"github.com/Costroid/costroid/internal/anomalyscan"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/insights"
	"github.com/Costroid/costroid/internal/storage"
)

// GetInsights implements GET /api/v1/insights. All store access lives here; the
// pure insights package receives already-queried inputs and returns ranked
// observations with exact decimal arithmetic.
func (s *Server) GetInsights(w http.ResponseWriter, r *http.Request, params GetInsightsParams) {
	var start, end time.Time
	if params.Start != nil {
		start = params.Start.Time
	}
	if params.End != nil {
		end = params.End.Time
	}
	if params.Currency != nil && !billingCurrencyPattern.MatchString(*params.Currency) {
		http.Error(w, "currency must be a three-letter uppercase code (for example, USD)", http.StatusBadRequest)
		return
	}

	currency, currencies, ok := s.resolveInsightsCurrency(w, r, start, end, params.Currency)
	if !ok {
		return
	}

	resp := Insights{
		Currency:   currency,
		Currencies: currencies,
		Parameters: toInsightParameters(insights.FixedParameters()),
		Insights:   []Insight{},
	}
	if params.Start != nil {
		d := openapi_types.Date{Time: start}
		resp.Start = &d
	}
	if params.End != nil {
		d := openapi_types.Date{Time: end}
		resp.End = &d
	}
	if currency == "" {
		writeJSON(w, resp)
		return
	}

	in := insights.Input{
		Currency:         currency,
		WindowStart:      start,
		WindowEnd:        end,
		CurrentServices:  map[string]decimal.Decimal{},
		PreviousServices: map[string]decimal.Decimal{},
	}

	// Service-grouped costs for the request window (top-mover, unit economics).
	currentDaily, err := s.store.DailyCostsByService(r.Context(), focus.DefaultTenant, start, end, currency, "", storage.GroupByService)
	if err != nil {
		http.Error(w, "querying daily costs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	in.CurrentServices, _, _ = periodKeyTotals(currentDaily)

	// Preceding window for comparison types when both bounds are set.
	if !start.IsZero() && !end.IsZero() {
		prevEnd := start.AddDate(0, 0, -1)
		prevStart := prevEnd.Add(-end.Sub(start))
		previousDaily, err := s.store.DailyCostsByService(r.Context(), focus.DefaultTenant, prevStart, prevEnd, currency, "", storage.GroupByService)
		if err != nil {
			http.Error(w, "querying daily costs: "+err.Error(), http.StatusInternalServerError)
			return
		}
		in.PreviousServices, _, _ = periodKeyTotals(previousDaily)
		in.PreviousHasDays = len(previousDaily.Days) > 0
	}

	// Untagged-spend: every known tag key's (untagged) bucket vs that query's total.
	tagKeys, err := s.store.TagKeys(r.Context(), focus.DefaultTenant, start, end)
	if err != nil {
		http.Error(w, "querying daily costs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	for _, tk := range tagKeys {
		tagDaily, err := s.store.DailyCostsByTag(r.Context(), focus.DefaultTenant, start, end, tk, currency, "")
		if err != nil {
			http.Error(w, "querying daily costs: "+err.Error(), http.StatusInternalServerError)
			return
		}
		totals, grand, _ := periodKeyTotals(tagDaily)
		in.TagSpends = append(in.TagSpends, insights.TagSpend{
			TagKey:        tk,
			UntaggedTotal: totals["(untagged)"],
			WindowTotal:   grand,
		})
	}

	// Unallocated-spend: silent suppress when rules are missing or malformed.
	if dim, ready := s.tryLoadAllocationDimension(); ready {
		allocDaily, err := s.store.DailyCostsByAllocation(r.Context(), focus.DefaultTenant, start, end, dim, currency, "")
		if err != nil {
			http.Error(w, "querying daily costs: "+err.Error(), http.StatusInternalServerError)
			return
		}
		totals, grand, _ := periodKeyTotals(allocDaily)
		in.AllocationReady = true
		in.UnallocatedTotal = totals[allocation.UnallocatedLabel]
		in.AllocationWindowTotal = grand
	}

	// Anomaly-digest: full-history score, window-filtered flags (same as GetAnomalies).
	historyDaily, err := s.store.DailyCostsByService(r.Context(), focus.DefaultTenant, time.Time{}, end, currency, "", storage.GroupByService)
	if err != nil {
		http.Error(w, "querying daily costs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	for _, sf := range anomalyscan.Flags(historyDaily) {
		if inRange(sf.Flag.Date, start, end) {
			in.Flags = append(in.Flags, sf)
		}
	}

	// Unit-cost-drift: one series per known business metric when the window is bounded.
	if !start.IsZero() && !end.IsZero() {
		metricInfos, err := s.store.BusinessMetricNames(r.Context(), focus.DefaultTenant)
		if err != nil {
			http.Error(w, "querying business metric names: "+err.Error(), http.StatusInternalServerError)
			return
		}
		prevEnd := start.AddDate(0, 0, -1)
		prevStart := prevEnd.Add(-end.Sub(start))
		for _, info := range metricInfos {
			curQty, err := s.store.DailyBusinessMetricQuantities(r.Context(), focus.DefaultTenant, info.Name, start, end)
			if err != nil {
				http.Error(w, "querying daily business metric quantities: "+err.Error(), http.StatusInternalServerError)
				return
			}
			prevQty, err := s.store.DailyBusinessMetricQuantities(r.Context(), focus.DefaultTenant, info.Name, prevStart, prevEnd)
			if err != nil {
				http.Error(w, "querying daily business metric quantities: "+err.Error(), http.StatusInternalServerError)
				return
			}
			// Previous window costs for this metric: whole-tenant service-grouped (same as unit-economics).
			prevCosts, err := s.store.DailyCostsByService(r.Context(), focus.DefaultTenant, prevStart, prevEnd, currency, "", storage.GroupByService)
			if err != nil {
				http.Error(w, "querying daily costs: "+err.Error(), http.StatusInternalServerError)
				return
			}
			in.Metrics = append(in.Metrics, insights.MetricSeries{
				Name:               info.Name,
				CurrentCosts:       currentDaily,
				CurrentQuantities:  curQty,
				PreviousCosts:      prevCosts,
				PreviousQuantities: prevQty,
			})
		}
	}

	// Commitment-realization from the per-currency billed/effective totals.
	totals, err := s.store.CostTotals(r.Context(), focus.DefaultTenant, start, end)
	if err != nil {
		http.Error(w, "querying cost totals: "+err.Error(), http.StatusInternalServerError)
		return
	}
	for _, row := range totals {
		if row.Currency == currency {
			in.HasCommitment = true
			in.BilledTotal = row.Billed
			in.EffectiveTotal = row.Effective
			break
		}
	}

	computed := insights.Compute(in)
	resp.Insights = make([]Insight, 0, len(computed))
	for _, c := range computed {
		resp.Insights = append(resp.Insights, toAPIInsight(c))
	}
	writeJSON(w, resp)
}

// resolveInsightsCurrency picks the digest currency: explicit parameter, else
// alphabetically-first in-window billing currency, else full-history fallback
// (matching GetAnomalies). Used only by GetInsights.
func (s *Server) resolveInsightsCurrency(w http.ResponseWriter, r *http.Request, start, end time.Time, param *string) (currency string, currencies []string, ok bool) {
	currencies, err := s.store.BillingCurrencies(r.Context(), focus.DefaultTenant, start, end, "")
	if err != nil {
		http.Error(w, "querying daily costs: "+err.Error(), http.StatusInternalServerError)
		return "", nil, false
	}
	if currencies == nil {
		currencies = []string{}
	}
	currency = ""
	if param != nil {
		currency = *param
	} else if len(currencies) > 0 {
		currency = currencies[0]
	} else {
		// Window empty: fall back to full history so anomaly scoring never trips
		// the mixed-currency guard with currency "".
		full, err := s.store.BillingCurrencies(r.Context(), focus.DefaultTenant, time.Time{}, end, "")
		if err != nil {
			http.Error(w, "querying daily costs: "+err.Error(), http.StatusInternalServerError)
			return "", nil, false
		}
		if len(full) > 0 {
			currency = full[0]
		}
	}
	return currency, currencies, true
}

// tryLoadAllocationDimension loads allocation rules without writing to the
// ResponseWriter. Missing or malformed files suppress the unallocated insight.
func (s *Server) tryLoadAllocationDimension() (allocation.Dimension, bool) {
	if s.allocationRulesPath == "" {
		return allocation.Dimension{}, false
	}
	f, err := os.Open(s.allocationRulesPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return allocation.Dimension{}, false
		}
		return allocation.Dimension{}, false
	}
	defer func() { _ = f.Close() }()
	dim, err := allocation.Parse(f)
	if err != nil {
		return allocation.Dimension{}, false
	}
	return dim, true
}

func toInsightParameters(p insights.Parameters) InsightParameters {
	return InsightParameters{
		K:                   p.K,
		ConsistencyConstant: p.ConsistencyConstant,
		WindowDays:          p.WindowDays,
		MinObservations:     p.MinObservations,
		RelativeFloor:       p.RelativeFloor,
		DivisionScale:       p.DivisionScale,
	}
}

func toAPIInsight(c insights.Insight) Insight {
	out := Insight{
		Type:      c.Type,
		Title:     c.Title,
		Body:      c.Body,
		Magnitude: c.Magnitude.String(),
		Evidence:  make([]InsightEvidence, 0, len(c.Evidence)),
		Period:    toAPIPeriod(c.Period),
		Link:      toAPILink(c.Link),
	}
	if c.Dimension != "" {
		d := c.Dimension
		out.Dimension = &d
	}
	if c.Key != "" {
		k := c.Key
		out.Key = &k
	}
	for _, e := range c.Evidence {
		out.Evidence = append(out.Evidence, InsightEvidence{Name: e.Name, Value: e.Value})
	}
	return out
}

func toAPIPeriod(p insights.Period) InsightPeriod {
	var out InsightPeriod
	if !p.Start.IsZero() {
		d := openapi_types.Date{Time: p.Start}
		out.Start = &d
	}
	if !p.End.IsZero() {
		d := openapi_types.Date{Time: p.End}
		out.End = &d
	}
	if !p.PreviousStart.IsZero() {
		d := openapi_types.Date{Time: p.PreviousStart}
		out.PreviousStart = &d
	}
	if !p.PreviousEnd.IsZero() {
		d := openapi_types.Date{Time: p.PreviousEnd}
		out.PreviousEnd = &d
	}
	return out
}

func toAPILink(l insights.Link) InsightLink {
	var out InsightLink
	if l.View != "" {
		v := l.View
		out.View = &v
	}
	if l.Start != "" {
		s := l.Start
		out.Start = &s
	}
	if l.End != "" {
		e := l.End
		out.End = &e
	}
	if l.GroupBy != "" {
		g := l.GroupBy
		out.GroupBy = &g
	}
	if l.TagKey != "" {
		t := l.TagKey
		out.TagKey = &t
	}
	if l.Currency != "" {
		c := l.Currency
		out.Currency = &c
	}
	if l.Provider != "" {
		p := l.Provider
		out.Provider = &p
	}
	if l.Metric != "" {
		m := l.Metric
		out.Metric = &m
	}
	return out
}
