package reconcile

import (
	"fmt"
	"regexp"

	"github.com/Costroid/costroid/internal/decimal"
)

// The form every figure of a row takes, as integration/v0.5 states it. An
// amount is an optional minus, digits, and an optional point with 1 to 30
// digits, at most 64 characters in all. A token count is digits only, at most
// 20 of them, and a difference of two counts may carry a minus.
var (
	amountForm      = regexp.MustCompile(`^-?[0-9]+(\.[0-9]{1,30})?$`)
	nonNegativeForm = regexp.MustCompile(`^[0-9]+(\.[0-9]{1,30})?$`)
	countForm       = regexp.MustCompile(`^[0-9]{1,20}$`)
	deltaForm       = regexp.MustCompile(`^-?[0-9]{1,20}$`)
)

const maxAmountLength = 64

const (
	amountRule = "an amount is a decimal of at most 30 places and 64 characters, and none is rounded to fit"
	countRule  = "a token count is a whole number of at most 20 digits"
)

// FigureError is a credential's day whose row would carry a figure past the
// form every amount and token count takes. The day is not written, and every
// other day still is. Field names where the figure stands in the row.
type FigureError struct {
	Key
	Field, Figure, rule string
}

func (e *FigureError) Error() string {
	return fmt.Sprintf("%s %s %s: %s reads %s, and %s, so the day is not written", e.Source, e.Credential, e.Day, e.Field, e.Figure, e.rule)
}

// form walks a row's figures and keeps the first one past its form.
type form struct {
	key     Key
	refused *FigureError
}

func (f *form) hold(field string, figure decimal.Decimal, pattern *regexp.Regexp, rule string) {
	text := figure.String()
	if f.refused != nil || (pattern.MatchString(text) && len(text) <= maxAmountLength) {
		return
	}
	f.refused = &FigureError{Key: f.key, Field: field, Figure: text, rule: rule}
}

func (f *form) amount(field string, figure decimal.Decimal) {
	f.hold(field, figure, amountForm, amountRule)
}

func (f *form) nonNegative(field string, figure decimal.Decimal) {
	f.hold(field, figure, nonNegativeForm, amountRule)
}

func (f *form) count(field string, figure decimal.Decimal) {
	f.hold(field, figure, countForm, countRule)
}

func (f *form) delta(field string, figure decimal.Decimal) {
	f.hold(field, figure, deltaForm, countRule)
}

// formOf holds every figure of a row to its form, and returns the first that
// breaks it, or nil.
func formOf(row Row) *FigureError {
	f := &form{key: row.Key}
	for i, check := range row.Tokens {
		at := fmt.Sprintf("tokens[%d].", i)
		f.count(at+"ledger", check.Ledger)
		f.count(at+"provider", check.Provider)
		f.delta(at+"delta", check.Delta)
	}
	for i, check := range row.Prices {
		at := fmt.Sprintf("prices[%d].", i)
		f.count(at+"tokens", check.Tokens)
		f.amount(at+"provider_list", check.ProviderList)
		if check.ImpliedUnitPrice != nil {
			f.amount(at+"implied_unit_price", *check.ImpliedUnitPrice)
		}
		f.nonNegative(at+"catalog_unit_price", check.CatalogUnitPrice)
	}
	if row.Dollars != nil {
		f.dollars(*row.Dollars)
	}
	return f.refused
}

func (f *form) dollars(check DollarCheck) {
	for i, class := range check.Classes {
		at := fmt.Sprintf("dollars.classes[%d].", i)
		f.nonNegative(at+"ledger", class.Ledger)
		f.amount(at+"provider", class.Provider)
		f.amount(at+"delta", class.Delta)
	}
	f.nonNegative("dollars.ledger", check.Ledger)
	f.amount("dollars.provider", check.Provider)
	f.amount("dollars.provider_effective", check.ProviderEffective)
	if check.ProviderList != nil {
		f.amount("dollars.provider_list", *check.ProviderList)
	}
	f.amount("dollars.other_cost", check.OtherCost)
	f.amount("dollars.residual", check.Residual)
	for i, cause := range check.Causes {
		f.amount(fmt.Sprintf("dollars.causes[%d].amount", i), cause.Amount)
	}
	f.amount("dollars.unattributed", check.Unattributed)
	if check.Rounding != nil {
		f.amount("dollars.rounding.variance", check.Rounding.Variance)
	}
}
