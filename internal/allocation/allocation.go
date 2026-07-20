// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package allocation models Costroid's query-time cost allocation ("virtual
// tagging", decision D18b): an ordered set of rules that maps each stored cost
// row to exactly one allocation label at QUERY time. This package owns the
// rules-file MODEL, PARSING, and VALIDATION only — it builds no SQL (the
// storage package compiles the validated dimension into a bound CASE) and it
// reads no store.
//
// # Cardinal Rule posture (decision D7)
//
// Allocation rules match stored cost METADATA only — the FOCUS columns and
// tags already persisted at ingest. This package ingests, stores, caches, and
// logs nothing new: it parses a small operator-declared JSON file and returns a
// validated in-memory model. No prompt/response content ever flows through it.
//
// # Matching semantics
//
// All matching is case-sensitive and byte-exact (FOCUS values are canonical):
// "prod" does not match "Prod". Rules are evaluated top-down, first-match-wins;
// cost matching no rule lands in the reserved UnallocatedLabel bucket.
package allocation

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// UnallocatedLabel is the reserved allocation label for cost that matches no
// rule. It is bound as data by the storage compiler, never interpolated, and no
// rule may claim it (validation rejects a rule whose label equals it).
const UnallocatedLabel = "Unallocated"

// Operator names — the closed set of condition operators.
const (
	// OpEquals matches when the dimension value equals the condition value
	// exactly (byte-for-byte, case-sensitive).
	OpEquals = "equals"
	// OpContains matches when the dimension value contains the condition value
	// as a literal substring (no wildcard/escape semantics).
	OpContains = "contains"
	// OpStartsWith matches when the dimension value has the condition value as
	// a literal prefix.
	OpStartsWith = "starts_with"
	// OpOneOf matches when the dimension value equals any of the condition
	// values.
	OpOneOf = "one_of"
	// OpExists matches when the dimension is present (for a tag: the key is
	// present with a non-null JSON value, including an empty string; for a
	// column: the column is non-NULL and non-empty-string).
	OpExists = "exists"
)

// tagPrefix marks a condition dimension as a FOCUS tag lookup: the text after
// it is the tag key (matched literally, never as a JSONPath/pointer/wildcard).
const tagPrefix = "tag:"

// columnDimensions is the CLOSED set of column-backed condition dimensions — a
// DELIBERATE subset of the cost_records columns (migration 0001). Other stored
// columns (billing_account_name, charge_class, availability_zone, sku_price_id,
// …) are excluded on purpose and may be opened by a later slice; unstored FOCUS
// dimensions (commitment-discount, capacity-reservation, pricing-currency,
// Allocated*) are never accepted. The names double as the storage column
// literals, so the storage package cross-checks its own map against this set.
var columnDimensions = map[string]struct{}{
	"billing_account_id":    {},
	"sub_account_id":        {},
	"sub_account_name":      {},
	"service_provider_name": {},
	"service_name":          {},
	"service_category":      {},
	"service_subcategory":   {},
	"charge_category":       {},
	"charge_description":    {},
	"region_id":             {},
	"region_name":           {},
	"resource_id":           {},
	"resource_name":         {},
	"resource_type":         {},
	"sku_id":                {},
}

// Config is the parsed allocation rules document. The "dimensions" array is
// deliberately plural-ready for a later slice, but this slice REQUIRES exactly
// one entry (validation rejects zero or two-or-more).
type Config struct {
	Dimensions []Dimension `json:"dimensions"`
}

// Dimension is one allocation dimension: a name (reaches error messages and the
// UI, never SQL) and an ordered, first-match-wins rule list (which MAY be empty
// — every row then falls to UnallocatedLabel).
type Dimension struct {
	Name  string `json:"name"`
	Rules []Rule `json:"rules"`
}

// Rule maps cost matching all of its AND-combined conditions to Label. Label is
// bound as data (never interpolated), so it carries no charset restriction
// beyond non-empty and not equal to UnallocatedLabel.
type Rule struct {
	Label string      `json:"label"`
	Match []Condition `json:"match"`
}

// Condition is one match predicate: a Dimension (a column name or "tag:<key>"),
// an Operator, and its operand(s). equals/contains/starts_with use Value;
// one_of uses Values; exists uses neither. Value is a pointer so a present-empty
// "value": "" is distinguishable from an absent one during validation.
type Condition struct {
	Dimension string   `json:"dimension"`
	Operator  string   `json:"operator"`
	Value     *string  `json:"value"`
	Values    []string `json:"values"`
}

