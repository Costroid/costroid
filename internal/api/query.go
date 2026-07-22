// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/nlquery"
	"github.com/Costroid/costroid/internal/nlquery/modelwire"
)

const queryRequestBodyLimit int64 = 65536

// QueryConcurrencyLimit is the fixed maximum number of outbound model calls
// one handler permits at a time.
const QueryConcurrencyLimit = 4

// QueryModelSettings is the operator-controlled model configuration carried
// from serve into the HTTP handler.
type QueryModelSettings struct {
	Endpoint   string
	Model      string
	Credential string
	Timeout    time.Duration
}

type queryHandlerOptions struct {
	settings   QueryModelSettings
	httpClient *http.Client
	logger     *slog.Logger
	now        func() time.Time
}

// queryHandler holds only the per-request settings; the store is reached
// through the Server, so this deliberately does not keep a second reference to
// it that a later change could leave stale.
type queryHandler struct {
	settings   QueryModelSettings
	httpClient *http.Client
	logger     *slog.Logger
	now        func() time.Time
	slots      chan struct{}
}

func newQueryHandler(opts queryHandlerOptions) queryHandler {
	if opts.httpClient == nil {
		opts.httpClient = &http.Client{}
	}
	if opts.logger == nil {
		// Structured JSON to stderr, matching AccessLog: every other line a
		// serve process writes is JSON, and slog.Default() (which nothing in
		// this binary replaces) would make these the only plain-text ones.
		opts.logger = slog.New(slog.NewJSONHandler(os.Stderr, nil))
	}
	if opts.now == nil {
		opts.now = time.Now
	}
	return queryHandler{
		settings: opts.settings, httpClient: opts.httpClient,
		logger: opts.logger, now: opts.now, slots: make(chan struct{}, QueryConcurrencyLimit),
	}
}

func (h queryHandler) configured() bool {
	return h.settings.Endpoint != "" && h.settings.Model != ""
}

// DiscoverPromptValues reads only the metadata lists a translation prompt is
// permitted to contain. It returns metric names separately for plan validation.
func DiscoverPromptValues(ctx context.Context, store CostStore) (nlquery.Values, []string, error) {
	providers, err := store.Providers(ctx, focus.DefaultTenant, time.Time{}, time.Time{})
	if err != nil {
		return nlquery.Values{}, nil, errors.New("querying provider names failed")
	}
	tagKeys, err := store.TagKeys(ctx, focus.DefaultTenant, time.Time{}, time.Time{})
	if err != nil {
		return nlquery.Values{}, nil, errors.New("querying tag keys failed")
	}
	currencies, err := store.BillingCurrencies(ctx, focus.DefaultTenant, time.Time{}, time.Time{}, "")
	if err != nil {
		return nlquery.Values{}, nil, errors.New("querying currency codes failed")
	}
	infos, err := store.BusinessMetricNames(ctx, focus.DefaultTenant)
	if err != nil {
		return nlquery.Values{}, nil, errors.New("querying business metric names failed")
	}
	metrics := make([]string, 0, len(infos))
	for _, info := range infos {
		metrics = append(metrics, info.Name)
	}
	return nlquery.Values{
		Providers: providers, TagKeys: tagKeys, Currencies: currencies, Metrics: metrics,
	}, metrics, nil
}

// PostQuery implements POST /api/v1/query. It translates and validates a plan
// but deliberately does not execute it.
func (s *Server) PostQuery(w http.ResponseWriter, r *http.Request) {
	request, ok := decodeQueryRequest(w, r)
	if !ok {
		return
	}
	if !s.query.configured() {
		http.Error(w, "natural-language queries are not configured", http.StatusServiceUnavailable)
		return
	}

	values, metrics, err := DiscoverPromptValues(r.Context(), s.store)
	if err != nil {
		s.query.logger.Error("natural-language query metadata discovery failed", "error", err)
		http.Error(w, "metadata discovery failed", http.StatusInternalServerError)
		return
	}
	prompt, err := nlquery.BuildPrompt(request.Question, s.query.now().UTC().Format(time.DateOnly), values)
	if err != nil {
		s.query.logger.Error("natural-language query prompt encoding failed")
		http.Error(w, "prompt encoding failed", http.StatusInternalServerError)
		return
	}

	select {
	case s.query.slots <- struct{}{}:
		defer func() { <-s.query.slots }()
	default:
		http.Error(w, "natural-language query concurrency limit reached", http.StatusTooManyRequests)
		return
	}

	timed := *s.query.httpClient
	timed.Timeout = s.query.settings.Timeout
	client := modelwire.New(
		s.query.settings.Endpoint, s.query.settings.Model, s.query.settings.Credential, &timed,
	)
	reply, err := client.Complete(r.Context(), prompt)
	if err != nil {
		s.query.logger.Error("natural-language query model request failed", "error", err)
		http.Error(w, "model request failed", http.StatusInternalServerError)
		return
	}
	plan, err := nlquery.ParseReply(reply)
	if err != nil {
		s.query.logger.Error("natural-language query reply parsing failed")
		http.Error(w, "model reply parsing failed", http.StatusInternalServerError)
		return
	}
	if err := ValidatePlan(plan, metrics); err != nil {
		s.query.logger.Error("natural-language query plan validation failed")
		http.Error(w, "model reply validation failed", http.StatusInternalServerError)
		return
	}

	writeJSON(w, plan)
	s.query.logger.Info("natural-language query translated")
}

func decodeQueryRequest(w http.ResponseWriter, r *http.Request) (QueryRequest, bool) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "content type must be application/json", http.StatusBadRequest)
		return QueryRequest{}, false
	}

	r.Body = http.MaxBytesReader(w, r.Body, queryRequestBodyLimit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request QueryRequest
	if err := decoder.Decode(&request); err != nil {
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			http.Error(w, fmt.Sprintf("request body must be at most %d bytes", queryRequestBodyLimit), http.StatusRequestEntityTooLarge)
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			http.Error(w, "request body contains an unknown field", http.StatusBadRequest)
		default:
			http.Error(w, "request body must be valid JSON", http.StatusBadRequest)
		}
		return QueryRequest{}, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, fmt.Sprintf("request body must be at most %d bytes", queryRequestBodyLimit), http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "request body must contain exactly one JSON object", http.StatusBadRequest)
		}
		return QueryRequest{}, false
	}
	if strings.TrimSpace(request.Question) == "" {
		http.Error(w, "question must not be empty", http.StatusBadRequest)
		return QueryRequest{}, false
	}
	if len(request.Question) > focus.MaxFreeTextBytes {
		http.Error(w, fmt.Sprintf("question must be at most %d bytes", focus.MaxFreeTextBytes), http.StatusBadRequest)
		return QueryRequest{}, false
	}
	return request, true
}
