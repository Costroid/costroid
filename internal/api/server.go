// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package api implements the Costroid HTTP API on top of the server
// scaffolding generated from contracts/openapi.yaml (see api.gen.go).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/allocation"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/storage"
)

// serviceName is the service identity reported by /api/v1/meta.
const serviceName = "costroid"

// focusVersion is the FOCUS specification version the internal model
// targets (decision D4).
const focusVersion = "1.4"

var billingCurrencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

var providerShapeMessage = fmt.Sprintf("provider must be a non-empty string of at most %d bytes", focus.MaxFreeTextBytes)

// CostStore is the slice of the storage interface the API reads from.
type CostStore interface {
	Providers(ctx context.Context, tenant string, start, end time.Time) ([]string, error)
	BillingCurrencies(ctx context.Context, tenant string, start, end time.Time, provider string) ([]string, error)
	DailyCostsByService(ctx context.Context, tenant string, start, end time.Time, currency, provider string, groupBy ...storage.CostGroupBy) (storage.DailyCosts, error)
	DailyCostsByAllocation(ctx context.Context, tenant string, start, end time.Time, dim allocation.Dimension, currency, provider string) (storage.DailyCosts, error)
	DailyTokensByService(ctx context.Context, tenant string, start, end time.Time) ([]storage.DailyTokenUsage, error)
	DailyUsageMetrics(ctx context.Context, tenant string, start, end time.Time) ([]storage.DailyUsageMetric, error)
	BusinessMetricNames(ctx context.Context, tenant string) ([]storage.BusinessMetricInfo, error)
	DailyBusinessMetricQuantities(ctx context.Context, tenant, metric string, start, end time.Time) ([]storage.DayQuantity, error)
	SyncStatuses(ctx context.Context) ([]storage.SyncStatus, error)
}

// SyncScheduleSource is the live scheduler state merged into persisted sync
// history by GET /api/v1/sync/status.
type SyncScheduleSource struct {
	Name      string
	Connector string
	Tenant    string
	Interval  string
	NextRunAt time.Time
}

// SyncScheduleProvider supplies a concurrency-safe scheduler snapshot.
type SyncScheduleProvider interface {
	SyncSchedule() []SyncScheduleSource
}

// Server implements the generated ServerInterface.
type Server struct {
	version string
	store   CostStore
	demo    bool
	// allocationRulesPath is the resolved path to the query-time allocation
	// rules JSON, or "" when unconfigured. The handler reads it per request
	// (the live-reload semantic); the file's presence and validity surface as
	// per-request 400/500, never at startup.
	allocationRulesPath string
	syncSchedule        SyncScheduleProvider
}

var _ ServerInterface = (*Server)(nil)

// NewServer returns a Server reporting the given binary version, querying the
// given store, and reading allocation rules from allocationRulesPath ("" =
// unconfigured).
func NewServer(version string, store CostStore, allocationRulesPath string) *Server {
	return &Server{version: version, store: store, allocationRulesPath: allocationRulesPath}
}

// GetHealthz implements GET /healthz.
func (s *Server) GetHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// GetMeta implements GET /api/v1/meta.
func (s *Server) GetMeta(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, Meta{
		Name:         serviceName,
		Version:      s.version,
		FocusVersion: focusVersion,
		Demo:         s.demo,
	})
}

// GetSyncStatus implements GET /api/v1/sync/status by merging configured live
// scheduler state with persisted history keyed by (source name, tenant).
func (s *Server) GetSyncStatus(w http.ResponseWriter, r *http.Request) {
	history, err := s.store.SyncStatuses(r.Context())
	if err != nil {
		http.Error(w, "querying sync status: "+err.Error(), http.StatusInternalServerError)
		return
	}
	byKey := make(map[string]storage.SyncStatus, len(history))
	for _, status := range history {
		byKey[syncStatusKey(status.Latest.SourceName, status.Latest.TenantID)] = status
	}

	response := SyncStatusResponse{Enabled: s.syncSchedule != nil, Sources: []SyncSourceStatus{}}
	if s.syncSchedule != nil {
		for _, configured := range s.syncSchedule.SyncSchedule() {
			entry := SyncSourceStatus{
				Name: configured.Name, Connector: configured.Connector, Tenant: configured.Tenant,
				Interval: &configured.Interval,
			}
			next := configured.NextRunAt.UTC()
			entry.NextRunAt = &next
			key := syncStatusKey(configured.Name, configured.Tenant)
			if status, ok := byKey[key]; ok {
				mergeSyncHistory(&entry, status)
				delete(byKey, key)
			}
			response.Sources = append(response.Sources, entry)
		}
	}
	for _, status := range history {
		key := syncStatusKey(status.Latest.SourceName, status.Latest.TenantID)
		if _, ok := byKey[key]; !ok {
			continue
		}
		entry := SyncSourceStatus{
			Name: status.Latest.SourceName, Connector: status.Latest.Connector,
			Tenant: status.Latest.TenantID,
		}
		mergeSyncHistory(&entry, status)
		response.Sources = append(response.Sources, entry)
		delete(byKey, key)
	}
	writeJSON(w, response)
}

