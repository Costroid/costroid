// Package codex reads `codex exec --json` runs.
//
// The stream reports usage once, at turn.completed, as a running total for the
// thread. It names neither the model nor a request id, so the model comes from
// the command or the Codex config, and a row's id stands in for a request id.
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

	costSource = "agent_stream"

	enforcementNote = "wrapper-watchdog: usage arrives at turn end, so overshoot is bounded by one turn, and one codex exec prompt is one turn"

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

// Plan adds --json. Codex has no budget flag, so the wrapper watches the stream.
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
	return adapter.Plan{
		Argv:        planned,
		Enforcement: adapter.Enforcement{Grade: adapter.GradeWrapperWatchdog, Note: enforcementNote, Watchdog: true},
	}, nil
}

// NewSession starts reading a run of the planned command.
func (Agent) NewSession(plan adapter.Plan) adapter.Session {
	return &Session{
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
	if model := modelFromConfig(configPath()); model != "" {
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

func configPath() string {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		return filepath.Join(dir, "config.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "config.toml")
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

// Session reads one Codex run.
type Session struct {
	model     string
	version   string
	costBasis focus.CostBasis
	threadID  string
	turnStart time.Time
	total     usage
	requests  []adapter.Request
	warnings  []string
}

// Feed reads one stdout line.
func (s *Session) Feed(line []byte, at time.Time) error {
	var parsed streamLine
	if err := json.Unmarshal(line, &parsed); err != nil {
		return fmt.Errorf("codex: stream line is not JSON: %w", err)
	}
	switch parsed.Type {
	case "thread.started":
		s.threadID = parsed.ThreadID
		s.turnStart = at
	case "turn.started":
		s.turnStart = at
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
	s.requests = append(s.requests, adapter.Request{
		ID:          s.threadID + ":turn:" + strconv.Itoa(len(s.requests)+1),
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

// Finished lists the turns the stream has completed.
func (s *Session) Finished() []adapter.Request {
	return s.requests
}

// Finish returns the run's usage. A run with no completed turn reported
// nothing, and whatever it spent is not on the stream.
func (s *Session) Finish() (adapter.Report, error) {
	if s.threadID == "" {
		return adapter.Report{}, fmt.Errorf("codex: the stream held no thread.started event")
	}
	warnings := s.warnings
	if len(s.requests) == 0 {
		warnings = append(warnings, "no turn completed, so the stream reported no usage")
	}
	return adapter.Report{
		SessionID:    s.threadID,
		AgentVersion: s.version,
		CostBasis:    s.costBasis,
		CostSource:   costSource,
		Requests:     s.requests,
		Warnings:     warnings,
	}, nil
}
