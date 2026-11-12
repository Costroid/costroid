package export

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden files under testdata")

// testOptions name the time and the bucket of an export, so every golden is
// the same bytes on every run.
var testOptions = Options{Now: time.Date(2026, 11, 12, 9, 30, 0, 0, time.UTC), RootDir: "example-focus-drops"}

// exampleRows and exampleCSV are focus-row/v0.3's examples, which the focus
// package's tests hold to the schemas' copy.
var (
	exampleRows = filepath.Join("..", "focus", "testdata", "request-rows.jsonl")
	exampleCSV  = filepath.Join("..", "focus", "testdata", "request-rows.csv")
)

// focus14Columns are the Column IDs of FOCUS 1.4's cost and usage dataset, one
// for each file in specification/datasets/cost_and_usage/columns at the tag
// v1.4 (https://github.com/FinOps-Open-Cost-and-Usage-Spec/FOCUS_Spec/tree/v1.4/specification/datasets/cost_and_usage/columns,
// commit f1eeb30a78f7c141ef1237d589355296a2761c1c, retrieved 2026-11-08).
var focus14Columns = []string{
	"AllocatedMethodDetails", "AllocatedMethodId", "AllocatedResourceId", "AllocatedResourceName", "AllocatedTags",
	"AvailabilityZone", "BilledCost", "BillingAccountId", "BillingAccountName", "BillingAccountType",
	"BillingCurrency", "BillingPeriodEnd", "BillingPeriodStart", "CapacityReservationId", "CapacityReservationStatus",
	"ChargeCategory", "ChargeClass", "ChargeDescription", "ChargeFrequency", "ChargePeriodEnd",
	"ChargePeriodStart", "CommitmentDiscountCategory", "CommitmentDiscountId", "CommitmentDiscountName", "CommitmentDiscountQuantity",
	"CommitmentDiscountStatus", "CommitmentDiscountType", "CommitmentDiscountUnit", "CommitmentProgramEligibilityDetails", "ConsumedQuantity",
	"ConsumedUnit", "ContractApplied", "ContractedCost", "ContractedUnitPrice", "EffectiveCost",
	"HostProviderName", "InvoiceDetailId", "InvoiceId", "InvoiceIssuerName", "ListCost",
	"ListUnitPrice", "PricingCategory", "PricingCurrency", "PricingCurrencyContractedUnitPrice", "PricingCurrencyEffectiveCost",
	"PricingCurrencyListUnitPrice", "PricingQuantity", "PricingUnit", "RegionId", "RegionName",
	"ResourceId", "ResourceName", "ResourceType", "ServiceCategory", "ServiceName",
	"ServiceProviderName", "ServiceSubcategory", "SkuId", "SkuMeter", "SkuPriceDetails",
	"SkuPriceId", "SubAccountId", "SubAccountName", "SubAccountType", "Tags",
}

// customColumn is FOCUS 1.4's Custom Column Handling: the x_ prefix it
// requires, then the Pascal case, alphanumeric name it recommends
// (specification/attributes/custom_column_handling.md at v1.4, retrieved
// 2026-11-08).
var customColumn = regexp.MustCompile(`^x_[A-Z][A-Za-z0-9]*$`)

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func write(t *testing.T, format Format, rows []byte) Export {
	t.Helper()
	written, err := Write(format, bytes.NewReader(rows), testOptions)
	if err != nil {
		t.Fatalf("%s: %v", format, err)
	}
	return written
}

func records(t *testing.T, file File) [][]string {
	t.Helper()
	records, err := csv.NewReader(bytes.NewReader(file.Body)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records)-1 != file.Rows {
		t.Fatalf("the file holds %d records and states %d rows", len(records)-1, file.Rows)
	}
	return records
}

// exampleLines are the example rows, each as its fields, for a test to edit.
func exampleLines(t *testing.T) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for line := range bytes.SplitSeq(bytes.TrimSpace(readFile(t, exampleRows)), []byte("\n")) {
		var fields map[string]any
		if err := json.Unmarshal(line, &fields); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, fields)
	}
	return lines
}

