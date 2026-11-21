package comment

import "strings"

// minPlaces is how many decimal places an amount shows at least, wherever a
// person reads it.
const minPlaces = 2

// places draws an exact decimal string with at least minPlaces decimal
// places. It only adds zeros: no digit is read as a number, rounded or
// dropped. The machine block, the outputs and --json keep the exact string.
func places(exact string) string {
	whole, fraction, _ := strings.Cut(exact, ".")
	if len(fraction) >= minPlaces {
		return exact
	}
	return whole + "." + fraction + strings.Repeat("0", minPlaces-len(fraction))
}

// money draws an amount after its currency code, as every amount a person
// reads is drawn.
func money(currency, exact string) string {
	return currency + " " + places(exact)
}
