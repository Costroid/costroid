// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package fakeanthropic is a development/test-only fake of the two read-only
// Anthropic Admin endpoints the anthropic-cost connector uses —
// GET /v1/organizations/cost_report and GET
// /v1/organizations/usage_report/messages — served over an http.Handler backed
// by a local directory of canned per-month responses. It exists so the
// connector and the CLI can be verified fully offline; it is NOT product
// surface and must never ship in a release code path. Everything is
// stdlib-only.
//
// The directory holds, per month, "<YYYY-MM>.json" (a JSON ARRAY of cost
// buckets) and "<YYYY-MM>.usage.json" (a JSON ARRAY of usage buckets) — each
// the elements of the real response's "data" array. A month with no file serves
// an empty data array — exactly what the connector treats as an empty
// (restated-to-zero) month. The handler enforces the x-api-key header (401 on
// mismatch, as the real API does), requires anthropic-version, asserts each
// endpoint's request SHAPE per parameter (the usage path gets its own check),
// and paginates with a small page size so the connector genuinely follows
// has_more/next_page cursors on BOTH endpoints.
package fakeanthropic

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"
)

// AdminKey is the fake's expected Admin API key. It is deliberately shaped
// like a real key (sk-ant-admin01-…) so hygiene assertions that grep for
// "sk-ant" have something to catch if the connector ever leaked it.
const AdminKey = "sk-ant-admin01-FAKEcanary0000000000000000000000000000000000AA"

// anthropicVersion is the API version the real endpoint requires.
const anthropicVersion = "2023-06-01"

// costReportPath and usageReportPath are the two served paths.
const (
	costReportPath  = "/v1/organizations/cost_report"
	usageReportPath = "/v1/organizations/usage_report/messages"
)

// Handler serves canned cost-report responses from a directory.
type Handler struct {
	dir    string
	apiKey string

	// PageSize caps buckets per page (0 → 1), so small fixtures still force
	// multi-page cursor following. Set it before serving.
	PageSize int

	// RateLimitN, when > 0, makes the first N (authenticated, well-shaped)
	// cost-report requests answer 429 with the RetryAfter header before any
	// real response, so the connector's bounded-retry and give-up paths can
	// be exercised. Test-only. Set it before serving.
	RateLimitN int
	// RetryAfter is the Retry-After header value sent on those 429s. Keep it
	// tiny (e.g. "0.01" seconds) so tests never sleep for real.
	RetryAfter string

	// UsageFailMonth, when non-empty, makes the usage_report/messages endpoint
	// answer 500 for that "YYYY-MM" (the month derived from starting_at), so the
	// connector's usage-fetch-failure degrade path can be exercised while other
	// months ingest. Test-only. Set it before serving.
	UsageFailMonth string

	// LogWriter, when set, receives one line per request (method, path, and
	// whether a cursor was presented) — never any header or key.
	LogWriter io.Writer

	mu       sync.Mutex
	requests []Request
	n429     int
}

// Request records one served request for test assertions.
type Request struct {
	Method    string
	Path      string
	HadCursor bool
}

// New returns a Handler serving <dir>/<YYYY-MM>.json, expecting AdminKey.
func New(dir string) *Handler { return &Handler{dir: dir, apiKey: AdminKey} }

// NewWithKey returns a Handler that expects a specific API key.
func NewWithKey(dir, apiKey string) *Handler { return &Handler{dir: dir, apiKey: apiKey} }

// Requests returns the served requests in order (test instrumentation).
func (h *Handler) Requests() []Request {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Request(nil), h.requests...)
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	hadCursor := r.URL.Query().Get("page") != ""
	h.mu.Lock()
	h.requests = append(h.requests, Request{Method: r.Method, Path: r.URL.Path, HadCursor: hadCursor})
	h.mu.Unlock()
	if h.LogWriter != nil {
		_, _ = fmt.Fprintf(h.LogWriter, "fakeanthropic: %s %s cursor=%t\n", r.Method, r.URL.Path, hadCursor)
	}

	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET is supported")
		return
	}
	if r.URL.Path != costReportPath && r.URL.Path != usageReportPath {
		writeError(w, http.StatusNotFound, "not_found", "only the cost report and usage report endpoints are implemented")
		return
	}
	// Auth: the x-api-key must match; never echo what was presented.
	if r.Header.Get("x-api-key") != h.apiKey {
		writeError(w, http.StatusUnauthorized, "authentication_error", "invalid x-api-key")
		return
	}
	if r.Header.Get("anthropic-version") != anthropicVersion {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "missing or wrong anthropic-version header")
		return
	}

	q := r.URL.Query()
	month, err := monthFromRFC3339(q.Get("starting_at"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "starting_at must be an RFC 3339 timestamp")
		return
	}

	if r.URL.Path == usageReportPath {
		// The usage endpoint gets its OWN per-parameter shape check.
		if !requireUsageShape(w, q) {
			return
		}
		if h.UsageFailMonth != "" && month == h.UsageFailMonth {
			writeError(w, http.StatusInternalServerError, "api_error", "usage report temporarily unavailable")
			return
		}
		buckets, err := h.loadUsageMonth(month)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "api_error", err.Error())
			return
		}
		h.paginate(w, q, buckets)
		return
	}

	// Cost report path. Assert the connector's request SHAPE per parameter
	// (never a whole-query string compare), so a connector that regresses its
	// parameters fails visibly instead of silently. The rate-limit gate runs
	// first so a well-shaped request can still be throttled.
	if h.rateLimited(w) {
		return
	}
	if !requireShape(w, q) {
		return
	}
	buckets, err := h.loadMonth(month)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api_error", err.Error())
		return
	}
	h.paginate(w, q, buckets)
}