// ColumnDimensions returns the closed set of column-backed dimension names,
// sorted. The storage package cross-checks its own column map against this set
// (a set-equality guard), and the returned slice is a fresh copy callers may
// not mutate into the package state.
func ColumnDimensions() []string {
	out := make([]string, 0, len(columnDimensions))
	for name := range columnDimensions {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// TagKey reports whether dimension is a tag lookup ("tag:<key>") and, if so,
// returns the bare key (everything after the "tag:" prefix). The storage
// compiler binds the returned key as a parameter to json_extract_string.
func TagKey(dimension string) (key string, ok bool) {
	return strings.CutPrefix(dimension, tagPrefix)
}

// allowedDimensions is the human-readable list of accepted dimension names for
// error messages: the sorted column set plus the tag form.
var allowedDimensions = strings.Join(append(ColumnDimensions(), "tag:<key>"), ", ")

// validate checks the whole document, returning the first actionable error.
func (c Config) validate() error {
	if len(c.Dimensions) != 1 {
		return fmt.Errorf("exactly one allocation dimension is supported, but the rules file declares %d", len(c.Dimensions))
	}
	return c.Dimensions[0].validate()
}

// dimensionNamePattern is the human-readable charset the dimension name must
// match (it reaches error messages and the UI, never SQL).
const dimensionNamePattern = "^[A-Za-z0-9_-]+$"

// validDimensionName reports whether s is a non-empty run of the allowed
// dimension-name characters ([A-Za-z0-9_-]).
func validDimensionName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

func (d Dimension) validate() error {
	if d.Name == "" {
		return fmt.Errorf("allocation dimension name is empty; it must be non-empty and match %s", dimensionNamePattern)
	}
	if !validDimensionName(d.Name) {
		return fmt.Errorf("allocation dimension name %q is invalid; it must match %s", d.Name, dimensionNamePattern)
	}
	for i, rule := range d.Rules {
		if err := rule.validate(); err != nil {
			return fmt.Errorf("rule %d: %w", i+1, err)
		}
	}
	return nil
}

func (r Rule) validate() error {
	if r.Label == "" {
		return errors.New("rule label is empty; every rule needs a non-empty label")
	}
	if r.Label == UnallocatedLabel {
		return fmt.Errorf("%q is reserved for unmatched cost; give the rule a different label", UnallocatedLabel)
	}
	if len(r.Match) == 0 {
		return fmt.Errorf("rule %q has no match conditions; a rule needs at least one condition (the Unallocated bucket catches everything else)", r.Label)
	}
	for i, cond := range r.Match {
		if err := cond.validate(); err != nil {
			return fmt.Errorf("rule %q condition %d: %w", r.Label, i+1, err)
		}
	}
	return nil
}

func (c Condition) validate() error {
	if err := validateDimension(c.Dimension); err != nil {
		return err
	}
	hasValue := c.Value != nil
	hasValues := c.Values != nil
	switch c.Operator {
	case OpEquals, OpContains, OpStartsWith:
		if hasValues {
			return fmt.Errorf("operator %q takes \"value\", not \"values\"", c.Operator)
		}
		if !hasValue || *c.Value == "" {
			return fmt.Errorf("operator %q requires a non-empty \"value\"", c.Operator)
		}
	case OpOneOf:
		if hasValue {
			return fmt.Errorf("operator %q takes \"values\", not \"value\"", OpOneOf)
		}
		if len(c.Values) == 0 {
			return fmt.Errorf("operator %q requires a non-empty \"values\" array", OpOneOf)
		}
		for _, v := range c.Values {
			if v == "" {
				return fmt.Errorf("operator %q \"values\" must not contain an empty string", OpOneOf)
			}
		}
	case OpExists:
		if hasValue || hasValues {
			return fmt.Errorf("operator %q takes neither \"value\" nor \"values\"", OpExists)
		}
	case "":
		return errors.New("condition operator is empty; allowed operators are equals, contains, starts_with, one_of, exists")
	default:
		return fmt.Errorf("unknown operator %q; allowed operators are equals, contains, starts_with, one_of, exists", c.Operator)
	}
	return nil
}

// validateDimension enforces the closed dimension set and, for tag dimensions,
// the tag-key safety rules (rejecting keys DuckDB would reinterpret).
func validateDimension(dimension string) error {
	if dimension == "" {
		return fmt.Errorf("condition dimension is empty; allowed dimensions are %s", allowedDimensions)
	}
	if key, ok := TagKey(dimension); ok {
		return validateTagKey(key)
	}
	if _, ok := columnDimensions[dimension]; !ok {
		return fmt.Errorf("unknown condition dimension %q; allowed dimensions are %s", dimension, allowedDimensions)
	}
	return nil
}

// validateTagKey rejects tag keys DuckDB's json_extract_string would treat as
// something other than a literal key (verified against the pinned engine),
// because each would silently misroute money or fail at execution:
//   - a "$" prefix is parsed as a JSONPath expression;
//   - a "/" prefix is parsed as a JSON pointer ("tag:/team" would match the key
//     "team", never the literal key "/team");
//   - "*" and "**" are JSON wildcards ("*" makes the compiled CASE fail with a
//     type conversion error at execution).
//
// Dotted keys like "a.b" and colon-bearing keys are SAFE (bare-key extraction
// treats them literally) and are accepted.
func validateTagKey(key string) error {
	switch {
	case key == "":
		return errors.New("tag dimension has an empty key; use tag:<key>")
	case strings.HasPrefix(key, "$"):
		return fmt.Errorf("tag key %q begins with '$', which DuckDB interprets as a JSONPath expression, not a literal key; drop the '$'", key)
	case strings.HasPrefix(key, "/"):
		return fmt.Errorf("tag key %q begins with '/', which DuckDB interprets as a JSON pointer (it would match the key without the leading slash); use the literal key", key)
	case key == "*" || key == "**":
		return fmt.Errorf("tag key %q is a DuckDB JSON wildcard, not a literal key", key)
	}
	return nil
}

// ValidateTagKey validates a FOCUS Tags key for literal DuckDB extraction.
func ValidateTagKey(key string) error {
	return validateTagKey(key)
}
