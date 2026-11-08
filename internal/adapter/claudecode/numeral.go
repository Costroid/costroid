package claudecode

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// numberPattern is a JSON number: sign, integer part, fraction, exponent.
var numberPattern = regexp.MustCompile(`^(-?)(0|[1-9][0-9]*)(?:\.([0-9]+))?(?:[eE]([+-]?[0-9]+))?$`)

// numeral is a JSON number held exactly, as digits times a power of ten.
type numeral struct {
	negative bool
	// digits has no leading zero. Empty means zero.
	digits   string
	exponent int64
}

var (
	errNotWhole    = errors.New("is not a whole number")
	errPastLargest = errors.New("is past the largest count")
)

const (
	// farthestExponent stands in for an exponent too large to hold. Any
	// count it scales is past the largest or below one.
	farthestExponent = 1 << 40
	// largestCountDigits is the most digits an int64 count can hold.
	largestCountDigits = 19
)

// readNumeral reads a JSON number, or a string that holds one.
func readNumeral(raw json.RawMessage) (numeral, bool) {
	literal := string(raw)
	if text, ok := jsonString(raw); ok {
		literal = text
	}
	parts := numberPattern.FindStringSubmatch(literal)
	if parts == nil {
		return numeral{}, false
	}
	exponent, err := strconv.ParseInt(parts[4], 10, 32)
	switch {
	case parts[4] == "":
		exponent = 0
	case err != nil && strings.HasPrefix(parts[4], "-"):
		exponent = -farthestExponent
	case err != nil:
		exponent = farthestExponent
	}
	return numeral{
		negative: parts[1] == "-",
		digits:   strings.TrimLeft(parts[2]+parts[3], "0"),
		exponent: exponent - int64(len(parts[3])),
	}, true
}

// integer is the numeral's value, when it is a whole number that fits.
func (n numeral) integer() (int64, error) {
	digits, exponent := n.digits, n.exponent
	if digits == "" {
		return 0, nil
	}
	for exponent < 0 && strings.HasSuffix(digits, "0") {
		digits, exponent = digits[:len(digits)-1], exponent+1
	}
	if exponent < 0 {
		return 0, errNotWhole
	}
	if int64(len(digits))+exponent > largestCountDigits {
		return 0, errPastLargest
	}
	value, err := strconv.ParseInt(digits+strings.Repeat("0", int(exponent)), 10, 64)
	if err != nil {
		return 0, errPastLargest
	}
	if n.negative {
		return -value, nil
	}
	return value, nil
}

// wholeCount reads a token count stated as 10, 10.0 or "10". Absent and null
// are zero. A fraction, a negative, a figure past the largest count and any
// other value are refused, because a count is a whole number of tokens.
func wholeCount(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}
	number, ok := readNumeral(raw)
	if !ok {
		return 0, fmt.Errorf("%s is not a number", shown(raw))
	}
	if number.negative && number.digits != "" {
		return 0, fmt.Errorf("%s is negative", shown(raw))
	}
	value, err := number.integer()
	if err != nil {
		return 0, fmt.Errorf("%s %w", shown(raw), err)
	}
	return value, nil
}
