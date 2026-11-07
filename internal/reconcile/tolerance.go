package reconcile

import (
	"fmt"

	"github.com/Costroid/costroid/internal/decimal"
)

// minorUnits is the number of decimal places of each currency's smallest unit,
// from ISO 4217. A currency missing here has no rounding tolerance.
var minorUnits = map[string]int{
	"USD": 2,
	"EUR": 2,
	"GBP": 2,
	"JPY": 0,
}

// roundingFloor is the tolerance's fixed part, in the currency's smallest
// units.
const roundingFloor = 100

// RoundingCheck holds FOCUS 1.4's rounding variance tolerance applied to one
// comparison of a sum of rows with a provider's own figure for the same
// charges.
type RoundingCheck struct {
	Currency string          `json:"currency"`
	Rows     int64           `json:"rows"`
	Variance decimal.Decimal `json:"variance"`
	Within   bool            `json:"within"`
}

// WithinRounding applies FOCUS 1.4's rounding variance tolerance,
// MAX(100 × Subunit, SQRT(Rows) × 0.5 × Subunit), to a variance between a sum
// of rows and the figure it is compared with. It compares squares, so no
// square root and no float is taken: with v the variance in smallest units,
// the variance is within when |v| ≤ 100, or when v² ≤ Rows × 0.5², which is
// 4 × v² ≤ Rows. The bound is inclusive.
func WithinRounding(currency string, rows int64, variance decimal.Decimal) (bool, error) {
	places, known := minorUnits[currency]
	if !known {
		return false, fmt.Errorf("currency %q has no stated smallest unit", currency)
	}
	if rows < 0 {
		return false, fmt.Errorf("row count %d is negative", rows)
	}
	units := absolute(variance).Mul(decimal.FromInt(pow10(places)))
	if units.Cmp(decimal.FromInt(roundingFloor)) <= 0 {
		return true, nil
	}
	return units.Mul(units).Mul(decimal.FromInt(4)).Cmp(decimal.FromInt(rows)) <= 0, nil
}

func pow10(places int) int64 {
	value := int64(1)
	for range places {
		value *= 10
	}
	return value
}
