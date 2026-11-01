package otlp

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/focus"
)

const sentinel = "SENTINEL-CONTENT-7f3a"

// attr builds one OTLP/JSON attribute.
func attr(key string, v any) map[string]any {
	value := map[string]any{}
	switch typed := v.(type) {
	case string:
		value["stringValue"] = typed
	case int:
		value["intValue"] = fmt.Sprint(typed)
	case float64:
		value["doubleValue"] = typed
	case []string:
		value["arrayValue"] = map[string]any{"values": typed}
	}
	return map[string]any{"key": key, "value": value}
}

func attrs(pairs map[string]any) []map[string]any {
	list := make([]map[string]any, 0, len(pairs))
	for key, v := range pairs {
		list = append(list, attr(key, v))
	}
	return list
}

func spanBody(t *testing.T, resource, span map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"resourceSpans": []any{map[string]any{
		"resource": map[string]any{"attributes": attrs(resource)},
		"scopeSpans": []any{map[string]any{"spans": []any{map[string]any{
			"traceId":           "aa",
			"spanId":            "bb",
			"name":              "chat",
			"startTimeUnixNano": "1793534400000000000",
			"endTimeUnixNano":   "1793534402000000000",
			"attributes":        attrs(span),
		}}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func logBody(t *testing.T, resource map[string]any, log map[string]any, text string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"resourceLogs": []any{map[string]any{
		"resource": map[string]any{"attributes": attrs(resource)},
		"scopeLogs": []any{map[string]any{"logRecords": []any{map[string]any{
			"timeUnixNano": "1793534400000000000",
			"body":         map[string]any{"stringValue": text},
			"attributes":   attrs(log),
		}}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func classes(counts []Count) map[focus.TokenClass]int64 {
	m := map[focus.TokenClass]int64{}
	for _, c := range counts {
		m[c.Class] = c.Tokens
	}
	return m
}

func one(t *testing.T, batch Batch) Usage {
	t.Helper()
	if len(batch.Usage) != 1 || batch.Rejected != 0 {
		t.Fatalf("%d usages, %d rejected", len(batch.Usage), batch.Rejected)
	}
	return batch.Usage[0]
}

func parseSpan(t *testing.T, resource, span map[string]any) Usage {
	t.Helper()
	batch, err := Parse(spanBody(t, resource, span), JSON, Traces)
	if err != nil {
		t.Fatal(err)
	}
	return one(t, batch)
}

func assertClasses(t *testing.T, got []Count, want map[focus.TokenClass]int64) {
	t.Helper()
	have := classes(got)
	if len(have) != len(want) {
		t.Fatalf("classes %v, want %v", have, want)
	}
	for class, tokens := range want {
		if have[class] != tokens {
			t.Fatalf("classes %v, want %v", have, want)
		}
	}
}

func TestDialectsMapTheirCountsToDisjointClasses(t *testing.T) {
	cases := []struct {
		name     string
		dialect  Dialect
		resource map[string]any
		span     map[string]any
		want     map[focus.TokenClass]int64
		context  int64
	}{
		{
			name:    "standard input is inclusive and writes use the new name",
			dialect: Standard,
			span: map[string]any{
				"gen_ai.provider.name": "anthropic", "gen_ai.response.model": "claude-opus-5-5",
				keyInput: 1000, keyOutput: 300, keyCacheRead: 200, keyCacheWrite: 100, keyReasoning: 50,
			},
			want: map[focus.TokenClass]int64{
				focus.InputUncached: 700, focus.CacheRead: 200, focus.CacheWrite5m: 100, focus.Output: 300,
				focus.ReasoningOutput: 50, focus.CachedInputReported: 200,
			},
			context: 1000,
		},
		{
			name:    "standard accepts the old write name and gen_ai.system",
			dialect: Standard,
			span: map[string]any{
				"gen_ai.system": "anthropic", "gen_ai.request.model": "claude-opus-5-5",
				keyInput: 1000, keyOutput: 300, keyCacheCreationOld: 100,
			},
			want:    map[focus.TokenClass]int64{focus.InputUncached: 900, focus.CacheWrite5m: 100, focus.Output: 300},
			context: 1000,
		},
		{
			name:    "the oldest names still read",
			dialect: Standard,
			span: map[string]any{
				"gen_ai.system": "openai", "gen_ai.request.model": "gpt-5.5",
				keyPromptTokensOld: 40, keyCompletionTokensOld: 7,
			},
			want:    map[focus.TokenClass]int64{focus.InputUncached: 40, focus.Output: 7},
			context: 40,
		},
		{
			name:     "codex reads its own reasoning attribute and writes last thirty minutes",
			dialect:  Codex,
			resource: map[string]any{"service.name": "codex_exec"},
			span: map[string]any{
				"gen_ai.response.model": "gpt-5.6-sol", keyInput: 1000, keyOutput: 300,
				keyCacheRead: 200, keyCacheWrite: 100, keyCodexReasoning: 50,
			},
			want: map[focus.TokenClass]int64{
				focus.InputUncached: 700, focus.CacheRead: 200, focus.CacheWrite30m: 100, focus.Output: 300,
				focus.ReasoningOutput: 50, focus.CachedInputReported: 200,
			},
			context: 1000,
		},
		{
			name:     "copilot writes under the old attribute",
			dialect:  Copilot,
			resource: map[string]any{"service.name": "github-copilot"},
			span: map[string]any{
				"gen_ai.response.model": "claude-sonnet-5-5", keyInput: 500, keyOutput: 20,
				keyCacheRead: 100, keyCacheCreationOld: 50,
			},
			want: map[focus.TokenClass]int64{
				focus.InputUncached: 350, focus.CacheRead: 100, focus.CacheWrite5m: 50, focus.Output: 20,
				focus.CachedInputReported: 100,
			},
			context: 500,
		},
		{
			name:     "opencode has no write attribute",
			dialect:  OpenCode,
			resource: map[string]any{"service.name": "opencode"},
			span: map[string]any{
				"gen_ai.provider.name": "openai", "gen_ai.response.model": "gpt-5.5",
				keyInput: 500, keyOutput: 20, keyCacheRead: 100,
			},
			want: map[focus.TokenClass]int64{
				focus.InputUncached: 400, focus.CacheRead: 100, focus.Output: 20, focus.CachedInputReported: 100,
			},
			context: 500,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			usage := parseSpan(t, tc.resource, tc.span)
			if usage.Dialect != tc.dialect {
				t.Fatalf("dialect %s", usage.Dialect)
			}
			assertClasses(t, usage.Counts, tc.want)
			if usage.ContextInputTokens != tc.context {
				t.Fatalf("context %d", usage.ContextInputTokens)
			}
		})
	}
}

func parseClaude(t *testing.T, extra map[string]any) Usage {
	t.Helper()
	log := map[string]any{
		"event.name": "api_request", "model": "claude-haiku-5-5", "request_id": "req_1",
		"input_tokens": "2", "output_tokens": "84", "cache_read_tokens": "2679",
		"cache_creation_tokens": "3608", "cache_creation_5m_tokens": "0", "cache_creation_1h_tokens": "3608",
	}
	for key, v := range extra {
		log[key] = v
	}
	batch, err := Parse(logBody(t, map[string]any{"service.name": "claude-code"}, log, eventClaudeRequest), JSON, Logs)
	if err != nil {
		t.Fatal(err)
	}
	return one(t, batch)
}

func TestClaudeCodeOwnShapeIsNotInclusive(t *testing.T) {
	usage := parseClaude(t, nil)
	if usage.Dialect != ClaudeCode || usage.Provider != providerAnthropic || usage.ID != "req_1" {
		t.Fatalf("%+v", usage)
	}
	assertClasses(t, usage.Counts, map[focus.TokenClass]int64{
		focus.InputUncached: 2, focus.CacheRead: 2679, focus.CacheWrite1h: 3608, focus.Output: 84,
		focus.CachedInputReported: 2679,
	})
	if usage.ContextInputTokens != 6289 {
		t.Fatalf("context %d", usage.ContextInputTokens)
	}
}

func TestClaudeCodeWithNoLifetimeSplitAssumesOneAndSaysSo(t *testing.T) {
	usage := parseClaude(t, map[string]any{"cache_creation_1h_tokens": "0"})
	assertClasses(t, usage.Counts, map[focus.TokenClass]int64{
		focus.InputUncached: 2, focus.CacheRead: 2679, focus.CacheWrite1h: 3608, focus.Output: 84,
		focus.CachedInputReported: 2679,
	})
	if !slices.Contains(usage.Notes, "cache lifetime assumed") {
		t.Fatalf("notes %v", usage.Notes)
	}
	measured := parseClaude(t, map[string]any{"cache_creation_1h_tokens": "0", "costroid.cost_basis": "measured"})
	if classes(measured.Counts)[focus.CacheWrite5m] != 3608 {
		t.Fatalf("an API-key run should assume five minutes: %v", measured.Counts)
	}
}

func TestClaudeCodeFastSpeedPricesTheFastTier(t *testing.T) {
	if usage := parseClaude(t, map[string]any{"speed": "fast"}); usage.ServiceTier != tierFast {
		t.Fatalf("tier %s", usage.ServiceTier)
	}
}

func TestCursorDialectIsNotInclusive(t *testing.T) {
	log := map[string]any{
		keyCursorInput: 100, keyCursorOutput: 10, keyCursorCacheRead: 40, keyCursorCacheCreation: 5,
		"model": "claude-sonnet-5-5",
	}
	batch, err := Parse(logBody(t, map[string]any{"service.name": "cursor"}, log, eventCursorRequest), JSON, Logs)
	if err != nil {
		t.Fatal(err)
	}
	usage := one(t, batch)
	if usage.Dialect != Cursor {
		t.Fatalf("dialect %s", usage.Dialect)
	}
	assertClasses(t, usage.Counts, map[focus.TokenClass]int64{
		focus.InputUncached: 100, focus.CacheRead: 40, focus.CacheWrite5m: 5, focus.Output: 10,
		focus.CachedInputReported: 40,
	})
}

func TestACodexLogIsLeftOutSoARequestCountsOnce(t *testing.T) {
	log := map[string]any{keyInput: 10, keyOutput: 2, "model": "gpt-5.5"}
	batch, err := Parse(logBody(t, map[string]any{"service.name": "codex_exec"}, log, "codex.sse_event"), JSON, Logs)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Usage) != 0 || batch.Rejected != 0 {
		t.Fatalf("%+v", batch)
	}
}

func TestARecordWithoutTokensYieldsNothing(t *testing.T) {
	batch, err := Parse(spanBody(t, nil, map[string]any{"gen_ai.operation.name": "execute_tool"}), JSON, Traces)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Usage) != 0 || batch.Rejected != 0 {
		t.Fatalf("%+v", batch)
	}
}

func TestImpossibleCountsAreRejectedNotClamped(t *testing.T) {
	cases := map[string]map[string]any{
		"cache above an inclusive input": {keyInput: 10, keyOutput: 1, keyCacheRead: 20},
		"reasoning above output":         {keyInput: 10, keyOutput: 1, keyReasoning: 2},
		"negative":                       {keyInput: "-5", keyOutput: 1},
		"not a number":                   {keyInput: "many", keyOutput: 1},
		"past the bound":                 {keyInput: "9000000000000000", keyOutput: 1},
		"fractional":                     {keyInput: 10.5, keyOutput: 1},
	}
	for name, span := range cases {
		t.Run(name, func(t *testing.T) {
			span["gen_ai.response.model"] = "gpt-5.5"
			batch, err := Parse(spanBody(t, nil, span), JSON, Traces)
			if err != nil {
				t.Fatal(err)
			}
			if len(batch.Usage) != 0 || batch.Rejected != 1 {
				t.Fatalf("%+v", batch)
			}
		})
	}
}

func TestAnUnknownProviderIsRejectedNeverPricedAtZero(t *testing.T) {
	span := map[string]any{"gen_ai.provider.name": "aws.bedrock", "gen_ai.response.model": "titan", keyInput: 1, keyOutput: 1}
	batch, err := Parse(spanBody(t, nil, span), JSON, Traces)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Rejected != 1 {
		t.Fatalf("%+v", batch)
	}
}

func TestAJavaScriptDoubleCountReads(t *testing.T) {
	usage := parseSpan(t, nil, map[string]any{"gen_ai.response.model": "gpt-5.5", keyInput: 40.0, keyOutput: 2.0})
	assertClasses(t, usage.Counts, map[focus.TokenClass]int64{focus.InputUncached: 40, focus.Output: 2})
}

func TestAResponseIDIsTheRowIdentitySoARepeatedSpanCollapses(t *testing.T) {
	span := map[string]any{"gen_ai.response.id": "resp_1", "gen_ai.response.model": "gpt-5.5", keyInput: 1, keyOutput: 1}
	if usage := parseSpan(t, nil, span); usage.ID != "resp_1" {
		t.Fatalf("id %s", usage.ID)
	}
}

func TestJoinKeysAndIdentityComeFromTheResource(t *testing.T) {
	usage := parseSpan(t, map[string]any{
		"vcs.repository.url.full": "https://github.com/acme/widgets",
		"vcs.change.id":           "12",
		"vcs.ref.head.revision":   "abc",
		"cicd.pipeline.run.id":    "99",
		"session.id":              "s-1",
		"gen_ai.agent.name":       "My Agent",
		"gen_ai.agent.version":    "1.2.3",
	}, map[string]any{"gen_ai.response.model": "gpt-5.5", keyInput: 1, keyOutput: 1})
	if usage.Repo != "https://github.com/acme/widgets" || usage.PullRequest != "12" || usage.CommitSHA != "abc" ||
		usage.RunID != "99" || usage.Session != "s-1" || usage.Agent != "my-agent" || usage.AgentVersion != "1.2.3" {
		t.Fatalf("%+v", usage)
	}
	if !usage.Start.Equal(time.Unix(1793534400, 0)) {
		t.Fatalf("start %v", usage.Start)
	}
}

func TestContentFieldsAreDroppedAndCountedByName(t *testing.T) {
	named := []string{
		"gen_ai.input.messages", "gen_ai.output.messages", "gen_ai.system_instructions",
		"gen_ai.tool.call.arguments", "gen_ai.tool.call.result", "gen_ai.tool.definitions",
		"gen_ai.prompt", "gen_ai.completion", "prompt", "tool_parameters", "tool_input", "tool_output",
		"body", "request_body", "response_body",
	}
	other := []string{"gen_ai.prompt.0.content", "gen_ai.completion.0.content", "llm.input_messages.0.text", "codex.prompt", "cursor.conversation.content"}
	span := map[string]any{"gen_ai.response.model": "gpt-5.5", keyInput: 1, keyOutput: 1, "cost_usd": 99.0}
	for _, key := range append(append([]string{}, named...), other...) {
		span[key] = sentinel
	}
	body := spanBody(t, nil, span)
	batch, err := Parse(body, JSON, Traces)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range named {
		if batch.Drops[key] != 1 {
			t.Errorf("%s: dropped %d times", key, batch.Drops[key])
		}
	}
	if batch.Drops[DropContent] != int64(len(other)) {
		t.Errorf("content_other %d", batch.Drops[DropContent])
	}
	if batch.Drops[DropClientCost] != 1 {
		t.Errorf("client_cost %d", batch.Drops[DropClientCost])
	}
	if strings.Contains(fmt.Sprintf("%+v", batch.Usage), sentinel) {
		t.Fatal("content reached a usage")
	}
}

func TestContentOnEventsLinksScopesAndStatusIsCounted(t *testing.T) {
	body := []byte(`{"resourceSpans":[{"scopeSpans":[{"scope":{"attributes":[{"key":"prompt","value":{"stringValue":"x"}}]},"spans":[{
		"status":{"message":"secret"},
		"events":[{"attributes":[{"key":"gen_ai.input.messages","value":{"stringValue":"x"}}]}],
		"links":[{"attributes":[{"key":"gen_ai.output.messages","value":{"stringValue":"x"}}]}]
	}]}]}]}`)
	batch, err := Parse(body, JSON, Traces)
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"prompt", "gen_ai.input.messages", "gen_ai.output.messages", DropStatusMessage} {
		if batch.Drops[label] != 1 {
			t.Errorf("%s: %d", label, batch.Drops[label])
		}
	}
}

func TestAFreeTextLogBodyIsDroppedAndAnEventNameIsNot(t *testing.T) {
	batch, err := Parse(logBody(t, nil, nil, sentinel), JSON, Logs)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Drops[DropLogBody] != 1 {
		t.Fatalf("%+v", batch.Drops)
	}
	batch, err = Parse(logBody(t, nil, nil, eventClaudeRequest), JSON, Logs)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Drops[DropLogBody] != 0 {
		t.Fatalf("%+v", batch.Drops)
	}
}

func TestAnUnlistedAttributeIsDroppedWhateverItIsNamed(t *testing.T) {
	span := map[string]any{"gen_ai.response.model": "gpt-5.5", keyInput: 1, keyOutput: 1, "x.anything": sentinel}
	batch, err := Parse(spanBody(t, nil, span), JSON, Traces)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Drops[DropUnlisted] != 1 {
		t.Fatalf("%+v", batch.Drops)
	}
}

func TestAllowedAttributesThatAreNotScalarsOrAreHugeAreDropped(t *testing.T) {
	span := map[string]any{
		"gen_ai.response.model": "gpt-5.5", keyInput: 1, keyOutput: 1,
		"session.id": strings.Repeat("x", maxValueBytes+1), "gen_ai.agent.name": []string{"a"},
	}
	batch, err := Parse(spanBody(t, nil, span), JSON, Traces)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Drops[DropOversized] != 1 || batch.Drops[DropNonScalar] != 1 {
		t.Fatalf("%+v", batch.Drops)
	}
}

func TestAnEmailStaysOutOfEverythingButItsOwnField(t *testing.T) {
	usage := parseClaude(t, map[string]any{"user.email": "dev@example.com"})
	if usage.Email != "dev@example.com" {
		t.Fatalf("email %q", usage.Email)
	}
}

// Protobuf builders, the inverse of the decoder.

func pbTag(number, wire int) []byte {
	return binary.AppendUvarint(nil, uint64(number)<<3|uint64(wire))
}

func pbBytes(number int, data []byte) []byte {
	out := pbTag(number, wireBytes)
	out = binary.AppendUvarint(out, uint64(len(data)))
	return append(out, data...)
}

func pbString(number int, text string) []byte {
	return pbBytes(number, []byte(text))
}

func pbVarint(number int, v uint64) []byte {
	return binary.AppendUvarint(pbTag(number, wireVarint), v)
}

func pbFixed(number int, v uint64) []byte {
	return binary.LittleEndian.AppendUint64(pbTag(number, wireFixed64), v)
}

func pbKV(key string, v any) []byte {
	var any2 []byte
	switch typed := v.(type) {
	case string:
		any2 = pbString(fieldAnyString, typed)
	case int:
		any2 = pbVarint(fieldAnyInt, uint64(typed))
	case float64:
		any2 = pbFixed(fieldAnyDouble, math.Float64bits(typed))
	case bool:
		any2 = pbVarint(fieldAnyBool, 1)
	}
	return append(pbString(fieldKey, key), pbBytes(fieldValue, any2)...)
}

func pbJoin(parts ...[]byte) []byte {
	return slices.Concat(parts...)
}

func pbSpanRequest() []byte {
	span := pbJoin(
		pbBytes(fieldSpanTraceID, []byte{0xaa}), pbBytes(fieldSpanSpanID, []byte{0xbb}),
		pbString(fieldSpanName, "chat"),
		pbFixed(fieldSpanStart, 1793534400000000000), pbFixed(fieldSpanEnd, 1793534402000000000),
		pbBytes(fieldSpanAttributes, pbKV("gen_ai.response.model", "gpt-5.5")),
		pbBytes(fieldSpanAttributes, pbKV(keyInput, 100)),
		pbBytes(fieldSpanAttributes, pbKV(keyOutput, 7.0)),
		pbBytes(fieldSpanAttributes, pbKV("gen_ai.input.messages", sentinel)),
		pbBytes(fieldSpanEvents, pbBytes(fieldEventAttributes, pbKV("gen_ai.output.messages", sentinel))),
		pbBytes(fieldSpanLinks, pbBytes(fieldLinkAttributes, pbKV("prompt", sentinel))),
		pbBytes(fieldSpanStatus, pbString(fieldStatusMessage, sentinel)),
	)
	scope := pbJoin(
		pbBytes(fieldRecord, span),
		pbBytes(fieldScope, pbBytes(fieldScopeAttributes, pbKV("tool_input", sentinel))),
	)
	return pbBytes(fieldResourceBlock, pbJoin(
		pbBytes(fieldScopeBlock, scope),
		pbBytes(fieldResource, pbBytes(fieldAttributes, pbKV("vcs.change.id", "12"))),
	))
}

func TestProtobufTracesDecodeLikeJSON(t *testing.T) {
	batch, err := Parse(pbSpanRequest(), Protobuf, Traces)
	if err != nil {
		t.Fatal(err)
	}
	usage := one(t, batch)
	assertClasses(t, usage.Counts, map[focus.TokenClass]int64{focus.InputUncached: 100, focus.Output: 7})
	if usage.PullRequest != "12" || usage.ID != "span:aa:bb" {
		t.Fatalf("%+v", usage)
	}
	if !usage.End.Equal(time.Unix(1793534402, 0)) {
		t.Fatalf("end %v", usage.End)
	}
	for _, label := range []string{"gen_ai.input.messages", "gen_ai.output.messages", "prompt", "tool_input", DropStatusMessage} {
		if batch.Drops[label] != 1 {
			t.Errorf("%s: %d", label, batch.Drops[label])
		}
	}
}

func TestProtobufLogsDecodeClaudeCode(t *testing.T) {
	entry := pbJoin(
		pbFixed(fieldLogTime, 1793534400000000000),
		pbBytes(fieldLogBody, pbString(fieldAnyString, eventClaudeRequest)),
		pbBytes(fieldLogAttributes, pbKV("model", "claude-haiku-5-5")),
		pbBytes(fieldLogAttributes, pbKV("request_id", "req_9")),
		pbBytes(fieldLogAttributes, pbKV("input_tokens", 2)),
		pbBytes(fieldLogAttributes, pbKV("output_tokens", "4")),
		pbBytes(fieldLogAttributes, pbKV("user.email", "dev@example.com")),
	)
	scope := pbBytes(fieldRecord, entry)
	body := pbBytes(fieldResourceBlock, pbJoin(
		pbBytes(fieldResource, pbBytes(fieldAttributes, pbKV("service.name", "claude-code"))),
		pbBytes(fieldScopeBlock, scope),
	))
	batch, err := Parse(body, Protobuf, Logs)
	if err != nil {
		t.Fatal(err)
	}
	usage := one(t, batch)
	if usage.ID != "req_9" || usage.Email != "dev@example.com" || usage.Dialect != ClaudeCode {
		t.Fatalf("%+v", usage)
	}
	if batch.Drops[DropEmail] != 1 || batch.Drops[DropLogBody] != 0 {
		t.Fatalf("%+v", batch.Drops)
	}
}

func TestMalformedBodiesAreRefused(t *testing.T) {
	for name, body := range map[string][]byte{
		"truncated tag":   {0x80},
		"length past end": {0x0a, 0x7f, 0x01},
		"reserved wire":   {0x0b},
		"truncated fixed": {0x09, 0x01},
		"nested past end": pbBytes(fieldResourceBlock, []byte{0x0a, 0x7f}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(body, Protobuf, Traces); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	if _, err := Parse([]byte(`{"resourceSpans":`), JSON, Traces); err == nil {
		t.Fatal("truncated JSON accepted")
	}
}

// assertNoContent is the fuzz targets' one invariant beyond not panicking.
func assertNoContent(t *testing.T, batch Batch) {
	t.Helper()
	text := fmt.Sprintf("%+v", batch)
	if strings.Contains(text, sentinel) {
		t.Fatalf("content in the batch: %s", text)
	}
	for label := range batch.Drops {
		if len(label) > 64 {
			t.Fatalf("an unbounded counter label: %q", label)
		}
	}
	for _, usage := range batch.Usage {
		for _, count := range usage.Counts {
			if count.Tokens <= 0 || count.Tokens > 3*MaxTokens {
				t.Fatalf("count %+v", count)
			}
		}
	}
}

func FuzzParseJSONTraces(f *testing.F) {
	f.Add([]byte(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"codex"}}]},"scopeSpans":[{"spans":[{"traceId":"a","spanId":"b","startTimeUnixNano":"1","attributes":[{"key":"gen_ai.usage.input_tokens","value":{"intValue":"5"}},{"key":"gen_ai.usage.output_tokens","value":{"intValue":5}},{"key":"gen_ai.input.messages","value":{"stringValue":"SENTINEL-CONTENT-7f3a"}}]}]}]}]}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		batch, err := Parse(body, JSON, Traces)
		if err == nil {
			assertNoContent(t, batch)
		}
	})
}

func FuzzParseJSONLogs(f *testing.F) {
	f.Add([]byte(`{"resourceLogs":[{"scopeLogs":[{"logRecords":[{"timeUnixNano":"1","body":{"stringValue":"claude_code.api_request"},"attributes":[{"key":"input_tokens","value":{"stringValue":"2"}},{"key":"prompt","value":{"stringValue":"SENTINEL-CONTENT-7f3a"}}]}]}]}]}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		batch, err := Parse(body, JSON, Logs)
		if err == nil {
			assertNoContent(t, batch)
		}
	})
}

func FuzzParseProtobuf(f *testing.F) {
	f.Add(pbSpanRequest(), true)
	f.Add([]byte{0x0a, 0x00}, false)
	f.Fuzz(func(t *testing.T, body []byte, traces bool) {
		signal := Logs
		if traces {
			signal = Traces
		}
		batch, err := Parse(body, Protobuf, signal)
		if err == nil {
			assertNoContent(t, batch)
		}
	})
}
