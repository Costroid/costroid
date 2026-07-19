// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package azurefocus_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/devtools/fakeblob"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/ingest"
	"github.com/Costroid/costroid/internal/ingest/azurefocus"
	"github.com/Costroid/costroid/internal/storage"
)

const (
	fixture       = "../../../testdata/azure-focus/fixture"
	restated      = "../../../testdata/azure-focus/restated"
	account       = "devaccount"
	containerName = "exports"
	prefix        = "costroid-demo"
)

// Documented fixture arithmetic (see the generator comment in
// testdata/azure-focus): May batch 20.50, June batch 12.00 (including
// the -0.50 May correction and the -1.50 credit), restated May 19.50.
const (
	mayTotal         = "20.5"
	juneTotal        = "12"
	mayRestatedTotal = "19.5"
)

// hermeticAzureEnv pins the escape hatch and scrubs the ambient Azure
// credential chain so the tests pass identically on any machine. When
// noAuth is true the connector talks to the fake anonymously via the
// documented test-only escape.
func hermeticAzureEnv(t *testing.T, noAuth bool) {
	t.Helper()
	for _, v := range []string{
		"AZURE_TENANT_ID", "AZURE_CLIENT_ID", "AZURE_CLIENT_SECRET",
		"AZURE_CLIENT_CERTIFICATE_PATH", "AZURE_USERNAME", "AZURE_PASSWORD",
		"AZURE_FEDERATED_TOKEN_FILE", "AZURE_TOKEN_CREDENTIALS",
	} {
		t.Setenv(v, "")
	}
	if noAuth {
		t.Setenv(azurefocus.InsecureNoAuthEnv, "1")
	} else {
		t.Setenv(azurefocus.InsecureNoAuthEnv, "")
	}
}

func startFake(t *testing.T, dir string) (*fakeblob.Handler, string) {
	t.Helper()
	h := fakeblob.New(dir)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return h, srv.URL + "/" + account
}

