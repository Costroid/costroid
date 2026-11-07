package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

type kind int

const (
	kindObject kind = iota
	kindArray
	kindString
	kindLiteral
)

// value is one JSON value that keeps its keys in order and its scalars as
// written, so a file that needs no change encodes back byte for byte.
type value struct {
	kind  kind
	keys  []string
	items []*value
	raw   string // a string's or a literal's text as written
	text  string // a string's decoded text
}

func stringValue(text string) *value {
	encoded, _ := json.Marshal(text)
	return &value{kind: kindString, raw: string(encoded), text: text}
}

func (v *value) field(key string) *value {
	if v == nil || v.kind != kindObject {
		return nil
	}
	for i, k := range v.keys {
		if k == key {
			return v.items[i]
		}
	}
	return nil
}

// fieldText is a string field's text, or "" when the field is absent or not a string.
func (v *value) fieldText(key string) string {
	if f := v.field(key); f != nil && f.kind == kindString {
		return f.text
	}
	return ""
}

func parseValue(data []byte) (*value, error) {
	p := parser{data: data}
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	p.space()
	if p.at < len(p.data) {
		return nil, fmt.Errorf("unexpected %q after the value at byte %d", p.data[p.at], p.at)
	}
	return v, nil
}

type parser struct {
	data []byte
	at   int
}

func (p *parser) space() {
	for p.at < len(p.data) && strings.IndexByte(" \t\r\n", p.data[p.at]) >= 0 {
		p.at++
	}
}

func (p *parser) value() (*value, error) {
	p.space()
	if p.at >= len(p.data) {
		return nil, fmt.Errorf("the value ends early")
	}
	switch c := p.data[p.at]; {
	case c == '{':
		return p.object()
	case c == '[':
		return p.array()
	case c == '"':
		raw, text, err := p.str()
		return &value{kind: kindString, raw: raw, text: text}, err
	default:
		return p.literal()
	}
}

func (p *parser) object() (*value, error) {
	v := &value{kind: kindObject}
	p.at++
	p.space()
	if p.at < len(p.data) && p.data[p.at] == '}' {
		p.at++
		return v, nil
	}
	for {
		p.space()
		if p.at >= len(p.data) || p.data[p.at] != '"' {
			return nil, fmt.Errorf("expected a key at byte %d", p.at)
		}
		_, key, err := p.str()
		if err != nil {
			return nil, err
		}
		p.space()
		if p.at >= len(p.data) || p.data[p.at] != ':' {
			return nil, fmt.Errorf("expected ':' at byte %d", p.at)
		}
		p.at++
		item, err := p.value()
		if err != nil {
			return nil, err
		}
		v.keys = append(v.keys, key)
		v.items = append(v.items, item)
		if done, err := p.next('}'); done || err != nil {
			return v, err
		}
	}
}

func (p *parser) array() (*value, error) {
	v := &value{kind: kindArray}
	p.at++
	p.space()
	if p.at < len(p.data) && p.data[p.at] == ']' {
		p.at++
		return v, nil
	}
	for {
		item, err := p.value()
		if err != nil {
			return nil, err
		}
		v.items = append(v.items, item)
		if done, err := p.next(']'); done || err != nil {
			return v, err
		}
	}
}

// next reads the ',' between members or the closing byte.
func (p *parser) next(closing byte) (bool, error) {
	p.space()
	if p.at >= len(p.data) {
		return false, fmt.Errorf("the value ends early")
	}
	switch p.data[p.at] {
	case ',':
		p.at++
		return false, nil
	case closing:
		p.at++
		return true, nil
	}
	return false, fmt.Errorf("unexpected %q at byte %d", p.data[p.at], p.at)
}

func (p *parser) str() (string, string, error) {
	start := p.at
	p.at++
	for p.at < len(p.data) {
		switch p.data[p.at] {
		case '\\':
			p.at += 2
			continue
		case '"':
			p.at++
			raw := string(p.data[start:p.at])
			var text string
			if err := json.Unmarshal([]byte(raw), &text); err != nil {
				return "", "", fmt.Errorf("a string at byte %d: %w", start, err)
			}
			return raw, text, nil
		}
		p.at++
	}
	return "", "", fmt.Errorf("a string at byte %d never ends", start)
}

func (p *parser) literal() (*value, error) {
	start := p.at
	for p.at < len(p.data) && strings.IndexByte(",]} \t\r\n", p.data[p.at]) < 0 {
		p.at++
	}
	raw := string(p.data[start:p.at])
	if !json.Valid([]byte(raw)) {
		return nil, fmt.Errorf("%q at byte %d is not a JSON value", raw, start)
	}
	return &value{kind: kindLiteral, raw: raw}, nil
}

// isNumber says whether a literal is a number rather than true, false or null.
func (v *value) isNumber() bool {
	return v.kind == kindLiteral && v.raw != "true" && v.raw != "false" && v.raw != "null"
}

// compact writes v on one line, as the JSON Lines files hold it.
func (v *value) compact(out *bytes.Buffer) {
	switch v.kind {
	case kindObject:
		out.WriteByte('{')
		for i, key := range v.keys {
			if i > 0 {
				out.WriteByte(',')
			}
			out.WriteString(stringValue(key).raw)
			out.WriteByte(':')
			v.items[i].compact(out)
		}
		out.WriteByte('}')
	case kindArray:
		out.WriteByte('[')
		for i, item := range v.items {
			if i > 0 {
				out.WriteByte(',')
			}
			item.compact(out)
		}
		out.WriteByte(']')
	default:
		out.WriteString(v.raw)
	}
}

// indent writes v with two spaces per level, as an execution file holds it.
func (v *value) indent(out *bytes.Buffer, depth int) {
	pad := func(d int) { out.WriteString("\n" + strings.Repeat("  ", d)) }
	switch {
	case v.kind == kindObject && len(v.keys) > 0:
		out.WriteByte('{')
		for i, key := range v.keys {
			if i > 0 {
				out.WriteByte(',')
			}
			pad(depth + 1)
			out.WriteString(stringValue(key).raw)
			out.WriteString(": ")
			v.items[i].indent(out, depth+1)
		}
		pad(depth)
		out.WriteByte('}')
	case v.kind == kindArray && len(v.items) > 0:
		out.WriteByte('[')
		for i, item := range v.items {
			if i > 0 {
				out.WriteByte(',')
			}
			pad(depth + 1)
			item.indent(out, depth+1)
		}
		pad(depth)
		out.WriteByte(']')
	default:
		v.compact(out)
	}
}
