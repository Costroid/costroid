// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package api

import (
	"net/http"
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/anomaly"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/storage"
)

// GetAnomalies implements GET /api/v1/anomalies. It mirrors GetDailyCosts's
// grouping (service default, provider, allocation) and its 400/500 semantics,
// but ALWAYS fetches the full stored history (zero start) up to the requested
// end and scores everything, returning only the flags whose day falls inside the
// requested [start, end]. That makes flags range-INDEPENDENT: a given day yields
// the identical flag regardless of the queried start.
//
// When no currency is selected, mixed-currency history defaults to the
// alphabetically-first billing currency. Detection is stateless (no storage of
// its own), so a retroactive FOCUS correction rewriting a past day is
// automatically re-scored.
func (s *Server) GetAnomalies(w http.ResponseWriter, r *http.Request, params GetAnomaliesParams) {
	var start, end time.Time // requested filter window; zero = unbounded on that side
	if params.Start != nil {
		start = params.Start.Time
	}
	if params.End != nil {
		end = params.End.Time
	}
	if params.GroupBy != nil && !params.GroupBy.Valid() {
		http.Error(w, "invalid groupBy value", http.StatusBadRequest)
		return
	}
	if params.Currency != nil && !billingCurrencyPattern.MatchString(*params.Currency) {
		http.Error(w, "currency must be a three-letter uppercase code (for example, USD)", http.StatusBadRequest)
		return
	}

	var (
		daily   storage.DailyCosts
		err     error
		groupBy = "service"
	)
	currencies, err := s.store.BillingCurrencies(r.Context(), focus.DefaultTenant, time.Time{}, end)
	if err != nil {
		http.Error(w, "querying daily costs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	currency := ""
	if params.Currency != nil {
		currency = *params.Currency
	} else if len(currencies) > 0 {
		currency = currencies[0]
	}
	// The store fetch uses a ZERO start (full history) so scoring is
	// range-independent; end still bounds it (later days cannot change past flags).
	switch {
	case params.GroupBy != nil && *params.GroupBy == GetAnomaliesParamsGroupByAllocation:
		groupBy = "allocation"
		dim, ok := s.loadAllocationDimension(w)
		if !ok {
			return // loadAllocationDimension already wrote the error response
		}
		daily, err = s.store.DailyCostsByAllocation(r.Context(), focus.DefaultTenant, time.Time{}, end, dim, currency)
	case params.GroupBy != nil && *params.GroupBy == GetAnomaliesParamsGroupByProvider:
		groupBy = "provider"
		daily, err = s.store.DailyCostsByService(r.Context(), focus.DefaultTenant, time.Time{}, end, currency, storage.GroupByProvider)
	default:
		daily, err = s.store.DailyCostsByService(r.Context(), focus.DefaultTenant, time.Time{}, end, currency, storage.GroupByService)
	}
	if err != nil {
		http.Error(w, "querying daily costs: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, buildAnomalies(daily, groupBy, start, end))
}

// buildAnomalies scores the per-day TOTAL series (the sum over each day's keys,
// added exactly in Go) and each grouping key's own series, then returns the flags
// whose day is inside [start, end]. An observation for a key exists only on days
// that key has data; the total exists only on days present in the response.
// Ordering is deterministic: date-ascending, then total scope before key scope,
// then key-ascending. The parameters block echoes the exact detector constants.
func buildAnomalies(daily storage.DailyCosts, groupBy string, start, end time.Time) Anomalies {
	total := make([]anomaly.Observation, 0, len(daily.Days))
	keySeries := map[string][]anomaly.Observation{}
	var keyOrder []string
	for _, day := range daily.Days {
		sum := decimal.Zero
		for _, svc := range day.Services {
			sum = sum.Add(svc.Cost)
			if _, seen := keySeries[svc.ServiceName]; !seen {
				keyOrder = append(keyOrder, svc.ServiceName)
			}
			keySeries[svc.ServiceName] = append(keySeries[svc.ServiceName],
				anomaly.Observation{Date: day.Date, Value: svc.Cost})
		}
		total = append(total, anomaly.Observation{Date: day.Date, Value: sum})
	}

	flags := []Anomaly{}
	for _, f := range anomaly.Detect(total) {
		if inRange(f.Date, start, end) {
			flags = append(flags, toAnomaly(f, "total", ""))
		}
	}
	for _, key := range keyOrder {
		for _, f := range anomaly.Detect(keySeries[key]) {
			if inRange(f.Date, start, end) {
				flags = append(flags, toAnomaly(f, "key", key))
			}
		}
	}
	sortAnomalies(flags)

	return Anomalies{
		Currency: daily.Currency,
		Parameters: AnomalyParameters{
			K:                   anomaly.K.String(),
			ConsistencyConstant: anomaly.ConsistencyConstant.String(),
			WindowDays:          anomaly.WindowDays,
			MinObservations:     anomaly.MinObservations,
			RelativeFloor:       anomaly.RelativeFloor.String(),
			GroupBy:             groupBy,
		},
		Anomalies: flags,
	}
}

// toAnomaly maps a detected Flag to the API shape. key is set ONLY for the "key"
// scope, so a "total" flag never carries a key and a real key literally named
// "total" stays unambiguous.
func toAnomaly(f anomaly.Flag, scope, key string) Anomaly {
	a := Anomaly{
		Scope:     scope,
		Direction: f.Direction,
		Observed:  f.Observed.String(),
		Median:    f.Median.String(),
		Mad:       f.MAD.String(),
		ScaledMad: f.ScaledMAD.String(),
		Threshold: f.Threshold.String(),
		Deviation: f.Deviation.String(),
	}
	a.Date.Time = f.Date
	if scope == "key" {
		a.Key = &key
	}
	return a
}

// inRange reports whether day falls within [start, end]; a zero bound is
// unbounded on that side. Bounds are inclusive UTC calendar days.
func inRange(day, start, end time.Time) bool {
	if !start.IsZero() && day.Before(start) {
		return false
	}
	if !end.IsZero() && day.After(end) {
		return false
	}
	return true
}

// sortAnomalies orders flags date-ascending, then total scope before key scope,
// then key-ascending. total-before-key is the REVERSE of a naive alphabetical
// scope sort ("key" < "total"), so it is placed first explicitly.
func sortAnomalies(flags []Anomaly) {
	sort.SliceStable(flags, func(i, j int) bool {
		a, b := flags[i], flags[j]
		if !a.Date.Equal(b.Date.Time) {
			return a.Date.Before(b.Date.Time)
		}
		if a.Scope != b.Scope {
			return a.Scope == "total" // total first, explicitly
		}
		return anomalyKey(a) < anomalyKey(b)
	})
}

// anomalyKey is the sort key for the key scope ("" for a total flag).
func anomalyKey(a Anomaly) string {
	if a.Key != nil {
		return *a.Key
	}
	return ""
}
