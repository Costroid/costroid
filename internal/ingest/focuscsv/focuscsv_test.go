// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package focuscsv_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/ingest"
	"github.com/Costroid/costroid/internal/ingest/focuscsv"
	"github.com/Costroid/costroid/internal/storage"
)

const (
	fx10      = "../../../testdata/focus-csv/focus-1.0.csv"
	fx11      = "../../../testdata/focus-csv/focus-1.1.csv"
	fx12      = "../../../testdata/focus-csv/focus-1.2.csv"
	fx13      = "../../../testdata/focus-csv/focus-1.3.csv"
	fx14      = "../../../testdata/focus-csv/focus-1.4.csv"
	fxHazard  = "../../../testdata/focus-csv/hazard.csv.gz"
	fxBadTS   = "../../../testdata/focus-csv/negative/nonrfc3339-1.0.csv"
	fxMinim   = "../../../testdata/focus-csv/negative/focus-1.4-minimal.csv"
	fxNull    = "../../../testdata/focus-csv/negative/literal-null.csv"
	fxNullTS  = "../../../testdata/focus-csv/negative/null-chargeperiodstart.csv"
	fxDup     = "../../../testdata/focus-csv/negative/duplicate-header.csv"
	fxUnk     = "../../../testdata/focus-csv/negative/unknown-header.csv"
	fxLenient = "../../../testdata/focus-csv/lenient/lenient-1.4.csv"
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

func writeTemp(t *testing.T, name string, b []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return path
}

func gzipBytes(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// minimalCSV is a valid, two-month FOCUS 1.4 CSV carrying exactly the 15
// not-null columns (LF line endings, no trailing BOM).
func minimalCSV() string {
	return "BilledCost,BillingAccountId,BillingCurrency,BillingPeriodEnd,BillingPeriodStart,ChargeCategory," +
		"ChargePeriodEnd,ChargePeriodStart,ContractedCost,EffectiveCost,InvoiceIssuerName,ListCost," +
		"ServiceCategory,ServiceName,ServiceProviderName\n" +
		"1.00,999,USD,2026-06-01T00:00:00Z,2026-05-01T00:00:00Z,Usage,2026-05-02T00:00:00Z,2026-05-01T00:00:00Z,1.00,1.00,Co,1.00,Compute,Svc,Co\n" +
		"2.00,999,USD,2026-07-01T00:00:00Z,2026-06-01T00:00:00Z,Usage,2026-06-02T00:00:00Z,2026-06-01T00:00:00Z,2.00,2.00,Co,2.00,Compute,Svc,Co\n"
}

func discover(t *testing.T, path string, v focus.Version, label string) ([]focuscsv.Period, []string) {
	t.Helper()
	periods, warnings, err := focuscsv.Discover(path, v, label, false) // strict (the default)
	if err != nil {
		t.Fatalf("Discover(%s, %s): %v", path, v, err)
	}
	return periods, warnings
}

// discoverLenient is the --lenient sibling of discover: it opts into zone-bearing
// UTC timestamp-format tolerance. Kept separate so discover's ~20 strict call
// sites stay unchanged.
func discoverLenient(t *testing.T, path string, v focus.Version, label string) ([]focuscsv.Period, []string) {
	t.Helper()
	periods, warnings, err := focuscsv.Discover(path, v, label, true)
	if err != nil {
		t.Fatalf("Discover(%s, %s, lenient): %v", path, v, err)
	}
	return periods, warnings
}

// ingestPeriods runs every discovered period through the shared pipeline and
// returns the per-month results keyed by month.
func ingestPeriods(t *testing.T, store storage.Store, periods []focuscsv.Period, tenant string) map[string]ingest.Result {
	t.Helper()
	out := map[string]ingest.Result{}
	for _, p := range periods {
		res, err := ingest.Run(context.Background(), p.Conn, store, tenant)
		if err != nil {
			t.Fatalf("ingest %s/%s: %v", p.Conn.SourceIdentity(), p.Month, err)
		}
		out[p.Month] = res
	}
	return out
}

// --- version gating: 1.0/1.1/1.2/1.3/1.4 accepted; 1.0r2 canonicalizes to 1.0 ---

func TestParseVersionAccepts(t *testing.T) {
	// 1.0 and 1.1 are now accepted (they were rejected pre-slice).
	for _, v := range []focus.Version{focus.V1_0, focus.V1_1, focus.V1_2, focus.V1_3, focus.V1_4} {
		if err := focuscsv.ParseVersion(v); err != nil {
			t.Errorf("ParseVersion(%s) = %v, want nil (accepted)", v, err)
		}
	}
	// The required/unsupported messages advertise the full 1.0…1.4 range.
	if err := focuscsv.ParseVersion(""); err == nil ||
		!strings.Contains(err.Error(), "required") || !strings.Contains(err.Error(), "1.0, 1.1, 1.2, 1.3, 1.4") {
		t.Errorf("ParseVersion(empty) = %v, want a 'required' error listing 1.0, 1.1, 1.2, 1.3, 1.4", err)
	}
	if err := focuscsv.ParseVersion(focus.Version("2.0")); err == nil ||
		!strings.Contains(err.Error(), "unsupported") || !strings.Contains(err.Error(), "1.0, 1.1, 1.2, 1.3, 1.4") {
		t.Errorf("ParseVersion(2.0) = %v, want an 'unsupported' error listing the range", err)
	}
	// 1.0r2 is NOT accepted by ParseVersion directly — Discover canonicalizes it to
	// 1.0 FIRST (see TestVersion10r2CanonicalizesToV10). This pins that ordering: if
	// canonicalization moved into ParseVersion, the raw "1.0r2" would flow into the
	// downstream maps that have no 1.0r2 entry.
	if err := focuscsv.ParseVersion(focus.Version("1.0r2")); err == nil {
		t.Errorf("ParseVersion(1.0r2) = nil, want it unsupported pre-canonicalization (Discover canonicalizes it upstream)")
	}
}

// TestVersion10And11Import proves 1.0 and 1.1 are accepted and imported (they were
// rejected before this slice): fx10 is a two-month conformant 1.0 export, fx11 a
// two-month conformant 1.1 export.
func TestVersion10And11Import(t *testing.T) {
	for _, tc := range []struct {
		path    string
		version focus.Version
	}{
		{fx10, focus.V1_0},
		{fx11, focus.V1_1},
	} {
		periods, warnings := discover(t, tc.path, tc.version, "legacy")
		if len(warnings) != 0 {
			t.Errorf("%s: unexpected warnings %v", tc.path, warnings)
		}
		if len(periods) != 2 {
			t.Fatalf("%s: periods = %d, want 2 (2026-05, 2026-06)", tc.path, len(periods))
		}
		if got := periods[0].Conn.FOCUSVersion(); got != tc.version {
			t.Errorf("%s: FOCUSVersion = %q, want %q", tc.path, got, tc.version)
		}
	}
}

// TestVersion10r2CanonicalizesToV10 proves the 1.0r2 alias flows through
// canonicalization: a "1.0r2" declaration yields a Connector whose FOCUSVersion is
// V1_0 (so the 1.0 known/mandatory set + transform are what run), and the fixture
// imports under it — not merely that ParseVersion returned nil.
func TestVersion10r2CanonicalizesToV10(t *testing.T) {
	periods, warnings := discover(t, fx10, focus.Version("1.0r2"), "oci-r2")
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings %v", warnings)
	}
	if len(periods) != 2 {
		t.Fatalf("periods = %d, want 2", len(periods))
	}
	if got := periods[0].Conn.FOCUSVersion(); got != focus.V1_0 {
		t.Errorf("FOCUSVersion = %q, want 1.0 (1.0r2 must canonicalize to 1.0)", got)
	}
	// It imports under the 1.0 path (2 records/month) — proving the canonical
	// version reaches the transform + tables, not just ParseVersion.
	store := openStore(t)
	res := ingestPeriods(t, store, periods, focus.DefaultTenant)
	if res["2026-05"].Records != 2 || res["2026-06"].Records != 2 {
		t.Errorf("1.0r2 import records = %+v, want 2 per month", res)
	}
}

// TestFocus10MarketplaceServiceProviderSynthesized proves the 1.0 fixture's
// marketplace row (PublisherName != ProviderName, no native ServiceProviderName)
// synthesizes ServiceProviderName ← PublisherName / HostProviderName ← ProviderName
// through the REGISTERED 1.0 transform — the connector-level analogue of the focus
// package's unit completeness test (service_provider_name is INSERT-only in the
// store, so this cannot be asserted through a read surface / the e2e).
func TestFocus10MarketplaceServiceProviderSynthesized(t *testing.T) {
	periods, _ := discover(t, fx10, focus.V1_0, "oci")
	transform, err := focus.TransformTo14(focus.V1_0)
	if err != nil {
		t.Fatalf("TransformTo14(1.0): %v", err)
	}
	r, err := periods[0].Conn.Records(context.Background()) // 2026-05 holds the marketplace row
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	defer func() { _ = r.Close() }()
	found := false
	for {
		row, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if row.Record["PublisherName"] == row.Record["ProviderName"] {
			continue // native row; the marketplace row diverges
		}
		found = true
		if _, ok := row.Record["ServiceProviderName"]; ok {
			t.Errorf("fixture setup: the 1.0 row carries a native ServiceProviderName (1.0 has none)")
		}
		out, err := transform(row.Record)
		if err != nil {
			t.Fatalf("transform: %v", err)
		}
		if out["ServiceProviderName"] != row.Record["PublisherName"] {
			t.Errorf("ServiceProviderName = %q, want the synthesized PublisherName %q",
				out["ServiceProviderName"], row.Record["PublisherName"])
		}
		if out["HostProviderName"] != row.Record["ProviderName"] {
			t.Errorf("HostProviderName = %q, want the synthesized ProviderName %q",
				out["HostProviderName"], row.Record["ProviderName"])
		}
	}
	if !found {
		t.Fatal("no marketplace row (PublisherName != ProviderName) found in the 1.0 fixture")
	}
}

// TestNonRFC3339TimestampRejected is the strict-parser boundary: a 1.0 file that is
// spec-shaped EXCEPT for a space-separated (non-RFC3339) ChargePeriodStart passes
// the version + header + month-bucketing gates but is REJECTED at the row level
// with the actionable, row-numbered timestamp error — proving the shared strict
// parser was NOT relaxed for 1.0.
func TestNonRFC3339TimestampRejected(t *testing.T) {
	store := openStore(t)
	periods, warnings := discover(t, fxBadTS, focus.V1_0, "bad")
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings %v", warnings)
	}
	if len(periods) != 1 {
		t.Fatalf("periods = %d, want 1 (BillingPeriodStart is valid, so the month gate passes)", len(periods))
	}
	_, err := ingest.Run(context.Background(), periods[0].Conn, store, focus.DefaultTenant)
	if err == nil {
		t.Fatal("non-RFC3339 ChargePeriodStart ingested, want a row-level rejection")
	}
	var rowErrs *ingest.RowErrors
	if !errors.As(err, &rowErrs) {
		t.Fatalf("err = %v (%T), want *ingest.RowErrors", err, err)
	}
	if rowErrs.First[0].Row != 1 {
		t.Errorf("offending row = %d, want 1", rowErrs.First[0].Row)
	}
	msg := rowErrs.First[0].Errs[0].Error()
	if !strings.Contains(msg, "ChargePeriodStart") || !strings.Contains(msg, "ISO 8601") {
		t.Errorf("row error %q, want a ChargePeriodStart ISO-8601 violation (strict parser not relaxed)", msg)
	}
}

