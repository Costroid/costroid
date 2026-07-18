// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package anthropiccost_test

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/Costroid/costroid/internal/credentials"
	"github.com/Costroid/costroid/internal/devtools/fakeanthropic"
	"github.com/Costroid/costroid/internal/ingest/anthropiccost"
	"github.com/Costroid/costroid/internal/ingest/contentsafety"
)

// TestAnthropicCardinalRulePathsOnly proves the anthropic-cost connector issues
// requests ONLY to its two allowlisted cost/usage endpoints — never /projects,
// /users, or any ID→name resolution call (Cardinal Rule D7). It drives a full
// month discovery against the fake and routes the forbidden-path assertion
// through contentsafety.ForbiddenPaths, the shared Layer-2 comparator. The two
// paths are already-public API URLs, asserted here as literals.
func TestAnthropicCardinalRulePathsOnly(t *testing.T) {
	h, baseURL := startFake(t, fixture)
	secret := credentials.NewSecret(fakeanthropic.AdminKey)
	if _, err := anthropiccost.Discover(context.Background(), http.DefaultClient, baseURL, anthropiccost.Name, secret, "", "2026-05"); err != nil {
		t.Fatalf("discover: %v", err)
	}
	var observed []string
	for _, r := range h.Requests() {
		observed = append(observed, r.Path)
	}
	allowed := []string{"/v1/organizations/cost_report", "/v1/organizations/usage_report/messages"}
	for _, p := range contentsafety.ForbiddenPaths(observed, allowed) {
		t.Errorf("connector hit a forbidden path %q (only the cost_report and usage_report/messages paths are permitted)", p)
	}
	// Coverage: both allowlisted paths were actually exercised, so a connector
	// that silently stopped calling one of them cannot pass vacuously.
	for _, want := range allowed {
		if !slices.Contains(observed, want) {
			t.Errorf("expected the connector to request %q, but it never did (observed paths: %v)", want, observed)
		}
	}
}
