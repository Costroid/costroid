package otlp

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/focus"
)

// validClaudeRecord is one api_request event with a stated split, as Claude
// Code writes it.
const validClaudeRecord = `{"timeUnixNano":"1793534400000000000","body":{"stringValue":"claude_code.api_request"},` +
	`"attributes":[` +
	`{"key":"event.name","value":{"stringValue":"api_request"}},` +
	`{"key":"model","value":{"stringValue":"claude-haiku-5-5"}},` +
	`{"key":"request_id","value":{"stringValue":"req_keep"}},` +
	`{"key":"input_tokens","value":{"stringValue":"10"}},` +
	`{"key":"output_tokens","value":{"stringValue":"2"}},` +
	`{"key":"cache_creation_tokens","value":{"stringValue":"100"}},` +
	`{"key":"cache_creation_5m_tokens","value":{"stringValue":"100"}}]}`

// claudeLogs wraps raw log records in one claude-code resource.
func claudeLogs(records ...string) []byte {
	return []byte(`{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"claude-code"}}]},` +
		`"scopeLogs":[{"logRecords":[` + strings.Join(records, ",") + `]}]}]}`)
}

// claudeSpans wraps raw spans in one claude-code resource.
func claudeSpans(spans ...string) []byte {
	return []byte(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"claude-code"}}]},` +
		`"scopeSpans":[{"spans":[` + strings.Join(spans, ",") + `]}]}]}`)
}

// claudeRecord is a log record with string attributes.
func claudeRecord(t *testing.T, attributes map[string]string) string {
	t.Helper()
	list := make([]map[string]any, 0, len(attributes))
	for key, value := range attributes {
		list = append(list, map[string]any{"key": key, "value": map[string]any{"stringValue": value}})
	}
	data, err := json.Marshal(map[string]any{
		"timeUnixNano": "1793534400000000000",
		"body":         map[string]any{"stringValue": eventClaudeRequest},
		"attributes":   list,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

var keptClasses = map[focus.TokenClass]int64{focus.InputUncached: 10, focus.Output: 2, focus.CacheWrite5m: 100}

// survives reports whether the valid record's usage is in the batch, whole.
func survives(batch Batch) bool {
	return slices.ContainsFunc(batch.Usage, func(usage Usage) bool {
		return usage.ID == "req_keep" && usage.AssumedWrite == "" && maps.Equal(classes(usage.Counts), keptClasses)
	})
}

func TestOneBadRecordKeepsTheBatch(t *testing.T) {
	cases := map[string]struct {
		body   []byte
		signal Signal
	}{
		"a time that is not a number":    {claudeLogs(`{"timeUnixNano":"x"}`, validClaudeRecord), Logs},
		"a time in exponent form":        {claudeLogs(validClaudeRecord, `{"timeUnixNano":1.73e18}`), Logs},
		"an observed time that is false": {claudeLogs(`{"observedTimeUnixNano":false}`, validClaudeRecord), Logs},
		"a body that is an array":        {claudeLogs(`{"body":[1,2]}`, validClaudeRecord), Logs},
		"a record that is a number":      {claudeLogs(`7`, validClaudeRecord), Logs},
		"attributes that are a string":   {claudeLogs(`{"attributes":"input_tokens"}`, validClaudeRecord), Logs},
		"a span start that is negative": {
			[]byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"startTimeUnixNano":"-1"},` +
				`{"spanId":"bb","attributes":[{"key":"gen_ai.response.id","value":{"stringValue":"req_keep"}},` +
				`{"key":"gen_ai.response.model","value":{"stringValue":"gpt-5.5"}},` +
				`{"key":"gen_ai.usage.input_tokens","value":{"intValue":"10"}},` +
				`{"key":"gen_ai.usage.output_tokens","value":{"intValue":"2"}}]}]}]}]}`), Traces,
		},
		"a span end that is an object": {
			[]byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[` +
				`{"spanId":"bb","attributes":[{"key":"gen_ai.response.id","value":{"stringValue":"req_keep"}},` +
				`{"key":"gen_ai.response.model","value":{"stringValue":"gpt-5.5"}},` +
				`{"key":"gen_ai.usage.input_tokens","value":{"intValue":"10"}},` +
				`{"key":"gen_ai.usage.output_tokens","value":{"intValue":"2"}}]},{"endTimeUnixNano":{}}]}]}]}`), Traces,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			batch, err := Parse(tc.body, JSON, tc.signal)
			if err != nil {
				t.Fatalf("the batch was refused: %v", err)
			}
			if len(batch.Usage) != 1 || batch.Usage[0].ID != "req_keep" {
				t.Fatalf("the valid record was lost: %+v", batch)
			}
			if batch.Rejected != 1 {
				t.Fatalf("rejected %d, want the one bad record", batch.Rejected)
			}
		})
	}
	if _, err := Parse([]byte(`{"resourceLogs":[{"scopeLogs":[{"logRecords":[`+validClaudeRecord+`,]}]}]}`), JSON, Logs); err == nil {
		t.Fatal("a body that is not JSON was accepted")
	}
}

func TestABareStringBodyReadsAsTheString(t *testing.T) {
	named := strings.Replace(validClaudeRecord, `{"stringValue":"claude_code.api_request"}`, `"claude_code.api_request"`, 1)
	named = strings.Replace(named, `{"key":"event.name","value":{"stringValue":"api_request"}},`, "", 1)
	batch, err := Parse(claudeLogs(named), JSON, Logs)
	if err != nil {
		t.Fatal(err)
	}
	usage := one(t, batch)
	assertClasses(t, usage.Counts, keptClasses)
	if batch.Drops[DropLogBody] != 0 {
		t.Fatalf("an event name was counted as a body: %+v", batch.Drops)
	}

	text := strings.Replace(validClaudeRecord, `{"stringValue":"claude_code.api_request"}`, `"`+sentinel+`"`, 1)
	batch, err = Parse(claudeLogs(text), JSON, Logs)
	if err != nil {
		t.Fatal(err)
	}
	one(t, batch)
	if batch.Drops[DropLogBody] != 1 {
		t.Fatalf("free text was not dropped: %+v", batch.Drops)
	}
	assertNoContent(t, batch)
}

func TestAClaudeCodeSpanIsLeftOutSoARequestCountsOnce(t *testing.T) {
	span := `{"traceId":"aa","spanId":"bb","name":"claude_code.llm_request","attributes":[` +
		`{"key":"model","value":{"stringValue":"claude-haiku-5-5"}},` +
		`{"key":"request_id","value":{"stringValue":"req_keep"}},` +
		`{"key":"input_tokens","value":{"intValue":"10"}},` +
		`{"key":"output_tokens","value":{"intValue":"2"}},` +
		`{"key":"cache_creation_tokens","value":{"intValue":"100"}}]}`
	batch, err := Parse(claudeSpans(span), JSON, Traces)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Usage) != 0 || batch.Rejected != 0 {
		t.Fatalf("the span counted: %+v", batch)
	}
	batch, err = Parse(claudeLogs(validClaudeRecord), JSON, Logs)
	if err != nil {
		t.Fatal(err)
	}
	assertClasses(t, one(t, batch).Counts, keptClasses)
}

func TestAClaudeCodeLogThatIsNotTheRequestEventCountsNothing(t *testing.T) {
	record := `{"timeUnixNano":"1793534400000000000","body":{"stringValue":"claude_code.tool_result"},"attributes":[` +
		`{"key":"model","value":{"stringValue":"claude-haiku-5-5"}},` +
		`{"key":"input_tokens","value":{"stringValue":"10"}},` +
		`{"key":"output_tokens","value":{"stringValue":"2"}}]}`
	batch, err := Parse(claudeLogs(record), JSON, Logs)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Usage) != 0 || batch.Rejected != 0 {
		t.Fatalf("%+v", batch)
	}
}

func TestAStatedLifetimeWinsOverAnAssumedOne(t *testing.T) {
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
			batch, err := Parse(claudeLogs(claudeRecord(t, order[0]), claudeRecord(t, order[1])), JSON, Logs)
			if err != nil {
				t.Fatal(err)
			}
			usage := one(t, batch)
			if usage.AssumedWrite != "" {
				t.Fatalf("the assumed lifetime was kept: %+v", usage)
			}
			assertClasses(t, usage.Counts, keptClasses)
		})
	}
}

func TestTwoRecordsThatDifferInMoreThanLifetimeAreBothKept(t *testing.T) {
	stated := map[string]string{
		"event.name": "api_request", "model": "claude-haiku-5-5", "request_id": "req_1",
		"input_tokens": "10", "output_tokens": "2", "cache_creation_tokens": "100", "cache_creation_5m_tokens": "100",
	}
	for name, change := range map[string][2]string{
		"another output":      {"output_tokens", "3"},
		"another write total": {"cache_creation_tokens", "200"},
		"another request":     {"request_id", "req_2"},
	} {
		t.Run(name, func(t *testing.T) {
			assumed := map[string]string{
				"event.name": "api_request", "model": "claude-haiku-5-5", "request_id": "req_1",
				"input_tokens": "10", "output_tokens": "2", "cache_creation_tokens": "100",
			}
			assumed[change[0]] = change[1]
			batch, err := Parse(claudeLogs(claudeRecord(t, stated), claudeRecord(t, assumed)), JSON, Logs)
			if err != nil {
				t.Fatal(err)
			}
			if len(batch.Usage) != 2 {
				t.Fatalf("%d usages, want both", len(batch.Usage))
			}
		})
	}
}