// --- per-month split, identity, ContentHash shape ---

func TestDiscoverSplitsByMonth(t *testing.T) {
	periods, _ := discover(t, fx14, focus.V1_4, "contoso")
	if len(periods) != 2 {
		t.Fatalf("periods = %d, want 2 (2026-05, 2026-06)", len(periods))
	}
	if periods[0].Month != "2026-05" || periods[1].Month != "2026-06" {
		t.Errorf("months = %s, %s, want 2026-05, 2026-06 (sorted)", periods[0].Month, periods[1].Month)
	}
	if got := periods[0].Conn.SourceIdentity(); got != "contoso/2026-05" {
		t.Errorf("SourceIdentity = %q, want contoso/2026-05", got)
	}
	if got := periods[0].Conn.FOCUSVersion(); got != focus.V1_4 {
		t.Errorf("FOCUSVersion = %q, want 1.4", got)
	}
	// Each month's hash is a sha256 digest and the two months differ.
	h05, _ := periods[0].Conn.ContentHash(context.Background())
	h06, _ := periods[1].Conn.ContentHash(context.Background())
	if !strings.HasPrefix(h05, "sha256:") || len(h05) != len("sha256:")+64 {
		t.Errorf("hash = %q, want a sha256 digest", h05)
	}
	if h05 == h06 {
		t.Errorf("both months share a ContentHash %q", h05)
	}

	// Each month's reader yields ONLY that month's rows, with file-global row
	// numbers (2 rows/month; June rows are numbered 3 and 4).
	nums := monthRowNumbers(t, periods[1].Conn)
	if !slices.Equal(nums, []int{3, 4}) {
		t.Errorf("June row numbers = %v, want [3 4] (file-global)", nums)
	}
}

func monthRowNumbers(t *testing.T, conn *focuscsv.Connector) []int {
	t.Helper()
	r, err := conn.Records(context.Background())
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	defer func() { _ = r.Close() }()
	var nums []int
	for {
		row, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		nums = append(nums, row.Number)
	}
	return nums
}

// --- full ingest of the three conformant fixtures ---

func TestIngestConformantFixtures(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)

	for _, tc := range []struct {
		path    string
		version focus.Version
		label   string
		service string
	}{
		{fx10, focus.V1_0, "oci-conf", "OCI Compute"},
		{fx11, focus.V1_1, "cloudnine-conf", "CloudNine Object Storage"},
		{fx12, focus.V1_2, "aws-may-june", "Amazon Elastic Compute Cloud"},
		{fx13, focus.V1_3, "gcp-marketplace", "Datadog Pro"},
		{fx14, focus.V1_4, "azure-export", "Azure Virtual Machines"},
	} {
		periods, warnings := discover(t, tc.path, tc.version, tc.label)
		if len(warnings) != 0 {
			t.Errorf("%s: unexpected warnings %v", tc.path, warnings)
		}
		results := ingestPeriods(t, store, periods, focus.DefaultTenant)
		if len(results) != 2 {
			t.Errorf("%s: ingested %d months, want 2", tc.path, len(results))
		}
		for m, res := range results {
			if res.Unchanged || res.Records != 2 {
				t.Errorf("%s %s: %+v, want 2 fresh records", tc.path, m, res)
			}
			if res.Batch.SourceIdentity != tc.label+"/"+m {
				t.Errorf("%s %s: identity %q, want %s/%s", tc.path, m, res.Batch.SourceIdentity, tc.label, m)
			}
		}
	}

	// All three services surface in the daily view.
	daily, err := store.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyCostsByService: %v", err)
	}
	seen := map[string]bool{}
	for _, d := range daily.Days {
		for _, s := range d.Services {
			seen[s.ServiceName] = true
		}
	}
	for _, svc := range []string{"OCI Compute", "CloudNine Object Storage", "Amazon Elastic Compute Cloud", "Datadog Pro", "Azure Virtual Machines"} {
		if !seen[svc] {
			t.Errorf("daily view missing service %q", svc)
		}
	}

	// Re-importing an identical month under the same label short-circuits.
	periods, _ := discover(t, fx14, focus.V1_4, "azure-export")
	res, err := ingest.Run(ctx, periods[0].Conn, store, focus.DefaultTenant)
	if err != nil {
		t.Fatalf("re-ingest: %v", err)
	}
	if !res.Unchanged {
		t.Errorf("identical re-import = %+v, want Unchanged", res)
	}
}

