// Package opencode reads `opencode run --format json` runs.
//
// OpenCode prints a step_finish line as each model request ends, with that
// request's tokens and OpenCode's own price for it. Each step is one set of
// rows, and the wrapper prices the run after every step, so a run is stopped
// at the cap between steps. OpenCode's price stays the agent's own figure; the
// rows are priced from the catalog.
package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
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
	// Name is the costroid/agent tag.
	Name = "opencode"

	costSource = "agent_stream"

	// watchedNote is the grade's note. OpenCode reports a step's tokens only
	// when the step ends, so the step that crosses the cap is paid in full.
	// The figures are the recorded stop under testdata/1.19.4/stop.
	watchedNote = "wrapper-watchdog: priced from each step OpenCode reports, with a stop at the cap between steps, so one step can pass it"

	// StatedUnit is the unit of OpenCode's own price for a step, and of its
	// figure for the run, AgentStated.
	StatedUnit = "USD"

	// taskTool is the tool that starts an OpenCode subagent in a session of
	// its own.
	taskTool = "task"
)

// provider is what the catalog and the run's key say about one OpenCode
// provider.
type provider struct {
	// keyVariable holds the provider's API key when the run bills one.
	keyVariable string
	// write is the class a cache write takes. OpenCode reports one write total
	// with no lifetime.
	write focus.TokenClass
}

// providers are the OpenCode providers the catalog prices. Anthropic's cache
// is written for five minutes unless a request asks for longer, and OpenCode
// asks for no lifetime; OpenAI's default is 30 minutes.
var providers = map[string]provider{
	"anthropic": {keyVariable: "ANTHROPIC_API_KEY", write: focus.CacheWrite5m},
	"openai":    {keyVariable: "OPENAI_API_KEY", write: focus.CacheWrite30m},
}

// providerNames lists the providers in a sentence.
func providerNames() string {
	return strings.Join(slices.Sorted(maps.Keys(providers)), " and ")
}

// Agent is OpenCode.
type Agent struct{}

// Name returns the costroid/agent tag.
func (Agent) Name() string {
	return Name
}

// Plan adds --format json, and --title= when the command names no title. It
// refuses a command whose model the catalog cannot price, since a run that
// cannot be priced cannot be held to the budget. The watchdog holds the run
// and stops it at the cap between steps.
func (Agent) Plan(argv []string, _ decimal.Decimal) (adapter.Plan, error) {
	subcommand := slices.Index(argv, "run")
	if subcommand < 1 {
		return adapter.Plan{}, errors.New("opencode: only `opencode run` prints its steps, so the command needs run")
	}
	args, err := readArgs(argv[subcommand+1:])
	if err != nil {
		return adapter.Plan{}, err
	}
	if _, err := resolveModel(args.model, localEnvironment()); err != nil {
		return adapter.Plan{}, err
	}
	var added []string
	if args.format == "" {
		added = append(added, "--format", "json")
	}
	if !args.titled {
		added = append(added, promptTitle)
	}
	return adapter.Plan{
		Argv:        slices.Insert(slices.Clone(argv), subcommand+1, added...),
		Enforcement: adapter.Enforcement{Grade: adapter.GradeWrapperWatchdog, Note: watchedNote, Watchdog: true},
	}, nil
}

// promptTitle titles a new session from its prompt. OpenCode then sends no
// request for a title, which its stream would not show.
const promptTitle = "--title="

// valueFlags are the run flags that take the next argument as their value.
var valueFlags = []string{
	"--command", "--session", "-s", "--model", "-m", "--agent", "--format",
	"--file", "-f", "--title", "--attach", "--password", "-p", "--username",
	"-u", "--dir", "--port", "--variant", "--replay-limit",
}

// runArgs is what a run's arguments say that the plan and the reading need.
type runArgs struct {
	model  string
	format string
	titled bool
	// promptTitled says the session is titled from its prompt.
	promptTitled bool
}

// readArgs reads the arguments after run, up to "--". A value that starts
// with "-" is read as the next flag, as OpenCode's parser reads it.
func readArgs(args []string) (runArgs, error) {
	var found runArgs
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		flag, value, inline := strings.Cut(arg, "=")
		if !inline && slices.Contains(valueFlags, flag) && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			i++
			value = args[i]
		}
		switch flag {
		case "--model", "-m":
			found.model = value
		case "--title":
			found.titled = true
			found.promptTitled = value == ""
		case "--format":
			if value != "json" {
				return runArgs{}, fmt.Errorf("opencode: --format %s hides the steps costroid reads; remove it", value)
			}
			found.format = value
		}
	}
	return found, nil
}

