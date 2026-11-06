package comment

import (
	"fmt"
	"regexp"
	"slices"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/reconcile"
)

// capCheck sets the run's budget beside the figures in its currency. A budget
// is compared with one figure only: the figures of one currency and one basis.
type capCheck struct {
	amount decimal.Decimal
	// grade is the weakest enforcement grade of the figures, empty with none.
	grade string
	// compared says one currency and basis matched the budget's, and the
	// fields below hold.
	compared  bool
	cost      decimal.Decimal
	basis     string
	remainder decimal.Decimal
	overshoot decimal.Decimal
	// overshootTokens is nil when the rows hold too many requests to keep.
	overshootTokens *decimal.Decimal
}

// capCheck is nil when the report was not told the budget.
func (i Input) capCheck() *capCheck {
	if i.Budget == nil {
		return nil
	}
	check := &capCheck{amount: *i.Budget, grade: weakestGrade(i.Ledger.Figures)}
	bases := map[string]bool{}
	for _, figure := range i.Ledger.Figures {
		if figure.Currency == budgetCurrency {
			bases[figure.Basis] = true
			check.cost = check.cost.Add(figure.Cost)
			check.basis = figure.Basis
		}
	}
	// An omitted figure could be in the budget's currency, so a total of the
	// figures shown would be short.
	if len(bases) != 1 || i.Ledger.FiguresOmitted > 0 {
		return &capCheck{amount: check.amount, grade: check.grade}
	}
	check.compared = true
	if over := check.cost.Sub(check.amount); over.Sign() > 0 {
		check.overshoot = over
	} else {
		check.remainder = check.amount.Sub(check.cost)
	}
	if tokens, known := i.Ledger.overshootTokens(budgetCurrency, check.basis, check.amount); known {
		check.overshootTokens = &tokens
	}
	return check
}

// weakestGrade reads the grades from the weakest up, so a run of two grades
// is never shown as the stronger one.
func weakestGrade(figures []Figure) string {
	weakest := len(enforcementOrder)
	for _, figure := range figures {
		weakest = min(weakest, slices.Index(enforcementOrder, figure.Enforcement))
	}
	if weakest == len(enforcementOrder) {
		return ""
	}
	return enforcementOrder[weakest]
}

func (c *capCheck) block() *BlockBudget {
	if c == nil {
		return nil
	}
	budget := &BlockBudget{Currency: budgetCurrency, Amount: c.amount.String()}
	if c.grade != "" {
		budget.EnforcementGrade = &c.grade
	}
	if !c.compared {
		return budget
	}
	budget.Cost, budget.CostBasis = text(c.cost.String()), &c.basis
	budget.Remainder, budget.Overshoot = text(c.remainder.String()), text(c.overshoot.String())
	if c.overshootTokens != nil {
		budget.OvershootTokens = text(c.overshootTokens.String())
	}
	return budget
}

func text(value string) *string {
	return &value
}

// line is the budget line of a report that shows amounts. A cap is stated in
// USD and never in turns, beside the grade that held it, and a run nothing
// held states no cap.
func (c *capCheck) line() string {
	switch {
	case c == nil:
		return "Budget: not recorded on these rows."
	case c.grade == "none":
		return "No cap held this run: its enforcement grade is none."
	}
	held := fmt.Sprintf("Cap: %s %s (%s: %s).", budgetCurrency, c.amount, c.grade, enforcementNotes[c.grade])
	if !c.compared {
		return held + " The figures above are not one " + budgetCurrency + " figure of one basis, so none is compared with the cap."
	}
	spent := fmt.Sprintf(" Cost: %s %s, %s.", budgetCurrency, c.cost, basisLabels[c.basis])
	if c.overshoot.Sign() == 0 {
		return held + spent + fmt.Sprintf(" Under the cap by %s %s.", budgetCurrency, c.remainder)
	}
	over := fmt.Sprintf(" Over the cap by %s %s", budgetCurrency, c.overshoot)
	if c.overshootTokens != nil {
		over += fmt.Sprintf(" and %s tokens", c.overshootTokens)
	}
	return held + spent + over + "."
}

// Reconciliation is the last reconciliation of one credential's day.
type Reconciliation struct {
	// Credential is the row's SubAccountId, a string from the rows.
	Credential Data
	// Day is the UTC day reconciled, as YYYY-MM-DD.
	Day   string
	State reconcile.State
}

var dayPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)

var reconciliationNotes = map[reconcile.State]string{
	reconcile.Matched:    "tokens matched the provider's report within 1%",
	reconcile.TokensMiss: "a token class missed the provider's report by more than 1%",
	reconcile.Pending:    "the provider had not finished reporting the day",
}

// reconciliationLines states each reconciliation in a closed sentence. One
// with a malformed day or an unknown state is left out.
func reconciliationLines(reconciliations []Reconciliation) []string {
	var lines []string
	for _, last := range reconciliations {
		note, known := reconciliationNotes[last.State]
		if !known || !dayPattern.MatchString(last.Day) {
			continue
		}
		lines = append(lines, fmt.Sprintf("Last reconciliation of %s: %s, %s.", last.Credential.Code(), last.Day, note))
	}
	return lines
}
