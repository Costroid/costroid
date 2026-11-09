package comment

import (
	"fmt"
	"regexp"
	"slices"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/reconcile/state"
)

// capCheck sets the run's cap beside the figures in its currency. A cap is
// compared with one figure only: the figures of one currency and one basis.
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

// capCheck is nil when the report was not told the cap.
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
	// An omitted figure could be in the cap's currency, so a total of the
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

func (c *capCheck) block() *BlockCap {
	if c == nil {
		return nil
	}
	held := &BlockCap{Currency: budgetCurrency, Amount: c.amount.String()}
	if c.grade != "" {
		held.EnforcementGrade = &c.grade
	}
	if !c.compared {
		return held
	}
	held.Cost, held.CostBasis = text(c.cost.String()), &c.basis
	held.Remainder, held.Overshoot = text(c.remainder.String()), text(c.overshoot.String())
	if c.overshootTokens != nil {
		held.OvershootTokens = text(c.overshootTokens.String())
	}
	return held
}

func text(value string) *string {
	return &value
}

// capLine is the cap line of a report that shows amounts. A cap is stated in
// USD and never in turns, beside the grade that held it, and a run nothing
// held states no cap. A cap the report was not told is named by its grade
// alone, so it never reads as a missing budget.
func (i Input) capLine() string {
	if i.Budget != nil {
		return i.capCheck().line(i.Preview)
	}
	switch grade := weakestGrade(i.Ledger.Figures); grade {
	case "":
		return ""
	case "none":
		return noCap
	default:
		return fmt.Sprintf("Cap: held by %s (%s). The rows do not record its amount, and the report was not given it.", grade, enforcementNotes[grade])
	}
}

const noCap = "No cap held this run: its enforcement grade is none."

// line states the cap beside the cost. A run nothing held states no cap,
// unless a preview gave it one: then the line compares the cost with it, as
// the result object does.
func (c *capCheck) line(preview bool) string {
	if c.grade == "none" {
		if !preview || !c.compared {
			return noCap
		}
		return noCap + fmt.Sprintf(" Cap: %s %s, which nothing enforced.", budgetCurrency, c.amount) + c.spent()
	}
	held := fmt.Sprintf("Cap: %s %s (%s: %s).", budgetCurrency, c.amount, c.grade, enforcementNotes[c.grade])
	if !c.compared {
		return held + " The figures above are not one " + budgetCurrency + " figure of one basis, so none is compared with the cap."
	}
	return held + c.spent()
}

// spent states the cost and how far under or over the cap it is.
func (c *capCheck) spent() string {
	spent := fmt.Sprintf(" Cost: %s %s, %s.", budgetCurrency, c.cost, basisLabels[c.basis])
	if c.overshoot.Sign() == 0 {
		return spent + fmt.Sprintf(" Under the cap by %s %s.", budgetCurrency, c.remainder)
	}
	over := fmt.Sprintf(" Over the cap by %s %s", budgetCurrency, c.overshoot)
	if c.overshootTokens != nil {
		over += fmt.Sprintf(" and %s tokens", c.overshootTokens)
	}
	return spent + over + "."
}

// Reconciliation is the last reconciliation of one credential's day.
type Reconciliation struct {
	// Credential is the row's SubAccountId, a string from the rows.
	Credential Data
	// Day is the UTC day reconciled, as YYYY-MM-DD.
	Day   string
	State state.State
}

var dayPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)

var reconciliationNotes = map[state.State]string{
	state.Matched:    "tokens matched the provider's report within 1%",
	state.TokensMiss: "a token class missed the provider's report by more than 1%",
	state.Pending:    "the provider had not finished reporting the day",
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

// limitSettings name the costroid.yml key each budget rule reads.
var limitSettings = map[string]string{
	policy.RuleRunBudget:        "policy.run_budget_usd",
	policy.RuleRunCostThreshold: "policy.max_run_cost_usd",
}

// limitLines state each limit the policy checked and the setting it came
// from, so a policy's budget is never read as the run's cap. A run that a cap
// held is told the verdict does not check that cap.
func limitLines(verdict *policy.Verdict, grade string) []string {
	if verdict == nil {
		return nil
	}
	var lines []string
	for _, result := range verdict.Results {
		setting, budgetRule := limitSettings[result.Rule]
		if !budgetRule || result.LimitUSD == "" {
			continue
		}
		line := fmt.Sprintf("%s: USD %s, from `%s` in `costroid.yml`.", ruleLabels[result.Rule], result.LimitUSD, setting)
		if grade != "none" {
			line += fmt.Sprintf(" The verdict checks this %s, never the cap.", limitNouns[result.Rule])
		}
		lines = append(lines, line)
	}
	return lines
}
