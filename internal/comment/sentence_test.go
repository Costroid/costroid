package comment

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/reason"
)

// sentenceStates are the states a verdict sentence opens with. Each names
// what is true of the run, and none is a conclusion value. A pack rule's
// state names the rule, and a stopped pack's its reason.
var sentenceStates = regexp.MustCompile(`^((Within budget|Over budget|Within limit|Over limit|Model not on the list|Models on the list|No budget set|Not decided|No figure \([a-z_]+\)|No agent run)(, by the job's own figures|, warn-only|, demoted to a warning|, approved by [A-Za-z0-9-]+|, on a fork's own rows)?|(Pack rule failed|Pack rule warned)(, warn-only|, on a fork's own rows)? \([a-z][a-z0-9_]{0,62}\)|Pack stopped \((pack_bound|pack_result)\)): `)

// markup is every character that would make a sentence render as anything
// but its own text in a comment, or break a table.
const markup = "*`#[]<>|\n"

// firstLine is the first line a surface shows, after the comment's marker.
func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimPrefix(text, Marker+"\n"), "\n")
	return line
}

// TestVerdictSentenceIsOneStringOnEverySurface holds every golden state to one
// sentence: the check's title, and in bold the first line of the comment, of
// the check's summary and of the job summary. The sentence is plain text that
// opens with what is true of the run and ends as a sentence. A check an
// approval concludes again opens on its own title too, and that title is the
// sentence the report itself posts for the same approval: with the amount
// where amounts are shown, and with none where they are withheld.
func TestVerdictSentenceIsOneStringOnEverySurface(t *testing.T) {
	for name, input := range goldenStates(t) {
		sentence := VerdictSentence(input)
		if strings.ContainsAny(sentence, markup) || !sentenceStates.MatchString(sentence) || !strings.HasSuffix(sentence, ".") {
			t.Errorf("%s: the sentence is not plain text that opens with a state and ends as a sentence: %q", name, sentence)
		}
		for _, value := range []string{policy.Success, policy.Neutral, policy.Failure} {
			if strings.HasPrefix(sentence, value) || strings.Contains(sentence, "Verdict: "+value) {
				t.Errorf("%s: the sentence opens on the conclusion value %s: %q", name, value, sentence)
			}
		}
		r := renderState(t, input)
		check, err := Check(input)
		if err != nil {
			t.Fatal(err)
		}
		if check.Title != sentence {
			t.Errorf("%s: the check's title is %q, the sentence %q", name, check.Title, sentence)
		}
		for surface, text := range map[string]string{"comment": r.comment, "check's summary": check.Summary, "job summary": r.summary} {
			if got := firstLine(text); got != "**"+sentence+"**" {
				t.Errorf("%s: the %s opens on %q, not on the sentence %q", name, surface, got, sentence)
			}
			if strings.Count(text, "**"+sentence+"**") != 1 || strings.Contains(text, "Verdict: ") {
				t.Errorf("%s: the %s states the sentence more than once, or a conclusion as a heading:\n%s", name, surface, text)
			}
		}
	}
	states := goldenStates(t)
	// A rule a ledger demoted says so in the state, then that its limit was
	// passed, the record, when the demotion ends and that the check is
	// neutral, whole and with no amount, shown and withheld.
	for name, want := range map[string]string{
		"demoted-run-budget":              "Over budget, demoted to a warning: this run is over its run budget. Reviewers approved 7 of the last 20 pull requests it failed, so it warns until 2026-12-12 09:00 UTC and the check is neutral.",
		"demoted-run-budget-withheld":     "Over budget, demoted to a warning: this run is over its run budget. Reviewers approved 7 of the last 20 pull requests it failed, so it warns until 2026-12-12 09:00 UTC and the check is neutral. Amounts are withheld here.",
		"demoted-request-budget":          "Over budget, demoted to a warning: this pull request's runs are over the pull request budget. Reviewers approved 7 of the last 20 pull requests it failed, so it warns until 2026-12-12 09:00 UTC and the check is neutral.",
		"demoted-request-budget-withheld": "Over budget, demoted to a warning: this pull request's runs are over the pull request budget. Reviewers approved 7 of the last 20 pull requests it failed, so it warns until 2026-12-12 09:00 UTC and the check is neutral. Amounts are withheld here.",
	} {
		if got := VerdictSentence(states[name]); got != want {
			t.Errorf("%s: the sentence is\n%q\nwant\n%q", name, got, want)
		}
	}
	object, err := Result(states["fail-shown"])
	if err != nil {
		t.Fatal(err)
	}
	block, err := ParseResult(fenced(object))
	if err != nil {
		t.Fatal(err)
	}
	approval := &Approval{Approver: "octocat", HeadSHA: sampleHead, At: time.Date(2026, 11, 12, 9, 30, 0, 0, time.UTC)}
	for name, tc := range map[string]struct {
		approval *Approval
		amounts  Amounts
		state    string
		want     string
	}{
		"approved, amounts shown":       {approval, AmountsShown, "approved-failure", "Over budget, approved by octocat: this run cost USD 0.001619725 at list (List-equivalent, not billed), over the USD 0.001 run budget. The approval covers commit 9f2c6e1 and ends with a new push."},
		"approved, amounts off":         {approval, AmountsOff, "approved-failure-withheld", "Over budget, approved by octocat: this run is over its run budget, priced at list (List-equivalent, not billed). The approval covers commit 9f2c6e1 and ends with a new push. Amounts are withheld here."},
		"approved, amounts unsettled":   {approval, AmountsUnsettled, "approved-failure-withheld", "Over budget, approved by octocat: this run is over its run budget, priced at list (List-equivalent, not billed). The approval covers commit 9f2c6e1 and ends with a new push. Amounts are withheld here."},
		"no approval, amounts shown":    {nil, AmountsShown, "fail-shown", "Over budget: this run cost USD 0.001619725 at list (List-equivalent, not billed), over the USD 0.001 run budget."},
		"no approval, amounts withheld": {nil, AmountsOff, "fail-withheld", "Over budget: this run is over its run budget, priced at list (List-equivalent, not billed). Amounts are withheld here."},
	} {
		again, err := Reconclude(block, tc.approval, tc.amounts, exampleCheckRun)
		if err != nil {
			t.Fatal(err)
		}
		if again.Title != tc.want || firstLine(again.Summary) != "**"+tc.want+"**" || tc.want != VerdictSentence(states[tc.state]) {
			t.Errorf("%s: the check concluded again is titled %q, the report's own sentence is %q, and the summary opens\n%s", name, again.Title, VerdictSentence(states[tc.state]), again.Summary)
		}
		if tc.amounts != AmountsShown && amountInWords.MatchString(again.Title+again.Summary) {
			t.Errorf("%s: a check concluded again where amounts are withheld names one:\n%s", name, again.Summary)
		}
	}
}

