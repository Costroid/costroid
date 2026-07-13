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

	"github.com/shopspring/decimal"

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
	corrections  = "../../../testdata/aws-focus-s3/corrections"
	sampleExport = "../../../testdata/aws-focus-1.2/sample-export.csv.gz"
	bucket       = "demo"
	prefix       = "exports/costroid-demo"
)

// The committed corrections fixture (period 2026-07) carries three normal
// July-1 rows plus a ChargeClass="Correction" pair whose ChargePeriod
// lies on MAY 1: a reversal of the prod EC2 charge billed at the
// incorrect $0.1008 rate and its re-entry at the corrected $0.0504 rate.
// The expected arithmetic, asserted below and in the e2e:
const (
	correctionReversal = "-2.4192" // 24 h × $0.1008, reversed
	correctionReentry  = "1.2096"  // 24 h × $0.0504
	// net May-1 shift = -2.4192 + 1.2096 = -1.2096:
	may1EC2Corrected = "2.4192" // 3.6288 - 1.2096
	may1EC2Original  = "3.6288"
	// July 1's normal rows: EC2 2.4192 + S3 0.575 + Lambda 0.1264.
	july1EC2    = "2.4192"
	july1S3     = "0.575"
	july1Lambda = "0.1264"
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

	periods, err := awsfocuss3.Discover(context.Background(), bucket, prefix, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(periods) != 2 {
		t.Fatalf("Discover found %d period(s), want 2", len(periods))
	}
	wantIdentity := map[string]string{
		"2026-05": "demo/exports/costroid-demo/2026-05",
		"2026-06": "demo/exports/costroid-demo/2026-06",
	}
	hashes := map[string]string{}
	for i, want := range []string{"2026-05", "2026-06"} {
		p := periods[i]
		if p.Skipped() {
			t.Fatalf("period %d skipped with no prior sync state", i)
		}
		if p.Manifest.Key == "" || p.Manifest.ETag == "" || p.Manifest.LastModified.IsZero() || p.Manifest.Size == 0 {
			t.Errorf("period %d manifest state incomplete: %+v", i, p.Manifest)
		}
		c := p.Conn
		if c.BillingPeriod() != p.Billing {
			t.Errorf("period %d Billing = %q but connector period = %q", i, p.Billing, c.BillingPeriod())
		}
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

	periods, err := awsfocuss3.Discover(ctx, bucket, prefix, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	s3Rows := readAll(t, ctx, periods[0].Conn) // 2026-05: the sample data split into two chunks

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
		periods, err := awsfocuss3.Discover(ctx, bucket, prefix, nil)
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		results := make([]ingest.Result, 0, len(periods))
		for _, p := range periods {
			res, err := ingest.Run(ctx, p.Conn, store, focus.DefaultTenant)
			if err != nil {
				t.Fatalf("Run(%s): %v", p.Billing, err)
			}
			results = append(results, res)
		}
		return results
	}

	// Fresh ingest: both periods stored, 42 records each, nothing
	// reported as replaced.
	for i, res := range ingestAll() {
		if res.Unchanged || res.Replaced || res.Records != 42 {
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
	// Restatement visibility (D26d): the store reports the period's
	// BilledCost total before → after the replace. The committed June
	// fixture totals (3.6288+0.1896+0.8625)×7 = 32.7663 before and, with
	// EC2 doubled in the restatement, (7.2576+0.1896+0.8625)×7 = 58.1679.
	if !results[1].Replaced {
		t.Errorf("2026-06 restatement not reported as replaced: %+v", results[1])
	}
	if got := results[1].PreviousBilledCost.String(); got != "32.7663" {
		t.Errorf("2026-06 previous BilledCost = %s, want 32.7663", got)
	}
	if got := results[1].NewBilledCost.String(); got != "58.1679" {
		t.Errorf("2026-06 new BilledCost = %s, want 58.1679", got)
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
	daily, err := store.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{}, "")
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

	periods, err := awsfocuss3.Discover(ctx, bucket, prefix, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	rows := readAll(t, ctx, periods[0].Conn)
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
		// global marks source-level failures that abort Discover itself;
		// everything else degrades to the period's Err (slice-3 review
		// fix-up: one poisoned period must not block the others).
		global bool
	}{
		{
			name:        "empty prefix",
			build:       func(t *testing.T, root string) { mkdir(t, filepath.Join(root, "unrelated")) },
			bucket:      bucket,
			wantErrPart: "no billing periods found",
			global:      true,
		},
		{
			name:        "missing bucket",
			build:       func(t *testing.T, root string) {},
			bucket:      "no-such-bucket",
			wantErrPart: "bucket not found",
			global:      true,
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

			periods, err := awsfocuss3.Discover(context.Background(), tt.bucket, prefix, nil)
			if tt.global {
				if err == nil {
					t.Fatal("Discover succeeded, want a source-level error")
				}
				if !strings.Contains(err.Error(), tt.wantErrPart) {
					t.Errorf("Discover error %q does not contain %q", err, tt.wantErrPart)
				}
				return
			}
			if err != nil {
				t.Fatalf("Discover aborted on a per-period anomaly: %v", err)
			}
			if len(periods) != 1 || periods[0].Billing != "2026-05" {
				t.Fatalf("Discover periods = %+v, want exactly 2026-05", periods)
			}
			p := periods[0]
			if p.Err == nil {
				t.Fatal("poisoned period carries no error")
			}
			if p.Skipped() || p.Conn != nil {
				t.Errorf("poisoned period = %+v, want neither skipped nor connected", p)
			}
			if !strings.Contains(p.Err.Error(), tt.wantErrPart) {
				t.Errorf("period error %q does not contain %q", p.Err, tt.wantErrPart)
			}
		})
	}
}

func TestDiscoverAccessDenied(t *testing.T) {
	fake, url := startFake(t, fixture)
	fake.Forbid(bucket + "/" + prefix)
	hermeticEnv(t, url, true)

	_, err := awsfocuss3.Discover(context.Background(), bucket, prefix, nil)
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
	_, err := awsfocuss3.Discover(context.Background(), bucket, prefix, nil)
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

// TestIngestCorrectionRetroactivity proves D26c end to end at the store
// boundary: ingesting the corrections period (2026-07) retroactively
// shifts MAY 1 by exactly the fixture's documented net (-1.2096 on EC2),
// leaves every other May day and all of June untouched, and adds July 1's
// normal rows — because time-series aggregation is by ChargePeriod and
// the correction rows keep their original May timeframe.
func TestIngestCorrectionRetroactivity(t *testing.T) {
	// The documented fixture arithmetic must be internally consistent:
	// original + reversal + re-entry = corrected May-1 EC2 total.
	if got := decimal.RequireFromString(may1EC2Original).
		Add(decimal.RequireFromString(correctionReversal)).
		Add(decimal.RequireFromString(correctionReentry)); got.String() != may1EC2Corrected {
		t.Fatalf("fixture constants are inconsistent: %s + %s + %s = %s, want %s",
			may1EC2Original, correctionReversal, correctionReentry, got, may1EC2Corrected)
	}

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

	ingestAll := func() {
		t.Helper()
		periods, err := awsfocuss3.Discover(ctx, bucket, prefix, nil)
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		for _, p := range periods {
			if _, err := ingest.Run(ctx, p.Conn, store, focus.DefaultTenant); err != nil {
				t.Fatalf("Run(%s): %v", p.Billing, err)
			}
		}
	}

	ingestAll() // 2026-05 + 2026-06
	baseline, err := store.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{}, "")
	if err != nil {
		t.Fatalf("DailyCostsByService: %v", err)
	}
	if len(baseline.Days) != 14 {
		t.Fatalf("baseline holds %d day(s), want 14", len(baseline.Days))
	}

	copyTree(t, corrections, tree) // adds period 2026-07
	ingestAll()                    // 05/06 short-circuit unchanged; 07 fresh

	daily, err := store.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{}, "")
	if err != nil {
		t.Fatalf("DailyCostsByService: %v", err)
	}
	if len(daily.Days) != 15 {
		t.Fatalf("store holds %d day(s) after the correction period, want 15 (7 May + 7 June + 1 July)", len(daily.Days))
	}

	mayNormal := map[string]string{
		"Amazon Elastic Compute Cloud":  may1EC2Original,
		"AWS Lambda":                    "0.1896",
		"Amazon Simple Storage Service": "0.8625",
	}
	tests := []struct {
		date     time.Time
		services map[string]string
	}{
		// May 1 is the corrected day: EC2 3.6288 - 2.4192 + 1.2096 = 2.4192.
		{time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), map[string]string{
			"Amazon Elastic Compute Cloud":  may1EC2Corrected,
			"AWS Lambda":                    "0.1896",
			"Amazon Simple Storage Service": "0.8625",
		}},
		// July 1 carries exactly the period's normal rows.
		{time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), map[string]string{
			"Amazon Elastic Compute Cloud":  july1EC2,
			"AWS Lambda":                    july1Lambda,
			"Amazon Simple Storage Service": july1S3,
		}},
	}
	// Every remaining May and June day is untouched.
	for d := 2; d <= 7; d++ {
		tests = append(tests, struct {
			date     time.Time
			services map[string]string
		}{time.Date(2026, 5, d, 0, 0, 0, 0, time.UTC), mayNormal})
	}
	for d := 1; d <= 7; d++ {
		tests = append(tests, struct {
			date     time.Time
			services map[string]string
		}{time.Date(2026, 6, d, 0, 0, 0, 0, time.UTC), mayNormal})
	}

	byDate := map[string]map[string]string{}
	for _, day := range daily.Days {
		services := map[string]string{}
		for _, svc := range day.Services {
			services[svc.ServiceName] = svc.Cost.String()
		}
		byDate[day.Date.Format(time.DateOnly)] = services
	}
	for _, tt := range tests {
		date := tt.date.Format(time.DateOnly)
		got, ok := byDate[date]
		if !ok {
			t.Errorf("day %s missing from daily costs", date)
			continue
		}
		if len(got) != len(tt.services) {
			t.Errorf("day %s services = %v, want %v", date, got, tt.services)
			continue
		}
		for svc, want := range tt.services {
			if got[svc] != want {
				t.Errorf("day %s %s = %s, want %s", date, svc, got[svc], want)
			}
		}
	}
}

