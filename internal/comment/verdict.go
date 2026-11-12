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

var ruleLabels = map[string]string{
	policy.RuleRunBudget:          "Run budget",
	policy.RuleRunCostThreshold:   "Run cost threshold",
	policy.RuleRunIncrease:        "Increase over baseline",
	policy.RuleRunIncreasePercent: "Percent increase over baseline",
	policy.RulePullRequestBudget:  "Pull request budget",
	policy.RuleMonthlyBudget:      "Monthly budget",
	policy.RuleModelAllowlist:     "Model allowlist",
	policy.RuleAttribution:        "Attribution",
}

var limitNouns = map[string]string{
	policy.RuleRunBudget:          "budget",
	policy.RuleRunCostThreshold:   "threshold",
	policy.RuleRunIncrease:        "limit",
	policy.RuleRunIncreasePercent: "limit",
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

// verdictLine opens with the approval when one turns the check neutral.
func (i Input) verdictLine(shown bool) string {
	if i.Verdict == nil {
		return "No policy ran on this change."
	}
	line := strings.Join(verdictSentences(*i.Verdict, shown, i.lifetimeBases(), i.configRead(), true), " ")
	if i.approved() {
		return approvalLine(*i.Approval) + " " + line
	}
	return line
}

// LocalConfig names a costroid.yml read from the working tree at path: the
// default file as the one in this directory, any other by its path.
func LocalConfig(path string) string {
	if filepath.Clean(path) == "costroid.yml" {
		return directoryConfig
	}
	return "`" + path + "`"
}

// VerdictSentences says what the policy found, one sentence per fact, in the
// words the comment uses. shown says whether amounts may be printed, and
// config names the costroid.yml the verdict read, as LocalConfig does. It
// reads no lifetime, so a pull request's total is said to add every basis. It
// carries no link, so a terminal can print it.
func VerdictSentences(verdict policy.Verdict, shown bool, config string) []string {
	return verdictSentences(verdict, shown, nil, config, false)
}

// verdictSentences is built from the verdict's closed fields and its amounts,
// so no text from the pull request reaches it. It names the bases the pull
// request's total adds, in basisOrder, or every basis when bases is empty,
// and config as the costroid.yml the verdict read. linked adds the sentences
// that say why a verdict on the agent's own rows is neutral, with their link.
func verdictSentences(verdict policy.Verdict, shown bool, bases []string, config string, linked bool) []string {
	var sentences []string
	switch verdict.Config {
	case policy.ConfigInvalid:
		sentences = append(sentences, "No rule ran, so the check concludes neutral: "+config+" is not valid, and `costroid validate` names the problem.")
	case policy.ConfigUnread:
		sentences = append(sentences, "No rule ran, so the check concludes neutral: "+config+" could not be read, and a missing input never blocks a merge.")
	}
	held := false
	for _, result := range verdict.Results {
		sentences = append(sentences, resultSentence(result, verdict.JoinableRows, shown, bases))
		held = held || result.HeldByWarnOnly
	}
	if len(sentences) == 0 {
		sentences = append(sentences, "No rule applied: "+config+" sets no budget and no threshold. "+setBudget)
	}
	if linked {
		sentences = append(sentences, trustSentences(verdict)...)
	}
	if held {
		sentences = append(sentences, warnOnlySentence(verdict.WarnOnly))
	}
	if verdict.ConfigFromBase {
		sentences = append(sentences, "A change to `costroid.yml` in this pull request applies after the merge, because the verdict reads the base branch's file.")
	}
	return sentences
}

func resultSentence(result policy.Result, joinable int, shown bool, bases []string) string {
	label, noun := ruleLabels[result.Rule], limitNouns[result.Rule]
	switch {
	case result.Rule == policy.RuleModelAllowlist:
		return label + ": " + allowlistSentence(result)
	case result.Rule == policy.RuleRunIncrease || result.Rule == policy.RuleRunIncreasePercent:
		if sentence := increaseSentence(result, shown); sentence != "" {
			return label + ": " + sentence
		}
	case result.Rule == policy.RulePullRequestBudget:
		if sentence := pullRequestSentence(result, shown, bases); sentence != "" {
			return label + ": " + sentence
		}
	}
	amounts := shown && result.CostUSD != "" && result.LimitUSD != ""
	switch result.Reason {
	case policy.ReasonOverLimit:
		if amounts {
			return fmt.Sprintf("%s: USD %s is over the USD %s %s.", label, result.CostUSD, result.LimitUSD, noun)
		}
		return fmt.Sprintf("%s: the run is over the %s.", label, noun)
	case policy.ReasonWithinLimit:
		if amounts {
			return fmt.Sprintf("%s: USD %s is within the USD %s %s.", label, result.CostUSD, result.LimitUSD, noun)
		}
		return fmt.Sprintf("%s: the run is within the %s.", label, noun)
	case policy.ReasonUnverifiedRows:
		return label + ": unverified rows cannot satisfy a budget." + standing(result, shown, bases)
	case policy.ReasonUnattributedRows:
		return label + ": unattributed rows cannot satisfy a budget." + standing(result, shown, bases)
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
	return fmt.Sprintf("%s: %d %s a credential, a pull request and a head commit, and stay unattributed until a ledger checks them against the job's OIDC token.", label, joinable, rows)
}

// budgetRules are the rules that hold a figure to a USD budget or threshold.
var budgetRules = []string{policy.RuleRunBudget, policy.RuleRunCostThreshold, policy.RulePullRequestBudget}

// standing says where a budget rule's figure stands against its limit, when
// the rows kept the rule from passing although the figure was within it. It
// is empty for any other rule, and for a rule that held no figure. The pack
// reaches these reasons only after it found the figure not over the limit.
func standing(result policy.Result, shown bool, bases []string) string {
	if !slices.Contains(budgetRules, result.Rule) || result.CostUSD == "" || result.LimitUSD == "" {
		return ""
	}
	noun := limitNouns[result.Rule]
	switch {
	case result.Rule == policy.RulePullRequestBudget && shown:
		return fmt.Sprintf(" This pull request's runs cost USD %s of the USD %s budget. %s", result.CostUSD, result.LimitUSD, totalBases(bases))
	case result.Rule == policy.RulePullRequestBudget:
		return " This pull request's runs are within the budget. " + totalBases(bases)
	case shown:
		return fmt.Sprintf(" The run cost USD %s of the USD %s %s.", result.CostUSD, result.LimitUSD, noun)
	}
	return fmt.Sprintf(" The run is within the %s.", noun)
}

// policyPage says what lets a rule pass, on each trust path.
const policyPage = "https://docs.costroid.com/policy"

// setBudget names the key that sets a run budget.
const setBudget = "Set `policy.run_budget_usd` to hold each run to a budget."

// onAgentRows reports a neutral verdict that only the agent's own rows kept
// from success: each rule that ran passed, or was within its limit on rows
// the agent's job wrote. budget says one of those is a budget rule.
func onAgentRows(verdict policy.Verdict) (neutral, budget bool) {
	if verdict.Conclusion != policy.Neutral {
		return false, false
	}
	for _, result := range verdict.Results {
		switch {
		case result.Outcome == policy.Pass:
		case result.Outcome == policy.Unproven && result.Reason == policy.ReasonUnverifiedRows:
			neutral = true
			budget = budget || slices.Contains(budgetRules, result.Rule)
		default:
			return false, false
		}
	}
	return neutral, budget
}

// trustSentences say that a verdict is neutral because the agent's own job
// wrote the rows, what a budget broken on them concludes, and which page says
// what lets a rule pass. They are empty for any other verdict.
func trustSentences(verdict policy.Verdict) []string {
	neutral, budget := onAgentRows(verdict)
	if !neutral {
		return nil
	}
	sentences := []string{"The verdict is neutral because the agent's own job wrote the rows."}
	rule := "rule"
	if budget {
		rule = "budget"
		over := "Over budget, the rule would fail."
		if verdict.WarnOnly.Active {
			over = "Over budget, the rule would warn until the warn-only period ends, then fail."
		}
		sentences = append(sentences, over)
	}
	return append(sentences, "[The policy page]("+policyPage+") says what lets a "+rule+" pass.")
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
		return "unverified rows cannot show that every model was on the list."
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
		allowed = "USD " + result.LimitUSD
	}
	switch result.Reason {
	case policy.ReasonOverBaseline:
		if amounts {
			return fmt.Sprintf("USD %s is more than %s over the USD %s median of earlier runs.", result.CostUSD, allowed, result.BaselineUSD)
		}
		return fmt.Sprintf("the run is more than %s over the median of earlier runs.", allowed)
	case policy.ReasonWithinLimit:
		if amounts {
			return fmt.Sprintf("USD %s is at most %s over the USD %s median of earlier runs.", result.CostUSD, allowed, result.BaselineUSD)
		}
		return fmt.Sprintf("the run is at most %s over the median of earlier runs.", allowed)
	case policy.ReasonNoBaseline:
		return fmt.Sprintf("no baseline of %d or more earlier runs reached this check, so the rule did not run.", policy.MinBaselineRuns)
	}
	return ""
}

// pullRequestSentence states the pull request's total against its budget, or
// is empty for a reason every rule states alike.
func pullRequestSentence(result policy.Result, shown bool, bases []string) string {
	amounts := shown && result.CostUSD != "" && result.LimitUSD != ""
	switch result.Reason {
	case policy.ReasonOverLimit:
		if amounts {
			return fmt.Sprintf("USD %s over this pull request's runs is over the USD %s budget. %s", result.CostUSD, result.LimitUSD, totalBases(bases))
		}
		return "this pull request's runs are over the budget. " + totalBases(bases)
	case policy.ReasonWithinLimit:
		if amounts {
			return fmt.Sprintf("USD %s over this pull request's runs is within the USD %s budget. %s", result.CostUSD, result.LimitUSD, totalBases(bases))
		}
		return "this pull request's runs are within the budget. " + totalBases(bases)
	case policy.ReasonNoLifetime:
		return "this check read no total for the pull request's earlier runs, so the rule did not run."
	case policy.ReasonRepeatPossible:
		return "the pull request's total may count a report twice, so it cannot show the budget broken or kept."
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
				ends.UTC().Format("2006-01-02 15:04 UTC"), int(policy.WarnOnlyPeriod.Hours()/24), enforceNow)
		}
	}
	return "Warn-only: the commit that added this workflow to the base branch could not be read, so a rule that fails warns. " + enforceNow
}