// longAmount is the longest amount a row can state: 64 characters, 12 of them
// decimal places.
var longAmount = strings.Repeat("9", 51) + "." + strings.Repeat("9", 12)

// worstCase is a report whose figures, limits and names are each as long as
// the rows, costroid.yml and GitHub allow: figures past the table's cap in
// the longest basis, each the sum of rows at the longest amount, under a
// verdict of the result given.
func worstCase(t *testing.T, figures int, result policy.Result) Input {
	t.Helper()
	var rows []rowFields
	for index := range figures {
		for range 3 {
			fields := sampleFields()
			fields.currency = fmt.Sprintf("X%c%c", 'A'+index/26, 'A'+index%26)
			fields.basis, fields.cost = "list_equivalent", longAmount
			rows = append(rows, fields)
		}
	}
	input := realInput(t)
	input.Ledger = ledgerOf(t, rows...)
	conclusion := policy.Neutral
	if result.Outcome == policy.Fail {
		conclusion = policy.Failure
	}
	input.Verdict = &policy.Verdict{Conclusion: conclusion, Results: []policy.Result{result}, WarnOnly: policy.WarnOnly{Active: true, Ends: "2026-11-09T09:00:00Z"}}
	input.Approval = &Approval{Approver: strings.Repeat("a", 39), HeadSHA: sampleHead, At: time.Date(2026, 11, 12, 9, 30, 0, 0, time.UTC)}
	return input
}

