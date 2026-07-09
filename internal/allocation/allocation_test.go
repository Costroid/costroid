// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package allocation

import (
	"strings"
	"testing"
)

// TestParseValid parses a representative multi-rule document and asserts the
// whole model round-trips: names, ordered rules, per-operator operands, and the
// present/absent distinction on Value (a pointer) and Values (a slice).
func TestParseValid(t *testing.T) {
	const doc = `{
	  "dimensions": [
	    {
	      "name": "team",
	      "rules": [
	        {"label": "platform", "match": [
	          {"dimension": "service_name", "operator": "starts_with", "value": "Amazon EC2"},
	          {"dimension": "tag:env", "operator": "equals", "value": "prod"}
	        ]},
	        {"label": "data", "match": [
	          {"dimension": "service_category", "operator": "one_of", "values": ["Analytics", "Databases"]}
	        ]},
	        {"label": "tagged", "match": [
	          {"dimension": "tag:owner", "operator": "exists"}
	        ]}
	      ]
	    }
	  ]
	}`
	dim, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if dim.Name != "team" {
		t.Errorf("dimension name = %q, want team", dim.Name)
	}
	if len(dim.Rules) != 3 {
		t.Fatalf("rules = %d, want 3", len(dim.Rules))
	}

	r0 := dim.Rules[0]
	if r0.Label != "platform" || len(r0.Match) != 2 {
		t.Fatalf("rule 0 = %+v, want platform with 2 conditions", r0)
	}
	if c := r0.Match[0]; c.Dimension != "service_name" || c.Operator != OpStartsWith || c.Value == nil || *c.Value != "Amazon EC2" {
		t.Errorf("rule 0 cond 0 = %+v, want service_name starts_with Amazon EC2", c)
	}
	if c := r0.Match[1]; c.Dimension != "tag:env" || c.Operator != OpEquals || c.Value == nil || *c.Value != "prod" {
		t.Errorf("rule 0 cond 1 = %+v, want tag:env equals prod", c)
	}

	r1 := dim.Rules[1]
	if c := r1.Match[0]; c.Operator != OpOneOf || c.Value != nil || len(c.Values) != 2 || c.Values[0] != "Analytics" || c.Values[1] != "Databases" {
		t.Errorf("rule 1 cond 0 = %+v, want one_of [Analytics Databases] and nil value", c)
	}

	r2 := dim.Rules[2]
	if c := r2.Match[0]; c.Operator != OpExists || c.Value != nil || c.Values != nil {
		t.Errorf("rule 2 cond 0 = %+v, want exists with neither value nor values", c)
	}
}

// TestParseEmptyRulesValid pins the degenerate-but-valid form: zero rules is
// accepted (everything falls to Unallocated at query time).
func TestParseEmptyRulesValid(t *testing.T) {
	dim, err := Parse(strings.NewReader(`{"dimensions":[{"name":"team","rules":[]}]}`))
	if err != nil {
		t.Fatalf("Parse (empty rules): %v", err)
	}
	if dim.Name != "team" || len(dim.Rules) != 0 {
		t.Errorf("dim = %+v, want name team with 0 rules", dim)
	}
}

// TestParseAcceptsSafeTagKeys pins that dotted and colon-bearing tag keys (bare
// keys DuckDB extracts literally — verified) are accepted.
func TestParseAcceptsSafeTagKeys(t *testing.T) {
	for _, key := range []string{"tag:a.b", "tag:user:team", "tag:cost-center", "tag:Env"} {
		doc := `{"dimensions":[{"name":"team","rules":[{"label":"x","match":[{"dimension":"` + key + `","operator":"exists"}]}]}]}`
		if _, err := Parse(strings.NewReader(doc)); err != nil {
			t.Errorf("Parse rejected safe tag dimension %q: %v", key, err)
		}
	}
}