func encodeLines(t *testing.T, lines []map[string]any) []byte {
	t.Helper()
	var out bytes.Buffer
	for _, fields := range lines {
		encoded, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(append(encoded, '\n'))
	}
	return out.Bytes()
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if want := readFile(t, path); !bytes.Equal(got, want) {
		t.Fatalf("%s differs from what was written:\n%s", path, got)
	}
}

// TestFocusCSVHeaderIsFocus14 holds focus-csv's header to FOCUS 1.4: each
// column is a FOCUS 1.4 Column ID, spelled as the specification spells it, or
// a custom column under its rule. The file is the row contract's CSV, byte for
// byte.
func TestFocusCSVHeaderIsFocus14(t *testing.T) {
	written := write(t, FocusCSV, readFile(t, exampleRows))
	if len(written.Files) != 1 {
		t.Fatalf("%d files, want 1", len(written.Files))
	}
	header := records(t, written.Files[0])[0]
	if len(header) != 40 {
		t.Errorf("%d columns, want the row contract's 40", len(header))
	}
	for _, column := range header {
		if !slices.Contains(focus14Columns, column) && !customColumn.MatchString(column) {
			t.Errorf("%s is neither a FOCUS 1.4 column nor a custom column", column)
		}
	}
	if !bytes.Equal(written.Files[0].Body, readFile(t, exampleCSV)) {
		t.Error("focus-csv is not the row contract's CSV")
	}
}

// smallRows are n copies of the first example row with no tags, each under
// its own row id, so that 10,000 of them stay under 2,000,000 bytes.
func smallRows(t *testing.T, n int) []byte {
	t.Helper()
	first := exampleLines(t)[0]
	first["Tags"] = map[string]string{}
	lines := make([]map[string]any, n)
	for i := range lines {
		row := make(map[string]any, len(first))
		for key, value := range first {
			row[key] = value
		}
		row["x_CostroidRowId"] = fmt.Sprintf("%064d", i)
		lines[i] = row
	}
	return encodeLines(t, lines)
}

// TestVantageSplitsAt10000Rows holds a Vantage file to 10,000 rows: 10,000
// rows fit one file, and the 10,001st starts a second with its own header.
func TestVantageSplitsAt10000Rows(t *testing.T) {
	whole := write(t, Vantage, smallRows(t, 10000))
	if len(whole.Files) != 1 || whole.Files[0].Rows != 10000 {
		t.Fatalf("10,000 rows went to %d files", len(whole.Files))
	}
	if size := len(whole.Files[0].Body); size >= vantageMaxBytes {
		t.Fatalf("10,000 rows take %d bytes, so the byte limit split them, not the row limit", size)
	}
	split := write(t, Vantage, smallRows(t, 10001))
	if len(split.Files) != 2 || split.Files[0].Rows != 10000 || split.Files[1].Rows != 1 {
		t.Fatalf("10,001 rows went to %d files", len(split.Files))
	}
	for i, file := range split.Files {
		if header := records(t, file)[0]; !slices.Equal(header, vantageColumns) {
			t.Errorf("file %d starts %v", i+1, header)
		}
	}
}

// TestVantageSplitsAt2000000Bytes holds a Vantage file to 2,000,000 bytes:
// rows that pass it before 10,000 rows go to a second file, each file under
// the limit, and no row is lost.
func TestVantageSplitsAt2000000Bytes(t *testing.T) {
	lines := exampleLines(t)
	padding := strings.Repeat("a", 1500)
	var rows []map[string]any
	for i := range 2000 {
		row := lines[i%len(lines)]
		copied := make(map[string]any, len(row))
		for key, value := range row {
			copied[key] = value
		}
		copied["ServiceName"] = padding
		rows = append(rows, copied)
	}
	written := write(t, Vantage, encodeLines(t, rows))
	if len(written.Files) < 2 {
		t.Fatalf("2,000 rows of over 1,500 bytes went to %d file", len(written.Files))
	}
	total := 0
	for i, file := range written.Files {
		if len(file.Body) > vantageMaxBytes {
			t.Errorf("file %d is %d bytes", i+1, len(file.Body))
		}
		total += file.Rows
	}
	if total != 2000 {
		t.Errorf("the files hold %d rows, want 2000", total)
	}
	next := bytes.SplitAfterN(written.Files[1].Body, []byte("\n"), 3)[1]
	if size := len(written.Files[0].Body) + len(next); size <= vantageMaxBytes {
		t.Errorf("the first file stops at %d bytes, and the next row would fit it at %d", len(written.Files[0].Body), size)
	}
}

