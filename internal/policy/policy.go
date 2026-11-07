// Package policy decides the check's conclusion. It evaluates the built-in Rego
// pack over one run's rows and the settings that costroid.yml states at the
// base commit, under a capabilities file that admits no network and no clock.
package policy

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
)

// Trust says who vouches for the rows' run identity.
type Trust string

// The two trust paths.
const (
	// Unverified rows were written by the agent's own job, so their tags are
	// claims.
	Unverified Trust = "unverified"
	// TokenVerified rows were checked by a ledger against the job's OIDC token.
	TokenVerified Trust = "token_verified"
)

// Evidence says what vouches for the spend on rows a ledger checked against a
// CI token. The token proves which run sent the rows. The token counts on them
// still come from the agent's job, so they are agent counts until the
// gateway's own count or a provider's report stands behind them.
type Evidence string

// The kinds of evidence for a token-verified run's spend.
const (
	// AgentCounts is the counts the agent's job sent, and nothing else.
	AgentCounts Evidence = "agent_counts"
	// GatewayMeasured is the gateway's own count for keys attested as minted
	// for the rows' runs, as costroid gateway close attested it.
	GatewayMeasured Evidence = "gateway_measured"
	// Reconciled is a provider's report.
	Reconciled Evidence = "reconciled"
)

// WarnOnlyPullRequests is how many pull requests with a measured cost end the
// warn-only period, if its 7 days have not ended it first.
const WarnOnlyPullRequests = 20

// The conclusions a check takes.
const (
	Success = "success"
	Neutral = "neutral"
	Failure = "failure"
)

// The rules of the default pack, in the order a verdict lists them.
const (
	RuleRunBudget          = "run_budget"
	RuleRunCostThreshold   = "run_cost_threshold"
	RuleRunIncrease        = "run_increase"
	RuleRunIncreasePercent = "run_increase_percent"
	RulePullRequestBudget  = "pr_budget"
	RuleMonthlyBudget      = "monthly_budget"
	RuleModelAllowlist     = "model_allowlist"
	RuleAttribution        = "attribution"
)

var ruleOrder = []string{
	RuleRunBudget, RuleRunCostThreshold, RuleRunIncrease, RuleRunIncreasePercent,
	RulePullRequestBudget, RuleMonthlyBudget, RuleModelAllowlist, RuleAttribution,
}

// runCostRules hold this run's own cost to a limit, so their results carry it.
var runCostRules = []string{RuleRunBudget, RuleRunCostThreshold, RuleRunIncrease, RuleRunIncreasePercent}

// measuredRules pass on token-verified rows only when measured or reconciled
// spend stands behind them: the rules that hold a cost to a limit, and the
// model allowlist, whose model names the agent's job stated beside its counts.
var measuredRules = append(slices.Clone(runCostRules), RulePullRequestBudget, RuleMonthlyBudget, RuleModelAllowlist)

// runRuleKeys are the settings of the rules that read one run's rows, by rule.
var runRuleKeys = map[string]string{
	RuleRunBudget:          keyRunBudget,
	RuleRunCostThreshold:   keyMaxRunCost,
	RuleRunIncrease:        keyMaxIncrease,
	RuleRunIncreasePercent: keyMaxIncreasePercent,
}

// The outcomes of a rule.
const (
	Pass     = "pass"
	Warn     = "warn"
	Fail     = "fail"
	Unproven = "unproven"
)

// The reasons a rule gives.
const (
	ReasonOverLimit        = "over_limit"
	ReasonWithinLimit      = "within_limit"
	ReasonNoRows           = "no_rows"
	ReasonUnverifiedRows   = "unverified_rows"
	ReasonUnattributedRows = "unattributed_rows"
	ReasonOtherCurrency    = "other_currency"
	ReasonJoinableRows     = "joinable_rows"
	ReasonOverBaseline     = "over_baseline"
	ReasonNoBaseline       = "no_baseline"
	ReasonNoLifetime       = "no_lifetime"
	ReasonRepeatPossible   = "repeat_possible"
	ReasonNoLedger         = "no_ledger"
	ReasonOutsideAllowlist = "model_outside_allowlist"
	ReasonUnreadableModel  = "unreadable_model"
	ReasonOnAllowlist      = "models_on_allowlist"
	// ReasonUnmeasuredSpend is a budget or the model allowlist held by
	// token-verified rows whose spend only the agent's job counted.
	ReasonUnmeasuredSpend = "unmeasured_spend"
	// ReasonNoRunRows is a run rule asked of a ledger, whose answer covers the
	// pull request and not one run.
	ReasonNoRunRows = "no_run_rows"
)

