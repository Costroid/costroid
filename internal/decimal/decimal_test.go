package decimal

import (
	"encoding/json"
	"testing"
)

func TestParseRefusesWhatARowMustNotCarry(t *testing.T) {
	for _, text := range []string{"", "-1", "+1", "1e3", "1.", ".5", "01", "1,5", "NaN", " 1"} {
		if _, err := Parse(text); err == nil {
			t.Errorf("Parse(%q) was accepted", text)
		}
	}
}

func TestStringKeepsNoPadding(t *testing.T) {
	cases := map[string]string{"0": "0", "0.0": "0", "3.750": "3.75", "20.00": "20", "0.000002": "0.000002", "12.5": "12.5"}
	for in, want := range cases {
		if got := MustParse(in).String(); got != want {
			t.Errorf("%s printed as %s, want %s", in, got, want)
		}
	}
}

func TestArithmeticIsExactWhereBinaryFloatIsNot(t *testing.T) {
	sum := MustParse("0.1").Add(MustParse("0.2"))
	if sum.String() != "0.3" {
		t.Errorf("0.1 + 0.2 = %s", sum)
	}
	product := MustParse("0.125").Mul(FromInt(3495).DivPow10(6))
	if product.String() != "0.000436875" {
		t.Errorf("product = %s", product)
	}
	if got := MustParse("1").Sub(MustParse("1.5")).String(); got != "-0.5" {
		t.Errorf("1 - 1.5 = %s", got)
	}
}

func TestCmpComparesAcrossScales(t *testing.T) {
	if MustParse("1.50").Cmp(MustParse("1.5")) != 0 {
		t.Error("1.50 and 1.5 differ")
	}
	if MustParse("0.09").Cmp(MustParse("0.1")) != -1 {
		t.Error("0.09 is not below 0.1")
	}
}

func TestScaleCountsDigitsTheValueNeeds(t *testing.T) {
	if got := MustParse("1.5000").Scale(); got != 1 {
		t.Errorf("scale = %d", got)
	}
}

func TestZeroValueIsZero(t *testing.T) {
	var d Decimal
	if d.String() != "0" || d.Sign() != 0 {
		t.Errorf("zero value printed %s", d)
	}
}

func TestMarshalJSONWritesAString(t *testing.T) {
	out, err := json.Marshal(MustParse("0.00552"))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `"0.00552"` {
		t.Errorf("marshalled %s", out)
	}
}
