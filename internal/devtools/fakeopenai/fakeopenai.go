// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package fakeopenai is a development/test-only fake of the read-only OpenAI
// Admin endpoints the openai-cost connector uses — GET /v1/organization/costs
// and the ten usage endpoints under GET /v1/organization/usage/<name>
// (completions, embeddings, moderations, images, audio_speeches,
// audio_transcriptions, code_interpreter_sessions, vector_stores,
// web_search_calls, file_search_calls) — served over an http.Handler backed by a
// local directory of canned per-month responses. It exists so the connector and
// the CLI can be verified fully offline; it is NOT product surface and must never
// ship in a release code path. Everything is stdlib-only.
//
// The directory holds, per month, "<YYYY-MM>.json" (a JSON ARRAY of cost
// buckets) and, per usage endpoint, "<YYYY-MM>.usage.<name>.json" (a JSON ARRAY
// of usage buckets) — each the elements of the real response's "data" array. The
// usage-fixture <name> is the request path's last segment VERBATIM. A month/
// endpoint with no file serves an empty data array. The handler enforces the
// Authorization: Bearer header (401 on mismatch), asserts each endpoint's request
// SHAPE per parameter (the usage paths get their own check — bare group_by=model
// for model endpoints, or NO group_by for model-less endpoints; limit=31), and
// paginates with a small page size so the connector genuinely follows
// has_more/next_page cursors.
package fakeopenai

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
	"strings"
	"sync"
	"time"
)

// AdminKey is the fake's expected Admin API key, shaped like a real OpenAI
// admin key so hygiene assertions have something to catch.
const AdminKey = "sk-admin-FAKEcanary000000000000000000000000000000000000T3BlbkFJ"

// costsPath is the monetary endpoint path; usagePathPrefix is the shared prefix
// of the ten usage endpoints (usage/<name>).
const (
	costsPath       = "/v1/organization/costs"
	usagePathPrefix = "/v1/organization/usage/"
)

// Handler serves canned costs responses from a directory.
type Handler struct {
	dir    string
	apiKey string

	// PageSize caps buckets per page (0 → 1). Set it before serving.
	PageSize int

	// RateLimitN, when > 0, makes the first N (authenticated, well-shaped)
	// costs requests answer 429 with the RetryAfter header before any real
	// response, so the connector's bounded-retry and give-up paths can be
	// exercised. Test-only. Set it before serving.
	RateLimitN int
	// RetryAfter is the Retry-After header value sent on those 429s. Keep it
	// tiny (e.g. "0.01" seconds) so tests never sleep for real.
	RetryAfter string

	// UsageFailMonth, when non-empty, makes the usage endpoints answer 500 for
	// that "YYYY-MM" (the month derived from start_time), so the connector's
	// usage-fetch-failure degrade path can be exercised while cost still ingests.
	// UsageFailEndpoint, when also set, narrows the 500 to that one endpoint
	// (its path last segment, e.g. "completions"); empty → every usage endpoint
	// for the month fails. Test-only. Set them before serving.
	UsageFailMonth    string
	UsageFailEndpoint string

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
		_, _ = fmt.Fprintf(h.LogWriter, "fakeopenai: %s %s cursor=%t\n", r.Method, r.URL.Path, hadCursor)
	}

	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET is supported")
		return
	}
	isCost := r.URL.Path == costsPath
	isUsage := strings.HasPrefix(r.URL.Path, usagePathPrefix)
	if !isCost && !isUsage {
		writeError(w, http.StatusNotFound, "not_found", "only the costs and usage endpoints are implemented")
		return
	}
	// Auth: Authorization: Bearer <key>; never echo what was presented.
	if r.Header.Get("Authorization") != "Bearer "+h.apiKey {
		writeError(w, http.StatusUnauthorized, "invalid_api_key", "invalid Authorization bearer token")
		return
	}

	q := r.URL.Query()
	if isUsage {
		h.serveUsage(w, r, q)
		return
	}

	// Cost path. Assert the connector's request SHAPE per parameter (never a
	// whole-query string compare). The rate-limit gate runs first so a
	// well-shaped request can still be throttled.
	if h.rateLimited(w) {
		return
	}
	if !requireShape(w, q) {
		return
	}
	month, err := monthFromUnix(q.Get("start_time"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "start_time must be Unix seconds")
		return
	}
	buckets, err := h.loadMonth(month)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api_error", err.Error())
		return
	}
	h.paginate(w, q, buckets)
}