func openStore(t *testing.T) *storage.DuckDB {
	t.Helper()
	store, err := storage.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
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

// writeMiniExport writes one synthetic export run below containerDir:
// a single tiny data partition plus a manifest with the given export
// name and dates. runDir and the manifest's blobName are
// container-relative, as the published samples deliver them.
func writeMiniExport(t *testing.T, containerDir, runDir, exportName, startDate, submitted string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte("BilledCost\n1\n")); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(containerDir, filepath.FromSlash(runDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "part_0_0001.csv.gz"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf(`{
  "blobs": [{"blobName": %q, "byteCount": %d, "dataRowCount": 1}],
  "exportConfig": {"exportName": %q, "dataVersion": "1.2-preview", "type": "FocusCost"},
  "deliveryConfig": {"fileFormat": "Csv", "compressionMode": "gzip", "dataOverwriteBehavior": "OverwritePreviousReport"},
  "runInfo": {"executionType": "Scheduled", "submittedTime": %q, "runId": "run", "startDate": %q}
}`, runDir+"/part_0_0001.csv.gz", buf.Len(), exportName, submitted, startDate)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDiscoverBlobNameCollidingWithContainerName proves manifest
// blobName resolution tries the verbatim container-relative name FIRST:
// an export whose in-container directory starts with the container's own
// name ("exports" container, "exports/demo" directory) must discover and
// build its connector, not fail with a misleading replaced-mid-read
// error.
func TestDiscoverBlobNameCollidingWithContainerName(t *testing.T) {
	tree := t.TempDir()
	writeMiniExport(t, filepath.Join(tree, account, containerName),
		"exports/demo/run1", "demo", "2026-05-01T00:00:00", "2026-06-01T08:00:00.0000000Z")
	_, accountURL := startFake(t, tree)
	hermeticAzureEnv(t, true)
	store := openStore(t)

	periods, err := azurefocus.Discover(context.Background(), accountURL, containerName, "exports/demo", nil, store)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(periods) != 1 || periods[0].Err != nil || periods[0].Conn == nil {
		t.Fatalf("periods = %+v, want one readable 2026-05 period", periods)
	}
	if periods[0].Billing != "2026-05" {
		t.Errorf("Billing = %q, want 2026-05", periods[0].Billing)
	}
}

// TestDiscoverRefusesMultipleExportsUnderOnePrefix proves two different
// exports delivering under one shared prefix poison the affected period
// with an actionable error instead of silently replacing each other's
// data on alternating syncs.
func TestDiscoverRefusesMultipleExportsUnderOnePrefix(t *testing.T) {
	tree := t.TempDir()
	containerDir := filepath.Join(tree, account, containerName)
	writeMiniExport(t, containerDir, "finops/actual/run1", "actual",
		"2026-05-01T00:00:00", "2026-06-01T08:00:00.0000000Z")
	writeMiniExport(t, containerDir, "finops/amortized/run1", "amortized",
		"2026-05-01T00:00:00", "2026-06-01T09:00:00.0000000Z")
	_, accountURL := startFake(t, tree)
	hermeticAzureEnv(t, true)
	store := openStore(t)

	periods, err := azurefocus.Discover(context.Background(), accountURL, containerName, "finops", nil, store)
	if err != nil {
		t.Fatalf("Discover aborted instead of degrading per period: %v", err)
	}
	if len(periods) != 1 || periods[0].Err == nil {
		t.Fatalf("periods = %+v, want one poisoned 2026-05 period", periods)
	}
	for _, part := range []string{"different exports", "actual, amortized", "ONE export's root"} {
		if !strings.Contains(periods[0].Err.Error(), part) {
			t.Errorf("multi-export error %q does not contain %q", periods[0].Err, part)
		}
	}
}

// writeTiedRun writes one run folder with a gzipped data partition and one
// or more (identical) manifest files, so tests can construct submittedTime
// ties within one billing period.
func writeTiedRun(t *testing.T, containerDir, runDir, exportName, startDate, submitted string, manifestNames ...string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte("BilledCost\n1\n")); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(containerDir, filepath.FromSlash(runDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "part_0_0001.csv.gz"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf(`{
  "blobs": [{"blobName": %q, "byteCount": %d, "dataRowCount": 1}],
  "exportConfig": {"exportName": %q, "dataVersion": "1.2-preview", "type": "FocusCost"},
  "deliveryConfig": {"fileFormat": "Csv", "compressionMode": "gzip", "dataOverwriteBehavior": "CreateNewReport"},
  "runInfo": {"executionType": "Scheduled", "submittedTime": %q, "runId": "run", "startDate": %q}
}`, runDir+"/part_0_0001.csv.gz", buf.Len(), exportName, submitted, startDate)
	for _, name := range manifestNames {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestDiscoverSubmittedTimeTie proves the submittedTime tie-break
// (slice-4 review fix-up): tied manifests describing IDENTICAL data (a
// manifest.json/_manifest.json pair for one run) keep the deterministic
// lexical pick, while tied manifests with DIFFERING contents degrade the
// period to an actionable error naming both instead of a silent lexical
// tie-break.
func TestDiscoverSubmittedTimeTie(t *testing.T) {
	ctx := context.Background()

	t.Run("identical contents keep the deterministic pick", func(t *testing.T) {
		tree := t.TempDir()
		writeTiedRun(t, filepath.Join(tree, account, containerName),
			"tie/run1", "demo", "2026-05-01T00:00:00", "2026-06-01T08:00:00.0000000Z",
			"manifest.json", "_manifest.json")
		_, accountURL := startFake(t, tree)
		hermeticAzureEnv(t, true)
		store := openStore(t)

		periods, err := azurefocus.Discover(ctx, accountURL, containerName, "tie", nil, store)
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		if len(periods) != 1 || periods[0].Err != nil || periods[0].Conn == nil {
			t.Fatalf("periods = %+v, want one readable 2026-05 period (identical-token tie)", periods)
		}
	})

	t.Run("differing contents degrade the period", func(t *testing.T) {
		tree := t.TempDir()
		containerDir := filepath.Join(tree, account, containerName)
		writeTiedRun(t, containerDir, "tie/run1", "demo",
			"2026-05-01T00:00:00", "2026-06-01T08:00:00.0000000Z", "manifest.json")
		writeTiedRun(t, containerDir, "tie/run2", "demo",
			"2026-05-01T00:00:00", "2026-06-01T08:00:00.0000000Z", "manifest.json")
		_, accountURL := startFake(t, tree)
		hermeticAzureEnv(t, true)
		store := openStore(t)

		periods, err := azurefocus.Discover(ctx, accountURL, containerName, "tie", nil, store)
		if err != nil {
			t.Fatalf("Discover aborted instead of degrading per period: %v", err)
		}
		if len(periods) != 1 || periods[0].Err == nil {
			t.Fatalf("periods = %+v, want one poisoned 2026-05 period", periods)
		}
		for _, part := range []string{"same runInfo.submittedTime", "tie/run1/manifest.json", "tie/run2/manifest.json"} {
			if !strings.Contains(periods[0].Err.Error(), part) {
				t.Errorf("tie error %q does not contain %q", periods[0].Err, part)
			}
		}
	})
}

// TestDiscoverCachedSubmittedTimeTie proves the tie-break's CACHED-sync
// fallback (slice-5 review fix-up): when a tied period's manifests were
// attributed from the persistent cache (so their bodies were not fetched
// during discovery), resolveTie must fetch the bodies itself to compare their
// change tokens. It covers BOTH sub-branches of that fallback: the fetch
// succeeding (the tie is still detected and the period degrades) and the
// fetch failing (the fetch error degrades the period). Both existing tie
// tests run only first-sync, where the bodies are already in hand.
func TestDiscoverCachedSubmittedTimeTie(t *testing.T) {
	ctx := context.Background()

	// twoRunTree writes two runs sharing a submittedTime but describing
	// different data (different blobName → different change token) → a
	// differing-content tie in billing period 2026-05.
	twoRunTree := func(t *testing.T) string {
		t.Helper()
		tree := t.TempDir()
		containerDir := filepath.Join(tree, account, containerName)
		writeTiedRun(t, containerDir, "tie/run1", "demo",
			"2026-05-01T00:00:00", "2026-06-01T08:00:00.0000000Z", "manifest.json")
		writeTiedRun(t, containerDir, "tie/run2", "demo",
			"2026-05-01T00:00:00", "2026-06-01T08:00:00.0000000Z", "manifest.json")
		return tree
	}

	t.Run("cached bodies are re-fetched and the tie still degrades", func(t *testing.T) {
		tree := twoRunTree(t)
		_, accountURL := startFake(t, tree)
		hermeticAzureEnv(t, true)
		store := openStore(t)

		// First sync warms the attribution cache for both manifests.
		if _, err := azurefocus.Discover(ctx, accountURL, containerName, "tie", nil, store); err != nil {
			t.Fatalf("first Discover: %v", err)
		}
		// Second sync: both manifests are attributed from the cache (bodies
		// NOT in hand), so resolveTie fetches them itself and still detects
		// the differing-content tie.
		periods, err := azurefocus.Discover(ctx, accountURL, containerName, "tie", nil, store)
		if err != nil {
			t.Fatalf("second Discover aborted instead of degrading per period: %v", err)
		}
		if len(periods) != 1 || periods[0].Err == nil {
			t.Fatalf("periods = %+v, want one poisoned 2026-05 period", periods)
		}
		for _, part := range []string{"same runInfo.submittedTime", "tie/run1/manifest.json", "tie/run2/manifest.json"} {
			if !strings.Contains(periods[0].Err.Error(), part) {
				t.Errorf("cached-tie error %q does not contain %q", periods[0].Err, part)
			}
		}
	})

	t.Run("a cached-tie body re-fetch failure degrades the period", func(t *testing.T) {
		tree := twoRunTree(t)
		fake, accountURL := startFake(t, tree)
		hermeticAzureEnv(t, true)
		store := openStore(t)

		// Warm the cache while the manifests are still readable.
		if _, err := azurefocus.Discover(ctx, accountURL, containerName, "tie", nil, store); err != nil {
			t.Fatalf("first Discover: %v", err)
		}
		// One tied manifest becomes unreadable. Its listing tuple is
		// unchanged, so it is still attributed from the cache; the failure
		// therefore surfaces on resolveTie's body re-fetch.
		fake.Forbid(account + "/" + containerName + "/tie/run1/manifest.json")

		periods, err := azurefocus.Discover(ctx, accountURL, containerName, "tie", nil, store)
		if err != nil {
			t.Fatalf("second Discover aborted instead of degrading per period: %v", err)
		}
		if len(periods) != 1 || periods[0].Err == nil {
			t.Fatalf("periods = %+v, want one poisoned 2026-05 period", periods)
		}
		if !strings.Contains(periods[0].Err.Error(), "access denied") {
			t.Errorf("cached-tie fetch-failure error %q, want a per-period access-denied error", periods[0].Err)
		}
	})
}

// TestDiscoverRefusesEmptyExportName proves a manifest with an empty
// exportConfig.exportName degrades its period (slice-4 review fix-up): an
// empty name is unattributable and conflating distinct exports as {""}
// would defeat the shared-prefix refusal.
func TestDiscoverRefusesEmptyExportName(t *testing.T) {
	tree := t.TempDir()
	writeMiniExport(t, filepath.Join(tree, account, containerName),
		"noname/run1", "", "2026-05-01T00:00:00", "2026-06-01T08:00:00.0000000Z")
	_, accountURL := startFake(t, tree)
	hermeticAzureEnv(t, true)
	store := openStore(t)

	periods, err := azurefocus.Discover(context.Background(), accountURL, containerName, "noname", nil, store)
	if err != nil {
		t.Fatalf("Discover aborted instead of degrading per period: %v", err)
	}
	if len(periods) != 1 || periods[0].Err == nil {
		t.Fatalf("periods = %+v, want one poisoned 2026-05 period", periods)
	}
	if !strings.Contains(periods[0].Err.Error(), "no exportConfig.exportName") {
		t.Errorf("empty-exportName error %q does not name the cause", periods[0].Err)
	}
}

// TestDiscoverCachedManifestRefetchFailureIsPerPeriod proves a period
// whose current manifest is attributed from the cache but whose body
// re-fetch fails (here: 403) poisons that period only — the period is
// known, so it must not abort the healthy ones.
func TestDiscoverCachedManifestRefetchFailureIsPerPeriod(t *testing.T) {
	ctx := context.Background()
	fake, accountURL := startFake(t, fixture)
	hermeticAzureEnv(t, true)
	store := openStore(t)

	// Warm the attribution cache (no sync tuples are stored — this
	// mirrors a first sync whose ingest failed, or --force).
	if _, err := azurefocus.Discover(ctx, accountURL, containerName, prefix, nil, store); err != nil {
		t.Fatalf("first Discover: %v", err)
	}

	// The May manifest becomes unreadable; its attribution is cached, so
	// the failure happens on the body re-fetch.
	fake.Forbid(account + "/" + containerName + "/" + prefix + "/mtd-snapshot/current/manifest.json")

	periods, err := azurefocus.Discover(ctx, accountURL, containerName, prefix, nil, store)
	if err != nil {
		t.Fatalf("Discover aborted instead of degrading per period: %v", err)
	}
	if len(periods) != 2 {
		t.Fatalf("Discover found %d period(s), want 2", len(periods))
	}
	if periods[0].Err == nil || !strings.Contains(periods[0].Err.Error(), "access denied") {
		t.Errorf("May = %+v, want a per-period access-denied error", periods[0])
	}
	if periods[1].Err != nil || periods[1].Conn == nil {
		t.Errorf("June = %+v, want unaffected and readable", periods[1])
	}
}

func TestDiscoverFixture(t *testing.T) {
	fake, accountURL := startFake(t, fixture)
	// Force multi-page listings (the fixture holds 7 blobs) to prove the
	// pager loop, not just its first page.
	fake.PageSize = 3
	hermeticAzureEnv(t, true)
	store := openStore(t)

	periods, err := azurefocus.Discover(context.Background(), accountURL, containerName, prefix, nil, store)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(periods) != 2 {
		t.Fatalf("Discover found %d period(s), want 2", len(periods))
	}
	// The May month lives under deliberately NONCONFORMING folder names
	// (mtd-snapshot/current) — discovering it at all proves periods come
	// from manifest bodies, never folder names.
	root := strings.TrimPrefix(accountURL, "http://")
	hashes := map[string]string{}
	for i, want := range []string{"2026-05", "2026-06"} {
		p := periods[i]
		if p.Err != nil {
			t.Fatalf("period %s: %v", want, p.Err)
		}
		if p.Billing != want || p.Skipped() {
			t.Fatalf("period %d = %+v, want %s discovered oldest-first and not skipped", i, p, want)
		}
		if p.Manifest.Key == "" || p.Manifest.ETag == "" || p.Manifest.LastModified.IsZero() || p.Manifest.Size == 0 {
			t.Errorf("period %s manifest state incomplete: %+v", want, p.Manifest)
		}
		c := p.Conn
		if c.Name() != "azure-focus" || string(c.FOCUSVersion()) != "1.2" || c.BillingPeriod() != want {
			t.Errorf("connector = %s/%s/%s, want azure-focus/1.2/%s", c.Name(), c.FOCUSVersion(), c.BillingPeriod(), want)
		}
		if got, wantID := c.SourceIdentity(), root+"/"+containerName+"/"+prefix+"/"+want; got != wantID {
			t.Errorf("SourceIdentity = %q, want %q", got, wantID)
		}
		hash, err := c.ContentHash(context.Background())
		if err != nil || !strings.HasPrefix(hash, "sha256:") || len(hash) != len("sha256:")+64 {
			t.Errorf("ContentHash = %q (%v), want a sha256: digest", hash, err)
		}
		hashes[want] = hash
	}
	if hashes["2026-05"] == hashes["2026-06"] {
		t.Error("different periods produced the same content hash")
	}

	// June's CURRENT run is the one with the greater submittedTime: run
	// 1a2b... (5 rows), not the superseded 8f7e... (2 rows).
	if !strings.Contains(periods[1].Manifest.Key, "1a2b3c4d-5e6f-7089-9abc-def012345678") {
		t.Errorf("June current manifest = %q, want the later-submitted run", periods[1].Manifest.Key)
	}
	rows := readAll(t, context.Background(), periods[1].Conn)
	if len(rows) != 5 {
		t.Errorf("June current run yields %d row(s), want the 5 of the later run", len(rows))
	}
}

// TestRecordsAcrossPartitions proves the May month's two partitions —
// each a complete CSV with its own header row — stream as one coherent
// record sequence: numbering spans partitions, headers never leak into
// data, and every row keys by its own partition's header.
func TestRecordsAcrossPartitions(t *testing.T) {
	ctx := context.Background()
	_, accountURL := startFake(t, fixture)
	hermeticAzureEnv(t, true)
	store := openStore(t)

	periods, err := azurefocus.Discover(ctx, accountURL, containerName, prefix, nil, store)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	rows := readAll(t, ctx, periods[0].Conn) // May: 3 rows + 2 rows
	if len(rows) != 5 {
		t.Fatalf("May yields %d row(s), want 5 across two partitions", len(rows))
	}
	for i, row := range rows {
		if row.Number != i+1 {
			t.Errorf("row %d numbered %d, want %d (numbering must span partitions)", i, row.Number, i+1)
		}
		if row.Record["BillingCurrency"] != "USD" {
			t.Errorf("row %d BillingCurrency = %q — a header row leaked into the data", i+1, row.Record["BillingCurrency"])
		}
	}
	// A partition-1 row keys by its own header (the savings-plan
	// purchase is partition 1's first row).
	if got := rows[3].Record["ChargeDescription"]; got != "Savings plan monthly fee" {
		t.Errorf("partition-1 row ChargeDescription = %q, want the savings-plan purchase", got)
	}
}

// TestRecordsApplyGapFill proves the documented gap-fill rules against
// the committed fixture rows: both empty-ServiceName cases (EA
// Marketplace and MCA), the zero-placeholder unit prices, the untouched
// zero costs, timestamp normalization, and ChargeClass passthrough.
func TestRecordsApplyGapFill(t *testing.T) {
	ctx := context.Background()
	_, accountURL := startFake(t, fixture)
	hermeticAzureEnv(t, true)
	store := openStore(t)

	periods, err := azurefocus.Discover(ctx, accountURL, containerName, prefix, nil, store)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	may := readAll(t, ctx, periods[0].Conn)
	june := readAll(t, ctx, periods[1].Conn)

	// AZF-1: EA Marketplace purchase → ServiceName from PublisherName.
	mp := may[2].Record
	if mp["ServiceName"] != "Contoso Software Ltd" {
		t.Errorf("EA Marketplace ServiceName = %q, want the PublisherName fill", mp["ServiceName"])
	}
	// AZF-3/AZF-4 on the same row: zero unit prices beside zero costs → null.
	if _, ok := mp["ListUnitPrice"]; ok {
		t.Errorf("EA Marketplace ListUnitPrice = %q, want nulled (AZF-3)", mp["ListUnitPrice"])
	}
	if _, ok := mp["ContractedUnitPrice"]; ok {
		t.Errorf("EA Marketplace ContractedUnitPrice = %q, want nulled (AZF-4)", mp["ContractedUnitPrice"])
	}
	// The zero COSTS are kept exactly as delivered.
	if mp["ListCost"] != "0" || mp["ContractedCost"] != "0" {
		t.Errorf("EA Marketplace ListCost/ContractedCost = %q/%q, want Azure's zeros kept", mp["ListCost"], mp["ContractedCost"])
	}

	// AZF-2: MCA savings-plan purchase → ServiceName from x_SkuMeterSubcategory.
	if got := may[3].Record["ServiceName"]; got != "Savings Plan Compute" {
		t.Errorf("savings-plan ServiceName = %q, want the x_SkuMeterSubcategory fill", got)
	}

	// AZF-5: the reservation row's second-less, timezone-less timestamps
	// normalized to RFC 3339 UTC.
	rv := may[4].Record
	if rv["ChargePeriodStart"] != "2026-05-03T00:00:00Z" || rv["ChargePeriodEnd"] != "2026-05-04T00:00:00Z" {
		t.Errorf("reservation charge period = %q..%q, want normalized RFC 3339", rv["ChargePeriodStart"], rv["ChargePeriodEnd"])
	}
	// The timezone-less-with-seconds row too.
	if got := may[1].Record["ChargePeriodStart"]; got != "2026-05-01T00:00:00Z" {
		t.Errorf("timezone-less ChargePeriodStart = %q, want normalized RFC 3339", got)
	}

	// AZF-2 beats the PublisherName fallback off the Purchase category:
	// the June credit row has PublisherName=Microsoft AND
	// x_SkuMeterSubcategory=Azure Credit.
	if got := june[3].Record["ServiceName"]; got != "Azure Credit" {
		t.Errorf("credit ServiceName = %q, want the x_SkuMeterSubcategory fill", got)
	}

	// ChargeClass passthrough: the correction row is not rewritten.
	co := june[4].Record
	if co["ChargeClass"] != "Correction" || co["ChargePeriodStart"] != "2026-05-01T00:00:00Z" {
		t.Errorf("correction row = class %q period %q, want Correction with its original May timeframe", co["ChargeClass"], co["ChargePeriodStart"])
	}
}

// TestGapFillRules pins each documented rule at the unit level,
// including the hard invariant that no rule nulls a validation-mandatory
// column and that zero-cost rows pass validation unmodified.
func TestGapFillRules(t *testing.T) {
	fill := func(rec focus.RawRecord) focus.RawRecord {
		azurefocus.GapFill(rec)
		return rec
	}

	// AZF-1: EA Marketplace purchase — PublisherName fill applies only when
	// the marketplace signal (x_PublisherType == "Marketplace") is present.
	if got := fill(focus.RawRecord{"ServiceName": "", "ChargeCategory": "Purchase", "PublisherName": "Contoso", "x_PublisherType": "Marketplace"})["ServiceName"]; got != "Contoso" {
		t.Errorf("AZF-1 gated ServiceName = %q, want Contoso", got)
	}
	// AZF-1/AZF-2 ordering (slice-4 review fix-up): a Purchase row carrying
	// BOTH a PublisherName and an x_SkuMeterSubcategory but NO marketplace
	// signal prefers x_SkuMeterSubcategory (AZF-2), not PublisherName — the
	// PublisherName fill is documented for EA Marketplace rows only.
	if got := fill(focus.RawRecord{"ServiceName": "", "ChargeCategory": "Purchase", "PublisherName": "Contoso", "x_SkuMeterSubcategory": "Reservation"})["ServiceName"]; got != "Reservation" {
		t.Errorf("ungated Purchase ServiceName = %q, want the x_SkuMeterSubcategory fill (AZF-2)", got)
	}
	// With the marketplace signal, AZF-1 wins even beside a subcategory.
	if got := fill(focus.RawRecord{"ServiceName": "", "ChargeCategory": "Purchase", "PublisherName": "Contoso", "x_SkuMeterSubcategory": "Reservation", "x_PublisherType": "Marketplace"})["ServiceName"]; got != "Contoso" {
		t.Errorf("marketplace-signaled Purchase ServiceName = %q, want the PublisherName fill (AZF-1)", got)
	}
	// AZF-2: MCA cases use x_SkuMeterSubcategory — even when
	// PublisherName is set, off the Purchase category.
	if got := fill(focus.RawRecord{"ServiceName": "", "ChargeCategory": "Credit", "PublisherName": "Microsoft", "x_SkuMeterSubcategory": "Azure Credit"})["ServiceName"]; got != "Azure Credit" {
		t.Errorf("AZF-2 ServiceName = %q, want Azure Credit", got)
	}
	if got := fill(focus.RawRecord{"ServiceName": "", "ChargeCategory": "Purchase", "x_SkuMeterSubcategory": "Savings Plan Compute"})["ServiceName"]; got != "Savings Plan Compute" {
		t.Errorf("AZF-2 purchase-without-publisher ServiceName = %q, want Savings Plan Compute", got)
	}
	// Last-resort PublisherName fallback.
	if got := fill(focus.RawRecord{"ServiceName": "", "ChargeCategory": "Usage", "PublisherName": "Contoso"})["ServiceName"]; got != "Contoso" {
		t.Errorf("fallback ServiceName = %q, want Contoso", got)
	}
	// Never invented: an unfillable row stays empty and still fails the
	// shared validation loudly (rows are never dropped).
	unfillable := fill(focus.RawRecord{"ServiceName": "", "ChargeCategory": "Usage"})
	if unfillable["ServiceName"] != "" {
		t.Errorf("unfillable ServiceName = %q, want left empty for validation to report", unfillable["ServiceName"])
	}
	// A populated ServiceName is never touched.
	if got := fill(focus.RawRecord{"ServiceName": "Virtual Machines", "PublisherName": "Contoso", "x_SkuMeterSubcategory": "X"})["ServiceName"]; got != "Virtual Machines" {
		t.Errorf("populated ServiceName rewritten to %q", got)
	}

	// AZF-3/AZF-4: zero unit price is nulled ONLY beside a zero cost.
	rec := fill(focus.RawRecord{"ListUnitPrice": "0", "ListCost": "0", "ContractedUnitPrice": "0.00", "ContractedCost": "0.000"})
	if _, ok := rec["ListUnitPrice"]; ok {
		t.Error("AZF-3: zero ListUnitPrice beside zero ListCost not nulled")
	}
	if _, ok := rec["ContractedUnitPrice"]; ok {
		t.Error("AZF-4: zero ContractedUnitPrice beside zero ContractedCost not nulled")
	}
	rec = fill(focus.RawRecord{"ListUnitPrice": "0", "ListCost": "5.00", "ContractedUnitPrice": "0.023", "ContractedCost": "0"})
	if rec["ListUnitPrice"] != "0" {
		t.Errorf("zero ListUnitPrice beside a NON-zero cost = %q, want kept as delivered", rec["ListUnitPrice"])
	}
	if rec["ContractedUnitPrice"] != "0.023" {
		t.Errorf("non-zero ContractedUnitPrice = %q, want untouched", rec["ContractedUnitPrice"])
	}

	// AZF-5 timestamp forms.
	for in, want := range map[string]string{
		"2026-05-01T00:00:00":      "2026-05-01T00:00:00Z", // timezone-less, seconds
		"2026-05-03T00:00":         "2026-05-03T00:00:00Z", // timezone-less, no seconds
		"2026-06-03T00:00Z":        "2026-06-03T00:00:00Z", // zoned, no seconds
		"2026-05-01T00:00:00.5":    "2026-05-01T00:00:00.5Z",
		"2026-05-01T00:00:00.000Z": "2026-05-01T00:00:00.000Z", // already RFC 3339: untouched
		"not-a-time":               "not-a-time",               // left for validation to report
	} {
		if got := fill(focus.RawRecord{"ChargePeriodStart": in})["ChargePeriodStart"]; got != want {
			t.Errorf("AZF-5 %q -> %q, want %q", in, got, want)
		}
	}

	// Hard invariant: the zero-cost reservation shape passes the shared
	// validation UNMODIFIED — no rule touches the mandatory cost columns.
	zeroCost := focus.RawRecord{
		"BilledCost": "0.00", "EffectiveCost": "1.20", "ListCost": "0", "ContractedCost": "0",
		"BillingCurrency": "USD", "ChargeCategory": "Usage",
		"BillingPeriodStart": "2026-05-01T00:00:00Z", "BillingPeriodEnd": "2026-06-01T00:00:00Z",
		"ChargePeriodStart": "2026-05-03T00:00:00Z", "ChargePeriodEnd": "2026-05-04T00:00:00Z",
		"BillingAccountId": "1234567", "ServiceName": "Virtual Machines", "ServiceCategory": "Compute",
		"ProviderName": "Microsoft", "InvoiceIssuerName": "Microsoft",
	}
	azurefocus.GapFill(zeroCost)
	if zeroCost["ListCost"] != "0" || zeroCost["ContractedCost"] != "0" || zeroCost["BilledCost"] != "0.00" {
		t.Fatalf("gap-fill modified mandatory cost columns: %+v", zeroCost)
	}
	transform, err := focus.TransformTo14(focus.V1_2)
	if err != nil {
		t.Fatalf("TransformTo14: %v", err)
	}
	normalized, err := transform(zeroCost)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if violations := focus.Validate(normalized, focus.DefaultRules()); len(violations) > 0 {
		t.Fatalf("zero-cost row fails validation after gap-fill: %v", violations)
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

// syncOutcome is one period's result of a CLI-equivalent sync pass.
type syncOutcome struct {
	period    string
	skipped   bool
	unchanged bool
	replaced  bool
	records   int
	prevCost  string
	newCost   string
}

// syncAll mirrors the CLI's sync flow exactly: read the stored tuples
// (none when force), discover with them, run every non-skipped period
// through the pipeline, and upsert the tuple after EVERY successful
// outcome.
func syncAll(t *testing.T, ctx context.Context, store *storage.DuckDB, accountURL string, force bool) []syncOutcome {
	t.Helper()
	prior := map[string]azurefocus.ManifestState{}
	if !force {
		states, err := store.SyncStates(ctx, azurefocus.Name)
		if err != nil {
			t.Fatalf("SyncStates: %v", err)
		}
		for id, st := range states {
			prior[id] = azurefocus.ManifestState{
				Key:          st.ManifestKey,
				ETag:         st.ManifestETag,
				LastModified: st.ManifestLastModified,
				Size:         st.ManifestSize,
			}
		}
	}
	periods, err := azurefocus.Discover(ctx, accountURL, containerName, prefix, prior, store)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	var out []syncOutcome
	for _, p := range periods {
		if p.Err != nil {
			t.Fatalf("period %s: %v", p.Billing, p.Err)
		}
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
		out = append(out, syncOutcome{
			period: p.Billing, unchanged: res.Unchanged, replaced: res.Replaced, records: res.Records,
			prevCost: res.PreviousBilledCost.String(), newCost: res.NewBilledCost.String(),
		})
	}
	return out
}

// TestSyncAttributionCacheAndTupleSkip proves the two-layer zero-fetch
// design with the instrumented fake: an unchanged re-sync performs ZERO
// Get Blob calls in BOTH delivery modes — the CreateNewReport month's
// superseded manifest is attributed from the persistent cache instead of
// being re-fetched — and a --force pass re-fetches ONLY the two current
// manifests (the superseded one still comes from the cache) before
// short-circuiting on the content hash.
func TestSyncAttributionCacheAndTupleSkip(t *testing.T) {
	ctx := context.Background()
	fake, accountURL := startFake(t, fixture)
	hermeticAzureEnv(t, true)
	store := openStore(t)

	// Sync 1: fresh — May (5 records) and June (5 records) ingest; the
	// three manifests are fetched and attributed.
	for _, o := range syncAll(t, ctx, store, accountURL, false) {
		if o.skipped || o.unchanged || o.records != 5 {
			t.Fatalf("first sync %s = %+v, want 5 fresh records", o.period, o)
		}
	}

	// Sync 2: unchanged — both periods tuple-skip with ZERO Get Blob
	// calls: no data files, no manifests, not even June's superseded one.
	before := len(fake.GetBlobKeys())
	for _, o := range syncAll(t, ctx, store, accountURL, false) {
		if !o.skipped {
			t.Fatalf("unchanged re-sync %s = %+v, want skipped", o.period, o)
		}
	}
	if calls := fake.GetBlobKeys()[before:]; len(calls) != 0 {
		t.Fatalf("unchanged re-sync performed %d Get Blob call(s): %v", len(calls), calls)
	}

	// Forced sync: the tuple skip is bypassed — the two CURRENT
	// manifests are re-fetched for their blob lists and the pipeline
	// streams the periods' data files before the store short-circuits on
	// the unchanged content hash. The superseded June manifest, however,
	// is STILL attributed from the persistent cache: nothing of the
	// superseded run is ever fetched again.
	before = len(fake.GetBlobKeys())
	for _, o := range syncAll(t, ctx, store, accountURL, true) {
		if o.skipped || !o.unchanged {
			t.Fatalf("forced sync %s = %+v, want processed via the hash path", o.period, o)
		}
	}
	calls := fake.GetBlobKeys()[before:]
	var manifests int
	for _, call := range calls {
		if strings.Contains(call, "8f7e6d5c") {
			t.Errorf("forced sync fetched the superseded run: %s", call)
		}
		if strings.HasSuffix(call, "manifest.json") {
			manifests++
		}
	}
	if manifests != 2 || len(calls) != 5 {
		t.Fatalf("forced sync = %d Get Blob call(s) (%d manifests), want 5 with exactly the 2 current manifests: %v",
			len(calls), manifests, calls)
	}
}

// TestSyncRestatedMonth proves the 5-day prior-month regeneration path:
// the May month folder is replaced wholesale (overwrite mode changes the
// run folder name every run), the re-sync replaces exactly that period
// with the documented BilledCost delta, and June still tuple-skips.
func TestSyncRestatedMonth(t *testing.T) {
	ctx := context.Background()
	tree := t.TempDir()
	copyTree(t, fixture, tree)
	_, accountURL := startFake(t, tree)
	hermeticAzureEnv(t, true)
	store := openStore(t)

	for _, o := range syncAll(t, ctx, store, accountURL, false) {
		if o.skipped || o.unchanged {
			t.Fatalf("first sync %s = %+v, want fresh", o.period, o)
		}
	}
	assertDaily(t, ctx, store, map[string]map[string]string{
		"2026-05-01": {"Storage": "2.5", "Virtual Machines": "9.5"}, // 10.00 - 0.50 June-delivered correction (D26c)
		"2026-05-02": {"Contoso Software Ltd": "5"},
		"2026-05-03": {"Savings Plan Compute": "3", "Virtual Machines": "0"},
		"2026-06-01": {"Virtual Machines": "8"},
		"2026-06-02": {"Storage": "2"},
		"2026-06-03": {"Azure App Service": "4", "Azure Credit": "-1.5"},
	})

	// The regeneration: a NEW run folder replaces the May month folder.
	if err := os.RemoveAll(filepath.Join(tree, account, containerName, prefix, "mtd-snapshot")); err != nil {
		t.Fatalf("removing the old May run: %v", err)
	}
	copyTree(t, restated, tree)

	outcomes := syncAll(t, ctx, store, accountURL, false)
	if !outcomes[0].replaced || outcomes[0].records != 5 {
		t.Fatalf("restated May = %+v, want replaced with 5 records", outcomes[0])
	}
	if outcomes[0].prevCost != mayTotal || outcomes[0].newCost != mayRestatedTotal {
		t.Errorf("restated May BilledCost = %s -> %s, want %s -> %s",
			outcomes[0].prevCost, outcomes[0].newCost, mayTotal, mayRestatedTotal)
	}
	if !outcomes[1].skipped {
		t.Errorf("June after the May restatement = %+v, want tuple-skipped", outcomes[1])
	}
	assertDaily(t, ctx, store, map[string]map[string]string{
		"2026-05-01": {"Storage": "2.5", "Virtual Machines": "8.5"}, // 9.00 - 0.50
		"2026-05-02": {"Contoso Software Ltd": "5"},
		"2026-05-03": {"Savings Plan Compute": "3", "Virtual Machines": "0"},
		"2026-06-01": {"Virtual Machines": "8"},
		"2026-06-02": {"Storage": "2"},
		"2026-06-03": {"Azure App Service": "4", "Azure Credit": "-1.5"},
	})
}

func assertDaily(t *testing.T, ctx context.Context, store storage.Store, want map[string]map[string]string) {
	t.Helper()
	daily, err := store.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{}, "", "")
	if err != nil {
		t.Fatalf("DailyCostsByService: %v", err)
	}
	if len(daily.Days) != len(want) {
		t.Fatalf("store holds %d day(s), want %d: %+v", len(daily.Days), len(want), daily.Days)
	}
	for _, day := range daily.Days {
		date := day.Date.Format(time.DateOnly)
		wantServices, ok := want[date]
		if !ok {
			t.Errorf("unexpected day %s", date)
			continue
		}
		if len(day.Services) != len(wantServices) {
			t.Errorf("day %s services = %+v, want %d", date, day.Services, len(wantServices))
			continue
		}
		for _, svc := range day.Services {
			if got := svc.Cost.String(); got != wantServices[svc.ServiceName] {
				t.Errorf("day %s %s = %s, want %s", date, svc.ServiceName, got, wantServices[svc.ServiceName])
			}
		}
	}
}

// TestRecordsRedeliveryRaceReportsActionably proves an If-Match race —
// a data blob replaced between Discover and Records, so the pinned ETag
// no longer matches (HTTP 412 ConditionNotMet) — surfaces the actionable
// re-run message instead of a raw SDK error.
func TestRecordsRedeliveryRaceReportsActionably(t *testing.T) {
	ctx := context.Background()
	tree := t.TempDir()
	copyTree(t, fixture, tree)
	_, accountURL := startFake(t, tree)
	hermeticAzureEnv(t, true)
	store := openStore(t)

	periods, err := azurefocus.Discover(ctx, accountURL, containerName, prefix, nil, store)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// Replace a May partition after discovery: same key, new bytes, new
	// content-derived ETag.
	target := filepath.Join(tree, account, containerName, prefix, "mtd-snapshot/current/part_0_0001.csv.gz")
	if err := os.WriteFile(target, []byte("replaced"), 0o644); err != nil {
		t.Fatalf("replacing partition: %v", err)
	}

	reader, err := periods[0].Conn.Records(ctx)
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	defer func() { _ = reader.Close() }()
	_, err = reader.Next()
	if err == nil {
		t.Fatal("Next succeeded despite the mid-read replacement")
	}
	for _, part := range []string{"replaced mid-read", "re-run ingest"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("412 race error %q does not contain %q", err, part)
		}
	}
}

// TestDiscoverPerPeriodAnomalies proves per-period degradation: a
// byteCount mismatch (the documented replaced-mid-read signature) or an
// unsupported delivery format poisons its period only.
func TestDiscoverPerPeriodAnomalies(t *testing.T) {
	ctx := context.Background()

	t.Run("byteCount mismatch", func(t *testing.T) {
		tree := t.TempDir()
		copyTree(t, fixture, tree)
		// Corrupt a June data blob so its listed size disagrees with the
		// (unchanged) manifest byteCount.
		target := filepath.Join(tree, account, containerName, prefix,
			"20260601-20260630/1a2b3c4d-5e6f-7089-9abc-def012345678/part_0_0001.csv.gz")
		if err := os.WriteFile(target, []byte("truncated"), 0o644); err != nil {
			t.Fatalf("truncating blob: %v", err)
		}
		_, accountURL := startFake(t, tree)
		hermeticAzureEnv(t, true)
		store := openStore(t)

		periods, err := azurefocus.Discover(ctx, accountURL, containerName, prefix, nil, store)
		if err != nil {
			t.Fatalf("Discover aborted on a per-period anomaly: %v", err)
		}
		if periods[0].Err != nil || periods[0].Conn == nil {
			t.Errorf("healthy May = %+v, want readable", periods[0])
		}
		if periods[1].Err == nil {
			t.Fatal("June carries no error despite the byteCount mismatch")
		}
		for _, part := range []string{"bytes", "replaced mid-read", "re-run ingest",
			// The named manifest path must be the real one — prefix once.
			containerName + "/" + prefix + "/20260601-20260630/1a2b3c4d-5e6f-7089-9abc-def012345678/manifest.json"} {
			if !strings.Contains(periods[1].Err.Error(), part) {
				t.Errorf("byteCount error %q does not contain %q", periods[1].Err, part)
			}
		}
		if strings.Contains(periods[1].Err.Error(), prefix+"/"+prefix) {
			t.Errorf("byteCount error doubles the prefix: %v", periods[1].Err)
		}
	})

	t.Run("non-gzip delivery config", func(t *testing.T) {
		tree := t.TempDir()
		copyTree(t, fixture, tree)
		manifest := filepath.Join(tree, account, containerName, prefix, "mtd-snapshot/current/manifest.json")
		body, err := os.ReadFile(manifest)
		if err != nil {
			t.Fatalf("reading manifest: %v", err)
		}
		body = []byte(strings.Replace(string(body), `"compressionMode": "gzip"`, `"compressionMode": "None"`, 1))
		if err := os.WriteFile(manifest, body, 0o644); err != nil {
			t.Fatalf("rewriting manifest: %v", err)
		}
		_, accountURL := startFake(t, tree)
		hermeticAzureEnv(t, true)
		store := openStore(t)

		periods, err := azurefocus.Discover(ctx, accountURL, containerName, prefix, nil, store)
		if err != nil {
			t.Fatalf("Discover aborted on a per-period anomaly: %v", err)
		}
		if periods[0].Err == nil || !strings.Contains(periods[0].Err.Error(), "file format CSV and compression gzip") {
			t.Errorf("non-gzip May = %+v, want the configure-CSV+gzip error", periods[0])
		}
		if periods[1].Err != nil || periods[1].Conn == nil {
			t.Errorf("healthy June = %+v, want readable", periods[1])
		}
	})
}

func TestDiscoverErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("empty prefix", func(t *testing.T) {
		tree := t.TempDir()
		if err := os.MkdirAll(filepath.Join(tree, account, containerName, "unrelated"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, accountURL := startFake(t, tree)
		hermeticAzureEnv(t, true)
		store := openStore(t)
		_, err := azurefocus.Discover(ctx, accountURL, containerName, prefix, nil, store)
		if err == nil || !strings.Contains(err.Error(), "no export manifests found") {
			t.Errorf("empty prefix error = %v, want the no-manifests message", err)
		}
	})

	t.Run("missing container", func(t *testing.T) {
		tree := t.TempDir()
		if err := os.MkdirAll(filepath.Join(tree, account), 0o755); err != nil {
			t.Fatal(err)
		}
		_, accountURL := startFake(t, tree)
		hermeticAzureEnv(t, true)
		store := openStore(t)
		_, err := azurefocus.Discover(ctx, accountURL, "no-such-container", prefix, nil, store)
		if err == nil || !strings.Contains(err.Error(), "container not found") {
			t.Errorf("missing container error = %v, want the container-not-found message", err)
		}
	})

	t.Run("access denied names the role and scope", func(t *testing.T) {
		fake, accountURL := startFake(t, fixture)
		fake.Forbid(account + "/" + containerName + "/" + prefix)
		hermeticAzureEnv(t, true)
		store := openStore(t)
		_, err := azurefocus.Discover(ctx, accountURL, containerName, prefix, nil, store)
		if err == nil {
			t.Fatal("Discover succeeded despite 403")
		}
		for _, part := range []string{"access denied", "Storage Blob Data Reader", "containers/<container>"} {
			if !strings.Contains(err.Error(), part) {
				t.Errorf("403 error %q does not mention %q", err, part)
			}
		}
	})

	t.Run("insecure escape refused for https", func(t *testing.T) {
		hermeticAzureEnv(t, true)
		store := openStore(t)
		_, err := azurefocus.Discover(ctx, "https://acct.blob.core.windows.net/", containerName, prefix, nil, store)
		if err == nil || !strings.Contains(err.Error(), "test-only escape") {
			t.Errorf("https + insecure escape = %v, want the refusal", err)
		}
	})

	t.Run("SAS-shaped prefix refused without echoing it", func(t *testing.T) {
		hermeticAzureEnv(t, true)
		store := openStore(t)
		_, err := azurefocus.Discover(ctx, "https://acct.blob.core.windows.net/", containerName,
			"dir/exp?sp=r&sig=SECRETVALUE", nil, store)
		if err == nil || !strings.Contains(err.Error(), "no SAS tokens") {
			t.Fatalf("SAS-shaped prefix = %v, want the refusal", err)
		}
		if strings.Contains(err.Error(), "SECRETVALUE") {
			t.Errorf("refusal echoes the SAS token: %v", err)
		}
	})

	t.Run("SAS-shaped account URL refused", func(t *testing.T) {
		hermeticAzureEnv(t, true)
		store := openStore(t)
		_, err := azurefocus.Discover(ctx, "https://acct.blob.core.windows.net/?sv=2024&sig=SECRETVALUE", containerName, prefix, nil, store)
		if err == nil || !strings.Contains(err.Error(), "no SAS tokens") {
			t.Fatalf("SAS URL = %v, want the refusal", err)
		}
		if strings.Contains(err.Error(), "SECRETVALUE") {
			t.Errorf("refusal echoes the SAS token: %v", err)
		}
	})

	// Slice-4 review fix-up: credential-shaped --account-url inputs are
	// refused WITHOUT echoing anything credential-shaped. A connection
	// string parses as a scheme-less URL whose AccountKey survives scrubURL,
	// so that path must echo nothing at all; userinfo and fragments are
	// refused like query strings.
	t.Run("connection-string account URL refused without echoing the key", func(t *testing.T) {
		hermeticAzureEnv(t, true)
		store := openStore(t)
		_, err := azurefocus.Discover(ctx,
			"DefaultEndpointsProtocol=https;AccountName=devacct;AccountKey=SECRETKEYVALUE==;EndpointSuffix=core.windows.net",
			containerName, prefix, nil, store)
		if err == nil || !strings.Contains(err.Error(), "invalid --account-url") {
			t.Fatalf("connection string = %v, want the invalid-account-url refusal", err)
		}
		if strings.Contains(err.Error(), "SECRETKEYVALUE") {
			t.Errorf("refusal echoes the account key: %v", err)
		}
	})

	t.Run("userinfo-bearing account URL refused", func(t *testing.T) {
		hermeticAzureEnv(t, true)
		store := openStore(t)
		_, err := azurefocus.Discover(ctx, "https://user:SECRETPASS@acct.blob.core.windows.net/", containerName, prefix, nil, store)
		if err == nil || !strings.Contains(err.Error(), "userinfo") {
			t.Fatalf("userinfo URL = %v, want the userinfo refusal", err)
		}
		if strings.Contains(err.Error(), "SECRETPASS") {
			t.Errorf("refusal echoes the userinfo secret: %v", err)
		}
	})

	t.Run("fragment-bearing account URL refused", func(t *testing.T) {
		hermeticAzureEnv(t, true)
		store := openStore(t)
		_, err := azurefocus.Discover(ctx, "https://acct.blob.core.windows.net/#SECRETFRAGMENT", containerName, prefix, nil, store)
		if err == nil || !strings.Contains(err.Error(), "fragment") {
			t.Fatalf("fragment URL = %v, want the fragment refusal", err)
		}
		if strings.Contains(err.Error(), "SECRETFRAGMENT") {
			t.Errorf("refusal echoes the fragment: %v", err)
		}
	})
}

// TestDiscoverMissingCredentials scrubs the ambient chain, pins it to
// EnvironmentCredential via AZURE_TOKEN_CREDENTIALS (making the failure
// deterministic and fast — no IMDS probe, no az/azd lookup), and proves
// the connector fails fast with the actionable chain message. The https
// URL is never dialed: the bearer policy requests a token before
// sending, and the pinned credential fails immediately.
func TestDiscoverMissingCredentials(t *testing.T) {
	hermeticAzureEnv(t, false)
	t.Setenv("AZURE_TOKEN_CREDENTIALS", "EnvironmentCredential")
	store := openStore(t)

	start := time.Now()
	_, err := azurefocus.Discover(context.Background(), "https://127.0.0.1:1/devaccount", containerName, prefix, nil, store)
	if err == nil {
		t.Fatal("Discover succeeded without any credentials")
	}
	for _, part := range []string{"ambient credential chain", "Storage Blob Data Reader", "AZURE_TOKEN_CREDENTIALS"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("credentials error %q lacks %q", err, part)
		}
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("credential failure took %s, want fast failure", elapsed)
	}
}