// NewSession starts reading a run of the planned command. It reads the
// version from the command's own opencode, and the basis from the key the
// provider runs on.
func (Agent) NewSession(plan adapter.Plan) adapter.Session {
	session := &Session{planned: plan.Enforcement, notify: make(chan struct{}, 1)}
	subcommand := slices.Index(plan.Argv, "run")
	if subcommand < 0 {
		session.problem = errors.New("opencode: the command has no run")
		return session
	}
	args, err := readArgs(plan.Argv[subcommand+1:])
	if err != nil {
		session.problem = err
		return session
	}
	session.promptTitled = args.promptTitled
	env := localEnvironment()
	model, err := resolveModel(args.model, env)
	if err != nil {
		session.problem = err
		return session
	}
	session.model = model
	session.costBasis = costBasis(model.provider, env)
	session.version = versionOf(plan.Argv[0])
	return session
}

// costBasis is measured when the provider's key variable is set and
// OpenCode's auth store holds no OAuth login for the provider, which OpenCode
// would use in the key's place. Otherwise it is list-equivalent.
func costBasis(provider string, env environment) focus.CostBasis {
	if env.getenv(providers[provider].keyVariable) == "" || oauthLogin(provider, env) {
		return focus.ListEquivalent
	}
	return focus.Measured
}

const versionTimeout = 10 * time.Second

var versionPattern = regexp.MustCompile(`[0-9]+\.[0-9]+\.[0-9]+[A-Za-z0-9.+-]*`)

// versionOf runs the command's own opencode with --version. A command that
// starts OpenCode through a launcher, such as npx, has no version read,
// because the launcher's own would answer.
func versionOf(command string) string {
	if filepath.Base(command) != Name {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), versionTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, command, "--version").Output()
	if err != nil {
		return ""
	}
	return versionPattern.FindString(string(out))
}