// TestParseCaseInsensitiveFieldNames documents (not fights) stdlib behavior:
// a case-variant of a correct field name is accepted, while a MISSPELLED field
// still fails via DisallowUnknownFields (covered separately below).
func TestParseCaseInsensitiveFieldNames(t *testing.T) {
	doc := `{"dimensions":[{"Name":"team","Rules":[{"Label":"x","Match":[{"Dimension":"service_name","Operator":"equals","Value":"AWS Lambda"}]}]}]}`
	dim, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse with case-variant field names: %v", err)
	}
	if dim.Name != "team" || len(dim.Rules) != 1 || dim.Rules[0].Match[0].Value == nil || *dim.Rules[0].Match[0].Value != "AWS Lambda" {
		t.Errorf("case-variant parse = %+v, want the same model", dim)
	}
}

// TestParseErrors is the table-driven proof that every validation-error class
// and every parse-error class produces an actionable, field-naming message.
func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want []string // every substring must be present
	}{
		// --- document shape ---
		{"zero dimensions", `{"dimensions":[]}`, []string{"exactly one allocation dimension is supported", "0"}},
		{"two dimensions", `{"dimensions":[{"name":"a","rules":[]},{"name":"b","rules":[]}]}`, []string{"exactly one allocation dimension is supported", "2"}},
		{"empty file", ``, []string{"empty"}},
		{"non-JSON", `not json at all`, []string{"parsing allocation rules"}},
		{"trailing data", `{"dimensions":[{"name":"a","rules":[]}]}{"x":1}`, []string{"trailing data after the rules object"}},
		{"unknown field (operater)", `{"dimensions":[{"name":"t","rules":[{"label":"x","match":[{"dimension":"service_name","operater":"equals","value":"y"}]}]}]}`, []string{"operater"}},

		// --- dimension name ---
		{"empty dimension name", `{"dimensions":[{"name":"","rules":[]}]}`, []string{"dimension name is empty"}},
		{"invalid dimension name", `{"dimensions":[{"name":"team space","rules":[]}]}`, []string{`"team space"`, "invalid", "[A-Za-z0-9_-]"}},

		// --- rule shape ---
		{"empty rule label", `{"dimensions":[{"name":"t","rules":[{"label":"","match":[{"dimension":"service_name","operator":"exists"}]}]}]}`, []string{"rule label is empty"}},
		{"reserved label", `{"dimensions":[{"name":"t","rules":[{"label":"Unallocated","match":[{"dimension":"service_name","operator":"exists"}]}]}]}`, []string{"Unallocated", "reserved"}},
		{"no conditions", `{"dimensions":[{"name":"t","rules":[{"label":"x","match":[]}]}]}`, []string{"no match conditions", `"x"`}},

		// --- dimension set ---
		{"unknown dimension", `{"dimensions":[{"name":"t","rules":[{"label":"x","match":[{"dimension":"billing_account_name","operator":"exists"}]}]}]}`, []string{"unknown condition dimension", "billing_account_name", "tag:<key>"}},
		{"empty dimension", `{"dimensions":[{"name":"t","rules":[{"label":"x","match":[{"dimension":"","operator":"exists"}]}]}]}`, []string{"dimension is empty"}},

		// --- tag-key hazards (each its own case) ---
		{"tag empty key", `{"dimensions":[{"name":"t","rules":[{"label":"x","match":[{"dimension":"tag:","operator":"exists"}]}]}]}`, []string{"empty key"}},
		{"tag jsonpath $", `{"dimensions":[{"name":"t","rules":[{"label":"x","match":[{"dimension":"tag:$.env","operator":"exists"}]}]}]}`, []string{"$", "JSONPath"}},
		{"tag pointer /", `{"dimensions":[{"name":"t","rules":[{"label":"x","match":[{"dimension":"tag:/team","operator":"exists"}]}]}]}`, []string{"/", "JSON pointer"}},
		{"tag wildcard *", `{"dimensions":[{"name":"t","rules":[{"label":"x","match":[{"dimension":"tag:*","operator":"exists"}]}]}]}`, []string{"wildcard"}},
		{"tag wildcard **", `{"dimensions":[{"name":"t","rules":[{"label":"x","match":[{"dimension":"tag:**","operator":"exists"}]}]}]}`, []string{"wildcard"}},

		// --- operator + operand field validation ---
		{"empty operator", `{"dimensions":[{"name":"t","rules":[{"label":"x","match":[{"dimension":"service_name"}]}]}]}`, []string{"operator is empty"}},
		{"unknown operator", `{"dimensions":[{"name":"t","rules":[{"label":"x","match":[{"dimension":"service_name","operator":"matches","value":"y"}]}]}]}`, []string{"unknown operator", "matches"}},
		{"equals missing value", `{"dimensions":[{"name":"t","rules":[{"label":"x","match":[{"dimension":"service_name","operator":"equals"}]}]}]}`, []string{"equals", `non-empty "value"`}},
		{"equals empty value", `{"dimensions":[{"name":"t","rules":[{"label":"x","match":[{"dimension":"service_name","operator":"equals","value":""}]}]}]}`, []string{"equals", `non-empty "value"`}},
		{"equals with values", `{"dimensions":[{"name":"t","rules":[{"label":"x","match":[{"dimension":"service_name","operator":"equals","values":["a"]}]}]}]}`, []string{"equals", `takes "value", not "values"`}},
		{"one_of missing values", `{"dimensions":[{"name":"t","rules":[{"label":"x","match":[{"dimension":"service_name","operator":"one_of"}]}]}]}`, []string{"one_of", `non-empty "values" array`}},
		{"one_of empty array", `{"dimensions":[{"name":"t","rules":[{"label":"x","match":[{"dimension":"service_name","operator":"one_of","values":[]}]}]}]}`, []string{"one_of", `non-empty "values" array`}},
		{"one_of empty member", `{"dimensions":[{"name":"t","rules":[{"label":"x","match":[{"dimension":"service_name","operator":"one_of","values":["a",""]}]}]}]}`, []string{"one_of", "must not contain an empty string"}},
		{"one_of with value", `{"dimensions":[{"name":"t","rules":[{"label":"x","match":[{"dimension":"service_name","operator":"one_of","value":"a"}]}]}]}`, []string{"one_of", `takes "values", not "value"`}},
		{"exists with value", `{"dimensions":[{"name":"t","rules":[{"label":"x","match":[{"dimension":"service_name","operator":"exists","value":"a"}]}]}]}`, []string{"exists", "takes neither"}},
		{"exists with values", `{"dimensions":[{"name":"t","rules":[{"label":"x","match":[{"dimension":"service_name","operator":"exists","values":["a"]}]}]}]}`, []string{"exists", "takes neither"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tt.doc))
			if err == nil {
				t.Fatalf("Parse(%s) = nil error, want an error", tt.name)
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err.Error(), want)
				}
			}
		})
	}
}

