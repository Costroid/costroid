package otlpemit

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/otlp"
)

// runRows is the recorded Claude Code 2.1.316 subagent run's rows
// (internal/server/testdata) and the recorded Codex run's rows
// (internal/comment/testdata/codex-stopped.jsonl), each given the join tags a
// pull request's run writes. Rows 6 to 13 are two requests in the same
// seconds, of one session and one model.
const runRows = "testdata/run-rows.jsonl"

// wantRequest is one request of runRows as its rows state it: the GenAI
// counts, with every cache write lifetime as one figure, and the exact sum of
// its ListCost.
type wantRequest struct {
	model                                      string
	input, output, cacheRead, written, reasons int64
	amount, basis                              string
	rows                                       int
}

var runRequests = []wantRequest{
	{"claude-haiku-5-5", 2 + 2679 + 3608, 84, 2679, 3608, 0, "0.00079059", "list_equivalent", 5},
	{"claude-haiku-5-5", 2 + 3495, 4, 0, 3495, 0, "0.000439075", "list_equivalent", 3},
	{"claude-haiku-5-5", 2 + 6287 + 352, 60, 6287, 352, 0, "0.00016347", "list_equivalent", 5},
	{"claude-haiku-5-5", 2 + 6639 + 490, 124, 6639, 490, 121, "0.00022659", "list_equivalent", 6},
	{"gpt-5.6-sol", 5632 + 11264, 64, 11264, 0, 0, "0.0283136", "measured", 4},
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func spansOf(t *testing.T, data []byte) []span {
	t.Helper()
	requests, _, err := readRequests(data)
	if err != nil {
		t.Fatal(err)
	}
	spans := make([]span, len(requests))
	for i, q := range requests {
		spans[i] = spanOf(q)
	}
	return spans
}

// bodies are one batch of spans in both encodings.
func bodies(t *testing.T, spans []span) map[otlp.Encoding][]byte {
	t.Helper()
	encoded, err := encodeJSON(spans, "1.0.0-test")
	if err != nil {
		t.Fatal(err)
	}
	return map[otlp.Encoding][]byte{otlp.JSON: encoded, otlp.Protobuf: encodeProtobuf(spans, "1.0.0-test")}
}

func encodingName(encoding otlp.Encoding) string {
	if encoding == otlp.JSON {
		return "json"
	}
	return "protobuf"
}

// genAI folds a decoded usage into the figures runRequests states.
func genAI(usage otlp.Usage) (input, output, cacheRead, written, reasoning int64) {
	for _, count := range usage.Counts {
		switch {
		case count.Class == focus.InputUncached:
			input += count.Tokens
		case count.Class == focus.CacheRead:
			input += count.Tokens
			cacheRead += count.Tokens
		case count.Class.Lifetime() != "":
			input += count.Tokens
			written += count.Tokens
		case count.Class == focus.Output:
			output += count.Tokens
		case count.Class == focus.ReasoningOutput:
			reasoning += count.Tokens
		}
	}
	return input, output, cacheRead, written, reasoning
}

func TestEmitRoundTripsThroughDecoder(t *testing.T) {
	spans := spansOf(t, readFile(t, runRows))
	if len(spans) != len(runRequests) {
		t.Fatalf("%d spans, want %d", len(spans), len(runRequests))
	}
	for encoding, body := range bodies(t, spans) {
		t.Run(encodingName(encoding), func(t *testing.T) {
			batch, err := otlp.Parse(body, encoding, otlp.Traces)
			if err != nil {
				t.Fatal(err)
			}
			if batch.Rejected != 0 || len(batch.Usage) != len(runRequests) {
				t.Fatalf("decoded %d usages and rejected %d, want %d and 0", len(batch.Usage), batch.Rejected, len(runRequests))
			}
			for i, want := range runRequests {
				usage := batch.Usage[i]
				input, output, cacheRead, written, reasoning := genAI(usage)
				if usage.Model != want.model || input != want.input || output != want.output ||
					cacheRead != want.cacheRead || written != want.written || reasoning != want.reasons {
					t.Errorf("request %d decodes as %s in %d out %d read %d written %d reasoning %d, want %+v",
						i+1, usage.Model, input, output, cacheRead, written, reasoning, want)
				}
				if usage.Agent == "" || usage.Agent == "costroid" {
					t.Errorf("request %d decodes as the agent %q, not the row's", i+1, usage.Agent)
				}
				if !usage.Start.Equal(spans[i].start) || !usage.End.Equal(spans[i].end) {
					t.Errorf("request %d decodes from %s to %s, want %s to %s", i+1, usage.Start, usage.End, spans[i].start, spans[i].end)
				}
			}
			for _, label := range []string{otlp.DropContent, otlp.DropClientCost} {
				if batch.Drops[label] != 0 {
					t.Errorf("the decoder dropped %d attributes as %s", batch.Drops[label], label)
				}
			}
		})
	}
}

func TestEmitSplitsTwoRequestsOfTheSameSeconds(t *testing.T) {
	requests, rows, err := readRequests(readFile(t, runRows))
	if err != nil {
		t.Fatal(err)
	}
	if rows != 23 {
		t.Errorf("read %d rows, want 23", rows)
	}
	for i, want := range runRequests {
		if got := len(requests[i].rowIDs); got != want.rows {
			t.Errorf("request %d holds %d rows, want %d", i+1, got, want.rows)
		}
	}
	if !requests[1].start.Equal(requests[2].start) || requests[1].key != requests[2].key {
		t.Fatal("the fixture no longer holds two requests in the same seconds")
	}
}

// attributeValue is one attribute as an encoding wrote it: the AnyValue's
// field, by its protobuf number or its JSON name, and its text.
type attributeValue struct {
	field string
	text  string
}

func jsonSpanAttributes(t *testing.T, body []byte) []map[string]attributeValue {
	t.Helper()
	if bytes.Contains(body, []byte("doubleValue")) {
		t.Error("the JSON body holds a doubleValue")
	}
	var payload struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []struct {
					Attributes []struct {
						Key   string                     `json:"key"`
						Value map[string]json.RawMessage `json:"value"`
					} `json:"attributes"`
				} `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	var spans []map[string]attributeValue
	for _, resource := range payload.ResourceSpans {
		for _, scope := range resource.ScopeSpans {
			for _, s := range scope.Spans {
				attributes := map[string]attributeValue{}
				for _, a := range s.Attributes {
					for field, raw := range a.Value {
						attributes[a.Key] = attributeValue{field: field, text: string(raw)}
					}
				}
				spans = append(spans, attributes)
			}
		}
	}
	return spans
}

// wireField is one protobuf field of a message.
type wireField struct {
	number, wire int
	data         []byte
	bits         uint64
}

func wireFields(t *testing.T, message []byte) []wireField {
	t.Helper()
	var fields []wireField
	for len(message) > 0 {
		tag, n := binary.Uvarint(message)
		if n <= 0 {
			t.Fatal("a bad tag")
		}
		message = message[n:]
		f := wireField{number: int(tag >> 3), wire: int(tag & 7)}
		switch f.wire {
		case wireVarint:
			f.bits, n = binary.Uvarint(message)
			message = message[n:]
		case wireFixed64:
			f.bits, message = binary.LittleEndian.Uint64(message), message[8:]
		case wireBytes:
			size, n := binary.Uvarint(message)
			f.data, message = message[n:n+int(size)], message[n+int(size):]
		default:
			t.Fatalf("wire type %d", f.wire)
		}
		fields = append(fields, f)
	}
	return fields
}

func protobufSpanAttributes(t *testing.T, body []byte) []map[string]attributeValue {
	t.Helper()
	names := map[int]string{fieldString: "stringValue", fieldInt: "intValue", fieldArray: "arrayValue", 4: "doubleValue"}
	var spans []map[string]attributeValue
	for _, resource := range wireFields(t, body) {
		for _, scope := range wireFields(t, resource.data) {
			if scope.number != fieldScopeSpans {
				continue
			}
			for _, s := range wireFields(t, scope.data) {
				if s.number != fieldSpans {
					continue
				}
				attributes := map[string]attributeValue{}
				for _, f := range wireFields(t, s.data) {
					if f.number != fieldSpanAttributes {
						continue
					}
					pair := wireFields(t, f.data)
					value := wireFields(t, pair[1].data)[0]
					text := `"` + string(value.data) + `"`
					if value.wire == wireVarint {
						text = `"` + strconv.FormatInt(int64(value.bits), 10) + `"`
					}
					attributes[string(pair[0].data)] = attributeValue{field: names[value.number], text: text}
				}
				spans = append(spans, attributes)
			}
		}
	}
	return spans
}

func TestEmitNeverWritesFloatCost(t *testing.T) {
	spans := spansOf(t, readFile(t, runRows))
	for encoding, body := range bodies(t, spans) {
		t.Run(encodingName(encoding), func(t *testing.T) {
			read := jsonSpanAttributes
			if encoding == otlp.Protobuf {
				read = protobufSpanAttributes
			}
			decoded := read(t, body)
			if len(decoded) != len(runRequests) {
				t.Fatalf("%d spans, want %d", len(decoded), len(runRequests))
			}
			for i, want := range runRequests {
				attributes := decoded[i]
				for key, value := range attributes {
					if value.field == "doubleValue" {
						t.Errorf("span %d writes %s as a double", i+1, key)
					}
				}
				for key, text := range map[string]string{keyCostAmount: want.amount, keyCostCurrency: "USD", keyCostBasis: want.basis} {
					if got := attributes[key]; got.field != "stringValue" || got.text != `"`+text+`"` {
						t.Errorf("span %d writes %s as %s %s, want the string %q", i+1, key, got.field, got.text, text)
					}
				}
				if got := attributes[keyInputTokens]; got.field != "intValue" {
					t.Errorf("span %d writes %s as %s", i+1, keyInputTokens, got.field)
				}
			}
		})
	}
}

func TestEmitCarriesJoinKeys(t *testing.T) {
	data := readFile(t, runRows)
	requests, _, err := readRequests(data)
	if err != nil {
		t.Fatal(err)
	}
	spans := spansOf(t, data)
	keys := map[string]string{
		keyRepository:   `"https://github.com/example/widgets"`,
		keyChange:       `"1423"`,
		keyHead:         `"9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d"`,
		keyRun:          `"16873409921"`,
		keyConversation: "",
		keyRowID:        "",
	}
	for i, attributes := range jsonSpanAttributes(t, bodies(t, spans)[otlp.JSON]) {
		for key, want := range keys {
			got, found := attributes[key]
			if !found || want != "" && got.text != want {
				t.Errorf("span %d carries %s as %q, want %s", i+1, key, got.text, want)
			}
		}
		var ids struct {
			Values []struct {
				String string `json:"stringValue"`
			} `json:"values"`
		}
		if err := json.Unmarshal([]byte(attributes[keyRowID].text), &ids); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, id := range ids.Values {
			got = append(got, id.String)
		}
		if !slices.Equal(got, requests[i].rowIDs) {
			t.Errorf("span %d carries the row ids %v, want %v", i+1, got, requests[i].rowIDs)
		}
	}
	batch, err := otlp.Parse(bodies(t, spans)[otlp.Protobuf], otlp.Protobuf, otlp.Traces)
	if err != nil {
		t.Fatal(err)
	}
	for i, usage := range batch.Usage {
		if usage.Repo != "https://github.com/example/widgets" || usage.PullRequest != "1423" ||
			usage.CommitSHA != "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d" || usage.RunID != "16873409921" ||
			usage.Session != requests[i].tags[focus.TagSessionID] {
			t.Errorf("request %d decodes with the join keys %q %q %q %q %q", i+1, usage.Repo, usage.PullRequest, usage.CommitSHA, usage.RunID, usage.Session)
		}
	}
	seen := map[[8]byte]bool{}
	for _, s := range spans {
		if s.traceID != spans[0].traceID {
			t.Error("one run's requests are in more than one trace")
		}
		if seen[s.spanID] {
			t.Error("two requests share a span id")
		}
		seen[s.spanID] = true
	}
}

func environment(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestEmitHonoursStandardEnvironment(t *testing.T) {
	cases := []struct {
		name      string
		env       map[string]string
		endpoint  string
		protocol  Protocol
		timeout   time.Duration
		gzip      bool
		headers   [][2]string
		warnings  int
		wantError string
	}{
		{name: "defaults", endpoint: "http://localhost:4318/v1/traces", protocol: Protobuf, timeout: 10 * time.Second},
		{
			name:     "the general endpoint takes v1/traces",
			env:      map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "https://collector.example:4318"},
			endpoint: "https://collector.example:4318/v1/traces", protocol: Protobuf, timeout: 10 * time.Second,
		},
		{
			name:     "a base path keeps its prefix",
			env:      map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector:4318/mycollector/"},
			endpoint: "http://collector:4318/mycollector/v1/traces", protocol: Protobuf, timeout: 10 * time.Second,
		},
		{
			name: "the traces endpoint is used as it is and wins",
			env: map[string]string{
				"OTEL_EXPORTER_OTLP_ENDPOINT":        "http://ignored:4318",
				"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://collector:4318",
			},
			endpoint: "http://collector:4318/", protocol: Protobuf, timeout: 10 * time.Second,
		},
		{
			name: "the traces forms win over the general ones",
			env: map[string]string{
				"OTEL_EXPORTER_OTLP_HEADERS":            "api-key=general",
				"OTEL_EXPORTER_OTLP_TRACES_HEADERS":     "api-key=traces%20key, x-tenant = a=b",
				"OTEL_EXPORTER_OTLP_PROTOCOL":           "http/protobuf",
				"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL":    "http/json",
				"OTEL_EXPORTER_OTLP_TIMEOUT":            "30000",
				"OTEL_EXPORTER_OTLP_TRACES_TIMEOUT":     "2500",
				"OTEL_EXPORTER_OTLP_COMPRESSION":        "none",
				"OTEL_EXPORTER_OTLP_TRACES_COMPRESSION": "gzip",
			},
			endpoint: "http://localhost:4318/v1/traces", protocol: JSON, timeout: 2500 * time.Millisecond, gzip: true,
			headers: [][2]string{{"api-key", "traces key"}, {"x-tenant", "a=b"}},
		},
		{
			name: "values the specification says to ignore are read as unset",
			env: map[string]string{
				"OTEL_EXPORTER_OTLP_PROTOCOL":    "thrift",
				"OTEL_EXPORTER_OTLP_TIMEOUT":     "10s",
				"OTEL_EXPORTER_OTLP_COMPRESSION": "zstd",
				"OTEL_EXPORTER_OTLP_HEADERS":     "novalue,api-key=kept",
			},
			endpoint: "http://localhost:4318/v1/traces", protocol: Protobuf, timeout: 10 * time.Second,
			headers: [][2]string{{"api-key", "kept"}}, warnings: 4,
		},
		{name: "grpc is refused", env: map[string]string{"OTEL_EXPORTER_OTLP_PROTOCOL": "grpc"}, wantError: "OTEL_EXPORTER_OTLP_PROTOCOL is grpc"},
		{name: "a scheme other than http is refused", env: map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "collector:4317"}, wantError: "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT is not an http or https URL"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			config, warnings, err := FromEnvironment(environment(c.env))
			if c.wantError != "" {
				if err == nil || !strings.HasPrefix(err.Error(), c.wantError) {
					t.Fatalf("error %v, want %q", err, c.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if config.Endpoint.String() != c.endpoint || config.Protocol != c.protocol || config.Timeout != c.timeout ||
				config.Gzip != c.gzip || !slices.Equal(config.Headers, c.headers) || len(warnings) != c.warnings {
				t.Errorf("read %s %s %s gzip %t headers %q, %d warnings %v", config.Endpoint, config.Protocol, config.Timeout, config.Gzip, config.Headers, len(warnings), warnings)
			}
			for _, warning := range warnings {
				if strings.Contains(warning.String(), "kept") || strings.Contains(warning.String(), "novalue") {
					t.Errorf("a warning prints a header: %s", warning)
				}
			}
		})
	}
	t.Run("a collector receives what the variables say", func(t *testing.T) {
		var received *http.Request
		var body []byte
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			received = r
			reader, err := gzip.NewReader(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			body, _ = io.ReadAll(reader)
		}))
		defer server.Close()
		config, _, err := FromEnvironment(environment(map[string]string{
			"OTEL_EXPORTER_OTLP_ENDPOINT":    server.URL + "/otlp",
			"OTEL_EXPORTER_OTLP_HEADERS":     "authorization=Basic%20dXNlcjpwYXNz",
			"OTEL_EXPORTER_OTLP_PROTOCOL":    "http/json",
			"OTEL_EXPORTER_OTLP_COMPRESSION": "gzip",
		}))
		if err != nil {
			t.Fatal(err)
		}
		result, err := Emitter{Config: config, Version: "1.0.0-test"}.Emit(context.Background(), readFile(t, runRows))
		if err != nil || result.Spans != len(runRequests) || result.Rows != 23 {
			t.Fatalf("sent %+v: %v", result, err)
		}
		if received.URL.Path != "/otlp/v1/traces" || received.Header.Get("Authorization") != "Basic dXNlcjpwYXNz" ||
			received.Header.Get("Content-Type") != "application/json" || received.Header.Get("Content-Encoding") != "gzip" {
			t.Errorf("the collector received %s %v", received.URL.Path, received.Header)
		}
		batch, err := otlp.Parse(body, otlp.JSON, otlp.Traces)
		if err != nil || len(batch.Usage) != len(runRequests) {
			t.Errorf("the collector decoded %d usages: %v", len(batch.Usage), err)
		}
	})
	t.Run("the certificate variable is the trusted root", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		defer server.Close()
		path := filepath.Join(t.TempDir(), "ca.pem")
		certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
		if err := os.WriteFile(path, certificate, 0o600); err != nil {
			t.Fatal(err)
		}
		env := map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": server.URL + "/v1/traces"}
		untrusted, _, err := FromEnvironment(environment(env))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := (Emitter{Config: untrusted, Sleep: noSleep}).Emit(context.Background(), readFile(t, runRows)); err == nil {
			t.Error("an endpoint whose certificate no root signs was trusted")
		}
		env["OTEL_EXPORTER_OTLP_CERTIFICATE"] = path
		trusted, _, err := FromEnvironment(environment(env))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := (Emitter{Config: trusted}).Emit(context.Background(), readFile(t, runRows)); err != nil {
			t.Errorf("the endpoint the certificate signs was refused: %v", err)
		}
	})
}

func noSleep(context.Context, time.Duration) error { return nil }

func TestEmitRetriesWhatTheSpecificationCallsTransient(t *testing.T) {
	answers := []int{http.StatusServiceUnavailable, http.StatusTooManyRequests, http.StatusOK}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if answers[calls] == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "7")
		}
		w.WriteHeader(answers[calls])
		calls++
	}))
	defer server.Close()
	config, _, err := FromEnvironment(environment(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": server.URL}))
	if err != nil {
		t.Fatal(err)
	}
	var waits []time.Duration
	sleep := func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	result, err := Emitter{Config: config, Sleep: sleep}.Emit(context.Background(), readFile(t, runRows))
	if err != nil || result.Spans != len(runRequests) || calls != 3 {
		t.Fatalf("%d calls, sent %+v: %v", calls, result, err)
	}
	if len(waits) != 2 || waits[0] < firstWait/2 || waits[0] > firstWait || waits[1] != 7*time.Second {
		t.Errorf("waited %v, want between 0.5 s and 1 s, then the 7 s Retry-After asks for", waits)
	}
}

func TestEmitStopsOnARefusalAndFollowsNoRedirect(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusTemporaryRedirect} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			w.Header().Set("Location", "http://elsewhere.example/v1/traces")
			w.WriteHeader(status)
		}))
		config, _, err := FromEnvironment(environment(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": server.URL}))
		if err != nil {
			t.Fatal(err)
		}
		_, err = Emitter{Config: config, Sleep: noSleep}.Emit(context.Background(), readFile(t, runRows))
		var refused *RefusedError
		if !errors.As(err, &refused) || refused.Status != status || refused.Spans != len(runRequests) || calls != 1 {
			t.Errorf("HTTP %d: %d calls, %v", status, calls, err)
		}
		server.Close()
	}
}

func TestEmitReadsAPartialSuccess(t *testing.T) {
	protobufAnswer := appendBytes(nil, 1, appendBytes(appendVarint(nil, 1, 2), 2, []byte("two spans\nhad no name")))
	answers := map[string][]byte{
		"application/json":       []byte(`{"partialSuccess":{"rejectedSpans":"2","errorMessage":"two spans\nhad no name"}}`),
		"application/x-protobuf": protobufAnswer,
	}
	for contentType, answer := range answers {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", contentType)
			w.Write(answer)
		}))
		config, _, err := FromEnvironment(environment(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": server.URL}))
		if err != nil {
			t.Fatal(err)
		}
		result, err := Emitter{Config: config}.Emit(context.Background(), readFile(t, runRows))
		if err != nil || result.Rejected != 2 || result.Message != "two spans had no name" {
			t.Errorf("%s: %+v, %v", contentType, result, err)
		}
		server.Close()
	}
}

func TestEmitSendsAtMostOneBatchOfSpansARequest(t *testing.T) {
	line := bytes.SplitN(readFile(t, runRows), []byte("\n"), 2)[0]
	var many bytes.Buffer
	for range batchSpans + 1 {
		many.Write(line)
		many.WriteByte('\n')
	}
	var sizes []int
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		batch, err := otlp.Parse(body, otlp.Protobuf, otlp.Traces)
		if err != nil {
			t.Error(err)
		}
		sizes = append(sizes, len(batch.Usage))
	}))
	defer server.Close()
	config, _, err := FromEnvironment(environment(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": server.URL}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := Emitter{Config: config}.Emit(context.Background(), many.Bytes())
	if err != nil || result.Spans != batchSpans+1 || !slices.Equal(sizes, []int{batchSpans, 1}) {
		t.Errorf("sent %+v in requests of %v: %v", result, sizes, err)
	}
}

func TestEmitRefusesALineThatIsNoRow(t *testing.T) {
	data := append(readFile(t, runRows), []byte(`{"BilledCost":"0"}`+"\n")...)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	config, _, err := FromEnvironment(environment(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": server.URL}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (Emitter{Config: config}).Emit(context.Background(), data); err == nil || calls != 0 {
		t.Errorf("%d calls, error %v", calls, err)
	}
}
