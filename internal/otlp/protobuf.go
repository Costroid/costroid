package otlp

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
)

// Protobuf wire types.
const (
	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
	wireFixed32 = 5
)

// The OTLP field numbers this decoder reads, from opentelemetry-proto.
const (
	fieldResourceBlock = 1
	fieldResource      = 1
	fieldScopeBlock    = 2
	fieldScope         = 1
	fieldRecord        = 2
	fieldAttributes    = 1

	fieldSpanTraceID    = 1
	fieldSpanSpanID     = 2
	fieldSpanName       = 5
	fieldSpanStart      = 7
	fieldSpanEnd        = 8
	fieldSpanAttributes = 9
	fieldSpanEvents     = 11
	fieldSpanLinks      = 13
	fieldSpanStatus     = 15

	fieldEventAttributes = 3
	fieldLinkAttributes  = 4
	fieldStatusMessage   = 2

	fieldLogTime       = 1
	fieldLogBody       = 5
	fieldLogAttributes = 6
	fieldLogTraceID    = 9
	fieldLogSpanID     = 10
	fieldLogObserved   = 11
	fieldLogEventName  = 12

	fieldKey   = 1
	fieldValue = 2

	fieldAnyString = 1
	fieldAnyBool   = 2
	fieldAnyInt    = 3
	fieldAnyDouble = 4
)

// field is one decoded protobuf field. Scalars land in bits, and
// length-delimited fields in data, which aliases the input.
type field struct {
	number int
	wire   int
	bits   uint64
	data   []byte
}

// walk calls fn for each field of a message. It never recurses, so nesting
// cannot exhaust the stack: decoders descend only into the shapes they name.
func walk(message []byte, fn func(field) error) error {
	for len(message) > 0 {
		tag, n := binary.Uvarint(message)
		if n <= 0 {
			return fmt.Errorf("%w: bad tag", ErrMalformed)
		}
		message = message[n:]
		current := field{number: int(tag >> 3), wire: int(tag & 7)}
		rest, err := current.read(message)
		if err != nil {
			return err
		}
		message = rest
		if err := fn(current); err != nil {
			return err
		}
	}
	return nil
}

func (f *field) read(message []byte) ([]byte, error) {
	switch f.wire {
	case wireVarint:
		value, n := binary.Uvarint(message)
		if n <= 0 {
			return nil, fmt.Errorf("%w: bad varint", ErrMalformed)
		}
		f.bits = value
		return message[n:], nil
	case wireFixed64:
		if len(message) < 8 {
			return nil, fmt.Errorf("%w: short fixed64", ErrMalformed)
		}
		f.bits = binary.LittleEndian.Uint64(message)
		return message[8:], nil
	case wireFixed32:
		if len(message) < 4 {
			return nil, fmt.Errorf("%w: short fixed32", ErrMalformed)
		}
		return message[4:], nil
	case wireBytes:
		size, n := binary.Uvarint(message)
		if n <= 0 || size > uint64(len(message)-n) {
			return nil, fmt.Errorf("%w: bad length", ErrMalformed)
		}
		end := n + int(size)
		f.data = message[n:end]
		return message[end:], nil
	}
	return nil, fmt.Errorf("%w: wire type %d", ErrMalformed, f.wire)
}

// isBytes reports whether f is the length-delimited field number.
func (f field) isBytes(number int) bool {
	return f.number == number && f.wire == wireBytes
}

func decodeProtobuf(body []byte, signal Signal, c *collector) ([]record, error) {
	var records []record
	decoder := func(message []byte, shared attributes) error {
		var entry record
		var err error
		if signal == Logs {
			entry, err = c.protoLog(message, shared)
		} else {
			entry, err = c.protoSpan(message, shared)
		}
		records = append(records, entry)
		return err
	}
	err := walk(body, func(f field) error {
		if !f.isBytes(fieldResourceBlock) {
			return nil
		}
		return c.protoResourceBlock(f.data, decoder)
	})
	if err != nil {
		return nil, err
	}
	return records, nil
}