// TestColumnDimensionsSet pins the exported closed set: the members and the
// deliberate exclusions (a stored column outside the subset must NOT appear).
func TestColumnDimensionsSet(t *testing.T) {
	got := map[string]bool{}
	for _, d := range ColumnDimensions() {
		got[d] = true
	}
	want := []string{
		"billing_account_id", "sub_account_id", "sub_account_name",
		"service_provider_name", "service_name", "service_category",
		"service_subcategory", "charge_category", "charge_description",
		"region_id", "region_name", "resource_id", "resource_name",
		"resource_type", "sku_id",
	}
	if len(got) != len(want) {
		t.Fatalf("ColumnDimensions() = %v (%d), want %d entries", ColumnDimensions(), len(got), len(want))
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("ColumnDimensions() missing %q", w)
		}
	}
	// Deliberate exclusions (stored, but not in the closed subset) must be absent.
	for _, excluded := range []string{"billing_account_name", "charge_class", "availability_zone", "sku_price_id"} {
		if got[excluded] {
			t.Errorf("ColumnDimensions() unexpectedly includes the deliberately-excluded column %q", excluded)
		}
	}
	// The returned slice is a fresh copy — mutating it must not affect the set.
	ColumnDimensions()[0] = "MUTATED"
	if ColumnDimensions()[0] == "MUTATED" {
		t.Error("ColumnDimensions() leaks its backing state (mutation persisted)")
	}
}
