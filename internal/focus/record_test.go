// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package focus

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestParseTags(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		want        map[string]any // nil means an error is expected
		wantErrPart string
	}{
		{
			name: "scalar values of every allowed kind",
			in:   `{"user:env": "prod", "user:opted-in": true, "user:weight": 3.5, "user:owner": null}`,
			want: map[string]any{
				"user:env":      "prod",
				"user:opted-in": true,
				"user:weight":   json.Number("3.5"),
				"user:owner":    nil,
			},
		},
		{
			name: "empty object",
			in:   `{}`,
			want: map[string]any{},
		},
		{
			name:        "duplicate keys are rejected, naming the key",
			in:          `{"user:env": "prod", "user:env": "staging"}`,
			wantErrPart: `duplicate tag key "user:env"`,
		},
		{
			name:        "duplicate keys with identical values are still rejected",
			in:          `{"user:team": "platform", "user:team": "platform"}`,
			wantErrPart: `duplicate tag key "user:team"`,
		},
		{
			name:        "trailing second object",
			in:          `{"a": 1}{"b": 2}`,
			wantErrPart: "trailing data after the key-value object",
		},
		{
			name:        "trailing garbage",
			in:          `{"a": 1}garbage`,
			wantErrPart: "trailing data after the key-value object",
		},
		{
			name: "trailing whitespace is fine",
			in:   `{"a": 1}` + " \n\t",
			want: map[string]any{"a": json.Number("1")},
		},
		{
			name:        "top-level null",
			in:          `null`,
			wantErrPart: "key-value JSON is null",
		},
		{
			name:        "top-level array",
			in:          `[{"a": 1}]`,
			wantErrPart: "not an object",
		},
		{
			name:        "top-level string",
			in:          `"user:env"`,
			wantErrPart: "not an object",
		},
		{
			name:        "object value",
			in:          `{"user:env": {"nested": "no"}}`,
			wantErrPart: `tag "user:env" has a JSON object value`,
		},
		{
			name:        "array value",
			in:          `{"user:env": ["prod"]}`,
			wantErrPart: `tag "user:env" has a JSON array value`,
		},
		{
			name:        "malformed JSON",
			in:          `{not json`,
			wantErrPart: "parsing key-value JSON",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTags(tt.in)
			if tt.want == nil {
				if err == nil {
					t.Fatalf("ParseTags(%q) = %v, want an error containing %q", tt.in, got, tt.wantErrPart)
				}
				if !strings.Contains(err.Error(), tt.wantErrPart) {
					t.Errorf("ParseTags(%q) error %q does not contain %q", tt.in, err, tt.wantErrPart)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTags(%q): %v", tt.in, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseTags(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}
