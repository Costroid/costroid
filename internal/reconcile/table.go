package reconcile

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math/big"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/Costroid/costroid/internal/decimal"
)

const (
	notApplicable = "n/a"
	columnGap     = 2
	percentDigits = 2
)

// Render prints the tokens table and the dollars table.
func Render(w io.Writer, result Result) error {
	out := bufio.NewWriter(w)
	table := tabwriter.NewWriter(out, 0, 0, columnGap, ' ', 0)
	fmt.Fprintln(table, "TOKENS\tcredential\tday\tclass\tledger\tprovider\tdelta\tdiff\tverdict")
	for _, row := range result.Rows {
		renderTokens(table, row)
	}
	err := table.Flush()
	fmt.Fprintln(out)
	table = tabwriter.NewWriter(out, 0, 0, columnGap, ' ', 0)
	fmt.Fprintln(table, "DOLLARS\tcredential\tday\tledger USD\tprovider USD\tresidual\tdiff\tverdict\trounding\tattribution")
	for _, row := range result.Rows {
		renderDollars(table, row)
	}
	err = errors.Join(err, table.Flush())
	renderFooter(out, result)
	return errors.Join(err, out.Flush())
}

func renderTokens(w io.Writer, row Row) {
	if row.State == Pending {
		fmt.Fprintf(w, "%s\t%s\t%s\t\t\t\t\t\tpending\n", row.Source, row.Credential, row.Day)
		return
	}
	for _, check := range row.Tokens {
		verdict := "within 1%"
		if !check.Within {
			verdict = "MISS"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", row.Source, row.Credential, row.Day, check.Class,
			check.Ledger, check.Provider, check.Delta, percentOf(check.Delta, check.Provider), verdict)
	}
}

func renderDollars(w io.Writer, row Row) {
	if row.Dollars == nil {
		return
	}
	check := row.Dollars
	if check.Verdict == NoBill {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t\t\tno API bill\t\t\n", row.Source, row.Credential, row.Day, check.Ledger, check.Provider)
		return
	}
	verdict := "within 10%"
	if check.Verdict == Outside {
		verdict = "outside 10%"
	}
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", row.Source, row.Credential, row.Day, check.Ledger, check.Provider,
		check.Residual, percentOf(check.Residual, check.Provider), verdict, check.Rounding.verdict(), check.attribution())
}

// verdict reads the list figures' comparison in words with its row count, and
// n/a when the provider states no list figure.
func (r *RoundingCheck) verdict() string {
	if r == nil {
		return notApplicable
	}
	word := "outside"
	if r.Within {
		word = "within"
	}
	return fmt.Sprintf("%s, %d rows", word, r.Rows)
}

// attribution reads a residual in words: each named cause with its amount, then
// whatever no cause reached.
func (c DollarCheck) attribution() string {
	if c.Residual.Sign() == 0 {
		return "none needed"
	}
	var parts []string
	for _, cause := range c.Causes {
		parts = append(parts, fmt.Sprintf("%s %s", cause.Name, cause.Amount))
	}
	if c.Unattributed.Sign() != 0 {
		parts = append(parts, fmt.Sprintf("unattributed %s", c.Unattributed))
	}
	return strings.Join(parts, ", ")
}

// percentOf prints the size of a delta against the provider's figure. It is
// rendered from exact rationals, and no verdict reads it.
func percentOf(delta, provider decimal.Decimal) string {
	whole, ok := new(big.Rat).SetString(provider.String())
	if !ok || whole.Sign() == 0 {
		return notApplicable
	}
	part, ok := new(big.Rat).SetString(absolute(delta).String())
	if !ok {
		return notApplicable
	}
	ratio := new(big.Rat).Quo(part, whole)
	return ratio.Mul(ratio, big.NewRat(100, 1)).FloatString(percentDigits) + "%"
}

func renderFooter(w io.Writer, result Result) {
	for _, month := range openMonths(result.Rows) {
		fmt.Fprintln(w, month)
	}
	for _, row := range result.Rows {
		for _, note := range row.Notes {
			fmt.Fprintf(w, "%s %s %s: %s\n", row.Source, row.Credential, row.Day, note)
		}
	}
	for _, other := range result.Unmatched {
		fmt.Fprintf(w, "%s %s: the provider reports this credential and the ledger never names it\n", other.Source, other.Credential)
	}
	if misses := result.Misses(); misses > 0 {
		fmt.Fprintf(w, "Credential days with a token miss: %d. Each is a defect.\n", misses)
	}
}

// openMonths says which figures can still move, once per month.
func openMonths(rows []Row) []string {
	var lines []string
	for _, row := range rows {
		if row.Final {
			continue
		}
		last := endOfRerun(row.Day).AddDate(0, 0, -1).Format(dayLayout)
		line := fmt.Sprintf("%s can be restated: rerun through %s.", row.Day[:len("2006-01")], last)
		if !slices.Contains(lines, line) {
			lines = append(lines, line)
		}
	}
	return lines
}
