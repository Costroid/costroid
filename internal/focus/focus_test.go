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

	"github.com/Costroid/costroid/internal/decimal"
)

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
