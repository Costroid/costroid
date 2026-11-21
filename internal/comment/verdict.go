package comment

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/policy"
)

func blockVerdict(verdict *policy.Verdict, shown bool) *policy.Verdict {
	if verdict == nil || shown {
		return verdict
	}
	withheld := verdict.WithoutAmounts()
	return &withheld
}

// ruleLabels open each rule's own line. The budget on a change's total is
// labelled by ruleLabel, after the host's word for the change.
var ruleLabels = map[string]string{
	policy.RuleRunBudget:          "Run budget",
	policy.RuleRunCostThreshold:   "Run cost limit",
	policy.RuleRunIncrease:        "Increase over baseline",
	policy.RuleRunIncreasePercent: "Percent increase over baseline",
	policy.RuleRunP95:             "P95 limit",
	policy.RuleMonthlyBudget:      "Monthly budget",
	policy.RuleModelAllowlist:     "Model allowlist",
	policy.RuleAttribution:        "Attribution",
}

// ruleLabel opens a rule's own line. request is the host's word for a change.
func ruleLabel(rule, request string) string {
	if rule == policy.RulePullRequestBudget {
		return capital(request) + " budget"
	}
	return ruleLabels[rule]
}

// capital is text with its first letter in upper case.
func capital(text string) string {
	return strings.ToUpper(text[:1]) + text[1:]
}

var limitNouns = map[string]string{
	policy.RuleRunBudget:          "budget",
	policy.RuleRunCostThreshold:   "limit",
	policy.RuleRunIncrease:        "limit",
	policy.RuleRunIncreasePercent: "limit",
	policy.RuleRunP95:             "limit",
	policy.RulePullRequestBudget:  "budget",
	policy.RuleMonthlyBudget:      "budget",
}

// The costroid.yml a verdict reads: a report reads the pull request's base
// branch's, and a preview the working directory's.
const (
	baseBranchConfig = "`costroid.yml` on the base branch"
	directoryConfig  = "`costroid.yml` in this directory"
)

// configRead names the costroid.yml the verdict read.
func (i Input) configRead() string {
	if i.Preview {
		return directoryConfig
	}
	return baseBranchConfig
}

// reading is what stands between the verdict sentence and the evidence, a
// paragraph each: the line for a recorded run, one sentence of reason, the
// rules the sentence does not name, and a next step only when the reader must
// act. The reason is the approval when one turned the check neutral, then who
// vouched for the rows, or what a report with no figures leaves unsaid.
func (i Input) reading(shown bool) []string {
	var paragraphs []string
	add := func(paragraph string) {
		if paragraph != "" {
			paragraphs = append(paragraphs, paragraph)
		}
	}
	add(i.recordedLine())
	if i.approved() {
		add(approvalLine(*i.Approval))
	}
	if i.forkHeld() {
		add(forkHeldLine(i.request()))
	}
	if i.State == Reported {
		add(i.trustLine())
	} else {
		add(i.why())
	}
	add(strings.Join(i.otherRules(shown, false), " "))
	add(i.nextStep())
	return paragraphs
}

// named reports a result the verdict sentence states in full: its figure
// against its limit, or the P95 limit's short history, which the sentence
// counts. Any other rule that could not be checked is explained by its own
// line, and so is the model list, whose line names the models.
func named(result policy.Result) bool {
	switch {
	case leadNouns[result.Rule] == "":
		return false
	case broken(result) || result.Outcome == policy.Pass:
		return true
	case result.Rule == policy.RuleRunP95 && result.Reason == policy.ReasonNoBaseline:
		return true
	}
	return result.Reason == policy.ReasonUnverifiedRows
}

// otherRules are the lines of the rules the verdict sentence leaves to the
// body: every rule but the one it states in full. The attribution rule only
// warns, and the reason line already says nothing outside the job checked the
// rows, so its line is left to the fold unless attribution asks for it.
func (i Input) otherRules(shown, attribution bool) []string {
	if i.Verdict == nil {
		return nil
	}
	decided, ruled := deciding(i.Verdict)
	stated := ruled && named(decided) && (i.State == Reported || broken(decided))
	var lines []string
	for _, result := range i.Verdict.Results {
		switch {
		case stated && result.Rule == decided.Rule:
		case result.Rule == policy.RuleAttribution && !attribution && result.Rule != decided.Rule:
		default:
			lines = append(lines, resultSentence(result, i.Verdict.JoinableRows, shown, i.lifetimeBases(), i.request()))
		}
	}
	return lines
}

