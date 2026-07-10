// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package storage

import (
	"context"
	"testing"
	"time"
)

func TestBusinessMetricsReplaceAndExactQueries(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = store.Close() }()
	var s Store = store

	row := func(day int, metric, quantity string) BusinessMetricRow {
		return BusinessMetricRow{MetricDay: time.Date(2026, 5, day, 0, 0, 0, 0, time.UTC), MetricName: metric, Quantity: dec(t, quantity)}
	}
	const metric = "requests'); DROP TABLE business_metrics; --"
	if err := s.ReplaceBusinessMetricsBatch(ctx, "default", "primary", []BusinessMetricRow{
		row(1, metric, "12345678901234567.89"),
		row(2, metric, "2.000000000000000001"),
		row(2, "customers", "7"),
	}); err != nil {
		t.Fatalf("ReplaceBusinessMetricsBatch(primary): %v", err)
	}
	lateDayThree := row(3, metric, "5")
	lateDayThree.MetricDay = lateDayThree.MetricDay.Add(23*time.Hour + 45*time.Minute)
	if err := s.ReplaceBusinessMetricsBatch(ctx, "default", "secondary", []BusinessMetricRow{
		row(1, metric, "0.12"),
		lateDayThree,
	}); err != nil {
		t.Fatalf("ReplaceBusinessMetricsBatch(secondary): %v", err)
	}
	if err := s.ReplaceBusinessMetricsBatch(ctx, "other", "primary", []BusinessMetricRow{row(1, metric, "999")}); err != nil {
		t.Fatalf("ReplaceBusinessMetricsBatch(other tenant): %v", err)
	}

	names, err := s.BusinessMetricNames(ctx, "default")
	if err != nil {
		t.Fatalf("BusinessMetricNames: %v", err)
	}
	if len(names) != 2 || names[0].Name != "customers" || names[0].FirstDay != day(2) || names[0].LastDay != day(2) || names[1].Name != metric || names[1].FirstDay != day(1) || names[1].LastDay != day(3) {
		t.Fatalf("names = %+v", names)
	}
	quantities, err := s.DailyBusinessMetricQuantities(ctx, "default", metric, day(1), day(2))
	if err != nil {
		t.Fatalf("DailyBusinessMetricQuantities: %v", err)
	}
	if len(quantities) != 2 || quantities[0].Date != day(1) || quantities[0].Quantity.String() != "12345678901234568.01" || quantities[1].Date != day(2) || quantities[1].Quantity.String() != "2.000000000000000001" {
		t.Fatalf("quantities = %+v", quantities)
	}

	// Replacing primary removes its old rows while preserving the other label,
	// and never touches the same label in another tenant.
	if err := s.ReplaceBusinessMetricsBatch(ctx, "default", "primary", []BusinessMetricRow{row(2, metric, "4")}); err != nil {
		t.Fatalf("replacing primary: %v", err)
	}
	got, err := s.DailyBusinessMetricQuantities(ctx, "default", metric, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Quantity.String() != "0.12" || got[1].Quantity.String() != "4" || got[2].Quantity.String() != "5" {
		t.Fatalf("after replace = %+v", got)
	}
	other, err := s.DailyBusinessMetricQuantities(ctx, "other", metric, time.Time{}, time.Time{})
	if err != nil || len(other) != 1 || other[0].Quantity.String() != "999" {
		t.Fatalf("other tenant = (%+v, %v)", other, err)
	}
	if err := s.ReplaceBusinessMetricsBatch(ctx, "default", "secondary", nil); err != nil {
		t.Fatalf("clearing secondary: %v", err)
	}
	cleared, err := s.DailyBusinessMetricQuantities(ctx, "default", metric, time.Time{}, time.Time{})
	if err != nil || len(cleared) != 1 || cleared[0].Date != day(2) || cleared[0].Quantity.String() != "4" {
		t.Fatalf("after clear = (%+v, %v)", cleared, err)
	}

	if names, err := s.BusinessMetricNames(ctx, "nobody"); err != nil || len(names) != 0 {
		t.Fatalf("isolated names = (%+v, %v)", names, err)
	}
	if rows, err := s.DailyBusinessMetricQuantities(ctx, "nobody", metric, time.Time{}, time.Time{}); err != nil || len(rows) != 0 {
		t.Fatalf("isolated quantities = (%+v, %v)", rows, err)
	}
}
