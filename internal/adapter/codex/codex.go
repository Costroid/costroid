// Package codex reads `codex exec --json` runs.
//
// The stream reports usage once, at turn.completed, as a running total for the
// thread, and one prompt is one turn. So the budget is watched in the rollout
// file Codex writes during the turn, which also gives per-request rows. The
// stream stays as the check on the file and as the record when it cannot be
// read.
package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

const (
	name     = "codex"
	provider = "openai"

	// UnknownModel stands in when neither the command nor the config names
	// a model. It prices at the catalog's fallback tier.
	UnknownModel = "unknown"

	costSourceStream  = "agent_stream"
	costSourceRollout = "agent_session_file"

	watchedNote = "wrapper-watchdog: priced from the rollout file during the turn and stopped at the cap, so requests in flight at the stop can pass it"

	ephemeralFlag = "--ephemeral"

	versionTimeout = 10 * time.Second
)

var (
	versionPattern = regexp.MustCompile(`[0-9]+\.[0-9]+\.[0-9]+[A-Za-z0-9.+-]*`)
	configModel    = regexp.MustCompile(`^model\s*=\s*"([^"]+)"`)
)

// Agent is the Codex CLI.
type Agent struct{}

// Name returns the costroid/agent tag.
func (Agent) Name() string {
	return name
}

// Plan adds --json. Codex has no budget flag, so the wrapper watches the
// rollout file, and a run that writes none has nothing holding it to the budget.
func (Agent) Plan(argv []string, _ decimal.Decimal) (adapter.Plan, error) {
	subcommand := slices.Index(argv, "exec")
	if subcommand < 1 {
		return adapter.Plan{}, fmt.Errorf("codex: only `codex exec` reports usage on a stream, so the command needs exec")
	}
	if subcommand+1 < len(argv) && argv[subcommand+1] == "resume" {
		return adapter.Plan{}, fmt.Errorf("codex: exec resume reports the whole thread's earlier spend, and the stream gives no way to subtract it; start a new thread")
	}
	planned := slices.Clone(argv)
	if !slices.Contains(argv, "--json") {
		planned = slices.Insert(planned, subcommand+1, "--json")
	}
	enforcement := adapter.Enforcement{Grade: adapter.GradeWrapperWatchdog, Note: watchedNote, Watchdog: true}
	if slices.Contains(argv, ephemeralFlag) {
		enforcement = unwatched(ephemeralFlag + " writes no rollout file")
	}
	return adapter.Plan{Argv: planned, Enforcement: enforcement}, nil
}

// unwatched is the grade of a run nothing could watch, and why.
func unwatched(why string) adapter.Enforcement {
	return adapter.Enforcement{Grade: adapter.GradeNone, Note: "none: the budget was not enforced: " + why}
}

// NewSession starts reading a run of the planned command.
func (Agent) NewSession(plan adapter.Plan) adapter.Session {
	return &Session{
		planned:   plan.Enforcement,
		home:      homeDir(),
		model:     modelOf(plan.Argv),
		costBasis: costBasisOf(os.Getenv("CODEX_API_KEY")),
		version:   versionOf(plan.Argv),
	}
}

func costBasisOf(apiKey string) focus.CostBasis {
	if apiKey != "" {
		return focus.Measured
	}
	return focus.ListEquivalent
}

func versionOf(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), versionTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, argv[0], "--version").Output()
	if err != nil {
		return ""
	}
	return versionPattern.FindString(string(out))
}

func modelOf(argv []string) string {
	if model := modelFromArgs(argv); model != "" {
		return model
	}
	if model := modelFromConfig(filepath.Join(homeDir(), "config.toml")); model != "" {
		return model
	}
	return UnknownModel
}

func modelFromArgs(argv []string) string {
	for i, arg := range argv {
		if value, ok := strings.CutPrefix(arg, "--model="); ok {
			return value
		}
		if i+1 == len(argv) {
			break
		}
		switch arg {
		case "-m", "--model":
			return argv[i+1]
		case "-c", "--config":
			if value, ok := strings.CutPrefix(argv[i+1], "model="); ok {
				return strings.Trim(value, `"'`)
			}
		}
	}
	return ""
}

// homeDir is where Codex keeps its config and rollout files.
func homeDir() string {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex")
}

// modelFromConfig reads the top-level model key, which sits above any table.
func modelFromConfig(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") {
			return ""
		}
		if match := configModel.FindStringSubmatch(line); match != nil {
			return match[1]
		}
	}
	return ""
}