// attributionNote is the attribution rule's line, which the fold holds, and
// empty when the rule said nothing or the reading states it.
func (i Input) attributionNote() string {
	decided, _ := deciding(i.Verdict)
	for _, result := range i.Verdict.Results {
		if result.Rule == policy.RuleAttribution && result.Rule != decided.Rule {
			return resultSentence(result, i.Verdict.JoinableRows, false, nil, i.request())
		}
	}
	return ""
}

// approvalPage says how a reviewer approves a failed check.
const approvalPage = "https://docs.costroid.com/approval"

// nextStep is the one sentence for a reader who must act: the file to fix
// when costroid.yml kept every rule from running, how a failure is approved,
// or when the warn-only period ends. It is empty for any other verdict. Only
// GitHub's check takes an approval. A fork's own rows leave nothing to act
// on: their check is neutral whether or not the period runs.
func (i Input) nextStep() string {
	switch {
	case i.Verdict == nil:
		return ""
	case configSentence(*i.Verdict, i.configRead()) != "":
		return configSentence(*i.Verdict, i.configRead())
	case i.forkRows:
		return ""
	case i.Verdict.Conclusion == policy.Failure && !i.approved() && i.AccountHost != AccountGitLab:
		return "[The approval page](" + approvalPage + ") says how a reviewer approves a failed check."
	case slices.ContainsFunc(i.Verdict.Results, func(result policy.Result) bool { return result.HeldByWarnOnly }):
		return warnOnlySentence(i.Verdict.WarnOnly)
	}
	return ""
}

// policyNotes are the sentences on the policy that a reader opens the fold
// for: that no policy ran or no rule applied, what a budget kept on the
// agent's own rows would conclude when broken, the page that says what lets a
// rule pass, the attribution rule's line, and where the settings were read.
func (i Input) policyNotes() string {
	if i.Verdict == nil {
		return "No policy ran on this change."
	}
	var notes []string
	if unruled(*i.Verdict) {
		notes = append(notes, noRuleApplied(i.configRead()))
	}
	notes = append(notes, passNotes(*i.Verdict)...)
	if attribution := i.attributionNote(); attribution != "" {
		notes = append(notes, attribution)
	}
	if i.Verdict.ConfigFromBase {
		notes = append(notes, fromBase(i.request()))
	}
	return strings.Join(notes, " ")
}

// LocalConfig names a costroid.yml read from the working tree at path: the
// default file as the one in this directory, any other by its path.
func LocalConfig(path string) string {
	if filepath.Clean(path) == "costroid.yml" {
		return directoryConfig
	}
	return "`" + path + "`"
}

// VerdictLines are what a terminal prints above the verdict sentence, one
// sentence a line: why no rule ran or that none applied, each rule the
// sentence does not name, and the warn-only period when it holds a rule. config
// names the costroid.yml the verdict read, as LocalConfig does. The lines
// carry no link. They are built from the verdict's closed fields and its
// amounts, so no text from the pull request reaches them.
func VerdictLines(input Input, config string) []string {
	if input.Verdict == nil {
		return nil
	}
	verdict := *input.Verdict
	var lines []string
	if problem := configSentence(verdict, config); problem != "" {
		lines = append(lines, problem)
	}
	if unruled(verdict) {
		lines = append(lines, noRuleApplied(config))
	}
	lines = append(lines, input.otherRules(input.Amounts == AmountsShown, true)...)
	if slices.ContainsFunc(verdict.Results, func(result policy.Result) bool { return result.HeldByWarnOnly }) {
		lines = append(lines, warnOnlySentence(verdict.WarnOnly))
	}
	if verdict.ConfigFromBase {
		lines = append(lines, fromBase(input.request()))
	}
	return lines
}

// fromBase says why a change to costroid.yml in the pull request does not
// reach its own verdict.
func fromBase(request string) string {
	return "A change to `costroid.yml` in this " + request + " applies after the merge, because the verdict reads the base branch's file."
}