// TestVerdictSentenceFitsTheStatusLimit holds the sentence to 255 characters
// in every golden state and in constructed worst cases: every rule and
// outcome, shown and withheld, over one figure and over more than the table
// holds, with the longest amounts, limits and login, on GitHub, on GitLab
// with its longer noun, and held at neutral on a fork's rows. A sentence past
// the limit is cut in a fixed order, and no cut drops the state or the first
// figure with its currency and basis.
func TestVerdictSentenceFitsTheStatusLimit(t *testing.T) {
	for name, input := range goldenStates(t) {
		if length := utf8.RuneCountInString(VerdictSentence(input)); length > StatusLimit {
			t.Errorf("%s: the sentence is %d characters", name, length)
		}
		if whole := input.telling().sentenceAt(cuts[0]); VerdictSentence(input) != whole {
			t.Errorf("%s: the title is a cut sentence:\n%s\nwhole, it is\n%s", name, VerdictSentence(input), whole)
		}
	}
	limit := strings.Repeat("7", 300) + ".123456789012"
	rules := []string{
		policy.RuleRunBudget, policy.RuleRunCostThreshold, policy.RuleRunIncrease, policy.RuleRunIncreasePercent, policy.RuleRunP95,
		policy.RulePullRequestBudget, policy.RuleMonthlyBudget, policy.RuleModelAllowlist, policy.RuleAttribution,
	}
	// A demotion states the most pull requests a window holds and the
	// longest instant.
	demotion := &policy.Demoted{Reason: policy.DemotedByOverrideRate, Approved: 20, Failing: 20, Until: "2026-12-31T23:59:59Z"}
	outcomes := []struct {
		outcome, reason string
		demoted         *policy.Demoted
	}{
		{policy.Fail, policy.ReasonOverLimit, nil}, {policy.Warn, policy.ReasonOverLimit, nil}, {policy.Pass, policy.ReasonWithinLimit, nil},
		{policy.Unproven, policy.ReasonUnverifiedRows, nil}, {policy.Unproven, policy.ReasonNoLedger, nil}, {policy.Unproven, policy.ReasonNoBaseline, nil},
		{policy.Warn, policy.ReasonOverLimit, demotion},
	}
	// A figure is a sum of rows, and so are a pull request's total and a
	// baseline's median and P95. Only a limit in costroid.yml has no longest
	// form. The P95 limit's factor is below 10000, with at most 12 fractional
	// digits.
	sum := decimal.MustParse(longAmount).Add(decimal.MustParse(longAmount)).Add(decimal.MustParse(longAmount)).String()
	runs := policy.MinP95Runs - 1
	checked := 0
	for _, rule := range rules {
		for _, outcome := range outcomes {
			for _, figures := range []int{1, 3, maxFigures + 3} {
				result := policy.Result{
					Rule: rule, Outcome: outcome.outcome, Reason: outcome.reason, LimitUSD: limit, CostUSD: sum, BaselineUSD: sum,
					LimitPercent: strings.Repeat("9", 40), Models: []string{"a", "b"}, P95USD: sum, LimitFactor: "9999.999999999999", BaselineRuns: &runs,
					Demoted: outcome.demoted,
				}
				shown := worstCase(t, figures, result)
				onGitLab, onFork := asGitLab(shown), shown.OnForkRows()
				for _, input := range []Input{shown, withheld(shown), onGitLab, withheld(onGitLab), onFork, withheld(onFork)} {
					sentence := VerdictSentence(input)
					checked++
					demoted := outcome.demoted != nil && policy.Demotable(rule) && !input.forkRows
					// A demoted rule's sentence is said whole, never cut, and
					// names no amount: the rule's own line gives them.
					if whole := input.telling().sentenceAt(cuts[0]); demoted && sentence != whole {
						t.Errorf("%s demoted over %d figures, amounts %d: the sentence is cut:\n%s\nwhole, it is\n%s", rule, figures, input.Amounts, sentence, whole)
					}
					if length := utf8.RuneCountInString(sentence); length > StatusLimit {
						t.Errorf("%s %s over %d figures, amounts %d: %d characters: %s", rule, outcome.outcome, figures, input.Amounts, length, sentence)
					}
					if !sentenceStates.MatchString(sentence) || strings.ContainsAny(sentence, markup) {
						t.Errorf("%s %s over %d figures: the cut dropped the state: %s", rule, outcome.outcome, figures, sentence)
					}
					first, basis := "XAA "+sum+" at list (List-equivalent, not billed)", "priced at list (List-equivalent, not billed)"
					if strings.Contains(sentence, " request's runs") {
						first, basis = "USD "+sum+" at list (every basis together)", "priced at list"
					}
					names := strings.Contains(sentence, first)
					switch {
					case demoted:
						names = strings.Contains(sentence, ", demoted to a warning: ") && !amountInWords.MatchString(sentence) &&
							strings.Contains(sentence, " and the check is neutral.")
					case input.Amounts != AmountsShown:
						names = strings.Contains(sentence, basis) && strings.HasSuffix(sentence, " Amounts are withheld here.") && !amountInWords.MatchString(sentence)
					}
					if !names {
						t.Errorf("%s %s over %d figures, amounts %d: the cut dropped the first figure, its basis or the withheld note: %s", rule, outcome.outcome, figures, input.Amounts, sentence)
					}
				}
			}
		}
	}
	if checked != len(rules)*len(outcomes)*3*6 {
		t.Fatalf("%d worst cases were built", checked)
	}
	checkPackWorstCases(t)

	// The order of the cuts, on one report: three figures and the closing
	// clause fit, a longer budget counts the later figures, and the longest
	// leaves the closing clause and then the budget out.
	within := policy.Result{Rule: policy.RuleRunBudget, Outcome: policy.Unproven, Reason: policy.ReasonUnverifiedRows, CostUSD: "1"}
	for _, tc := range []struct {
		name, limit string
		want        []string
		lost        []string
	}{
		{"it fits whole", "5", []string{"XAA 0.30 at list (Measured), XAB 0.30 at list (Measured) and XAC 0.30 at list (Measured) of the USD 5.00 run budget. Nothing outside the job checked the figures, so the check is neutral."}, nil},
		{"the later figures are counted", "5555555555.111111111111",
			[]string{"this run cost XAA 0.30 at list (Measured) and 2 more of the USD 5555555555.111111111111 run budget. Nothing outside the job checked the figures, so the check is neutral."}, []string{"XAB"}},
		{"the closing clause goes", strings.Repeat("5", 90), []string{"this run cost XAA 0.30 at list (Measured) and 2 more of the USD " + strings.Repeat("5", 90) + ".00 run budget."}, []string{"XAB", "Nothing outside"}},
		{"the budget goes", strings.Repeat("5", 200), []string{"Within budget, by the job's own figures: this run cost XAA 0.30 at list (Measured) and 2 more."}, []string{"XAB", "Nothing outside", "run budget", "555"}},
	} {
		var rows []rowFields
		for _, currency := range []string{"XAA", "XAB", "XAC"} {
			for range 3 {
				fields := sampleFields()
				fields.currency = currency
				rows = append(rows, fields)
			}
		}
		input := realInput(t)
		input.Ledger = ledgerOf(t, rows...)
		result := within
		result.LimitUSD = tc.limit
		input.Verdict = &policy.Verdict{Conclusion: policy.Neutral, Results: []policy.Result{result}}
		sentence := VerdictSentence(input)
		for _, want := range tc.want {
			if !strings.HasSuffix(sentence, want) || utf8.RuneCountInString(sentence) > StatusLimit {
				t.Errorf("%s: %d characters, and the sentence does not end %q:\n%s", tc.name, utf8.RuneCountInString(sentence), want, sentence)
			}
		}
		for _, lost := range tc.lost {
			if strings.Contains(sentence, lost) {
				t.Errorf("%s: the cut kept %q:\n%s", tc.name, lost, sentence)
			}
		}
	}
}

