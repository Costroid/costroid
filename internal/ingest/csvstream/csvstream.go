// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package csvstream is the shared, header-keyed CSV reading core the FOCUS
// CSV connectors build on. It strips a leading UTF-8 BOM, consumes the
// header row, keys each data row by its header names into a
// focus.RawRecord, and numbers data rows from a caller-supplied offset so a
// multi-chunk source (each chunk a complete CSV with its own header) keeps
// coherent numbering.
//
// It reads a PLAIN (already-decompressed) byte stream — gzip is an optional
// layer the caller adds (awsfocus wraps a gzip.Reader before handing the
// stream here; focus-csv layers gzip only when the source's magic bytes say
// so). The core lived inside the awsfocus package; extracting it lets the
// aws-focus-s3, azure-focus, and focus-csv connectors share it without any
// of them importing another connector's package.
package csvstream

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"fmt"
	"io"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/ingest"
)

// utf8BOM is the UTF-8 byte order mark some tools prepend to CSV files. Left
// in place it would become part of the first header name, silently nulling
// the first column of every record.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Stream parses one CSV stream: it strips a UTF-8 BOM if present, keys each
// data row by the stream's own header row, and numbers rows from the offset
// passed to New.
type Stream struct {
	csv    *csv.Reader
	header []string
	row    int
}

// New opens a CSV stream over r (already decompressed) and consumes its
// header row. Row numbering continues from rowOffset (0 for the first or
// only chunk).
func New(r io.Reader, rowOffset int) (*Stream, error) {
	br := bufio.NewReader(r)
	if bom, err := br.Peek(len(utf8BOM)); err == nil && bytes.Equal(bom, utf8BOM) {
		_, _ = br.Discard(len(utf8BOM))
	}
	cr := csv.NewReader(br)
	cr.ReuseRecord = true

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("reading CSV header: %w", err)
	}
	return &Stream{csv: cr, header: append([]string(nil), header...), row: rowOffset}, nil
}

// Header returns the stream's parsed header names.
func (s *Stream) Header() []string { return s.header }

// Next returns the next data row, or io.EOF after the last one.
func (s *Stream) Next() (ingest.Row, error) {
	fields, err := s.csv.Read()
	if err == io.EOF {
		return ingest.Row{}, io.EOF
	}
	s.row++
	if err != nil {
		return ingest.Row{}, fmt.Errorf("reading CSV row %d: %w", s.row, err)
	}
	rec := make(focus.RawRecord, len(s.header))
	for i, name := range s.header {
		rec[name] = fields[i]
	}
	return ingest.Row{Number: s.row, Record: rec}, nil
}

// Rows returns the number of the last data row read.
func (s *Stream) Rows() int { return s.row }
