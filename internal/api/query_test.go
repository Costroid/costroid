// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Costroid/costroid/internal/allocation"
	"github.com/Costroid/costroid/internal/nlquery"
	"github.com/Costroid/costroid/internal/storage"
)

type queryRoundTripFunc func(*http.Request) (*http.Response, error)

func (f queryRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type queryRecordingStore struct {
	mu            sync.Mutex
	metadataReads map[string]int
	costReads     int
	otherReads    int
	providers     []string
	tagKeys       []string
	currencies    []string
	metrics       []storage.BusinessMetricInfo
}

func newQueryRecordingStore() *queryRecordingStore {
	return &queryRecordingStore{
		metadataReads: map[string]int{},
		providers:     []string{"NorthCloud", "SouthCloud"},
		tagKeys:       []string{"cost-center", "team"},
		currencies:    []string{"EUR", "USD"},
		metrics:       []storage.BusinessMetricInfo{{Name: "requests"}, {Name: "seats"}},
	}
}

func (s *queryRecordingStore) recordMetadata(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metadataReads[name]++
}

func (s *queryRecordingStore) recordCost() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.costReads++
}

func (s *queryRecordingStore) Providers(context.Context, string, time.Time, time.Time) ([]string, error) {
	s.recordMetadata("providers")
	return append([]string{}, s.providers...), nil
}

func (s *queryRecordingStore) TagKeys(context.Context, string, time.Time, time.Time) ([]string, error) {
	s.recordMetadata("tagKeys")
	return append([]string{}, s.tagKeys...), nil
}

func (s *queryRecordingStore) BillingCurrencies(context.Context, string, time.Time, time.Time, string) ([]string, error) {
	s.recordMetadata("currencies")
	return append([]string{}, s.currencies...), nil
}

func (s *queryRecordingStore) BusinessMetricNames(context.Context, string) ([]storage.BusinessMetricInfo, error) {
	s.recordMetadata("metrics")
	return append([]storage.BusinessMetricInfo{}, s.metrics...), nil
}

func (s *queryRecordingStore) CostTotals(context.Context, string, time.Time, time.Time) ([]storage.CostTotals, error) {
	s.recordCost()
	return nil, nil
}

func (s *queryRecordingStore) DailyCostsByService(context.Context, string, time.Time, time.Time, string, string, ...storage.CostGroupBy) (storage.DailyCosts, error) {
	s.recordCost()
	return storage.DailyCosts{}, nil
}

func (s *queryRecordingStore) DailyCostsByAllocation(context.Context, string, time.Time, time.Time, allocation.Dimension, string, string) (storage.DailyCosts, error) {
	s.recordCost()
	return storage.DailyCosts{}, nil
}

func (s *queryRecordingStore) DailyCostsByTag(context.Context, string, time.Time, time.Time, string, string, string) (storage.DailyCosts, error) {
	s.recordCost()
	return storage.DailyCosts{}, nil
}

func (s *queryRecordingStore) DailyTokensByService(context.Context, string, time.Time, time.Time) ([]storage.DailyTokenUsage, error) {
	s.recordCost()
	return nil, nil
}

func (s *queryRecordingStore) DailyUsageMetrics(context.Context, string, time.Time, time.Time) ([]storage.DailyUsageMetric, error) {
	s.recordCost()
	return nil, nil
}

func (s *queryRecordingStore) DailyBusinessMetricQuantities(context.Context, string, string, time.Time, time.Time) ([]storage.DayQuantity, error) {
	s.recordCost()
	return nil, nil
}

func (s *queryRecordingStore) SyncStatuses(context.Context) ([]storage.SyncStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.otherReads++
	return nil, nil
}

func (s *queryRecordingStore) snapshot() (map[string]int, int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	metadata := make(map[string]int, len(s.metadataReads))
	for name, count := range s.metadataReads {
		metadata[name] = count
	}
	return metadata, s.costReads, s.otherReads
}

func queryModelEnvelope(reply string) *http.Response {
	body, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": reply}}},
	})
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}
}

func queryValidPlan() string {
	return `{"endpoint":"costs-summary","start":null,"end":null,"groupBy":"service","tagKey":null,"currency":null,"provider":null,"metric":null}`
}

func querySettings() QueryModelSettings {
	return QueryModelSettings{
		Endpoint: "https://model.example.invalid/v1/chat/completions", Model: "local-model",
		Credential: "header-credential", Timeout: 2 * time.Minute,
	}
}