// TestVerdictSentenceNamesTheRuleThatDecides holds the sentence to one rule
// when several ran: the first, in the order the pack lists its rules, of the
// worst outcome the verdict holds. A rule that fails decides before one the
// warn-only period holds, that before one that could not pass, and that
// before one that passed. Every other rule's own line follows above the
// fold, and the rule the sentence states in full is not said twice. The
// attribution rule only warns, never decides alone, and its line is folded.
func TestVerdictSentenceNamesTheRuleThatDecides(t *testing.T) {
	budget := func(outcome, reason string) policy.Result {
		return policy.Result{Rule: policy.RuleRunBudget, Outcome: outcome, Reason: reason, LimitUSD: "5", CostUSD: realTotal}
	}
	limit := func(outcome, reason string) policy.Result {
		return policy.Result{Rule: policy.RuleRunCostThreshold, Outcome: outcome, Reason: reason, LimitUSD: "0.001", CostUSD: realTotal, HeldByWarnOnly: outcome == policy.Warn}
	}
	models := func(outcome, reason string) policy.Result {
		return policy.Result{Rule: policy.RuleModelAllowlist, Outcome: outcome, Reason: reason, Models: []string{"claude-haiku-5-5"}}
	}
	total := policy.Result{Rule: policy.RulePullRequestBudget, Outcome: policy.Unproven, Reason: policy.ReasonNoLifetime, LimitUSD: "10"}
	attribution := policy.Result{Rule: policy.RuleAttribution, Outcome: policy.Warn, Reason: policy.ReasonJoinableRows}
	runs := policy.MinP95Runs
	p95 := policy.Result{Rule: policy.RuleRunP95, Outcome: policy.Fail, Reason: policy.ReasonOverBaseline, CostUSD: realTotal, P95USD: "0.0006", LimitFactor: "1.5", BaselineRuns: &runs}
	increase := policy.Result{Rule: policy.RuleRunIncrease, Outcome: policy.Fail, Reason: policy.ReasonOverBaseline, LimitUSD: "0.0001", CostUSD: realTotal, BaselineUSD: "0.001"}
	overTotal := policy.Result{Rule: policy.RulePullRequestBudget, Outcome: policy.Fail, Reason: policy.ReasonOverLimit, LimitUSD: "0.001", CostUSD: realTotal}
	figure := "this run cost USD " + realTotal + " at list (List-equivalent, not billed)"
	for _, tc := range []struct {
		name       string
		conclusion string
		results    []policy.Result
		sentence   string
		body, gone []string
	}{
		{"a rule that fails decides before one that could not pass", policy.Failure,
			[]policy.Result{budget(policy.Unproven, policy.ReasonUnverifiedRows), limit(policy.Fail, policy.ReasonOverLimit)},
			"Over limit: " + figure + ", over the USD 0.001 run cost limit.",
			[]string{"Run budget: unchecked rows cannot satisfy a budget. The run cost USD " + realTotal + " of the USD 5.00 budget."}, []string{"Run cost limit: "}},
		{"of two rules that fail, the first the pack lists", policy.Failure,
			[]policy.Result{budget(policy.Fail, policy.ReasonOverLimit), limit(policy.Fail, policy.ReasonOverLimit), models(policy.Fail, policy.ReasonOutsideAllowlist)},
			"Over budget: " + figure + ", over the USD 5.00 run budget.",
			[]string{"Run cost limit: USD " + realTotal + " is over the USD 0.001 limit.", "Model allowlist: the run used `claude-haiku-5-5`, which is not on the list."}, []string{"Run budget: "}},
		{"a rule that fails decides before one the warn-only period holds", policy.Failure,
			[]policy.Result{limit(policy.Warn, policy.ReasonOverLimit), models(policy.Fail, policy.ReasonOutsideAllowlist)},
			"Model not on the list: " + figure + " and used a model that is not on the list.",
			[]string{"Run cost limit: USD " + realTotal + " is over the USD 0.001 limit.", "Model allowlist: the run used `claude-haiku-5-5`, which is not on the list."}, nil},
		{"a rule the warn-only period holds decides before one that could not pass", policy.Neutral,
			[]policy.Result{budget(policy.Unproven, policy.ReasonUnverifiedRows), limit(policy.Warn, policy.ReasonOverLimit)},
			"Over limit, warn-only: " + figure + ", over the USD 0.001 run cost limit. After the warn-only period this fails the check.",
			[]string{"Run budget: unchecked rows cannot satisfy a budget."}, []string{"Run cost limit: "}},
		{"a rule that could not pass decides before one that passed", policy.Neutral,
			[]policy.Result{budget(policy.Pass, policy.ReasonWithinLimit), total},
			"Not decided: the pull request budget could not be checked. This run cost USD " + realTotal + " at list (List-equivalent, not billed).",
			[]string{"Run budget: USD " + realTotal + " is within the USD 5.00 budget.", "Pull request budget: this check read no total for the pull request's earlier runs, so the rule did not run."}, nil},
		{"of two rules that could not pass, the first the pack lists", policy.Neutral,
			[]policy.Result{budget(policy.Unproven, policy.ReasonUnverifiedRows), total, attribution},
			"Within budget, by the job's own figures: " + figure + " of the USD 5.00 run budget. Nothing outside the job checked the figures, so the check is neutral.",
			[]string{"Pull request budget: this check read no total"}, []string{"Run budget: ", "Attribution: "}},
		{"of rules that fail, the P95 limit follows the increase rules", policy.Failure,
			[]policy.Result{increase, p95},
			"Over limit: " + figure + ", more than USD 0.0001 over the USD 0.001 median of earlier runs.",
			[]string{"P95 limit: USD " + realTotal + " is over 1.5 times the USD 0.0006 P95 of this agent's latest 20 runs."}, []string{"Increase over baseline: "}},
		{"of rules that fail, the P95 limit comes before the pull request budget and the model list", policy.Failure,
			[]policy.Result{p95, overTotal, models(policy.Fail, policy.ReasonOutsideAllowlist)},
			"Over limit: " + figure + ", over 1.5 times the USD 0.0006 P95 of this agent's latest 20 runs.",
			[]string{"Pull request budget: USD " + realTotal + " over this pull request's runs is over the USD 0.001 budget.", "Model allowlist: the run used `claude-haiku-5-5`, which is not on the list."}, []string{"P95 limit: "}},
		{"a P95 limit that did not run decides only after every rule that fails or warns", policy.Neutral,
			[]policy.Result{budget(policy.Pass, policy.ReasonWithinLimit), {Rule: policy.RuleRunP95, Outcome: policy.Unproven, Reason: policy.ReasonNoBaseline, LimitFactor: "1.5", BaselineRuns: new(7)}},
			"Not decided: the P95 limit needs 20 earlier runs of this agent and 7 reached the check, so the rule did not run. This run cost USD " + realTotal + " at list (List-equivalent, not billed).",
			[]string{"Run budget: USD " + realTotal + " is within the USD 5.00 budget."}, []string{"P95 limit: "}},
		{"the attribution rule never decides alone", policy.Neutral,
			[]policy.Result{attribution},
			"No budget set: " + figure + ". No rule decides this check.",
			nil, []string{"Attribution: "}},
	} {
		input := verdictOf(t, tc.conclusion, tc.results...)
		input.Budget = nil
		if got := VerdictSentence(input); got != tc.sentence {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.sentence)
		}
		above, _, _ := strings.Cut(commentOf(t, input), "### Costroid")
		for _, want := range tc.body {
			if !strings.Contains(above, want) {
				t.Errorf("%s: the body above the fold does not say %q:\n%s", tc.name, want, above)
			}
		}
		for _, gone := range tc.gone {
			if strings.Contains(above, gone) {
				t.Errorf("%s: the body says again what the sentence states, or states the attribution rule above the fold, %q:\n%s", tc.name, gone, above)
			}
		}
		if folded := commentOf(t, input); slices.ContainsFunc(tc.results, func(result policy.Result) bool { return result.Rule == policy.RuleAttribution }) && !strings.Contains(folded, "Attribution: 0 rows name a credential") {
			t.Errorf("%s: the fold does not hold the attribution rule's line:\n%s", tc.name, folded)
		}
	}
}

