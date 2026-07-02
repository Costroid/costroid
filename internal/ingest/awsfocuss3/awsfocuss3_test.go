// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package awsfocuss3_test

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/devtools/fakes3"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/ingest"
	"github.com/Costroid/costroid/internal/ingest/awsfocus"
	"github.com/Costroid/costroid/internal/ingest/awsfocuss3"
	"github.com/Costroid/costroid/internal/storage"
)

const (
	fixture      = "../../../testdata/aws-focus-s3/fixture"
	restated     = "../../../testdata/aws-focus-s3/restated"
	sampleExport = "../../../testdata/aws-focus-1.2/sample-export.csv.gz"
	bucket       = "demo"
	prefix       = "exports/costroid-demo"
)

// hermeticEnv pins the ENTIRE ambient AWS credential chain to test-local
// values so the tests pass identically on any machine: static test
// credentials (or none), no shared config, no IMDS probing, and the
// given fake endpoint.
func hermeticEnv(t *testing.T, endpoint string, withCreds bool) {
	t.Helper()
	if withCreds {
		t.Setenv("AWS_ACCESS_KEY_ID", "test")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	} else {
		t.Setenv("AWS_ACCESS_KEY_ID", "")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	}
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_CONFIG_FILE", "/dev/null")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/dev/null")
	t.Setenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "")
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", "")
	t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", "")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ENDPOINT_URL_S3", endpoint)
}

func startFake(t *testing.T, dir string) (*fakes3.Handler, string) {
	t.Helper()
	h := fakes3.New(dir)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return h, srv.URL
}

func TestDiscoverFixture(t *testing.T) {
	_, url := startFake(t, fixture)
	hermeticEnv(t, url, true)

	conns, err := awsfocuss3.Discover(context.Background(), bucket, prefix)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(conns) != 2 {
		t.Fatalf("Discover found %d period(s), want 2", len(conns))
	}
	wantIdentity := map[string]string{
		"2026-05": "demo/exports/costroid-demo/2026-05",
		"2026-06": "demo/exports/costroid-demo/2026-06",
	}
	hashes := map[string]string{}
	for i, want := range []string{"2026-05", "2026-06"} {
		c := conns[i]
		if c.Name() != "aws-focus-s3" {
			t.Errorf("Name = %q, want aws-focus-s3", c.Name())
		}
		if string(c.FOCUSVersion()) != "1.2" {
			t.Errorf("FOCUSVersion = %q, want 1.2", c.FOCUSVersion())
		}
		if c.BillingPeriod() != want {
			t.Errorf("period %d = %q, want %q (oldest first)", i, c.BillingPeriod(), want)
		}
		if got := c.SourceIdentity(); got != wantIdentity[want] {
			t.Errorf("SourceIdentity = %q, want %q", got, wantIdentity[want])
		}
		hash, err := c.ContentHash(context.Background())
		if err != nil {
			t.Fatalf("ContentHash: %v", err)
		}
		if !strings.HasPrefix(hash, "sha256:") || len(hash) != len("sha256:")+64 {
			t.Errorf("ContentHash = %q, want a sha256: digest", hash)
		}
		again, err := c.ContentHash(context.Background())
		if err != nil || again != hash {
			t.Errorf("ContentHash not stable: %q vs %q (%v)", hash, again, err)
		}
		hashes[want] = hash
	}
	if hashes["2026-05"] == hashes["2026-06"] {
		t.Error("different periods produced the same content hash")
	}
}

// TestRecordsEquivalentToLocalConnector proves the multi-chunk S3 stream
// (two chunks, each with its own header row) yields exactly the rows the
// local-file connector reads from the equivalent single-file export,
// with row numbering coherent across the chunk boundary.
func TestRecordsEquivalentToLocalConnector(t *testing.T) {
	ctx := context.Background()
	_, url := startFake(t, fixture)
	hermeticEnv(t, url, true)

	conns, err := awsfocuss3.Discover(ctx, bucket, prefix)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	s3Rows := readAll(t, ctx, conns[0]) // 2026-05: the sample data split into two chunks

	local := awsfocus.New(sampleExport)
	localRows := readAll(t, ctx, local)

	if len(s3Rows) != len(localRows) {
		t.Fatalf("S3 connector read %d rows, local connector %d", len(s3Rows), len(localRows))
	}
	for i := range s3Rows {
		if s3Rows[i].Number != i+1 {
			t.Fatalf("row %d numbered %d, want %d (numbering must span chunks)", i, s3Rows[i].Number, i+1)
		}
		if len(s3Rows[i].Record) != len(localRows[i].Record) {
			t.Fatalf("row %d has %d columns via S3, %d locally", i+1, len(s3Rows[i].Record), len(localRows[i].Record))
		}
		for col, want := range localRows[i].Record {
			if got := s3Rows[i].Record[col]; got != want {
				t.Errorf("row %d column %s = %q via S3, want %q", i+1, col, got, want)
			}
		}
	}
}

