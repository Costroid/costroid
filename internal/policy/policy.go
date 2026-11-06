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
	RuleRunBudget        = "run_budget"
	RuleRunCostThreshold = "run_cost_threshold"
	RuleAttribution      = "attribution"
)

var ruleOrder = []string{RuleRunBudget, RuleRunCostThreshold, RuleAttribution}

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
)

var outcomes = []string{Pass, Warn, Fail, Unproven}

var reasons = []string{
	ReasonOverLimit, ReasonWithinLimit, ReasonNoRows, ReasonUnverifiedRows,
	ReasonUnattributedRows, ReasonOtherCurrency, ReasonJoinableRows,
}

// Request is everything one evaluation reads. The pack reads no clock, so At
// is the time of the check.
type Request struct {
	Usage  Usage
	Trust  Trust
	Config ConfigFile
	Start  Start
	At     time.Time
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

// Result is one rule's outcome. LimitUSD and CostUSD are amounts, so a
// comment that withholds amounts drops them.
type Result struct {
	Rule           string `json:"rule"`
	Outcome        string `json:"outcome"`
	Reason         string `json:"reason"`
	HeldByWarnOnly bool   `json:"held_by_warn_only,omitempty"`
	LimitUSD       string `json:"limit_usd,omitempty"`
	CostUSD        string `json:"cost_usd,omitempty"`
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
	}
	return v
}

// Evaluate runs the default pack. Settings that cannot be read run no rule and
// conclude neutral, and the verdict says why. The error is for a pack that
// could not run.
func Evaluate(ctx context.Context, request Request) (Verdict, error) {
	if request.Trust != TokenVerified {
		request.Trust = Unverified
	}
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
	verdict.Results = annotate(answer.Results, request.Usage)
	return verdict, nil
}

// annotate orders the results and adds the run's USD cost to each budget rule.
func annotate(results []Result, usage Usage) []Result {
	slices.SortFunc(results, func(a, b Result) int {
		return cmp.Compare(slices.Index(ruleOrder, a.Rule), slices.Index(ruleOrder, b.Rule))
	})
	cost, found := usage.usd()
	for index := range results {
		if results[index].LimitUSD != "" && found {
			results[index].CostUSD = cost.String()
		}
	}
	return results
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
