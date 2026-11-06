package policy

import (
	"bufio"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

const (
	maxRowBytes     = 1 << 20
	maxAmountLength = 64
)

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// Usage is one run's rows, reduced to what the pack reads.
type Usage struct {
	Rows       int
	Currencies []CurrencyUsage
}

// CurrencyUsage is the rows of one currency. ListCost is their exact sum, and
// no sum spans two currencies.
type CurrencyUsage struct {
	Currency string
	ListCost decimal.Decimal
	Rows     int
	// UnattributedRows never satisfy a budget rule.
	UnattributedRows int
	// JoinableRows are unattributed, and name a credential, a pull
	// request and a head commit, so a token-verified join would grade them
	// exact.
	JoinableRows int
}

func (u Usage) usd() (decimal.Decimal, bool) {
	for _, entry := range u.Currencies {
		if entry.Currency == "USD" {
			return entry.ListCost, true
		}
	}
	return decimal.Decimal{}, false
}

func (u Usage) joinable() int {
	count := 0
	for _, entry := range u.Currencies {
		count += entry.JoinableRows
	}
	return count
}

type usageRow struct {
	BillingCurrency string                     `json:"BillingCurrency"`
	ListCost        string                     `json:"ListCost"`
	SubAccountID    *string                    `json:"SubAccountId"`
	Tags            map[string]json.RawMessage `json:"Tags"`
}

// tag reads a string tag, empty when it is absent or not a string.
func (r usageRow) tag(key string) string {
	var value string
	if json.Unmarshal(r.Tags[key], &value) != nil {
		return ""
	}
	return value
}

// joinable applies the ledger's rule for an exact grade to a row that
// lacks only the token: a credential that is not the session id, a pull
// request and a head.
func (r usageRow) joinable() bool {
	credential := ""
	if r.SubAccountID != nil {
		credential = *r.SubAccountID
	}
	return credential != "" && credential != r.tag(focus.TagSessionID) &&
		r.tag(focus.TagPR) != "" && r.tag(focus.TagCommitSHA) != ""
}

// ReadUsage reads FOCUS rows as JSON lines from a file the agent's job wrote.
// Every row is unattributed whatever its tags claim, because the job could
// write any tag. One bad row refuses the file: a partial total would read as a
// smaller cost.
func ReadUsage(source io.Reader) (Usage, error) {
	totals := map[string]*CurrencyUsage{}
	scanner := bufio.NewScanner(source)
	scanner.Buffer(nil, maxRowBytes)
	rows := 0
	line := 1
	for ; scanner.Scan(); line++ {
		if len(scanner.Bytes()) == 0 {
			continue
		}
		if err := add(totals, scanner.Bytes()); err != nil {
			return Usage{}, fmt.Errorf("policy: rows line %d: %w", line, err)
		}
		rows++
	}
	err := scanner.Err()
	if errors.Is(err, bufio.ErrTooLong) {
		return Usage{}, fmt.Errorf("policy: rows line %d is past %d bytes", line, maxRowBytes)
	}
	if err != nil {
		return Usage{}, fmt.Errorf("policy: read rows: %w", err)
	}
	usage := Usage{Rows: rows}
	for _, entry := range totals {
		usage.Currencies = append(usage.Currencies, *entry)
	}
	slices.SortFunc(usage.Currencies, func(a, b CurrencyUsage) int { return cmp.Compare(a.Currency, b.Currency) })
	return usage, nil
}

func add(totals map[string]*CurrencyUsage, line []byte) error {
	var row usageRow
	if err := json.Unmarshal(line, &row); err != nil {
		return err
	}
	if !currencyPattern.MatchString(row.BillingCurrency) {
		return errors.New("BillingCurrency is not an ISO code")
	}
	if len(row.ListCost) > maxAmountLength {
		return fmt.Errorf("ListCost is past %d characters", maxAmountLength)
	}
	cost, err := decimal.Parse(row.ListCost)
	if err != nil {
		return fmt.Errorf("ListCost: %w", err)
	}
	if cost.Scale() > focus.MaxScale {
		return fmt.Errorf("ListCost has more than %d fractional digits", focus.MaxScale)
	}
	entry, found := totals[row.BillingCurrency]
	if !found {
		entry = &CurrencyUsage{Currency: row.BillingCurrency}
		totals[row.BillingCurrency] = entry
	}
	entry.ListCost = entry.ListCost.Add(cost)
	entry.Rows++
	entry.UnattributedRows++
	if row.joinable() {
		entry.JoinableRows++
	}
	return nil
}
