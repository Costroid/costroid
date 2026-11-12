package export

import (
	"bytes"
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// focus11Spec and focus12Spec are the Column IDs of FOCUS 1.1 and 1.2, one
// for each file in specification/columns at the tags v1.1 (commit
// 6108d409686bfbfd74c118b2ab50f9fa1891b3ca) and v1.2 (commit
// 7296e3146fb931ee32b6496fa6e4200e7f4384f7), retrieved 2026-11-12.
var (
	focus11Spec = []string{
		"AvailabilityZone", "BilledCost", "BillingAccountId", "BillingAccountName", "BillingCurrency",
		"BillingPeriodEnd", "BillingPeriodStart", "CapacityReservationId", "CapacityReservationStatus", "ChargeCategory",
		"ChargeClass", "ChargeDescription", "ChargeFrequency", "ChargePeriodEnd", "ChargePeriodStart",
		"CommitmentDiscountCategory", "CommitmentDiscountId", "CommitmentDiscountName", "CommitmentDiscountQuantity", "CommitmentDiscountStatus",
		"CommitmentDiscountType", "CommitmentDiscountUnit", "ConsumedQuantity", "ConsumedUnit", "ContractedCost",
		"ContractedUnitPrice", "EffectiveCost", "InvoiceIssuerName", "ListCost", "ListUnitPrice",
		"PricingCategory", "PricingQuantity", "PricingUnit", "ProviderName", "PublisherName",
		"RegionId", "RegionName", "ResourceId", "ResourceName", "ResourceType",
		"ServiceCategory", "ServiceName", "ServiceSubcategory", "SkuId", "SkuMeter",
		"SkuPriceDetails", "SkuPriceId", "SubAccountId", "SubAccountName", "Tags",
	}
	focus12Spec = slices.Concat(focus11Spec, []string{
		"BillingAccountType", "InvoiceId", "PricingCurrency", "PricingCurrencyContractedUnitPrice",
		"PricingCurrencyEffectiveCost", "PricingCurrencyListUnitPrice", "SubAccountType",
	})
	// focus11Mandatory are the 1.1 columns whose feature level is Mandatory.
	focus11Mandatory = []string{
		"BilledCost", "BillingAccountId", "BillingAccountName", "BillingCurrency", "BillingPeriodEnd",
		"BillingPeriodStart", "ChargeCategory", "ChargeClass", "ChargeDescription", "ChargePeriodEnd",
		"ChargePeriodStart", "ContractedCost", "EffectiveCost", "InvoiceIssuerName", "ListCost",
		"PricingQuantity", "PricingUnit", "ProviderName", "PublisherName", "ServiceCategory", "ServiceName",
	}
)

// utcSecond is the one date form Cloudability's page names, and Ternary's
// for a timestamp.
var utcSecond = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)

// dateColumns are the four date and time columns of a row.
var dateColumns = []string{"BillingPeriodStart", "BillingPeriodEnd", "ChargePeriodStart", "ChargePeriodEnd"}

// parsedCSV is a file's header and records.
func parsedCSV(t *testing.T, body []byte) ([]string, [][]string) {
	t.Helper()
	all, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return all[0], all[1:]
}

// cellOf reads a record's column by the header's names.
func cellOf(t *testing.T, header, record []string, column string) string {
	t.Helper()
	at := slices.Index(header, column)
	if at < 0 {
		t.Fatalf("the file has no column %s", column)
	}
	return record[at]
}

func tagsOf(t *testing.T, cell string) map[string]string {
	t.Helper()
	var tags map[string]string
	if err := json.Unmarshal([]byte(cell), &tags); err != nil {
		t.Fatalf("Tags %q: %v", cell, err)
	}
	return tags
}

// holdsTheRow checks a written record against the row it came from: its
// amounts byte for byte, its currency, and the tags that join it to its
// repository, pull request, run and basis.
func holdsTheRow(t *testing.T, header, record []string, source map[string]any) {
	t.Helper()
	for _, column := range []string{"EffectiveCost", "BillingCurrency", "ConsumedQuantity"} {
		if got := cellOf(t, header, record, column); got != source[column] {
			t.Errorf("row %s: %s reads %q, and the row's is %q", source["x_CostroidRowId"], column, got, source[column])
		}
	}
	tags := tagsOf(t, cellOf(t, header, record, "Tags"))
	for _, key := range []string{"costroid/repo", "costroid/pr", "costroid/workflow_run_id", "costroid/cost_basis"} {
		if want := source["Tags"].(map[string]any)[key]; tags[key] != want {
			t.Errorf("row %s: the tag %s reads %q, want %q", source["x_CostroidRowId"], key, tags[key], want)
		}
	}
}

