package comment

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/policy"
)

// StatusLimit is the longest a verdict sentence gets, in characters. It is
// GitLab's limit on a commit status description, the shortest of the fields
// a host gives such a sentence.
const StatusLimit = 255

// The values of a result object's amounts.
const (
	amountsShown    = "shown"
	amountsWithheld = "withheld"
)

// instantLayout is how a person reads an instant, always in UTC.
const instantLayout = "2006-01-02 15:04 UTC"

// VerdictSentence says what is true of a run in one sentence of plain text.
// It is the same string on every surface a person reads: the check's title,
// the first line of the comment and of the job summary, and the last line a
// terminal prints. It states what the run is against its policy, the figure
// with its currency and basis, the budget, and one clause on what was checked
// or what follows. Withheld, it names no amount and says so.
func VerdictSentence(input Input) string {
	var approval *Approval
	if input.approved() {
		approval = input.Approval
	}
	return input.fields().sentence(approval, len(input.Ledger.Infrastructure.Figures) > 0)
}

// cut is how much of a verdict sentence is said.
type cut struct {
	// figures is how many figures are named before the rest are counted.
	figures int
	// limit keeps the budget, the limit or the cap the figure is held to.
	limit bool
	// closing keeps the clause on what was checked or what follows.
	closing bool
}

// cuts is the order a sentence past StatusLimit is cut in: the figures after
// the first are counted, not named; then the closing clause goes; then the
// budget. Every cut keeps the state, the first figure with its currency and
// basis, and the note that amounts are withheld. An amount is at most 64
// characters a row and 12 decimal places, so the last cut fits.
var cuts = []cut{{firstFigures, true, true}, {1, true, true}, {1, true, false}, {1, false, false}}

// sentence builds the verdict sentence from the result object, so the two
// never disagree. approval is the approval that turned a failure neutral, nil
// for none, and besideEstimates says the report shows figures that are not an
// agent's.
func (b Block) sentence(approval *Approval, besideEstimates bool) string {
	var text string
	for _, said := range cuts {
		text = b.sentenceAt(said, approval, besideEstimates)
		if utf8.RuneCountInString(text) <= StatusLimit {
			break
		}
	}
	return text
}

// claim is a verdict sentence in its parts.
type claim struct {
	// state is what is true of the run, and figure what it cost.
	state, figure string
	// limit is what the figure is held to, and closing what was checked or
	// what follows.
	limit, closing string
}

func (c claim) text(said cut, withheld bool) string {
	text := c.state + ": " + c.figure
	if said.limit {
		text += c.limit
	}
	text += "."
	if said.closing && c.closing != "" {
		text += " " + c.closing
	}
	if withheld {
		text += " Amounts are withheld here."
	}
	return text
}

const refusedSentence = "No figure (artifact_refused): the rows artifact held files besides the rows and the run object, so this report read none of it and the check fails."

func (b Block) sentenceAt(said cut, approval *Approval, besideEstimates bool) string {
	withheld := b.Amounts != amountsShown
	decided, ruled := deciding(b.Policy)
	switch {
	case b.RunReason == ArtifactRefused:
		return refusedSentence
	case ruled && broken(decided):
		return b.overClaim(decided, approval, said).text(said, withheld)
	case b.State != Reported:
		return b.noFigure(besideEstimates, !ruled && said.closing)
	case b.Policy != nil && b.Policy.Config == policy.ConfigInvalid:
		return b.undecided("costroid.yml is not valid, so no rule ran", said).text(said, withheld)
	case b.Policy != nil && b.Policy.Config == policy.ConfigUnread:
		return b.undecided("costroid.yml could not be read, so no rule ran", said).text(said, withheld)
	case !ruled:
		return claim{state: "No budget set", figure: b.runCost(said), limit: b.overCap(), closing: "No rule decides this check."}.text(said, withheld)
	}
	return b.keptClaim(decided, said).text(said, withheld)
}

