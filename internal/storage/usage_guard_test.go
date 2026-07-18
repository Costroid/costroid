// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package storage

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/focus"
)

// usageMetric builds one allowlisted usage-metric row with the given metric name
// and unit; quantity and the categorical dims are fixed.
func usageMetric(t *testing.T, name, unit string) Metric {
	t.Helper()
	return Metric{
		ChargePeriodStart: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		ServiceName:       "claude-opus-4-6",
		ServiceTier:       "standard",
		MetricName:        name,
		Unit:              unit,
		Quantity:          dec(t, "100"),
	}
}

// countUsageRows returns the number of usage_metrics rows stored for one
// (connector, source_identity), reading the table directly.
func countUsageRows(t *testing.T, store *DuckDB, connector, sourceIdentity string) int {
	t.Helper()
	var n int
	if err := store.db.QueryRow(
		`SELECT COUNT(*) FROM usage_metrics WHERE connector = ? AND source_identity = ?`,
		connector, sourceIdentity).Scan(&n); err != nil {
		t.Fatalf("counting usage rows: %v", err)
	}
	return n
}

// TestReplaceUsageBatchUnitAllowlist proves the closed Unit vocabulary: a
// non-allowlisted unit is rejected with an error naming the unit, and every one
// of the nine allowed units is accepted.
func TestReplaceUsageBatchUnitAllowlist(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = store.Close() }()

	batch := UsageBatch{Connector: "anthropic-cost", SourceIdentity: "src-unit", TenantID: focus.DefaultTenant}

	if err := store.ReplaceUsageBatch(ctx, batch, []Metric{usageMetric(t, "uncached_input_tokens", "Bogus")}); err == nil || !strings.Contains(err.Error(), "Bogus") {
		t.Fatalf("bogus-unit error = %v, want an error mentioning the unit", err)
	}

	for _, u := range []string{"Tokens", "Requests", "Unknown", "Images", "Characters", "Seconds", "Sessions", "Bytes", "Calls"} {
		if err := store.ReplaceUsageBatch(ctx, batch, []Metric{usageMetric(t, "uncached_input_tokens", u)}); err != nil {
			t.Errorf("allowed unit %q rejected: %v", u, err)
		}
	}
}

// TestReplaceUsageBatchBoundsFreeText proves the write-boundary length guard: a
// metric_name over the 8 KiB bound is rejected (absolute literal length, so
// lowering the bound reddens this), while a normal metric is accepted.
func TestReplaceUsageBatchBoundsFreeText(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = store.Close() }()

	batch := UsageBatch{Connector: "anthropic-cost", SourceIdentity: "src-len", TenantID: focus.DefaultTenant}

	over := usageMetric(t, strings.Repeat("m", 9000), "Tokens")
	if err := store.ReplaceUsageBatch(ctx, batch, []Metric{over}); err == nil ||
		!strings.Contains(err.Error(), "metric_name") || !strings.Contains(err.Error(), "size bound") {
		t.Fatalf("over-bound metric_name error = %v, want a metric_name size-bound error", err)
	}

	if err := store.ReplaceUsageBatch(ctx, batch, []Metric{usageMetric(t, "uncached_input_tokens", "Tokens")}); err != nil {
		t.Fatalf("normal metric rejected: %v", err)
	}
}

// TestReplaceUsageBatchRejectRollsBackDelete proves the whole batch is atomic: a
// rejected metric rolls back the DELETE too. Seeded rows under an identity must
// survive a later rejected re-run for the same identity, and a rejected batch on
// an empty identity writes nothing.
func TestReplaceUsageBatchRejectRollsBackDelete(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = store.Close() }()

	batch := UsageBatch{Connector: "openai-cost", SourceIdentity: "src-rollback", TenantID: focus.DefaultTenant}
	seed := []Metric{
		usageMetric(t, "uncached_input_tokens", "Tokens"),
		usageMetric(t, "output_tokens", "Tokens"),
		usageMetric(t, "web_search_requests", "Requests"),
	}
	if err := store.ReplaceUsageBatch(ctx, batch, seed); err != nil {
		t.Fatalf("seed ReplaceUsageBatch: %v", err)
	}
	if n := countUsageRows(t, store, batch.Connector, batch.SourceIdentity); n != 3 {
		t.Fatalf("after seed, rows = %d, want 3", n)
	}

	// Re-run for the SAME identity with a batch carrying one bad metric. The whole
	// transaction must roll back, including the DELETE, so the 3 seeded rows survive.
	bad := []Metric{
		usageMetric(t, "output_tokens", "Tokens"),
		usageMetric(t, "leaked", "Bogus"),
	}
	if err := store.ReplaceUsageBatch(ctx, batch, bad); err == nil {
		t.Fatal("rejected re-run returned nil error")
	}
	if n := countUsageRows(t, store, batch.Connector, batch.SourceIdentity); n != 3 {
		t.Fatalf("after rejected re-run, rows = %d, want the 3 seeded rows to survive (DELETE rolled back)", n)
	}

	// A rejected batch on an empty identity writes nothing.
	empty := UsageBatch{Connector: "openai-cost", SourceIdentity: "src-empty", TenantID: focus.DefaultTenant}
	if err := store.ReplaceUsageBatch(ctx, empty, []Metric{usageMetric(t, "leaked", "Bogus")}); err == nil {
		t.Fatal("rejected batch on empty identity returned nil error")
	}
	if n := countUsageRows(t, store, empty.Connector, empty.SourceIdentity); n != 0 {
		t.Fatalf("rejected batch on empty identity wrote %d rows, want 0", n)
	}
}
