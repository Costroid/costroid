package comment

import (
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/policy"
)

// amountForm is one amount as a person reads it: a currency code, then its
// digits.
const amountForm = `[A-Z]{3} -?[0-9]+(?:\.[0-9]+)?`

var (
	amountRead = regexp.MustCompile(`\b` + amountForm)
	codeSpan   = regexp.MustCompile("`[^`\n]*`")
)

// basisNames are the words that name a basis after an amount: each label
// whole, and the first word of each, which a sum over several bases uses.
func basisNames() (whole, first string) {
	var labels, words []string
	for _, basis := range basisOrder {
		labels = append(labels, regexp.QuoteMeta(basisLabels[basis]))
		word, _, _ := strings.Cut(basisLabels[basis], ",")
		words = append(words, regexp.QuoteMeta(word))
	}
	return strings.Join(labels, "|"), strings.Join(words, "|")
}

// basisForm is one way a sentence gives an amount its basis: the amount and
// the words beside it that say what it was computed from, or what kind of
// amount it is.
type basisForm struct {
	kind string
	form *regexp.Regexp
}

// basisForms are every way a sentence of the comment gives an amount its
// basis. The list is closed: a sentence that states an amount in any other
// way fails TestEveryAmountStandsBesideItsBasis until its form is added here,
// which is where a reader of the change sees it.
func basisForms() []basisForm {
	whole, first := basisNames()
	var words []string
	for _, basis := range basisOrder {
		words = append(words, regexp.QuoteMeta(basisWords[basis]))
	}
	form := func(kind, pattern string) basisForm {
		return basisForm{kind, regexp.MustCompile(strings.ReplaceAll(pattern, "AMOUNT", amountForm))}
	}
	return []basisForm{
		// A cost: at list, then its basis, or the bases it adds, in brackets.
		form("cost", `\bAMOUNT at list \((?:`+whole+`|(?:`+first+`)(?:, (?:`+first+`))* and (?:`+first+`) together|every basis together)\)`),
		// A baseline: at list, then which runs it is the P95 or the median of.
		form("baseline", `\bAMOUNT at list (?:P95 of this agent's latest [0-9]+ runs|median of earlier runs)`),
		// An amount a person set, named by what it is: a budget, a limit or a
		// cap. It is no cost, so it has no cost basis.
		form("set", `\bAMOUNT (?:(?:run|pull request|merge request|monthly) budget|run cost limit|budget|limit|cap)\b`),
		form("set", `\bCap: AMOUNT`),
		form("set", `\b(?:Run budget|Run cost limit): AMOUNT, from `),
		form("set", `\b(?:at most|more than) AMOUNT over the `),
		// An infrastructure figure: its basis stands before it.
		form("infrastructure", `\bInfrastructure, (?:`+strings.Join(words, "|")+`): AMOUNT for `),
		// A figure a source states about itself, which Costroid did not compute.
		form("stated", `\bstated AMOUNT `),
	}
}

// differenceForms are how far a cost stands from a cap. Each is based only in
// a sentence that states the cost with its basis.
var differenceForms = []*regexp.Regexp{
	regexp.MustCompile(`\b` + amountForm + ` over its `),
	regexp.MustCompile(`\b` + amountForm + `(?: and [0-9]+ tokens)? (?:under|over) the cap\b`),
}

// pricedRow marks a row of a figure's arithmetic: a price from the catalog,
// beside the price id that names it and the tokens it was multiplied by.
var pricedRow = regexp.MustCompile(`\| ` + amountForm + ` per (?:million|[0-9]+) tokens \|`)

// unbasedAmounts lists every amount in text a person reads that states no
// basis: in a sentence or a fold's label, none of basisForms beside it, and
// in a table's row, no cell that names its basis and no unit price it was
// computed from. Code spans and fenced blocks are left out: they are data,
// not sentences.
func unbasedAmounts(text string) []string {
	whole, _ := basisNames()
	var words []string
	for _, basis := range basisOrder {
		words = append(words, regexp.QuoteMeta(capital(basisWords[basis])))
	}
	basisCell := regexp.MustCompile(`\| (?:` + whole + `|` + strings.Join(words, "|") + `) \|`)
	forms := basisForms()
	var unbased []string
	fenced := false
	for line := range strings.SplitSeq(text, "\n") {
		if strings.HasPrefix(line, "```") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		line = codeSpan.ReplaceAllString(strings.Trim(line, "*"), "`x`")
		if !amountRead.MatchString(line) {
			continue
		}
		if strings.HasPrefix(line, "|") {
			if !basisCell.MatchString(line) && !pricedRow.MatchString(line) {
				unbased = append(unbased, line)
			}
			continue
		}
		for sentence := range strings.SplitSeq(line, ". ") {
			rest, cost := sentence, false
			for _, known := range forms {
				if known.form.MatchString(rest) {
					cost = cost || known.kind == "cost"
					rest = known.form.ReplaceAllString(rest, "~")
				}
			}
			if cost {
				for _, difference := range differenceForms {
					rest = difference.ReplaceAllString(rest, "~")
				}
			}
			if amountRead.MatchString(rest) {
				unbased = append(unbased, sentence)
			}
		}
	}
	return unbased
}

