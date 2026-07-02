// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package ingest_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/ingest"
	"github.com/Costroid/costroid/internal/ingest/awsfocus"
	"github.com/Costroid/costroid/internal/storage"
)

const (
	sampleExport  = "../../testdata/aws-focus-1.2/sample-export.csv.gz"
	invalidExport = "../../testdata/aws-focus-1.2/invalid-export.csv.gz"
	bomExport     = "../../testdata/aws-focus-1.2/bom-export.csv.gz"
)

func openStore(t *testing.T) *storage.DuckDB {
	t.Helper()
	store, err := storage.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestRunSampleExport proves the full pipeline against the committed
// synthetic sample: 3 services × 7 days × 2 sub-accounts with known
// expected totals (the same expectations the end-to-end curl check uses).
func TestRunSampleExport(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)

	res, err := ingest.Run(ctx, awsfocus.New(sampleExport), store, focus.DefaultTenant)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Records != 42 || res.Unchanged {
		t.Fatalf("Run = %+v, want 42 fresh records", res)
	}
	if res.Batch.Connector != "aws-focus" || res.Batch.SourceIdentity != "sample-export.csv.gz" {
		t.Errorf("batch identity = %s/%s, want aws-focus/sample-export.csv.gz", res.Batch.Connector, res.Batch.SourceIdentity)
	}
	if !strings.HasPrefix(res.Batch.ContentHash, "sha256:") {
		t.Errorf("content hash = %q, want a sha256: digest", res.Batch.ContentHash)
	}

	daily, err := store.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyCostsByService: %v", err)
	}
	assertSampleTotals(t, daily)

	// Idempotent re-ingest: same file, same store — totals identical,
	// batch short-circuited as unchanged.
	res2, err := ingest.Run(ctx, awsfocus.New(sampleExport), store, focus.DefaultTenant)
	if err != nil {
		t.Fatalf("re-ingest Run: %v", err)
	}
	if !res2.Unchanged || res2.Records != 42 {
		t.Fatalf("re-ingest Run = %+v, want unchanged with 42 records", res2)
	}
	again, err := store.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyCostsByService after re-ingest: %v", err)
	}
	assertSampleTotals(t, again)
}

// assertSampleTotals asserts the sample's known expected values.
func assertSampleTotals(t *testing.T, daily storage.DailyCosts) {
	t.Helper()
	if daily.Currency != "USD" {
		t.Errorf("Currency = %q, want USD", daily.Currency)
	}
	if len(daily.Days) != 7 {
		t.Fatalf("days = %d, want 7", len(daily.Days))
	}
	wantServices := map[string]string{
		"AWS Lambda":                    "0.1896",
		"Amazon Elastic Compute Cloud":  "3.6288",
		"Amazon Simple Storage Service": "0.8625",
	}
	for i, dayCosts := range daily.Days {
		wantDate := time.Date(2026, 5, i+1, 0, 0, 0, 0, time.UTC)
		if !dayCosts.Date.Equal(wantDate) {
			t.Errorf("day %d date = %s, want %s", i, dayCosts.Date, wantDate)
		}
		if len(dayCosts.Services) != 3 {
			t.Fatalf("day %d services = %+v, want 3", i, dayCosts.Services)
		}
		prev := ""
		for _, svc := range dayCosts.Services {
			if svc.ServiceName <= prev {
				t.Errorf("day %d services out of order: %q after %q", i, svc.ServiceName, prev)
			}
			prev = svc.ServiceName
			if want := wantServices[svc.ServiceName]; svc.Cost.String() != want {
				t.Errorf("day %d %s = %s, want %s", i, svc.ServiceName, svc.Cost, want)
			}
		}
	}
}

// TestRunBOMExport proves a UTF-8-BOM'd export (the sample data with a
// BOM prepended) ingests completely, with the first column intact.
func TestRunBOMExport(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)

	res, err := ingest.Run(ctx, awsfocus.New(bomExport), store, focus.DefaultTenant)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Records != 42 || res.Unchanged {
		t.Fatalf("Run = %+v, want 42 fresh records", res)
	}
	daily, err := store.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyCostsByService: %v", err)
	}
	assertSampleTotals(t, daily)
}

