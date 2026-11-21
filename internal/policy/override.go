package policy

import (
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

// LedgerOverrides is a record read from a ledger's own tables of the verdicts
// serve answered and the approvals it verified. It is the one record that can
// demote a rule: a record built from check runs, which any job can post, is a
// claim, and a claim never relaxes a rule.
func LedgerOverrides(failing, approved, approvers int) Overrides {
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
}

// Until is when the term of a demotion from From ends.
func (d Demotion) Until() time.Time {
	return d.From.Add(DemotionTerm)
}

// Demoted reports whether the rule is demoted at a verdict's time.
func (d Demotion) Demoted(at time.Time) bool {
	return !d.From.IsZero() && at.Before(d.Until())
}

// Rearm is the demotion at a verdict, before its record is read. It re-arms at
// once when demotion is turned off or the rule's limit has changed, and at the
// end of a term. A re-armed window starts empty.
func (d Demotion) Rearm(at time.Time, limit decimal.Decimal, on bool) Demotion {
	switch {
	case !on, d.Limit.Cmp(limit) != 0:
		return Demotion{Limit: limit, Rearmed: at}
	case !d.From.IsZero() && !at.Before(d.Until()):
		return Demotion{Limit: limit, Rearmed: d.Until()}
	}
	return d
}

// Record is the demotion after a verdict's record is read. The first verdict
// whose record demotes the rule starts the term, and a later one leaves it as
// it is, so the state does not flap as the window rolls.
func (d Demotion) Record(at time.Time, overrides Overrides) Demotion {
	if d.From.IsZero() && overrides.demotes() {
		d.From = at
	}
	return d
}