func syncStatusKey(name, tenant string) string { return name + "\x00" + tenant }

func mergeSyncHistory(entry *SyncSourceStatus, status storage.SyncStatus) {
	latest := status.Latest
	lastRun := SyncLastRun{
		StartedAt: latest.StartedAt.UTC(), FinishedAt: latest.FinishedAt.UTC(),
		Outcome:          SyncLastRunOutcome(latest.Outcome),
		PeriodsProcessed: latest.PeriodsProcessed, PeriodsSkipped: latest.PeriodsSkipped,
		RecordsIngested: latest.RecordsIngested,
	}
	if latest.Error != "" {
		lastRun.Error = &latest.Error
	}
	entry.LastRun = &lastRun
	if status.LastSuccessAt != nil {
		lastSuccess := status.LastSuccessAt.UTC()
		entry.LastSuccessAt = &lastSuccess
	}
}

// GetDailyCosts implements GET /api/v1/costs/daily. Invalid date
// parameters never reach it: the generated binding wrapper rejects them
// with a 400 before the handler runs. Invalid groupBy values bind as
// strings, so this handler validates that enum explicitly. groupBy=allocation
// reads and applies the configured query-time allocation rules per request.
func (s *Server) GetDailyCosts(w http.ResponseWriter, r *http.Request, params GetDailyCostsParams) {
	var start, end time.Time // zero = unbounded
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
	if params.Provider != nil && (*params.Provider == "" || len(*params.Provider) > focus.MaxFreeTextBytes) {
		http.Error(w, providerShapeMessage, http.StatusBadRequest)
		return
	}

	provider := ""
	if params.Provider != nil {
		provider = *params.Provider
	}
	providers, err := s.store.Providers(r.Context(), focus.DefaultTenant, start, end)
	if err != nil {
		http.Error(w, "querying daily costs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	currencies, err := s.store.BillingCurrencies(r.Context(), focus.DefaultTenant, start, end, provider)
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

	var (
		daily    storage.DailyCosts
		queryErr error
	)
	if params.GroupBy != nil && *params.GroupBy == GetDailyCostsParamsGroupByAllocation {
		dim, ok := s.loadAllocationDimension(w)
		if !ok {
			return // loadAllocationDimension already wrote the error response
		}
		daily, queryErr = s.store.DailyCostsByAllocation(r.Context(), focus.DefaultTenant, start, end, dim, currency, provider)
	} else {
		groupBy := storage.GroupByService
		if params.GroupBy != nil && *params.GroupBy == GetDailyCostsParamsGroupByProvider {
			groupBy = storage.GroupByProvider
		}
		daily, queryErr = s.store.DailyCostsByService(r.Context(), focus.DefaultTenant, start, end, currency, provider, groupBy)
	}
	if queryErr != nil {
		http.Error(w, "querying daily costs: "+queryErr.Error(), http.StatusInternalServerError)
		return
	}

	resp := DailyCosts{
		Currency: daily.Currency, Currencies: currencies, Days: []DailyCost{},
		Provider: provider, Providers: providers,
	}
	grandTotal := decimal.Zero
	for _, day := range daily.Days {
		entry := DailyCost{
			Services: make([]ServiceCost, 0, len(day.Services)),
		}
		// openapi_types.Date embeds time.Time; set it via the embedded
		// field so hand-written code needs no oapi-codegen import.
		entry.Date.Time = day.Date
		dayTotal := decimal.Zero
		for _, svc := range day.Services {
			entry.Services = append(entry.Services, ServiceCost{
				Key:  svc.ServiceName,
				Cost: svc.Cost.String(),
			})
			dayTotal = dayTotal.Add(svc.Cost)
		}
		entry.Total = dayTotal.String()
		grandTotal = grandTotal.Add(dayTotal)
		resp.Days = append(resp.Days, entry)
	}
	resp.Total = grandTotal.String()
	writeJSON(w, resp)
}

// GetCostsSummary implements GET /api/v1/costs/summary. Reuses the daily-cost
// store methods (two window queries) and the grand-total decimal loop — no new
// store method. Money is always shopspring/decimal strings; never float64.
func (s *Server) GetCostsSummary(w http.ResponseWriter, r *http.Request, params GetCostsSummaryParams) {
	var start, end time.Time // zero = unbounded
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

	currencies, err := s.store.BillingCurrencies(r.Context(), focus.DefaultTenant, start, end, "")
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

	isAllocation := params.GroupBy != nil && *params.GroupBy == Allocation
	var dim allocation.Dimension
	if isAllocation {
		var ok bool
		dim, ok = s.loadAllocationDimension(w)
		if !ok {
			return
		}
	}

	current, err := s.queryDailyCosts(r.Context(), start, end, currency, params.GroupBy, dim, isAllocation)
	if err != nil {
		http.Error(w, "querying daily costs: "+err.Error(), http.StatusInternalServerError)
		return
	}

	curTotals, curGrand, curCurrency := periodKeyTotals(current)
	resp := CostsSummary{
		Currency:   curCurrency,
		Currencies: currencies,
		Total:      curGrand.String(),
		Keys:       make([]CostSummaryKey, 0, len(curTotals)),
	}

	// Preceding window is only defined for a fully bounded current range.
	prevDefined := !start.IsZero() && !end.IsZero()
	if currency == "" || len(current.Days) == 0 {
		prevDefined = false
	}
	var prevTotals map[string]decimal.Decimal
	var prevGrand decimal.Decimal
	var prevStart, prevEnd time.Time
	if prevDefined {
		// prevEnd = start − 1 day; prevStart = prevEnd − (end − start).
		prevEnd = start.AddDate(0, 0, -1)
		prevStart = prevEnd.Add(-end.Sub(start))
		previous, err := s.queryDailyCosts(r.Context(), prevStart, prevEnd, currency, params.GroupBy, dim, isAllocation)
		if err != nil {
			http.Error(w, "querying daily costs: "+err.Error(), http.StatusInternalServerError)
			return
		}
		prevTotals, prevGrand, _ = periodKeyTotals(previous)
		// Empty previous: never emit previousTotal "0".
		if len(previous.Days) == 0 {
			prevDefined = false
		}
	}

	// Stable key order: total desc, key asc.
	type keyTotal struct {
		key   string
		total decimal.Decimal
	}
	ordered := make([]keyTotal, 0, len(curTotals))
	for k, t := range curTotals {
		ordered = append(ordered, keyTotal{key: k, total: t})
	}
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].total.Equal(ordered[j].total) {
			return ordered[i].total.GreaterThan(ordered[j].total)
		}
		return ordered[i].key < ordered[j].key
	})

	for _, kt := range ordered {
		entry := CostSummaryKey{Key: kt.key, Total: kt.total.String()}
		if prevDefined {
			prev, ok := prevTotals[kt.key]
			if ok {
				pt := prev.String()
				entry.PreviousTotal = &pt
			} else {
				prev = decimal.Zero
			}
			delta := kt.total.Sub(prev).String()
			entry.Delta = &delta
		}
		resp.Keys = append(resp.Keys, entry)
	}

	if prevDefined {
		pg := prevGrand.String()
		resp.PreviousTotal = &pg
		ps := openapi_types.Date{Time: prevStart}
		pe := openapi_types.Date{Time: prevEnd}
		resp.PreviousStart = &ps
		resp.PreviousEnd = &pe
	}

	writeJSON(w, resp)
}

