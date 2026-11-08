package otlpemit

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"strconv"
)

// scopeName names the instrumentation scope of every span.
const scopeName = "costroid"

// encodeJSON writes an ExportTraceServiceRequest in OTLP/JSON. Ids are hex,
// 64-bit integers and timestamps are decimal strings, and the span kind is
// its number, as OTLP 1.11.1 states.
func encodeJSON(spans []span, version string) ([]byte, error) {
	encoded := make([]jsonSpan, len(spans))
	for i, s := range spans {
		encoded[i] = jsonSpan{
			TraceID:    hex.EncodeToString(s.traceID[:]),
			SpanID:     hex.EncodeToString(s.spanID[:]),
			Name:       s.name,
			Kind:       spanKindClient,
			Start:      nanosText(s.start),
			End:        nanosText(s.end),
			Attributes: jsonAttributes(s.attributes),
		}
	}
	return json.Marshal(jsonRequest{ResourceSpans: []jsonResourceSpans{{
		Resource:   jsonResource{Attributes: jsonAttributes(resource(version))},
		ScopeSpans: []jsonScopeSpans{{Scope: jsonScope{Name: scopeName, Version: version}, Spans: encoded}},
	}}})
}

// resource names Costroid as the service, so a backend never takes these
// spans for the agent's own telemetry.
func resource(version string) []attribute {
	return []attribute{text(keyServiceName, scopeName), text(keyServiceVersion, version)}
}

type jsonRequest struct {
	ResourceSpans []jsonResourceSpans `json:"resourceSpans"`
}

type jsonResourceSpans struct {
	Resource   jsonResource     `json:"resource"`
	ScopeSpans []jsonScopeSpans `json:"scopeSpans"`
}

type jsonResource struct {
	Attributes []jsonKeyValue `json:"attributes"`
}

type jsonScopeSpans struct {
	Scope jsonScope  `json:"scope"`
	Spans []jsonSpan `json:"spans"`
}

type jsonScope struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type jsonSpan struct {
	TraceID    string         `json:"traceId"`
	SpanID     string         `json:"spanId"`
	Name       string         `json:"name"`
	Kind       int            `json:"kind"`
	Start      string         `json:"startTimeUnixNano"`
	End        string         `json:"endTimeUnixNano"`
	Attributes []jsonKeyValue `json:"attributes"`
}

type jsonKeyValue struct {
	Key   string    `json:"key"`
	Value jsonValue `json:"value"`
}

// jsonValue is an AnyValue of the three kinds a span carries. It has no
// doubleValue field, so no attribute can be written as a float.
type jsonValue struct {
	String *string    `json:"stringValue,omitempty"`
	Int    string     `json:"intValue,omitempty"`
	Array  *jsonArray `json:"arrayValue,omitempty"`
}

type jsonArray struct {
	Values []jsonValue `json:"values"`
}

func jsonAttributes(attributes []attribute) []jsonKeyValue {
	list := make([]jsonKeyValue, len(attributes))
	for i, a := range attributes {
		list[i] = jsonKeyValue{Key: a.key, Value: jsonValueOf(a)}
	}
	return list
}

func jsonValueOf(a attribute) jsonValue {
	switch a.kind {
	case kindInt:
		return jsonValue{Int: strconv.FormatInt(a.integer, 10)}
	case kindStrings:
		values := make([]jsonValue, len(a.list))
		for i := range a.list {
			values[i] = jsonValue{String: &a.list[i]}
		}
		return jsonValue{Array: &jsonArray{Values: values}}
	}
	return jsonValue{String: &a.text}
}

// The opentelemetry-proto field numbers the protobuf encoder writes.
const (
	fieldResourceSpans  = 1
	fieldResource       = 1
	fieldScopeSpans     = 2
	fieldResourceAttrs  = 1
	fieldScope          = 1
	fieldSpans          = 2
	fieldScopeName      = 1
	fieldScopeVersion   = 2
	fieldTraceID        = 1
	fieldSpanID         = 2
	fieldName           = 5
	fieldKind           = 6
	fieldStart          = 7
	fieldEnd            = 8
	fieldSpanAttributes = 9
	fieldKey            = 1
	fieldValue          = 2
	fieldString         = 1
	fieldInt            = 3
	fieldArray          = 5
	fieldArrayValues    = 1

	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
	wireFixed32 = 5
)

// encodeProtobuf writes the same ExportTraceServiceRequest in protobuf. An
// AnyValue is written only as string_value, int_value or array_value, never
// as double_value.
func encodeProtobuf(spans []span, version string) []byte {
	var scope []byte
	scope = appendBytes(scope, fieldScope, appendBytes(appendBytes(nil, fieldScopeName, []byte(scopeName)), fieldScopeVersion, []byte(version)))
	for _, s := range spans {
		scope = appendBytes(scope, fieldSpans, protobufSpan(s))
	}
	var block []byte
	block = appendBytes(block, fieldResource, protobufAttributes(nil, fieldResourceAttrs, resource(version)))
	block = appendBytes(block, fieldScopeSpans, scope)
	return appendBytes(nil, fieldResourceSpans, block)
}

func protobufSpan(s span) []byte {
	var out []byte
	out = appendBytes(out, fieldTraceID, s.traceID[:])
	out = appendBytes(out, fieldSpanID, s.spanID[:])
	out = appendBytes(out, fieldName, []byte(s.name))
	out = appendVarint(out, fieldKind, spanKindClient)
	out = appendFixed64(out, fieldStart, nanos(s.start))
	out = appendFixed64(out, fieldEnd, nanos(s.end))
	return protobufAttributes(out, fieldSpanAttributes, s.attributes)
}

func protobufAttributes(out []byte, field int, attributes []attribute) []byte {
	for _, a := range attributes {
		pair := appendBytes(nil, fieldKey, []byte(a.key))
		out = appendBytes(out, field, appendBytes(pair, fieldValue, protobufValue(a)))
	}
	return out
}

func protobufValue(a attribute) []byte {
	switch a.kind {
	case kindInt:
		return appendVarint(nil, fieldInt, uint64(a.integer))
	case kindStrings:
		var array []byte
		for _, item := range a.list {
			array = appendBytes(array, fieldArrayValues, appendBytes(nil, fieldString, []byte(item)))
		}
		return appendBytes(nil, fieldArray, array)
	}
	return appendBytes(nil, fieldString, []byte(a.text))
}

func appendTag(out []byte, field, wire int) []byte {
	return binary.AppendUvarint(out, uint64(field)<<3|uint64(wire))
}

func appendBytes(out []byte, field int, data []byte) []byte {
	out = appendTag(out, field, wireBytes)
	out = binary.AppendUvarint(out, uint64(len(data)))
	return append(out, data...)
}

func appendVarint(out []byte, field int, value uint64) []byte {
	return binary.AppendUvarint(appendTag(out, field, wireVarint), value)
}

func appendFixed64(out []byte, field int, value uint64) []byte {
	return binary.LittleEndian.AppendUint64(appendTag(out, field, wireFixed64), value)
}
