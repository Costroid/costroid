// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package anthropiccost

import (
	"reflect"
	"slices"
	"testing"

	"github.com/Costroid/costroid/internal/ingest/contentsafety"
)

// TestAnthropicDecodeFieldAllowlist pins the COMPLETE set of JSON fields the
// anthropic-cost connector's decode structs can read from the vendor API against
// a committed allowlist (Cardinal Rule, decision D7 / Layer 2). The roots are
// the shared envelope pagedResponse plus the per-element json.Unmarshal targets
// bucket (cost) and usageBucket (usage, defined in enrich.go). Recursion reaches
// result, usageResult, and the nil-guarded pointer structs usageCacheCreation /
// usageServerToolUse. The token COUNT fields are json.Number (exact integers),
// aggregate counts, not prompt or response content.
//
// The roots MUST list EVERY struct this connector decodes vendor JSON into: a
// new json.Unmarshal or Body.Decode target added in future code must be added
// here too, or this gate cannot see its fields. Adding or renaming a field on
// any reachable struct turns this test red until the committed want list is
// updated (a visible, reviewer-gated diff); a content-bearing field name would
// also trip ScanForbidden.
func TestAnthropicDecodeFieldAllowlist(t *testing.T) {
	got, structCount := contentsafety.FieldSet(
		reflect.TypeOf(pagedResponse{}), reflect.TypeOf(bucket{}), reflect.TypeOf(usageBucket{}),
	)
	want := []string{ // the COMPLETE set anthropic-cost is capable of decoding (24)
		"amount", "cache_creation", "cache_read_input_tokens", "context_window",
		"cost_type", "currency", "data", "description", "ending_at",
		"ephemeral_1h_input_tokens", "ephemeral_5m_input_tokens", "has_more",
		"inference_geo", "model", "next_page", "output_tokens", "results",
		"server_tool_use", "service_tier", "starting_at", "token_type",
		"uncached_input_tokens", "web_search_requests", "workspace_id",
	}
	if !slices.Equal(got, want) {
		t.Errorf("anthropic decode field set drifted:\n got  %v\n want %v", got, want)
	}
	// Anti-vacuity: the exact counts are reflection-verified ground truth.
	if structCount != 7 {
		t.Errorf("structCount = %d, want 7 (pagedResponse, bucket, result, usageBucket, usageResult, usageCacheCreation, usageServerToolUse)", structCount)
	}
	if len(got) != 24 {
		t.Errorf("len(fields) = %d, want 24", len(got))
	}
	// No decoded field name may be a classic AI-content field name. The
	// *_input_tokens / server_tool_use names in this set are token/tool COUNTS,
	// so ScanForbidden must pass them (its curated denylist excludes
	// input/output/tool by design).
	if forbidden := contentsafety.ScanForbidden(got); len(forbidden) != 0 {
		t.Errorf("anthropic decode fields tripped the content-word scan: %v", forbidden)
	}
}
