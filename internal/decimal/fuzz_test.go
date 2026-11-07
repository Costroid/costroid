package decimal

import (
	"math/big"
	"regexp"
	"strings"
	"testing"
)

// canonical is the only form String writes: no sign on zero, no leading zero
// before a digit, no trailing zero after the point.
var canonical = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]*[1-9])?$`)

// figureSeeds are the amounts and counts rows, catalogs and provider reports
// carry, with the edges of each form.
var figureSeeds = []string{
	"0", "0.0", "1", "3.750", "0.0000002", "0.001619725", "92233720368.5483494",
	"9223372036854775807", "9223372036854775808", "1000000", "12.000000000000",
	"-0.25", "1.2e-05", "1E64", "1e65", "-0", "00", "1.", ".5", "+1", " 1", "",
}

// exact is text as a rational, read independently of this package.
func exact(t *testing.T, text string) *big.Rat {
	t.Helper()
	value, ok := new(big.Rat).SetString(text)
	if !ok {
		t.Fatalf("big.Rat cannot read %q, which this package accepted", text)
	}
	return value
}

// assertRoundTrip holds a parsed value to its printed form: the form is
// canonical, it reads back to the same value, and it equals the text read.
func assertRoundTrip(t *testing.T, text string, d Decimal) {
	t.Helper()
	printed := d.String()
	if !canonical.MatchString(printed) {
		t.Fatalf("%q printed as %q, which is not canonical", text, printed)
	}
	back, err := ParseNumber(printed)
	if err != nil {
		t.Fatalf("%q printed as %q, which does not parse: %v", text, printed, err)
	}
	if back.Cmp(d) != 0 || back.String() != printed {
		t.Fatalf("%q printed as %q, which reads back as %q", text, printed, back.String())
	}
	if exact(t, printed).Cmp(exact(t, text)) != 0 {
		t.Fatalf("%q printed as %q, another value", text, printed)
	}
}

// FuzzParse holds Parse to its contract: what it accepts is plain,
// non-negative, and Parse(d.String()) equals d.
func FuzzParse(f *testing.F) {
	for _, seed := range figureSeeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		d, err := Parse(text)
		if err != nil {
			return
		}
		if strings.ContainsAny(text, "-+eE ") || d.Sign() < 0 {
			t.Fatalf("Parse accepted %q, which is not a plain non-negative decimal", text)
		}
		again, err := Parse(d.String())
		if err != nil || again.Cmp(d) != 0 {
			t.Fatalf("Parse(%q) = %s, and Parse of that is %s, %v", text, d, again, err)
		}
		assertRoundTrip(t, text, d)
	})
}

// FuzzParseNumber holds ParseNumber, which reads a provider's figure, to the
// value the text states, exactly, and to the same round trip.
func FuzzParseNumber(f *testing.F) {
	for _, seed := range figureSeeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		d, err := ParseNumber(text)
		if err != nil {
			return
		}
		if exact(t, text).Cmp(exact(t, d.String())) != 0 {
			t.Fatalf("ParseNumber(%q) = %s, another value", text, d)
		}
		assertRoundTrip(t, text, d)
	})
}

// FuzzArithmetic holds the sums every figure is built from to exact rational
// arithmetic: a sum, a difference and a product equal big.Rat's, and adding
// then taking away a value returns the first.
func FuzzArithmetic(f *testing.F) {
	f.Add("0.0000002", "0.001619725", uint8(6))
	f.Add("9223372036854775807", "1", uint8(0))
	f.Add("3.75", "-12.5", uint8(12))
	f.Fuzz(func(t *testing.T, left, right string, power uint8) {
		a, err := ParseNumber(left)
		if err != nil {
			return
		}
		b, err := ParseNumber(right)
		if err != nil {
			return
		}
		ra, rb := exact(t, a.String()), exact(t, b.String())
		for name, pair := range map[string]struct {
			got  Decimal
			want *big.Rat
		}{
			"sum":        {a.Add(b), new(big.Rat).Add(ra, rb)},
			"difference": {a.Sub(b), new(big.Rat).Sub(ra, rb)},
			"product":    {a.Mul(b), new(big.Rat).Mul(ra, rb)},
		} {
			if exact(t, pair.got.String()).Cmp(pair.want) != 0 {
				t.Fatalf("the %s of %s and %s is %s, want %s", name, a, b, pair.got, pair.want.FloatString(40))
			}
		}
		if back := a.Add(b).Sub(b); back.Cmp(a) != 0 {
			t.Fatalf("%s + %s - %s = %s", a, b, b, back)
		}
		if got, want := a.Cmp(b), ra.Cmp(rb); got != want {
			t.Fatalf("Cmp(%s, %s) = %d, want %d", a, b, got, want)
		}
		shifted := a.DivPow10(int(power % 40))
		scale := new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(power%40)), nil))
		if exact(t, shifted.String()).Cmp(new(big.Rat).Quo(ra, scale)) != 0 {
			t.Fatalf("%s / 10^%d = %s", a, power%40, shifted)
		}
	})
}