// TestFocus10And11BilledTotals pins the conformant 1.0/1.1 fixtures' BilledCost
// totals end-to-end through the store — the money invariant for the new transforms.
// COUPLED to the fixtures: the 1.0 fixture's BilledCost column sums to 10 (1+2+3+4)
// and the 1.1 fixture's to 20 (5+5+4+6). Each fixture ingests into its own store,
// so summing every service across every day equals that fixture's total.
func TestFocus10And11BilledTotals(t *testing.T) {
	for _, tc := range []struct {
		path    string
		version focus.Version
		label   string
		want    string
	}{
		{fx10, focus.V1_0, "oci-total", "10"},
		{fx11, focus.V1_1, "cloudnine-total", "20"},
	} {
		store := openStore(t)
		periods, _ := discover(t, tc.path, tc.version, tc.label)
		ingestPeriods(t, store, periods, focus.DefaultTenant)
		daily, err := store.DailyCostsByService(context.Background(), focus.DefaultTenant, time.Time{}, time.Time{})
		if err != nil {
			t.Fatalf("%s: DailyCostsByService: %v", tc.path, err)
		}
		total := decimal.Zero
		for _, d := range daily.Days {
			for _, s := range d.Services {
				total = total.Add(s.Cost)
			}
		}
		if !total.Equal(decimal.RequireFromString(tc.want)) {
			t.Errorf("%s BilledCost total = %s, want %s", tc.path, total, tc.want)
		}
	}
}

// TestNativeServiceProviderPreserved proves the 1.3 fixture's DIVERGING
// ServiceProviderName (a marketplace row where the native successor differs
// from the deprecated PublisherName) survives the registered 1.3 transform
// unchanged — i.e. 1.3 is NOT routed through the 1.2 entity mapping.
func TestNativeServiceProviderPreserved(t *testing.T) {
	periods, _ := discover(t, fx13, focus.V1_3, "gcp-marketplace")
	transform, err := focus.TransformTo14(focus.V1_3)
	if err != nil {
		t.Fatalf("TransformTo14(1.3): %v", err)
	}
	r, err := periods[0].Conn.Records(context.Background())
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	defer func() { _ = r.Close() }()
	row, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	// The fixture diverges on purpose: ServiceProviderName != PublisherName.
	if row.Record["ServiceProviderName"] == row.Record["PublisherName"] {
		t.Fatalf("fixture setup: ServiceProviderName == PublisherName (%q); no divergence to test",
			row.Record["ServiceProviderName"])
	}
	out, err := transform(row.Record)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if out["ServiceProviderName"] != "Datadog" {
		t.Errorf("ServiceProviderName = %q, want the native Datadog (not overwritten from PublisherName)",
			out["ServiceProviderName"])
	}
}

// --- format detection (GEN-1) ---

func TestFormatDetection(t *testing.T) {
	content := []byte(minimalCSV())

	// gzip content named .csv → magic bytes win, it gunzips and ingests.
	gzNamedCSV := writeTemp(t, "looks-plain.csv", gzipBytes(t, content))
	if p, _ := discover(t, gzNamedCSV, focus.V1_4, "g"); len(p) != 2 {
		t.Errorf("gzip-named-.csv split into %d months, want 2", len(p))
	}

	// plain content named .gz → no magic, name/content mismatch → error.
	plainNamedGz := writeTemp(t, "looks-gz.gz", content)
	if _, _, err := focuscsv.Discover(plainNamedGz, focus.V1_4, "g", false); err == nil ||
		!strings.Contains(err.Error(), "named .gz") {
		t.Errorf("plain-named-.gz err = %v, want a name/content mismatch error", err)
	}

	// empty file → error.
	empty := writeTemp(t, "empty.csv", nil)
	if _, _, err := focuscsv.Discover(empty, focus.V1_4, "g", false); err == nil ||
		!strings.Contains(err.Error(), "empty") {
		t.Errorf("empty-file err = %v, want an empty-file error", err)
	}

	// binary container (NUL bytes) → error naming accepted formats.
	binary := writeTemp(t, "data.csv", append([]byte("PAR1\x00\x00\x00"), content...))
	if _, _, err := focuscsv.Discover(binary, focus.V1_4, "g", false); err == nil ||
		!strings.Contains(err.Error(), "gzip-compressed CSV") {
		t.Errorf("binary-file err = %v, want an accepted-formats error", err)
	}

	// header-only file (no data rows) → error.
	headerOnly := writeTemp(t, "header.csv", []byte(strings.SplitN(minimalCSV(), "\n", 2)[0]+"\n"))
	if _, _, err := focuscsv.Discover(headerOnly, focus.V1_4, "g", false); err == nil ||
		!strings.Contains(err.Error(), "no data rows") {
		t.Errorf("header-only err = %v, want a no-data-rows error", err)
	}
}

// --- header policy (GEN-2 / GEN-3) ---

func TestUnknownHeaderRejectedWithHints(t *testing.T) {
	// An unknown NON-x_ column fails naming it.
	_, _, err := focuscsv.Discover(fxUnk, focus.V1_4, "u", false)
	if err == nil || !strings.Contains(err.Error(), "MadeUpColumn") {
		t.Errorf("unknown-header err = %v, want it to name MadeUpColumn", err)
	}

	// Hint 1: a 1.2-shaped file declared 1.4 → ProviderName/PublisherName are
	// unknown in 1.4 → suggest 1.2 or 1.3.
	_, _, err = focuscsv.Discover(fx12, focus.V1_4, "h", false)
	if err == nil || !strings.Contains(err.Error(), "ProviderName") ||
		!strings.Contains(err.Error(), "1.2 or 1.3") {
		t.Errorf("1.2-as-1.4 err = %v, want the ProviderName/1.2-or-1.3 hint", err)
	}

	// Hint 2: a 1.3-shaped file declared 1.2 → ServiceProviderName is unknown in
	// 1.2 → suggest 1.3 (or 1.4).
	_, _, err = focuscsv.Discover(fx13, focus.V1_2, "h", false)
	if err == nil || !strings.Contains(err.Error(), "ServiceProviderName") ||
		!strings.Contains(err.Error(), "1.3 (or 1.4)") {
		t.Errorf("1.3-as-1.2 err = %v, want the ServiceProviderName/1.3-or-1.4 hint", err)
	}
}