// configSentence says why no rule ran when the costroid.yml named could not
// be used, and is empty when it could.
func configSentence(verdict policy.Verdict, config string) string {
	switch verdict.Config {
	case policy.ConfigInvalid:
		return "No rule ran, so the check concludes neutral: " + config + " is not valid, and `costroid validate` names the problem."
	case policy.ConfigUnread:
		return "No rule ran, so the check concludes neutral: " + config + " could not be read, and a missing input never blocks a merge."
	}
	return ""
}

// unruled reports a verdict whose costroid.yml was read and set nothing a
// rule reads: no rule ran but attribution, which needs no setting.
func unruled(verdict policy.Verdict) bool {
	_, ruled := deciding(&verdict)
	return !ruled && configSentence(verdict, "") == ""
}

// noRuleApplied says the costroid.yml named sets nothing a rule reads, and
// names the key that sets a run budget.
func noRuleApplied(config string) string {
	return "No rule applied: " + config + " sets no budget and no limit. " + setBudget
}

// resultSentence is a rule's own line. request is the host's word for a
// change, and bases are the bases the change's total adds.
func resultSentence(result policy.Result, joinable int, shown bool, bases []string, request string) string {
	label, noun := ruleLabel(result.Rule, request), limitNouns[result.Rule]
	switch {
	case result.Rule == policy.RuleModelAllowlist:
		return label + ": " + allowlistSentence(result)
	case result.Rule == policy.RuleRunIncrease || result.Rule == policy.RuleRunIncreasePercent:
		if sentence := increaseSentence(result, shown); sentence != "" {
			return label + ": " + sentence
		}
	case result.Rule == policy.RuleRunP95:
		if sentence := p95Sentence(result, shown); sentence != "" {
			return label + ": " + sentence
		}
	case result.Rule == policy.RulePullRequestBudget:
		if sentence := pullRequestSentence(result, shown, bases, request); sentence != "" {
			return label + ": " + sentence
		}
	}
	amounts := shown && result.CostUSD != "" && result.LimitUSD != ""
	switch result.Reason {
	case policy.ReasonOverLimit:
		if amounts {
			return fmt.Sprintf("%s: %s is over the %s %s.", label, Money(budgetCurrency, result.CostUSD), Money(budgetCurrency, result.LimitUSD), noun)
		}
		return fmt.Sprintf("%s: the run is over the %s.", label, noun)
	case policy.ReasonWithinLimit:
		if amounts {
			return fmt.Sprintf("%s: %s is within the %s %s.", label, Money(budgetCurrency, result.CostUSD), Money(budgetCurrency, result.LimitUSD), noun)
		}
		return fmt.Sprintf("%s: the run is within the %s.", label, noun)
	case policy.ReasonUnverifiedRows:
		return label + ": unchecked rows cannot satisfy a budget." + standing(result, shown, bases, request)
	case policy.ReasonUnattributedRows:
		return label + ": unattributed rows cannot satisfy a budget." + standing(result, shown, bases, request)
	case policy.ReasonNoRows:
		return label + ": this check read no rows, so it cannot pass."
	case policy.ReasonOtherCurrency:
		return label + ": rows in another currency are never held to a USD " + noun + ", so it cannot pass."
	case policy.ReasonNoLedger:
		return label + ": the month to date comes only from a ledger, and this check has none, so the rule did not run."
	}
	rows := "rows name"
	if joinable == 1 {
		rows = "row names"
	}
	return fmt.Sprintf("%s: %d %s a credential, a %s and a head commit, and stay unattributed until a ledger checks them against the job's OIDC token.", label, joinable, rows, request)
}

// budgetRules are the rules that hold a figure to a USD budget or threshold.
var budgetRules = []string{policy.RuleRunBudget, policy.RuleRunCostThreshold, policy.RulePullRequestBudget}

// standing says where a budget rule's figure stands against its limit, when
// the rows kept the rule from passing although the figure was within it. It
// is empty for any other rule, and for a rule that held no figure. The pack
// reaches these reasons only after it found the figure not over the limit.
func standing(result policy.Result, shown bool, bases []string, request string) string {
	if !slices.Contains(budgetRules, result.Rule) || result.CostUSD == "" || result.LimitUSD == "" {
		return ""
	}
	noun := limitNouns[result.Rule]
	switch {
	case result.Rule == policy.RulePullRequestBudget && shown:
		return fmt.Sprintf(" This %s's runs cost %s of the %s budget. %s", request, Money(budgetCurrency, result.CostUSD), Money(budgetCurrency, result.LimitUSD), totalBases(bases))
	case result.Rule == policy.RulePullRequestBudget:
		return " This " + request + "'s runs are within the budget. " + totalBases(bases)
	case shown:
		return fmt.Sprintf(" The run cost %s of the %s %s.", Money(budgetCurrency, result.CostUSD), Money(budgetCurrency, result.LimitUSD), noun)
	}
	return fmt.Sprintf(" The run is within the %s.", noun)
}

