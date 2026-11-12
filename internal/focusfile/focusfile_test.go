package focusfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/focus"
)

// ciTags are the join keys a CI system states for a pull request's run.
var ciTags = map[string]string{
	focus.TagRepo:          "github.com/example/widgets",
	focus.TagPR:            "7",
	focus.TagCommitSHA:     "0123456789abcdef0123456789abcdef01234567",
	focus.TagWorkflowRunID: "1234",
}

func readFixture(t *testing.T, name string, options Options) File {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	file, err := Read(bytes.NewReader(data), options)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func TestARowKeepsItsFigureCurrencyPeriodAndResource(t *testing.T) {
	file := readFixture(t, "two-accounts.csv", Options{Tags: ciTags})
	if len(file.Rows) != 2 || file.CostColumn != "ListCost" {
		t.Fatalf("%d rows from %s", len(file.Rows), file.CostColumn)
	}
	row := file.Rows[0]
	for column, got := range map[string]string{
		"ListCost":           row.ListCost.String(),
		"ListUnitPrice":      row.ListUnitPrice.String(),
		"ContractedCost":     row.ContractedCost.String(),
		"EffectiveCost":      row.EffectiveCost.String(),
		"BilledCost":         row.BilledCost.String(),
		"BillingCurrency":    row.BillingCurrency,
		"ChargePeriodStart":  row.ChargePeriodStart,
		"ChargePeriodEnd":    row.ChargePeriodEnd,
		"BillingPeriodStart": row.BillingPeriodStart,
		"ServiceName":        row.ServiceName,
		"ServiceCategory":    row.ServiceCategory,
		"SkuId":              row.SkuID,
		"BillingAccountId":   row.BillingAccountID,
		"SubAccountId":       *row.SubAccountID,
		"RegionId":           *row.RegionID,
		"RegionName":         *row.RegionName,
		"ChargeDescription":  row.ChargeDescription,
	} {
		want := map[string]string{
			"ListCost": "25.488", "ListUnitPrice": "25.488", "ContractedCost": "25.488", "EffectiveCost": "0", "BilledCost": "0",
			"BillingCurrency": "EUR", "ChargePeriodStart": "2026-12-01T00:00:00Z", "ChargePeriodEnd": "2027-01-01T00:00:00Z",
			"BillingPeriodStart": "2026-12-01T00:00:00Z", "ServiceName": "Amazon EC2", "ServiceCategory": "Compute",
			"SkuId": "example-sku-0001", "BillingAccountId": "example-billing", "SubAccountId": "example-account-a",
			"RegionId": "eu-central-1", "RegionName": "Europe (Frankfurt)",
			"ChargeDescription": "resource name aws_instance.web; resource id example-instance-0001; Compute instance at 730 hours a month",
		}[column]
		if got != want {
			t.Errorf("%s reads %q, want %q", column, got, want)
		}
	}
	if err := row.Check(); err != nil {
		t.Error(err)
	}
	if got := Resource(row.ChargeDescription); got != "aws_instance.web" {
		t.Errorf("the resource reads %q", got)
	}
}

func TestTheFilesTagsAreDroppedAndTheJoinKeysAreTheCISystems(t *testing.T) {
	file := readFixture(t, "two-accounts.csv", Options{Tags: ciTags})
	if file.TaggedRows != 1 {
		t.Errorf("%d rows counted as tagged, want 1", file.TaggedRows)
	}
	want := map[string]string{
		focus.TagCostSource: DefaultSource, focus.TagCostBasis: "estimate", focus.TagAttributionGrade: "unattributed",
		focus.TagEnforcementGrade: "none", focus.TagRunIdentity: "unverified",
	}
	for key, value := range ciTags {
		want[key] = value
	}
	for _, row := range file.Rows {
		if len(row.Tags) != len(want) {
			t.Errorf("the row holds the tags %v", row.Tags)
		}
		for key, value := range want {
			if row.Tags[key] != value {
				t.Errorf("%s reads %q, want %q", key, row.Tags[key], value)
			}
		}
		if _, found := row.Tags[focus.TagAgent]; found {
			t.Error("a row of the file names an agent")
		}
	}
}

func TestASourceNameIsHeldToItsRule(t *testing.T) {
	file := readFixture(t, "two-accounts.csv", Options{Source: "hcp-terraform"})
	if got := file.Rows[0].Tags[focus.TagCostSource]; got != "hcp-terraform" {
		t.Errorf("cost_source reads %q", got)
	}
	for _, name := range []string{"Terraform", "-x", "x-", "a--b", "a_b", "otlp", strings.Repeat("a", 65)} {
		if err := CheckSource(name); err == nil {
			t.Errorf("the source %q was taken", name)
		}
	}
}

// csvOf builds a file from a header and rows, each a comma-separated line.
func csvOf(header string, rows ...string) string {
	return strings.Join(append([]string{header}, rows...), "\n") + "\n"
}

const (
	minimalHeader = "BillingCurrency,ChargePeriodStart,ChargePeriodEnd,ListCost"
	december      = "2026-12-01T00:00:00Z,2027-01-01T00:00:00Z"
)

func TestTheFigureComesFromOneStatedColumn(t *testing.T) {
	cases := []struct {
		name, header, row, column string
		refusal                   error
	}{
		{"ListCost before every other", "BillingCurrency,ChargePeriodStart,ChargePeriodEnd,EffectiveCost,ListCost,BilledCost", "USD," + december + ",9,10,8", "ListCost", nil},
		{"EffectiveCost alone", "BillingCurrency,ChargePeriodStart,ChargePeriodEnd,EffectiveCost", "USD," + december + ",9", "EffectiveCost", nil},
		{"BilledCost alone", "BillingCurrency,ChargePeriodStart,ChargePeriodEnd,BilledCost", "USD," + december + ",8", "BilledCost", nil},
		{"two without ListCost", "BillingCurrency,ChargePeriodStart,ChargePeriodEnd,EffectiveCost,BilledCost", "USD," + december + ",9,8", "", ErrAmbiguousCost},
		{"none", "BillingCurrency,ChargePeriodStart,ChargePeriodEnd,Cost", "USD," + december + ",9", "", ErrNoCostColumn},
	}
	for _, tc := range cases {
		file, err := Read(strings.NewReader(csvOf(tc.header, tc.row)), Options{})
		switch {
		case tc.refusal != nil && !errors.Is(err, tc.refusal):
			t.Errorf("%s: %v, want %v", tc.name, err, tc.refusal)
		case tc.refusal == nil && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.refusal == nil && file.CostColumn != tc.column:
			t.Errorf("%s: read from %s, want %s", tc.name, file.CostColumn, tc.column)
		}
	}
}

func TestEachRefusalIsTypedAndNamesItsLine(t *testing.T) {
	cases := []struct {
		name, file string
		refusal    error
		line       int
	}{
		{"an empty file", "", ErrNoHeader, 0},
		{"no currency column", csvOf("ChargePeriodStart,ChargePeriodEnd,ListCost", december+",1"), ErrMissingColumn, 1},
		{"no period column", csvOf("BillingCurrency,ListCost", "USD,1"), ErrMissingColumn, 1},
		{"a column twice", csvOf(minimalHeader+",ListCost", "USD,"+december+",1,2"), ErrColumnTwice, 1},
		{"no currency", csvOf(minimalHeader, "USD,"+december+",1", ","+december+",1"), ErrNoCurrency, 3},
		{"a lower-case currency", csvOf(minimalHeader, "usd,"+december+",1"), ErrNoCurrency, 2},
		{"no amount", csvOf(minimalHeader, "USD,"+december+","), ErrNoAmount, 2},
		{"a negative amount", csvOf(minimalHeader, "USD,"+december+",-4.5"), ErrNegative, 2},
		{"an exponent", csvOf(minimalHeader, "USD,"+december+",1e3"), ErrNotDecimal, 2},
		{"a grouped amount", csvOf(minimalHeader, `USD,`+december+`,"1,000"`), ErrNotDecimal, 2},
		{"a leading space", csvOf(minimalHeader, "USD,"+december+", 1"), ErrNotDecimal, 2},
		{"a leading zero", csvOf(minimalHeader, "USD,"+december+",01"), ErrNotDecimal, 2},
		{"27 integer digits", csvOf(minimalHeader, "USD,"+december+","+strings.Repeat("9", 27)), ErrNotDecimal, 2},
		{"past the decimal package's bound", csvOf(minimalHeader, "USD,"+december+",1."+strings.Repeat("0", 64)), ErrNotDecimal, 2},
		{"13 fractional digits", csvOf(minimalHeader, "USD,"+december+",0.0000000000001"), ErrScale, 2},
		{"no start", csvOf(minimalHeader, "USD,,2027-01-01T00:00:00Z,1"), ErrNoPeriod, 2},
		{"a date without a time", csvOf(minimalHeader, "USD,2026-12-01,2027-01-01T00:00:00Z,1"), ErrInstant, 2},
		{"an offset", csvOf(minimalHeader, "USD,2026-12-01T00:00:00+01:00,2027-01-01T00:00:00Z,1"), ErrInstant, 2},
		{"an end before the start", csvOf(minimalHeader, "USD,2027-01-01T00:00:00Z,2026-12-01T00:00:00Z,1"), ErrPeriodOrder, 2},
		{"a purchase", csvOf(minimalHeader+",ChargeCategory", "USD,"+december+",1,Purchase"), ErrNotUsage, 2},
		{"a correction", csvOf(minimalHeader+",ChargeClass", "USD,"+december+",1,Correction"), ErrChargeClass, 2},
		{"a long service name", csvOf(minimalHeader+",ServiceName", "USD,"+december+",1,"+strings.Repeat("s", 513)), ErrTooLong, 2},
		{"a short row", csvOf(minimalHeader, "USD,"+december), ErrNotCSV, 2},
	}
	for _, tc := range cases {
		_, err := Read(strings.NewReader(tc.file), Options{})
		var refusal *Refusal
		if !errors.Is(err, tc.refusal) || !errors.As(err, &refusal) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.refusal)
			continue
		}
		if refusal.Line != tc.line {
			t.Errorf("%s: line %d, want %d: %v", tc.name, refusal.Line, tc.line, err)
		}
	}
	big := strings.NewReader(csvOf(minimalHeader, strings.Repeat("USD,"+december+",1\n", MaxBytes/40)))
	if _, err := Read(big, Options{}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("a file over %d bytes: %v", MaxBytes, err)
	}
}