// step is the part of a step_finish line this reader takes.
type step struct {
	ID     string `json:"id"`
	Tokens *struct {
		Total     *int64 `json:"total"`
		Input     int64  `json:"input"`
		Output    int64  `json:"output"`
		Reasoning int64  `json:"reasoning"`
		Cache     struct {
			Read  int64 `json:"read"`
			Write int64 `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
	Cost json.RawMessage `json:"cost"`
}

// line is one line of the stream, with the fields every type may carry.
type line struct {
	Type      json.RawMessage `json:"type"`
	Timestamp json.RawMessage `json:"timestamp"`
	SessionID json.RawMessage `json:"sessionID"`
	Part      json.RawMessage `json:"part"`
	Error     json.RawMessage `json:"error"`
}

// Session reads one OpenCode run. Feed and Tail run on separate goroutines,
// so the lock covers everything below it.
type Session struct {
	planned      adapter.Enforcement
	model        model
	costBasis    focus.CostBasis
	version      string
	promptTitled bool
	// problem is why the run cannot be read, found before it started.
	problem error
	// notify carries a signal to Tail each time a step settles.
	notify chan struct{}

	mu        sync.Mutex
	sessionID string
	stepStart time.Time
	stepOpen  bool
	steps     []adapter.Request
	// unreadStep is why a step_finish line could not be taken. The line holds
	// the run's figures, so the run's reading stops at Finish.
	unreadStep error
	stated     decimal.Decimal
	// unstated counts the steps whose cost OpenCode did not state as a number.
	unstated int
	// mismatched counts the steps whose total is not the sum of their parts.
	mismatched int
	tasks      int
	// messages are the messages whose steps started in this run. A resumed
	// session can print a tool part of an earlier message again.
	messages   []string
	skipped    int
	otherIDs   int
	agentError adapter.AgentError
}

// Feed reads one stdout line. A line whose shape this reader does not know is
// skipped and counted. A step_finish line that cannot be read stops the run's
// reading at Finish, because it holds the run's figures.
func (s *Session) Feed(raw []byte, at time.Time) error {
	if !json.Valid(raw) {
		return errors.New("opencode: stream line is not JSON")
	}
	var parsed line
	kind, ok := "", json.Unmarshal(raw, &parsed) == nil
	if ok {
		kind, ok = jsonString(parsed.Type)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, named := jsonString(parsed.SessionID)
	switch {
	case !ok || !named || id == "":
		s.skipped++
		return nil
	case s.sessionID == "":
		s.sessionID = id
	case id != s.sessionID:
		s.otherIDs++
		return nil
	}
	when := timestamp(parsed.Timestamp, at)
	switch kind {
	case "step_start":
		s.stepStart, s.stepOpen = when, true
		if message := messageOf(parsed.Part); message != "" {
			s.messages = append(s.messages, message)
		}
	case "step_finish":
		s.readStep(parsed.Part, when)
	case "tool_use":
		var tool struct {
			Tool      string `json:"tool"`
			MessageID string `json:"messageID"`
		}
		if json.Unmarshal(parsed.Part, &tool) == nil && tool.Tool == taskTool && slices.Contains(s.messages, tool.MessageID) {
			s.tasks++
		}
	case "error":
		s.agentError = agentError(parsed.Error)
	}
	return nil
}

// messageOf is the message a part belongs to, or "" when it names none.
func messageOf(part json.RawMessage) string {
	var named struct {
		MessageID string `json:"messageID"`
	}
	if json.Unmarshal(part, &named) != nil {
		return ""
	}
	return named.MessageID
}

// jsonString reads a JSON string. Absent, null and any other value read as
// not a string.
func jsonString(raw json.RawMessage) (string, bool) {
	var value string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

// timestamp reads OpenCode's milliseconds since the epoch, or the time the
// line was read when it states none.
func timestamp(raw json.RawMessage, at time.Time) time.Time {
	millis, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || millis <= 0 {
		return at
	}
	return time.UnixMilli(millis).UTC()
}

// readStep takes a step_finish line. The first that cannot be read is kept,
// and the reading stops at Finish.
func (s *Session) readStep(raw json.RawMessage, end time.Time) {
	if s.unreadStep != nil {
		return
	}
	var finished step
	if err := json.Unmarshal(raw, &finished); err != nil || finished.Tokens == nil {
		s.unreadStep = fmt.Errorf("opencode: step_finish %d holds no tokens this reader can read", len(s.steps)+1)
		return
	}
	request, err := s.request(finished, end)
	if err != nil {
		s.unreadStep = fmt.Errorf("opencode: step_finish %d: %w", len(s.steps)+1, err)
		return
	}
	if cost, err := decimal.ParseNumber(string(finished.Cost)); err == nil && cost.Sign() >= 0 {
		s.stated = s.stated.Add(cost)
	} else {
		s.unstated++
	}
	s.steps = append(s.steps, request)
	s.stepOpen = false
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// request maps one step. OpenCode's input holds neither cache reads nor
// writes, and its output holds no reasoning, so output is the two added. The
// step's whole input is all three inputs. A count below zero, or a sum past
// what an int64 holds, is refused.
func (s *Session) request(finished step, end time.Time) (adapter.Request, error) {
	counts := finished.Tokens
	parts := []int64{counts.Input, counts.Cache.Read, counts.Cache.Write, counts.Output, counts.Reasoning}
	var sum int64
	for _, part := range parts {
		if part < 0 || part > math.MaxInt64-sum {
			return adapter.Request{}, errors.New("a token count is below zero or past what this reader holds")
		}
		sum += part
	}
	if counts.Total != nil && *counts.Total != sum {
		s.mismatched++
	}
	tokens := adapter.Tokens{
		InputUncached:   counts.Input,
		CacheRead:       counts.Cache.Read,
		Output:          counts.Output + counts.Reasoning,
		ReasoningOutput: counts.Reasoning,
	}
	var assumed focus.TokenClass
	if counts.Cache.Write > 0 {
		assumed = providers[s.model.provider].write
		if assumed == focus.CacheWrite30m {
			tokens.CacheWrite30m = counts.Cache.Write
		} else {
			tokens.CacheWrite5m = counts.Cache.Write
		}
	}
	id := finished.ID
	if id == "" {
		id = "step:" + strconv.Itoa(len(s.steps)+1)
	}
	start := end
	if s.stepOpen {
		start = s.stepStart
	}
	return adapter.Request{
		ID:                 s.sessionID + ":" + id,
		Provider:           s.model.provider,
		Model:              s.model.id,
		Start:              start,
		End:                end,
		ServiceTier:        catalog.TierStandard,
		Region:             catalog.RegionGlobal,
		ContextInputTokens: counts.Input + counts.Cache.Read + counts.Cache.Write,
		Grain:              adapter.PerRequest,
		Tokens:             tokens,
		AssumedWrite:       assumed,
	}, nil
}

// agentError reads the class of an error line from its name and status code,
// never from its text. An aborted step is the stop's doing, not an error.
func agentError(raw json.RawMessage) adapter.AgentError {
	var stated struct {
		Name string `json:"name"`
		Data struct {
			StatusCode int `json:"statusCode"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &stated) != nil {
		return adapter.AgentErrorUnknown
	}
	switch code := stated.Data.StatusCode; {
	case stated.Name == "MessageAbortedError":
		return ""
	case stated.Name == "ProviderAuthError", code == 401, code == 403:
		return adapter.AgentErrorAuthentication
	case code == 402:
		return adapter.AgentErrorBilling
	case code == 429:
		return adapter.AgentErrorRateLimit
	case stated.Name == "ContextOverflowError", code >= 400 && code < 500:
		return adapter.AgentErrorInvalidRequest
	case code >= 500:
		return adapter.AgentErrorServer
	}
	return adapter.AgentErrorUnknown
}

// Tail calls settled with the run's steps so far each time one settles, until
// ctx ends.
func (s *Session) Tail(ctx context.Context, settled func([]adapter.Request)) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.notify:
			s.mu.Lock()
			steps := slices.Clone(s.steps)
			s.mu.Unlock()
			settled(steps)
		}
	}
}