// TestHarnessFileHoldsTheDocumentedColumns holds the Harness file to its page:
// the mandatory fields in the page's order, then the currency and the unit;
// no null where the page allows none; a ChargeCategory among its allowed
// values; dates as instants; one billing period; and under 20,000,000 bytes.
func TestHarnessFileHoldsTheDocumentedColumns(t *testing.T) {
	pageFields := []string{
		"BillingAccountId", "BillingAccountName", "BillingPeriodStart", "BillingPeriodEnd", "ChargeCategory",
		"ChargePeriodStart", "ChargePeriodEnd", "ConsumedQuantity", "EffectiveCost", "ProviderName",
		"RegionName", "ResourceId", "ServiceName", "SkuId", "SubAccountId", "SubAccountName", "Tags",
	}
	noNulls := []string{"BillingAccountId", "BillingPeriodStart", "BillingPeriodEnd", "ChargeCategory", "ChargePeriodStart", "ChargePeriodEnd", "EffectiveCost", "ProviderName", "ServiceName", "BillingCurrency"}
	written := write(t, Harness, readFile(t, exampleRows))
	if len(written.Files) != 1 || written.Files[0].Note != "billing period 2026-10-01T00:00:00Z to 2026-11-01T00:00:00Z" {
		t.Fatalf("%d files, the first noted %q", len(written.Files), written.Files[0].Note)
	}
	file := written.Files[0]
	if len(file.Body) > 20000000 {
		t.Fatalf("the file is %d bytes", len(file.Body))
	}
	header, rows := parsedCSV(t, file.Body)
	if want := append(slices.Clone(pageFields), "BillingCurrency", "ConsumedUnit"); !slices.Equal(header, want) {
		t.Fatalf("the header is %v, want %v", header, want)
	}
	source := exampleLines(t)
	for i, record := range rows {
		for _, column := range noNulls {
			if cellOf(t, header, record, column) == "" {
				t.Errorf("row %d: %s is null, which the page allows nowhere", i+1, column)
			}
		}
		if category := cellOf(t, header, record, "ChargeCategory"); !slices.Contains([]string{"Usage", "Purchase", "Tax", "Credit", "Adjustment"}, category) {
			t.Errorf("row %d: ChargeCategory %q", i+1, category)
		}
		for _, column := range dateColumns {
			if _, err := time.Parse(time.RFC3339, cellOf(t, header, record, column)); err != nil {
				t.Errorf("row %d: %s: %v", i+1, column, err)
			}
		}
		if period := cellOf(t, header, record, "BillingPeriodStart"); period != "2026-10-01T00:00:00Z" {
			t.Errorf("row %d is in the billing period from %s", i+1, period)
		}
		if got := cellOf(t, header, record, "ProviderName"); got != source[i]["ServiceProviderName"] {
			t.Errorf("row %d: ProviderName %q", i+1, got)
		}
		if got := cellOf(t, header, record, "ConsumedUnit"); got != source[i]["ConsumedUnit"] {
			t.Errorf("row %d: ConsumedUnit %q", i+1, got)
		}
		if cellOf(t, header, record, "ResourceId") != "" {
			t.Errorf("row %d names a resource", i+1)
		}
		holdsTheRow(t, header, record, source[i])
	}
	golden(t, "harness.csv", file.Body)
}

// Rows of two billing periods go to two Harness files, the earlier first,
// since an upload's rows match one invoice month.
func TestHarnessWritesOneFileForEachBillingPeriod(t *testing.T) {
	lines := exampleLines(t)
	for _, fields := range lines[:3] {
		fields["BillingPeriodStart"], fields["BillingPeriodEnd"] = "2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z"
	}
	written := write(t, Harness, encodeLines(t, lines))
	if len(written.Files) != 2 || written.Files[0].Rows != 8 || written.Files[1].Rows != 3 {
		t.Fatalf("%d files", len(written.Files))
	}
	if written.Files[1].Note != "billing period 2026-11-01T00:00:00Z to 2026-12-01T00:00:00Z" {
		t.Errorf("the second file is noted %q", written.Files[1].Note)
	}
}