// TestNoFigureSentenceSaysNoRuleRanOnlyWhenNoneDid holds a report with no
// figures to what its policy did: with no rule set the sentence says no rule
// ran, and under a rule that read no rows it leaves that to the rule's own
// line, which follows it.
func TestNoFigureSentenceSaysNoRuleRanOnlyWhenNoneDid(t *testing.T) {
	empty := noFigures(NoRows, EmptyRowsFile)
	sentence := "No figure (empty_rows_file): the rows file holds no rows. Either the agent printed no usage or its job stopped before it wrote a row, and that job's log says which."
	for name, tc := range map[string]struct {
		verdict *policy.Verdict
		want    string
		line    string
	}{
		"no policy":           {nil, sentence + " No rule ran.", ""},
		"no rule set":         {&policy.Verdict{Conclusion: policy.Neutral}, sentence + " No rule ran.", ""},
		"a rule read no rows": {&policy.Verdict{Conclusion: policy.Neutral, Results: []policy.Result{{Rule: policy.RuleRunBudget, Outcome: policy.Unproven, Reason: policy.ReasonNoRows, LimitUSD: "5"}}}, sentence, "Run budget: this check read no rows, so it cannot pass."},
		"an earlier run is over": {&policy.Verdict{Conclusion: policy.Failure, Results: []policy.Result{{Rule: policy.RulePullRequestBudget, Outcome: policy.Fail, Reason: policy.ReasonOverLimit, LimitUSD: "10", CostUSD: "10.5"}}},
			"Over budget: this pull request's runs cost USD 10.50 at list (every basis together), over the USD 10.00 pull request budget.", sentence},
	} {
		input := empty
		input.Verdict = tc.verdict
		if got := VerdictSentence(input); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", name, got, tc.want)
		}
		body := commentOf(t, input)
		if tc.line != "" && !strings.Contains(body, "**"+tc.want+"**\n\n"+tc.line+"\n") {
			t.Errorf("%s: %q does not follow the sentence:\n%s", name, tc.line, body)
		}
		if strings.Contains(body, "| Cost |") || strings.Contains(body, "<summary>Rows</summary>") {
			t.Errorf("%s: a report with no figures shows a table or a fold of rows:\n%s", name, body)
		}
	}
}