// syncOutcome is one period's result of a CLI-equivalent sync pass.
type syncOutcome struct {
	period    string
	skipped   bool
	unchanged bool
	replaced  bool
	records   int
}

// syncAll mirrors the CLI's sync flow exactly: read the stored tuples
// (none when force), discover with them, run every non-skipped period
// through the pipeline, and upsert the tuple after EVERY successful
// outcome — fresh, replaced, and unchanged short-circuit alike.
func syncAll(t *testing.T, ctx context.Context, store *storage.DuckDB, force bool) []syncOutcome {
	t.Helper()
	prior := map[string]awsfocuss3.ManifestState{}
	if !force {
		states, err := store.SyncStates(ctx, awsfocuss3.Name)
		if err != nil {
			t.Fatalf("SyncStates: %v", err)
		}
		for id, st := range states {
			prior[id] = awsfocuss3.ManifestState{
				Key:          st.ManifestKey,
				ETag:         st.ManifestETag,
				LastModified: st.ManifestLastModified,
				Size:         st.ManifestSize,
			}
		}
	}
	periods, err := awsfocuss3.Discover(ctx, bucket, prefix, prior)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	var out []syncOutcome
	for _, p := range periods {
		if p.Skipped() {
			out = append(out, syncOutcome{period: p.Billing, skipped: true})
			continue
		}
		res, err := ingest.Run(ctx, p.Conn, store, focus.DefaultTenant)
		if err != nil {
			t.Fatalf("Run(%s): %v", p.Billing, err)
		}
		if err := store.UpsertSyncState(ctx, storage.SyncState{
			Connector:            p.Conn.Name(),
			SourceIdentity:       p.Conn.SourceIdentity(),
			ManifestKey:          p.Manifest.Key,
			ManifestETag:         p.Manifest.ETag,
			ManifestLastModified: p.Manifest.LastModified,
			ManifestSize:         p.Manifest.Size,
		}); err != nil {
			t.Fatalf("UpsertSyncState(%s): %v", p.Billing, err)
		}
		out = append(out, syncOutcome{period: p.Billing, unchanged: res.Unchanged, replaced: res.Replaced, records: res.Records})
	}
	return out
}