// rank orders the outcomes a verdict holds from the one that decides its
// conclusion down.
func rank(result policy.Result) int {
	switch {
	case result.Outcome == policy.Fail:
		return 0
	case result.Rule == policy.RuleAttribution:
		return 3
	case result.Outcome == policy.Warn:
		return 1
	case result.Outcome == policy.Unproven:
		return 2
	}
	return 4
}

// deciding is the result the verdict sentence names. The pack concludes
// failure on any rule that fails and success only when every rule passes, so
// the result that decides is the first, in the order the verdict lists its
// rules, of the worst outcome it holds: a rule that fails, then one the
// warn-only period holds, then one that could not pass, then one that passed.
// The attribution rule only warns and holds no figure, so it is named only
// after every other rule passed, and never alone.
func deciding(verdict *policy.Verdict) (policy.Result, bool) {
	if verdict == nil || !slices.ContainsFunc(verdict.Results, func(result policy.Result) bool { return result.Rule != policy.RuleAttribution }) {
		return policy.Result{}, false
	}
	return slices.MinFunc(verdict.Results, func(a, b policy.Result) int { return rank(a) - rank(b) }), true
}

// broken reports a result that found its limit passed or a model off the
// list, whether it fails the check or the warn-only period holds it.
func broken(result policy.Result) bool {
	return rank(result) < 2
}

// leadNouns name what each rule holds a cost to, in the state a sentence
// opens with: a budget, or a limit.
var leadNouns = map[string]string{
	policy.RuleRunBudget:          "budget",
	policy.RuleRunCostThreshold:   "limit",
	policy.RuleRunIncrease:        "limit",
	policy.RuleRunIncreasePercent: "limit",
	policy.RulePullRequestBudget:  "budget",
	policy.RuleMonthlyBudget:      "budget",
}

// ruleNouns name each rule inside a sentence.
var ruleNouns = map[string]string{
	policy.RuleRunBudget:          "run budget",
	policy.RuleRunCostThreshold:   "run cost limit",
	policy.RuleRunIncrease:        "increase over baseline",
	policy.RuleRunIncreasePercent: "percent increase over baseline",
	policy.RulePullRequestBudget:  "pull request budget",
	policy.RuleMonthlyBudget:      "monthly budget",
	policy.RuleModelAllowlist:     "model list",
	policy.RuleAttribution:        "attribution",
}

// overClaim is the sentence for a rule that found its limit passed or a model
// off the list: enforced, held by the warn-only period, or approved.
func (b Block) overClaim(result policy.Result, approval *Approval, said cut) claim {
	over := claim{state: "Over " + leadNouns[result.Rule]}
	if result.Rule == policy.RuleModelAllowlist {
		over.state = "Model not on the list"
	}
	over.figure, over.limit = b.against(result, said, true)
	switch {
	case result.Outcome == policy.Warn:
		over.state += ", warn-only"
		over.closing = "After " + b.warnOnlyEnd() + " this fails the check."
	case approval != nil:
		over.state += ", approved by " + approval.Approver
		over.closing = "The approval covers commit " + approval.HeadSHA[:min(len(approval.HeadSHA), shortCommitLength)] + " and ends with a new push."
	}
	return over
}

// warnOnlyEnd is the instant the warn-only period ends, or the period by name
// when it has no end yet.
func (b Block) warnOnlyEnd() string {
	if ends, err := time.Parse(time.RFC3339, b.Policy.WarnOnly.Ends); err == nil {
		return ends.UTC().Format(instantLayout)
	}
	return "the warn-only period"
}