// TestHarnessSplitsAt20000000Bytes keeps each Harness file within 20,000,000
// bytes, with every row in one of them.
func TestHarnessSplitsAt20000000Bytes(t *testing.T) {
	first := exampleLines(t)[0]
	first["ServiceName"] = strings.Repeat("a", 2000)
	lines := make([]map[string]any, 11000)
	for i := range lines {
		lines[i] = first
	}
	written := write(t, Harness, encodeLines(t, lines))
	total := 0
	for i, file := range written.Files {
		if len(file.Body) > harnessMaxBytes {
			t.Errorf("file %d is %d bytes", i+1, len(file.Body))
		}
		total += file.Rows
	}
	if len(written.Files) < 2 || total != len(lines) {
		t.Fatalf("%d files hold %d rows", len(written.Files), total)
	}
}

// Harness takes no null in ProviderName, which a row reads from
// ServiceProviderName, so a row without one refuses the file.
func TestHarnessRefusesANullThePageForbids(t *testing.T) {
	lines := exampleLines(t)
	lines[3]["ServiceProviderName"] = nil
	_, err := Write(Harness, bytes.NewReader(encodeLines(t, lines)), testOptions)
	var refused *RowsError
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), "row 4 ") || !strings.Contains(err.Error(), "ProviderName is blank") {
		t.Fatalf("%v", err)
	}
}

// gunzipped reads a gzip file whole.
func gunzipped(t *testing.T, data []byte) []byte {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return plain
}

// TestCloudabilityFileIsFocus11WithItsManifest holds a Cloudability drop to
// its page: the folders vendor, report period and epoch; a manifest named
// with a capital M that names FOCUS 1.1, the bucket and the data file by its
// path under the bucket; and a gzip CSV whose columns are FOCUS 1.1's or
// custom, with every 1.1 Mandatory column, dates as UTC instants, and the
// repository, pull request, run and basis kept in Tags.
func TestCloudabilityFileIsFocus11WithItsManifest(t *testing.T) {
	written := write(t, Cloudability, readFile(t, exampleRows))
	if len(written.Files) != 2 {
		t.Fatalf("%d files, want a data file and its manifest", len(written.Files))
	}
	data, manifest := written.Files[0], written.Files[1]
	folder := "Costroid/20261001-20261101/" + strconv.FormatInt(testOptions.Now.Unix(), 10)
	if data.Name != folder+"/costroid-cost-and-usage.csv.gz" || manifest.Name != folder+"/costroid-Manifest.json" {
		t.Fatalf("the files are %s and %s", data.Name, manifest.Name)
	}
	var stated map[string]any
	if err := json.Unmarshal(manifest.Body, &stated); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"compression": "GZIP", "content_type": "CSV", "root_dir": testOptions.RootDir,
		"all_report_keys": []any{data.Name}, "updated_at": "2026-11-12T09:30:00Z", "focus_version": "1.1",
	}
	if fmt.Sprint(stated) != fmt.Sprint(want) {
		t.Errorf("the manifest states %v, want %v", stated, want)
	}
	plain := gunzipped(t, data.Body)
	header, rows := parsedCSV(t, plain)
	for _, column := range header {
		if !slices.Contains(focus11Spec, column) && !customColumn.MatchString(column) {
			t.Errorf("%s is neither a FOCUS 1.1 column nor a custom column", column)
		}
	}
	for _, column := range focus11Mandatory {
		if !slices.Contains(header, column) {
			t.Errorf("the file lacks %s, which FOCUS 1.1 makes mandatory", column)
		}
	}
	source := exampleLines(t)
	if len(rows) != len(source) || data.Rows != len(source) {
		t.Fatalf("%d rows, stated %d, for %d", len(rows), data.Rows, len(source))
	}
	for i, record := range rows {
		for _, column := range dateColumns {
			if value := cellOf(t, header, record, column); !utcSecond.MatchString(value) {
				t.Errorf("row %d: %s is %q", i+1, column, value)
			}
		}
		provider := source[i]["ServiceProviderName"]
		if cellOf(t, header, record, "ProviderName") != provider || cellOf(t, header, record, "PublisherName") != provider {
			t.Errorf("row %d: ProviderName and PublisherName are not %q", i+1, provider)
		}
		if got := cellOf(t, header, record, "x_HostProviderName"); got != source[i]["HostProviderName"] {
			t.Errorf("row %d: x_HostProviderName %q", i+1, got)
		}
		if got := cellOf(t, header, record, "BilledCost"); got != "0" {
			t.Errorf("row %d: BilledCost %q", i+1, got)
		}
		holdsTheRow(t, header, record, source[i])
	}
	golden(t, "cloudability.csv", plain)
	golden(t, "cloudability-Manifest.json", manifest.Body)
}

