// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package openaicost

import (
	"reflect"
	"slices"
	"testing"

	"github.com/Costroid/costroid/internal/ingest/contentsafety"
)

// TestOpenaiDecodeFieldAllowlist pins the COMPLETE set of JSON fields the
// openai-cost connector's decode structs can read from the vendor API against a
// committed allowlist (Cardinal Rule, decision D7 / Layer 2). The four roots are
// this connector's decode targets: the envelope Body.Decode targets costsPage /
// usagePage, plus the per-json.RawMessage-element json.Unmarshal targets bucket
// / usageBucket. Recursion reaches result, amount, and usageResult.
//
// The roots MUST list EVERY struct this connector decodes vendor JSON into: a
// new json.Unmarshal or Body.Decode target added in future code must be added
// here too, or this gate cannot see its fields. Adding or renaming a field on
// any reachable struct turns this test red until the committed want list is
// updated (a visible, reviewer-gated diff); a content-bearing field name would
// also trip ScanForbidden.
func TestOpenaiDecodeFieldAllowlist(t *testing.T) {
	got, structCount := contentsafety.FieldSet(
		reflect.TypeOf(costsPage{}), reflect.TypeOf(bucket{}),
		reflect.TypeOf(usagePage{}), reflect.TypeOf(usageBucket{}),
	)
	want := []string{ // the COMPLETE set openai-cost is capable of decoding (20)
		"amount", "characters", "currency", "data", "end_time", "has_more",
		"images", "line_item", "model", "next_page", "num_model_requests",
		"num_requests", "num_sessions", "project_id", "quantity", "results",
		"seconds", "start_time", "usage_bytes", "value",
	}
	if !slices.Equal(got, want) {
		t.Errorf("openai decode field set drifted:\n got  %v\n want %v", got, want)
	}
	// Anti-vacuity: the exact counts are reflection-verified ground truth.
	if structCount != 7 {
		t.Errorf("structCount = %d, want 7 (costsPage, bucket, result, amount, usagePage, usageBucket, usageResult)", structCount)
	}
	if len(got) != 20 {
		t.Errorf("len(fields) = %d, want 20", len(got))
	}
	// No decoded field name may be a classic AI-content field name.
	if forbidden := contentsafety.ScanForbidden(got); len(forbidden) != 0 {
		t.Errorf("openai decode fields tripped the content-word scan: %v", forbidden)
	}
}
