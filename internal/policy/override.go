package policy

import (
	"context"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
)

// The override rate's least sample: below either count the rate decides
// nothing, and the rule fails as written.
const (
	overrideLeastFailing   = 10
	overrideLeastApprovers = 2
)

// DemotionTerm is how long a demoted rule warns, from the verdict that first
// crossed the threshold.
const DemotionTerm = 30 * 24 * time.Hour

// demotable says, for every rule Costroid has, whether its record can demote
// it. The six rules that hold a cost to a limit can be. The model allowlist
// names what may run, not what a run costs, the monthly budget and attribution
// never fail, and customer_pack says why a repository's own pack gave no
// results. A rule this list does not name, such as a customer pack's own, is
// never demoted.
var demotable = map[string]bool{
	RuleRunBudget:          true,
	RuleRunCostThreshold:   true,
	RuleRunIncrease:        true,
	RuleRunIncreasePercent: true,
	RuleRunP95:             true,
	RulePullRequestBudget:  true,
	RuleMonthlyBudget:      false,
	RuleModelAllowlist:     false,
	RuleAttribution:        false,
	RuleCustomerPack:       false,
}

// Demotable reports whether a rule's record can demote it.
func Demotable(rule string) bool {
	return demotable[rule]
}

// Overrides is a rule's record over its window: the pull requests on which it
// failed, how many of them an approval overrode, and how many accounts gave
// those approvals.
type Overrides struct {
	Failing   int
	Approved  int
	Approvers int
	verified  bool
}

// ledgerOverrides is a record read through an OverrideLedger, a ledger's own
// tables of the verdicts serve answered and the approvals it verified. It is
// the one record that can demote a rule, and only EvaluateLedger reads one: a
// record built from check runs, which any job can post, or from anything a
// request states, is a claim, and a claim never relaxes a rule.
func ledgerOverrides(failing, approved, approvers int) Overrides {
	return Overrides{Failing: failing, Approved: approved, Approvers: approvers, verified: true}
}

// demotes reports whether a rule's record demotes it: a verified record of at
// least 10 pull requests and 2 approvers, overridden more than one time in
// five.
func (o Overrides) demotes() bool {
	return o.verified && o.Failing >= overrideLeastFailing && o.Approvers >= overrideLeastApprovers &&
		o.Approved*5 > o.Failing
}

// Demotion is where a rule's demotion stands in one repository. A rule with no
// demotion kept yet is Demotion{Limit: its limit}: armed since the record
// began.
type Demotion struct {
	// Limit is the limit the window counts failures under.
	Limit decimal.Decimal
	// Rearmed is when the window last started empty.
	Rearmed time.Time
	// From is the time of the verdict that first crossed the threshold, and
	// zero while the rule is armed.
	From time.Time
	// Crossed is the record that crossed the threshold at From.
	Crossed Overrides
	// Off says the last verdict that read the rule's demotion read
	// policy.auto_demote: false.
	Off bool
}

// Until is when the term of a demotion from From ends.
func (d Demotion) Until() time.Time {
	return d.From.Add(DemotionTerm)
}

// Demoted reports whether the rule is demoted at a verdict's time: a verified
// record crossed the threshold, and the term it started has not ended.
func (d Demotion) Demoted(at time.Time) bool {
	return d.Crossed.demotes() && at.Before(d.Until())
}

// Rearm is the demotion at a verdict, before its record is read. It re-arms at
// once when demotion is turned off or the rule's limit has changed, again when
// demotion is turned back on, and at the end of a term. A re-armed window
// starts empty.
func (d Demotion) Rearm(at time.Time, limit decimal.Decimal, on bool) Demotion {
	switch {
	case !on && d.Off && d.Limit.Cmp(limit) == 0:
		return d
	case !on:
		return Demotion{Limit: limit, Rearmed: at, Off: true}
	case d.Off, d.Limit.Cmp(limit) != 0:
		return Demotion{Limit: limit, Rearmed: at}
	case !d.From.IsZero() && !at.Before(d.Until()):
		return Demotion{Limit: limit, Rearmed: d.Until()}
	}
	return d
}

// Record is the demotion after a verdict's record is read. The first verdict
// whose record demotes the rule starts the term and keeps that record, and a
// later one leaves it as it is, so the state does not flap as the window
// rolls.
func (d Demotion) Record(at time.Time, overrides Overrides) Demotion {
	if d.From.IsZero() && !d.Off && overrides.demotes() {
		d.From, d.Crossed = at, overrides
	}
	return d
}

// same reports two demotions that stand alike.
func (d Demotion) same(other Demotion) bool {
	return d.Limit.Cmp(other.Limit) == 0 && d.Rearmed.Equal(other.Rearmed) && d.From.Equal(other.From) &&
		d.Off == other.Off && d.Crossed.Failing == other.Crossed.Failing &&
		d.Crossed.Approved == other.Crossed.Approved && d.Crossed.Approvers == other.Crossed.Approvers
}

// fromLedger is the demotion as a ledger kept it, its record verified: the
// ledger keeps only what a verdict of serve's own computed from its record.
func (d Demotion) fromLedger() Demotion {
	if !d.From.IsZero() {
		d.Crossed = ledgerOverrides(d.Crossed.Failing, d.Crossed.Approved, d.Crossed.Approvers)
	}
	return d
}

