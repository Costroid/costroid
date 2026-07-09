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
	"strings"
	"time"

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

// CostStore is the slice of the storage interface the API reads from.
type CostStore interface {
	DailyCostsByService(ctx context.Context, tenant string, start, end time.Time, groupBy ...storage.CostGroupBy) (storage.DailyCosts, error)
	DailyCostsByAllocation(ctx context.Context, tenant string, start, end time.Time, dim allocation.Dimension) (storage.DailyCosts, error)
	DailyTokensByService(ctx context.Context, tenant string, start, end time.Time) ([]storage.DailyTokenUsage, error)
	DailyUsageMetrics(ctx context.Context, tenant string, start, end time.Time) ([]storage.DailyUsageMetric, error)
}

// Server implements the generated ServerInterface.
type Server struct {
	version string
	store   CostStore
	// allocationRulesPath is the resolved path to the query-time allocation
	// rules JSON, or "" when unconfigured. The handler reads it per request
	// (the live-reload semantic); the file's presence and validity surface as
	// per-request 400/500, never at startup.
	allocationRulesPath string
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
	})
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

	var (
		daily storage.DailyCosts
		err   error
	)
	if params.GroupBy != nil && *params.GroupBy == Allocation {
		dim, ok := s.loadAllocationDimension(w)
		if !ok {
			return // loadAllocationDimension already wrote the error response
		}
		daily, err = s.store.DailyCostsByAllocation(r.Context(), focus.DefaultTenant, start, end, dim)
	} else {
		groupBy := storage.GroupByService
		if params.GroupBy != nil && *params.GroupBy == Provider {
			groupBy = storage.GroupByProvider
		}
		daily, err = s.store.DailyCostsByService(r.Context(), focus.DefaultTenant, start, end, groupBy)
	}
	if err != nil {
		http.Error(w, "querying daily costs: "+err.Error(), http.StatusInternalServerError)
		return
	}

	resp := DailyCosts{Currency: daily.Currency, Days: []DailyCost{}}
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}

// NewHandler returns the root HTTP handler: the API routes from the
// generated scaffolding plus the built dashboard served from static at /.
// allocationRulesPath ("" = unconfigured) is the query-time allocation rules
// file, read per request by the daily-cost handler.
func NewHandler(version string, static fs.FS, store CostStore, allocationRulesPath string) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", staticHandler(static))
	return HandlerFromMux(NewServer(version, store, allocationRulesPath), mux)
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