// queryDailyCosts runs the same store path as GetDailyCosts for one window.
// Allocation rules are pre-resolved once by the caller and shared across both
// windows. On store failure this returns the error without writing a response.
func (s *Server) queryDailyCosts(ctx context.Context, start, end time.Time, currency string, groupBy *GetCostsSummaryParamsGroupBy, dim allocation.Dimension, isAllocation bool) (storage.DailyCosts, error) {
	if isAllocation {
		return s.store.DailyCostsByAllocation(ctx, focus.DefaultTenant, start, end, dim, currency, "")
	}
	gb := storage.GroupByService
	if groupBy != nil && *groupBy == Provider {
		gb = storage.GroupByProvider
	}
	return s.store.DailyCostsByService(ctx, focus.DefaultTenant, start, end, currency, "", gb)
}

// periodKeyTotals sums per-key period totals and the grand total with exact
// decimal arithmetic (same loop pattern as GetDailyCosts).
func periodKeyTotals(daily storage.DailyCosts) (map[string]decimal.Decimal, decimal.Decimal, string) {
	totals := make(map[string]decimal.Decimal)
	grand := decimal.Zero
	for _, day := range daily.Days {
		for _, svc := range day.Services {
			totals[svc.ServiceName] = totals[svc.ServiceName].Add(svc.Cost)
			grand = grand.Add(svc.Cost)
		}
	}
	return totals, grand, daily.Currency
}