// keptClaim is the sentence for a rule nothing broke: one that passed, one
// the agent's own rows kept from passing, or one that could not be checked.
func (b Block) keptClaim(result policy.Result, said cut) claim {
	kept := claim{closing: "A ledger checked the rows."}
	switch {
	case result.Outcome == policy.Pass && result.Rule == policy.RuleModelAllowlist:
		kept.state, kept.figure = "Models on the list", b.runCost(said)
		return kept
	case result.Outcome == policy.Pass && leadNouns[result.Rule] != "":
		kept.state = "Within " + leadNouns[result.Rule]
	case result.Reason == policy.ReasonUnverifiedRows && leadNouns[result.Rule] != "":
		kept.state = "Within " + leadNouns[result.Rule] + ", by the job's own figures"
		kept.closing = "Nothing outside the job checked the figures, so the check is neutral."
	default:
		return b.undecided("the "+ruleNouns[result.Rule]+" could not be checked", said)
	}
	kept.figure, kept.limit = b.against(result, said, false)
	return kept
}

// undecided is the sentence for a check no rule decided, for the reason
// given, with what the run cost after it.
func (b Block) undecided(why string, said cut) claim {
	cost := b.runCost(said)
	return claim{state: "Not decided", figure: why + ". " + strings.ToUpper(cost[:1]) + cost[1:]}
}

// against sets a rule's figure beside what the rule holds it to. over says
// the figure is past it. The limit comes back apart, so a long sentence can
// leave it out. Withheld, the figure names no amount.
func (b Block) against(result policy.Result, said cut, over bool) (figure, limit string) {
	shown := b.Amounts == amountsShown
	noun := ruleNouns[result.Rule]
	switch result.Rule {
	case policy.RuleModelAllowlist:
		used := "used a model that is not on the list"
		if len(result.Models) > 1 {
			used = "used models that are not on the list"
		}
		if !shown {
			return "this run " + used + b.priced(said), ""
		}
		return b.runCost(said), " and " + used
	case policy.RuleRunIncrease, policy.RuleRunIncreasePercent:
		reach := "at most "
		if over {
			reach = "more than "
		}
		reach += allowedIncrease(result, shown) + " over the "
		switch {
		case !shown:
			return "this run is " + reach + "median of earlier runs" + b.priced(said), ""
		case result.BaselineUSD == "":
			return b.runCost(said), ", " + reach + "median of earlier runs"
		}
		return b.runCost(said), ", " + reach + money(budgetCurrency, result.BaselineUSD) + " median of earlier runs"
	case policy.RulePullRequestBudget:
		if !shown || result.CostUSD == "" || result.LimitUSD == "" {
			return "this pull request's runs are " + standingWord(over) + " the " + noun + ", priced at list", ""
		}
		return "this pull request's runs cost " + money(budgetCurrency, result.CostUSD) + " at list" + b.totalBases(), heldTo(result.LimitUSD, noun, over)
	}
	switch {
	case !shown:
		return "this run is " + standingWord(over) + " its " + noun + b.priced(said), ""
	case result.LimitUSD == "":
		return b.runCost(said), ", " + standingWord(over) + " its " + noun
	}
	return b.runCost(said), heldTo(result.LimitUSD, noun, over)
}

func standingWord(over bool) string {
	if over {
		return "over"
	}
	return "within"
}

// heldTo names the limit a figure stands against.
func heldTo(limit, noun string, over bool) string {
	if over {
		return ", over the " + money(budgetCurrency, limit) + " " + noun
	}
	return " of the " + money(budgetCurrency, limit) + " " + noun
}

// allowedIncrease is how far over the median a run may go: the amount when
// amounts are shown, the percent when the rule states one.
func allowedIncrease(result policy.Result, shown bool) string {
	switch {
	case shown && result.LimitUSD != "" && result.BaselineUSD != "":
		return money(budgetCurrency, result.LimitUSD)
	case result.LimitPercent != "":
		return result.LimitPercent + "%"
	}
	return "the allowed increase"
}

// totalBases names the bases a pull request's total adds, as a figure names
// its one basis: after "at list", in brackets.
func (b Block) totalBases() string {
	var labels []string
	if b.Lifetime != nil {
		for _, basis := range basisOrder {
			if slices.ContainsFunc(b.Lifetime.Figures, func(figure BlockLifetimeFigure) bool {
				return figure.Currency == budgetCurrency && figure.CostBasis == basis
			}) {
				labels = append(labels, basisLabels[basis])
			}
		}
	}
	switch len(labels) {
	case 0:
		return " (every basis together)"
	case 1:
		return " (" + labels[0] + ")"
	}
	for index, label := range labels {
		labels[index], _, _ = strings.Cut(label, ",")
	}
	return " (" + listed(labels, 0) + " together)"
}