// Each billing period is a drop of its own, and a drop names its bucket, so a
// Cloudability export without one writes nothing.
func TestCloudabilityWritesADropForEachBillingPeriod(t *testing.T) {
	lines := exampleLines(t)
	lines[0]["BillingPeriodStart"], lines[0]["BillingPeriodEnd"] = "2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z"
	written := write(t, Cloudability, encodeLines(t, lines))
	var periods []string
	for _, file := range written.Files {
		periods = append(periods, strings.Split(file.Name, "/")[1])
	}
	if !slices.Equal(periods, []string{"20261001-20261101", "20261001-20261101", "20261101-20261201", "20261101-20261201"}) {
		t.Fatalf("the drops are %v", periods)
	}
	if _, err := Write(Cloudability, bytes.NewReader(readFile(t, exampleRows)), Options{Now: testOptions.Now}); !errors.Is(err, ErrNoRootDir) {
		t.Fatalf("%v", err)
	}
}

// bigQueryName is a valid BigQuery column name, which Ternary's page asks of
// each column.
var bigQueryName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// TestTernaryWritesOneFilePerUTCDay puts each row in the file of the UTC day
// its charge starts on, a charge over midnight in the day it started. Each
// file is named for its day, its columns are FOCUS 1.2's or custom and valid
// BigQuery names, and every row is in one file, its figures as it wrote them.
func TestTernaryWritesOneFilePerUTCDay(t *testing.T) {
	lines := exampleLines(t)
	for i, fields := range lines {
		fields["ChargePeriodStart"], fields["ChargePeriodEnd"] = "2026-10-14T23:59:58Z", "2026-10-15T00:00:01Z"
		if i >= 6 {
			fields["ChargePeriodStart"], fields["ChargePeriodEnd"] = "2026-10-15T00:00:00Z", "2026-10-15T00:00:09Z"
		}
	}
	written := write(t, Ternary, encodeLines(t, lines))
	var names []string
	seen := map[string]bool{}
	for _, file := range written.Files {
		names = append(names, file.Name)
		day := strings.TrimSuffix(file.Name, ".csv")
		header, rows := parsedCSV(t, file.Body)
		if len(rows) != file.Rows {
			t.Errorf("%s holds %d rows and states %d", file.Name, len(rows), file.Rows)
		}
		for _, column := range header {
			if !slices.Contains(focus12Spec, column) && !customColumn.MatchString(column) || !bigQueryName.MatchString(column) {
				t.Errorf("%s is not a FOCUS 1.2 column or a custom one with a BigQuery name", column)
			}
		}
		for _, record := range rows {
			start, err := time.Parse(time.RFC3339, cellOf(t, header, record, "ChargePeriodStart"))
			if err != nil || start.UTC().Format(time.DateOnly) != day {
				t.Errorf("%s holds a charge that starts %s", file.Name, start)
			}
			id := cellOf(t, header, record, "x_CostroidRowId")
			seen[id] = true
			for _, fields := range lines {
				if fields["x_CostroidRowId"] == id {
					holdsTheRow(t, header, record, fields)
				}
			}
		}
	}
	if !slices.Equal(names, []string{"2026-10-14.csv", "2026-10-15.csv"}) || written.Files[0].Rows != 6 || written.Files[1].Rows != 5 {
		t.Fatalf("the files are %v", names)
	}
	if len(seen) != len(lines) {
		t.Errorf("the files hold %d of %d rows", len(seen), len(lines))
	}
	golden(t, "ternary-2026-10-14.csv", write(t, Ternary, readFile(t, exampleRows)).Files[0].Body)
}

// A charge stated with an offset is refused, so no row is moved to another
// day to fit.
func TestTernaryRefusesAnInstantWithAnOffset(t *testing.T) {
	lines := exampleLines(t)
	lines[2]["ChargePeriodStart"] = "2026-10-14T23:30:00-02:00"
	_, err := Write(Ternary, bytes.NewReader(encodeLines(t, lines)), testOptions)
	var refused *RowsError
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), "row 3 ") || !strings.Contains(err.Error(), "ChargePeriodStart is not a UTC instant") {
		t.Fatalf("%v", err)
	}
}

