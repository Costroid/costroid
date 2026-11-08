// Package adapter holds what every agent adapter hands the wrapper.
package adapter

import (
	"context"
	"errors"
	"fmt"
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
	GradeGatewayPrecheck = "gateway-precheck"
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

// Check refuses a negative class. No source counts fewer than no tokens, and
// a negative count would price as a negative cost.
func (t Tokens) Check() error {
	for _, count := range t.Counts() {
		if count.Tokens < 0 {
			return fmt.Errorf("%s holds %d tokens, below zero", count.Class, count.Tokens)
		}
	}
	return nil
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
	// ContextInputTokens is the request's whole input. At a coarser grain it
	// is the whole input of every request the entry sums, which no request in
	// it can pass, or zero when the adapter states none.
	ContextInputTokens int64
	Grain              Grain
	Tokens             Tokens
	// AssumedWrite is the cache write class that took tokens whose lifetime the
	// source did not report, by the adapter's rule. Empty when it reported all.
	AssumedWrite focus.TokenClass
	// CostSource names where this entry came from when it differs from the
	// report's. Empty means the report's.
	CostSource string
	// Note says in words how this entry was read, when the report's own
	// source does not.
	Note string
	// TierEvidence is what an entry coarser than a request offers about its
	// context tier beyond its summed input. Nil when it offers nothing.
	TierEvidence *TierEvidence
}

// TierEvidence is an agent's own figure for one entry's billed tokens. The
// entry's input is a sum over its requests, and past the lowest context tier
// a sum says nothing about any one request. The agent's figure says which
// tier the requests were billed in: the rows take the tier whose prices give
// it.
type TierEvidence struct {
	// Agent names the agent in words, as a row's note names it.
	Agent string
	// USD is the agent's figure at list price, nil when it states none.
	USD *decimal.Decimal
}

// AgentError is the class of error a run's agent ended in. The class is read
// from the agent's own fields, never from its text. The values are a closed
// list; a class the agent states that is not on it reads as unknown.
type AgentError string

// The classes an agent's run can end in. Empty means it ended in none.
const (
	AgentErrorAuthentication AgentError = "authentication_failed"
	AgentErrorBilling        AgentError = "billing_error"
	AgentErrorRateLimit      AgentError = "rate_limit"
	AgentErrorInvalidRequest AgentError = "invalid_request"
	AgentErrorServer         AgentError = "server_error"
	AgentErrorUnknown        AgentError = "unknown"
)

// ParseAgentError maps the class an agent states onto the closed list.
func ParseAgentError(stated string) AgentError {
	switch class := AgentError(stated); class {
	case AgentErrorAuthentication, AgentErrorBilling, AgentErrorRateLimit, AgentErrorInvalidRequest, AgentErrorServer:
		return class
	default:
		return AgentErrorUnknown
	}
}

// Report is what a run's usage came to.
type Report struct {
	SessionID    string
	AgentVersion string
	CostBasis    focus.CostBasis
	CostSource   string
	Requests     []Request
	Warnings     []string
	// AgentError is the class of error the agent ended the run in. Empty means
	// the run did not end in one.
	AgentError AgentError
	// Enforcement is the grade the run earned, when it differs from what the
	// plan could promise. Empty means the plan's grade held.
	Enforcement Enforcement
	// Nested holds the sessions of other agent processes that started and
	// ended inside the run, each under its own session id. Their requests are
	// never in Requests.
	Nested []Report
	// Unrecorded counts the model requests the run sent whose usage no source
	// reported, so no row holds them. A warning says which.
	Unrecorded int
	// AgentStated is the agent's own figure for the run, in the agent's own
	// unit, nil when it states none. No row holds it, and it is never
	// converted or added to an amount.
	AgentStated *Stated
}

// UnitAICredits is the unit of gh-aw's AI credits.
const UnitAICredits = "AIC"

// Stated is a figure an agent states about its own run, as it states it.
type Stated struct {
	// Unit is the agent's own unit, such as UnitAICredits.
	Unit string
	// Quantity is a non-negative decimal string, never a JSON number.
	Quantity string
	// Source names the agent that stated it.
	Source string
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

// ErrClaimed means another run holds the directory an agent keeps its state in.
var ErrClaimed = errors.New("another costroid run holds the agent's state directory")

// Exclusive is an Agent whose runs cannot share one state directory, because
// a run reads the directory and would count another run's spend as its own.
type Exclusive interface {
	// Claim holds the directory for one run, and release ends the hold. It
	// fails with ErrClaimed while another run holds it.
	Claim() (release func(), err error)
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
