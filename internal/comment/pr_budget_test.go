package comment

import (
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/policy"
)

// TestPRBudgetNamesItsBases holds the pull request budget's line to the bases
// its USD total adds: both when the pull request's runs hold two, the one when
// they hold one, and every basis when no lifetime names them. A figure in
// another currency is not in the total, so its basis is not named.
func TestPRBudgetNamesItsBases(t *testing.T) {
	over := "Pull request budget: USD 10.5 over this pull request's runs is over the USD 10 budget. "
	figure := func(currency, basis, cost string) LifetimeFigure {
		return LifetimeFigure{Currency: currency, Basis: basis, Cost: decimal.MustParse(cost), Reports: 1}
	}
	for _, tc := range []struct {
		name     string
		lifetime *Lifetime
		want     string
	}{
		{"two bases", &Lifetime{Figures: []LifetimeFigure{figure("USD", "list_equivalent", "4"), figure("EUR", "billed", "2"), figure("USD", "measured", "6.5")}},
			over + "The total adds Measured and List-equivalent together, at list."},
		{"one basis", &Lifetime{Figures: []LifetimeFigure{figure("USD", "list_equivalent", "10.5"), figure("EUR", "measured", "2")}},
			over + "The total is at list (List-equivalent, not billed)."},
		{"no lifetime", nil, over + "The total adds every basis together, at list."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := realInput(t)
			input.Lifetime = tc.lifetime
			input.Verdict = &policy.Verdict{Conclusion: policy.Failure, Results: []policy.Result{
				{Rule: policy.RulePullRequestBudget, Outcome: policy.Fail, Reason: policy.ReasonOverLimit, LimitUSD: "10", CostUSD: "10.5"},
			}}
			if body := commentOf(t, input); !strings.Contains(body, tc.want) {
				t.Errorf("the comment does not say %q:\n%s", tc.want, body)
			}
			check, err := Check(input)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(check.Summary, tc.want) {
				t.Errorf("the check does not say %q:\n%s", tc.want, check.Summary)
			}
		})
	}
}