func newQueryTestHandler(store CostStore, settings QueryModelSettings, transport http.RoundTripper, logs *bytes.Buffer, now func() time.Time, extra ...HandlerOption) http.Handler {
	if logs == nil {
		logs = &bytes.Buffer{}
	}
	if now == nil {
		now = func() time.Time { return time.Date(2026, 7, 19, 9, 0, 0, 0, time.UTC) }
	}
	opts := []HandlerOption{
		WithQueryModel(settings),
		func(o *handlerOptions) {
			o.query.httpClient = &http.Client{Transport: transport}
			o.query.logger = slog.New(slog.NewJSONHandler(logs, nil))
			o.query.now = now
		},
	}
	opts = append(opts, extra...)
	return NewHandler("test", fstest.MapFS{}, store, "", opts...)
}

func queryRequest(handler http.Handler, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/query", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestQueryReturnsValidatedPlanFromMetadataOnly(t *testing.T) {
	store := newQueryRecordingStore()
	question := "summarize the recent invoice window"
	var outboundBody []byte
	var outboundAuth string
	transport := queryRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		var err error
		outboundBody, err = io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		outboundAuth = request.Header.Get("Authorization")
		return queryModelEnvelope(queryValidPlan()), nil
	})
	now := func() time.Time { return time.Date(2026, 7, 17, 23, 59, 0, 0, time.FixedZone("offset", -7*60*60)) }
	handler := newQueryTestHandler(store, querySettings(), transport, nil, now)
	recorder := queryRequest(handler, `{"question":"`+question+`"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", recorder.Code, recorder.Body.String())
	}
	var plan nlquery.Plan
	if err := json.Unmarshal(recorder.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Endpoint != "costs-summary" || plan.GroupBy == nil || *plan.GroupBy != "service" {
		t.Fatalf("plan = %+v", plan)
	}

	if outboundAuth != "Bearer header-credential" {
		t.Fatalf("authorization = %q", outboundAuth)
	}
	queryAssertJSONKeys(t, "model request", outboundBody, []string{"model", "messages", "response_format"})
	var envelope struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(outboundBody, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Model != "local-model" || len(envelope.Messages) != 1 || envelope.Messages[0].Role != "user" {
		t.Fatalf("model envelope = %+v", envelope)
	}
	queryAssertJSONKeys(t, "message", mustJSON(t, envelope.Messages[0]), []string{"role", "content"})
	queryAssertJSONKeys(t, "prompt", []byte(envelope.Messages[0].Content), []string{"instruction", "question", "today", "schema", "values"})
	var prompt struct {
		Question string         `json:"question"`
		Today    string         `json:"today"`
		Values   nlquery.Values `json:"values"`
	}
	if err := json.Unmarshal([]byte(envelope.Messages[0].Content), &prompt); err != nil {
		t.Fatal(err)
	}
	wantValues := nlquery.Values{
		Providers: []string{"NorthCloud", "SouthCloud"}, TagKeys: []string{"cost-center", "team"},
		Currencies: []string{"EUR", "USD"}, Metrics: []string{"requests", "seats"},
	}
	if prompt.Question != question || prompt.Today != "2026-07-18" || !reflect.DeepEqual(prompt.Values, wantValues) {
		t.Fatalf("prompt question = %q, today = %q, values = %+v", prompt.Question, prompt.Today, prompt.Values)
	}
	queryAssertJSONKeys(t, "values", mustJSON(t, prompt.Values), []string{"providers", "tagKeys", "currencies", "metrics"})

	metadata, costReads, otherReads := store.snapshot()
	if want := map[string]int{"providers": 1, "tagKeys": 1, "currencies": 1, "metrics": 1}; !reflect.DeepEqual(metadata, want) {
		t.Fatalf("metadata reads = %v, want %v", metadata, want)
	}
	if costReads != 0 || otherReads != 0 {
		t.Fatalf("cost reads = %d, other reads = %d, want zero", costReads, otherReads)
	}
}

func TestQueryRejectsInvalidPlan(t *testing.T) {
	for _, reply := range []string{
		`{"endpoint":"unknown-resource","start":null,"end":null,"groupBy":null,"tagKey":null,"currency":null,"provider":null,"metric":null}`,
		`{"endpoint":"usage","start":null,"end":null,"groupBy":"service","tagKey":null,"currency":null,"provider":null,"metric":null}`,
	} {
		transport := queryRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return queryModelEnvelope(reply), nil
		})
		recorder := queryRequest(newQueryTestHandler(newQueryRecordingStore(), querySettings(), transport, nil, nil), `{"question":"show the trend"}`)
		if recorder.Code != http.StatusInternalServerError || recorder.Body.String() != "model reply validation failed\n" {
			t.Fatalf("status = %d, body = %q", recorder.Code, recorder.Body.String())
		}
	}
}

func TestQueryRequestGuards(t *testing.T) {
	transport := queryRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("request guard allowed an outbound call")
		return nil, nil
	})
	handler := newQueryTestHandler(newQueryRecordingStore(), querySettings(), transport, nil, nil)
	tests := []struct {
		name        string
		body        string
		contentType string
		wantStatus  int
		wantBody    string
	}{
		{name: "content type", body: `{"question":"hello"}`, contentType: "text/plain", wantStatus: 400, wantBody: "content type must be application/json\n"},
		{name: "body cap", body: `{"question":"` + strings.Repeat("x", 65536) + `"}`, contentType: "application/json", wantStatus: 413, wantBody: "request body must be at most 65536 bytes\n"},
		{name: "malformed", body: `{`, contentType: "application/json", wantStatus: 400, wantBody: "request body must be valid JSON\n"},
		{name: "trailing token", body: `{"question":"hello"}{}`, contentType: "application/json", wantStatus: 400, wantBody: "request body must contain exactly one JSON object\n"},
		{name: "empty question", body: `{"question":"  "}`, contentType: "application/json", wantStatus: 400, wantBody: "question must not be empty\n"},
		{name: "question byte limit", body: `{"question":"` + strings.Repeat("é", 4097) + `"}`, contentType: "application/json", wantStatus: 400, wantBody: "question must be at most 8192 bytes\n"},
		{name: "endpoint field", body: `{"question":"hello","endpoint":"https://elsewhere.invalid"}`, contentType: "application/json", wantStatus: 400, wantBody: "request body contains an unknown field\n"},
		{name: "model field", body: `{"question":"hello","model":"different"}`, contentType: "application/json", wantStatus: 400, wantBody: "request body contains an unknown field\n"},
		{name: "credential field", body: `{"question":"hello","credential":"different"}`, contentType: "application/json", wantStatus: 400, wantBody: "request body contains an unknown field\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/v1/query", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", tc.contentType)
			handler.ServeHTTP(recorder, request)
			if recorder.Code != tc.wantStatus || recorder.Body.String() != tc.wantBody {
				t.Fatalf("status = %d, body = %q, want %d and %q", recorder.Code, recorder.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestQueryReadOnlyPosture(t *testing.T) {
	transport := queryRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("empty question reached the transport")
		return nil, nil
	})
	base := newQueryTestHandler(newQueryRecordingStore(), querySettings(), transport, nil, nil)
	without := queryRequest(base, `{"question":""}`)
	if without.Code != http.StatusBadRequest || without.Body.String() != "question must not be empty\n" {
		t.Fatalf("without read-only: status = %d, body = %q", without.Code, without.Body.String())
	}
	readOnlyHandler := newQueryTestHandler(newQueryRecordingStore(), querySettings(), transport, nil, nil, WithReadOnly())
	with := queryRequest(readOnlyHandler, `{"question":""}`)
	if with.Code != http.StatusMethodNotAllowed || with.Header().Get("Allow") != "GET, HEAD" || with.Body.String() != "method not allowed\n" {
		t.Fatalf("with read-only: status = %d, Allow = %q, body = %q", with.Code, with.Header().Get("Allow"), with.Body.String())
	}
}

func TestQueryConcurrencyLimit(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	transport := queryRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		<-release
		return queryModelEnvelope(queryValidPlan()), nil
	})
	handler := newQueryTestHandler(newQueryRecordingStore(), querySettings(), transport, nil, nil)
	results := make(chan int, QueryConcurrencyLimit+1)
	for range QueryConcurrencyLimit + 1 {
		go func() {
			results <- queryRequest(handler, `{"question":"show the trend"}`).Code
		}()
	}

	select {
	case first := <-results:
		if first != http.StatusTooManyRequests {
			t.Fatalf("first completed status = %d, want 429", first)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no rejection completed while outbound calls were blocked")
	}
	close(release)
	for range QueryConcurrencyLimit {
		select {
		case status := <-results:
			if status != http.StatusOK {
				t.Errorf("released request status = %d, want 200", status)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("released outbound call did not complete")
		}
	}
	if got := calls.Load(); got != int32(QueryConcurrencyLimit) {
		t.Fatalf("outbound calls = %d, want %d", got, QueryConcurrencyLimit)
	}
}

func TestQueryClientCancellation(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan error, 1)
	transport := queryRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		canceled <- request.Context().Err()
		return nil, request.Context().Err()
	})
	handler := newQueryTestHandler(newQueryRecordingStore(), querySettings(), transport, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/query", strings.NewReader(`{"question":"show the trend"}`)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(recorder, request)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("outbound call did not start")
	}
	cancel()
	select {
	case err := <-canceled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("transport context error = %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("transport did not observe client cancellation")
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("handler did not return after cancellation")
	}
}

func TestQueryLogsExcludeSensitiveValues(t *testing.T) {
	settings := querySettings()
	settings.Credential = "credential-sensitive-marker"
	validReply := `{"endpoint":"costs-summary","start":null,"end":null,"groupBy":"service","tagKey":null,"currency":null,"provider":"plan-sensitive-marker","metric":null}`
	replies := []string{validReply, "reply-sensitive-marker"}
	var mu sync.Mutex
	transport := queryRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer credential-sensitive-marker" {
			t.Fatal("configured credential was not sent")
		}
		mu.Lock()
		defer mu.Unlock()
		reply := replies[0]
		replies = replies[1:]
		return queryModelEnvelope(reply), nil
	})
	var logs bytes.Buffer
	handler := newQueryTestHandler(newQueryRecordingStore(), settings, transport, &logs, nil)
	question := "question-sensitive-marker"
	success := queryRequest(handler, `{"question":"`+question+`"}`)
	if success.Code != http.StatusOK || !strings.Contains(success.Body.String(), "plan-sensitive-marker") {
		t.Fatalf("success status = %d, body = %q", success.Code, success.Body.String())
	}
	failure := queryRequest(handler, `{"question":"`+question+`"}`)
	if failure.Code != http.StatusInternalServerError || failure.Body.String() != "model reply parsing failed\n" {
		t.Fatalf("failure status = %d, body = %q", failure.Code, failure.Body.String())
	}
	if logs.Len() == 0 {
		t.Fatal("log capture is empty")
	}
	values := queryDecodedLogValues(t, logs.String())
	for _, sensitive := range []string{question, "reply-sensitive-marker", "plan-sensitive-marker", "credential-sensitive-marker", validReply} {
		for _, value := range values {
			if strings.Contains(value, sensitive) {
				t.Fatalf("logs contain sensitive value %q in %q", sensitive, value)
			}
		}
		if strings.Contains(failure.Body.String(), sensitive) {
			t.Fatalf("error body contains sensitive value %q", sensitive)
		}
	}
}

func TestQueryTransportFailureRedactsEndpoint(t *testing.T) {
	settings := querySettings()
	settings.Endpoint = "http://127.0.0.1:43129/v1/chat/completions"
	transport := queryRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp 127.0.0.1:43129: connection refused")
	})
	var logs bytes.Buffer
	recorder := queryRequest(newQueryTestHandler(newQueryRecordingStore(), settings, transport, &logs, nil), `{"question":"show the trend"}`)
	if recorder.Code != http.StatusInternalServerError || recorder.Body.String() != "model request failed\n" {
		t.Fatalf("status = %d, body = %q", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "127.0.0.1:43129") {
		t.Fatalf("response leaked endpoint: %q", recorder.Body.String())
	}
	if !strings.Contains(logs.String(), "127.0.0.1:43129") {
		t.Fatalf("log did not retain transport detail: %q", logs.String())
	}
}

func TestQueryConfigurationControlsOutboundCalls(t *testing.T) {
	var calls atomic.Int32
	transport := queryRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return queryModelEnvelope(queryValidPlan()), nil
	})
	store := newQueryRecordingStore()
	build := func(endpoint, model string) http.Handler {
		settings := querySettings()
		settings.Endpoint = endpoint
		settings.Model = model
		return newQueryTestHandler(store, settings, transport, nil, nil)
	}
	unconfigured := queryRequest(build("", ""), `{"question":"show the trend"}`)
	if unconfigured.Code != http.StatusServiceUnavailable || calls.Load() != 0 {
		t.Fatalf("unconfigured status = %d, calls = %d", unconfigured.Code, calls.Load())
	}
	configured := queryRequest(build(querySettings().Endpoint, querySettings().Model), `{"question":"show the trend"}`)
	if configured.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("configured status = %d, calls = %d", configured.Code, calls.Load())
	}
}

func queryAssertJSONKeys(t *testing.T, name string, encoded []byte, want []string) {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatalf("%s is not a JSON object: %v", name, err)
	}
	got := make([]string, 0, len(object))
	for key := range object {
		got = append(got, key)
	}
	sort.Strings(got)
	want = append([]string{}, want...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s keys = %v, want %v", name, got, want)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func queryDecodedLogValues(t *testing.T, captured string) []string {
	t.Helper()
	var values []string
	var walk func(any)
	walk = func(node any) {
		switch typed := node.(type) {
		case string:
			values = append(values, typed)
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			for key, item := range typed {
				values = append(values, key)
				walk(item)
			}
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(captured), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", line, err)
		}
		walk(record)
	}
	return values
}
