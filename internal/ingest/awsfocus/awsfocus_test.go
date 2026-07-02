// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package awsfocus

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleExport = "../../../testdata/aws-focus-1.2/sample-export.csv.gz"

func TestConnectorIdentity(t *testing.T) {
	c := New("/some/dir/sample-export.csv.gz")
	if c.Name() != "aws-focus" {
		t.Errorf("Name = %q, want aws-focus", c.Name())
	}
	if got := c.FOCUSVersion(); string(got) != "1.2" {
		t.Errorf("FOCUSVersion = %q, want 1.2", got)
	}
	// The logical source identity of a file source is its base name.
	if got := c.SourceIdentity(); got != "sample-export.csv.gz" {
		t.Errorf("SourceIdentity = %q, want sample-export.csv.gz", got)
	}
}

func TestContentHashIsStable(t *testing.T) {
	c := New(sampleExport)
	h1, err := c.ContentHash(context.Background())
	if err != nil {
		t.Fatalf("ContentHash: %v", err)
	}
	h2, err := c.ContentHash(context.Background())
	if err != nil {
		t.Fatalf("ContentHash: %v", err)
	}
	if h1 != h2 || len(h1) != len("sha256:")+64 {
		t.Errorf("ContentHash = %q / %q, want one stable sha256: digest", h1, h2)
	}
}

// TestRecordsParsesHeaderAndRows proves header-driven row parsing against
// the committed sample export.
func TestRecordsParsesHeaderAndRows(t *testing.T) {
	reader, err := New(sampleExport).Records(context.Background())
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	defer func() { _ = reader.Close() }()

	row, err := reader.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if row.Number != 1 {
		t.Errorf("first data row number = %d, want 1", row.Number)
	}
	want := map[string]string{
		"BilledCost":        "2.4192",
		"BillingCurrency":   "USD",
		"ChargeCategory":    "Usage",
		"ChargePeriodStart": "2026-05-01T00:00:00.000Z",
		"ProviderName":      "AWS",
		"PublisherName":     "AWS",
		"InvoiceIssuerName": "Amazon Web Services, Inc.",
		"ServiceName":       "Amazon Elastic Compute Cloud",
		"SubAccountId":      "111111111111",
		"ConsumedQuantity":  "24",
		"ChargeClass":       "", // empty CSV field parses as null
		"x_ServiceCode":     "AmazonEC2",
	}
	for col, wantVal := range want {
		if got, ok := row.Record[col]; !ok || got != wantVal {
			t.Errorf("row 1 column %s = %q (present=%v), want %q", col, got, ok, wantVal)
		}
	}

	count := 1
	for {
		row, err = reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		count++
		if row.Number != count {
			t.Fatalf("row number = %d, want %d", row.Number, count)
		}
	}
	if count != 42 {
		t.Errorf("data rows = %d, want 42", count)
	}
}

// TestRecordsStripsUTF8BOM proves a BOM'd export parses with the first
// header column intact — without stripping, the BOM becomes part of the
// first header name and silently nulls that column on every record.
func TestRecordsStripsUTF8BOM(t *testing.T) {
	reader, err := New("../../../testdata/aws-focus-1.2/bom-export.csv.gz").Records(context.Background())
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	defer func() { _ = reader.Close() }()

	row, err := reader.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got := row.Record["AvailabilityZone"]; got != "eu-central-1a" {
		t.Errorf("first column AvailabilityZone = %q, want eu-central-1a", got)
	}
	for col := range row.Record {
		if strings.HasPrefix(col, "\uFEFF") {
			t.Errorf("column %q still carries the BOM", col)
		}
	}
}

// TestRecordsHeaderOrderIndependent proves columns are keyed by header
// name, not position (AWS does not guarantee column order).
func TestRecordsHeaderOrderIndependent(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte("ServiceName,BilledCost\nAWS Lambda,0.1264\n")); err != nil {
		t.Fatalf("writing test gzip: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("closing test gzip: %v", err)
	}
	path := filepath.Join(t.TempDir(), "reordered.csv.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("writing test export: %v", err)
	}

	reader, err := New(path).Records(context.Background())
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	defer func() { _ = reader.Close() }()
	row, err := reader.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if row.Record["ServiceName"] != "AWS Lambda" || row.Record["BilledCost"] != "0.1264" {
		t.Errorf("row = %v, want header-keyed values", row.Record)
	}
}