// paginate serves one page of buckets honoring the page cursor and PageSize,
// setting has_more/next_page — the shared pagination for both endpoints.
func (h *Handler) paginate(w http.ResponseWriter, q url.Values, buckets []json.RawMessage) {
	start := 0
	if p := q.Get("page"); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n >= 0 {
			start = n
		}
	}
	size := h.PageSize
	if size <= 0 {
		size = 1
	}
	if start > len(buckets) {
		start = len(buckets)
	}
	end := start + size
	hasMore := end < len(buckets)
	if end > len(buckets) {
		end = len(buckets)
	}
	nextPage := ""
	if hasMore {
		nextPage = strconv.Itoa(end)
	}
	writeJSON(w, http.StatusOK, response{
		Data:     buckets[start:end],
		HasMore:  hasMore,
		NextPage: nextPage,
	})
}

// rateLimited answers the first RateLimitN requests with a 429 (plus the
// configured Retry-After) and reports whether it did.
func (h *Handler) rateLimited(w http.ResponseWriter) bool {
	h.mu.Lock()
	limited := h.n429 < h.RateLimitN
	if limited {
		h.n429++
	}
	h.mu.Unlock()
	if !limited {
		return false
	}
	if h.RetryAfter != "" {
		w.Header().Set("Retry-After", h.RetryAfter)
	}
	writeError(w, http.StatusTooManyRequests, "rate_limit_error", "slow down")
	return true
}

// requireShape verifies the connector's documented request parameters per
// parameter and writes a 400 (returning false) on any mismatch. Anthropic
// documents group_by as the bracketed, repeated group_by[]=, so a bare
// group_by= is rejected outright.
func requireShape(w http.ResponseWriter, q url.Values) bool {
	if len(q["group_by"]) != 0 {
		writeError(w, http.StatusBadRequest, "invalid_request_error",
			"group_by must be sent bracketed as group_by[]=, not bare group_by=")
		return false
	}
	if !equalStringSet(q["group_by[]"], []string{"description", "workspace_id"}) {
		writeError(w, http.StatusBadRequest, "invalid_request_error",
			"group_by[] must be exactly {description, workspace_id}")
		return false
	}
	if got := q.Get("bucket_width"); got != "1d" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "bucket_width must be 1d, got "+got)
		return false
	}
	if got := q.Get("limit"); got != "31" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "limit must be 31, got "+got)
		return false
	}
	return true
}

// requireUsageShape verifies the connector's documented usage_report/messages
// request parameters per parameter: the bracketed group_by[]= must be EXACTLY
// the five join dims (never a bare group_by=), bucket_width=1d, limit=31.
func requireUsageShape(w http.ResponseWriter, q url.Values) bool {
	if len(q["group_by"]) != 0 {
		writeError(w, http.StatusBadRequest, "invalid_request_error",
			"group_by must be sent bracketed as group_by[]=, not bare group_by=")
		return false
	}
	if !equalStringSet(q["group_by[]"], []string{"model", "workspace_id", "context_window", "inference_geo", "service_tier"}) {
		writeError(w, http.StatusBadRequest, "invalid_request_error",
			"group_by[] must be exactly {model, workspace_id, context_window, inference_geo, service_tier}")
		return false
	}
	if got := q.Get("bucket_width"); got != "1d" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "bucket_width must be 1d, got "+got)
		return false
	}
	if got := q.Get("limit"); got != "31" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "limit must be 31, got "+got)
		return false
	}
	return true
}

// equalStringSet reports whether got and want hold the same values,
// order-independent (a multiset compare).
func equalStringSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	g := slices.Clone(got)
	w := slices.Clone(want)
	slices.Sort(g)
	slices.Sort(w)
	return slices.Equal(g, w)
}

// loadMonth reads <dir>/<month>.json as a JSON array of cost buckets; a
// missing file is an empty month.
func (h *Handler) loadMonth(month string) ([]json.RawMessage, error) {
	return loadBuckets(filepath.Join(h.dir, month+".json"), month+".json")
}

// loadUsageMonth reads <dir>/<month>.usage.json as a JSON array of usage
// buckets; a missing file is an empty month.
func (h *Handler) loadUsageMonth(month string) ([]json.RawMessage, error) {
	return loadBuckets(filepath.Join(h.dir, month+".usage.json"), month+".usage.json")
}

// loadBuckets reads a JSON array of raw bucket objects from path; a missing
// file is an empty month.
func loadBuckets(path, label string) ([]json.RawMessage, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []json.RawMessage{}, nil
		}
		return nil, err
	}
	var buckets []json.RawMessage
	if err := json.Unmarshal(body, &buckets); err != nil {
		return nil, fmt.Errorf("fixture %s is not a JSON array of buckets: %v", label, err)
	}
	return buckets, nil
}

func monthFromRFC3339(s string) (string, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return "", err
	}
	return t.UTC().Format("2006-01"), nil
}

type response struct {
	Data     []json.RawMessage `json:"data"`
	HasMore  bool              `json:"has_more"`
	NextPage string            `json:"next_page"`
}

type apiError struct {
	Type  string  `json:"type"`
	Error errBody `json:"error"`
}

type errBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, apiError{Type: "error", Error: errBody{Type: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