type usage struct {
	InputTokens     int64 `json:"input_tokens"`
	CachedInput     int64 `json:"cached_input_tokens"`
	CacheWriteInput int64 `json:"cache_write_input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	ReasoningOutput int64 `json:"reasoning_output_tokens"`
}

func (u usage) minus(earlier usage) usage {
	return usage{
		InputTokens:     u.InputTokens - earlier.InputTokens,
		CachedInput:     u.CachedInput - earlier.CachedInput,
		CacheWriteInput: u.CacheWriteInput - earlier.CacheWriteInput,
		OutputTokens:    u.OutputTokens - earlier.OutputTokens,
		ReasoningOutput: u.ReasoningOutput - earlier.ReasoningOutput,
	}
}

// tokens maps one turn's usage. Codex input holds cached tokens and cache
// writes, so uncached input is what remains. Writes have no reported lifetime;
// OpenAI's default is 30 minutes.
func (u usage) tokens() (adapter.Tokens, error) {
	uncached := u.InputTokens - u.CachedInput - u.CacheWriteInput
	if uncached < 0 {
		return adapter.Tokens{}, fmt.Errorf("codex: input %d is below cached %d plus cache writes %d", u.InputTokens, u.CachedInput, u.CacheWriteInput)
	}
	return adapter.Tokens{
		InputUncached:       uncached,
		CacheRead:           u.CachedInput,
		CacheWrite30m:       u.CacheWriteInput,
		Output:              u.OutputTokens,
		ReasoningOutput:     u.ReasoningOutput,
		CachedInputReported: u.CachedInput,
	}, nil
}

type streamLine struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Usage    usage  `json:"usage"`
	Message  string `json:"message"`
}

// Session reads one Codex run. Feed and Tail run on separate goroutines, so
// the lock covers everything below it.
type Session struct {
	planned   adapter.Enforcement
	home      string
	costBasis focus.CostBasis

	mu        sync.Mutex
	model     string
	version   string
	threadID  string
	turnStart time.Time
	turnOpen  bool
	total     usage
	turns     []adapter.Request
	warnings  []string
	log       rolloutLog
}

// Feed reads one stdout line.
func (s *Session) Feed(line []byte, at time.Time) error {
	var parsed streamLine
	if err := json.Unmarshal(line, &parsed); err != nil {
		return fmt.Errorf("codex: stream line is not JSON: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch parsed.Type {
	case "thread.started":
		s.threadID = parsed.ThreadID
		s.turnStart = at
	case "turn.started":
		s.turnStart = at
		s.turnOpen = true
	case "turn.completed":
		return s.completeTurn(parsed.Usage, at)
	case "turn.failed", "error":
		s.warnings = append(s.warnings, "codex reported "+parsed.Type+": a failed turn's usage is not on the stream")
	}
	return nil
}

func (s *Session) completeTurn(total usage, at time.Time) error {
	tokens, err := total.minus(s.total).tokens()
	if err != nil {
		return err
	}
	s.total = total
	s.turnOpen = false
	s.turns = append(s.turns, adapter.Request{
		ID:          s.threadID + ":turn:" + strconv.Itoa(len(s.turns)+1),
		Provider:    provider,
		Model:       s.model,
		Start:       s.turnStart,
		End:         at,
		ServiceTier: catalog.TierStandard,
		Region:      catalog.RegionGlobal,
		Grain:       adapter.PerTurn,
		Tokens:      tokens,
	})
	return nil
}

// Finish returns the run's usage and the grade it earned. Per-request rows
// from the rollout file stand unless the stream's total contradicts them. A
// run with neither source reported nothing, and whatever it spent is unknown.
func (s *Session) Finish() (adapter.Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.threadID == "" {
		return adapter.Report{}, fmt.Errorf("codex: the stream held no thread.started event")
	}
	s.judgeRollout()
	report := adapter.Report{
		SessionID:    s.threadID,
		AgentVersion: s.version,
		CostBasis:    s.costBasis,
		CostSource:   costSourceStream,
		Requests:     s.turns,
		Warnings:     s.warnings,
		Enforcement:  s.earned(),
	}
	if len(s.log.requests) > 0 && (s.log.problem == "" || len(s.turns) == 0) {
		report.CostSource = costSourceRollout
		report.Requests = s.log.requests
		if s.turnOpen || len(s.turns) == 0 {
			report.Warnings = append(report.Warnings, "the run ended inside a turn: the rows stop at the last request the rollout file settled, and a request in flight is not counted")
		}
	}
	if len(report.Requests) == 0 {
		report.Warnings = append(report.Warnings, "no turn completed, so neither the stream nor a rollout file reported usage")
	}
	return report, nil
}

// judgeRollout decides whether the file held the run. The totals are compared
// only when the stream closed its last turn, because a stopped turn has no
// stream total to compare.
func (s *Session) judgeRollout() {
	switch {
	case !s.planned.Watchdog:
	case !s.log.opened:
		s.log.refuse("no rollout file appeared for this thread")
	case !s.turnOpen && len(s.turns) > 0 && s.log.settled != s.total:
		s.log.refuse("the rollout file's total differs from the stream's")
	}
}

func (s *Session) earned() adapter.Enforcement {
	if s.planned.Watchdog && s.log.problem != "" {
		return unwatched(s.log.problem)
	}
	return s.planned
}
