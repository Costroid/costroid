// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package storage

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/allocation"
	"github.com/Costroid/costroid/internal/focus"
)

// --- helpers ---------------------------------------------------------------

func sp(s string) *string { return &s }

func withTags(r focus.CostRecord, tags map[string]any) focus.CostRecord {
	r.Tags = tags
	return r
}

func withCol(r focus.CostRecord, set func(*focus.CostRecord)) focus.CostRecord {
	set(&r)
	return r
}

// allocStore opens a fresh store and seeds every record in one batch. Records
// carry their own XTenantID/BillingCurrency, so a single batch can hold mixed
// tenants or currencies (the query filters by x_tenant_id).
func allocStore(t *testing.T, records ...focus.CostRecord) *DuckDB {
	t.Helper()
	store, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if len(records) > 0 {
		batch := Batch{Connector: "test", SourceIdentity: "alloc", ContentHash: "sha256:alloc", TenantID: focus.DefaultTenant}
		if _, err := store.ReplaceIngestBatch(context.Background(), batch, records); err != nil {
			t.Fatalf("ReplaceIngestBatch: %v", err)
		}
	}
	return store
}

func flattenLabels(dc DailyCosts) map[string]decimal.Decimal {
	out := map[string]decimal.Decimal{}
	for _, d := range dc.Days {
		for _, s := range d.Services {
			out[s.ServiceName] = out[s.ServiceName].Add(s.Cost)
		}
	}
	return out
}