func TestMissingMandatoryHint13To12(t *testing.T) {
	// Hint 3: a 1.2-shaped file declared 1.3 → its ProviderName/PublisherName are
	// valid 1.3 headers (so unknown-header can't fire), but 1.3-mandatory
	// ServiceProviderName is absent → missing-mandatory failure + the 1.2 hint.
	_, _, err := focuscsv.Discover(fx12, focus.V1_3, "h", false)
	if err == nil || !strings.Contains(err.Error(), "ServiceProviderName") ||
		!strings.Contains(err.Error(), "re-run with --focus-version 1.2") {
		t.Errorf("1.2-as-1.3 err = %v, want the missing-ServiceProviderName/1.2 hint", err)
	}
}

func TestDuplicateHeaderRejected(t *testing.T) {
	_, _, err := focuscsv.Discover(fxDup, focus.V1_4, "d", false)
	if err == nil || !strings.Contains(err.Error(), "duplicate") ||
		!strings.Contains(err.Error(), "BilledCost") {
		t.Errorf("duplicate-header err = %v, want a duplicate BilledCost error", err)
	}
}

func TestXPrefixColumnsAcceptedAndDropped(t *testing.T) {
	// x_awesome_column_one is NOT PascalCase after x_ — it must still be accepted
	// (PascalCase is only a SHOULD), and dropped by the transform.
	csv := "BilledCost,BillingAccountId,BillingCurrency,BillingPeriodEnd,BillingPeriodStart,ChargeCategory," +
		"ChargePeriodEnd,ChargePeriodStart,ContractedCost,EffectiveCost,InvoiceIssuerName,ListCost," +
		"ServiceCategory,ServiceName,ServiceProviderName,x_awesome_column_one\n" +
		"1.00,999,USD,2026-06-01T00:00:00Z,2026-05-01T00:00:00Z,Usage,2026-05-02T00:00:00Z,2026-05-01T00:00:00Z,1.00,1.00,Co,1.00,Compute,Svc,Co,anything\n"
	path := writeTemp(t, "xcol.csv", []byte(csv))
	store := openStore(t)
	periods, _ := discover(t, path, focus.V1_4, "x")
	res := ingestPeriods(t, store, periods, focus.DefaultTenant)["2026-05"]
	if res.Records != 1 {
		t.Errorf("records = %d, want 1 (x_ column accepted and dropped, not rejected)", res.Records)
	}
}

func TestMandatoryPresence(t *testing.T) {
	store := openStore(t)

	// 1.4 file missing only Mandatory-but-NULLABLE columns → WARNING, ingests.
	periods, warnings := discover(t, fxMinim, focus.V1_4, "minimal")
	if len(warnings) != 1 || !strings.Contains(warnings[0], "DatasetConfiguration") {
		t.Fatalf("minimal-1.4 warnings = %v, want one DatasetConfiguration warning", warnings)
	}
	for _, absent := range []string{"BillingAccountName", "ChargeClass", "HostProviderName", "PricingUnit"} {
		if !strings.Contains(warnings[0], absent) {
			t.Errorf("warning does not name absent nullable column %q: %s", absent, warnings[0])
		}
	}
	if got := ingestPeriods(t, store, periods, focus.DefaultTenant); len(got) != 2 {
		t.Errorf("minimal-1.4 ingested %d months, want 2 (warn, do not fail)", len(got))
	}

	// 1.4 file missing a NOT-NULL column → FAIL (drop ServiceName from minimal).
	noSvc := strings.Replace(minimalCSV(), "ServiceCategory,ServiceName,ServiceProviderName",
		"ServiceCategory,ServiceProviderName", 1)
	// also drop the ServiceName field from each data row (14 fields now)
	noSvc = dropField(t, noSvc, 13)
	p14 := writeTemp(t, "no-servicename.csv", []byte(noSvc))
	if _, _, err := focuscsv.Discover(p14, focus.V1_4, "x", false); err == nil ||
		!strings.Contains(err.Error(), "required non-null") || !strings.Contains(err.Error(), "ServiceName") {
		t.Errorf("1.4 missing ServiceName err = %v, want a required-non-null ServiceName failure", err)
	}
}

// dropField removes the (0-based) idx column from a header+rows CSV whose rows
// have no quoting (test helper only).
func dropField(t *testing.T, csv string, idx int) string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(strings.TrimRight(csv, "\n"), "\n") {
		fields := strings.Split(line, ",")
		fields = slices.Delete(fields, idx, idx+1)
		out = append(out, strings.Join(fields, ","))
	}
	return strings.Join(out, "\n") + "\n"
}

// --- cell semantics (GEN-4): a literal "null" is not null ---

func TestLiteralNullFailsAtRowLevel(t *testing.T) {
	store := openStore(t)
	periods, _ := discover(t, fxNull, focus.V1_4, "n")
	_, err := ingest.Run(context.Background(), periods[0].Conn, store, focus.DefaultTenant)
	if err == nil {
		t.Fatal("literal-null ingest succeeded, want a row-level failure")
	}
	var rowErrs *ingest.RowErrors
	if !errors.As(err, &rowErrs) {
		t.Fatalf("err = %v (%T), want *ingest.RowErrors", err, err)
	}
	if rowErrs.First[0].Row != 1 {
		t.Errorf("offending row = %d, want 1", rowErrs.First[0].Row)
	}
	msg := rowErrs.First[0].Errs[0].Error()
	if !strings.Contains(msg, "BilledCost") || !strings.Contains(msg, "valid decimal") {
		t.Errorf("row error %q, want a BilledCost not-a-decimal violation (literal null flows through)", msg)
	}
}

// --- ContentHash pinning (GEN-6) ---

func TestContentHashPinning(t *testing.T) {
	content := []byte(minimalCSV())
	hashOf := func(path string) (string, string) {
		p, _ := discover(t, path, focus.V1_4, "c")
		h05, _ := p[0].Conn.ContentHash(context.Background())
		h06, _ := p[1].Conn.ContentHash(context.Background())
		return h05, h06
	}

	plain := writeTemp(t, "plain.csv", content)
	gzipped := writeTemp(t, "same.csv.gz", gzipBytes(t, content))
	p05, p06 := hashOf(plain)
	g05, g06 := hashOf(gzipped)
	// A .csv and its identical .csv.gz hash the same (decompressed domain).
	if p05 != g05 || p06 != g06 {
		t.Errorf("plain vs gzip hashes differ: %s/%s vs %s/%s", p05, p06, g05, g06)
	}

	// A leading BOM does not change the hash (it is stripped before hashing).
	bommed := writeTemp(t, "bom.csv", append([]byte{0xEF, 0xBB, 0xBF}, content...))
	b05, _ := hashOf(bommed)
	if b05 != p05 {
		t.Errorf("BOM changed the hash: %s vs %s", b05, p05)
	}

	// A CRLF rewrite of the SAME logical rows counts as changed (line endings
	// are hashed as-is).
	crlf := writeTemp(t, "crlf.csv", []byte(strings.ReplaceAll(string(content), "\n", "\r\n")))
	c05, _ := hashOf(crlf)
	if c05 == p05 {
		t.Errorf("CRLF rewrite hashed identically to LF (%s); line endings must be as-is", c05)
	}

	// A HEADER-ONLY change invalidates EVERY month's hash. Build a file with an
	// x_ column, then rename ONLY that header cell — the data rows stay
	// byte-identical, so the hashes can differ only because the header is part
	// of each month's hash.
	withX := strings.Replace(minimalCSV(), "ServiceProviderName\n", "ServiceProviderName,x_Note\n", 1)
	lines := strings.Split(strings.TrimRight(withX, "\n"), "\n")
	for i := 1; i < len(lines); i++ {
		lines[i] += ",note" + strconv.Itoa(i) // give each data row a value in the new column
	}
	withX = strings.Join(lines, "\n") + "\n"
	baseX := writeTemp(t, "withx.csv", []byte(withX))
	bx05, bx06 := hashOf(baseX)

	renamed := strings.Replace(withX, "ServiceProviderName,x_Note\n", "ServiceProviderName,x_Remark\n", 1)
	if strings.Count(renamed, "note1") != 1 { // guard: only the header line changed
		t.Fatalf("test setup: the data rows were altered by the header rename")
	}
	ren := writeTemp(t, "renamed.csv", []byte(renamed))
	r05, r06 := hashOf(ren)
	if r05 == bx05 || r06 == bx06 {
		t.Errorf("header-only rename did not invalidate both months' hashes: %s/%s vs %s/%s", r05, r06, bx05, bx06)
	}
}