// policyPage says what lets a rule pass, on each trust path.
const policyPage = "https://docs.costroid.com/policy"

// setBudget names the key that sets a run budget.
const setBudget = "Set `policy.run_budget_usd` to hold each run to a budget."

// onAgentRows reports a neutral verdict that only the agent's own rows kept
// from success: each rule that ran passed, or was within its limit on rows
// the agent's job wrote. The attribution rule warns of those same rows, so
// its warning does not count against it. budget says one of the rules is a
// budget rule.
func onAgentRows(verdict policy.Verdict) (neutral, budget bool) {
	if verdict.Conclusion != policy.Neutral {
		return false, false
	}
	for _, result := range verdict.Results {
		switch {
		case result.Outcome == policy.Pass, result.Rule == policy.RuleAttribution:
		case result.Outcome == policy.Unproven && result.Reason == policy.ReasonUnverifiedRows:
			neutral = true
			budget = budget || slices.Contains(budgetRules, result.Rule)
		default:
			return false, false
		}
	}
	return neutral, budget
}

// passNotes say what a budget kept on the agent's own rows would conclude
// when broken, and which page says what lets a rule pass. They are empty for
// any verdict but one only the agent's own rows kept from success.
func passNotes(verdict policy.Verdict) []string {
	neutral, budget := onAgentRows(verdict)
	if !neutral {
		return nil
	}
	var notes []string
	rule := "rule"
	if budget {
		rule = "budget"
		over := "Over budget, the rule would fail."
		if verdict.WarnOnly.Active {
			over = "Over budget, the rule would warn until the warn-only period ends, then fail."
		}
		notes = append(notes, over)
	}
	return append(notes, "[The policy page]("+policyPage+") says what lets a "+rule+" pass.")
}

// allowlistSentence names the models off the list as code spans, each through
// the allowlist every pull request string passes.
func allowlistSentence(result policy.Result) string {
	switch result.Reason {
	case policy.ReasonOutsideAllowlist:
		names := make([]string, 0, len(result.Models))
		for _, model := range result.Models {
			names = append(names, Clean(model).Code())
		}
		switch len(names) {
		case 0:
			return "the run used a model that is not on the list."
		case 1:
			return "the run used " + names[0] + ", which is not on the list."
		}
		return "the run used " + strings.Join(names, ", ") + ", which are not on the list."
	case policy.ReasonOnAllowlist:
		return "every row names a model on the list."
	case policy.ReasonUnreadableModel:
		return "a row names no model this check can read, so the rule cannot pass."
	case policy.ReasonUnverifiedRows:
		return "unchecked rows cannot show that every model was on the list."
	case policy.ReasonUnattributedRows:
		return "unattributed rows cannot show that every model was on the list."
	}
	return "this check read no rows, so it cannot pass."
}

// increaseSentence states a run against the median of earlier runs, or is
// empty for a reason every rule states alike.
func increaseSentence(result policy.Result, shown bool) string {
	allowed := "the allowed increase"
	if result.LimitPercent != "" {
		allowed = result.LimitPercent + "%"
	}
	amounts := shown && result.CostUSD != "" && result.BaselineUSD != ""
	if amounts && result.LimitUSD != "" {
		allowed = Money(budgetCurrency, result.LimitUSD)
	}
	switch result.Reason {
	case policy.ReasonOverBaseline:
		if amounts {
			return fmt.Sprintf("%s is more than %s over the %s median of earlier runs.", Money(budgetCurrency, result.CostUSD), allowed, Money(budgetCurrency, result.BaselineUSD))
		}
		return fmt.Sprintf("the run is more than %s over the median of earlier runs.", allowed)
	case policy.ReasonWithinLimit:
		if amounts {
			return fmt.Sprintf("%s is at most %s over the %s median of earlier runs.", Money(budgetCurrency, result.CostUSD), allowed, Money(budgetCurrency, result.BaselineUSD))
		}
		return fmt.Sprintf("the run is at most %s over the median of earlier runs.", allowed)
	case policy.ReasonNoBaseline:
		return fmt.Sprintf("no baseline of %d or more earlier runs reached this check, so the rule did not run.", policy.MinBaselineRuns)
	}
	return ""
}

