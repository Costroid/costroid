package policy

import "time"

// WarnOnlyPeriod is how long after the workflow reached the base branch a rule
// that fails warns instead.
const WarnOnlyPeriod = 7 * 24 * time.Hour

// The reasons the warn-only period is or is not running.
const (
	PeriodRunning      = "in_period"
	PeriodEnded        = "ended"
	PeriodEnforced     = "enforced"
	PeriodNotOnBase    = "not_on_base"
	PeriodStartUnknown = "start_unknown"
)

// Start is when the warn-only period began: the time of the oldest commit on
// the base branch that touched the workflow file.
type Start struct {
	// At is zero when the commit could not be read.
	At time.Time
	// NotOnBase says the base branch holds no such commit yet, as on the pull
	// request that adds the workflow.
	NotOnBase bool
}

// WarnOnly is the period's state at the time of the check.
type WarnOnly struct {
	Active  bool   `json:"active"`
	Reason  string `json:"reason"`
	Started string `json:"started,omitempty"`
	Ends    string `json:"ends,omitempty"`
}

// warnOnly runs the period until it has provably ended. A start that cannot
// be read keeps it running, so a check never fails on a date it did not see.
func warnOnly(enforce bool, start Start, at time.Time) WarnOnly {
	switch {
	case enforce:
		return WarnOnly{Reason: PeriodEnforced}
	case start.NotOnBase:
		return WarnOnly{Active: true, Reason: PeriodNotOnBase}
	case start.At.IsZero():
		return WarnOnly{Active: true, Reason: PeriodStartUnknown}
	}
	ends := start.At.Add(WarnOnlyPeriod)
	period := WarnOnly{
		Reason:  PeriodEnded,
		Started: start.At.UTC().Format(time.RFC3339),
		Ends:    ends.UTC().Format(time.RFC3339),
	}
	if at.Before(ends) {
		period.Active, period.Reason = true, PeriodRunning
	}
	return period
}
