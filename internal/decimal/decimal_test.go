package decimal

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
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

func TestParseExponent(t *testing.T) {
	cases := map[string]string{
		"0.06":                "0.06",
		"8.6e-1":              "0.86",
		"8.6E-1":              "0.86",
		"1.5e2":               "150",
		"1.5e+2":              "150",
		"1.2e-05":             "0.000012",
		"-0.25":               "-0.25",
		"41280.000000":        "41280",
		"0.30000000000000004": "0.30000000000000004",
	}
	for in, want := range cases {
		got, err := ParseNumber(in)
		if err != nil {
			t.Errorf("ParseNumber(%q): %v", in, err)
		} else if got.String() != want {
			t.Errorf("ParseNumber(%q) = %s, want %s", in, got, want)
		}
	}
	for _, in := range []string{"", "abc", "1e", "1e999", "1e-65", "--1", "+1", "NaN", "Infinity", ".5", "1.5e2.5"} {
		if _, err := ParseNumber(in); err == nil {
			t.Errorf("ParseNumber(%q) was accepted", in)
		}
	}
}

func TestParseRefusesALongLiteral(t *testing.T) {
	longest := "1." + strings.Repeat("0", MaxLength-3) + "1"
	if got, err := Parse(longest); err != nil || got.Scale() != MaxLength-2 {
		t.Errorf("Parse refused a literal of %d characters: %v", len(longest), err)
	}
	for _, text := range []string{longest + "0", "1" + strings.Repeat("0", MaxLength), "1." + strings.Repeat("0", 1<<20)} {
		if _, err := Parse(text); err == nil {
			t.Errorf("Parse accepted a literal of %d characters", len(text))
		}
	}
	if _, err := ParseNumber("1e64"); err == nil {
		t.Error("ParseNumber accepted a value whose plain form is 65 characters")
	}
	if got, err := ParseNumber("1e63"); err != nil || len(got.String()) != MaxLength {
		t.Errorf("ParseNumber(1e63) = %s, %v", got, err)
	}
}

// TestParseIsLinearInTrailingZeros reads a megabyte of trailing zeros with the
// unbounded reader Parse wraps. One big-integer division per zero would take
// minutes on it, so the deadline only catches work that grows faster than the
// text.
func TestParseIsLinearInTrailingZeros(t *testing.T) {
	text := "1." + strings.Repeat("0", 1<<20)
	done := make(chan error, 1)
	go func() {
		got, err := parse(text)
		if err == nil && (got.String() != "1" || got.Scale() != 0) {
			err = fmt.Errorf("read as %s at scale %d", got, got.Scale())
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a megabyte of trailing zeros took over 10 seconds to read")
	}
	if got := MustParse("2." + strings.Repeat("0", MaxLength-2)); got.String() != "2" || got.Scale() != 0 {
		t.Errorf("the longest literal of trailing zeros read as %s at scale %d", got, got.Scale())
	}
}