// runCost says what the run cost, figure by figure, each with its currency
// and basis. Withheld, it says how the run was priced and names no amount.
func (b Block) runCost(said cut) string {
	if b.Amounts != amountsShown {
		return "this run was priced " + b.figures(said)
	}
	return "this run cost " + b.figures(said)
}

// priced ends a figure that names no amount with how the run was priced.
func (b Block) priced(said cut) string {
	if len(b.Figures) == 0 {
		return ""
	}
	return ", priced " + b.figures(said)
}

// figures lists the first figures of the run and counts the rest. Withheld,
// it lists each basis once and no amount.
func (b Block) figures(said cut) string {
	shown := b.Amounts == amountsShown
	var clauses []string
	for _, figure := range b.Figures {
		clause := "at list (" + basisLabels[figure.CostBasis] + ")"
		switch {
		case shown:
			clause = money(figure.Currency, figure.ListCost) + " " + clause
		case slices.Contains(clauses, clause):
			continue
		}
		clauses = append(clauses, clause)
	}
	omitted := 0
	if shown {
		omitted = b.FiguresOmitted
	}
	named := min(len(clauses), said.figures)
	return listed(clauses[:named], len(clauses)-named+omitted)
}

// overCap says how far past its cap a run went, when a grade held it to one
// and one figure compares with it.
func (b Block) overCap() string {
	if b.Cap == nil || b.Cap.Overshoot == nil || b.Cap.EnforcementGrade == nil || *b.Cap.EnforcementGrade == "none" {
		return ""
	}
	if over, err := decimal.Parse(*b.Cap.Overshoot); err != nil || over.Sign() <= 0 {
		return ""
	}
	return ", " + money(b.Cap.Currency, *b.Cap.Overshoot) + " over its " + money(b.Cap.Currency, b.Cap.Amount) + " cap"
}

// noFigure is the sentence for a report with no figures: the typed reason and
// its sentence, and that no rule ran when none did.
func (b Block) noFigure(besideEstimates, noRule bool) string {
	if b.Reason == AgentSkipped {
		return "No agent run: the workflow skipped the agent's job on this pull request, so there is no cost to report."
	}
	code, why := b.absence(besideEstimates)
	text := "No figure (" + code + "): " + why + "."
	if noRule {
		text += " No rule ran."
	}
	return text
}

// absence is the typed code for why a report has no figures, and its
// sentence. The run's own reason names it when the run stated one, then the
// report's reason, then its state.
func (b Block) absence(besideEstimates bool) (code, why string) {
	code = string(b.State)
	if b.Reason != "" {
		code = string(b.Reason)
	}
	rows := "rows"
	if besideEstimates {
		rows = "agent rows"
	}
	switch {
	case b.RunReason.Sentence() != "":
		return string(b.RunReason), b.RunReason.Sentence()
	case b.Reason == NoRowsFile || b.Reason == ForkNoRows:
		return code, "no rows file reached this report. The agent's job log says why it wrote none"
	case b.Reason == NotAFile:
		return code, "the rows path is not a regular file, so this report did not read it"
	case b.Reason == InvalidRow && b.ReasonLine > 0:
		return code, fmt.Sprintf("line %d of the rows file is not a valid row. A partial total would read as a smaller cost", b.ReasonLine)
	case b.Reason == InvalidRow:
		return code, "a line of the rows file is not a valid row. A partial total would read as a smaller cost"
	case b.State == NoRows:
		return code, "the rows file holds no " + rows + ". Either the agent printed no usage or its job stopped before it wrote a row, and that job's log says which"
	}
	return code, "the rows file could not be read"
}