// loadAllocationDimension reads, parses, and validates the configured
// allocation rules file PER REQUEST (the live-reload semantic — the file is
// tiny). It writes the appropriate error response itself and returns ok=false
// on any failure:
//   - no path configured           → 400, the unconfigured message (reached in
//     production only when os.UserConfigDir() itself errors, since serve then
//     starts with an empty path rather than failing startup);
//   - path set but file missing    → 400, naming the path with a create-it hint
//     (the message a never-configured user sees);
//   - unreadable/malformed/invalid → 500, "loading allocation rules: <err>".
func (s *Server) loadAllocationDimension(w http.ResponseWriter) (allocation.Dimension, bool) {
	if s.allocationRulesPath == "" {
		http.Error(w, "no allocation rules configured (start serve with --allocation-rules or set $COSTROID_ALLOCATION_RULES)", http.StatusBadRequest)
		return allocation.Dimension{}, false
	}
	f, err := os.Open(s.allocationRulesPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.Error(w, fmt.Sprintf("allocation rules file not found: %s (create it, or start serve with --allocation-rules or set $COSTROID_ALLOCATION_RULES)", s.allocationRulesPath), http.StatusBadRequest)
			return allocation.Dimension{}, false
		}
		http.Error(w, "loading allocation rules: "+err.Error(), http.StatusInternalServerError)
		return allocation.Dimension{}, false
	}
	defer func() { _ = f.Close() }()

	dim, err := allocation.Parse(f)
	if err != nil {
		http.Error(w, "loading allocation rules: "+err.Error(), http.StatusInternalServerError)
		return allocation.Dimension{}, false
	}
	return dim, true
}

// GetDailyTokens implements GET /api/v1/usage/tokens/daily. Invalid date
// parameters never reach it: the generated binding wrapper rejects them
// with a 400 before the handler runs. Quantities are rendered as exact
// decimal strings (decisions D23, D25) — never floats. Only enriched token
// rows are returned; the store excludes money-only (null-quantity) rows.
func (s *Server) GetDailyTokens(w http.ResponseWriter, r *http.Request, params GetDailyTokensParams) {
	var start, end time.Time // zero = unbounded
	if params.Start != nil {
		start = params.Start.Time
	}
	if params.End != nil {
		end = params.End.Time
	}

	usage, err := s.store.DailyTokensByService(r.Context(), focus.DefaultTenant, start, end)
	if err != nil {
		http.Error(w, "querying daily token usage: "+err.Error(), http.StatusInternalServerError)
		return
	}

	resp := make([]DailyTokenUsage, 0, len(usage))
	for _, u := range usage {
		entry := DailyTokenUsage{
			ServiceName:      u.ServiceName,
			ConsumedUnit:     u.ConsumedUnit,
			ConsumedQuantity: u.Quantity.String(),
		}
		// openapi_types.Date embeds time.Time; set it via the embedded
		// field so hand-written code needs no oapi-codegen import.
		entry.Date.Time = u.Date
		resp = append(resp, entry)
	}
	writeJSON(w, resp)
}