// --- unparseable BillingPeriodStart fails before storing (GEN-5) ---

func TestUnparseableBillingPeriodStartFailsImport(t *testing.T) {
	bad := strings.Replace(minimalCSV(), "2026-05-01T00:00:00Z,Usage,2026-05-02", "not-a-date,Usage,2026-05-02", 1)
	path := writeTemp(t, "bad-bps.csv", []byte(bad))
	_, _, err := focuscsv.Discover(path, focus.V1_4, "b", false)
	if err == nil || !strings.Contains(err.Error(), "row 1") ||
		!strings.Contains(err.Error(), "BillingPeriodStart") {
		t.Errorf("unparseable BillingPeriodStart err = %v, want a row-1 BillingPeriodStart failure", err)
	}
}

// TestEmptyBillingPeriodStartFailsImport covers the monthOf null branch
// (focuscsv.go: "BillingPeriodStart is null"): an EMPTY BillingPeriodStart cell
// (distinct from an unparseable one) cannot be assigned to a billing month, so
// the whole import fails, row-numbered, before anything is stored. This branch
// had zero coverage.
func TestEmptyBillingPeriodStartFailsImport(t *testing.T) {
	// Empty only field 5 (BillingPeriodStart), leaving ChargeCategory in place;
	// "2026-05-01T00:00:00Z,Usage,2026-05-02" uniquely identifies that field pair
	// (ChargePeriodStart is followed by ContractedCost, not by ",Usage,").
	bad := strings.Replace(minimalCSV(), "2026-05-01T00:00:00Z,Usage,2026-05-02", ",Usage,2026-05-02", 1)
	path := writeTemp(t, "empty-bps.csv", []byte(bad))
	_, _, err := focuscsv.Discover(path, focus.V1_4, "b", false)
	if err == nil || !strings.Contains(err.Error(), "row 1") ||
		!strings.Contains(err.Error(), "BillingPeriodStart is null") {
		t.Errorf("empty BillingPeriodStart err = %v, want a row-1 'BillingPeriodStart is null' failure", err)
	}
}

// TestHeaderlessFileRejected covers the analyze() "no header row" branch: a
// file that is non-empty and non-binary (so it passes the format gate) but
// yields NO header row — a lone blank line, which encoding/csv skips to EOF —
// fails distinctly from the header-only "no data rows" case. This branch had
// zero coverage.
func TestHeaderlessFileRejected(t *testing.T) {
	path := writeTemp(t, "blank.csv", []byte("\n"))
	if _, _, err := focuscsv.Discover(path, focus.V1_4, "h", false); err == nil ||
		!strings.Contains(err.Error(), "no header row") {
		t.Errorf("headerless-file err = %v, want the 'no header row' error", err)
	}
}

// --- default label is the file base name ---

func TestDefaultLabelIsBaseName(t *testing.T) {
	periods, _ := discover(t, fx14, focus.V1_4, "") // empty label
	if got := periods[0].Conn.SourceIdentity(); got != "focus-1.4.csv/2026-05" {
		t.Errorf("default-label identity = %q, want focus-1.4.csv/2026-05", got)
	}
}

// --- scientific notation → exact decimal (D23/D25), no float64 ---

func TestScientificNotationParsesExactly(t *testing.T) {
	// A >17-significant-digit scientific literal that float64 MANGLES: the exact
	// value is 123.4567890123456789, but float64 rounds it to
	// 123.45678901234568. If ParseDecimal ever routed through float64, the
	// stored value would be the mangled one — so this ingests the literal end to
	// end and asserts the daily-view total is EXACTLY the unmangled decimal
	// (decisions D23/D25).
	const exact = "123.4567890123456789"
	csv := "BilledCost,BillingAccountId,BillingCurrency,BillingPeriodEnd,BillingPeriodStart,ChargeCategory," +
		"ChargePeriodEnd,ChargePeriodStart,ContractedCost,EffectiveCost,InvoiceIssuerName,ListCost," +
		"ServiceCategory,ServiceName,ServiceProviderName\n" +
		exact + "e0,999,USD,2026-06-01T00:00:00Z,2026-05-01T00:00:00Z,Usage,2026-05-02T00:00:00Z,2026-05-01T00:00:00Z," +
		exact + "e0," + exact + "e0,Co," + exact + "e0,Compute,SciSvc,Co\n"
	path := writeTemp(t, "sci.csv", []byte(csv))
	store := openStore(t)
	periods, _ := discover(t, path, focus.V1_4, "sci")
	ingestPeriods(t, store, periods, focus.DefaultTenant)

	daily, err := store.DailyCostsByService(context.Background(), focus.DefaultTenant, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyCostsByService: %v", err)
	}
	var got decimal.Decimal
	found := false
	for _, d := range daily.Days {
		for _, s := range d.Services {
			if s.ServiceName == "SciSvc" {
				got, found = s.Cost, true
			}
		}
	}
	if !found {
		t.Fatal("SciSvc not found in the daily view")
	}
	if !got.Equal(decimal.RequireFromString(exact)) {
		t.Errorf("scientific BilledCost stored as %s, want exactly %s (no float64 rounding)", got, exact)
	}
	// And prove it is NOT the float64-mangled value, so the test genuinely
	// discriminates a float64 regression.
	if got.Equal(decimal.NewFromFloat(123.4567890123456789)) {
		t.Errorf("stored value equals the float64-mangled literal (%s); ParseDecimal must not use float64", got)
	}
}

// --- embedded comma + newline in quoted fields survive (RFC 4180) ---

func TestQuotedEmbeddedCommaAndNewlineSurvive(t *testing.T) {
	// One row whose ChargeDescription holds a comma AND a newline, and whose
	// Tags hold a comma — none of which may split the record.
	csv := "BilledCost,BillingAccountId,BillingCurrency,BillingPeriodEnd,BillingPeriodStart,ChargeCategory," +
		"ChargePeriodEnd,ChargePeriodStart,ContractedCost,EffectiveCost,InvoiceIssuerName,ListCost," +
		"ServiceCategory,ServiceName,ServiceProviderName,ChargeDescription,Tags\n" +
		"1.00,999,USD,2026-06-01T00:00:00Z,2026-05-01T00:00:00Z,Usage,2026-05-02T00:00:00Z,2026-05-01T00:00:00Z,1.00,1.00,Co,1.00,Compute,Svc,Co," +
		"\"line one, still one\nline two\",\"{\"\"env\"\":\"\"prod, dev\"\"}\"\n"
	path := writeTemp(t, "quoted.csv", []byte(csv))
	store := openStore(t)
	periods, _ := discover(t, path, focus.V1_4, "q")
	if len(periods) != 1 {
		t.Fatalf("periods = %d, want 1 (the embedded newline must NOT create a second record)", len(periods))
	}

	// Read the parsed record back and confirm the embedded comma, the embedded
	// newline, and the escaped double-quotes survived byte-intact (not just that
	// the record count was 1).
	r, err := periods[0].Conn.Records(context.Background())
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	row, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	_ = r.Close()
	if got, want := row.Record["ChargeDescription"], "line one, still one\nline two"; got != want {
		t.Errorf("ChargeDescription = %q, want %q (embedded comma + newline intact)", got, want)
	}
	if got, want := row.Record["Tags"], `{"env":"prod, dev"}`; got != want {
		t.Errorf("Tags = %q, want %q (escaped quotes + embedded comma intact)", got, want)
	}

	res := ingestPeriods(t, store, periods, focus.DefaultTenant)["2026-05"]
	if res.Records != 1 {
		t.Errorf("records = %d, want 1 (embedded comma/newline did not split the row)", res.Records)
	}
}