// protoResourceBlock reads ResourceSpans or ResourceLogs, which share a shape.
// The resource can follow the scopes on the wire, so scopes wait for it.
func (c *collector) protoResourceBlock(message []byte, decode func([]byte, attributes) error) error {
	shared := attributes{}
	var scopes [][]byte
	err := walk(message, func(f field) error {
		switch {
		case f.isBytes(fieldResource):
			return c.protoAttributeList(f.data, fieldAttributes, shared)
		case f.isBytes(fieldScopeBlock):
			scopes = append(scopes, f.data)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, scope := range scopes {
		err := walk(scope, func(f field) error {
			switch {
			case f.isBytes(fieldScope):
				return c.protoDiscardScope(f.data)
			case f.isBytes(fieldRecord):
				return decode(f.data, shared)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// protoAttributeList reads the repeated KeyValue at number in a message.
func (c *collector) protoAttributeList(message []byte, number int, into attributes) error {
	return walk(message, func(f field) error {
		if f.isBytes(number) {
			return c.protoKeyValue(f.data, into)
		}
		return nil
	})
}

// instrumentationScope.attributes is field 3.
const fieldScopeAttributes = 3

func (c *collector) protoDiscardScope(message []byte) error {
	return c.protoDiscardList(message, fieldScopeAttributes)
}

func (c *collector) protoDiscardList(message []byte, number int) error {
	return walk(message, func(f field) error {
		if !f.isBytes(number) {
			return nil
		}
		key, _, _, err := protoKeyValueParts(f.data)
		if err != nil {
			return err
		}
		c.discard(key)
		return nil
	})
}

func (c *collector) protoKeyValue(message []byte, into attributes) error {
	key, text, scalar, err := protoKeyValueParts(message)
	if err != nil {
		return err
	}
	c.keep(into, key, text, scalar)
	return nil
}

func protoKeyValueParts(message []byte) (string, string, bool, error) {
	var key, text string
	var scalar bool
	err := walk(message, func(f field) error {
		var err error
		switch {
		case f.isBytes(fieldKey):
			key = string(f.data)
		case f.isBytes(fieldValue):
			text, scalar, err = protoScalar(f.data)
		}
		return err
	})
	return key, text, scalar, err
}

// protoScalar reads an AnyValue. Arrays, maps and bytes come back non-scalar
// and are never descended into.
func protoScalar(message []byte) (string, bool, error) {
	var text string
	var scalar bool
	err := walk(message, func(f field) error {
		switch {
		case f.isBytes(fieldAnyString):
			text, scalar = string(f.data), true
		case f.number == fieldAnyBool && f.wire == wireVarint:
			text, scalar = strconv.FormatBool(f.bits != 0), true
		case f.number == fieldAnyInt && f.wire == wireVarint:
			text, scalar = strconv.FormatInt(int64(f.bits), 10), true
		case f.number == fieldAnyDouble && f.wire == wireFixed64:
			text, scalar = strconv.FormatFloat(math.Float64frombits(f.bits), 'f', -1, 64), true
		}
		return nil
	})
	return text, scalar, err
}

func (c *collector) protoSpan(message []byte, shared attributes) (record, error) {
	entry := record{}
	own := attributes{}
	err := walk(message, func(f field) error {
		switch {
		case f.isBytes(fieldSpanTraceID):
			entry.traceID = hex.EncodeToString(f.data)
		case f.isBytes(fieldSpanSpanID):
			entry.spanID = hex.EncodeToString(f.data)
		case f.isBytes(fieldSpanName):
			entry.name = string(f.data)
		case f.number == fieldSpanStart && f.wire == wireFixed64:
			entry.start = f.bits
		case f.number == fieldSpanEnd && f.wire == wireFixed64:
			entry.end = f.bits
		case f.isBytes(fieldSpanAttributes):
			return c.protoKeyValue(f.data, own)
		case f.isBytes(fieldSpanEvents):
			return c.protoDiscardList(f.data, fieldEventAttributes)
		case f.isBytes(fieldSpanLinks):
			return c.protoDiscardList(f.data, fieldLinkAttributes)
		case f.isBytes(fieldSpanStatus):
			return c.protoStatus(f.data)
		}
		return nil
	})
	entry.attrs = merged(shared, own)
	return entry, err
}

func (c *collector) protoStatus(message []byte) error {
	return walk(message, func(f field) error {
		if f.isBytes(fieldStatusMessage) && len(f.data) > 0 {
			c.drop(DropStatusMessage)
		}
		return nil
	})
}

func (c *collector) protoLog(message []byte, shared attributes) (record, error) {
	entry := record{log: true}
	own := attributes{}
	var body string
	var observed uint64
	var bodyPresent bool
	err := walk(message, func(f field) error {
		switch {
		case f.number == fieldLogTime && f.wire == wireFixed64:
			entry.start = f.bits
		case f.number == fieldLogObserved && f.wire == wireFixed64:
			observed = f.bits
		case f.isBytes(fieldLogTraceID):
			entry.traceID = hex.EncodeToString(f.data)
		case f.isBytes(fieldLogSpanID):
			entry.spanID = hex.EncodeToString(f.data)
		case f.isBytes(fieldLogEventName):
			entry.name = string(f.data)
		case f.isBytes(fieldLogAttributes):
			return c.protoKeyValue(f.data, own)
		case f.isBytes(fieldLogBody):
			bodyPresent = len(f.data) > 0
			text, _, err := protoScalar(f.data)
			body = text
			return err
		}
		return nil
	})
	if entry.start == 0 {
		entry.start = observed
	}
	entry.end = entry.start
	if entry.name == "" && eventNames[body] {
		entry.name = body
	} else if bodyPresent && body != entry.name {
		c.drop(DropLogBody)
	}
	entry.attrs = merged(shared, own)
	return entry, err
}