func readAll(t *testing.T, ctx context.Context, conn ingest.Connector) []ingest.Row {
	t.Helper()
	reader, err := conn.Records(ctx)
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	defer func() { _ = reader.Close() }()
	var rows []ingest.Row
	for {
		row, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return rows
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		rows = append(rows, row)
	}
}

// TestIngestIdempotencyAndRestatement runs the full pipeline against the
// fake: fresh ingest of both periods, unchanged-content short-circuit on
// re-run, and a restated re-delivery of 2026-06 that replaces exactly
// that period — totals change, no duplication, 2026-05 untouched.
func TestIngestIdempotencyAndRestatement(t *testing.T) {
	ctx := context.Background()
	tree := t.TempDir()
	copyTree(t, fixture, tree)
	_, url := startFake(t, tree)
	hermeticEnv(t, url, true)

	store, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ingestAll := func() []ingest.Result {
		t.Helper()
		conns, err := awsfocuss3.Discover(ctx, bucket, prefix)
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		results := make([]ingest.Result, 0, len(conns))
		for _, conn := range conns {
			res, err := ingest.Run(ctx, conn, store, focus.DefaultTenant)
			if err != nil {
				t.Fatalf("Run(%s): %v", conn.BillingPeriod(), err)
			}
			results = append(results, res)
		}
		return results
	}

	// Fresh ingest: both periods stored, 42 records each.
	for i, res := range ingestAll() {
		if res.Unchanged || res.Records != 42 {
			t.Fatalf("fresh ingest %d = %+v, want 42 fresh records", i, res)
		}
	}
	assertDays(t, ctx, store, 14, map[string]string{
		"Amazon Elastic Compute Cloud":  "3.6288",
		"AWS Lambda":                    "0.1896",
		"Amazon Simple Storage Service": "0.8625",
	}, map[string]string{
		"Amazon Elastic Compute Cloud":  "3.6288",
		"AWS Lambda":                    "0.1896",
		"Amazon Simple Storage Service": "0.8625",
	})

	// Unchanged content: every period short-circuits.
	for i, res := range ingestAll() {
		if !res.Unchanged || res.Records != 42 {
			t.Fatalf("re-ingest %d = %+v, want unchanged with 42 records", i, res)
		}
	}

	// Restated re-delivery of 2026-06 (new execution folder + updated
	// partition manifest, exactly like AWS create-new redelivery).
	copyTree(t, restated, tree)
	results := ingestAll()
	if !results[0].Unchanged {
		t.Errorf("2026-05 after restatement = %+v, want unchanged", results[0])
	}
	if results[1].Unchanged || results[1].Records != 42 {
		t.Errorf("2026-06 after restatement = %+v, want replaced with 42 records", results[1])
	}
	assertDays(t, ctx, store, 14, map[string]string{
		"Amazon Elastic Compute Cloud":  "3.6288",
		"AWS Lambda":                    "0.1896",
		"Amazon Simple Storage Service": "0.8625",
	}, map[string]string{
		"Amazon Elastic Compute Cloud":  "7.2576", // EC2 usage doubled in the restatement
		"AWS Lambda":                    "0.1896",
		"Amazon Simple Storage Service": "0.8625",
	})
}

// assertDays asserts the store holds wantDays days total, every May day
// carrying the first per-service totals and every June day the second.
func assertDays(t *testing.T, ctx context.Context, store storage.Store, wantDays int, may, june map[string]string) {
	t.Helper()
	daily, err := store.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyCostsByService: %v", err)
	}
	if len(daily.Days) != wantDays {
		t.Fatalf("store holds %d day(s), want %d", len(daily.Days), wantDays)
	}
	for _, day := range daily.Days {
		want := may
		if day.Date.Month() == time.June {
			want = june
		}
		if len(day.Services) != len(want) {
			t.Fatalf("day %s has services %+v, want %d services", day.Date, day.Services, len(want))
		}
		for _, svc := range day.Services {
			if got := svc.Cost.String(); got != want[svc.ServiceName] {
				t.Errorf("day %s %s = %s, want %s", day.Date.Format(time.DateOnly), svc.ServiceName, got, want[svc.ServiceName])
			}
		}
	}
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(from, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(to, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, body, 0o644)
	})
	if err != nil {
		t.Fatalf("copying %s to %s: %v", from, to, err)
	}
}