// TestDatadogRefusesNonUSD refuses a file with a row in EUR, and names every
// such row by its place and its id. Nothing is converted and nothing is
// written.
func TestDatadogRefusesNonUSD(t *testing.T) {
	lines := exampleLines(t)
	lines[1]["BillingCurrency"], lines[4]["BillingCurrency"] = "EUR", "GBP"
	written, err := Write(Datadog, bytes.NewReader(encodeLines(t, lines)), testOptions)
	var refused *RowsError
	if !errors.As(err, &refused) || len(written.Files) != 0 {
		t.Fatalf("Write returned %d files and %v", len(written.Files), err)
	}
	for _, want := range []string{
		"row 2 (x_CostroidRowId " + lines[1]["x_CostroidRowId"].(string) + ") in EUR",
		"row 5 (x_CostroidRowId " + lines[4]["x_CostroidRowId"].(string) + ") in GBP",
		"Datadog takes USD only",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not name %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "row 1 ") || strings.Contains(err.Error(), "row 3 ") {
		t.Errorf("%q names a USD row", err)
	}
}

// A refusal names at most 10 rows and counts the rest.
func TestRefusalNamesTenRowsAndCountsTheRest(t *testing.T) {
	refused := &RowsError{Format: Datadog, Reason: "the reason"}
	for i := range 12 {
		refused.Rows = append(refused.Rows, fmt.Sprintf("row %d", i+1))
	}
	want := "datadog refuses row 1; row 2; row 3; row 4; row 5; row 6; row 7; row 8; row 9; row 10 and 2 more rows: the reason"
	if refused.Error() != want {
		t.Fatalf("%q", refused.Error())
	}
}

// TestDatadogColumnOrder holds the six required columns to the page's order,
// with every tag after them, and the whole file to its golden.
func TestDatadogColumnOrder(t *testing.T) {
	written := write(t, Datadog, readFile(t, exampleRows))
	all := records(t, written.Files[0])
	want := []string{"ProviderName", "ChargeDescription", "ChargePeriodStart", "ChargePeriodEnd", "BilledCost", "BillingCurrency"}
	if !slices.Equal(all[0][:6], want) {
		t.Fatalf("the header starts %v, want %v", all[0][:6], want)
	}
	for _, column := range all[0][6:] {
		if !strings.HasPrefix(column, "costroid/") {
			t.Errorf("%s follows the required columns and is not a tag", column)
		}
	}
	if !slices.Contains(all[0], "costroid/cost_basis") {
		t.Error("the file has no costroid/cost_basis column, so its figures carry no basis")
	}
	if len(written.OmittedTags) != 1 || written.OmittedTags[0] != "costroid/assumed_ttl" {
		t.Errorf("omitted %v, want costroid/assumed_ttl, which one row carries", written.OmittedTags)
	}
	golden(t, "datadog.csv", written.Files[0].Body)
}

func TestVantageGolden(t *testing.T) {
	written := write(t, Vantage, readFile(t, exampleRows))
	golden(t, "vantage.csv", written.Files[0].Body)
}

