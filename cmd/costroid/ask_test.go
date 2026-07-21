// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/allocation"
	"github.com/Costroid/costroid/internal/api"
	"github.com/Costroid/costroid/internal/nlquery"
	"github.com/Costroid/costroid/internal/storage"
)

type askRoundTripFunc func(*http.Request) (*http.Response, error)

func (f askRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type askFakeStore struct {
	daily  storage.DailyCosts
	closed int
}

func newAskFakeStore() *askFakeStore {
	day := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	return &askFakeStore{daily: storage.DailyCosts{Currency: "USD", Days: []storage.DayCosts{{Date: day, Services: []storage.ServiceCost{{ServiceName: "compute", Cost: decimal.RequireFromString("98765.432100000000000001")}}}}}}
}

func (s *askFakeStore) Close() error { s.closed++; return nil }
func (s *askFakeStore) Providers(context.Context, string, time.Time, time.Time) ([]string, error) {
	return []string{"AcmeCloud"}, nil
}
func (s *askFakeStore) TagKeys(context.Context, string, time.Time, time.Time) ([]string, error) {
	return []string{"cost-center"}, nil
}
func (s *askFakeStore) BillingCurrencies(context.Context, string, time.Time, time.Time, string) ([]string, error) {
	return []string{"USD"}, nil
}
func (s *askFakeStore) CostTotals(context.Context, string, time.Time, time.Time) ([]storage.CostTotals, error) {
	return nil, nil
}
func (s *askFakeStore) DailyCostsByService(context.Context, string, time.Time, time.Time, string, string, ...storage.CostGroupBy) (storage.DailyCosts, error) {
	return s.daily, nil
}
func (s *askFakeStore) DailyCostsByAllocation(context.Context, string, time.Time, time.Time, allocation.Dimension, string, string) (storage.DailyCosts, error) {
	return s.daily, nil
}
func (s *askFakeStore) DailyCostsByTag(context.Context, string, time.Time, time.Time, string, string, string) (storage.DailyCosts, error) {
	return s.daily, nil
}
func (s *askFakeStore) DailyTokensByService(context.Context, string, time.Time, time.Time) ([]storage.DailyTokenUsage, error) {
	return nil, nil
}
func (s *askFakeStore) DailyUsageMetrics(context.Context, string, time.Time, time.Time) ([]storage.DailyUsageMetric, error) {
	return nil, nil
}
func (s *askFakeStore) BusinessMetricNames(context.Context, string) ([]storage.BusinessMetricInfo, error) {
	return []storage.BusinessMetricInfo{{Name: "requests", FirstDay: time.Date(1999, 1, 2, 0, 0, 0, 0, time.UTC), LastDay: time.Date(2030, 12, 30, 0, 0, 0, 0, time.UTC)}}, nil
}
func (s *askFakeStore) DailyBusinessMetricQuantities(context.Context, string, string, time.Time, time.Time) ([]storage.DayQuantity, error) {
	return nil, nil
}
func (s *askFakeStore) SyncStatuses(context.Context) ([]storage.SyncStatus, error) { return nil, nil }

func modelEnvelope(reply string) *http.Response {
	quoted := strings.NewReplacer("\\", "\\\\", `"`, `\"`, "\n", `\n`).Replace(reply)
	body := `{"choices":[{"message":{"role":"assistant","content":"` + quoted + `"}}]}`
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
}

func validSummaryReply() string {
	return `{"endpoint":"costs-summary","start":null,"end":null,"groupBy":"service","tagKey":null,"currency":null,"provider":null,"metric":null}`
}

func configuredAsk(t *testing.T) {
	t.Helper()
	t.Setenv(envModelEndpoint, "https://model.invalid/v1/chat/completions")
	t.Setenv(envModelName, "local-model")
	t.Setenv(envModelCredentialFile, "")
}

func askDeps(store *askFakeStore, out, logs *bytes.Buffer, transport http.RoundTripper) askDependencies {
	return askDependencies{
		out:        out,
		logger:     slog.New(slog.NewJSONHandler(logs, nil)),
		httpClient: &http.Client{Transport: transport},
		openStore:  func(context.Context) (askStore, error) { return store, nil },
	}
}

func TestAskUnconfiguredIsInertAndConfiguredCallsOnce(t *testing.T) {
	t.Setenv(envModelEndpoint, "")
	t.Setenv(envModelName, "")
	t.Setenv(envModelCredentialFile, "")
	calls := 0
	transport := askRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return modelEnvelope(validSummaryReply()), nil
	})
	store := newAskFakeStore()
	var out, logs bytes.Buffer
	deps := askDeps(store, &out, &logs, transport)
	err := askCommand(context.Background(), []string{"anything"}, deps)
	if err == nil || !strings.Contains(err.Error(), envModelEndpoint) || calls != 0 {
		t.Fatalf("unconfigured: err = %v, calls = %d", err, calls)
	}
	if store.closed != 0 {
		t.Fatalf("unconfigured command opened store; closes = %d", store.closed)
	}

	configuredAsk(t)
	if err := askCommand(context.Background(), []string{"anything"}, deps); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("configured calls = %d, want 1", calls)
	}
}