// GetDailyUsageMetrics implements GET /api/v1/usage/metrics/daily. Invalid date
// parameters never reach it: the generated binding wrapper rejects them with a
// 400 before the handler runs. Quantities are rendered as exact decimal strings
// (decisions D23, D25) — never floats. These metrics live outside the FOCUS cost
// dataset, so they never overlap the daily-cost or daily-token views.
func (s *Server) GetDailyUsageMetrics(w http.ResponseWriter, r *http.Request, params GetDailyUsageMetricsParams) {
	var start, end time.Time // zero = unbounded
	if params.Start != nil {
		start = params.Start.Time
	}
	if params.End != nil {
		end = params.End.Time
	}

	metrics, err := s.store.DailyUsageMetrics(r.Context(), focus.DefaultTenant, start, end)
	if err != nil {
		http.Error(w, "querying daily usage metrics: "+err.Error(), http.StatusInternalServerError)
		return
	}

	resp := make([]DailyUsageMetric, 0, len(metrics))
	for _, m := range metrics {
		entry := DailyUsageMetric{
			ServiceName: m.ServiceName,
			ServiceTier: m.ServiceTier,
			MetricName:  m.MetricName,
			Unit:        m.Unit,
			Quantity:    m.Quantity.String(),
		}
		// openapi_types.Date embeds time.Time; set it via the embedded
		// field so hand-written code needs no oapi-codegen import.
		entry.Date.Time = m.Date
		resp = append(resp, entry)
	}
	writeJSON(w, resp)
}

// GetBusinessMetrics implements GET /api/v1/business-metrics.
func (s *Server) GetBusinessMetrics(w http.ResponseWriter, r *http.Request) {
	infos, err := s.store.BusinessMetricNames(r.Context(), focus.DefaultTenant)
	if err != nil {
		http.Error(w, "querying business metric names: "+err.Error(), http.StatusInternalServerError)
		return
	}
	resp := BusinessMetrics{Metrics: make([]BusinessMetricInfo, 0, len(infos))}
	for _, info := range infos {
		entry := BusinessMetricInfo{Name: info.Name}
		entry.FirstDay.Time = info.FirstDay
		entry.LastDay.Time = info.LastDay
		resp.Metrics = append(resp.Metrics, entry)
	}
	writeJSON(w, resp)
}