var outcomes = []string{Pass, Warn, Fail, Unproven}

var reasons = []string{
	ReasonOverLimit, ReasonWithinLimit, ReasonNoRows, ReasonUnverifiedRows,
	ReasonUnattributedRows, ReasonOtherCurrency, ReasonJoinableRows,
	ReasonOverBaseline, ReasonNoBaseline, ReasonNoLifetime, ReasonRepeatPossible,
	ReasonNoLedger, ReasonOutsideAllowlist, ReasonUnreadableModel, ReasonOnAllowlist,
}

// Request is everything one evaluation reads. The pack reads no clock, so At
// is the time of the check.
type Request struct {
	Usage  Usage
	Trust  Trust
	Config ConfigFile
	Start  Start
	At     time.Time
	// Baseline is what earlier runs of the agent cost. Nil, the increase
	// rules cannot run.
	Baseline *Baseline
	// Lifetime is what the pull request's earlier runs cost. Nil, the pull
	// request budget cannot run.
	Lifetime *Lifetime
	// MeasuredPullRequests counts the repository's pull requests with a
	// measured cost. At WarnOnlyPullRequests the warn-only period ends. A
	// count can only end the period early, so an unverified one is safe.
	MeasuredPullRequests int
	// Evidence is what stands behind the spend of token-verified rows. Only
	// GatewayMeasured and Reconciled let a budget rule or the model allowlist
	// pass on them. Anything else reads as AgentCounts.
	Evidence Evidence
}

// Verdict is the pack's answer and what it was computed from.
type Verdict struct {
	Conclusion string `json:"conclusion"`
	Trust      Trust  `json:"trust"`
	PackSHA256 string `json:"pack_sha256"`
	// ConfigSHA256 hashes the costroid.yml bytes read. A missing file hashes
	// as zero bytes.
	ConfigSHA256 string      `json:"config_sha256"`
	ConfigRef    string      `json:"config_ref,omitempty"`
	Config       ConfigState `json:"config"`
	// ConfigFromBase says the settings are a pull request's base commit's, so
	// a change the pull request makes to them waits for the merge.
	ConfigFromBase bool     `json:"config_from_base"`
	EvaluatedAt    string   `json:"evaluated_at"`
	WarnOnly       WarnOnly `json:"warn_only"`
	Results        []Result `json:"results"`
	// JoinableRows counts the rows the attribution rule names.
	JoinableRows int `json:"joinable_rows"`
	// Evidence is what vouched for the spend of token-verified rows, as a
	// ledger states it. A verdict on rows the agent's job wrote states none,
	// and so does one from Evaluate.
	Evidence Evidence `json:"evidence,omitempty"`

	problem error
}

// Result is one rule's outcome. LimitUSD, CostUSD and BaselineUSD are
// amounts, so a comment that withholds amounts drops them.
type Result struct {
	Rule           string `json:"rule"`
	Outcome        string `json:"outcome"`
	Reason         string `json:"reason"`
	HeldByWarnOnly bool   `json:"held_by_warn_only,omitempty"`
	LimitUSD       string `json:"limit_usd,omitempty"`
	// LimitPercent is the increase over the baseline a run may reach, in
	// percent.
	LimitPercent string `json:"limit_percent,omitempty"`
	// CostUSD is the run's USD cost, or the pull request's for pr_budget.
	CostUSD string `json:"cost_usd,omitempty"`
	// BaselineUSD is the median the increase rules compare the run with.
	BaselineUSD string `json:"baseline_usd,omitempty"`
	// Models names the models off the allowlist, sorted, at most 10.
	Models []string `json:"models,omitempty"`
}

// ConfigProblem says why the settings could not be used, nil when they were.
func (v Verdict) ConfigProblem() error {
	return v.problem
}

// WithoutAmounts returns the verdict with every amount dropped.
func (v Verdict) WithoutAmounts() Verdict {
	v.Results = slices.Clone(v.Results)
	for index := range v.Results {
		v.Results[index].LimitUSD = ""
		v.Results[index].CostUSD = ""
		v.Results[index].BaselineUSD = ""
	}
	return v
}

// Evaluate runs the default pack. Settings that cannot be read run no rule and
// conclude neutral, and the verdict says why. On token-verified rows a budget
// rule or the model allowlist passes only on the evidence the request states.
// The error is for a pack that could not run.
func Evaluate(ctx context.Context, request Request) (Verdict, error) {
	return evaluateScoped(ctx, request, false)
}

