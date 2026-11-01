// Package adapter holds what every agent adapter hands the wrapper.
package adapter

import (
	"context"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

// Grain says what one Request covers.
type Grain string

// The grains an agent can report at.
const (
	// PerRequest is one model request.
	PerRequest Grain = "request"
	// PerTurn is one agent turn, which may hold many model requests.
	PerTurn Grain = "turn"
	// PerRun is one model's total across a run.
	PerRun Grain = "run"
)

// Enforcement is the grade a run was held at. Note states the limit of the
// grade in words and travels on every row.
type Enforcement struct {
	Grade    string
	Note     string
	Watchdog bool
}

// Enforcement grades.
const (
	GradeAgentFlag       = "agent-flag"
	GradeWrapperWatchdog = "wrapper-watchdog"
	GradeNone            = "none"
)

// Tokens holds the canonical classes. Billed classes are disjoint; the two
// informational classes repeat tokens a billed class already counts.
type Tokens struct {
	InputUncached       int64
	CacheRead           int64
	CacheWrite5m        int64
	CacheWrite1h        int64
	CacheWrite30m       int64
	Output              int64
	ReasoningOutput     int64
	CachedInputReported int64
}

// Count is one class and its tokens.
type Count struct {
	Class  focus.TokenClass
	Tokens int64
}

// Counts lists the non-zero classes in a fixed order.
func (t Tokens) Counts() []Count {
	all := []Count{
		{focus.InputUncached, t.InputUncached},
		{focus.CacheRead, t.CacheRead},
		{focus.CacheWrite5m, t.CacheWrite5m},
		{focus.CacheWrite1h, t.CacheWrite1h},
		{focus.CacheWrite30m, t.CacheWrite30m},
		{focus.Output, t.Output},
		{focus.ReasoningOutput, t.ReasoningOutput},
		{focus.CachedInputReported, t.CachedInputReported},
	}
	present := all[:0]
	for _, count := range all {
		if count.Tokens != 0 {
			present = append(present, count)
		}
	}
	return present
}

// Add returns the class-by-class sum.
func (t Tokens) Add(other Tokens) Tokens {
	return Tokens{
		InputUncached:       t.InputUncached + other.InputUncached,
		CacheRead:           t.CacheRead + other.CacheRead,
		CacheWrite5m:        t.CacheWrite5m + other.CacheWrite5m,
		CacheWrite1h:        t.CacheWrite1h + other.CacheWrite1h,
		CacheWrite30m:       t.CacheWrite30m + other.CacheWrite30m,
		Output:              t.Output + other.Output,
		ReasoningOutput:     t.ReasoningOutput + other.ReasoningOutput,
		CachedInputReported: t.CachedInputReported + other.CachedInputReported,
	}
}

// Request is one unit of usage at the finest grain the source reports.
type Request struct {
	// ID is the provider's request id, or the adapter's stated stand-in when
	// the source has none.
	ID          string
	Provider    string
	Model       string
	Start, End  time.Time
	ServiceTier string
	Region      string
	// ContextInputTokens is the request's whole input. Zero when the grain
	// is coarser than a request, because a sum cannot select a context tier.
	ContextInputTokens int64
	Grain              Grain
	Tokens             Tokens
	// AssumedWrite is the cache write class that took tokens whose lifetime the
	// source did not report, by the adapter's rule. Empty when it reported all.
	AssumedWrite focus.TokenClass
}

// Report is what a run's usage came to.
type Report struct {
	SessionID    string
	AgentVersion string
	CostBasis    focus.CostBasis
	CostSource   string
	Requests     []Request
	Warnings     []string
	// Enforcement is the grade the run earned, when it differs from what the
	// plan could promise. Empty means the plan's grade held.
	Enforcement Enforcement
}

// Plan is how one agent command runs under a budget.
type Plan struct {
	Argv        []string
	Enforcement Enforcement
}

// Session reads one run.
type Session interface {
	// Feed reads one line of the agent's stdout, seen at the given time.
	Feed(line []byte, at time.Time) error
	// Finish reads whatever the agent left behind and returns the run's usage.
	Finish() (Report, error)
}

// Tailer is a Session that watches a file the agent writes during a run.
type Tailer interface {
	// Tail follows the file until ctx ends. It calls settled with the run's
	// requests so far each time a new one settles while the agent is working.
	Tail(ctx context.Context, settled func([]Request))
}

// Agent is one supported agent.
type Agent interface {
	// Name is the costroid/agent tag.
	Name() string
	// Plan rewrites a command to run under the budget, or refuses it.
	Plan(argv []string, budget decimal.Decimal) (Plan, error)
	// NewSession starts reading a run of the planned command.
	NewSession(plan Plan) Session
}
