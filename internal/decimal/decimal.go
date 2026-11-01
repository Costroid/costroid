// Package decimal is an exact base-10 number for money and quantities.
//
// A Decimal is an integer coefficient over a power of ten, so addition and
// multiplication never round. Scale limits are the caller's to enforce.
package decimal

import (
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

var literal = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]+)?$`)

// Decimal is an exact non-negative or negative decimal value. The zero value is 0.
type Decimal struct {
	coefficient *big.Int
	scale       int
}

// Parse reads a plain decimal string. It refuses signs, exponents and blanks.
func Parse(text string) (Decimal, error) {
	if !literal.MatchString(text) {
		return Decimal{}, fmt.Errorf("decimal: %q is not a plain non-negative decimal", text)
	}
	whole, fraction, _ := strings.Cut(text, ".")
	coefficient, ok := new(big.Int).SetString(whole+fraction, 10)
	if !ok {
		return Decimal{}, fmt.Errorf("decimal: %q has no integer form", text)
	}
	return Decimal{coefficient: coefficient, scale: len(fraction)}.normal(), nil
}

// MustParse is Parse for literals the program itself states.
func MustParse(text string) Decimal {
	d, err := Parse(text)
	if err != nil {
		panic(err)
	}
	return d
}

// FromInt converts a whole count.
func FromInt(value int64) Decimal {
	return Decimal{coefficient: big.NewInt(value)}
}

func (d Decimal) int() *big.Int {
	if d.coefficient == nil {
		return new(big.Int)
	}
	return d.coefficient
}

func (d Decimal) normal() Decimal {
	coefficient := new(big.Int).Set(d.int())
	scale := d.scale
	ten := big.NewInt(10)
	for scale > 0 {
		quotient, remainder := new(big.Int).QuoRem(coefficient, ten, new(big.Int))
		if remainder.Sign() != 0 {
			break
		}
		coefficient, scale = quotient, scale-1
	}
	return Decimal{coefficient: coefficient, scale: scale}
}

// aligned returns both coefficients at the larger of the two scales.
func aligned(a, b Decimal) (*big.Int, *big.Int, int) {
	scale := max(a.scale, b.scale)
	return rescale(a, scale), rescale(b, scale), scale
}

func rescale(d Decimal, scale int) *big.Int {
	factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale-d.scale)), nil)
	return new(big.Int).Mul(d.int(), factor)
}

// Add returns d + other, exactly.
func (d Decimal) Add(other Decimal) Decimal {
	a, b, scale := aligned(d, other)
	return Decimal{coefficient: new(big.Int).Add(a, b), scale: scale}.normal()
}

// Sub returns d - other, exactly.
func (d Decimal) Sub(other Decimal) Decimal {
	a, b, scale := aligned(d, other)
	return Decimal{coefficient: new(big.Int).Sub(a, b), scale: scale}.normal()
}

// Mul returns d × other, exactly.
func (d Decimal) Mul(other Decimal) Decimal {
	product := new(big.Int).Mul(d.int(), other.int())
	return Decimal{coefficient: product, scale: d.scale + other.scale}.normal()
}

// DivPow10 returns d ÷ 10^power, exactly.
func (d Decimal) DivPow10(power int) Decimal {
	return Decimal{coefficient: d.int(), scale: d.scale + power}.normal()
}

// Cmp returns -1, 0 or 1 as d is below, equal to or above other.
func (d Decimal) Cmp(other Decimal) int {
	a, b, _ := aligned(d, other)
	return a.Cmp(b)
}

// Sign returns -1, 0 or 1.
func (d Decimal) Sign() int {
	return d.int().Sign()
}

// Scale returns the number of fractional digits the value needs.
func (d Decimal) Scale() int {
	return d.normal().scale
}

// String writes the value in plain notation with no padding.
func (d Decimal) String() string {
	n := d.normal()
	digits := new(big.Int).Abs(n.int()).String()
	sign := ""
	if n.int().Sign() < 0 {
		sign = "-"
	}
	if n.scale == 0 {
		return sign + digits
	}
	if len(digits) <= n.scale {
		digits = strings.Repeat("0", n.scale-len(digits)+1) + digits
	}
	point := len(digits) - n.scale
	return sign + digits[:point] + "." + digits[point:]
}

// MarshalJSON writes the value as a JSON string, never a JSON number.
func (d Decimal) MarshalJSON() ([]byte, error) {
	return []byte(`"` + d.String() + `"`), nil
}