// TestIncrementalSyncTupleSkip proves the manifest-tuple skip end to end
// with an instrumented fake: an unchanged re-sync performs ZERO GetObject
// calls; a bumped manifest LastModified re-processes exactly that period
// (whose identical content then hash-short-circuits); the tuple upserted
// on that Unchanged outcome makes the following sync skip again; and a
// forced sync bypasses the tuple skip.
func TestIncrementalSyncTupleSkip(t *testing.T) {
	ctx := context.Background()
	tree := t.TempDir()
	copyTree(t, fixture, tree)
	fake, url := startFake(t, tree)
	hermeticEnv(t, url, true)

	store, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Sync 1: no stored tuples — both periods ingest fresh.
	for _, o := range syncAll(t, ctx, store, false) {
		if o.skipped || o.unchanged || o.records != 42 {
			t.Fatalf("first sync %s = %+v, want 42 fresh records", o.period, o)
		}
	}

	// Sync 2: unchanged tuples — both periods skipped, ZERO GetObject
	// calls (not even the manifests are fetched).
	before := len(fake.GetObjectKeys())
	for _, o := range syncAll(t, ctx, store, false) {
		if !o.skipped {
			t.Fatalf("unchanged re-sync %s = %+v, want skipped", o.period, o)
		}
	}
	if calls := fake.GetObjectKeys()[before:]; len(calls) != 0 {
		t.Fatalf("unchanged re-sync performed %d GetObject call(s): %v", len(calls), calls)
	}

	// Touch 2026-06's partition manifest: identical bytes, later
	// LastModified. The tuple misses for exactly that period; its
	// unchanged content then short-circuits on the hash path.
	bumpMtime(t, filepath.Join(tree, bucket, prefix, "metadata/BILLING_PERIOD=2026-06/costroid-demo-Manifest.json"), 2*time.Second)
	outcomes := syncAll(t, ctx, store, false)
	if !outcomes[0].skipped {
		t.Errorf("2026-05 after unrelated touch = %+v, want skipped", outcomes[0])
	}
	if outcomes[1].skipped || !outcomes[1].unchanged {
		t.Errorf("touched 2026-06 = %+v, want re-processed and hash-short-circuited", outcomes[1])
	}

	// The tuple was upserted on that Unchanged outcome, so the next
	// sync tuple-skips both periods again — with zero GETs.
	before = len(fake.GetObjectKeys())
	for _, o := range syncAll(t, ctx, store, false) {
		if !o.skipped {
			t.Fatalf("sync after touched-but-identical %s = %+v, want skipped", o.period, o)
		}
	}
	if calls := fake.GetObjectKeys()[before:]; len(calls) != 0 {
		t.Fatalf("sync after touched-but-identical performed %d GetObject call(s): %v", len(calls), calls)
	}

	// force (the CLI's --force) bypasses the tuple skip; identical
	// content still short-circuits on the hash path.
	for _, o := range syncAll(t, ctx, store, true) {
		if o.skipped || !o.unchanged {
			t.Fatalf("forced sync %s = %+v, want processed via the hash path", o.period, o)
		}
	}
}

