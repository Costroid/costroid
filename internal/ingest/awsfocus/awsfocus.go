// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package awsfocus implements the "aws-focus" connector (decisions D16,
// D21): it reads an already-downloaded AWS Data Exports "FOCUS 1.2 with
// AWS columns" export — a gzipped CSV — from a local path. Live S3 sync
// and credentials are a later slice on the same connector contract; this
// package never touches the network.
package awsfocus

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/ingest"
)

// Name is the connector's registry name.
const Name = "aws-focus"

// utf8BOM is the UTF-8 byte order mark some tools prepend to CSV files.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Connector reads one local AWS FOCUS 1.2 export file.
type Connector struct {
	path string
}

var _ ingest.Connector = (*Connector)(nil)

// New returns a connector for the export file at path.
func New(path string) *Connector {
	return &Connector{path: path}
}

// Name implements ingest.Connector.
func (c *Connector) Name() string { return Name }

// FOCUSVersion implements ingest.Connector: AWS Data Exports produces
// FOCUS 1.2 (with proprietary x_ columns, which the pipeline drops).
func (c *Connector) FOCUSVersion() focus.Version { return focus.V1_2 }

// SourceIdentity implements ingest.Connector: the export file's base
// name, the default logical identity for file sources.
func (c *Connector) SourceIdentity() string { return filepath.Base(c.path) }

// ContentHash implements ingest.Connector: the SHA-256 of the file's
// bytes as stored on disk.
func (c *Connector) ContentHash(_ context.Context) (string, error) {
	f, err := os.Open(c.path)
	if err != nil {
		return "", fmt.Errorf("opening export file: %w", err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hashing export file: %w", err)
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil)), nil
}

// Records implements ingest.Connector. Column order in AWS exports is
// not guaranteed, so the header row is parsed dynamically and each data
// row is keyed by its header names.
func (c *Connector) Records(_ context.Context) (ingest.RecordReader, error) {
	f, err := os.Open(c.path)
	if err != nil {
		return nil, fmt.Errorf("opening export file: %w", err)
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("reading gzip export %s: %w", c.path, err)
	}
	// A UTF-8 BOM would otherwise become part of the first header name,
	// silently nulling the first column of every record.
	br := bufio.NewReader(gz)
	if bom, err := br.Peek(len(utf8BOM)); err == nil && bytes.Equal(bom, utf8BOM) {
		_, _ = br.Discard(len(utf8BOM))
	}
	cr := csv.NewReader(br)
	cr.ReuseRecord = true

	header, err := cr.Read()
	if err != nil {
		_ = gz.Close()
		_ = f.Close()
		return nil, fmt.Errorf("reading export CSV header: %w", err)
	}
	return &reader{file: f, gz: gz, csv: cr, header: append([]string(nil), header...)}, nil
}

type reader struct {
	file   *os.File
	gz     *gzip.Reader
	csv    *csv.Reader
	header []string
	row    int
}

// Next implements ingest.RecordReader.
func (r *reader) Next() (ingest.Row, error) {
	fields, err := r.csv.Read()
	if err == io.EOF {
		return ingest.Row{}, io.EOF
	}
	r.row++
	if err != nil {
		return ingest.Row{}, fmt.Errorf("reading export CSV row %d: %w", r.row, err)
	}
	rec := make(focus.RawRecord, len(r.header))
	for i, name := range r.header {
		rec[name] = fields[i]
	}
	return ingest.Row{Number: r.row, Record: rec}, nil
}

// Close implements ingest.RecordReader.
func (r *reader) Close() error {
	gzErr := r.gz.Close()
	if err := r.file.Close(); err != nil {
		return err
	}
	return gzErr
}