// assertLabels checks the EXACT label set (no unexpected keys, no "") and each
// label's exact decimal-string sum.
func assertLabels(t *testing.T, dc DailyCosts, want map[string]string) {
	t.Helper()
	got := flattenLabels(dc)
	if len(got) != len(want) {
		keys := make([]string, 0, len(got))
		for k := range got {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		t.Fatalf("label set = %v, want keys for %v", keys, want)
	}
	for label, wantSum := range want {
		g, ok := got[label]
		if !ok {
			t.Errorf("missing label %q", label)
			continue
		}
		if g.String() != wantSum {
			t.Errorf("label %q sum = %s, want %s", label, g.String(), wantSum)
		}
	}
}

func rowCount(t *testing.T, store *DuckDB) int {
	t.Helper()
	var n int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM cost_records`).Scan(&n); err != nil {
		t.Fatalf("counting cost_records: %v", err)
	}
	return n
}

func rule(label string, conds ...allocation.Condition) allocation.Rule {
	return allocation.Rule{Label: label, Match: conds}
}

func dim(rules ...allocation.Rule) allocation.Dimension {
	return allocation.Dimension{Name: "team", Rules: rules}
}

// --- the money test --------------------------------------------------------

// TestDailyCostsByAllocationExactSumAndKeySet is the money test: it seeds rows
// across two days (including rows matching no rule and a value that cannot
// survive a float64 round-trip), then asserts (i) the grand total, per-label
// sums, and the float-hazard survive as EXACT decimal strings, (ii) the exact
// key set (Unallocated present, no ""), and (iii) that the allocation grand
// total equals the ungrouped total from DailyCostsByService (both cover every
// row once).
func TestDailyCostsByAllocationExactSumAndKeySet(t *testing.T) {
	ctx := context.Background()
	// COUPLED to the seeds below:
	//   compute     = 123.4567890123456789 + 0.000000000000000001 = 123.456789012345678901
	//   serverless  = 10.5
	//   Unallocated = 3.25 + 7 = 10.25
	//   grand total = 144.206789012345678901
	store := allocStore(t,
		testRecord(t, "Amazon Elastic Compute Cloud", day(1), "123.4567890123456789"),
		testRecord(t, "AWS Lambda", day(1), "10.5"),
		testRecord(t, "Amazon Simple Storage Service", day(1), "3.25"),
		testRecord(t, "Amazon Elastic Compute Cloud", day(2), "0.000000000000000001"),
		testRecord(t, "Other Thing", day(2), "7"),
	)

	d := dim(
		rule("compute", allocation.Condition{Dimension: "service_name", Operator: allocation.OpStartsWith, Value: sp("Amazon Elastic")}),
		rule("serverless", allocation.Condition{Dimension: "service_name", Operator: allocation.OpEquals, Value: sp("AWS Lambda")}),
	)

	got, err := store.DailyCostsByAllocation(ctx, focus.DefaultTenant, time.Time{}, time.Time{}, d)
	if err != nil {
		t.Fatalf("DailyCostsByAllocation: %v", err)
	}
	assertLabels(t, got, map[string]string{
		"compute":     "123.456789012345678901", // float-hazard preserved exactly
		"serverless":  "10.5",
		"Unallocated": "10.25",
	})

	// Grand total is exact and equals the ungrouped service-grouped total.
	allocTotal := decimal.Zero
	for _, v := range flattenLabels(got) {
		allocTotal = allocTotal.Add(v)
	}
	if allocTotal.String() != "144.206789012345678901" {
		t.Errorf("allocation grand total = %s, want 144.206789012345678901", allocTotal.String())
	}

	byService, err := store.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyCostsByService: %v", err)
	}
	svcTotal := decimal.Zero
	for _, v := range flattenLabels(byService) {
		svcTotal = svcTotal.Add(v)
	}
	if allocTotal.String() != svcTotal.String() {
		t.Errorf("allocation total %s != service-grouped total %s (a row was dropped or double-counted)", allocTotal, svcTotal)
	}
	if got.Currency != "USD" {
		t.Errorf("currency = %q, want USD", got.Currency)
	}
}

// TestDailyCostsByAllocationZeroRules pins the degenerate form: with zero rules
// every row lands under Unallocated (via the CAST(? AS VARCHAR) grouping) and
// the sums stay exact.
func TestDailyCostsByAllocationZeroRules(t *testing.T) {
	store := allocStore(t,
		testRecord(t, "Amazon Elastic Compute Cloud", day(1), "1.25"),
		testRecord(t, "AWS Lambda", day(2), "0.000000000000000003"),
	)
	got, err := store.DailyCostsByAllocation(context.Background(), focus.DefaultTenant, time.Time{}, time.Time{}, dim())
	if err != nil {
		t.Fatalf("DailyCostsByAllocation(zero rules): %v", err)
	}
	assertLabels(t, got, map[string]string{"Unallocated": "1.250000000000000003"})
}

// TestDailyCostsByAllocationDayBucketingAndOrdering compares the FULL DailyCosts
// value across two days, with one label sorting before Unallocated (Apex) and
// one after (zeus), pinning day bucketing on charge_period_start and lexical key
// ordering (Unallocated sorts as an ordinary label).
func TestDailyCostsByAllocationDayBucketingAndOrdering(t *testing.T) {
	store := allocStore(t,
		testRecord(t, "SvcA", day(1), "1"),
		testRecord(t, "SvcZ", day(1), "2"),
		testRecord(t, "SvcU", day(1), "3"),
		testRecord(t, "SvcA", day(2), "4"),
		testRecord(t, "SvcU", day(2), "5"),
	)
	d := dim(
		rule("Apex", allocation.Condition{Dimension: "service_name", Operator: allocation.OpEquals, Value: sp("SvcA")}),
		rule("zeus", allocation.Condition{Dimension: "service_name", Operator: allocation.OpEquals, Value: sp("SvcZ")}),
	)
	got, err := store.DailyCostsByAllocation(context.Background(), focus.DefaultTenant, time.Time{}, time.Time{}, d)
	if err != nil {
		t.Fatalf("DailyCostsByAllocation: %v", err)
	}
	// Apex < Unallocated < zeus (ASCII 'A' < 'U' < 'z').
	want := DailyCosts{Currency: "USD", Days: []DayCosts{
		{Date: day(1), Services: []ServiceCost{
			{ServiceName: "Apex", Cost: dec(t, "1")},
			{ServiceName: "Unallocated", Cost: dec(t, "3")},
			{ServiceName: "zeus", Cost: dec(t, "2")},
		}},
		{Date: day(2), Services: []ServiceCost{
			{ServiceName: "Apex", Cost: dec(t, "4")},
			{ServiceName: "Unallocated", Cost: dec(t, "5")},
		}},
	}}
	assertDailyCosts(t, got, want)
}

// TestDailyCostsByAllocationFirstMatchWins proves top-down, first-match-wins: a
// row matching BOTH rule 1 and rule 2 carries rule 1's label only (not summed
// under both).
func TestDailyCostsByAllocationFirstMatchWins(t *testing.T) {
	store := allocStore(t,
		testRecord(t, "Shared", day(1), "10"), // matches rule 1 (service_name) AND rule 2 (provider)
		testRecord(t, "Other", day(1), "20"),  // matches rule 2 only
	)
	d := dim(
		rule("first", allocation.Condition{Dimension: "service_name", Operator: allocation.OpEquals, Value: sp("Shared")}),
		rule("second", allocation.Condition{Dimension: "service_provider_name", Operator: allocation.OpEquals, Value: sp("AWS")}),
	)
	got, err := store.DailyCostsByAllocation(context.Background(), focus.DefaultTenant, time.Time{}, time.Time{}, d)
	if err != nil {
		t.Fatalf("DailyCostsByAllocation: %v", err)
	}
	// "first" is exactly 10 (the shared row did NOT also count under "second").
	assertLabels(t, got, map[string]string{"first": "10", "second": "20"})
}

// TestDailyCostsByAllocationInjectionInert is the injection guard, table-driven
// over the four parameter classes (match value, label, tag key, one_of member).
// Each hostile string is bound as an inert literal: the query succeeds, the
// label round-trips verbatim, and cost_records keeps its full row count. It
// FAILS if any of the four classes is string-interpolated instead of bound.
func TestDailyCostsByAllocationInjectionInert(t *testing.T) {
	const dropVal = "'; DROP TABLE cost_records;--"
	const delLabel = "x'); DELETE FROM cost_records;--"
	const delKey = "x'); DELETE FROM cost_records;--"
	const oneOfEvil = "y'); DELETE FROM cost_records;--"

	tests := []struct {
		name      string
		records   []focus.CostRecord
		dim       allocation.Dimension
		wantLabel string
	}{
		{
			name:      "hostile match value",
			records:   []focus.CostRecord{testRecord(t, dropVal, day(1), "1")},
			dim:       dim(rule("safe", allocation.Condition{Dimension: "service_name", Operator: allocation.OpEquals, Value: sp(dropVal)})),
			wantLabel: "safe",
		},
		{
			name:      "hostile label",
			records:   []focus.CostRecord{testRecord(t, "Injectable", day(1), "1")},
			dim:       dim(rule(delLabel, allocation.Condition{Dimension: "service_name", Operator: allocation.OpEquals, Value: sp("Injectable")})),
			wantLabel: delLabel,
		},
		{
			name:      "hostile tag key",
			records:   []focus.CostRecord{withTags(testRecord(t, "Svc", day(1), "1"), map[string]any{delKey: "present"})},
			dim:       dim(rule("tagged", allocation.Condition{Dimension: "tag:" + delKey, Operator: allocation.OpExists})),
			wantLabel: "tagged",
		},
		{
			name:      "hostile one_of member",
			records:   []focus.CostRecord{testRecord(t, oneOfEvil, day(1), "1")},
			dim:       dim(rule("picked", allocation.Condition{Dimension: "service_name", Operator: allocation.OpOneOf, Values: []string{"normal", oneOfEvil}})),
			wantLabel: "picked",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := allocStore(t, tt.records...)
			before := rowCount(t, store)
			got, err := store.DailyCostsByAllocation(context.Background(), focus.DefaultTenant, time.Time{}, time.Time{}, tt.dim)
			if err != nil {
				t.Fatalf("DailyCostsByAllocation: %v", err)
			}
			if after := rowCount(t, store); after != before {
				t.Fatalf("cost_records row count %d → %d: an injected statement executed", before, after)
			}
			labels := flattenLabels(got)
			if _, ok := labels[tt.wantLabel]; !ok {
				t.Errorf("label %q did not round-trip verbatim; got labels %v", tt.wantLabel, labels)
			}
		})
	}
}

// TestDailyCostsByAllocationMatching is the table-driven proof of every operator
// and tag semantic against real stored data: tag equals/exists, NULL tags and a
// JSON-null tag value (neither satisfies exists), a dotted tag key extracted
// literally, one_of / contains (with a literal '%') / starts_with on columns,
// exists on a column against ” and NULL, and byte-exact case sensitivity.
func TestDailyCostsByAllocationMatching(t *testing.T) {
	tests := []struct {
		name    string
		records []focus.CostRecord
		dim     allocation.Dimension
		want    map[string]string
	}{
		{
			name: "tag equals is case-sensitive",
			records: []focus.CostRecord{
				withTags(testRecord(t, "a", day(1), "1"), map[string]any{"env": "prod"}),
				withTags(testRecord(t, "b", day(1), "2"), map[string]any{"env": "dev"}),
				withTags(testRecord(t, "c", day(1), "3"), map[string]any{"env": "Prod"}), // case differs → no match
			},
			dim:  dim(rule("prod", allocation.Condition{Dimension: "tag:env", Operator: allocation.OpEquals, Value: sp("prod")})),
			want: map[string]string{"prod": "1", "Unallocated": "5"},
		},
		{
			name: "tag exists: NULL tags and JSON-null value both fail",
			records: []focus.CostRecord{
				withTags(testRecord(t, "a", day(1), "1"), map[string]any{"env": "anything"}), // exists
				withTags(testRecord(t, "b", day(1), "2"), nil),                               // NULL tags → Unallocated
				withTags(testRecord(t, "c", day(1), "3"), map[string]any{"env": nil}),        // JSON null → not exists
				withTags(testRecord(t, "d", day(1), "4"), map[string]any{"other": "x"}),      // env absent → not exists
			},
			dim:  dim(rule("tagged", allocation.Condition{Dimension: "tag:env", Operator: allocation.OpExists})),
			want: map[string]string{"tagged": "1", "Unallocated": "9"},
		},
		{
			name: "dotted tag key extracts literally",
			records: []focus.CostRecord{
				withTags(testRecord(t, "a", day(1), "1"), map[string]any{"a.b": "yes"}), // literal key a.b
				withTags(testRecord(t, "b", day(1), "2"), map[string]any{"a.b": "no"}),  // present but different value
			},
			dim:  dim(rule("matched", allocation.Condition{Dimension: "tag:a.b", Operator: allocation.OpEquals, Value: sp("yes")})),
			want: map[string]string{"matched": "1", "Unallocated": "2"},
		},
		{
			name: "one_of on a column",
			records: []focus.CostRecord{
				testRecord(t, "AWS Lambda", day(1), "1"),
				testRecord(t, "Amazon S3 Glacier", day(1), "2"),
				testRecord(t, "Other", day(1), "3"),
			},
			dim:  dim(rule("picked", allocation.Condition{Dimension: "service_name", Operator: allocation.OpOneOf, Values: []string{"AWS Lambda", "Amazon S3 Glacier"}})),
			want: map[string]string{"picked": "3", "Unallocated": "3"},
		},
		{
			name: "contains matches a literal percent",
			records: []focus.CostRecord{
				withCol(testRecord(t, "a", day(1), "1"), func(r *focus.CostRecord) { r.ChargeDescription = "discount 50% off" }),
				withCol(testRecord(t, "b", day(1), "2"), func(r *focus.CostRecord) { r.ChargeDescription = "discount 50 off" }),
			},
			dim:  dim(rule("pct", allocation.Condition{Dimension: "charge_description", Operator: allocation.OpContains, Value: sp("50%")})),
			want: map[string]string{"pct": "1", "Unallocated": "2"},
		},
		{
			name: "starts_with on a column",
			records: []focus.CostRecord{
				testRecord(t, "Amazon Elastic Compute Cloud", day(1), "1"),
				testRecord(t, "Amazon Simple Storage Service", day(1), "2"),
			},
			dim:  dim(rule("ec2", allocation.Condition{Dimension: "service_name", Operator: allocation.OpStartsWith, Value: sp("Amazon Elastic")})),
			want: map[string]string{"ec2": "1", "Unallocated": "2"},
		},
		{
			name: "exists on a column: NULL fails",
			records: []focus.CostRecord{
				withCol(testRecord(t, "a", day(1), "1"), func(r *focus.CostRecord) { r.ResourceName = "i-123" }),
				withCol(testRecord(t, "b", day(1), "2"), func(r *focus.CostRecord) { r.ResourceName = "" }), // "" → SQL NULL
			},
			dim:  dim(rule("hasres", allocation.Condition{Dimension: "resource_name", Operator: allocation.OpExists})),
			want: map[string]string{"hasres": "1", "Unallocated": "2"},
		},
		{
			name: "exists on a column: empty string fails",
			records: []focus.CostRecord{
				withCol(testRecord(t, "a", day(1), "1"), func(r *focus.CostRecord) { r.ServiceCategory = "Compute" }),
				withCol(testRecord(t, "b", day(1), "2"), func(r *focus.CostRecord) { r.ServiceCategory = "" }), // NOT NULL '' → not exists
			},
			dim:  dim(rule("hascat", allocation.Condition{Dimension: "service_category", Operator: allocation.OpExists})),
			want: map[string]string{"hascat": "1", "Unallocated": "2"},
		},
		{
			name: "equals on a column is case-sensitive",
			records: []focus.CostRecord{
				withCol(testRecord(t, "a", day(1), "1"), func(r *focus.CostRecord) { r.SubAccountName = "prod" }),
				withCol(testRecord(t, "b", day(1), "2"), func(r *focus.CostRecord) { r.SubAccountName = "Prod" }),
			},
			dim:  dim(rule("prod", allocation.Condition{Dimension: "sub_account_name", Operator: allocation.OpEquals, Value: sp("prod")})),
			want: map[string]string{"prod": "1", "Unallocated": "2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := allocStore(t, tt.records...)
			got, err := store.DailyCostsByAllocation(context.Background(), focus.DefaultTenant, time.Time{}, time.Time{}, tt.dim)
			if err != nil {
				t.Fatalf("DailyCostsByAllocation: %v", err)
			}
			assertLabels(t, got, tt.want)
		})
	}
}

// TestDailyCostsByAllocationTenantCurrencyRange covers tenant scoping (other
// tenants invisible), the single-currency guard (byte-identical to
// DailyCostsByService's message), and inclusive start/end range filtering.
func TestDailyCostsByAllocationTenantCurrencyRange(t *testing.T) {
	ctx := context.Background()
	d := dim(rule("ec2", allocation.Condition{Dimension: "service_name", Operator: allocation.OpStartsWith, Value: sp("Amazon Elastic")}))

	// --- tenant scoping ---
	acme := testRecord(t, "Amazon Elastic Compute Cloud", day(1), "9")
	acme.XTenantID = "acme"
	store := allocStore(t,
		testRecord(t, "Amazon Elastic Compute Cloud", day(1), "1"),
		acme,
	)
	got, err := store.DailyCostsByAllocation(ctx, focus.DefaultTenant, time.Time{}, time.Time{}, d)
	if err != nil {
		t.Fatalf("DailyCostsByAllocation(default): %v", err)
	}
	assertLabels(t, got, map[string]string{"ec2": "1"}) // acme's 9 is invisible

	other, err := store.DailyCostsByAllocation(ctx, "acme", time.Time{}, time.Time{}, d)
	if err != nil {
		t.Fatalf("DailyCostsByAllocation(acme): %v", err)
	}
	assertLabels(t, other, map[string]string{"ec2": "9"})

	// --- start/end range filtering (inclusive calendar days) ---
	ranged := allocStore(t,
		testRecord(t, "Amazon Elastic Compute Cloud", day(1), "1"),
		testRecord(t, "Amazon Elastic Compute Cloud", day(2), "2"),
		testRecord(t, "Amazon Elastic Compute Cloud", day(3), "4"),
	)
	got, err = ranged.DailyCostsByAllocation(ctx, focus.DefaultTenant, day(2), day(2), d)
	if err != nil {
		t.Fatalf("DailyCostsByAllocation(ranged): %v", err)
	}
	if len(got.Days) != 1 || !got.Days[0].Date.Equal(day(2)) {
		t.Fatalf("ranged days = %+v, want only %s", got.Days, day(2))
	}
	assertLabels(t, got, map[string]string{"ec2": "2"})

	// --- single-currency guard: byte-identical message to DailyCostsByService ---
	eur := testRecord(t, "Amazon Elastic Compute Cloud", day(1), "0.5")
	eur.BillingCurrency = "EUR"
	mixed := allocStore(t,
		testRecord(t, "Amazon Elastic Compute Cloud", day(1), "1"), // USD
		eur,
	)
	_, allocErr := mixed.DailyCostsByAllocation(ctx, focus.DefaultTenant, time.Time{}, time.Time{}, d)
	_, svcErr := mixed.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{})
	if allocErr == nil || svcErr == nil {
		t.Fatalf("mixed-currency errors = alloc:%v svc:%v, want both non-nil", allocErr, svcErr)
	}
	if allocErr.Error() != svcErr.Error() {
		t.Errorf("currency-guard message differs:\n alloc: %q\n svc:   %q", allocErr.Error(), svcErr.Error())
	}
	if !strings.Contains(allocErr.Error(), "mix billing currencies") {
		t.Errorf("currency-guard message = %q, want the mixed-currency guard", allocErr.Error())
	}
}

// TestAllocationColumnMapMatchesExportedSet is the set-equality guard: storage's
// hardcoded column map keys are SET-EQUAL to allocation.ColumnDimensions() (so
// the two closed sets can never drift), and each mapped literal is identical to
// its dimension name.
func TestAllocationColumnMapMatchesExportedSet(t *testing.T) {
	exported := map[string]bool{}
	for _, d := range allocation.ColumnDimensions() {
		exported[d] = true
	}
	if len(allocationColumns) != len(exported) {
		t.Fatalf("allocationColumns has %d keys, allocation.ColumnDimensions() has %d", len(allocationColumns), len(exported))
	}
	for k, col := range allocationColumns {
		if !exported[k] {
			t.Errorf("allocationColumns key %q is not an exported allocation dimension", k)
		}
		if col != k {
			t.Errorf("allocationColumns[%q] = %q, want the identical column literal", k, col)
		}
	}
	for k := range exported {
		if _, ok := allocationColumns[k]; !ok {
			t.Errorf("exported allocation dimension %q is missing from allocationColumns", k)
		}
	}
}