// focus15Spec are the Column IDs of the candidate's Cost and Usage dataset,
// from the column table of FOCUS_spec-v1.5-cr.md at the release v1.5-cr,
// retrieved 2026-11-12.
var focus15Spec = []string{
	"AllocatedMethodDetails", "AllocatedMethodId", "AllocatedResourceId", "AllocatedResourceName", "AllocatedServiceName",
	"AllocatedTags", "AvailabilityZone", "BilledCost", "BillingAccountId", "BillingAccountName",
	"BillingAccountType", "BillingCurrency", "BillingPeriodEnd", "BillingPeriodStart", "CapacityReservationId",
	"CapacityReservationStatus", "ChargeCategory", "ChargeClass", "ChargeDescription", "ChargeFrequency",
	"ChargePeriodEnd", "ChargePeriodStart", "CommitmentDiscountCategory", "CommitmentDiscountId", "CommitmentDiscountName",
	"CommitmentDiscountQuantity", "CommitmentDiscountStatus", "CommitmentDiscountType", "CommitmentDiscountUnit", "CommitmentProgramEligibilityDetails",
	"ConsumedQuantity", "ConsumedUnit", "ContractApplied", "ContractedCost", "ContractedUnitPrice",
	"CredentialId", "EffectiveCost", "HostProviderName", "InvoiceDetailId", "InvoiceId",
	"InvoiceIssuerName", "ListCost", "ListUnitPrice", "PricingCategory", "PricingCurrency",
	"PricingCurrencyContractedUnitPrice", "PricingCurrencyEffectiveCost", "PricingCurrencyListUnitPrice", "PricingQuantity", "PricingUnit",
	"PrincipalId", "RegionId", "RegionName", "RequesterDetails", "ResourceId",
	"ResourceName", "ResourceType", "ServiceCategory", "ServiceName", "ServiceProviderName",
	"ServiceSubcategory", "SkuId", "SkuMeter", "SkuPriceDetails", "SkuPriceId",
	"SubAccountId", "SubAccountName", "SubAccountType", "Tags",
}

// focus15File is the candidate's export, each file by its name.
func focus15File(t *testing.T, written Export) map[string]File {
	t.Helper()
	files := map[string]File{}
	for _, file := range written.Files {
		files[file.Name] = file
	}
	return files
}