// amountShown finds each amount a person reads: a currency code, then the
// digits and the decimal places after it.
var amountShown = regexp.MustCompile(`\b[A-Z]{3} (-?[0-9]+)((?:\.[0-9]+)?)`)

// TestAmountsShowTwoDecimalsAndEveryDigit holds every amount a person reads
// to at least two decimal places and to every digit of the exact string: a
// whole amount gains two zeros, one decimal place gains one, and nothing
// longer changes. Each figure of every golden state is in its comment with
// the digits its machine block states, and a token class that carries no
// price states no amount.
func TestAmountsShowTwoDecimalsAndEveryDigit(t *testing.T) {
	for exact, want := range map[string]string{
		"5": "5.00", "0": "0.00", "0.1": "0.10", "180.1": "180.10", "0.25": "0.25", "0.001619725": "0.001619725",
		"-3": "-3.00", "1234567903.128456789012": "1234567903.128456789012", "96413207": "96413207.00",
	} {
		got := places(exact)
		if got != want || !strings.HasPrefix(got, exact) || strings.Trim(strings.TrimPrefix(got, exact), ".0") != "" {
			t.Errorf("places(%q) = %q, want %q: only zeros may follow the exact string", exact, got, want)
		}
		if Money("USD", exact) != "USD "+want {
			t.Errorf("Money(USD, %q) = %q", exact, Money("USD", exact))
		}
	}
	for name, input := range goldenStates(t) {
		r := renderState(t, input)
		check, err := Check(input)
		if err != nil {
			t.Fatal(err)
		}
		for surface, text := range map[string]string{"comment": readable(r.comment), "check's summary": check.Summary, "job summary": r.summary} {
			for _, amount := range amountShown.FindAllStringSubmatch(text, -1) {
				if len(amount[2]) < 1+minPlaces {
					t.Errorf("%s: the %s shows %q with fewer than %d decimal places", name, surface, amount[0], minPlaces)
				}
			}
			if strings.Contains(text, "| USD 0.00 |") || strings.Contains(text, "| USD 0 |") {
				t.Errorf("%s: the %s prices a token class that carries no price:\n%s", name, surface, text)
			}
		}
		if input.Amounts != AmountsShown {
			continue
		}
		for _, figure := range input.Ledger.Figures {
			exact := figure.Cost.String()
			if !strings.Contains(r.comment, `"list_cost": "`+exact+`"`) {
				t.Errorf("%s: the machine block lost the exact amount %s", name, exact)
			}
			if shown := figure.Currency + " " + places(exact); !strings.Contains(readable(r.comment), "| "+shown) {
				t.Errorf("%s: the comment's table does not show %s", name, shown)
			}
		}
	}
}

