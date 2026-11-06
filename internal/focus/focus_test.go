package focus

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
)

func TestLifetimeIsTheTagValueOfACacheWriteClassOnly(t *testing.T) {
	for class, want := range map[TokenClass]string{
		CacheWrite5m: "5m", CacheWrite1h: "1h", CacheWrite30m: "30m", CacheWrite12h: "12h",
		InputUncached: "", CacheRead: "", Output: "", ReasoningOutput: "", CachedInputReported: "",
	} {
		if got := class.Lifetime(); got != want {
			t.Errorf("%s: %q, want %q", class, got, want)
		}
	}
}

func sampleRow() Row {
	price := decimal.MustParse("0.125")
	quantity := decimal.MustParse("0.003495")
	cost := price.Mul(quantity)
	version := "0.1.0"
	return Row{
		ChargeCategory:      "Usage",
		ConsumedQuantity:    decimal.FromInt(3495),
		ContractedCost:      cost,
		ContractedUnitPrice: price,
		EffectiveCost:       cost,
		ListCost:            cost,
		ListUnitPrice:       price,
		PricingQuantity:     quantity,
		SkuMeter:            CacheWrite5m,
		Tags:                map[string]string{TagAgent: "claude-code"},
		CatalogVersion:      &version,
		RowID:               strings.Repeat("a", 64),
	}
}

func TestWriterEmitsEveryAmountAsAString(t *testing.T) {
	var out bytes.Buffer
	if err := NewWriter(&out).Write(sampleRow()); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"BilledCost", "ConsumedQuantity", "ContractedCost", "ContractedUnitPrice", "EffectiveCost", "ListCost", "ListUnitPrice", "PricingQuantity"} {
		raw := string(fields[column])
		if !strings.HasPrefix(raw, `"`) {
			t.Errorf("%s is %s, not a string", column, raw)
		}
	}
	if string(fields["ListCost"]) != `"0.000436875"` {
		t.Errorf("ListCost = %s", fields["ListCost"])
	}
}

func TestWriterEmitsAbsentColumnsAsNull(t *testing.T) {
	var out bytes.Buffer
	if err := NewWriter(&out).Write(sampleRow()); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"ChargeClass", "SubAccountId", "x_CostroidOvershootBound"} {
		if string(fields[column]) != "null" {
			t.Errorf("%s = %s", column, fields[column])
		}
	}
}

func TestWriterRefusesAFigurePastTheContractScale(t *testing.T) {
	row := sampleRow()
	row.ListUnitPrice = decimal.MustParse("0.00000012345")
	row.PricingQuantity = decimal.MustParse("0.1234567")
	row.ListCost = row.ListUnitPrice.Mul(row.PricingQuantity)
	if err := NewWriter(&bytes.Buffer{}).Write(row); err == nil {
		t.Error("a row with 18 fractional digits was written")
	}
}

func TestWriterRefusesACostThatIsNotThePriceTimesTheQuantity(t *testing.T) {
	row := sampleRow()
	row.ListCost = decimal.MustParse("0.0005")
	if err := NewWriter(&bytes.Buffer{}).Write(row); err == nil {
		t.Error("a wrong ListCost was written")
	}
}

func TestWriterRefusesABilledCost(t *testing.T) {
	row := sampleRow()
	row.BilledCost = decimal.MustParse("1")
	if err := NewWriter(&bytes.Buffer{}).Write(row); err == nil {
		t.Error("a row with a BilledCost was written")
	}
}

func floatUses(path string, source any) ([]string, error) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, source, 0)
	if err != nil {
		return nil, err
	}
	var found []string
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.Ident:
			if n.Name == "float64" || n.Name == "float32" {
				found = append(found, fileSet.Position(n.Pos()).String())
			}
		case *ast.SelectorExpr:
			if slices.Contains([]string{"ParseFloat", "Float64", "FormatFloat", "NewFloat", "SetFloat64"}, n.Sel.Name) {
				found = append(found, fileSet.Position(n.Pos()).String())
			}
		}
		return true
	})
	return found, nil
}

func TestNoGoSourceInTheModuleTouchesAFloat(t *testing.T) {
	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatal(err)
	}
	scanned := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		if strings.HasSuffix(path, "focus_test.go") {
			return nil
		}
		scanned++
		found, err := floatUses(path, nil)
		if err != nil {
			return err
		}
		for _, position := range found {
			t.Errorf("float at %s", position)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 10 {
		t.Fatalf("the scan read only %d files", scanned)
	}
}

func TestFloatScanFindsAFloat(t *testing.T) {
	sources := []string{
		"package p\nvar cost float64\n",
		"package p\nimport \"strconv\"\nvar cost, _ = strconv.ParseFloat(\"1\", 32)\n",
	}
	for _, source := range sources {
		found, err := floatUses("sample.go", source)
		if err != nil {
			t.Fatal(err)
		}
		if len(found) == 0 {
			t.Errorf("no float found in %q", source)
		}
	}
}

