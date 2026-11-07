package otlp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

type jsonKeyValue struct {
	Key   string    `json:"key"`
	Value jsonValue `json:"value"`
}

// jsonValue is the OTLP/JSON AnyValue. Int64 arrives as a string, and some
// senders write a bare number.
type jsonValue struct {
	String *string         `json:"stringValue"`
	Bool   *bool           `json:"boolValue"`
	Int    json.RawMessage `json:"intValue"`
	Double json.RawMessage `json:"doubleValue"`
	Array  json.RawMessage `json:"arrayValue"`
	Map    json.RawMessage `json:"kvlistValue"`
	Bytes  json.RawMessage `json:"bytesValue"`
}

func (v jsonValue) scalar() (string, bool) {
	switch {
	case v.String != nil:
		return *v.String, true
	case v.Bool != nil:
		return strconv.FormatBool(*v.Bool), true
	case len(v.Int) > 0:
		return string(bytes.Trim(v.Int, `"`)), true
	case len(v.Double) > 0:
		return string(bytes.Trim(v.Double, `"`)), true
	}
	return "", false
}

// present reports whether the value holds anything at all.
func (v jsonValue) present() bool {
	text, _ := v.scalar()
	return text != "" || v.Array != nil || v.Map != nil || v.Bytes != nil
}

type jsonResource struct {
	Attributes []jsonKeyValue `json:"attributes"`
}

type jsonScope struct {
	Attributes []jsonKeyValue `json:"attributes"`
}

type jsonEvent struct {
	Attributes []jsonKeyValue `json:"attributes"`
}

type jsonSpan struct {
	TraceID    string         `json:"traceId"`
	SpanID     string         `json:"spanId"`
	Name       string         `json:"name"`
	Start      jsonNano       `json:"startTimeUnixNano"`
	End        jsonNano       `json:"endTimeUnixNano"`
	Attributes []jsonKeyValue `json:"attributes"`
	Events     []jsonEvent    `json:"events"`
	Links      []jsonEvent    `json:"links"`
	Status     struct {
		Message string `json:"message"`
	} `json:"status"`
}

type jsonLog struct {
	Time       jsonNano       `json:"timeUnixNano"`
	Observed   jsonNano       `json:"observedTimeUnixNano"`
	EventName  string         `json:"eventName"`
	TraceID    string         `json:"traceId"`
	SpanID     string         `json:"spanId"`
	Body       jsonBody       `json:"body"`
	Attributes []jsonKeyValue `json:"attributes"`
}

// jsonBody is a log body. OTLP/JSON writes an AnyValue, and some senders
// write a bare string, which reads as that string.
type jsonBody struct {
	jsonValue
}

// UnmarshalJSON accepts an AnyValue object or a bare string.
func (b *jsonBody) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return err
		}
		b.String = &text
		return nil
	}
	return json.Unmarshal(trimmed, &b.jsonValue)
}

// jsonNano reads a fixed64 timestamp, a string in OTLP/JSON.
type jsonNano uint64

// UnmarshalJSON accepts the string form and a bare number.
func (n *jsonNano) UnmarshalJSON(data []byte) error {
	text := string(bytes.Trim(data, `"`))
	if text == "" || text == "null" {
		return nil
	}
	value, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return err
	}
	*n = jsonNano(value)
	return nil
}

// The envelopes hold each record undecoded. A record decodes on its own, so
// one that does not is rejected and counted, and its siblings still read.
type jsonTraces struct {
	ResourceSpans []struct {
		Resource   jsonResource `json:"resource"`
		ScopeSpans []struct {
			Scope jsonScope         `json:"scope"`
			Spans []json.RawMessage `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"resourceSpans"`
}

type jsonLogs struct {
	ResourceLogs []struct {
		Resource  jsonResource `json:"resource"`
		ScopeLogs []struct {
			Scope      jsonScope         `json:"scope"`
			LogRecords []json.RawMessage `json:"logRecords"`
		} `json:"scopeLogs"`
	} `json:"resourceLogs"`
}

func decodeJSON(body []byte, signal Signal, c *collector) ([]record, error) {
	if signal == Logs {
		return decodeJSONLogs(body, c)
	}
	return decodeJSONTraces(body, c)
}

func decodeJSONTraces(body []byte, c *collector) ([]record, error) {
	var payload jsonTraces
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	var records []record
	for _, resource := range payload.ResourceSpans {
		shared := c.jsonAttributes(resource.Resource.Attributes)
		for _, scope := range resource.ScopeSpans {
			c.jsonDiscard(scope.Scope.Attributes)
			for _, raw := range scope.Spans {
				var span jsonSpan
				if err := json.Unmarshal(raw, &span); err != nil {
					c.rejected++
					continue
				}
				records = append(records, c.jsonSpanRecord(&span, shared))
			}
		}
	}
	return records, nil
}

func (c *collector) jsonSpanRecord(span *jsonSpan, shared attributes) record {
	for _, part := range [][]jsonEvent{span.Events, span.Links} {
		for _, event := range part {
			c.jsonDiscard(event.Attributes)
		}
	}
	if span.Status.Message != "" {
		c.drop(DropStatusMessage)
	}
	return record{
		attrs:   merged(shared, c.jsonAttributes(span.Attributes)),
		name:    span.Name,
		start:   uint64(span.Start),
		end:     uint64(span.End),
		traceID: span.TraceID,
		spanID:  span.SpanID,
	}
}

func decodeJSONLogs(body []byte, c *collector) ([]record, error) {
	var payload jsonLogs
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	var records []record
	for _, resource := range payload.ResourceLogs {
		shared := c.jsonAttributes(resource.Resource.Attributes)
		for _, scope := range resource.ScopeLogs {
			c.jsonDiscard(scope.Scope.Attributes)
			for _, raw := range scope.LogRecords {
				var entry jsonLog
				if err := json.Unmarshal(raw, &entry); err != nil {
					c.rejected++
					continue
				}
				records = append(records, c.jsonLogRecord(&entry, shared))
			}
		}
	}
	return records, nil
}

func (c *collector) jsonLogRecord(entry *jsonLog, shared attributes) record {
	name := entry.EventName
	text, _ := entry.Body.scalar()
	if name == "" && eventNames[text] {
		name = text
	} else if entry.Body.present() && text != name {
		c.drop(DropLogBody)
	}
	at := uint64(entry.Time)
	if at == 0 {
		at = uint64(entry.Observed)
	}
	return record{
		attrs:   merged(shared, c.jsonAttributes(entry.Attributes)),
		name:    name,
		start:   at,
		end:     at,
		traceID: entry.TraceID,
		spanID:  entry.SpanID,
		log:     true,
	}
}

func (c *collector) jsonAttributes(list []jsonKeyValue) attributes {
	kept := make(attributes, len(list))
	for _, kv := range list {
		text, scalar := kv.Value.scalar()
		c.keep(kept, kv.Key, text, scalar)
	}
	return kept
}

func (c *collector) jsonDiscard(list []jsonKeyValue) {
	for _, kv := range list {
		c.discard(kv.Key)
	}
}
