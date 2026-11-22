package comment

import (
	"maps"
	"slices"
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
		if on := input.OnForkRows(); VerdictSentence(on) != VerdictSentence(input) || visiblePart(commentOf(t, on)) != visiblePart(commentOf(t, input)) {
			t.Errorf("%s: a check no rule was broken on reads as held:\n%s", name, VerdictSentence(on))
		}
	}
}

// TestAForksRowsWithinBudgetFoldWhatIsTrueOfAFork reports a fork's own rows
// within a budget, inside the warn-only period, after it and with amounts
// withheld. Under the fold, once, the comment says what a run over the budget
// would conclude on a fork, in the words of a held check's title and in the
// conditional the fold uses off a fork: the check would stay neutral. It is
// the same sentence inside the period and after it, and never says that the
// rule would fail, or would warn until the period ends. That line is all that differs from the same rows off a
// fork: the title, the machine block and the outputs are the same, and rows
// that are no fork's keep the line they had. The golden state holds it.
func TestAForksRowsWithinBudgetFoldWhatIsTrueOfAFork(t *testing.T) {
	for _, words := range []string{"Over budget, the check would stay neutral", "a fork's rows are the fork's own claim"} {
		if !strings.Contains(forkOverBudget, words) || !strings.Contains(forkClosing, "A fork's rows are the fork's own claim") {
			t.Errorf("the fold's line does not say %q, in the words a held check's title closes on: %s", words, forkOverBudget)
		}
	}
	inside := neutralOnAgentRows(t)
	after := withVerdict(t, goldenVerdict(t, underBudget, policy.Unverified, afterPeriod))
	for name, tc := range map[string]struct {
		input Input
		off   string
	}{
		"inside the warn-only period": {inside, "Over budget, the rule would warn until the warn-only period ends, then fail."},
		"after the warn-only period":  {after, "Over budget, the rule would fail."},
		"amounts withheld":            {withheld(inside), "Over budget, the rule would warn until the warn-only period ends, then fail."},
	} {
		fork := tc.input.OnForkRows()
		if fork.Conclusion() != policy.Neutral {
			t.Fatalf("%s: a fork's rows within the budget conclude %s", name, fork.Conclusion())
		}
		on, off := renderState(t, fork), renderState(t, tc.input)
		for file, body := range map[string]string{goldenComment: on.comment, goldenSummary: on.summary, goldenCheck: on.check} {
			if strings.Contains(body, "would fail") || strings.Contains(body, "then fail") || strings.Contains(body, "period ends") {
				t.Errorf("%s: %s says a rule would fail on a fork's rows:\n%s", name, file, body)
			}
		}
		if strings.Count(on.comment, forkOverBudget) != 1 || strings.Contains(visiblePart(on.comment), forkOverBudget) {
			t.Errorf("%s: the fold does not state %q once:\n%s", name, forkOverBudget, on.comment)
		}
		if strings.Contains(off.comment, forkOverBudget) || strings.Count(off.comment, tc.off) != 1 {
			t.Errorf("%s: rows that are no fork's do not keep the line %q:\n%s", name, tc.off, off.comment)
		}
		for file, pair := range map[string][2]string{goldenComment: {on.comment, off.comment}, goldenSummary: {on.summary, off.summary}, goldenCheck: {on.check, off.check}} {
			if strings.ReplaceAll(pair[1], tc.off, forkOverBudget) != pair[0] {
				t.Errorf("%s: %s differs from the same rows off a fork in more than the fold's line:\n%s", name, file, pair[0])
			}
		}
		if strings.Contains(on.check, forkOverBudget) != strings.Contains(off.check, tc.off) || strings.Contains(on.summary, forkOverBudget) != strings.Contains(off.summary, tc.off) {
			t.Errorf("%s: the line stands in a file of the fork's report and not in the other report's, or the reverse", name)
		}
		if VerdictSentence(fork) != VerdictSentence(tc.input) || !slices.Equal(on.outputs, off.outputs) || !maps.Equal(machineBlocks(t, fork), machineBlocks(t, tc.input)) {
			t.Errorf("%s: a fork's rows within the budget move the title, the machine block or the outputs", name)
		}
	}
	golden, recorded := goldenStates(t)["fork-within-budget"], goldenStates(t)["neutral-shown"]
	if !golden.forkRows || strings.Count(commentOf(t, golden), forkOverBudget) != 1 || commentOf(t, golden) == commentOf(t, recorded) {
		t.Errorf("no golden state holds the fold's line of a fork's rows within a budget")
	}
	if held := commentOf(t, goldenStates(t)["fork-failure-held"]); strings.Contains(held, forkOverBudget) {
		t.Errorf("a fork's rows over the budget fold what a run over it would conclude, beside the title that says it:\n%s", held)
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