// GetDailyUnitEconomics implements GET /api/v1/unit-economics/daily. Cost and
// quantity stay exact decimals; division happens only in Go at explicit scale
// 18 and the result is transported as a string.
func (s *Server) GetDailyUnitEconomics(w http.ResponseWriter, r *http.Request, params GetDailyUnitEconomicsParams) {
	// metric is a required query parameter (the generated binding wrapper 400s a
	// wholly-absent metric before the handler runs); a present-but-empty
	// "?metric=" still binds "" and is rejected here.
	if params.Metric == "" {
		http.Error(w, "metric query parameter is required and must be non-empty", http.StatusBadRequest)
		return
	}
	if params.Currency != nil && !billingCurrencyPattern.MatchString(*params.Currency) {
		http.Error(w, "currency must be a three-letter uppercase code (for example, USD)", http.StatusBadRequest)
		return
	}
	metric := params.Metric
	start, end := time.Time{}, time.Time{}
	if params.Start != nil {
		start = params.Start.Time
	}
	if params.End != nil {
		end = params.End.Time
	}

	infos, err := s.store.BusinessMetricNames(r.Context(), focus.DefaultTenant)
	if err != nil {
		http.Error(w, "querying business metric names: "+err.Error(), http.StatusInternalServerError)
		return
	}
	known := false
	for _, info := range infos {
		if info.Name == metric {
			known = true
			break
		}
	}
	if !known {
		http.Error(w, fmt.Sprintf("unknown business metric %q; list available metrics at /api/v1/business-metrics", metric), http.StatusNotFound)
		return
	}

	currencies, err := s.store.BillingCurrencies(r.Context(), focus.DefaultTenant, start, end, "")
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

	costs, err := s.store.DailyCostsByService(r.Context(), focus.DefaultTenant, start, end, currency, "")
	if err != nil {
		http.Error(w, "querying daily costs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	quantities, err := s.store.DailyBusinessMetricQuantities(r.Context(), focus.DefaultTenant, metric, start, end)
	if err != nil {
		http.Error(w, "querying daily business metric quantities: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, mergeUnitEconomics(metric, costs, quantities, currencies))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}

// HandlerOption configures optional middleware wrapped around the root handler
// by NewHandler. Existing callers pass none and stay auth-free.
type HandlerOption func(*handlerOptions)

type handlerOptions struct {
	auth     *AuthConfig
	readOnly bool
	demo     bool
	sync     SyncScheduleProvider
}

// WithAuth installs the authentication middleware built from cfg, gating the
// data endpoints (see auth.go). Without it, NewHandler serves unauthenticated —
// so secure-by-default lives in serve (which fails closed unless a mode or
// --no-auth is configured), not in this constructor's default.
func WithAuth(cfg AuthConfig) HandlerOption {
	return func(o *handlerOptions) { o.auth = &cfg }
}

// WithReadOnly rejects every non-GET/HEAD request under /api/. The middleware
// wraps outside the generated mux so future write routes are denied without
// relying on today's route table. Static assets and /healthz remain exempt.
func WithReadOnly() HandlerOption {
	return func(o *handlerOptions) { o.readOnly = true }
}

// WithDemo marks /api/v1/meta as an isolated synthetic demo instance.
func WithDemo() HandlerOption {
	return func(o *handlerOptions) { o.demo = true }
}

// WithSyncSchedule enables the status endpoint's live scheduler fields. The
// provider must return a concurrency-safe snapshot.
func WithSyncSchedule(provider SyncScheduleProvider) HandlerOption {
	return func(o *handlerOptions) { o.sync = provider }
}

func readOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gated(r.URL.Path) && r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// NewHandler returns the root HTTP handler: the API routes from the
// generated scaffolding plus the built dashboard served from static at /.
// allocationRulesPath ("" = unconfigured) is the query-time allocation rules
// file, read per request by the daily-cost handler. Options may wrap the
// handler in middleware (e.g. WithAuth); the generated scaffolding in
// api.gen.go is never touched.
func NewHandler(version string, static fs.FS, store CostStore, allocationRulesPath string, opts ...HandlerOption) http.Handler {
	var o handlerOptions
	for _, opt := range opts {
		opt(&o)
	}
	mux := http.NewServeMux()
	mux.Handle("/", staticHandler(static))
	server := NewServer(version, store, allocationRulesPath)
	server.demo = o.demo
	server.syncSchedule = o.sync
	h := HandlerFromMux(server, mux)
	if o.auth != nil {
		h = o.auth.middleware(h)
	}
	if o.readOnly {
		h = readOnly(h)
	}
	return h
}

// staticHandler serves the embedded dashboard: no directory listings, no
// dotfiles, and unknown extensionless GET paths outside /api/ and
// /healthz fall back to index.html so client-side routes work (SPA).
func staticHandler(static fs.FS) http.Handler {
	files := http.FileServerFS(static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// All checks run on the decoded, cleaned path: the raw path can
		// hide API routes behind encodings (e.g. /%2Fapi/... decodes to
		// //api/..., which must 404, not fall back to index.html).
		cleaned := path.Clean(r.URL.Path)
		name := strings.TrimPrefix(cleaned, "/")
		if hasDotSegment(name) {
			http.NotFound(w, r)
			return
		}

		if name != "" {
			if info, err := fs.Stat(static, name); err == nil && !info.IsDir() {
				files.ServeHTTP(w, r)
				return
			}
			// Unknown /api/ paths and asset-like paths (with a file
			// extension) are real 404s, not the SPA fallback.
			if strings.HasPrefix(cleaned, "/api/") || cleaned == "/healthz" || path.Ext(name) != "" {
				http.NotFound(w, r)
				return
			}
		}

		index, err := fs.ReadFile(static, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(index)
		}
	})
}

// hasDotSegment reports whether any path segment starts with a dot
// (e.g. /.gitkeep, /assets/.hidden) — those are never served.
func hasDotSegment(name string) bool {
	for _, seg := range strings.Split(name, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}
