package reconcile

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

const dayLayout = "2006-01-02"

// Provider fetches one source's report for the credentials the ledger names.
type Provider interface {
	Name() string
	Fetch(ctx context.Context, days Range, credentials []string) (Report, error)
}

// Usage is one provider or ledger token record of one credential on one day.
type Usage struct {
	Credential string
	Day        string
	Model      string
	Tier       string
	Region     string
	Tokens     map[focus.TokenClass]decimal.Decimal
}

// Cost is one line of a cost report in USD. Tokens is false for a line that
// bills anything else. List is the same line at list price, where the provider
// states one.
type Cost struct {
	Credential string
	Day        string
	Amount     decimal.Decimal
	List       *decimal.Decimal
	Tokens     bool
}

// Report is one source's records for a range. Unsettled names the days the
// provider says it has not finished reporting. Lag is how long after a day's
// end to wait when the provider stamps no such thing.
type Report struct {
	Source    string
	Usage     []Usage
	Costs     []Cost
	Unsettled map[string]bool
	Lag       time.Duration
}

func (r Report) usageOf(key Key) []Usage {
	var records []Usage
	for _, record := range r.Usage {
		if record.Credential == key.Credential && record.Day == key.Day {
			records = append(records, record)
		}
	}
	return records
}

// costTotal is one credential's day. List is nil unless every token line
// stated a list figure.
type costTotal struct {
	amount, other decimal.Decimal
	list          *decimal.Decimal
}

func (r Report) costOf(key Key) costTotal {
	var total costTotal
	var list decimal.Decimal
	stated := false
	missing := false
	for _, cost := range r.Costs {
		if cost.Credential != key.Credential || cost.Day != key.Day {
			continue
		}
		if !cost.Tokens {
			total.other = total.other.Add(cost.Amount)
			continue
		}
		total.amount = total.amount.Add(cost.Amount)
		if cost.List == nil {
			missing = true
			continue
		}
		stated = true
		list = list.Add(*cost.List)
	}
	if stated && !missing {
		total.list = &list
	}
	return total
}

// Range is an inclusive run of UTC days.
type Range struct {
	From, To time.Time
}

// ParseRange reads two YYYY-MM-DD days.
func ParseRange(from, to string) (Range, error) {
	first, err := time.Parse(dayLayout, from)
	if err != nil {
		return Range{}, fmt.Errorf("reconcile: from day: %w", err)
	}
	last, err := time.Parse(dayLayout, to)
	if err != nil {
		return Range{}, fmt.Errorf("reconcile: to day: %w", err)
	}
	if last.Before(first) {
		return Range{}, fmt.Errorf("reconcile: to day %s is before from day %s", to, from)
	}
	return Range{From: first, To: last}, nil
}

func (r Range) holds(day string) bool {
	return day >= r.From.Format(dayLayout) && day <= r.To.Format(dayLayout)
}

// window is a half-open span of whole days, as the providers read one.
type window struct {
	start, end time.Time
}

func (w window) days() []string {
	var days []string
	for at := w.start; at.Before(w.end); at = at.AddDate(0, 0, 1) {
		days = append(days, at.Format(dayLayout))
	}
	return days
}

// windows cuts the range into spans of at most limit days.
func (r Range) windows(limit int) []window {
	var spans []window
	end := r.To.AddDate(0, 0, 1)
	for start := r.From; start.Before(end); start = start.AddDate(0, 0, limit) {
		spans = append(spans, window{start, minTime(start.AddDate(0, 0, limit), end)})
	}
	return spans
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func parseDay(day string) time.Time {
	at, err := time.Parse(dayLayout, day)
	if err != nil {
		panic(fmt.Sprintf("reconcile: day %q was not built by this package: %v", day, err))
	}
	return at
}

func endOfDay(day string) time.Time {
	return parseDay(day).AddDate(0, 0, 1)
}

// grains says which report dimension each credential needs, by the id's
// prefix. Everything else is an API key.
func grains(credentials []string, scopePrefix, keyGrain, scopeGrain string) []string {
	var needed []string
	for _, credential := range credentials {
		grain := keyGrain
		if strings.HasPrefix(credential, scopePrefix) {
			grain = scopeGrain
		}
		if !slices.Contains(needed, grain) {
			needed = append(needed, grain)
		}
	}
	return needed
}

// maxExponent bounds a provider's scientific notation. Anything larger is not
// a money figure.
const maxExponent = 64

// parseAmount reads a provider's figure exactly. Unlike a row's amounts it may
// carry a sign, and OpenAI's are JSON numbers, which can carry an exponent.
func parseAmount(text string) (decimal.Decimal, error) {
	digits, negative := strings.CutPrefix(text, "-")
	mantissa, exponent, scientific := strings.Cut(strings.ToLower(digits), "e")
	value, err := decimal.Parse(mantissa)
	if err != nil {
		return decimal.Decimal{}, err
	}
	if scientific {
		shift, err := strconv.Atoi(exponent)
		if err != nil || shift > maxExponent || shift < -maxExponent {
			return decimal.Decimal{}, fmt.Errorf("reconcile: exponent of %q is out of range", text)
		}
		if shift > 0 {
			value = value.Mul(decimal.MustParse("1" + strings.Repeat("0", shift)))
		} else {
			value = value.DivPow10(-shift)
		}
	}
	if negative {
		return decimal.Decimal{}.Sub(value), nil
	}
	return value, nil
}
