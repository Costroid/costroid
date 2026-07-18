// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package contentsafety_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/Costroid/costroid/internal/ingest/contentsafety"
)

// csLeaf/csInner/csRoot are a synthetic decode graph exercising every shape the
// walker must handle: a nested struct, a *pointer struct field, a []slice of
// structs, an opaque json.RawMessage, a scalar json.Number, an omitempty tag, a
// json:"-" skip, and an untagged exported field (name falls back to the Go name).
type csLeaf struct {
	Alpha string `json:"alpha"`
}

type csInner struct {
	Beta  string `json:"beta,omitempty"`
	Gamma csLeaf `json:"gamma"`
}

type csRoot struct {
	Delta   string          `json:"delta"`
	Ptr     *csInner        `json:"ptr"`
	Items   []csLeaf        `json:"items"`
	Raw     json.RawMessage `json:"raw"`
	Num     json.Number     `json:"num"`
	Ignored string          `json:"-"`
	Plain   string          // no json tag: name falls back to the Go field name.
}

func TestFieldSetWalksNestedTreatsRawMessageAndNumberAsLeaves(t *testing.T) {
	got, structCount := contentsafety.FieldSet(reflect.TypeOf(csRoot{}))

	// The three distinct struct types: csRoot, csInner, csLeaf. json.RawMessage
	// and json.Number are leaves, not structs, so they do not add to the count.
	want := []string{"Plain", "alpha", "beta", "delta", "gamma", "items", "num", "ptr", "raw"}
	if !slices.Equal(got, want) {
		t.Errorf("FieldSet names =\n  %v\nwant\n  %v", got, want)
	}
	if structCount != 3 {
		t.Errorf("structCount = %d, want 3 (csRoot, csInner, csLeaf)", structCount)
	}
	// "raw" (json.RawMessage) is COLLECTED but the walker did not descend into
	// its []byte; "num" (json.Number) is collected as a scalar leaf; the
	// json:"-" field is absent.
	if !slices.Contains(got, "raw") {
		t.Error("expected the json.RawMessage field name \"raw\" to be collected (as a leaf)")
	}
	if slices.Contains(got, "Ignored") {
		t.Error("the json:\"-\" field must not appear in the field set")
	}
	// Anti-vacuity (decision D64): a walker that silently returned empty must
	// fail loudly, not pass.
	if structCount < 3 {
		t.Errorf("anti-vacuity: structCount = %d, want >= 3", structCount)
	}
	if len(got) < 6 {
		t.Errorf("anti-vacuity: len(names) = %d, want >= 6", len(got))
	}
}

func TestScanForbiddenCatchesContentWordsButPassesCountsAndTools(t *testing.T) {
	// Each of these carries a content word and MUST be flagged. imageURL and
	// image_url both resolve to the multi-word image_url term.
	flagged := []string{"message", "content", "prompt", "completion_text", "imageURL", "image_url", "choices"}
	if got := contentsafety.ScanForbidden(flagged); !slices.Equal(got, flagged) {
		t.Errorf("ScanForbidden(content names) = %v, want every one flagged: %v", got, flagged)
	}

	// Real cost/usage metadata field names that MUST all pass. context_window is
	// the load-bearing case: "context" CONTAINS the substring "text", so a naive
	// substring scan would wrongly flag it; the tokenizer must be TOKEN-exact.
	// The *_input_tokens / server_tool_use names pin the input/output/tool
	// false-positive fix. Do not weaken this test.
	pass := []string{
		"output_tokens", "uncached_input_tokens", "cache_read_input_tokens",
		"ephemeral_5m_input_tokens", "server_tool_use", "description",
		"characters", "service_tier", "currency", "start_time",
		"num_model_requests", "context_window",
	}
	if got := contentsafety.ScanForbidden(pass); len(got) != 0 {
		t.Errorf("ScanForbidden wrongly flagged legitimate metadata names: %v", got)
	}
}

func TestAIConnectorsImportingAiwireEnumeratesExactlyTheGatedConnectors(t *testing.T) {
	// This package lives at internal/ingest/contentsafety, so the ingest
	// directory is the parent (".."). Only openaicost and anthropiccost route
	// their AI fetches through aiwire today.
	got, err := contentsafety.AIConnectorsImportingAiwire("..")
	if err != nil {
		t.Fatalf("AIConnectorsImportingAiwire: %v", err)
	}
	// Anti-vacuity: an empty result (e.g. a walker that found nothing) must fail
	// before the equality check can vacuously "pass".
	if len(got) == 0 {
		t.Fatal("AIConnectorsImportingAiwire returned no connectors; expected at least the two gated ones")
	}
	// MANDATORY-ACKNOWLEDGEMENT TRIPWIRE: this exact set is committed. A future
	// connector that imports aiwire but is not added here turns CI red on
	// purpose, forcing a reviewer to acknowledge the new AI-fetch surface (and
	// to pin its own decode-field allowlist).
	want := []string{"anthropiccost", "openaicost"}
	if !slices.Equal(got, want) {
		t.Errorf("aiwire importers = %v, want exactly %v", got, want)
	}
}