// Datadog states days. A row's ChargePeriodEnd is exclusive and Datadog's is
// inclusive, so a charge that ends at midnight UTC ends the day before.
func TestDatadogDaysEndInclusive(t *testing.T) {
	for name, check := range map[string]struct {
		start, end, wantStart, wantEnd string
	}{
		"within a day":     {"2026-10-14T09:31:02Z", "2026-10-14T09:31:08Z", "2026-10-14", "2026-10-14"},
		"ends at midnight": {"2026-10-14T23:59:58Z", "2026-10-15T00:00:00Z", "2026-10-14", "2026-10-14"},
		"over midnight":    {"2026-10-14T23:59:58Z", "2026-10-15T00:00:01Z", "2026-10-14", "2026-10-15"},
	} {
		t.Run(name, func(t *testing.T) {
			lines := exampleLines(t)[:1]
			lines[0]["ChargePeriodStart"], lines[0]["ChargePeriodEnd"] = check.start, check.end
			record := records(t, write(t, Datadog, encodeLines(t, lines)).Files[0])[1]
			if record[2] != check.wantStart || record[3] != check.wantEnd {
				t.Fatalf("%s to %s, want %s to %s", record[2], record[3], check.wantStart, check.wantEnd)
			}
		})
	}
}

// Datadog takes no blank value, so a null in a required column refuses the
// file and names the row.
func TestDatadogRefusesABlankValue(t *testing.T) {
	lines := exampleLines(t)
	lines[2]["ChargeDescription"] = nil
	_, err := Write(Datadog, bytes.NewReader(encodeLines(t, lines)), testOptions)
	var refused *RowsError
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), "row 3 ") || !strings.Contains(err.Error(), "ChargeDescription is blank") {
		t.Fatalf("%v", err)
	}
}

// TestNoAmountConverted writes amounts no float holds, with trailing zeros,
// in two currencies, and finds each in every format exactly as the row wrote
// it, one row for one row: nothing converted, rounded or added up.
func TestNoAmountConverted(t *testing.T) {
	amounts := []string{"0.000000000001", "123456789012345678901234567890.123456789012", "0.0100", "0", "7"}
	quantities := []string{"18446744073709551617", "1", "1000000", "100", "3"}
	build := func(currencies []string) ([]byte, []map[string]any) {
		lines := exampleLines(t)[:len(amounts)]
		for i, fields := range lines {
			fields["EffectiveCost"], fields["ConsumedQuantity"], fields["BillingCurrency"] = amounts[i], quantities[i], currencies[i]
		}
		return encodeLines(t, lines), lines
	}
	mixed, _ := build([]string{"USD", "EUR", "USD", "JPY", "EUR"})
	usd, _ := build([]string{"USD", "USD", "USD", "USD", "USD"})
	for _, check := range []struct {
		format  Format
		rows    []byte
		columns map[string]string
	}{
		{FocusCSV, mixed, map[string]string{"EffectiveCost": "EffectiveCost", "ConsumedQuantity": "ConsumedQuantity", "BillingCurrency": "BillingCurrency"}},
		{Vantage, mixed, map[string]string{"BilledCost": "EffectiveCost", "ConsumedQuantity": "ConsumedQuantity", "BillingCurrency": "BillingCurrency"}},
		{Datadog, usd, map[string]string{"BilledCost": "EffectiveCost", "BillingCurrency": "BillingCurrency"}},
	} {
		t.Run(string(check.format), func(t *testing.T) {
			var source []map[string]any
			for line := range bytes.SplitSeq(bytes.TrimSpace(check.rows), []byte("\n")) {
				var fields map[string]any
				if err := json.Unmarshal(line, &fields); err != nil {
					t.Fatal(err)
				}
				source = append(source, fields)
			}
			written := write(t, check.format, check.rows)
			all := records(t, written.Files[0])
			if len(all)-1 != len(source) {
				t.Fatalf("%d rows written for %d", len(all)-1, len(source))
			}
			for i, record := range all[1:] {
				for column, from := range check.columns {
					if got, want := record[slices.Index(all[0], column)], source[i][from].(string); got != want {
						t.Errorf("row %d: %s reads %q, and the row's %s is %q", i+1, column, got, from, want)
					}
				}
			}
		})
	}
}
