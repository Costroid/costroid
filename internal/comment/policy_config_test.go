package comment

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	costroid "github.com/Costroid/costroid"
	"github.com/Costroid/costroid/internal/policy"
)

// TestSchemaMatchesParsers lists the keys of the published schema, of the
// comment's reader and of the policy's parser, and holds them equal at every
// level.
func TestSchemaMatchesParsers(t *testing.T) {
	type properties map[string]struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	var schema struct {
		Properties properties `json:"properties"`
	}
	if err := json.Unmarshal(costroid.ConfigSchema, &schema); err != nil {
		t.Fatal(err)
	}
	top := slices.Sorted(maps.Keys(schema.Properties))
	read := slices.Sorted(maps.Keys(knownKeys[""]))
	reserved := slices.Sorted(maps.Keys(reservedKeys))
	if want := slices.Sorted(slices.Values(append(slices.Clone(read), reserved...))); !slices.Equal(top, want) {
		t.Errorf("the schema names %v, the comment reads %v and reserves %v", top, read, reserved)
	}
	for _, section := range []string{"comment", "policy"} {
		if got, want := slices.Sorted(maps.Keys(schema.Properties[section].Properties)), slices.Sorted(maps.Keys(knownKeys[section])); !slices.Equal(got, want) {
			t.Errorf("%s: the schema names %v, the comment reads %v", section, got, want)
		}
	}
	if got, want := slices.Sorted(maps.Keys(schema.Properties["policy"].Properties)), policy.Keys(); !slices.Equal(got, want) {
		t.Errorf("policy: the schema names %v, the policy parses %v", got, want)
	}
}

func TestShowCostNonBoolWarns(t *testing.T) {
	for _, tc := range []struct {
		document, warning string
		show              bool
	}{
		{"comment:\n  show_cost: yes\n", "costroid: warning: costroid.yml: comment.show_cost is a YAML 1.1 word and reads as true; write true or false\n", true},
		{"comment:\n  show_cost: no\n", "costroid: warning: costroid.yml: comment.show_cost is a YAML 1.1 word and reads as false; write true or false\n", false},
		{"comment:\n  show_cost: off\n", "costroid: warning: costroid.yml: comment.show_cost is a YAML 1.1 word and reads as false; write true or false\n", false},
		{"comment:\n  show_cost: false\n", "", false},
	} {
		config, output := parseWarnings(t, tc.document)
		if config.ShowCost != tc.show || output != tc.warning {
			t.Errorf("%q: show %v, printed:\n%s", tc.document, config.ShowCost, output)
		}
	}
	for _, document := range []string{
		"comment:\n  show_cost: maybe\n",
		"comment:\n  show_cost: 1\n",
		"comment:\n  show_cost: \"false\"\n",
		"comment:\n  show_cost: \"x\\n::warning::forged\"\n",
		"comment:\n  show_cost: [false]\n",
	} {
		_, err := ParseConfig([]byte(document))
		if err == nil || err.Error() != "comment: comment.show_cost in costroid.yml must be true or false" {
			t.Errorf("%q: %v", document, err)
		}
	}
}

// TestPolicySentencesNameEachRule renders every result the pack can give for
// the rules a setting turns on, with amounts shown and withheld.
func TestPolicySentencesNameEachRule(t *testing.T) {
	results := []struct {
		result      policy.Result
		shown, held string
	}{
		{policy.Result{Rule: policy.RuleRunIncrease, Reason: policy.ReasonOverBaseline, LimitUSD: "0.5", CostUSD: "3", BaselineUSD: "2"},
			"Increase over baseline: USD 3 is more than USD 0.5 over the USD 2 median of earlier runs.",
			"Increase over baseline: the run is more than the allowed increase over the median of earlier runs."},
		{policy.Result{Rule: policy.RuleRunIncreasePercent, Reason: policy.ReasonWithinLimit, LimitPercent: "12.5", CostUSD: "2.2", BaselineUSD: "2"},
			"Percent increase over baseline: USD 2.2 is at most 12.5% over the USD 2 median of earlier runs.",
			"Percent increase over baseline: the run is at most 12.5% over the median of earlier runs."},
		{policy.Result{Rule: policy.RuleRunIncrease, Reason: policy.ReasonNoBaseline, LimitUSD: "0.5"},
			"Increase over baseline: no baseline of 10 or more earlier runs reached this check, so the rule did not run.", ""},
		{policy.Result{Rule: policy.RuleRunIncrease, Reason: policy.ReasonUnverifiedRows, LimitUSD: "0.5", CostUSD: "1", BaselineUSD: "2"},
			"Increase over baseline: unverified rows cannot satisfy a budget.", ""},
		{policy.Result{Rule: policy.RulePullRequestBudget, Reason: policy.ReasonOverLimit, LimitUSD: "10", CostUSD: "10.5"},
			"Pull request budget: USD 10.5 over this pull request's runs is over the USD 10 budget. The total adds every basis together, at list.",
			"Pull request budget: this pull request's runs are over the budget. The total adds every basis together, at list."},
		{policy.Result{Rule: policy.RulePullRequestBudget, Reason: policy.ReasonNoLifetime, LimitUSD: "10"},
			"Pull request budget: this check read no total for the pull request's earlier runs, so the rule did not run.", ""},
		{policy.Result{Rule: policy.RulePullRequestBudget, Reason: policy.ReasonRepeatPossible, LimitUSD: "10", CostUSD: "90"},
			"Pull request budget: the pull request's total may count a report twice, so it cannot show the budget broken or kept.", ""},
		{policy.Result{Rule: policy.RulePullRequestBudget, Reason: policy.ReasonOtherCurrency, LimitUSD: "10", CostUSD: "2"},
			"Pull request budget: rows in another currency are never held to a USD budget, so it cannot pass.", ""},
		{policy.Result{Rule: policy.RuleMonthlyBudget, Reason: policy.ReasonNoLedger, LimitUSD: "100"},
			"Monthly budget: the month to date comes only from a ledger, and this check has none, so the rule did not run.", ""},
		{policy.Result{Rule: policy.RuleModelAllowlist, Reason: policy.ReasonOutsideAllowlist, Models: []string{"gpt-5.5"}},
			"Model allowlist: the run used `gpt-5.5`, which is not on the list.", ""},
		{policy.Result{Rule: policy.RuleModelAllowlist, Reason: policy.ReasonOutsideAllowlist, Models: []string{"a", "b@c"}},
			"Model allowlist: the run used `a`, `not-shown`, which are not on the list.", ""},
		{policy.Result{Rule: policy.RuleModelAllowlist, Reason: policy.ReasonUnreadableModel},
			"Model allowlist: a row names no model this check can read, so the rule cannot pass.", ""},
		{policy.Result{Rule: policy.RuleModelAllowlist, Reason: policy.ReasonUnverifiedRows},
			"Model allowlist: unverified rows cannot show that every model was on the list.", ""},
		{policy.Result{Rule: policy.RuleModelAllowlist, Reason: policy.ReasonOnAllowlist},
			"Model allowlist: every row names a model on the list.", ""},
	}
	for _, tc := range results {
		withheld := tc.held
		if withheld == "" {
			withheld = tc.shown
		}
		verdict := policy.Verdict{Results: []policy.Result{tc.result}}
		if got := VerdictSentences(verdict, true)[0]; got != tc.shown {
			t.Errorf("shown:\n got %s\nwant %s", got, tc.shown)
		}
		if got := VerdictSentences(verdict.WithoutAmounts(), false)[0]; got != withheld {
			t.Errorf("withheld:\n got %s\nwant %s", got, withheld)
		}
	}
}