// serveUsage handles a GET under usagePathPrefix: it asserts the per-endpoint
// usage shape, honors the optional per-endpoint fail mode, and paginates the
// canned "<YYYY-MM>.usage.<name>.json" fixtures. name is the path's last segment
// VERBATIM (never a hand-built lookup table).
func (h *Handler) serveUsage(w http.ResponseWriter, r *http.Request, q url.Values) {
	name := strings.TrimPrefix(r.URL.Path, usagePathPrefix)
	if !requireUsageShape(w, q, name) {
		return
	}
	month, err := monthFromUnix(q.Get("start_time"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "start_time must be Unix seconds")
		return
	}
	if h.UsageFailMonth != "" && month == h.UsageFailMonth &&
		(h.UsageFailEndpoint == "" || h.UsageFailEndpoint == name) {
		writeError(w, http.StatusInternalServerError, "api_error", "usage endpoint temporarily unavailable")
		return
	}
	buckets, err := h.loadUsage(month, name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api_error", err.Error())
		return
	}
	h.paginate(w, q, buckets)
}

// paginate serves one page of buckets honoring the page cursor and PageSize,
// setting has_more/next_page — the shared pagination for the cost and usage
// endpoints.
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
	var nextPage *string
	if hasMore {
		s := strconv.Itoa(end)
		nextPage = &s
	}
	writeJSON(w, http.StatusOK, response{
		Object:   "page",
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
	writeError(w, http.StatusTooManyRequests, "rate_limit_exceeded", "slow down")
	return true
}

// requireShape verifies the connector's documented request parameters per
// parameter and writes a 400 (returning false) on any mismatch. OpenAI
// documents group_by as a bare, repeated group_by= (NOT bracketed), so a
// bracketed group_by[]= is rejected outright.
func requireShape(w http.ResponseWriter, q url.Values) bool {
	if len(q["group_by[]"]) != 0 {
		writeError(w, http.StatusBadRequest, "invalid_request_error",
			"group_by must be sent bare as group_by=, not bracketed group_by[]=")
		return false
	}
	if !equalStringSet(q["group_by"], []string{"project_id", "line_item"}) {
		writeError(w, http.StatusBadRequest, "invalid_request_error",
			"group_by must be exactly {project_id, line_item}")
		return false
	}
	if got := q.Get("bucket_width"); got != "1d" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "bucket_width must be 1d, got "+got)
		return false
	}
	if got := q.Get("limit"); got != "180" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "limit must be 180, got "+got)
		return false
	}
	return true
}

// requireUsageShape verifies a usage endpoint's documented request parameters
// per parameter and writes a 400 (returning false) on any mismatch. Model
// endpoints require the bare, repeated group_by=model (never bracketed);
// model-less endpoints must send NO group_by at all. bucket_width must be 1d,
// limit must be 31 (NOT the costs endpoint's 180), and start_time must be
// present. name is the path's last segment.
func requireUsageShape(w http.ResponseWriter, q url.Values, name string) bool {
	switch name {
	case "code_interpreter_sessions", "vector_stores", "file_search_calls":
		if len(q["group_by"]) != 0 || len(q["group_by[]"]) != 0 {
			writeError(w, http.StatusBadRequest, "invalid_request_error",
				name+" must send no group_by (it has no model dim)")
			return false
		}
	default:
		if len(q["group_by[]"]) != 0 {
			writeError(w, http.StatusBadRequest, "invalid_request_error",
				"group_by must be sent bare as group_by=, not bracketed group_by[]=")
			return false
		}
		if !equalStringSet(q["group_by"], []string{"model"}) {
			writeError(w, http.StatusBadRequest, "invalid_request_error",
				"group_by must be exactly {model}")
			return false
		}
	}
	if got := q.Get("bucket_width"); got != "1d" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "bucket_width must be 1d, got "+got)
		return false
	}
	if got := q.Get("limit"); got != "31" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "usage limit must be 31, got "+got)
		return false
	}
	if q.Get("start_time") == "" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "start_time is required")
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

// loadMonth reads <dir>/<month>.json as a JSON array of cost buckets; a missing
// file is an empty month.
func (h *Handler) loadMonth(month string) ([]json.RawMessage, error) {
	return loadBuckets(filepath.Join(h.dir, month+".json"), month+".json")
}

// loadUsage reads <dir>/<month>.usage.<name>.json as a JSON array of usage
// buckets; a missing file is an empty endpoint-month.
func (h *Handler) loadUsage(month, name string) ([]json.RawMessage, error) {
	label := month + ".usage." + name + ".json"
	return loadBuckets(filepath.Join(h.dir, label), label)
}

// loadBuckets reads a JSON array of raw bucket objects from path; a missing file
// is an empty month.
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

func monthFromUnix(s string) (string, error) {
	secs, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return "", err
	}
	return time.Unix(secs, 0).UTC().Format("2006-01"), nil
}

// response mirrors the real envelope (object="page", null next_page when
// there are no more pages).
type response struct {
	Object   string            `json:"object"`
	Data     []json.RawMessage `json:"data"`
	HasMore  bool              `json:"has_more"`
	NextPage *string           `json:"next_page"`
}

type apiError struct {
	Error errBody `json:"error"`
}

type errBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, apiError{Error: errBody{Type: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