// evaluateScoped runs the pack. On token-verified rows a budget rule or the
// model allowlist passes only on measured or reconciled spend. A ledger's
// request covers a pull request: its run rules are withheld from the pack and
// conclude unproven, and its verdict names the evidence.
func evaluateScoped(ctx context.Context, request Request, ledger bool) (Verdict, error) {
	request.Trust = trusted(request.Trust)
	evidence := measured(request.Evidence)
	settings, problem := request.Config.settings()
	verdict := Verdict{
		Conclusion:     Neutral,
		Trust:          request.Trust,
		PackSHA256:     PackSHA256(),
		ConfigSHA256:   digest(request.Config.Data),
		ConfigRef:      request.Config.Ref,
		Config:         request.Config.state(problem),
		ConfigFromBase: request.Config.FromBase,
		EvaluatedAt:    request.At.UTC().Format(time.RFC3339),
		WarnOnly:       endAtPullRequests(warnOnly(settings.Enforce, request.Start, request.At), request.MeasuredPullRequests),
		Results:        []Result{},
		JoinableRows:   request.Usage.joinable(),
		problem:        problem,
	}
	if ledger && request.Trust == TokenVerified {
		verdict.Evidence = evidence
	}
	if problem != nil {
		return verdict, nil
	}
	var withheld []Result
	if ledger {
		settings, withheld = withoutRunRules(settings)
	}
	answer, err := evaluatePack(ctx, packInput(request, settings, verdict.WarnOnly.Active))
	if err != nil {
		return Verdict{}, err
	}
	results := append(answer.Results, withheld...)
	conclusion := answer.Conclusion
	if request.Trust == TokenVerified && evidence == AgentCounts && demoteUnmeasured(results) && conclusion == Success {
		conclusion = Neutral
	}
	if len(withheld) > 0 && conclusion == Success {
		conclusion = Neutral
	}
	verdict.Conclusion = conclusion
	verdict.Results = annotate(results, request, settings)
	return verdict, nil
}

// endAtPullRequests ends a running period at the twentieth pull request with a
// measured cost, whichever of that and its 7 days comes first. The reason is
// ended, and no end date is stated, because the date did not end it.
func endAtPullRequests(period WarnOnly, measured int) WarnOnly {
	if !period.Active || measured < WarnOnlyPullRequests {
		return period
	}
	return WarnOnly{Reason: PeriodEnded, Started: period.Started}
}

// measured reads stated evidence. Anything but a measured or reconciled spend
// is the agent's own counts.
func measured(evidence Evidence) Evidence {
	if evidence == GatewayMeasured || evidence == Reconciled {
		return evidence
	}
	return AgentCounts
}

// demoteUnmeasured turns each budget rule's and the model allowlist's pass
// into unproven, because the token counts and model names behind it are the
// agent's own. It reports whether any changed. A rule that warns or fails
// keeps its outcome: what the agent's job wrote can show spend over a limit or
// a model off the list, never the reverse.
func demoteUnmeasured(results []Result) bool {
	demoted := false
	for index := range results {
		result := &results[index]
		if result.Outcome == Pass && slices.Contains(measuredRules, result.Rule) {
			result.Outcome, result.Reason = Unproven, ReasonUnmeasuredSpend
			demoted = true
		}
	}
	return demoted
}

// withoutRunRules removes the run rules' settings, so the pack does not hold a
// pull request's total to a run's limit, and returns each one that was set as
// unproven.
func withoutRunRules(settings Settings) (Settings, []Result) {
	settings.Amounts = maps.Clone(settings.Amounts)
	var withheld []Result
	for _, rule := range runCostRules {
		key := runRuleKeys[rule]
		limit, set := settings.Amounts[key]
		if !set {
			continue
		}
		delete(settings.Amounts, key)
		result := Result{Rule: rule, Outcome: Unproven, Reason: ReasonNoRunRows}
		if rule == RuleRunIncreasePercent {
			result.LimitPercent = limit.String()
		} else {
			result.LimitUSD = limit.String()
		}
		withheld = append(withheld, result)
	}
	return settings, withheld
}

// LedgerLine is a group of one pull request's rows as a ledger holds them.
type LedgerLine struct {
	Currency string
	ListCost decimal.Decimal
	Rows     int64
	// Model is the rows' model, empty when they state none.
	Model string
	// Verified says the ledger checked the rows' run identity against the
	// job's CI token.
	Verified bool
	// Attributed says the ledger tied the rows to their run, windowed or
	// exact.
	Attributed bool
}

