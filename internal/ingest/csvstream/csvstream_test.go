// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package csvstream

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// TestStreamKeysRowsAndNumbers proves rows are keyed by header name (not
// position) and numbered from the given offset.
func TestStreamKeysRowsAndNumbers(t *testing.T) {
	s, err := New(strings.NewReader("ServiceName,BilledCost\nAWS Lambda,0.1264\nAmazon S3,0.5\n"), 10)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := s.Header(); len(got) != 2 || got[0] != "ServiceName" || got[1] != "BilledCost" {
		t.Fatalf("Header = %v", got)
	}
	row, err := s.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	// Numbering continues from rowOffset (10) -> first data row is 11.
	if row.Number != 11 {
		t.Errorf("first row number = %d, want 11", row.Number)
	}
	if row.Record["ServiceName"] != "AWS Lambda" || row.Record["BilledCost"] != "0.1264" {
		t.Errorf("row = %v, want header-keyed values", row.Record)
	}
	if _, err := s.Next(); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if _, err := s.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("third Next err = %v, want io.EOF", err)
	}
	if s.Rows() != 12 {
		t.Errorf("Rows = %d, want 12", s.Rows())
	}
}

// TestStreamStripsBOM proves a leading UTF-8 BOM does not contaminate the
// first header name (U+FEFF is the BOM; a literal BOM in Go source is illegal
// outside the file start).
func TestStreamStripsBOM(t *testing.T) {
	const bom = "\uFEFF"
	s, err := New(strings.NewReader(bom+"ServiceName,BilledCost\nAWS Lambda,0.1\n"), 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	row, err := s.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if _, ok := row.Record["ServiceName"]; !ok {
		t.Errorf("first column not keyed as ServiceName (BOM leaked): %v", row.Record)
	}
	for col := range row.Record {
		if strings.HasPrefix(col, bom) {
			t.Errorf("column %q still carries the BOM", col)
		}
	}
}