func TestAskRejectsQuestionOverByteLimit(t *testing.T) {
	question := strings.Repeat("é", 4097)
	err := askCommand(context.Background(), []string{question}, askDependencies{})
	if err == nil || err.Error() != "question must be at most 8192 bytes" {
		t.Fatalf("over-length question error = %v", err)
	}
}

func TestExecutePlanMatchesIndependentRequestAndOmitsNulls(t *testing.T) {
	store := newAskFakeStore()
	handler := api.NewHandler("test", fstest.MapFS{}, store, "")
	start := "2026-07-01"
	group := "service"
	plan := nlquery.Plan{Endpoint: "costs-summary", Start: &start, GroupBy: &group}
	got, err := executePlan(context.Background(), handler, plan)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	independent := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/costs/summary?groupBy=service&start=2026-07-01", nil)
	handler.ServeHTTP(recorder, independent)
	if !bytes.Equal(got, recorder.Body.Bytes()) {
		t.Fatalf("execute result = %s, independent export-equivalent result = %s", got, recorder.Body.Bytes())
	}

	var rawQuery string
	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	})
	if _, err := executePlan(context.Background(), probe, plan); err != nil {
		t.Fatal(err)
	}
	if rawQuery != "groupBy=service&start=2026-07-01" || strings.Contains(rawQuery, "currency") || strings.Contains(rawQuery, "provider") {
		t.Fatalf("raw query = %q", rawQuery)
	}
}

func TestAskAnswerMatchesJSONExport(t *testing.T) {
	seedExportStore(t)
	configuredAsk(t)
	transport := askRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return modelEnvelope(validSummaryReply()), nil
	})
	var out, logs bytes.Buffer
	deps := askDependencies{
		out:        &out,
		logger:     slog.New(slog.NewJSONHandler(&logs, nil)),
		httpClient: &http.Client{Transport: transport},
		openStore: func(ctx context.Context) (askStore, error) {
			return openStore(ctx, "")
		},
	}
	if err := askCommand(context.Background(), []string{"total by service"}, deps); err != nil {
		t.Fatal(err)
	}
	_, answer, ok := strings.Cut(out.String(), "Result:\n")
	if !ok {
		t.Fatalf("ask output lacks result marker: %q", out.String())
	}
	exported, err := runCLI([]string{"export", "costs-summary", "--format", "json", "--group-by", "service"}, "")
	if err != nil {
		t.Fatalf("export: %v\n%s", err, exported)
	}
	if answer != exported {
		t.Fatalf("ask answer differs from JSON export\nask:    %s\nexport: %s", answer, exported)
	}
}

func TestAskWritesPlanBeforeResult(t *testing.T) {
	configuredAsk(t)
	store := newAskFakeStore()
	transport := askRoundTripFunc(func(*http.Request) (*http.Response, error) { return modelEnvelope(validSummaryReply()), nil })
	var out, logs bytes.Buffer
	if err := askCommand(context.Background(), []string{"total by service"}, askDeps(store, &out, &logs, transport)); err != nil {
		t.Fatal(err)
	}
	planAt := strings.Index(out.String(), "Resolved plan:")
	resultAt := strings.Index(out.String(), "Result:")
	amountAt := strings.Index(out.String(), "98765.432100000000000001")
	if planAt < 0 || resultAt <= planAt || amountAt <= resultAt {
		t.Fatalf("output order is wrong: %q", out.String())
	}
}

func TestAskOutboundBodyMatchesGolden(t *testing.T) {
	configuredAsk(t)
	store := newAskFakeStore()
	want, err := os.ReadFile("testdata/nlquery-model-request.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	want = bytes.TrimSuffix(want, []byte("\n"))
	transport := askRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(body, want) {
			t.Fatalf("outbound body mismatch\ngot:  %s\nwant: %s", body, want)
		}
		return modelEnvelope(validSummaryReply()), nil
	})
	var out, logs bytes.Buffer
	if err := askCommand(context.Background(), []string{"show spend"}, askDeps(store, &out, &logs, transport)); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"98765.432100000000000001", "1999-01-02", "2030-12-30"} {
		if bytes.Contains(want, []byte(forbidden)) {
			t.Fatalf("golden contains forbidden store value %q", forbidden)
		}
	}
	assertOutboundShapeIsClosed(t, want)
}