// Finish returns the run's usage and the grade it earned.
func (s *Session) Finish() (adapter.Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.problem != nil:
		return adapter.Report{}, s.problem
	case s.unreadStep != nil:
		return adapter.Report{}, s.unreadStep
	case s.sessionID == "":
		return adapter.Report{}, errors.New("opencode: the stream held no line that names a session. OpenCode did not start a session, or did not print its steps as JSON")
	}
	report := adapter.Report{
		SessionID:    s.sessionID,
		AgentVersion: s.version,
		CostBasis:    s.costBasis,
		CostSource:   costSource,
		Requests:     slices.Clone(s.steps),
		Enforcement:  s.planned,
		AgentError:   s.agentError,
	}
	if s.unstated == 0 && len(s.steps) > 0 {
		report.AgentStated = &adapter.Stated{Unit: StatedUnit, Quantity: s.stated.String(), Source: Name}
	}
	s.reportUnrecorded(&report)
	report.Warnings = append(report.Warnings, s.notes()...)
	if len(report.Requests) == 0 {
		report.Warnings = append(report.Warnings, "no step finished, so the stream reported no usage")
	}
	return report, nil
}

// reportUnrecorded counts the model requests the run made that the stream
// does not report, and says which.
func (s *Session) reportUnrecorded(report *adapter.Report) {
	if s.stepOpen {
		report.Unrecorded++
		report.Warnings = append(report.Warnings, "the run ended inside a step: OpenCode reports a step's tokens when it ends, so the step in flight is not counted and its tokens reach no row")
	}
	if s.tasks > 0 {
		report.Unrecorded += s.tasks
		report.Enforcement = adapter.Enforcement{Grade: adapter.GradeNone, Note: "none: the budget was not enforced: OpenCode's subagents spent outside the stream the watchdog reads"}
		report.Warnings = append(report.Warnings, fmt.Sprintf("OpenCode started %s with its task tool. Each runs in a session the stream does not print, so no row holds at least %s, and the watchdog did not see them", counted(s.tasks, "subagent"), counted(s.tasks, "model request")))
	}
}

// notes are the warnings about how the run was read.
func (s *Session) notes() []string {
	var notes []string
	if s.promptTitled {
		notes = append(notes, "--title= titles the session from its prompt, so OpenCode sends no request for a title, which its stream would not show; costroid adds it when the command names no title")
	}
	if s.model.fromConfig {
		notes = append(notes, fmt.Sprintf("the stream names no model, so every step reads %s/%s, the model in OpenCode's config; an agent's own model in that config would replace it, and --model names it for certain", s.model.provider, s.model.id))
	}
	if s.unstated > 0 {
		notes = append(notes, fmt.Sprintf("OpenCode stated no cost as a number for %s, so its own figure for the run is not stated", counted(s.unstated, "step")))
	}
	if s.mismatched > 0 {
		notes = append(notes, fmt.Sprintf("the total of %s is not the sum of its input, cache, output and reasoning tokens; the rows hold the parts", counted(s.mismatched, "step")))
	}
	if s.otherIDs > 0 {
		notes = append(notes, fmt.Sprintf("%s named another session than the run's first, and %s not read", counted(s.otherIDs, "stream line"), plural(s.otherIDs, "was", "were")))
	}
	if s.skipped > 0 {
		notes = append(notes, fmt.Sprintf("%s %s skipped: %s not one this reader knows", counted(s.skipped, "stream line"), plural(s.skipped, "was", "were"), plural(s.skipped, "its shape is", "their shape is")))
	}
	return notes
}

func counted(count int, noun string) string {
	return strconv.Itoa(count) + " " + plural(count, noun, noun+"s")
}

func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}

// localEnvironment is the run's own environment and working directory.
func localEnvironment() environment {
	dir, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	return environment{getenv: os.Getenv, dir: dir, home: home}
}
