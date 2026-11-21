package comment

import (
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/policy"
)

// TestOnForkRowsHoldsEachRulesFailureAtNeutral fails each rule in turn on a
// fork's own rows. The check concludes neutral, the sentence opens on the
// rule's own noun and says the rows are a fork's and why the check is neutral,
// the fork's sentence is the reason under it, and no next step asks for an
// approval a fork's check does not need. The verdict it was given is left as
// it was, and a verdict that is neutral already is not said to be held.
func TestOnForkRowsHoldsEachRulesFailureAtNeutral(t *testing.T) {
	for rule, opening := range map[string]string{
		policy.RuleRunBudget:          "Over budget",
		policy.RuleRunCostThreshold:   "Over limit",
		policy.RuleRunIncrease:        "Over limit",
		policy.RuleRunIncreasePercent: "Over limit",
		policy.RulePullRequestBudget:  "Over budget",
		policy.RuleMonthlyBudget:      "Over budget",
		policy.RuleModelAllowlist:     "Model not on the list",
	} {
		failed := verdictOf(t, policy.Failure, policy.Result{
			Rule: rule, Outcome: policy.Fail, Reason: policy.ReasonOverLimit,
			LimitUSD: "0.001", CostUSD: realTotal, BaselineUSD: "0.001", Models: []string{"claude-haiku-5-5"},
		})
		held := failed.OnForkRows()
		if held.Conclusion() != policy.Neutral || failed.Conclusion() != policy.Failure || failed.Verdict.Conclusion != policy.Failure {
			t.Errorf("%s: held, the check concludes %s, and the verdict it was given now concludes %s", rule, held.Conclusion(), failed.Verdict.Conclusion)
		}
		sentence := VerdictSentence(held)
		if !strings.HasPrefix(sentence, opening+", on a fork's own rows: ") || !strings.HasSuffix(sentence, ". "+forkClosing) {
			t.Errorf("%s: held, the sentence reads %q", rule, sentence)
		}
		if enforced := VerdictSentence(failed); !strings.HasPrefix(enforced, opening+": ") || strings.Contains(enforced, "fork") {
			t.Errorf("%s: enforced, the sentence reads %q", rule, enforced)
		}
		body := commentOf(t, held)
		if !strings.HasPrefix(body, Marker+"\n**"+sentence+"**\n\n"+forkHeldLine(requestNoun(AccountGitHub))+"\n\n"+uncheckedLine+"\n") {
			t.Errorf("%s: the fork's sentence is not the reason under the verdict sentence:\n%s", rule, body)
		}
		if strings.Contains(body, approvalPage) || strings.Contains(body, "Reason: `") {
			t.Errorf("%s: a held check asks for an approval, or keeps a sentence in the old form:\n%s", rule, body)
		}
	}
	for name, input := range map[string]Input{
		"a rule the warn-only period holds": goldenStates(t)["warn-shown"],
		"a run within its budget":           goldenStates(t)["neutral-shown"],
		"no verdict":                        realInput(t),
	} {
		if on := input.OnForkRows(); VerdictSentence(on) != VerdictSentence(input) || commentOf(t, on) != commentOf(t, input) {
			t.Errorf("%s: a check that was neutral already reads as held:\n%s", name, VerdictSentence(on))
		}
	}
}
