// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package contentsafety holds the pure, stdlib-only machinery the Cardinal-Rule
// (decision D7) Layer-2 CI gate is built from: a reflection walker that
// enumerates every JSON field a set of decode structs can read, a curated
// forbidden-content scan, a request-path allowlist comparator, and a source
// enumerator that finds the AI connectors sharing the aiwire wire chokepoint.
//
// It is deliberately a LEAF: it imports only the standard library, never
// testing, and never any connector package (importing a connector would create
// an import cycle, since the connectors' own package tests are the callers).
// Every function here is pure; the t.Errorf/t.Fatalf assertions that turn a
// mismatch into a red build live in the callers' _test.go files. Layer 1 (the
// structural wire chokepoint) lives in internal/ingest/aiwire; this package is
// the committed, reviewer-gated allowlist that sits on top of it.
package contentsafety

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// rawMessageType is json.RawMessage's reflect.Type, treated as an opaque leaf by
// FieldSet (it is the ContentHash carrier for the connectors' raw data envelope
// elements, so it must never be recursed into).
var rawMessageType = reflect.TypeOf(json.RawMessage(nil))

// FieldSet walks every JSON-decodable field reachable from roots and returns the
// sorted, unique set of json field names plus the count of distinct struct types
// visited. It is the primary Cardinal-Rule allowlist guard: a caller pins
// FieldSet(<the connector's decode structs>) against a committed set, so any
// field added to or renamed on those structs turns the caller's test red until a
// reviewer updates the committed set (a visible diff).
//
// json.RawMessage is an opaque leaf (never recursed into, so a raw envelope
// element cannot smuggle its inner fields into the set), and json.Number is a
// scalar leaf (its field name is collected, but it is not descended into).
// Pointers, slices, arrays, and maps are unwrapped to reach the struct types
// underneath; a map key is a string leaf. Each distinct struct reflect.Type is
// counted once, which also stops recursion on any cycle.
func FieldSet(roots ...reflect.Type) (names []string, structCount int) {
	seen := map[reflect.Type]bool{}
	set := map[string]bool{}
	for _, r := range roots {
		walkType(r, seen, set)
	}
	names = make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, len(seen)
}

// walkType recurses through t, collecting json field names into set and struct
// types into seen (which doubles as the cycle guard and the struct counter).
func walkType(t reflect.Type, seen map[reflect.Type]bool, set map[string]bool) {
	// json.RawMessage is checked BEFORE unwrapping slices: it is itself a
	// []byte, so a naive slice-unwrap would descend into it. It is opaque.
	if t == rawMessageType {
		return
	}
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		walkType(t.Elem(), seen, set)
	case reflect.Map:
		// The key is a string leaf (no name, no struct); only the value can
		// carry further modeled fields.
		walkType(t.Elem(), seen, set)
	case reflect.Struct:
		if seen[t] {
			return
		}
		seen[t] = true
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			// Skip unexported, non-embedded fields: json never decodes into
			// them, so they carry no decodable field name.
			if f.PkgPath != "" && !f.Anonymous {
				continue
			}
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue // json:"-" — the field is never decoded.
			}
			// An anonymous (embedded) field promotes its own fields; collect no
			// name for it, just recurse into its type.
			if !f.Anonymous {
				if name := jsonFieldName(tag, f.Name); name != "" {
					set[name] = true
				}
			}
			walkType(f.Type, seen, set)
		}
	default:
		// A scalar leaf (string, bool, int, json.Number, ...): the name was
		// collected by the parent; there is nothing to descend into.
	}
}

// jsonFieldName returns the wire name a field decodes from: the json tag's name
// (the text before the first comma) when present, otherwise the Go field name.
func jsonFieldName(tag, goName string) string {
	if tag == "" {
		return goName
	}
	if comma := strings.IndexByte(tag, ','); comma >= 0 {
		tag = tag[:comma]
	}
	if tag == "" {
		return goName // e.g. `json:",omitempty"` — no explicit name.
	}
	return tag
}

// Denylist is the curated set of UNAMBIGUOUS AI-content words that must never
// appear in a decoded field name. It deliberately EXCLUDES input/output/tool/
// function: those denote token COUNTS and tool COUNTS in cost/usage APIs (e.g.
// output_tokens, uncached_input_tokens, server_tool_use are legitimate metadata
// fields). The exact-match allowlist (FieldSet vs a committed set) is the
// PRIMARY guard; ScanForbidden is a secondary tripwire for the classic content
// field names. A future field that trips it is fail-safe: CI blocks until a
// reviewer makes a deliberate call, never a silent allow.
var Denylist = []string{
	"choices", "completion", "content", "conversation",
	"image_url", "message", "prompt", "text", "transcript",
}

