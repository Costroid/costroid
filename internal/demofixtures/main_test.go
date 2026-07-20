// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package main

import (
	"strings"
	"testing"
)

func TestPinnedCapturedTagKeysRejectsUnexpectedDiscovery(t *testing.T) {
	got, err := pinnedCapturedTagKeys(capturedDaily{TagKeys: []string{"environment", "team"}})
	if err == nil || !strings.Contains(err.Error(), "want exactly") {
		t.Fatalf("pinnedCapturedTagKeys = %v, %v, want a pin error", got, err)
	}
}

func TestValidateTagKeySlugCollisionsRejectsCollidingKeys(t *testing.T) {
	err := validateTagKeySlugCollisions([]string{"env.a", "env-a"})
	if err == nil || !strings.Contains(err.Error(), `share slug "env-a"`) {
		t.Fatalf("validateTagKeySlugCollisions error = %v, want env-a collision", err)
	}
}
