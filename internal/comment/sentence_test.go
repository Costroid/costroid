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
// what is true of the run, and none is a conclusion value.
var sentenceStates = regexp.MustCompile(`^(Within budget|Over budget|Within limit|Over limit|Model not on the list|Models on the list|No budget set|Not decided|No figure \([a-z_]+\)|No agent run)(, by the job's own figures|, warn-only|, approved by [A-Za-z0-9-]+)?: `)

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
// approval concludes again opens on its own title too.
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
	failed := goldenStates(t)["fail-shown"]
	object, err := Result(failed)
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
		want     string
	}{
		"approved":    {approval, "Over budget, approved by octocat: this run is over its run budget, priced at list (List-equivalent, not billed). The approval covers commit 9f2c6e1 and ends with a new push. Amounts are withheld here."},
		"no approval": {nil, "Over budget: this run is over its run budget, priced at list (List-equivalent, not billed). Amounts are withheld here."},
	} {
		again, err := Reconclude(block, tc.approval, exampleCheckRun)
		if err != nil {
			t.Fatal(err)
		}
		if again.Title != tc.want || firstLine(again.Summary) != "**"+tc.want+"**" || amountInWords.MatchString(again.Title+again.Summary) {
			t.Errorf("%s: the check concluded again is titled %q and opens\n%s", name, again.Title, again.Summary)
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
// holds, with the longest amounts, limits and login. A sentence past the
// limit is cut in a fixed order, and no cut drops the state or the first
// figure with its currency and basis.
func TestVerdictSentenceFitsTheStatusLimit(t *testing.T) {
	for name, input := range goldenStates(t) {
		if length := utf8.RuneCountInString(VerdictSentence(input)); length > StatusLimit {
			t.Errorf("%s: the sentence is %d characters", name, length)
		}
	}
	limit := strings.Repeat("7", 300) + ".123456789012"
	rules := []string{
		policy.RuleRunBudget, policy.RuleRunCostThreshold, policy.RuleRunIncrease, policy.RuleRunIncreasePercent,
		policy.RulePullRequestBudget, policy.RuleMonthlyBudget, policy.RuleModelAllowlist, policy.RuleAttribution,
	}
	outcomes := []struct{ outcome, reason string }{
		{policy.Fail, policy.ReasonOverLimit}, {policy.Warn, policy.ReasonOverLimit}, {policy.Pass, policy.ReasonWithinLimit},
		{policy.Unproven, policy.ReasonUnverifiedRows}, {policy.Unproven, policy.ReasonNoLedger},
	}
	// A figure is a sum of rows, and so are a pull request's total and a
	// baseline's median. Only a limit in costroid.yml has no longest form.
	sum := decimal.MustParse(longAmount).Add(decimal.MustParse(longAmount)).Add(decimal.MustParse(longAmount)).String()
	checked := 0
	for _, rule := range rules {
		for _, outcome := range outcomes {
			for _, figures := range []int{1, 3, maxFigures + 3} {
				result := policy.Result{Rule: rule, Outcome: outcome.outcome, Reason: outcome.reason, LimitUSD: limit, CostUSD: sum, BaselineUSD: sum, LimitPercent: strings.Repeat("9", 40), Models: []string{"a", "b"}}
				shown := worstCase(t, figures, result)
				for _, input := range []Input{shown, withheld(shown)} {
					sentence := VerdictSentence(input)
					checked++
					if length := utf8.RuneCountInString(sentence); length > StatusLimit {
						t.Errorf("%s %s over %d figures, amounts %d: %d characters: %s", rule, outcome.outcome, figures, input.Amounts, length, sentence)
					}
					if !sentenceStates.MatchString(sentence) || strings.ContainsAny(sentence, markup) {
						t.Errorf("%s %s over %d figures: the cut dropped the state: %s", rule, outcome.outcome, figures, sentence)
					}
					first, basis := "XAA "+sum+" at list (List-equivalent, not billed)", "priced at list (List-equivalent, not billed)"
					if strings.Contains(sentence, "this pull request's runs") {
						first, basis = "USD "+sum+" at list (every basis together)", "priced at list"
					}
					names := strings.Contains(sentence, first)
					if input.Amounts != AmountsShown {
						names = strings.Contains(sentence, basis) && strings.HasSuffix(sentence, " Amounts are withheld here.") && !amountInWords.MatchString(sentence)
					}
					if !names {
						t.Errorf("%s %s over %d figures, amounts %d: the cut dropped the first figure, its basis or the withheld note: %s", rule, outcome.outcome, figures, input.Amounts, sentence)
					}
				}
			}
		}
	}
	if checked != len(rules)*len(outcomes)*3*2 {
		t.Fatalf("%d worst cases were built", checked)
	}

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
// fold, and the rule the sentence states in full is not said twice.
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
			[]string{"Pull request budget: this check read no total", "Attribution: 0 rows name a credential"}, []string{"Run budget: "}},
		{"the attribution rule never decides alone", policy.Neutral,
			[]policy.Result{attribution},
			"No budget set: " + figure + ". No rule decides this check.",
			[]string{"Attribution: 0 rows name a credential"}, nil},
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
				t.Errorf("%s: the body says again what the sentence states, %q:\n%s", tc.name, gone, above)
			}
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
		if money("USD", exact) != "USD "+want {
			t.Errorf("money(USD, %q) = %q", exact, money("USD", exact))
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