func TestARefusalRepeatsAShortPartOfTheValue(t *testing.T) {
	_, err := Read(strings.NewReader(csvOf(minimalHeader+",ServiceName", "USD,"+december+",1,"+strings.Repeat("s", 600))), Options{})
	if err == nil || len(err.Error()) > 400 || !strings.Contains(err.Error(), `line 2, ServiceName, "sss`) {
		t.Errorf("the refusal reads %v", err)
	}
}

func TestAMinimalFileFillsWhatTheContractRequires(t *testing.T) {
	file, err := Read(strings.NewReader("\xef\xbb\xbf"+csvOf(minimalHeader, "JPY,"+december+",2400")), Options{BillingAccount: "example-billing"})
	if err != nil {
		t.Fatal(err)
	}
	row := file.Rows[0]
	if row.BillingAccountID != "example-billing" || row.ServiceName != "unknown" || row.ServiceCategory != "Other" ||
		row.ChargeDescription != "estimate from infrastructure" || row.RegionID != nil || row.SubAccountID != nil {
		t.Errorf("the row reads %+v", row)
	}
	if err := row.Check(); err != nil {
		t.Error(err)
	}
}

func TestOneRowIDPerRowRunAndFile(t *testing.T) {
	first := readFixture(t, "two-accounts.csv", Options{Tags: ciTags})
	if first.Rows[0].RowID == first.Rows[1].RowID {
		t.Error("two rows of one file share an id")
	}
	again := readFixture(t, "two-accounts.csv", Options{Tags: ciTags})
	if again.Rows[0].RowID != first.Rows[0].RowID {
		t.Error("the same file and run gave another id")
	}
	other := map[string]string{focus.TagWorkflowRunID: "1235"}
	if readFixture(t, "two-accounts.csv", Options{Tags: other}).Rows[0].RowID == first.Rows[0].RowID {
		t.Error("another run gave the same id")
	}
}