// --- the combined hazard fixture is an integration smoke test ---

func TestHazardFixtureIngests(t *testing.T) {
	store := openStore(t)
	periods, warnings := discover(t, fxHazard, focus.V1_4, "hazard")
	if len(warnings) != 0 {
		t.Errorf("hazard warnings = %v, want none", warnings)
	}
	if len(periods) != 2 {
		t.Fatalf("hazard periods = %d, want 2", len(periods))
	}
	for m, res := range ingestPeriods(t, store, periods, focus.DefaultTenant) {
		if res.Records != 1 {
			t.Errorf("hazard %s: %d records, want 1", m, res.Records)
		}
	}
}

// --- generated column tables: pinned counts and 1.4 partition invariants ---

func TestColumnTablesPinned(t *testing.T) {
	// Exercised through Discover's behaviour: a file carrying EXACTLY one of the
	// two 1.4-added columns is known in 1.4 but unknown in 1.3.
	added := "InvoiceDetailId" // added in 1.4, absent from 1.3
	csv := "BilledCost,BillingAccountId,BillingCurrency,BillingPeriodEnd,BillingPeriodStart,ChargeCategory," +
		"ChargePeriodEnd,ChargePeriodStart,ContractedCost,EffectiveCost,InvoiceIssuerName,ListCost," +
		"ServiceCategory,ServiceName,ServiceProviderName," + added + "\n" +
		"1.00,999,USD,2026-06-01T00:00:00Z,2026-05-01T00:00:00Z,Usage,2026-05-02T00:00:00Z,2026-05-01T00:00:00Z,1.00,1.00,Co,1.00,Compute,Svc,Co,det-1\n"
	path := writeTemp(t, "added.csv", []byte(csv))
	if _, _, err := focuscsv.Discover(path, focus.V1_4, "k", false); err != nil {
		t.Errorf("InvoiceDetailId rejected under 1.4: %v", err)
	}
	// Declared 1.3, the same 1.4-only column is unknown → rejected.
	if _, _, err := focuscsv.Discover(path, focus.V1_3, "k", false); err == nil ||
		!strings.Contains(err.Error(), "InvoiceDetailId") {
		t.Errorf("InvoiceDetailId under 1.3 err = %v, want it rejected as unknown", err)
	}
}

// --- gzip edge cases (magic present, body degenerate) ---

func TestGzipDegenerateBodies(t *testing.T) {
	// A VALID gzip of EMPTY content: magic present, decompresses to nothing.
	emptyGz := writeTemp(t, "empty.csv", gzipBytes(t, nil))
	if _, _, err := focuscsv.Discover(emptyGz, focus.V1_4, "g", false); err == nil ||
		!strings.Contains(err.Error(), "decompressed to nothing") {
		t.Errorf("empty-gzip err = %v, want a 'decompressed to nothing' error", err)
	}

	// Bytes that carry the gzip magic (1f 8b) but are NOT valid gzip.
	badGz := writeTemp(t, "bad.csv", []byte{0x1f, 0x8b, 0xff, 0xff, 0x00, 0x01})
	if _, _, err := focuscsv.Discover(badGz, focus.V1_4, "g", false); err == nil ||
		!strings.Contains(err.Error(), "not valid gzip") {
		t.Errorf("gzip-magic-but-invalid-body err = %v, want a 'not valid gzip' error", err)
	}
}

// --- isolated BOM tolerance: a BOM-prefixed file INGESTS (header matches) ---

func TestBOMPrefixedFileIngests(t *testing.T) {
	// Without the BOM strip, the first header cell becomes a BOM-prefixed
	// "BilledCost", an unknown non-x_ column, and Discover fails — so a clean
	// ingest proves the strip happened before header matching.
	withBOM := append([]byte{0xEF, 0xBB, 0xBF}, []byte(minimalCSV())...)
	path := writeTemp(t, "bom.csv", withBOM)
	store := openStore(t)
	periods, _ := discover(t, path, focus.V1_4, "bom")
	results := ingestPeriods(t, store, periods, focus.DefaultTenant)
	if len(results) != 2 || results["2026-05"].Records != 1 {
		t.Errorf("BOM-prefixed file did not ingest cleanly: %+v", results)
	}
}

// --- isolated CRLF tolerance: a CRLF-lined file ingests successfully ---

func TestCRLFLinedFileIngests(t *testing.T) {
	crlf := strings.ReplaceAll(minimalCSV(), "\n", "\r\n")
	path := writeTemp(t, "crlf.csv", []byte(crlf))
	store := openStore(t)
	periods, _ := discover(t, path, focus.V1_4, "crlf")
	if len(periods) != 2 {
		t.Fatalf("CRLF file split into %d months, want 2", len(periods))
	}
	results := ingestPeriods(t, store, periods, focus.DefaultTenant)
	if results["2026-05"].Records != 1 || results["2026-06"].Records != 1 {
		t.Errorf("CRLF-lined file did not ingest cleanly: %+v", results)
	}
}

// --- deterministic ordering of column lists in error messages ---

func TestUnknownHeaderListedInFileOrder(t *testing.T) {
	// Two unknown non-x_ columns in NON-alphabetical file order: the error must
	// list them in FILE order ("Zebra" before "Apple"), not sorted.
	csv := "Zebra,Apple,BilledCost,BillingCurrency,ServiceName\n" +
		"z,a,1.00,USD,Svc\n"
	path := writeTemp(t, "nonalpha-unknown.csv", []byte(csv))
	_, _, err := focuscsv.Discover(path, focus.V1_4, "o", false)
	if err == nil {
		t.Fatal("expected an unknown-header error")
	}
	msg := err.Error()
	if !strings.Contains(msg, `"Zebra", "Apple"`) {
		t.Errorf("unknown-header error does not list columns in file order:\n%s", msg)
	}
	if strings.Index(msg, "Zebra") > strings.Index(msg, "Apple") {
		t.Errorf("unknown columns listed sorted, want file order (Zebra before Apple):\n%s", msg)
	}
}

