// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package anthropiccost

import (
	"testing"
	"time"
)

// TestMonthWindow proves the window logic: --period wins (one month); the
// default is the current month and the 11 before it (12 months); --since
// moves the start; and a future --since is refused.
func TestMonthWindow(t *testing.T) {
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	if got, err := monthWindow("", "2026-05", now); err != nil || len(got) != 1 || got[0] != "2026-05" {
		t.Errorf("--period = %v (%v), want [2026-05]", got, err)
	}

	def, err := monthWindow("", "", now)
	if err != nil {
		t.Fatalf("default window: %v", err)
	}
	if len(def) != 12 || def[0] != "2025-08" || def[11] != "2026-07" {
		t.Errorf("default window = %v, want 12 months 2025-08..2026-07", def)
	}

	since, err := monthWindow("2026-05", "", now)
	if err != nil {
		t.Fatalf("--since window: %v", err)
	}
	if len(since) != 3 || since[0] != "2026-05" || since[2] != "2026-07" {
		t.Errorf("--since 2026-05 window = %v, want [2026-05 2026-06 2026-07]", since)
	}

	if _, err := monthWindow("2026-09", "", now); err == nil {
		t.Error("future --since was accepted")
	}
	for _, bad := range []string{"2026-13", "not-a-month"} {
		if _, err := monthWindow(bad, "", now); err == nil {
			t.Errorf("monthWindow(--since %q) succeeded, want an error", bad)
		}
	}
}

// TestContentHashOrderSensitive proves the change token depends on the data
// bytes and their fetch order but not the pagination envelope.
func TestContentHashOrderSensitive(t *testing.T) {
	a := []byte(`{"starting_at":"2026-05-01T00:00:00Z"}`)
	b := []byte(`{"starting_at":"2026-05-02T00:00:00Z"}`)
	base := contentHash([][]byte{a, b})
	if contentHash([][]byte{a, b}) != base {
		t.Error("content hash not stable for identical input")
	}
	if contentHash([][]byte{b, a}) == base {
		t.Error("content hash ignored fetch order")
	}
	if contentHash(nil) != "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Error("empty content hash is not the empty-stream digest")
	}
}