// FuzzFocusFile reads any bytes as a FOCUS cost file. It never panics, the same
// bytes read twice give the same rows, and every row it returns is an estimate
// the row contract holds.
func FuzzFocusFile(f *testing.F) {
	seed, err := os.ReadFile(filepath.Join("testdata", "two-accounts.csv"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Add([]byte(csvOf(minimalHeader, "USD,"+december+",1", "EUR,"+december+",2.5")))
	f.Add([]byte(csvOf(minimalHeader, "USD,"+december+",-1")))
	f.Add([]byte("ListCost\n\"1\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		first, err := Read(bytes.NewReader(data), Options{Tags: ciTags})
		second, errAgain := Read(bytes.NewReader(data), Options{Tags: ciTags})
		if (err == nil) != (errAgain == nil) {
			t.Fatalf("one read refused and the other did not: %v, %v", err, errAgain)
		}
		if err != nil {
			return
		}
		a, _ := json.Marshal(first)
		b, _ := json.Marshal(second)
		if !bytes.Equal(a, b) {
			t.Fatal("the same bytes gave other rows")
		}
		for _, row := range first.Rows {
			if err := row.Check(); err != nil {
				t.Fatal(err)
			}
			if row.Tags[focus.TagCostBasis] != Basis || row.EffectiveCost.Sign() != 0 || row.BilledCost.Sign() != 0 {
				t.Fatalf("a row is no estimate: %+v", row)
			}
		}
	})
}