func TestMissingMandatory12ListedSorted(t *testing.T) {
	// A 1.2-declared file (the 1.2 arm) missing TWO mandatory columns, BilledCost
	// and ServiceName. Both must appear, and in SORTED order (BilledCost before
	// ServiceName). Every present column is a valid 1.2 column, so the unknown
	// arm cannot fire — only the missing-mandatory arm.
	//
	// Header = the 21 1.2-Mandatory columns MINUS {BilledCost, ServiceName}.
	header := "BillingAccountId,BillingAccountName,BillingCurrency,BillingPeriodEnd,BillingPeriodStart," +
		"ChargeCategory,ChargeClass,ChargeDescription,ChargePeriodEnd,ChargePeriodStart,ContractedCost," +
		"EffectiveCost,InvoiceIssuerName,ListCost,PricingQuantity,PricingUnit,ProviderName,PublisherName,ServiceCategory"
	row := "999,Acct,USD,2026-06-01T00:00:00Z,2026-05-01T00:00:00Z,Usage,,desc,2026-05-02T00:00:00Z," +
		"2026-05-01T00:00:00Z,1.00,1.00,Issuer,1.00,1,Hrs,AWS,AWS,Compute"
	path := writeTemp(t, "missing2-12.csv", []byte(header+"\n"+row+"\n"))
	_, _, err := focuscsv.Discover(path, focus.V1_2, "m", false)
	if err == nil {
		t.Fatal("expected a missing-mandatory error for the 1.2 file")
	}
	msg := err.Error()
	iB, iS := strings.Index(msg, "BilledCost"), strings.Index(msg, "ServiceName")
	if iB < 0 || iS < 0 {
		t.Fatalf("missing-mandatory error must name BOTH BilledCost and ServiceName:\n%s", msg)
	}
	if iB > iS {
		t.Errorf("missing columns not in sorted order (BilledCost must precede ServiceName):\n%s", msg)
	}
}

// --- UTC month bucketing: BillingPeriodStart is normalized to UTC ---

func TestMonthBucketingUsesUTC(t *testing.T) {
	// 2026-05-31T20:00:00-05:00 == 2026-06-01T01:00:00Z, so the row belongs to
	// 2026-06 (UTC), not 2026-05 (its local wall-clock month). If monthOf did
	// not normalize to UTC, it would bucket into 2026-05.
	csv := "BilledCost,BillingAccountId,BillingCurrency,BillingPeriodEnd,BillingPeriodStart,ChargeCategory," +
		"ChargePeriodEnd,ChargePeriodStart,ContractedCost,EffectiveCost,InvoiceIssuerName,ListCost," +
		"ServiceCategory,ServiceName,ServiceProviderName\n" +
		"1.00,999,USD,2026-07-01T00:00:00Z,2026-05-31T20:00:00-05:00,Usage,2026-06-01T02:00:00Z,2026-06-01T01:00:00Z,1.00,1.00,Co,1.00,Compute,Svc,Co\n"
	path := writeTemp(t, "offset.csv", []byte(csv))
	periods, _ := discover(t, path, focus.V1_4, "utc")
	if len(periods) != 1 {
		t.Fatalf("periods = %d, want 1", len(periods))
	}
	if periods[0].Month != "2026-06" {
		t.Errorf("offset BillingPeriodStart bucketed into %s, want 2026-06 (UTC-normalized)", periods[0].Month)
	}
}

// --- ContentHash spans come from InputOffset, never a physical-newline split ---

func TestContentHashSpansMultiLineRecords(t *testing.T) {
	// One record whose ChargeDescription is a QUOTED field with an embedded
	// newline (the record spans two physical lines). The byte that differs
	// between the two files is on the SECOND physical line of that record. A
	// hasher that split on physical '\n' would capture only the first line and
	// miss the change → identical hashes. csv.Reader.InputOffset spans the whole
	// record → the hashes MUST differ.
	build := func(tail string) []byte {
		hdr := "BilledCost,BillingAccountId,BillingCurrency,BillingPeriodEnd,BillingPeriodStart,ChargeCategory," +
			"ChargePeriodEnd,ChargePeriodStart,ContractedCost,EffectiveCost,InvoiceIssuerName,ListCost," +
			"ServiceCategory,ServiceName,ServiceProviderName,ChargeDescription\n"
		row := "1.00,999,USD,2026-06-01T00:00:00Z,2026-05-01T00:00:00Z,Usage,2026-05-02T00:00:00Z,2026-05-01T00:00:00Z," +
			"1.00,1.00,Co,1.00,Compute,Svc,Co,\"first line\nsecond line " + tail + "\"\n"
		return []byte(hdr + row)
	}
	hashOf := func(b []byte) string {
		path := writeTemp(t, "ml.csv", b)
		p, _, err := focuscsv.Discover(path, focus.V1_4, "ml", false)
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		h, _ := p[0].Conn.ContentHash(context.Background())
		return h
	}
	hA := hashOf(build("AAA"))
	hB := hashOf(build("BBB"))
	if hA == hB {
		t.Errorf("a change on the second physical line of a multi-line record did not change the hash "+
			"(%s) — spans must come from csv.Reader.InputOffset, not a '\\n' split", hA)
	}
}

// --- --lenient: zone-bearing UTC timestamp-format tolerance (opt-in) ---

// monthServices reads one connector's month of records (exercising the lenient
// reader.Next path) and returns the set of ServiceName values in it.
func monthServices(t *testing.T, conn *focuscsv.Connector) map[string]bool {
	t.Helper()
	r, err := conn.Records(context.Background())
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	defer func() { _ = r.Close() }()
	out := map[string]bool{}
	for {
		row, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		out[row.Record["ServiceName"]] = true
	}
	return out
}

// TestLenientAcceptsZoneBearingQuirks is the primary end-to-end proof for
// --lenient AND the "both paths lenient-aware" guard (test #6). It is COUPLED to
// lenient-1.4.csv: May holds 1 row (OCI Compute, BilledCost 1) whose
// BillingPeriodStart is the no-seconds "2026-05-01T00:00Z" form; June holds 2 rows
// (Boundary Crosser, BilledCost 2, whose BillingPeriodStart "2026-05-31T20:00-05:00"
// is June 01T01:00Z in UTC; BigQuery Export, BilledCost 3, whose BillingPeriodStart
// is the space+"UTC" form). Grand total BilledCost = 6.
//
// The Discover/analyze month-split reaches focus.ParseTime ONLY via
// monthOf(BillingPeriodStart), so a fixture with quirk BillingPeriodStart values
// can be discovered at all only if the lenient thread reaches analyze; the full
// ingest below re-streams every row through reader.Next, so the per-month record
// counts also prove the reader path normalizes. (See TestStrictRejectsLenientFixture
// for the inverse: the same file is REJECTED without --lenient.)
func TestLenientAcceptsZoneBearingQuirks(t *testing.T) {
	store := openStore(t)
	periods, warnings := discoverLenient(t, fxLenient, focus.V1_4, "lenient")
	if len(warnings) != 0 { // the fixture carries all 21 Mandatory columns → no warning
		t.Errorf("unexpected warnings %v", warnings)
	}
	if len(periods) != 2 || periods[0].Month != "2026-05" || periods[1].Month != "2026-06" {
		t.Fatalf("periods = %+v, want [2026-05 2026-06]", periods)
	}

	results := ingestPeriods(t, store, periods, focus.DefaultTenant)
	// Per-month record counts (a cross-month misbucket of the boundary row would
	// flip these to 2/1 — the grand total alone cannot catch that).
	if results["2026-05"].Records != 1 {
		t.Errorf("2026-05 records = %d, want 1", results["2026-05"].Records)
	}
	if results["2026-06"].Records != 2 {
		t.Errorf("2026-06 records = %d, want 2 (incl. the UTC-boundary row)", results["2026-06"].Records)
	}
	// Per-month batch identities.
	if got := results["2026-05"].Batch.SourceIdentity; got != "lenient/2026-05" {
		t.Errorf("May batch identity = %q, want lenient/2026-05", got)
	}
	if got := results["2026-06"].Batch.SourceIdentity; got != "lenient/2026-06" {
		t.Errorf("June batch identity = %q, want lenient/2026-06", got)
	}

	// Exact stored money total (COUPLED to the fixture: 1 + 2 + 3 = 6).
	daily, err := store.DailyCostsByService(context.Background(), focus.DefaultTenant, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyCostsByService: %v", err)
	}
	total := decimal.Zero
	for _, d := range daily.Days {
		for _, s := range d.Services {
			total = total.Add(s.Cost)
		}
	}
	if !total.Equal(decimal.RequireFromString("6")) {
		t.Errorf("lenient fixture BilledCost total = %s, want 6", total)
	}

	// Per-MONTH cost (step-0 finding 2b): a grand total alone cannot catch a
	// cross-month misbucket that preserves the sum. COUPLED to the fixture: May
	// holds the single 1.00 row; June holds 2.00 + 3.00 = 5.00 (incl. the
	// UTC-boundary row whose local-wall-clock May ChargePeriodStart is June in UTC).
	monthCost := map[string]decimal.Decimal{}
	for _, d := range daily.Days {
		m := d.Date.UTC().Format("2006-01")
		for _, s := range d.Services {
			monthCost[m] = monthCost[m].Add(s.Cost)
		}
	}
	if got := monthCost["2026-05"]; !got.Equal(decimal.RequireFromString("1")) {
		t.Errorf("May BilledCost = %s, want 1.00", got)
	}
	if got := monthCost["2026-06"]; !got.Equal(decimal.RequireFromString("5")) {
		t.Errorf("June BilledCost = %s, want 5.00 (incl. the UTC-boundary row)", got)
	}

	// The boundary row lands in JUNE (its UTC month), not its local-wall-clock May.
	if svc := monthServices(t, periods[1].Conn); !svc["Boundary Crosser"] {
		t.Errorf("June services = %v, want the UTC-boundary row (2026-05-31T20:00-05:00 == June UTC)", svc)
	}
	if svc := monthServices(t, periods[0].Conn); svc["Boundary Crosser"] {
		t.Errorf("May services = %v, must NOT contain the boundary row (it is June in UTC)", svc)
	}
}