// TestPolicyPageQuotesSentencesTheGoldensHold reads the section of
// docs/policy.md that says what the check says. Each sentence it quotes is a
// golden state's, word for word, and its table names the opening of every
// golden state's sentence.
func TestPolicyPageQuotesSentencesTheGoldensHold(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("..", "..", "docs", "policy.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, section, found := strings.Cut(string(page), "\n## What the check says\n")
	section, _, _ = strings.Cut(section, "\n## ")
	_, quoted, fenced := strings.Cut(section, "```text\n")
	quoted, table, closed := strings.Cut(quoted, "\n```\n")
	if !found || !fenced || !closed {
		t.Fatal("docs/policy.md has no section that quotes the check's sentences")
	}
	sentences := map[string]bool{}
	for _, input := range goldenStates(t) {
		sentences[VerdictSentence(input)] = true
	}
	for line := range strings.SplitSeq(quoted, "\n") {
		if !sentences[line] {
			t.Errorf("docs/policy.md quotes a sentence no golden state holds: %q", line)
		}
	}
	var openings []string
	for row := range strings.SplitSeq(table, "\n") {
		cell, _, isRow := strings.Cut(strings.TrimPrefix(row, "| "), " |")
		if !strings.HasPrefix(row, "| `") || !isRow {
			continue
		}
		for _, opening := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(cell, -1) {
			openings = append(openings, opening[1])
		}
	}
	for sentence := range sentences {
		if !slices.ContainsFunc(openings, func(opening string) bool { return strings.HasPrefix(sentence, opening) }) {
			t.Errorf("the table of docs/policy.md names no opening of %q: %q", sentence, openings)
		}
	}
	for _, opening := range openings {
		if !slices.ContainsFunc(slices.Collect(maps.Keys(sentences)), func(sentence string) bool { return strings.HasPrefix(sentence, opening) }) {
			t.Errorf("docs/policy.md names the opening %q, and no golden state's sentence opens with it", opening)
		}
	}
}

// TestApprovalPageQuotesTheApprovedSentence holds the title docs/approval.md
// quotes for an approved check to the golden state's sentence, word for word.
func TestApprovalPageQuotesTheApprovedSentence(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("..", "..", "docs", "approval.md"))
	if err != nil {
		t.Fatal(err)
	}
	if quoted := "\n```text\n" + VerdictSentence(goldenStates(t)["approved-failure"]) + "\n```\n"; !strings.Contains(string(page), quoted) {
		t.Errorf("docs/approval.md does not quote the approved check's title:%s", quoted)
	}
}

// TestTroubleshootingListsEveryNoFigureCode holds docs/troubleshooting.md to
// the codes a sentence with no figure can carry: each reason a report gives,
// each reason a run states and the refused artifact's, and no other.
func TestTroubleshootingListsEveryNoFigureCode(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("..", "..", "docs", "troubleshooting.md"))
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, match := range regexp.MustCompile("`No figure \\(([a-z_]+)\\)`").FindAllStringSubmatch(string(page), -1) {
		listed[match[1]] = true
	}
	carried := map[string]bool{}
	for _, why := range []Reason{NoRowsFile, EmptyRowsFile, NotAFile, UnreadRowsFile, InvalidRow, ForkNoRows} {
		carried[string(why)] = true
	}
	for _, why := range []reason.Reason{reason.NoUsage, reason.ConcurrentRun, reason.ReaderRefused, reason.AgentError, ArtifactRefused} {
		carried[string(why)] = true
	}
	for code := range carried {
		input := noFigures(Unreadable, Reason(code))
		if why := reason.Reason(code); why.Sentence() != "" {
			input = emptyRun(why)
		}
		if code == string(ArtifactRefused) {
			input = noFigures(Unreadable, NoRowsFile)
			input.Refused = &RefusedArtifact{Count: 1}
		}
		if sentence := VerdictSentence(input); !strings.HasPrefix(sentence, "No figure ("+code+"): ") {
			t.Errorf("the sentence for %s opens %q", code, sentence)
		}
		if !listed[code] {
			t.Errorf("docs/troubleshooting.md has no row for No figure (%s)", code)
		}
	}
	for code := range listed {
		if !carried[code] {
			t.Errorf("docs/troubleshooting.md lists No figure (%s), which no sentence carries", code)
		}
	}
	if !strings.Contains(string(page), "| `No agent run` |") || !strings.HasPrefix(VerdictSentence(noFigures(NoRows, AgentSkipped)), "No agent run: ") {
		t.Error("docs/troubleshooting.md has no row for a skipped agent, or its sentence opens otherwise")
	}
}

// TestRecordedRunSentenceNamesTheRecordedRun holds a report on a recorded
// run's rows to a sentence that says whose cost it states, in every state it
// can be in: under a budget, over one, with no rule, with a costroid.yml that
// is not valid, and with amounts withheld. No such sentence says "this run".
func TestRecordedRunSentenceNamesTheRecordedRun(t *testing.T) {
	states := goldenStates(t)
	figure := "USD " + realTotal + " at list (List-equivalent, not billed)"
	for name, tc := range map[string]struct {
		input Input
		want  string
	}{
		"within the budget": {states["neutral-shown"], "Within budget, by the job's own figures: the recorded run cost " + figure + " of the USD 5.00 run budget."},
		"over the budget":   {states["fail-shown"], "Over budget: the recorded run cost " + figure + ", over the USD 0.001 run budget."},
		"no rule":           {realInput(t), "No budget set: the recorded run cost " + figure + ". No rule decides this check."},
		"not decided":       {states["config-unread"], "Not decided: costroid.yml could not be read, so no rule ran. The recorded run cost " + figure + "."},
		"withheld":          {states["fail-withheld"], "Over budget: the recorded run is over its run budget, priced at list (List-equivalent, not billed). Amounts are withheld here."},
		"no rule, withheld": {withheld(realInput(t)), "No budget set: the recorded run was priced at list (List-equivalent, not billed). No rule decides this check. Amounts are withheld here."},
	} {
		own := VerdictSentence(tc.input)
		tc.input.Recorded = &Recorded{RunID: 42368537950, Date: "2026-11-07"}
		sentence := VerdictSentence(tc.input)
		if !strings.HasPrefix(sentence, tc.want) || strings.Contains(strings.ToLower(sentence), "this run") {
			t.Errorf("%s:\n got %s\nwant %s", name, sentence, tc.want)
		}
		if !strings.Contains(strings.ToLower(own), "this run") || strings.Contains(own, "recorded") {
			t.Errorf("%s: a report on the pull request's own rows reads %q", name, own)
		}
		if body := commentOf(t, tc.input); !strings.HasPrefix(body, Marker+"\n**"+sentence+"**\n\nThese figures are a recorded run's, workflow run 42368537950 on 2026-11-07, not this pull request's.\n") {
			t.Errorf("%s: the recorded line does not stand under the sentence:\n%s", name, body)
		}
	}
}

