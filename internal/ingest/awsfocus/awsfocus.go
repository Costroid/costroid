// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package awsfocus implements the "aws-focus" connector (decisions D16,
// D21): it reads an already-downloaded AWS Data Exports "FOCUS 1.2 with
// AWS columns" export — a gzipped CSV — from a local path. Live S3 sync
// lives in the sibling awsfocuss3 package; this package never touches
// the network.
//
// The connector keeps NO incremental sync state (unlike aws-focus-s3's
// manifest tuple): reading a local file has no fetch cost to save, and
// the pipeline's content-hash short-circuit already makes re-ingesting
// an unchanged file a no-op.
package awsfocus

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/ingest"
	"github.com/Costroid/costroid/internal/ingest/csvstream"
)

// Name is the connector's registry name.
const Name = "aws-focus"

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
	stream, err := NewGzipCSVStream(f, 0)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("reading export %s: %w", c.path, err)
	}
	return &reader{file: f, stream: stream}, nil
}

type reader struct {
	file   *os.File
	stream *GzipCSVStream
}

// Next implements ingest.RecordReader.
func (r *reader) Next() (ingest.Row, error) { return r.stream.Next() }

// Close implements ingest.RecordReader.
func (r *reader) Close() error {
	gzErr := r.stream.Close()
	if err := r.file.Close(); err != nil {
		return err
	}
	return gzErr
}

// GzipCSVStream parses one gzipped AWS FOCUS CSV stream: it gunzips r, then
// hands the decompressed bytes to the shared csvstream core (BOM strip,
// header-keyed rows, row numbering from rowOffset so multi-chunk exports —
// each chunk a complete CSV with its own header — keep coherent numbering).
// It is the gzip-mandatory wrapper shared by the local-file connector, the
// S3 connector, and the Azure connector; the plain-or-gzip focus-csv
// connector layers gzip itself and calls the csvstream core directly.
type GzipCSVStream struct {
	gz     *gzip.Reader
	stream *csvstream.Stream
}

// NewGzipCSVStream opens a gzipped CSV stream over r and consumes its
// header row. Row numbering continues from rowOffset (0 for the first or
// only chunk).
func NewGzipCSVStream(r io.Reader, rowOffset int) (*GzipCSVStream, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("reading gzip: %w", err)
	}
	stream, err := csvstream.New(gz, rowOffset)
	if err != nil {
		_ = gz.Close()
		return nil, err
	}
	return &GzipCSVStream{gz: gz, stream: stream}, nil
}

// Next returns the next data row, or io.EOF after the last one.
func (s *GzipCSVStream) Next() (ingest.Row, error) { return s.stream.Next() }

// Rows returns the number of the last data row read.
func (s *GzipCSVStream) Rows() int { return s.stream.Rows() }

// Close releases the gzip reader; the caller owns the underlying reader.
func (s *GzipCSVStream) Close() error { return s.gz.Close() }
