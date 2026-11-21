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
// it was. A rule the warn-only period holds reads the same as one that fails,
// with no word of the period, and a run that breaks no rule is not said to be
// held.
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
		if !strings.HasPrefix(body, Marker+"\n**"+sentence+"**\n\n"+forkHeldLine(RequestNoun(AccountGitHub))+"\n\n"+uncheckedLine+"\n") {
			t.Errorf("%s: the fork's sentence is not the reason under the verdict sentence:\n%s", rule, body)
		}
		if strings.Contains(body, approvalPage) || strings.Contains(body, "Reason: `") {
			t.Errorf("%s: a held check asks for an approval, or keeps a sentence in the old form:\n%s", rule, body)
		}
	}
	states := goldenStates(t)
	inPeriod, enforced := states["warn-shown"].OnForkRows(), states["fail-shown"].OnForkRows()
	if got, want := VerdictSentence(inPeriod), VerdictSentence(enforced); got != want || strings.Contains(got, "warn-only") {
		t.Errorf("inside the warn-only period a fork's rows over a budget read\n%s\nand after it\n%s", got, want)
	}
	if body := readable(commentOf(t, inPeriod)); strings.Contains(strings.ToLower(body), "warn-only") || strings.Contains(body, "this fails the check") || strings.Contains(body, "enforce: true") ||
		!strings.Contains(body, "\n\n"+forkHeldLine(RequestNoun(AccountGitHub))+"\n\n") {
		t.Errorf("inside the warn-only period a fork's report names the period or what follows it, or leaves the fork's sentence out:\n%s", body)
	}
	for name, input := range map[string]Input{
		"a run within its budget": states["neutral-shown"],
		"no verdict":              realInput(t),
	} {
		if on := input.OnForkRows(); VerdictSentence(on) != VerdictSentence(input) || commentOf(t, on) != commentOf(t, input) {
			t.Errorf("%s: a check no rule was broken on reads as held:\n%s", name, VerdictSentence(on))
		}
	}
}

// TestOnForkRowsGivesAHeldSuccessNoSentence records the one state the hold
// has no words for: a verdict that passed every rule, held at neutral. Its
// sentence is the one a checked run gets, which is not true of a neutral
// check. No command reaches it, because a follow-up reads rows the fork's own
// job wrote, and such rows pass no rule: a test of the follow-up in
// cmd/costroid fails the day one does.
func TestOnForkRowsGivesAHeldSuccessNoSentence(t *testing.T) {
	passed := goldenStates(t)["pass-shown"]
	held := passed.OnForkRows()
	if passed.Conclusion() != policy.Success || held.Conclusion() != policy.Neutral {
		t.Fatalf("the state concludes %s, and held %s", passed.Conclusion(), held.Conclusion())
	}
	if got := VerdictSentence(held); got != VerdictSentence(passed) || strings.Contains(got, "fork") {
		t.Errorf("a held success now has a sentence of its own, so the follow-up's test and this one are due a state:\n%s", got)
	}
}