// TestStrictRejectsLenientFixture is the inverse of the accept test: strict mode
// (the default, lenient=false) does NOT accept the zone-bearing quirks. Discovering
// the lenient fixture WITHOUT --lenient fails at analyze (monthOf→focus.ParseTime)
// on the May row's no-seconds BillingPeriodStart — proving --lenient is load-bearing
// (deleting the lenient thread into analyze would make this fixture undiscoverable,
// so the accept test genuinely exercises it).
func TestStrictRejectsLenientFixture(t *testing.T) {
	_, _, err := focuscsv.Discover(fxLenient, focus.V1_4, "strict", false)
	if err == nil || !strings.Contains(err.Error(), "is not a valid ISO 8601 date/time") ||
		!strings.Contains(err.Error(), "BillingPeriodStart") {
		t.Errorf("strict Discover of the lenient fixture err = %v, want a BillingPeriodStart ISO-8601 failure", err)
	}
}

// TestLenientStillRejectsZoneLess is the money-safety boundary: even WITH --lenient,
// a genuinely zone-less ChargePeriodStart (fxBadTS's "2026-05-01 00:00:00") is NOT
// assumed UTC — it still fails at the row level with the ChargePeriodStart ISO-8601
// error. fxBadTS's BillingPeriodStart is canonical, so Discover succeeds and the
// failure surfaces in the pipeline, not in monthOf. This is the key proof that the
// user's zone-bearing-only policy holds under --lenient.
func TestLenientStillRejectsZoneLess(t *testing.T) {
	store := openStore(t)
	periods, _ := discoverLenient(t, fxBadTS, focus.V1_0, "bad-lenient")
	if len(periods) != 1 {
		t.Fatalf("periods = %d, want 1 (BillingPeriodStart is canonical)", len(periods))
	}
	_, err := ingest.Run(context.Background(), periods[0].Conn, store, focus.DefaultTenant)
	if err == nil {
		t.Fatal("zone-less ChargePeriodStart ingested under --lenient, want a row-level rejection")
	}
	var rowErrs *ingest.RowErrors
	if !errors.As(err, &rowErrs) {
		t.Fatalf("err = %v (%T), want *ingest.RowErrors", err, err)
	}
	msg := rowErrs.First[0].Errs[0].Error()
	if !strings.Contains(msg, "ChargePeriodStart") || !strings.Contains(msg, "ISO 8601") {
		t.Errorf("row error %q, want a ChargePeriodStart ISO-8601 violation (zone-less not coerced under --lenient)", msg)
	}
}

// TestLenientDoesNotCoerceNullTokens proves --lenient is timestamp-format-only: a
// literal "null" BilledCost (fxNull) still fails identically to strict mode — no
// null-token coercion (GEN-4 / D34(d) unchanged).
func TestLenientDoesNotCoerceNullTokens(t *testing.T) {
	store := openStore(t)
	periods, _ := discoverLenient(t, fxNull, focus.V1_4, "null-lenient")
	_, err := ingest.Run(context.Background(), periods[0].Conn, store, focus.DefaultTenant)
	if err == nil {
		t.Fatal("literal-null BilledCost ingested under --lenient, want a row-level failure")
	}
	var rowErrs *ingest.RowErrors
	if !errors.As(err, &rowErrs) {
		t.Fatalf("err = %v (%T), want *ingest.RowErrors", err, err)
	}
	msg := rowErrs.First[0].Errs[0].Error()
	if !strings.Contains(msg, "BilledCost") || !strings.Contains(msg, "valid decimal") {
		t.Errorf("row error %q, want a BilledCost not-a-decimal violation (null token not coerced under --lenient)", msg)
	}
}

// TestLenientRejectsNullInRewrittenDateColumn proves --lenient does NOT coerce a
// literal null even in a Date/Time column it DOES rewrite (step-0 finding 3): a
// row with a literal "null" ChargePeriodStart (fxNullTS) still fails at the row
// level under --lenient. normalizeTimestamp("null") matches no zone-bearing
// layout, so it comes back verbatim and the strict parser rejects it — the
// rewrite path is format-normalizing only, never a null-swallowing coercion.
// (fxNullTS's BillingPeriodStart is canonical, so Discover succeeds and the
// failure surfaces in the pipeline on ChargePeriodStart, not in monthOf.)
func TestLenientRejectsNullInRewrittenDateColumn(t *testing.T) {
	store := openStore(t)
	periods, _ := discoverLenient(t, fxNullTS, focus.V1_4, "null-ts-lenient")
	if len(periods) != 1 {
		t.Fatalf("periods = %d, want 1 (BillingPeriodStart is canonical)", len(periods))
	}
	_, err := ingest.Run(context.Background(), periods[0].Conn, store, focus.DefaultTenant)
	if err == nil {
		t.Fatal("literal-null ChargePeriodStart ingested under --lenient, want a row-level rejection")
	}
	var rowErrs *ingest.RowErrors
	if !errors.As(err, &rowErrs) {
		t.Fatalf("err = %v (%T), want *ingest.RowErrors", err, err)
	}
	msg := rowErrs.First[0].Errs[0].Error()
	if !strings.Contains(msg, "ChargePeriodStart") || !strings.Contains(msg, "ISO 8601") {
		t.Errorf("row error %q, want a ChargePeriodStart ISO-8601 violation (null not coerced in a rewritten column)", msg)
	}
}