// DemotedByOverrideRate is why a rule is demoted: reviewers overrode it more
// than one time in five. The list is closed.
const DemotedByOverrideRate = "override_rate"

// Demoted is the record that demoted a rule, as a result states it: the pull
// requests in the window that crossed the threshold, how many of them an
// approval overrode, and when the demotion ends.
type Demoted struct {
	Reason   string `json:"reason"`
	Approved int    `json:"approved"`
	Failing  int    `json:"failing"`
	Until    string `json:"until"`
}

// OverrideLedger is a ledger's own record of the failing verdicts serve
// answered and the approvals it verified, for the one pull request a verdict
// judges, which it never counts. Only EvaluateLedger reads one, and no field
// of Request holds one, so a verdict on rows a job wrote, on check runs or on
// anything a request states never reaches a record.
type OverrideLedger interface {
	// AddFailure records that rule failed under limit at the verdict's time,
	// on the pull request's head commit.
	AddFailure(ctx context.Context, rule string, limit decimal.Decimal, at time.Time) error
	// Overrides counts the rule's record under limit from rearmed to at.
	Overrides(ctx context.Context, rule string, limit decimal.Decimal, rearmed, at time.Time) (failing, approved, approvers int, err error)
	// Demotion is the rule's demotion as last kept, and false when none is.
	Demotion(ctx context.Context, rule string) (Demotion, bool, error)
	// KeepDemotion keeps the demotion a verdict moved the rule to, and is the
	// one kept: another verdict's when two moved it at once.
	KeepDemotion(ctx context.Context, rule string, demotion Demotion) (Demotion, error)
}

// demotionLimits are the setting each demotable rule is held to: the limit a
// failing verdict records and a window counts under.
var demotionLimits = map[string]string{
	RuleRunBudget:          keyRunBudget,
	RuleRunCostThreshold:   keyMaxRunCost,
	RuleRunIncrease:        keyMaxIncrease,
	RuleRunIncreasePercent: keyMaxIncreasePercent,
	RuleRunP95:             keyMaxP95Factor,
	RulePullRequestBudget:  keyPullRequestBudget,
}

// demote is the one step that lowers an outcome of Costroid's. It runs on
// Costroid's own results, before a customer pack's are joined. Each rule that
// failed is recorded. Under policy.auto_demote: false every rule is re-armed
// and none is demoted. Otherwise a failing rule whose record demotes it warns,
// with that record. It reports whether it lowered any.
func demote(ctx context.Context, record OverrideLedger, settings Settings, results []Result, at time.Time) (bool, error) {
	on, lowered := !settings.DemotionOff, false
	for index := range results {
		result := &results[index]
		limit, set := settings.Amounts[demotionLimits[result.Rule]]
		failed := result.Outcome == Fail
		switch {
		case !set:
			continue
		case failed:
			if err := record.AddFailure(ctx, result.Rule, limit, at); err != nil {
				return false, err
			}
		case on:
			continue
		}
		demotion, err := standing(ctx, record, result.Rule, limit, on, failed, at)
		if err != nil {
			return false, err
		}
		*result = demoted(*result, demotion, at)
		lowered = lowered || result.Demoted != nil
	}
	return lowered, nil
}

// standing is a rule's demotion at a verdict: as the ledger kept it, re-armed
// as the verdict's settings ask, and, for a rule that failed with demotion on,
// with its record read. A demotion the verdict moved is kept.
func standing(ctx context.Context, record OverrideLedger, rule string, limit decimal.Decimal, on, failed bool, at time.Time) (Demotion, error) {
	kept, found, err := record.Demotion(ctx, rule)
	if err != nil {
		return Demotion{}, err
	}
	if !found {
		kept = Demotion{Limit: limit}
	}
	kept = kept.fromLedger()
	now := kept.Rearm(at, limit, on)
	if on && failed {
		failing, approved, approvers, err := record.Overrides(ctx, rule, limit, now.Rearmed, at)
		if err != nil {
			return Demotion{}, err
		}
		now = now.Record(at, ledgerOverrides(failing, approved, approvers))
	}
	if now.same(kept) {
		return now, nil
	}
	now, err = record.KeepDemotion(ctx, rule, now)
	if err != nil {
		return Demotion{}, err
	}
	return now.fromLedger(), nil
}

// demoted is a result as its rule's demotion leaves it at a verdict. It lowers
// one outcome only, a fail to a warn, of a rule that holds a cost to a limit,
// while a verified record's term runs. The reason stays the rule's own.
func demoted(result Result, demotion Demotion, at time.Time) Result {
	switch {
	case !Demotable(result.Rule):
		return result
	case result.Outcome != Fail:
		return result
	case !demotion.Demoted(at):
		return result
	}
	result.Outcome = Warn
	result.Demoted = &Demoted{
		Reason:   DemotedByOverrideRate,
		Approved: demotion.Crossed.Approved,
		Failing:  demotion.Crossed.Failing,
		Until:    demotion.Until().UTC().Format(time.RFC3339),
	}
	return result
}
