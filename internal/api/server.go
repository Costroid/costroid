// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package api implements the Costroid HTTP API on top of the server
// scaffolding generated from contracts/openapi.yaml (see api.gen.go).
package api

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/shopspring/decimal"

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
	DailyCostsByService(ctx context.Context, tenant string, start, end time.Time) (storage.DailyCosts, error)
}

// Server implements the generated ServerInterface.
type Server struct {
	version string
	store   CostStore
}

var _ ServerInterface = (*Server)(nil)

// NewServer returns a Server reporting the given binary version and
// querying the given store.
func NewServer(version string, store CostStore) *Server {
	return &Server{version: version, store: store}
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
// with a 400 before the handler runs.
func (s *Server) GetDailyCosts(w http.ResponseWriter, r *http.Request, params GetDailyCostsParams) {
	var start, end time.Time // zero = unbounded
	if params.Start != nil {
		start = params.Start.Time
	}
	if params.End != nil {
		end = params.End.Time
	}

	daily, err := s.store.DailyCostsByService(r.Context(), focus.DefaultTenant, start, end)
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
				ServiceName: svc.ServiceName,
				Cost:        svc.Cost.String(),
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}

// NewHandler returns the root HTTP handler: the API routes from the
// generated scaffolding plus the built dashboard served from static at /.
func NewHandler(version string, static fs.FS, store CostStore) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", staticHandler(static))
	return HandlerFromMux(NewServer(version, store), mux)
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