// TestFocus15CandidateFillsTheTokenColumns holds the candidate file to v1.5-cr:
// its columns are the candidate's or custom, with the four it adds; each
// row's SkuPriceDetails names its model and, for a billed class, the token
// direction and cache action, a cache write of either lifetime "Write" with
// the lifetime kept in x_CacheWriteLifetime; an informational class carries
// neither property; and the schema metadata declares 1.5-cr and defines each
// column once, in order.
func TestFocus15CandidateFillsTheTokenColumns(t *testing.T) {
	files := focus15File(t, write(t, Focus15CR, readFile(t, exampleRows)))
	data := files["data/cost-and-usage.csv"]
	header, rows := parsedCSV(t, data.Body)
	custom := false
	for _, column := range header {
		switch {
		case customColumn.MatchString(column):
			custom = true
		case !slices.Contains(focus15Spec, column):
			t.Errorf("%s is neither a candidate column nor a custom column", column)
		case custom:
			t.Errorf("%s follows a custom column", column)
		}
	}
	for _, column := range []string{"CredentialId", "PrincipalId", "RequesterDetails", "SkuPriceDetails"} {
		if !slices.Contains(header, column) {
			t.Errorf("the file lacks %s", column)
		}
	}
	want := map[string]map[string]string{
		"input_uncached":        {"TokenDirection": "Input", "TokenCacheAction": "Uncached"},
		"cache_read":            {"TokenDirection": "Input", "TokenCacheAction": "Read"},
		"cache_write_5m":        {"TokenDirection": "Input", "TokenCacheAction": "Write", "x_CacheWriteLifetime": "5m"},
		"cache_write_1h":        {"TokenDirection": "Input", "TokenCacheAction": "Write", "x_CacheWriteLifetime": "1h"},
		"cache_write_30m":       {"TokenDirection": "Input", "TokenCacheAction": "Write", "x_CacheWriteLifetime": "30m"},
		"cache_write_12h":       {"TokenDirection": "Input", "TokenCacheAction": "Write", "x_CacheWriteLifetime": "12h"},
		"output":                {"TokenDirection": "Output"},
		"reasoning_output":      {},
		"cached_input_reported": {},
	}
	source := exampleLines(t)
	for i, record := range rows {
		var details map[string]string
		if err := json.Unmarshal([]byte(cellOf(t, header, record, "SkuPriceDetails")), &details); err != nil {
			t.Fatalf("row %d: SkuPriceDetails: %v", i+1, err)
		}
		class := source[i]["SkuMeter"].(string)
		expected := map[string]string{"ModelId": source[i]["Tags"].(map[string]any)["costroid/model"].(string)}
		for key, value := range want[class] {
			expected[key] = value
		}
		if fmt.Sprint(details) != fmt.Sprint(expected) {
			t.Errorf("row %d, %s: SkuPriceDetails %v, want %v", i+1, class, details, expected)
		}
		if cellOf(t, header, record, "PrincipalId") != "" {
			t.Errorf("row %d names a principal, which no row states", i+1)
		}
		holdsTheRow(t, header, record, source[i])
	}
	var schema focus15Schema
	schemas := 0
	for name, file := range files {
		if strings.HasPrefix(name, "metadata/schemas/") {
			schemas++
			if err := json.Unmarshal(file.Body, &schema); err != nil {
				t.Fatal(err)
			}
		}
	}
	if schemas != 1 || schema.FocusVersion != "1.5-cr" {
		t.Fatalf("%d schemas, declaring %q", schemas, schema.FocusVersion)
	}
	var defined []string
	for _, definition := range schema.ColumnDefinition {
		defined = append(defined, definition.ColumnName)
	}
	if !slices.Equal(defined, header) {
		t.Errorf("the schema defines %v, and the file holds %v", defined, header)
	}
	var index dataIndex
	if err := json.Unmarshal(files["data/cost-and-usage-metadata.json"].Body, &index); err != nil {
		t.Fatal(err)
	}
	if index.SchemaID != schema.SchemaID || len(index.DataLocation) != 1 || index.DataLocation[0].TotalRows != len(rows) || index.DataLocation[0].TotalBytes != len(data.Body) {
		t.Errorf("the data names %+v", index)
	}
	golden(t, "focus-1.5-cr.csv", data.Body)
	golden(t, "focus-1.5-cr-schema.json", files["metadata/schemas/schema-"+schema.SchemaID+".json"].Body)
}

// TestCredentialIdNeverHoldsAKeyShape refuses a rows file with a provider key
// in SubAccountId, never repeating it, and writes no file. A SubAccountId
// that is the session id names no credential. A stated credential id is the
// CredentialId, with the Credential entry the candidate asks beside it.
func TestCredentialIdNeverHoldsAKeyShape(t *testing.T) {
	for _, key := range []string{"sk-ant-api03-" + strings.Repeat("Q", 40), "sk-proj-" + strings.Repeat("Z", 48)} {
		lines := exampleLines(t)
		lines[4]["SubAccountId"] = key
		written, err := Write(Focus15CR, bytes.NewReader(encodeLines(t, lines)), testOptions)
		var refused *RowsError
		if !errors.As(err, &refused) || len(written.Files) != 0 || !strings.Contains(err.Error(), "row 5 ") || !strings.Contains(err.Error(), "SubAccountId reads like a key") {
			t.Fatalf("Write returned %d files and %v", len(written.Files), err)
		}
		if strings.Contains(err.Error(), key[:8]) {
			t.Errorf("the refusal repeats the key: %v", err)
		}
	}
	lines := exampleLines(t)
	lines[1]["SubAccountId"] = lines[1]["Tags"].(map[string]any)["costroid/session_id"]
	data := focus15File(t, write(t, Focus15CR, encodeLines(t, lines)))["data/cost-and-usage.csv"]
	header, rows := parsedCSV(t, data.Body)
	for i, record := range rows {
		credential, requester := cellOf(t, header, record, "CredentialId"), cellOf(t, header, record, "RequesterDetails")
		switch stated, _ := lines[i]["SubAccountId"].(string); {
		case i == 1 || stated == "":
			if credential != "" || requester != "" {
				t.Errorf("row %d: CredentialId %q and RequesterDetails %q, want null", i+1, credential, requester)
			}
		case credential != stated || requester != `[{"key":"Credential","value":{}}]`:
			t.Errorf("row %d: CredentialId %q and RequesterDetails %q", i+1, credential, requester)
		}
	}
}