// TestRecordsStripsBOMViaSharedParser proves the S3 path flows through
// the same BOM-stripping CSV parser as the local connector (Step 0.6).
func TestRecordsStripsBOMViaSharedParser(t *testing.T) {
	ctx := context.Background()
	tree := t.TempDir()
	root := filepath.Join(tree, bucket, prefix)
	writeGzCSV(t, filepath.Join(root, "data/BILLING_PERIOD=2026-05/costroid-demo-00001.csv.gz"),
		"\uFEFFAvailabilityZone,ServiceName\neu-central-1a,AWS Lambda\n")
	writeFile(t, filepath.Join(root, "metadata/BILLING_PERIOD=2026-05/costroid-demo-Manifest.json"),
		`{"dataFiles": ["s3://demo/exports/costroid-demo/data/BILLING_PERIOD=2026-05/costroid-demo-00001.csv.gz"]}`)
	_, url := startFake(t, tree)
	hermeticEnv(t, url, true)

	conns, err := awsfocuss3.Discover(ctx, bucket, prefix)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	rows := readAll(t, ctx, conns[0])
	if len(rows) != 1 || rows[0].Record["AvailabilityZone"] != "eu-central-1a" {
		t.Errorf("rows = %+v, want one row with an intact first column", rows)
	}
}

func TestDiscoverErrors(t *testing.T) {
	manifestKey := prefix + "/metadata/BILLING_PERIOD=2026-05/costroid-demo-Manifest.json"
	dataKey := prefix + "/data/BILLING_PERIOD=2026-05/costroid-demo-00001.csv.gz"

	tests := []struct {
		name        string
		build       func(t *testing.T, root string) // root = <tree>/<bucket>
		bucket      string
		wantErrPart string
	}{
		{
			name:        "empty prefix",
			build:       func(t *testing.T, root string) { mkdir(t, filepath.Join(root, "unrelated")) },
			bucket:      bucket,
			wantErrPart: "no billing periods found",
		},
		{
			name:        "missing bucket",
			build:       func(t *testing.T, root string) {},
			bucket:      "no-such-bucket",
			wantErrPart: "bucket not found",
		},
		{
			name: "malformed manifest JSON",
			build: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, manifestKey), "{not json")
			},
			bucket:      bucket,
			wantErrPart: "malformed manifest",
		},
		{
			name: "manifest without data files",
			build: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, manifestKey), `{"dataFiles": []}`)
			},
			bucket:      bucket,
			wantErrPart: "lists no data files",
		},
		{
			name: "non-CSV export format",
			build: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, manifestKey),
					`{"dataFiles": ["s3://demo/`+prefix+`/data/BILLING_PERIOD=2026-05/costroid-demo-00001.snappy.parquet"]}`)
			},
			bucket:      bucket,
			wantErrPart: "gzipped-CSV FOCUS 1.2 exports only",
		},
		{
			name: "manifest references another bucket",
			build: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, manifestKey),
					`{"dataFiles": ["s3://other-bucket/`+dataKey+`"]}`)
			},
			bucket:      bucket,
			wantErrPart: "references bucket",
		},
		{
			name: "manifest lists a missing object",
			build: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, manifestKey),
					`{"dataFiles": ["s3://demo/`+dataKey+`"]}`)
			},
			bucket:      bucket,
			wantErrPart: "the object is missing",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tree := t.TempDir()
			mkdir(t, filepath.Join(tree, bucket))
			tt.build(t, filepath.Join(tree, bucket))
			_, url := startFake(t, tree)
			hermeticEnv(t, url, true)

			_, err := awsfocuss3.Discover(context.Background(), tt.bucket, prefix)
			if err == nil {
				t.Fatal("Discover succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tt.wantErrPart) {
				t.Errorf("Discover error %q does not contain %q", err, tt.wantErrPart)
			}
		})
	}
}

func TestDiscoverAccessDenied(t *testing.T) {
	fake, url := startFake(t, fixture)
	fake.Forbid(bucket + "/" + prefix)
	hermeticEnv(t, url, true)

	_, err := awsfocuss3.Discover(context.Background(), bucket, prefix)
	if err == nil {
		t.Fatal("Discover succeeded despite AccessDenied")
	}
	for _, part := range []string{"access denied", "s3:ListBucket", "s3:GetObject"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("AccessDenied error %q does not mention %q", err, part)
		}
	}
}

// TestDiscoverMissingCredentials scrubs the entire ambient chain and
// proves the connector fails fast with the actionable message instead of
// probing the network (decision D24).
func TestDiscoverMissingCredentials(t *testing.T) {
	_, url := startFake(t, fixture)
	hermeticEnv(t, url, false)

	start := time.Now()
	_, err := awsfocuss3.Discover(context.Background(), bucket, prefix)
	if err == nil {
		t.Fatal("Discover succeeded without any credentials")
	}
	if !strings.Contains(err.Error(), "no AWS credentials found in the default chain") {
		t.Errorf("error %q lacks the actionable credentials message", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("credential failure took %s, want fast failure", elapsed)
	}
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func writeGzCSV(t *testing.T, path, content string) {
	t.Helper()
	mkdir(t, filepath.Dir(path))
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating %s: %v", path, err)
	}
	gz := gzip.NewWriter(f)
	if _, err := gz.Write([]byte(content)); err != nil {
		t.Fatalf("writing gzip: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("closing gzip: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("closing %s: %v", path, err)
	}
}