// TestEveryAmountStandsBesideItsBasis walks every golden state as a person
// reads it, the comment, the check's summary and the job summary, which
// TestGolden holds to the golden files, and then a line of every rule no
// golden holds. It fails on an amount whose basis is not stated in its own
// sentence, or in its own row of a table. It tells by a closed list of forms,
// basisForms: a cost says at list and names its basis or the bases it adds, a
// baseline says at list and which runs it is the P95 or the median of, an
// amount a person set is named a budget, a limit or a cap, a distance from a
// cap stands in the sentence that states the cost, and a table's row holds a
// basis cell or the unit price the amount was computed from. It lets no
// amount stand without one.
func TestEveryAmountStandsBesideItsBasis(t *testing.T) {
	walked := 0
	for name, input := range goldenStates(t) {
		for file, text := range renderState(t, input).files() {
			if file == goldenOutputs {
				continue
			}
			walked += len(amountRead.FindAllString(text, -1))
			for _, unbased := range unbasedAmounts(text) {
				t.Errorf("%s/%s states an amount and no basis: %q", name, file, unbased)
			}
		}
	}
	if walked == 0 {
		t.Fatal("the golden states hold no amount")
	}

	runs := policy.MinP95Runs
	budget := policy.Result{Rule: policy.RuleRunBudget, LimitUSD: "5", CostUSD: realTotal}
	limit := policy.Result{Rule: policy.RuleRunCostThreshold, LimitUSD: "0.001", CostUSD: realTotal}
	increase := policy.Result{Rule: policy.RuleRunIncrease, LimitUSD: "0.0001", CostUSD: realTotal, BaselineUSD: "0.001"}
	percent := policy.Result{Rule: policy.RuleRunIncreasePercent, LimitPercent: "10", CostUSD: realTotal, BaselineUSD: "0.001"}
	p95 := policy.Result{Rule: policy.RuleRunP95, CostUSD: realTotal, P95USD: "0.0006", LimitFactor: "1.5", BaselineRuns: &runs}
	total := policy.Result{Rule: policy.RulePullRequestBudget, LimitUSD: "10", CostUSD: "10.5"}
	lines := map[string]bool{}
	for _, result := range []policy.Result{budget, limit, increase, percent, p95, total} {
		for _, outcome := range []struct{ outcome, reason string }{
			{policy.Fail, policy.ReasonOverLimit}, {policy.Fail, policy.ReasonOverBaseline}, {policy.Pass, policy.ReasonWithinLimit},
			{policy.Unproven, policy.ReasonUnverifiedRows}, {policy.Unproven, policy.ReasonUnattributedRows},
		} {
			result.Outcome, result.Reason = outcome.outcome, outcome.reason
			for _, bases := range []addedBases{{}, {run: []string{"measured"}, total: []string{"measured"}}, {run: []string{"measured", "list_equivalent"}, total: []string{"measured", "list_equivalent", "billed"}}} {
				line := resultSentence(result, 0, true, bases, RequestNoun(AccountGitHub))
				lines[line] = true
				for _, unbased := range unbasedAmounts(line) {
					t.Errorf("the line of %s, %s, states an amount and no basis: %q", result.Rule, outcome.reason, unbased)
				}
			}
		}
	}
	if stated := len(slices.DeleteFunc(slices.Collect(maps.Keys(lines)), func(line string) bool { return !amountRead.MatchString(line) })); stated < 20 {
		t.Fatalf("only %d rule lines state an amount", stated)
	}

	// The walk bites: each of these states an amount with no basis beside it,
	// and each of the others states it with one.
	for _, bare := range []string{
		"Run budget: USD 0.001619725 is over the USD 0.001 budget.",
		"Over limit: this run cost USD 0.001619725 at list (List-equivalent, not billed), over 1.5 times the USD 0.0006 P95 of this agent's latest 20 runs.",
		"Cap: USD 5.00 (client-side estimate, can pass the cap). Cost: USD 0.001619725, List-equivalent, not billed. Under the cap by USD 4.998380275.",
		"Pull request budget: USD 10.50 over this pull request's runs is over the USD 10.00 budget. The total adds every basis together, at list.",
		"This run cost USD 5.00.",
		"This run cost USD 5.00 at list.",
		"The run went USD 0.05 over the cap.",
		"| USD 0.10 | no cap held the run |",
		"| `0.4.3` | `output` | USD 0.000136 |",
		"<summary>How USD 0.001619725 was computed</summary>",
		"| `aws_instance.web` | `Amazon EC2` | 2026-12 | EUR 25.488 |",
		"Developer-machine sessions, unchecked: USD 11.4619213 at list, 2 sessions of Claude Code on 2026-11-20 (UTC).",
	} {
		if len(unbasedAmounts(bare)) == 0 {
			t.Errorf("the walk passes an amount with no basis: %q", bare)
		}
	}
	for _, based := range []string{
		"Run budget: USD 0.001619725 at list (Measured and List-equivalent together) is over the USD 0.001 budget.",
		"Cap: USD 0.05, which nothing enforced. Cost: USD 0.10 at list (Measured), USD 0.05 over the cap.",
		"| USD 0.10, fallback price | Measured | no cap held the run |",
		"| `0.4.3` | `output` | `x` | USD 0.50 per million tokens | 272 | USD 0.000136 |",
		"The pack said `USD 9 is too much`.",
		"<summary>How USD 0.001619725 at list (List-equivalent, not billed) was computed</summary>",
		"| `aws_instance.web` | `Amazon EC2` | 2026-12 | EUR 25.488 | Estimated |",
	} {
		if unbased := unbasedAmounts(based); len(unbased) != 0 {
			t.Errorf("the walk refuses an amount that states its basis: %q", unbased)
		}
	}
}
