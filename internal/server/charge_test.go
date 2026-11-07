package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/store"
)

// claudeSpans wraps OTLP/JSON span attributes for one Claude Code resource.
func claudeSpans(t *testing.T, name string, attributes map[string]string) []byte {
	t.Helper()
	list := make([]map[string]any, 0, len(attributes))
	for key, value := range attributes {
		list = append(list, map[string]any{"key": key, "value": map[string]any{"stringValue": value}})
	}
	body, err := json.Marshal(map[string]any{"resourceSpans": []any{map[string]any{
		"resource": map[string]any{"attributes": []any{
			map[string]any{"key": "service.name", "value": map[string]any{"stringValue": "claude-code"}},
		}},
		"scopeSpans": []any{map[string]any{"spans": []any{map[string]any{
			"traceId":           "aa",
			"spanId":            "bb",
			"name":              name,
			"startTimeUnixNano": "1793523600000000000",
			"endTimeUnixNano":   "1793523602000000000",
			"attributes":        list,
		}}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// writeRows lists the stored cache write rows by class.
func writeRows(records []store.Record) map[focus.TokenClass]store.Record {
	writes := map[focus.TokenClass]store.Record{}
	for _, record := range records {
		if record.Row.SkuMeter.Lifetime() != "" {
			writes[record.Row.SkuMeter] = record
		}
	}
	return writes
}

// wantOneStatedWrite asserts the request's writes landed once, in the
// lifetime the event stated, with no assumed lifetime on the row.
func wantOneStatedWrite(t *testing.T, records []store.Record) {
	t.Helper()
	writes := writeRows(records)
	stated, ok := writes[focus.CacheWrite5m]
	if len(writes) != 1 || !ok {
		classes := make([]focus.TokenClass, 0, len(writes))
		for class := range writes {
			classes = append(classes, class)
		}
		t.Fatalf("write rows %v, want one cache_write_5m row", classes)
	}
	if stated.Row.ConsumedQuantity.String() != "100" || stated.Row.Tags[focus.TagAssumedTTL] != "" {
		t.Fatalf("write row %s tokens, assumed %q", stated.Row.ConsumedQuantity, stated.Row.Tags[focus.TagAssumedTTL])
	}
}

// One request reaches the edge as a claude_code.llm_request span, which states
// its writes unsplit, and as an api_request event, which states the split.
func TestClaudeSpanAndEventCountOnce(t *testing.T) {
	w, ledger := memoryWorld(t)
	span := claudeSpans(t, "claude_code.llm_request", map[string]string{
		"model": "claude-haiku-5-5", "request_id": "req_1",
		"input_tokens": "10", "output_tokens": "2", "cache_creation_tokens": "100",
	})
	event := claudeLogs(t, map[string]string{"service.name": "claude-code"}, map[string]string{
		"event.name": "api_request", "model": "claude-haiku-5-5", "request_id": "req_1",
		"input_tokens": "10", "output_tokens": "2", "cache_creation_tokens": "100", "cache_creation_5m_tokens": "100",
	})
	w.send(http.MethodPost, "/otlp/v1/traces", w.token(), "application/json", span).want(t, http.StatusOK)
	w.send(http.MethodPost, "/otlp/v1/logs", w.token(), "application/json", event).want(t, http.StatusOK)
	records := ledger.stored(t, tenantA)
	wantOneStatedWrite(t, records)
	if len(records) != 3 {
		t.Fatalf("%d rows, want input, output and one write", len(records))
	}
}

// Two records of one request that differ only in write lifetime land the
// stated one, whichever comes first.
func TestSplitStatedWins(t *testing.T) {
	stated := map[string]string{
		"event.name": "api_request", "model": "claude-haiku-5-5", "request_id": "req_1",
		"input_tokens": "10", "output_tokens": "2", "cache_creation_tokens": "100", "cache_creation_5m_tokens": "100",
	}
	assumed := map[string]string{
		"event.name": "api_request", "model": "claude-haiku-5-5", "request_id": "req_1",
		"input_tokens": "10", "output_tokens": "2", "cache_creation_tokens": "100",
	}
	for name, order := range map[string][]map[string]string{
		"stated first":  {stated, assumed},
		"assumed first": {assumed, stated},
	} {
		t.Run(name, func(t *testing.T) {
			w, ledger := memoryWorld(t)
			body := claudeLogs(t, map[string]string{"service.name": "claude-code"}, order...)
			w.send(http.MethodPost, "/otlp/v1/logs", w.token(), "application/json", body).want(t, http.StatusOK)
			wantOneStatedWrite(t, ledger.stored(t, tenantA))
		})
	}
}

// A record that does not decode is answered as a rejected record, and the
// records beside it are stored.
func TestABadRecordIsRejectedAndItsSiblingsStored(t *testing.T) {
	w, ledger := memoryWorld(t)
	var body map[string]any
	if err := json.Unmarshal(claudeLogs(t, map[string]string{"service.name": "claude-code"}, map[string]string{
		"event.name": "api_request", "model": "claude-haiku-5-5", "request_id": "req_1",
		"input_tokens": "10", "output_tokens": "2",
	}), &body); err != nil {
		t.Fatal(err)
	}
	scope := body["resourceLogs"].([]any)[0].(map[string]any)["scopeLogs"].([]any)[0].(map[string]any)
	scope["logRecords"] = append([]any{map[string]any{"timeUnixNano": "x"}}, scope["logRecords"].([]any)...)
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	got := w.send(http.MethodPost, "/otlp/v1/logs", w.token(), "application/json", data).want(t, http.StatusOK)
	var answer struct {
		PartialSuccess struct {
			Rejected int `json:"rejected"`
		} `json:"partialSuccess"`
	}
	if err := json.Unmarshal(got.body, &answer); err != nil {
		t.Fatal(err)
	}
	if answer.PartialSuccess.Rejected != 1 {
		t.Fatalf("answer %s, want one rejected record", got.body)
	}
	if len(ledger.stored(t, tenantA)) != 2 {
		t.Fatalf("%d rows, want input and output", len(ledger.stored(t, tenantA)))
	}
}
