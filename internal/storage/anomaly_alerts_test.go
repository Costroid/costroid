// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package storage

import (
	"context"
	"strings"
	"testing"
	"time"
)

// alertKey is the dedup identity of an alert as a comparable string, with the
// date normalized to its calendar day (matching the DATE column). Used to assert
// which alerts InsertNewAnomalyAlerts reported new, and in what order.
func alertKey(a AnomalyAlert) string {
	return strings.Join([]string{
		a.TenantID, a.Scope, a.SeriesKey, a.Currency,
		a.Date.UTC().Format(time.DateOnly), a.Direction,
	}, "|")
}

func assertNewAlerts(t *testing.T, label string, got, want []AnomalyAlert) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: returned %d new alerts, want %d", label, len(got), len(want))
	}
	for i := range want {
		if g, w := alertKey(got[i]), alertKey(want[i]); g != w {
			t.Fatalf("%s: new alert %d = %s, want %s (input order must be preserved)", label, i, g, w)
		}
	}
}

func aDay(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC) }

// TestInsertNewAnomalyAlertsDedup pins the insert-if-absent contract: an empty
// input is a no-op, a first batch is all new in input order, an identical replay
// is all-none, a mixed batch returns only the never-seen entries in input order,
// and a within-call duplicate is reported new exactly once.
func TestInsertNewAnomalyAlertsDedup(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	at := time.Date(2026, 7, 19, 9, 30, 0, 0, time.UTC)

	// Empty-input guard: an empty slice back, no error, nothing recorded.
	got, err := store.InsertNewAnomalyAlerts(ctx, nil, at)
	if err != nil {
		t.Fatalf("empty insert: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty insert returned %d alerts, want 0", len(got))
	}

	batch := []AnomalyAlert{
		{TenantID: "default", Scope: "total", SeriesKey: "", Currency: "USD", Date: aDay(2026, 1, 15), Direction: "increase"},
		{TenantID: "default", Scope: "key", SeriesKey: "compute", Currency: "USD", Date: aDay(2026, 1, 15), Direction: "increase"},
		{TenantID: "default", Scope: "key", SeriesKey: "compute", Currency: "USD", Date: aDay(2026, 1, 16), Direction: "decrease"},
	}

	// First insert: every entry is new, returned in input order.
	got, err = store.InsertNewAnomalyAlerts(ctx, batch, at)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	assertNewAlerts(t, "first insert", got, batch)

	// Identical replay (even with a later `at`): none are new.
	got, err = store.InsertNewAnomalyAlerts(ctx, batch, at.Add(time.Hour))
	if err != nil {
		t.Fatalf("replay insert: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("replay returned %d alerts, want 0 (all already recorded)", len(got))
	}

	// Mixed batch: a new entry bracketed by two already-seen ones. Only the new
	// entry comes back, and the count grows by exactly one.
	fresh := AnomalyAlert{TenantID: "default", Scope: "total", SeriesKey: "", Currency: "USD", Date: aDay(2026, 1, 17), Direction: "decrease"}
	got, err = store.InsertNewAnomalyAlerts(ctx, []AnomalyAlert{batch[0], fresh, batch[2]}, at)
	if err != nil {
		t.Fatalf("mixed insert: %v", err)
	}
	assertNewAlerts(t, "mixed insert", got, []AnomalyAlert{fresh})

	// Within a single call: the same identity twice is reported new once (the row
	// inserted earlier in the uncommitted transaction is visible to the retry).
	dupNew := AnomalyAlert{TenantID: "default", Scope: "key", SeriesKey: "storage", Currency: "USD", Date: aDay(2026, 1, 18), Direction: "increase"}
	got, err = store.InsertNewAnomalyAlerts(ctx, []AnomalyAlert{dupNew, dupNew}, at)
	if err != nil {
		t.Fatalf("intra-batch duplicate insert: %v", err)
	}
	assertNewAlerts(t, "intra-batch duplicate", got, []AnomalyAlert{dupNew})

	// AnomalyAlertCount reflects the five distinct identities recorded so far.
	count, err := store.AnomalyAlertCount(ctx, "default")
	if err != nil {
		t.Fatalf("count default: %v", err)
	}
	if count != 5 {
		t.Fatalf("default count = %d, want 5 distinct identities", count)
	}
	// A tenant with no alerts counts zero (the alerter's first-enable signal).
	other, err := store.AnomalyAlertCount(ctx, "acme")
	if err != nil {
		t.Fatalf("count acme: %v", err)
	}
	if other != 0 {
		t.Fatalf("acme count = %d, want 0", other)
	}
}

// TestInsertNewAnomalyAlertsDistinctIdentities pins that a single differing
// primary-key column (direction, currency, or series key) makes a distinct
// dedup identity, so each variant is recorded independently.
func TestInsertNewAnomalyAlertsDistinctIdentities(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	at := time.Date(2026, 7, 19, 9, 30, 0, 0, time.UTC)

	base := AnomalyAlert{TenantID: "default", Scope: "key", SeriesKey: "compute", Currency: "USD", Date: aDay(2026, 1, 15), Direction: "increase"}
	byDirection := base
	byDirection.Direction = "decrease"
	byCurrency := base
	byCurrency.Currency = "EUR"
	byKey := base
	byKey.SeriesKey = "storage"
	variants := []AnomalyAlert{base, byDirection, byCurrency, byKey}

	got, err := store.InsertNewAnomalyAlerts(ctx, variants, at)
	if err != nil {
		t.Fatalf("distinct insert: %v", err)
	}
	assertNewAlerts(t, "distinct identities", got, variants)

	count, err := store.AnomalyAlertCount(ctx, "default")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 4 {
		t.Fatalf("count = %d, want 4 distinct identities", count)
	}
}

// TestInsertNewAnomalyAlertsDateNormalizesToDay pins that a stray time component
// on Date cannot split one anomaly into two rows: the DATE column normalizes any
// input to its calendar day, so a mid-day alert and a midnight alert on the same
// day are the same identity. It also asserts alerted_at round-trips the passed
// `at` (no wall clock) and anomaly_date reads back as the calendar day.
func TestInsertNewAnomalyAlertsDateNormalizesToDay(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	at := time.Date(2026, 7, 19, 9, 30, 15, 0, time.UTC)

	midday := AnomalyAlert{
		TenantID: "default", Scope: "total", SeriesKey: "", Currency: "USD",
		Date: time.Date(2026, 1, 15, 13, 45, 0, 0, time.UTC), Direction: "increase",
	}
	got, err := store.InsertNewAnomalyAlerts(ctx, []AnomalyAlert{midday}, at)
	if err != nil {
		t.Fatalf("mid-day insert: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("mid-day insert returned %d, want 1 (first record)", len(got))
	}

	// Same calendar day at exact midnight: already recorded, returns none.
	midnight := midday
	midnight.Date = time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	got, err = store.InsertNewAnomalyAlerts(ctx, []AnomalyAlert{midnight}, at.Add(time.Hour))
	if err != nil {
		t.Fatalf("midnight insert: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("midnight insert returned %d, want 0 (same calendar day already recorded)", len(got))
	}

	// Exactly one row exists, and its columns round-trip.
	count, err := store.AnomalyAlertCount(ctx, "default")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1 (the mid-day time component must not split the day)", count)
	}

	var gotAt, gotDate time.Time
	if err := store.db.QueryRowContext(ctx,
		`SELECT alerted_at, anomaly_date FROM anomaly_alerts WHERE tenant_id = 'default'`).Scan(&gotAt, &gotDate); err != nil {
		t.Fatalf("reading back the row: %v", err)
	}
	if !gotAt.Equal(at) {
		t.Fatalf("alerted_at = %s, want %s (the passed at, not a clock)", gotAt.UTC(), at)
	}
	if d := gotDate.UTC().Format(time.DateOnly); d != "2026-01-15" {
		t.Fatalf("anomaly_date = %s, want 2026-01-15 (normalized to the calendar day)", d)
	}
}