// LedgerRequest is what costroid serve reads for one pull request. The ledger
// holds the rows. The caller states what serve cannot read: the settings at
// the base commit, the warn-only period's start and the count of measured pull
// requests.
type LedgerRequest struct {
	Lines    []LedgerLine
	Evidence Evidence
	// MeasuredUSD is the gateway's own count for the pull request's keys. It
	// is read only with GatewayMeasured, and pr_budget is then held to the
	// larger of it and the rows' USD list cost.
	MeasuredUSD          decimal.Decimal
	Config               ConfigFile
	Start                Start
	MeasuredPullRequests int
	At                   time.Time
}

// EvaluateLedger runs the default pack over a pull request's rows in a ledger.
// The rows are token-verified only when every line is. Their spend is agent
// counts unless the request says it was measured or reconciled, and only then
// can a budget rule or the model allowlist pass. The answer covers the whole
// pull request, so the rules that read one run's rows conclude unproven.
func EvaluateLedger(ctx context.Context, request LedgerRequest) (Verdict, error) {
	usage := ledgerUsage(request.Lines)
	trust := Unverified
	if len(request.Lines) > 0 && !slices.ContainsFunc(request.Lines, func(line LedgerLine) bool { return !line.Verified }) {
		trust = TokenVerified
	}
	rowsUSD, _ := usage.usd()
	lifetime := &Lifetime{Trust: trust, OtherCurrency: slices.ContainsFunc(usage.Currencies, func(entry CurrencyUsage) bool {
		return entry.Currency != "USD"
	})}
	if request.Evidence == GatewayMeasured && request.MeasuredUSD.Cmp(rowsUSD) > 0 {
		lifetime.USD = request.MeasuredUSD.Sub(rowsUSD)
	}
	return evaluateScoped(ctx, Request{
		Usage:                usage,
		Trust:                trust,
		Config:               request.Config,
		Start:                request.Start,
		At:                   request.At,
		Lifetime:             lifetime,
		MeasuredPullRequests: request.MeasuredPullRequests,
		Evidence:             request.Evidence,
	}, true)
}

// ledgerUsage reduces a ledger's lines to what the pack reads. A line the
// ledger did not attribute counts as unattributed rows, which never satisfy a
// rule.
func ledgerUsage(lines []LedgerLine) Usage {
	totals := map[string]*CurrencyUsage{}
	usage := Usage{}
	for _, line := range lines {
		entry, found := totals[line.Currency]
		if !found {
			entry = &CurrencyUsage{Currency: line.Currency, Models: map[string]int{}}
			totals[line.Currency] = entry
		}
		rows := int(line.Rows)
		usage.Rows += rows
		entry.Rows += rows
		entry.ListCost = entry.ListCost.Add(line.ListCost)
		if !line.Attributed {
			entry.UnattributedRows += rows
		}
		if len(line.Model) <= maxModelLength && modelPattern.MatchString(line.Model) {
			entry.Models[line.Model] += rows
		} else {
			entry.UnreadableModels += rows
		}
	}
	for _, entry := range totals {
		usage.Currencies = append(usage.Currencies, *entry)
	}
	slices.SortFunc(usage.Currencies, func(a, b CurrencyUsage) int { return cmp.Compare(a.Currency, b.Currency) })
	return usage
}

// annotate orders the results and adds the figures each was held to: the
// run's USD cost, the baseline's median, the pull request's total, and the
// models off the allowlist.
func annotate(results []Result, request Request, settings Settings) []Result {
	slices.SortFunc(results, func(a, b Result) int {
		return cmp.Compare(slices.Index(ruleOrder, a.Rule), slices.Index(ruleOrder, b.Rule))
	})
	cost, found := request.Usage.usd()
	for index := range results {
		result := &results[index]
		switch {
		case result.Reason == ReasonNoBaseline || result.Reason == ReasonNoLifetime || result.Reason == ReasonNoRunRows:
			// A rule that compared nothing was held to no figure.
		case slices.Contains(runCostRules, result.Rule) && found:
			result.CostUSD = cost.String()
		case result.Rule == RulePullRequestBudget && request.Lifetime != nil:
			result.CostUSD = request.Lifetime.total(request.Usage).String()
		case result.Rule == RuleModelAllowlist && result.Reason == ReasonOutsideAllowlist:
			_, _, result.Models = request.Usage.modelRows(settings)
		}
		increase := result.Rule == RuleRunIncrease || result.Rule == RuleRunIncreasePercent
		if increase && result.Reason != ReasonNoBaseline && request.Baseline != nil {
			result.BaselineUSD = request.Baseline.MedianUSD.String()
		}
	}
	return results
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
