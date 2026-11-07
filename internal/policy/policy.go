// Package policy decides the check's conclusion. It evaluates the built-in Rego
// pack over one run's rows and the settings that costroid.yml states at the
// base commit, under a capabilities file that admits no network and no clock.
package policy

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"time"
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
// conclude neutral, and the verdict says why. The error is for a pack that
// could not run.
func Evaluate(ctx context.Context, request Request) (Verdict, error) {
	request.Trust = trusted(request.Trust)
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
		WarnOnly:       warnOnly(settings.Enforce, request.Start, request.At),
		Results:        []Result{},
		JoinableRows:   request.Usage.joinable(),
		problem:        problem,
	}
	if problem != nil {
		return verdict, nil
	}
	answer, err := evaluatePack(ctx, packInput(request, settings, verdict.WarnOnly.Active))
	if err != nil {
		return Verdict{}, err
	}
	verdict.Conclusion = answer.Conclusion
	verdict.Results = annotate(answer.Results, request, settings)
	return verdict, nil
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
		case result.Reason == ReasonNoBaseline || result.Reason == ReasonNoLifetime:
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
