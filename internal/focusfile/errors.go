package focusfile

import (
	"encoding/csv"
	"errors"
	"fmt"
	"strings"
)

// The refusals. Each says what is wrong and what to write instead.
var (
	ErrTooLarge      = fmt.Errorf("the file is over %d bytes: write one row per resource, for this pull request alone", MaxBytes)
	ErrNoHeader      = errors.New("the file is empty: write a header line of FOCUS column names, then one row per resource")
	ErrNotCSV        = errors.New("the file is not CSV: write comma-separated values, every row with as many cells as the header")
	ErrColumnTwice   = errors.New("the header names this column twice, and nothing says which one holds the value: name it once")
	ErrNoCostColumn  = errors.New("the header names no cost column: write the estimate in ListCost")
	ErrAmbiguousCost = errors.New("the header has no ListCost and more than one of these, which mean different things: write the estimate in ListCost")
	ErrMissingColumn = errors.New("the header lacks this column, which every row needs")
	ErrNoAmount      = errors.New("the row has no amount: write the estimate as a decimal, such as 25.488")
	ErrNotDecimal    = errors.New("not a plain decimal: write digits with at most one point, such as 25.488, with no sign, exponent, grouping or space, and at most 26 digits before the point")
	ErrNegative      = errors.New("negative: the row contract holds amounts of 0 and above, so a saving cannot be one row. Write what the resources will cost, not the change")
	ErrScale         = errors.New("more than 12 digits after the point, which the row contract holds: round it where the estimator computes it")
	ErrNoCurrency    = errors.New("not a currency: write the ISO 4217 code of the row's amount, three upper-case letters such as EUR")
	ErrNoPeriod      = errors.New("the row has no charge period: write the start and the end of the period the amount covers")
	ErrInstant       = errors.New("not a FOCUS date and time: write UTC to the second, such as 2026-12-01T00:00:00Z")
	ErrPeriodOrder   = errors.New("the period ends at or before it starts: ChargePeriodEnd is exclusive and after ChargePeriodStart")
	ErrNotUsage      = errors.New("the row contract holds Usage charges alone: leave purchases, taxes, credits and adjustments out of the file")
	ErrChargeClass   = errors.New("the row contract holds no correction: leave ChargeClass empty and write the corrected amount")
	ErrTooLong       = fmt.Errorf("longer than %d characters, which the row contract holds", MaxText)
)

// Refusal is a file Read refused: the line and the column at fault, the value
// it holds, and why. Line 0 is the file as a whole, and line 1 its header.
type Refusal struct {
	Line   int
	Column string
	Value  string
	Err    error
}

func (r *Refusal) Error() string {
	var where []string
	if r.Line > 0 {
		where = append(where, fmt.Sprintf("line %d", r.Line))
	}
	if r.Column != "" {
		where = append(where, r.Column)
	}
	if r.Value != "" {
		where = append(where, fmt.Sprintf("%q", quoted(r.Value)))
	}
	if len(where) == 0 {
		return r.Err.Error()
	}
	return strings.Join(where, ", ") + ": " + r.Err.Error()
}

func (r *Refusal) Unwrap() error {
	return r.Err
}

// quoted is the start of a refused value, short enough to read in a job log.
func quoted(value string) string {
	runes := []rune(value)
	if len(runes) <= maxQuoted {
		return value
	}
	return string(runes[:maxQuoted]) + "..."
}

// csvRefusal names the line the CSV reader stopped at.
func csvRefusal(err error) error {
	var parse *csv.ParseError
	if errors.As(err, &parse) {
		return &Refusal{Line: parse.Line, Err: fmt.Errorf("%w: %v", ErrNotCSV, parse.Err)}
	}
	return fmt.Errorf("focusfile: read: %w", err)
}