// p95Sentence states a run against its P95 limit, or is empty for a reason
// every rule states alike.
func p95Sentence(result policy.Result, shown bool) string {
	run := "the run"
	if shown && result.CostUSD != "" && result.P95USD != "" {
		run = Money(budgetCurrency, result.CostUSD)
	}
	switch result.Reason {
	case policy.ReasonOverBaseline:
		return run + " is over " + p95Limit(result, shown) + "."
	case policy.ReasonWithinLimit:
		return run + " is within " + p95Limit(result, shown) + "."
	case policy.ReasonNoBaseline:
		return fmt.Sprintf("%d earlier runs of this agent reached this check, and the rule needs %d, so it did not run.", baselineRuns(result), policy.MinP95Runs)
	}
	return ""
}

// pullRequestSentence states the pull request's total against its budget, or
// is empty for a reason every rule states alike.
func pullRequestSentence(result policy.Result, shown bool, bases []string, request string) string {
	amounts := shown && result.CostUSD != "" && result.LimitUSD != ""
	switch result.Reason {
	case policy.ReasonOverLimit:
		if amounts {
			return fmt.Sprintf("%s over this %s's runs is over the %s budget. %s", Money(budgetCurrency, result.CostUSD), request, Money(budgetCurrency, result.LimitUSD), totalBases(bases))
		}
		return "this " + request + "'s runs are over the budget. " + totalBases(bases)
	case policy.ReasonWithinLimit:
		if amounts {
			return fmt.Sprintf("%s over this %s's runs is within the %s budget. %s", Money(budgetCurrency, result.CostUSD), request, Money(budgetCurrency, result.LimitUSD), totalBases(bases))
		}
		return "this " + request + "'s runs are within the budget. " + totalBases(bases)
	case policy.ReasonNoLifetime:
		return "this check read no total for the " + request + "'s earlier runs, so the rule did not run."
	case policy.ReasonRepeatPossible:
		return "the " + request + "'s total may count a report twice, so it cannot show the budget broken or kept."
	}
	return ""
}

// basisOrder is the order totalBases names the bases in.
var basisOrder = []string{"measured", "list_equivalent", "billed", "estimate", "derived"}

// lifetimeBases are the bases of the pull request's USD figures, which the
// pull request budget adds together, in basisOrder. Empty when no lifetime
// was read.
func (i Input) lifetimeBases() []string {
	if i.Lifetime == nil {
		return nil
	}
	var bases []string
	for _, basis := range basisOrder {
		for _, figure := range i.Lifetime.Figures {
			if figure.Currency == budgetCurrency && figure.Basis == basis {
				bases = append(bases, basis)
				break
			}
		}
	}
	return bases
}

// totalBases says which bases the pull request's total adds, so the total
// never reads as one basis when it holds several.
func totalBases(bases []string) string {
	switch len(bases) {
	case 0:
		return "The total adds every basis together, at list."
	case 1:
		return "The total is at list (" + basisLabels[bases[0]] + ")."
	}
	labels := make([]string, len(bases))
	for index, basis := range bases {
		labels[index], _, _ = strings.Cut(basisLabels[basis], ",")
	}
	return "The total adds " + listed(labels, 0) + " together, at list."
}

// enforceNow is the one line that ends the warn-only period.
const enforceNow = "Add `enforce: true` under `policy` in `costroid.yml` to enforce now."

func warnOnlySentence(period policy.WarnOnly) string {
	switch period.Reason {
	case policy.PeriodNotOnBase:
		return "Warn-only: this workflow is not on the base branch yet, so a rule that fails warns. " + enforceNow
	case policy.PeriodRunning:
		ends, err := time.Parse(time.RFC3339, period.Ends)
		if err == nil {
			return fmt.Sprintf("Warn-only until %s: for %d days from the commit that added this workflow to the base branch, a rule that fails warns. %s",
				ends.UTC().Format(instantLayout), int(policy.WarnOnlyPeriod.Hours()/24), enforceNow)
		}
	}
	return "Warn-only: the commit that added this workflow to the base branch could not be read, so a rule that fails warns. " + enforceNow
}