// assertOutboundShapeIsClosed pins the outbound prompt to an exact set of
// fields. Scanning for known store values only catches a leak whose value the
// test already knows, so regenerating the golden around a NEW field would pass
// silently. Asserting the key set instead fails on any added field, whatever
// it contains.
func assertOutboundShapeIsClosed(t *testing.T, body []byte) {
	t.Helper()
	var envelope struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("outbound body is not JSON: %v", err)
	}
	assertKeys(t, "envelope", body, []string{"model", "messages"})
	if len(envelope.Messages) != 1 {
		t.Fatalf("messages = %d, want exactly 1", len(envelope.Messages))
	}
	assertKeys(t, "prompt", []byte(envelope.Messages[0].Content),
		[]string{"instruction", "question", "schema", "values"})

	var prompt struct {
		Schema json.RawMessage `json:"schema"`
		Values json.RawMessage `json:"values"`
	}
	if err := json.Unmarshal([]byte(envelope.Messages[0].Content), &prompt); err != nil {
		t.Fatalf("prompt is not JSON: %v", err)
	}
	assertKeys(t, "schema", prompt.Schema,
		[]string{"objectOnly", "endpoints", "groupBy", "dateFormat", "nullable"})
	// The four permitted value lists, and nothing else: no amounts, no
	// quantities, no dates, no rows.
	assertKeys(t, "values", prompt.Values,
		[]string{"providers", "tagKeys", "currencies", "metrics"})
}

func assertKeys(t *testing.T, what string, encoded []byte, want []string) {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatalf("%s is not a JSON object: %v", what, err)
	}
	got := make([]string, 0, len(object))
	for key := range object {
		got = append(got, key)
	}
	sort.Strings(got)
	expected := append([]string(nil), want...)
	sort.Strings(expected)
	if !slices.Equal(got, expected) {
		t.Fatalf("%s keys = %v, want exactly %v", what, got, expected)
	}
}

func TestAskLogsExcludeQuestionReplyPlanAndCredential(t *testing.T) {
	configuredAsk(t)
	credentialPath := t.TempDir() + "/model.key"
	if err := os.WriteFile(credentialPath, []byte("credential-sentinel\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envModelCredentialFile, credentialPath)
	store := newAskFakeStore()
	replies := []string{validSummaryReply(), "reply-sentinel"}
	transport := askRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer credential-sentinel" {
			t.Fatal("credential was not sent in the authorization header")
		}
		reply := replies[0]
		replies = replies[1:]
		return modelEnvelope(reply), nil
	})
	var out, logs bytes.Buffer
	deps := askDeps(store, &out, &logs, transport)
	if err := askCommand(context.Background(), []string{"question-sentinel"}, deps); err != nil {
		t.Fatal(err)
	}
	if err := askCommand(context.Background(), []string{"question-sentinel"}, deps); err == nil {
		t.Fatal("invalid reply unexpectedly succeeded")
	}
	if logs.Len() == 0 {
		t.Fatal("log capture is empty")
	}
	// Assert against DECODED records, not the raw buffer. The JSON handler
	// escapes quotes inside attribute values, so a whole plan or a reply
	// containing a quote is present in the log yet invisible to a substring
	// scan of the encoded bytes.
	values := decodedLogValues(t, logs.String())
	if len(values) == 0 {
		t.Fatal("no log values decoded")
	}
	for _, sensitive := range []string{"question-sentinel", "reply-sentinel", "credential-sentinel", validSummaryReply()} {
		for _, value := range values {
			if strings.Contains(value, sensitive) {
				t.Fatalf("logs contain sensitive value %q in %q", sensitive, value)
			}
		}
	}
}

// decodedLogValues returns every string that appears anywhere in the captured
// JSON log records, with the handler's escaping undone, so assertions see what
// was actually logged rather than its encoded form.
func decodedLogValues(t *testing.T, captured string) []string {
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

func TestResolveModelSettingsRejectsMalformedAndAcceptsUnreachable(t *testing.T) {
	t.Setenv(envModelName, "local-model")
	t.Setenv(envModelCredentialFile, "")
	t.Setenv(envModelEndpoint, "://malformed")
	if _, err := resolveModelSettings(); err == nil || !strings.Contains(err.Error(), envModelEndpoint) {
		t.Fatalf("malformed endpoint error = %v", err)
	}
	if _, _, _, err := serveConfig([]string{"--no-auth"}); err == nil || !strings.Contains(err.Error(), envModelEndpoint) {
		t.Fatalf("serve malformed endpoint error = %v", err)
	}

	t.Setenv(envModelEndpoint, "http://127.0.0.1:1/v1/chat/completions")
	settings, err := resolveModelSettings()
	if err != nil || !settings.configured() {
		t.Fatalf("valid unreachable endpoint: settings = %+v, err = %v", settings, err)
	}
	if _, _, stop, err := serveConfig([]string{"--no-auth"}); stop || err != nil {
		t.Fatalf("serve valid unreachable endpoint: stop = %v, err = %v", stop, err)
	}
}