// ScanForbidden returns every name in names that carries a Denylist word. Each
// name is tokenized on underscores and camelCase boundaries and lowercased: a
// single-word Denylist entry matches when it EXACTLY equals a token (so
// "context" is not flagged for containing the substring "text"), and a
// multi-word entry like image_url matches when the underscore-joined tokens
// contain it (so both image_url and imageURL are caught).
func ScanForbidden(names []string) []string {
	var out []string
	for _, name := range names {
		if forbidden(name) {
			out = append(out, name)
		}
	}
	return out
}

func forbidden(name string) bool {
	tokens := tokenize(name)
	joined := strings.Join(tokens, "_")
	for _, word := range Denylist {
		if strings.Contains(word, "_") {
			// A multi-word content term: match on the normalized, underscore-
			// joined token stream so both image_url and imageURL are caught.
			if strings.Contains(joined, word) {
				return true
			}
			continue
		}
		// A single-word term: exact token match, never a substring, so a token
		// like "context" (which contains "text") is not a false positive.
		for _, tok := range tokens {
			if tok == word {
				return true
			}
		}
	}
	return false
}

// tokenize splits name on underscores and camelCase boundaries and lowercases
// each token, so "output_tokens" -> [output tokens] and "imageURL" ->
// [image url].
func tokenize(name string) []string {
	var tokens []string
	for _, part := range strings.Split(name, "_") {
		tokens = append(tokens, splitCamel(part)...)
	}
	for i := range tokens {
		tokens[i] = strings.ToLower(tokens[i])
	}
	return tokens
}

// splitCamel breaks a single underscore-free segment at each lower-to-upper
// boundary ("imageURL" -> ["image", "URL"]).
func splitCamel(s string) []string {
	if s == "" {
		return nil
	}
	var (
		out   []string
		start int
		runes = []rune(s)
	)
	for i := 1; i < len(runes); i++ {
		if isUpper(runes[i]) && !isUpper(runes[i-1]) {
			out = append(out, string(runes[start:i]))
			start = i
		}
	}
	return append(out, string(runes[start:]))
}

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }

// ForbiddenPaths returns every path in observed that is not present in allowed
// (exact string match). It is the shared comparator both connectors' request-
// path allowlist tests use; the caller turns each returned path into a t.Errorf.
func ForbiddenPaths(observed, allowed []string) []string {
	allow := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		allow[a] = true
	}
	var out []string
	for _, p := range observed {
		if !allow[p] {
			out = append(out, p)
		}
	}
	return out
}

// aiwireImportPath is the wire chokepoint whose importers are the gated AI
// connectors.
const aiwireImportPath = "github.com/Costroid/costroid/internal/ingest/aiwire"

// AIConnectorsImportingAiwire returns the sorted base names of the immediate
// sub-directories of ingestDir whose NON-test .go files import the aiwire wire
// chokepoint. It uses only go/parser in ImportsOnly mode (never the deprecated
// parser.ParseDir), so it adds no module dependency and never imports a
// connector package. The set it returns is pinned by a caller's test, making any
// new connector that routes through aiwire a mandatory, reviewer-visible
// acknowledgement rather than a silent addition.
func AIConnectorsImportingAiwire(ingestDir string) ([]string, error) {
	entries, err := os.ReadDir(ingestDir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sub := filepath.Join(ingestDir, entry.Name())
		imports, err := dirImportsAiwire(sub)
		if err != nil {
			return nil, err
		}
		if imports {
			out = append(out, entry.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// dirImportsAiwire reports whether any non-test .go file directly in dir imports
// the aiwire chokepoint.
func dirImportsAiwire(dir string) (bool, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	fset := token.NewFileSet()
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		name := file.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ImportsOnly)
		if err != nil {
			return false, err
		}
		for _, spec := range parsed.Imports {
			// The AST stores the import path WITH its quotes; unquote before
			// comparing or the match never fires.
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				continue
			}
			if path == aiwireImportPath {
				return true, nil
			}
		}
	}
	return false, nil
}