// TestRunRejectsOverScaleValues proves values with more fractional digits
// than the store holds exactly (storage.MaxDecimalScale) abort the ingest
// with a row-numbered error instead of being silently rounded.
func TestRunRejectsOverScaleValues(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)

	header := "BilledCost,EffectiveCost,ListCost,ContractedCost,BillingCurrency," +
		"BillingPeriodStart,BillingPeriodEnd,ChargeCategory,ChargePeriodStart,ChargePeriodEnd," +
		"BillingAccountId,ServiceName,ServiceCategory,ProviderName,InvoiceIssuerName"
	row := "0.1234567890123456789,1,1,1,USD," + // 19 fractional digits
		"2026-05-01T00:00:00Z,2026-06-01T00:00:00Z,Usage,2026-05-01T00:00:00Z,2026-05-02T00:00:00Z," +
		"999999999999,AWS Lambda,Compute,AWS,\"Amazon Web Services, Inc.\""
	path := writeGzCSV(t, header+"\n"+row+"\n")

	_, err := ingest.Run(ctx, awsfocus.New(path), store, focus.DefaultTenant)
	if err == nil {
		t.Fatal("Run accepted a value exceeding the store's decimal scale")
	}
	var rowErrs *ingest.RowErrors
	if !errors.As(err, &rowErrs) {
		t.Fatalf("Run error = %v (%T), want *ingest.RowErrors", err, err)
	}
	if rowErrs.Total != 1 || len(rowErrs.First) != 1 || rowErrs.First[0].Row != 1 {
		t.Fatalf("RowErrors = %+v, want exactly row 1", rowErrs)
	}
	msg := rowErrs.First[0].Errs[0].Error()
	if !strings.Contains(msg, "BilledCost") || !strings.Contains(msg, "more than 18 fractional digits") {
		t.Errorf("row error %q, want the column name and the 18-digit scale limit", msg)
	}

	// Trailing zeros beyond the limit lose nothing and must pass.
	okRow := strings.Replace(row, "0.1234567890123456789", "0.1234567890123456700", 1)
	okPath := writeGzCSV(t, header+"\n"+okRow+"\n")
	res, err := ingest.Run(ctx, awsfocus.New(okPath), store, focus.DefaultTenant)
	if err != nil {
		t.Fatalf("Run rejected a trailing-zero value: %v", err)
	}
	if res.Records != 1 {
		t.Fatalf("Run = %+v, want 1 record", res)
	}
}

func writeGzCSV(t *testing.T, content string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(content)); err != nil {
		t.Fatalf("writing test gzip: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("closing test gzip: %v", err)
	}
	path := filepath.Join(t.TempDir(), "export.csv.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("writing test export: %v", err)
	}
	return path
}

// TestRunInvalidExportAbortsWithRowNumbers proves validation failures
// abort the whole ingest — nothing stored — and report offending rows by
// number and rule.
func TestRunInvalidExportAbortsWithRowNumbers(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)

	_, err := ingest.Run(ctx, awsfocus.New(invalidExport), store, focus.DefaultTenant)
	if err == nil {
		t.Fatal("Run accepted the invalid export")
	}
	var rowErrs *ingest.RowErrors
	if !errors.As(err, &rowErrs) {
		t.Fatalf("Run error = %v (%T), want *ingest.RowErrors", err, err)
	}
	if rowErrs.Total != 4 {
		t.Errorf("offending rows = %d, want 4", rowErrs.Total)
	}
	wantRows := map[int]string{
		2: "CAU-ChargeCategory-C-003-M",  // ChargeCategory "Consumption"
		3: "CAU-ChargePeriodEnd-C-004-M", // start not before end
		4: "CAU-BilledCost-C-001-M",      // "12,34" is not a decimal
		5: "CAU-BillingCurrency-C-004-M", // null currency
	}
	for _, re := range rowErrs.First {
		wantRule, ok := wantRows[re.Row]
		if !ok {
			t.Errorf("unexpected offending row %d: %v", re.Row, re.Errs)
			continue
		}
		delete(wantRows, re.Row)
		found := false
		for _, err := range re.Errs {
			if strings.Contains(err.Error(), wantRule) {
				found = true
			}
		}
		if !found {
			t.Errorf("row %d errors %v, want rule %s", re.Row, re.Errs, wantRule)
		}
	}
	for row, rule := range wantRows {
		t.Errorf("missing offending row %d (rule %s)", row, rule)
	}

	// No partial load: the store holds nothing.
	daily, err := store.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyCostsByService: %v", err)
	}
	if len(daily.Days) != 0 {
		t.Errorf("store holds %d day(s) after an aborted ingest, want none", len(daily.Days))
	}
}
