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
	return input.telling().sentence()
}

// telling is a result object beside what its verdict sentence says that the
// object does not hold.
type telling struct {
	Block
	// approval is the approval that turned a failure neutral, nil for none.
	approval *Approval
	// forkRows says the rows are a fork's own, so a rule they break is held
	// at neutral, in the warn-only period and after it.
	forkRows bool
	// besideEstimates says the report shows figures that are not an agent's.
	besideEstimates bool
	// request is what the host calls the change the run was for.
	request string
}

// telling is the result object the report writes, so the sentence and the
// object never disagree, beside what only the report knows.
func (i Input) telling() telling {
	told := telling{
		Block:           i.fields(),
		forkRows:        i.forkRows,
		besideEstimates: len(i.Ledger.Infrastructure.Figures) > 0,
		request:         i.request(),
	}
	if i.approved() {
		told.approval = i.Approval
	}
	return told
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

// sentence is the verdict sentence, cut until it fits StatusLimit.
func (b telling) sentence() string {
	var text string
	for _, said := range cuts {
		text = b.sentenceAt(said)
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

func (b telling) sentenceAt(said cut) string {
	withheld := b.Amounts != amountsShown
	decided, ruled := deciding(b.Policy)
	pack, packDecides := packDeciding(b.Policy, b.State)
	switch {
	case b.RunReason == ArtifactRefused:
		return refusedSentence
	case packDecides:
		return b.packClaim(pack, said).text(said, withheld)
	case ruled && overRule(decided):
		return b.overClaim(decided, said).text(said, withheld)
	case ruled && broken(decided):
		return b.packStopped(decided, said).text(said, withheld)
	case b.State != Reported:
		return b.noFigure(!ruled && !packAnswered(b.Policy) && said.closing)
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
// list, or a customer pack stopped past its bound or its words, whether it
// fails the check or the warn-only period holds it.
func broken(result policy.Result) bool {
	return rank(result) < 2
}

// overRule reports a broken result of a rule that holds a figure to a limit
// or the models to a list: every broken result but customer_pack's.
func overRule(result policy.Result) bool {
	return broken(result) && result.Rule != policy.RuleCustomerPack
}

// packRank orders a customer pack's results from the one that decides a
// conclusion down, on the scale of rank times packScale: a pack's failure
// comes after a failing rule of Costroid's and before one the warn-only
// period holds, and a pack's failure the period holds, then its warning, come
// after that and before a rule that could not pass.
func packRank(result policy.PackResult) int {
	switch {
	case result.HeldByWarnOnly:
		return packScale + 1
	case result.Outcome == policy.PackFail:
		return 1
	}
	return packScale + 2
}

// packScale is how many ranks of the pack's fit between two of Costroid's.
const packScale = 4

// packDeciding is the customer pack's result the verdict sentence names: the
// first, in the order the verdict lists them, of the worst outcome, when it
// comes before every result of Costroid's. A failing rule of Costroid's always
// decides first. A report with no figures names none, because the sentence
// names the figure the pack's rule was held to.
func packDeciding(verdict *policy.Verdict, state State) (policy.PackResult, bool) {
	if state != Reported || verdict == nil || verdict.Pack == nil || len(verdict.Pack.Results) == 0 {
		return policy.PackResult{}, false
	}
	pack := slices.MinFunc(verdict.Pack.Results, func(a, b policy.PackResult) int { return packRank(a) - packRank(b) })
	if decided, ruled := deciding(verdict); ruled && rank(decided)*packScale < packRank(pack) {
		return policy.PackResult{}, false
	}
	return pack, true
}

// packAnswered reports a verdict whose customer pack returned a result.
func packAnswered(verdict *policy.Verdict) bool {
	return verdict != nil && verdict.Pack != nil && len(verdict.Pack.Results) > 0
}

// leadNouns name what each rule holds a cost to, in the state a sentence
// opens with: a budget, or a limit.
var leadNouns = map[string]string{
	policy.RuleRunBudget:          "budget",
	policy.RuleRunCostThreshold:   "limit",
	policy.RuleRunIncrease:        "limit",
	policy.RuleRunIncreasePercent: "limit",
	policy.RuleRunP95:             "limit",
	policy.RulePullRequestBudget:  "budget",
	policy.RuleMonthlyBudget:      "budget",
}

// ruleNouns name each rule inside a sentence. The budget on a change's total
// is named by ruleNoun, after the host's word for the change.
var ruleNouns = map[string]string{
	policy.RuleRunBudget:          "run budget",
	policy.RuleRunCostThreshold:   "run cost limit",
	policy.RuleRunIncrease:        "increase over baseline",
	policy.RuleRunIncreasePercent: "percent increase over baseline",
	policy.RuleRunP95:             "P95 limit",
	policy.RuleMonthlyBudget:      "monthly budget",
	policy.RuleModelAllowlist:     "model list",
	policy.RuleAttribution:        "attribution",
}

// ruleNoun names a rule inside a sentence. request is the host's word for a
// change, which the budget on a change's total is named after.
func ruleNoun(rule, request string) string {
	if rule == policy.RulePullRequestBudget {
		return request + " budget"
	}
	return ruleNouns[rule]
}

// forkClosing says why a rule a fork's own rows break leaves the check
// neutral.
const forkClosing = "A fork's rows are the fork's own claim, so the check is neutral."

// overClaim is the sentence for a rule that found its limit passed or a model
// off the list: held at neutral on a fork's own rows, held by the warn-only
// period, approved, or enforced. A fork's rows come first: their check stays
// neutral when the period ends, so the sentence never says it will fail.
func (b telling) overClaim(result policy.Result, said cut) claim {
	over := claim{state: "Over " + leadNouns[result.Rule]}
	if result.Rule == policy.RuleModelAllowlist {
		over.state = "Model not on the list"
	}
	over.figure, over.limit = b.against(result, said, true)
	switch {
	case b.forkRows:
		over.state += ", on a fork's own rows"
		over.closing = forkClosing
	case result.Outcome == policy.Warn:
		over.state += ", warn-only"
		over.closing = "After " + b.warnOnlyEnd() + " this fails the check."
	case b.approval != nil:
		over.state += ", approved by " + b.approval.Approver
		over.closing = "The approval covers commit " + b.approval.HeadSHA[:min(len(b.approval.HeadSHA), shortCommitLength)] + " and ends with a new push."
	}
	return over
}

// packOwn opens the closing clause of a pack rule's sentence.
const packOwn = "The rule is this repository's own"

// packClaim is the sentence for a rule of the repository's own pack that
// decides the check: one that fails, one the warn-only period holds, one
// that warns, or one a fork's own rows break. The pack's message is the
// pack's own text, so it follows in the body and never here.
func (b telling) packClaim(result policy.PackResult, said cut) claim {
	pack := claim{state: "Pack rule warned (" + result.Rule + ")", figure: b.runCost(said), closing: packOwn + ", and a warning never fails the check."}
	switch {
	case result.Outcome == policy.PackWarn && !result.HeldByWarnOnly:
	case b.forkRows:
		pack.state, pack.closing = "Pack rule failed, on a fork's own rows ("+result.Rule+")", forkClosing
	case result.HeldByWarnOnly:
		pack.state, pack.closing = "Pack rule failed, warn-only ("+result.Rule+")", packOwn+". After "+b.warnOnlyEnd()+" this fails the check."
	default:
		pack.state, pack.closing = "Pack rule failed ("+result.Rule+")", packOwn+", read from the base branch."
	}
	return pack
}

// packStopped is the sentence for a customer pack that did not run to its
// end for a cause the rows can reach: past its bound, or an answer outside
// its words. It fails the check, or warns in the warn-only period, and says
// when it will fail. A report with no figures names none, and the reason for
// that opens the comment under it.
func (b telling) packStopped(result policy.Result, said cut) claim {
	why := "the policy pack passed its evaluation bound on this run's rows"
	if result.Reason == policy.ReasonPackResult {
		why = "the policy pack answered with something other than its results"
	}
	stopped := claim{state: "Pack stopped (" + result.Reason + ")", figure: why}
	switch {
	case b.forkRows:
		stopped.closing = forkClosing
	case result.Outcome == policy.Warn:
		stopped.closing = "After " + b.warnOnlyEnd() + " this fails the check."
	default:
		stopped.figure += ", so the check fails"
	}
	if b.State == Reported {
		stopped.figure += ". " + capital(b.runCost(said))
	}
	return stopped
}

// packUnproven is why a customer pack that the base branch alone kept from
// running ran none of its rules: a file that is not valid, at the line the
// compiler named, or one that could not be read.
func (b telling) packUnproven(result policy.Result) string {
	switch {
	case result.Reason == policy.ReasonPackInvalid && result.Line > 0:
		return fmt.Sprintf("the policy pack is not valid at line %d, so none of its rules ran", result.Line)
	case result.Reason == policy.ReasonPackInvalid:
		return "the policy pack is not valid, so none of its rules ran"
	case b.Policy.ConfigFromBase:
		return "the policy pack could not be read at the base commit, so none of its rules ran"
	}
	return "the policy pack could not be read, so none of its rules ran"
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
func (b telling) keptClaim(result policy.Result, said cut) claim {
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
	case result.Rule == policy.RuleRunP95 && result.Reason == policy.ReasonNoBaseline:
		return b.undecided(fmt.Sprintf("the P95 limit needs %d earlier runs of this agent and %d reached the check, so the rule did not run", policy.MinP95Runs, baselineRuns(result)), said)
	case result.Rule == policy.RuleCustomerPack:
		return b.undecided(b.packUnproven(result), said)
	default:
		return b.undecided("the "+ruleNoun(result.Rule, b.request)+" could not be checked", said)
	}
	kept.figure, kept.limit = b.against(result, said, false)
	return kept
}

// undecided is the sentence for a check no rule decided, for the reason
// given, with what the run cost after it.
func (b Block) undecided(why string, said cut) claim {
	return claim{state: "Not decided", figure: why + ". " + capital(b.runCost(said))}
}

// against sets a rule's figure beside what the rule holds it to. over says
// the figure is past it. The limit comes back apart, so a long sentence can
// leave it out. Withheld, the figure names no amount.
func (b telling) against(result policy.Result, said cut, over bool) (figure, limit string) {
	shown := b.Amounts == amountsShown
	noun := ruleNoun(result.Rule, b.request)
	switch result.Rule {
	case policy.RuleModelAllowlist:
		used := "used a model that is not on the list"
		if len(result.Models) > 1 {
			used = "used models that are not on the list"
		}
		if !shown {
			return b.subject() + " " + used + b.priced(said), ""
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
			return b.subject() + " is " + reach + "median of earlier runs" + b.priced(said), ""
		case result.BaselineUSD == "":
			return b.runCost(said), ", " + reach + "median of earlier runs"
		}
		return b.runCost(said), ", " + reach + Money(budgetCurrency, result.BaselineUSD) + " median of earlier runs"
	case policy.RuleRunP95:
		held := standingWord(over) + " " + p95Limit(result, shown)
		if !shown {
			return b.subject() + " is " + held + b.priced(said), ""
		}
		return b.runCost(said), ", " + held
	case policy.RulePullRequestBudget:
		if !shown || result.CostUSD == "" || result.LimitUSD == "" {
			return "this " + b.request + "'s runs are " + standingWord(over) + " the " + noun + ", priced at list", ""
		}
		return "this " + b.request + "'s runs cost " + Money(budgetCurrency, result.CostUSD) + " at list" + b.totalBases(), heldTo(result.LimitUSD, noun, over)
	}
	switch {
	case !shown:
		return b.subject() + " is " + standingWord(over) + " its " + noun + b.priced(said), ""
	case result.LimitUSD == "":
		return b.runCost(said), ", " + standingWord(over) + " its " + noun
	}
	return b.runCost(said), heldTo(result.LimitUSD, noun, over)
}

// p95Limit names the limit run_p95 holds a run to: the factor times the P95 of
// the baseline, with the P95's amount when amounts are shown. The rule
// compares a run only with a baseline of MinP95Runs runs.
func p95Limit(result policy.Result, shown bool) string {
	p95 := "P95"
	if shown && result.P95USD != "" {
		p95 = Money(budgetCurrency, result.P95USD) + " P95"
	}
	return fmt.Sprintf("%s times the %s of this agent's latest %d runs", result.LimitFactor, p95, policy.MinP95Runs)
}

// baselineRuns is how many earlier runs run_p95's baseline held, 0 when the
// result states none.
func baselineRuns(result policy.Result) int {
	if result.BaselineRuns == nil {
		return 0
	}
	return *result.BaselineRuns
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
		return ", over the " + Money(budgetCurrency, limit) + " " + noun
	}
	return " of the " + Money(budgetCurrency, limit) + " " + noun
}

// allowedIncrease is how far over the median a run may go: the amount when
// amounts are shown, the percent when the rule states one.
func allowedIncrease(result policy.Result, shown bool) string {
	switch {
	case shown && result.LimitUSD != "" && result.BaselineUSD != "":
		return Money(budgetCurrency, result.LimitUSD)
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
		return b.subject() + " was priced " + b.figures(said)
	}
	return b.subject() + " cost " + b.figures(said)
}

// subject names the run the figures are: this pull request's own, or the
// recorded run a report shows in its place, so no sentence gives a recorded
// run's cost as the pull request's.
func (b Block) subject() string {
	if b.Recorded != nil {
		return "the recorded run"
	}
	return "this run"
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
			clause = Money(figure.Currency, figure.ListCost) + " " + clause
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
	return ", " + Money(b.Cap.Currency, *b.Cap.Overshoot) + " over its " + Money(b.Cap.Currency, b.Cap.Amount) + " cap"
}

// noFigure is the sentence for a report with no figures: the typed reason and
// its sentence, and that no rule ran when none did.
func (b telling) noFigure(noRule bool) string {
	if b.Reason == AgentSkipped {
		return "No agent run: the workflow skipped the agent's job on this " + b.request + ", so there is no cost to report."
	}
	code, why := b.absence()
	text := "No figure (" + code + "): " + why + "."
	if noRule {
		text += " No rule ran."
	}
	return text
}

// absence is the typed code for why a report has no figures, and its
// sentence. The run's own reason names it when the run stated one, then the
// report's reason, then its state.
func (b telling) absence() (code, why string) {
	code = string(b.State)
	if b.Reason != "" {
		code = string(b.Reason)
	}
	rows := "rows"
	if b.besideEstimates {
		rows = "agent rows"
	}
	switch {
	case b.RunReason.Sentence() != "":
		return string(b.RunReason), b.RunReason.Sentence()
	case b.Reason == ForkNoRows:
		return code, "this " + b.request + " comes from a fork, and its workflow run left no rows"
	case b.Reason == NoRowsFile:
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