func TestChargePeriodIsWholeSecondsAndEndsAfterItStarts(t *testing.T) {
	base := time.Date(2026, 11, 1, 11, 15, 12, 0, time.UTC)
	cases := []struct {
		start, end time.Time
		seconds    time.Duration
	}{
		{base.Add(814 * time.Millisecond), base.Add(2363 * time.Millisecond), 3 * time.Second},
		{base, base, time.Second},
		{base, base.Add(2 * time.Second), 2 * time.Second},
	}
	for _, tc := range cases {
		first, last := ChargePeriod(tc.start, tc.end)
		if !first.Equal(base) || last.Sub(first) != tc.seconds {
			t.Errorf("%s to %s", first, last)
		}
	}
}

func TestBillingPeriodCrossesTheYear(t *testing.T) {
	first, last := BillingPeriod(time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC))
	if first.Format(InstantLayout) != "2026-12-01T00:00:00Z" || last.Format(InstantLayout) != "2027-01-01T00:00:00Z" {
		t.Errorf("%s to %s", first, last)
	}
}

func TestRowIDIsStableAndSeparatesClasses(t *testing.T) {
	// sha256 of the text req:output, computed outside this package.
	const want = "8ffecdf34ee2f4b71af971bb3720fa114774e9c270450353ad019926c5f1326c"
	output := RowIdentity("req", Output)
	if output == RowIdentity("req", CacheRead) || output == RowIdentity("other", Output) {
		t.Error("row ids do not follow the request and class")
	}
	if output != want {
		t.Errorf("row id %s", output)
	}
}

func sampleCharge() Charge {
	return Charge{
		RowID: strings.Repeat("a", 64), Provider: "anthropic", ServiceProviderName: "Anthropic", Model: "claude-opus-5-5",
		Class: Output, Tokens: 2_000_000, CatalogVersion: "0.1.0", Basis: Measured,
		Start: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 1, 10, 0, 1, 0, time.UTC),
		Price: &Price{SkuPriceID: "sku", PricingUnit: "1M tokens", ListUnitPrice: decimal.MustParse("2.5")},
		Tags:  map[string]string{TagAgent: "claude-code"}, Notes: []string{"first", "second"},
	}
}

func TestNewRowPricesAMeasuredChargeIntoBothCostColumns(t *testing.T) {
	row := NewRow(sampleCharge())
	if err := row.Check(); err != nil {
		t.Fatal(err)
	}
	if row.ListCost.String() != "5" || row.EffectiveCost.String() != "5" || row.ContractedCost.String() != "5" {
		t.Errorf("costs %s %s %s", row.ListCost, row.EffectiveCost, row.ContractedCost)
	}
	if row.SkuID != "anthropic/claude-opus-5-5/output" || row.BillingAccountID != "unknown" {
		t.Errorf("%s %s", row.SkuID, row.BillingAccountID)
	}
	if row.ChargeDescription != "claude-opus-5-5 output tokens; first; second" {
		t.Errorf("%s", row.ChargeDescription)
	}
}

func TestNewRowLeavesAListEquivalentChargeOutOfEffectiveCost(t *testing.T) {
	charge := sampleCharge()
	charge.Basis = ListEquivalent
	row := NewRow(charge)
	if row.EffectiveCost.Sign() != 0 || row.ListCost.Sign() <= 0 {
		t.Errorf("%s %s", row.EffectiveCost, row.ListCost)
	}
}

func TestNewRowGivesAnInformationalChargeNoCost(t *testing.T) {
	charge := sampleCharge()
	charge.Price, charge.Class = nil, ReasoningOutput
	row := NewRow(charge)
	if err := row.Check(); err != nil {
		t.Fatal(err)
	}
	if row.ConsumedUnit != "Informational Tokens" || row.SkuPriceID != "anthropic:informational:reasoning_output" {
		t.Errorf("%s %s", row.ConsumedUnit, row.SkuPriceID)
	}
	if row.ListCost.Sign() != 0 || row.PricingQuantity.Cmp(row.ConsumedQuantity) != 0 {
		t.Errorf("%s %s", row.ListCost, row.PricingQuantity)
	}
}

func TestADistinctRegionFillsBothColumnsAndGlobalLeavesThemNull(t *testing.T) {
	charge := Charge{Region: "us", ServiceTier: "batch", Credential: "apikey_01", Start: time.Unix(0, 0), End: time.Unix(1, 0)}
	row := NewRow(charge)
	if row.RegionID == nil || *row.RegionID != "us" || row.RegionName == nil || *row.RegionName != "us" {
		t.Fatalf("%v %v", row.RegionID, row.RegionName)
	}
	if row.ServiceTier == nil || *row.ServiceTier != "batch" || row.SubAccountID == nil || *row.SubAccountID != "apikey_01" {
		t.Fatalf("%v %v", row.ServiceTier, row.SubAccountID)
	}
	charge.Region = RegionGlobal
	if row := NewRow(charge); row.RegionID != nil || row.RegionName != nil {
		t.Fatalf("global reads %v %v", row.RegionID, row.RegionName)
	}
}

func TestAbsentTierAndCredentialAreNull(t *testing.T) {
	row := NewRow(Charge{Start: time.Unix(0, 0), End: time.Unix(1, 0)})
	if row.ServiceTier != nil || row.SubAccountID != nil {
		t.Fatalf("%v %v", row.ServiceTier, row.SubAccountID)
	}
}