// longestPackRule is the longest id a pack's rule takes: a letter and 62
// more characters.
var longestPackRule = "a" + strings.Repeat("z", 62)

// checkPackWorstCases holds every sentence of a repository's own pack to the
// status limit, as TestVerdictSentenceFitsTheStatusLimit holds Costroid's: a
// pack rule that fails, that the warn-only period holds and that warns, each
// named by the longest rule id, and the pack not valid at a long line, not
// read, past its bound and with an answer outside its words, enforced and
// held, over one figure and more than the table holds at the longest amounts,
// shown and withheld, on GitHub, on GitLab and on a fork's rows.
func checkPackWorstCases(t *testing.T) {
	t.Helper()
	results := []policy.PackResult{
		{Rule: longestPackRule, Outcome: policy.PackFail, Reason: "r"},
		{Rule: longestPackRule, Outcome: policy.PackWarn, Reason: "r", HeldByWarnOnly: true},
		{Rule: longestPackRule, Outcome: policy.PackWarn, Reason: "r"},
	}
	faults := []policy.Result{
		{Rule: policy.RuleCustomerPack, Outcome: policy.Unproven, Reason: policy.ReasonPackInvalid, Line: 999999999},
		{Rule: policy.RuleCustomerPack, Outcome: policy.Unproven, Reason: policy.ReasonPackUnread},
		{Rule: policy.RuleCustomerPack, Outcome: policy.Fail, Reason: policy.ReasonPackBound},
		{Rule: policy.RuleCustomerPack, Outcome: policy.Warn, Reason: policy.ReasonPackBound, HeldByWarnOnly: true},
		{Rule: policy.RuleCustomerPack, Outcome: policy.Fail, Reason: policy.ReasonPackResult},
		{Rule: policy.RuleCustomerPack, Outcome: policy.Warn, Reason: policy.ReasonPackResult, HeldByWarnOnly: true},
	}
	var verdicts []policy.Verdict
	for _, result := range results {
		verdicts = append(verdicts, policy.Verdict{Results: []policy.Result{}, Pack: &policy.PackRun{Path: ".costroid/policy.rego", Results: []policy.PackResult{result}}})
	}
	for _, result := range faults {
		verdicts = append(verdicts, policy.Verdict{Results: []policy.Result{result}, Pack: &policy.PackRun{Path: ".costroid/policy.rego", Results: []policy.PackResult{}}})
	}
	opening := regexp.MustCompile(`^(Pack rule failed|Pack rule warned|Pack stopped|Not decided)`)
	checked := 0
	for _, verdict := range verdicts {
		for _, figures := range []int{1, 3, maxFigures + 3} {
			shown := worstCase(t, figures, policy.Result{})
			held := verdict
			held.ConfigFromBase, held.WarnOnly = true, shown.Verdict.WarnOnly
			shown.Verdict = &held
			onGitLab, onFork := asGitLab(shown), shown.OnForkRows()
			for _, input := range []Input{shown, withheld(shown), onGitLab, withheld(onGitLab), onFork, withheld(onFork)} {
				sentence := VerdictSentence(input)
				checked++
				if length := utf8.RuneCountInString(sentence); length > StatusLimit {
					t.Errorf("%+v over %d figures, amounts %d: %d characters: %s", verdict.Results, figures, input.Amounts, length, sentence)
				}
				if !sentenceStates.MatchString(sentence) || !opening.MatchString(sentence) || strings.ContainsAny(sentence, markup) {
					t.Errorf("%+v over %d figures: the cut dropped the state: %s", verdict.Results, figures, sentence)
				}
				names := strings.Contains(sentence, "XAA ")
				if input.Amounts != AmountsShown {
					names = strings.Contains(sentence, "priced at list (List-equivalent, not billed)") && strings.HasSuffix(sentence, " Amounts are withheld here.") && !amountInWords.MatchString(sentence)
				}
				if !names {
					t.Errorf("%+v over %d figures, amounts %d: the cut dropped the first figure, its basis or the withheld note: %s", verdict.Results, figures, input.Amounts, sentence)
				}
			}
		}
	}
	if checked != len(verdicts)*3*6 {
		t.Fatalf("%d worst cases of a pack were built", checked)
	}
}