func bumpMtime(t *testing.T, path string, d time.Duration) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	bumped := info.ModTime().Add(d)
	if err := os.Chtimes(path, bumped, bumped); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

// TestRecordsRedeliveryRaceReportsActionably proves an If-Match race —
// the export re-delivered between Discover and Records, so the pinned
// ETag no longer matches (HTTP 412) — surfaces the actionable re-run
// message instead of a raw SDK error.
func TestRecordsRedeliveryRaceReportsActionably(t *testing.T) {
	ctx := context.Background()
	tree := t.TempDir()
	copyTree(t, fixture, tree)
	_, url := startFake(t, tree)
	hermeticEnv(t, url, true)

	periods, err := awsfocuss3.Discover(ctx, bucket, prefix, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// Re-deliver a 2026-05 chunk after discovery: same key, new bytes,
	// new content-derived ETag.
	writeGzCSV(t, filepath.Join(tree, bucket, prefix, "data/BILLING_PERIOD=2026-05/costroid-demo-00001.csv.gz"),
		"AvailabilityZone,ServiceName\neu-central-1a,AWS Lambda\n")

	reader, err := periods[0].Conn.Records(ctx)
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	defer func() { _ = reader.Close() }()
	_, err = reader.Next()
	if err == nil {
		t.Fatal("Next succeeded despite the mid-read re-delivery")
	}
	for _, part := range []string{"re-delivered mid-read", "re-run ingest", "discovery will pick up the new manifest"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("412 race error %q does not contain %q", err, part)
		}
	}
}

// TestDiscoverRejectsMultiplePartitionManifests proves a partition holding
// more than one partition-level manifest — an anomaly AWS never writes —
// poisons exactly that period, listing the candidates instead of
// nondeterministically picking one, while the other periods still sync
// (slice-3 review fix-up: per-period degradation).
func TestDiscoverRejectsMultiplePartitionManifests(t *testing.T) {
	tree := t.TempDir()
	copyTree(t, fixture, tree)
	stray := prefix + "/metadata/BILLING_PERIOD=2026-05/stray-copy-Manifest.json"
	writeFile(t, filepath.Join(tree, bucket, stray),
		`{"dataFiles": ["s3://demo/exports/costroid-demo/data/BILLING_PERIOD=2026-05/costroid-demo-00001.csv.gz"]}`)
	_, url := startFake(t, tree)
	hermeticEnv(t, url, true)

	periods, err := awsfocuss3.Discover(context.Background(), bucket, prefix, nil)
	if err != nil {
		t.Fatalf("Discover aborted on a single poisoned period: %v", err)
	}
	if len(periods) != 2 {
		t.Fatalf("Discover found %d period(s), want 2", len(periods))
	}
	poisoned := periods[0]
	if poisoned.Billing != "2026-05" || poisoned.Err == nil {
		t.Fatalf("period 2026-05 = %+v, want its multi-manifest error", poisoned)
	}
	for _, part := range []string{
		"billing period 2026-05 has 2 partition-level manifests",
		"s3://demo/" + prefix + "/metadata/BILLING_PERIOD=2026-05/costroid-demo-Manifest.json",
		"s3://demo/" + stray,
	} {
		if !strings.Contains(poisoned.Err.Error(), part) {
			t.Errorf("multi-manifest error %q does not contain %q", poisoned.Err, part)
		}
	}
	healthy := periods[1]
	if healthy.Billing != "2026-06" || healthy.Err != nil || healthy.Conn == nil {
		t.Fatalf("period 2026-06 = %+v, want unaffected and readable", healthy)
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
