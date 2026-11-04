// Package claudecode reads Claude Code runs.
//
// The stream supplies the session id, the billing source and the result's
// per-model totals. Its assistant messages carry a placeholder output count,
// so per-request rows come from the session files Claude Code writes, checked
// against those totals. Without the files a run falls to one set of rows per
// model.
package claudecode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

const (
	// Name is the costroid/agent tag.
	Name     = "claude-code"
	provider = "anthropic"

	// SourceStream marks usage read from the result object alone.
	SourceStream = "agent_stream"
	// SourceSessionFile marks usage read from Claude Code's session files.
	SourceSessionFile = "agent_session_file"

	enforcementNote = "agent-flag: client-side estimate, can pass the cap"
)

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,256}$`)

// keyBilledSources are the apiKeySource values that mean the run bills an API
// key. Anything else, including none, reads as a subscription: that claim is
// the weaker one, because a list-equivalent figure never enters EffectiveCost.
var keyBilledSources = []string{"ANTHROPIC_API_KEY", "apiKeyHelper", "user", "project", "org", "temporary"}

// Agent is Claude Code.
type Agent struct {
	// ConfigDir holds Claude Code's sessions. Empty means CLAUDE_CONFIG_DIR,
	// then ~/.claude.
	ConfigDir string
}

// Name returns the costroid/agent tag.
func (Agent) Name() string {
	return Name
}

// Plan adds the stream flags and the budget flag.
func (Agent) Plan(argv []string, budget decimal.Decimal) (adapter.Plan, error) {
	if len(argv) == 0 {
		return adapter.Plan{}, fmt.Errorf("claude-code: no command")
	}
	args := argv[1:]
	if !slices.Contains(args, "-p") && !slices.Contains(args, "--print") {
		return adapter.Plan{}, fmt.Errorf("claude-code: --max-budget-usd works only with -p, so the command needs -p")
	}
	var added []string
	for _, arg := range args {
		if arg == "--max-budget-usd" || strings.HasPrefix(arg, "--max-budget-usd=") {
			return adapter.Plan{}, fmt.Errorf("claude-code: the command already sets --max-budget-usd; pass the budget to costroid alone")
		}
	}
	switch format := outputFormat(args); format {
	case "":
		added = append(added, "--output-format", "stream-json")
	case "stream-json":
	default:
		return adapter.Plan{}, fmt.Errorf("claude-code: --output-format %s hides the stream costroid reads; remove it", format)
	}
	if !slices.Contains(args, "--verbose") {
		added = append(added, "--verbose")
	}
	added = append(added, "--max-budget-usd", budget.String())
	planned := append([]string{argv[0]}, added...)
	return adapter.Plan{
		Argv:        append(planned, args...),
		Enforcement: adapter.Enforcement{Grade: adapter.GradeAgentFlag, Note: enforcementNote},
	}, nil
}

func outputFormat(args []string) string {
	for i, arg := range args {
		if arg == "--output-format" && i+1 < len(args) {
			return args[i+1]
		}
		if value, ok := strings.CutPrefix(arg, "--output-format="); ok {
			return value
		}
	}
	return ""
}

// NewSession starts reading a run.
func (a Agent) NewSession(adapter.Plan) adapter.Session {
	return &Session{configDir: a.configDir()}
}

func (a Agent) configDir() string {
	if a.ConfigDir != "" {
		return a.ConfigDir
	}
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

type cacheCreation struct {
	FiveMinute int64 `json:"ephemeral_5m_input_tokens"`
	OneHour    int64 `json:"ephemeral_1h_input_tokens"`
}

type usage struct {
	InputTokens   int64          `json:"input_tokens"`
	OutputTokens  int64          `json:"output_tokens"`
	CacheRead     int64          `json:"cache_read_input_tokens"`
	CacheCreated  int64          `json:"cache_creation_input_tokens"`
	CacheCreation *cacheCreation `json:"cache_creation"`
	Details       struct {
		ThinkingTokens int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
	ServiceTier  string `json:"service_tier"`
	InferenceGeo string `json:"inference_geo"`
	Speed        string `json:"speed"`
}

// modelUsage is the result object's total for one model, subagents included.
type modelUsage struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
	CacheRead    int64 `json:"cacheReadInputTokens"`
	CacheCreated int64 `json:"cacheCreationInputTokens"`
	FiveMinute   int64 `json:"cacheCreationEphemeral5mInputTokens"`
	OneHour      int64 `json:"cacheCreationEphemeral1hInputTokens"`
	Thinking     int64 `json:"thinkingTokens"`
}

type streamLine struct {
	Type         string                `json:"type"`
	Subtype      string                `json:"subtype"`
	SessionID    string                `json:"session_id"`
	APIKeySource string                `json:"apiKeySource"`
	Version      string                `json:"claude_code_version"`
	CWD          string                `json:"cwd"`
	ModelUsage   map[string]modelUsage `json:"modelUsage"`
}

// Session reads one Claude Code run.
type Session struct {
	configDir    string
	sessionID    string
	version      string
	apiKeySource string
	cwd          string
	totals       map[string]modelUsage
	sawResult    bool
	first, last  time.Time
	// ranFrom and ranTo are when the agent's process ran, when known.
	ranFrom, ranTo time.Time
}

// Feed reads one stdout line. A line that is not JSON is not the agent's
// stream and carries no usage.
func (s *Session) Feed(line []byte, at time.Time) error {
	if s.first.IsZero() {
		s.first = at
	}
	s.last = at
	var parsed streamLine
	if err := json.Unmarshal(line, &parsed); err != nil {
		return fmt.Errorf("claude-code: stream line is not JSON: %w", err)
	}
	switch parsed.Type {
	case "system":
		if parsed.Subtype == "init" {
			s.sessionID, s.version, s.apiKeySource = parsed.SessionID, parsed.Version, parsed.APIKeySource
			if s.cwd == "" {
				s.cwd = parsed.CWD
			}
		}
	case "result":
		// A run can hold several results. modelUsage is cumulative, so the
		// last one wins and adding them would double count.
		s.sawResult = true
		s.totals = parsed.ModelUsage
		if parsed.SessionID != "" {
			s.sessionID = parsed.SessionID
		}
	}
	return nil
}

// Finish returns per-request usage when the session files agree with the
// result, and per-model usage otherwise, then the nested sessions the run's
// project directory holds.
func (s *Session) Finish() (adapter.Report, error) {
	report, run, err := s.finishRun()
	if err != nil {
		return report, err
	}
	nested, warnings := s.nestedSessions(run)
	report.Nested = nested
	report.Warnings = append(report.Warnings, warnings...)
	return report, nil
}

func (s *Session) finishRun() (adapter.Report, transcript, error) {
	report := adapter.Report{SessionID: s.sessionID, AgentVersion: s.version, CostBasis: s.costBasis()}
	run, err := s.readSessionFiles()
	requests := run.requests
	if err != nil {
		report.Warnings = append(report.Warnings, err.Error())
	}
	switch {
	case len(requests) > 0 && !s.sawResult:
		report.Warnings = append(report.Warnings, "no result object: the session files are the only record of this run")
	case len(requests) > 0 && !agrees(requests, s.totals):
		report.Warnings = append(report.Warnings, "the session files disagree with the result object; the result's per-model totals were used")
		requests = nil
	}
	if len(requests) > 0 {
		report.CostSource, report.Requests = SourceSessionFile, requests
		return report, run, nil
	}
	if len(s.totals) == 0 {
		if s.sawResult {
			return report, run, nil
		}
		return report, run, fmt.Errorf("claude-code: the stream held no result object and no session file was found")
	}
	if err == nil && len(report.Warnings) == 0 {
		report.Warnings = append(report.Warnings, "no session file: one set of rows per model, for the whole run")
	}
	report.CostSource, report.Requests = SourceStream, s.perModel()
	return report, run, nil
}

func (s *Session) keyBilled() bool {
	return slices.Contains(keyBilledSources, s.apiKeySource)
}

func (s *Session) costBasis() focus.CostBasis {
	if s.keyBilled() {
		return focus.Measured
	}
	return focus.ListEquivalent
}

func (s *Session) perModel() []adapter.Request {
	models := make([]string, 0, len(s.totals))
	for model := range s.totals {
		models = append(models, model)
	}
	slices.Sort(models)
	requests := make([]adapter.Request, 0, len(models))
	for _, model := range models {
		tokens, assumed := s.totals[model].tokens(s.keyBilled())
		requests = append(requests, adapter.Request{
			ID:           s.sessionID + ":" + model,
			Provider:     provider,
			Model:        model,
			Start:        s.first,
			End:          s.last,
			ServiceTier:  catalog.TierStandard,
			Region:       catalog.RegionGlobal,
			Grain:        adapter.PerRun,
			Tokens:       tokens,
			AssumedWrite: assumed,
		})
	}
	return requests
}

func (m modelUsage) tokens(keyBilled bool) (adapter.Tokens, focus.TokenClass) {
	tokens := adapter.Tokens{
		InputUncached:       m.InputTokens,
		CacheRead:           m.CacheRead,
		Output:              m.OutputTokens,
		ReasoningOutput:     m.Thinking,
		CachedInputReported: m.CacheRead,
	}
	var assumed focus.TokenClass
	tokens.CacheWrite5m, tokens.CacheWrite1h, assumed = splitWrites(m.CacheCreated, m.FiveMinute, m.OneHour, keyBilled)
	return tokens, assumed
}

// splitWrites keeps the reported lifetimes. Tokens with no reported lifetime
// are assigned by the matrix's rule: five minutes on an API key, one hour on a
// subscription. The class that took them comes back, empty when none did.
func splitWrites(created, fiveMinute, oneHour int64, keyBilled bool) (int64, int64, focus.TokenClass) {
	unsplit := created - fiveMinute - oneHour
	switch {
	case unsplit == 0:
		return fiveMinute, oneHour, ""
	case keyBilled:
		return fiveMinute + unsplit, oneHour, focus.CacheWrite5m
	default:
		return fiveMinute, oneHour + unsplit, focus.CacheWrite1h
	}
}

// agrees compares every billed class per model. The informational classes do
// not decide it: thinking counts are re-tokenized and may differ by a few.
func agrees(requests []adapter.Request, totals map[string]modelUsage) bool {
	summed := map[string]adapter.Tokens{}
	for _, request := range requests {
		summed[request.Model] = summed[request.Model].Add(request.Tokens)
	}
	if len(summed) != len(totals) {
		return false
	}
	for model, want := range totals {
		got := summed[model]
		if got.InputUncached != want.InputTokens || got.CacheRead != want.CacheRead ||
			got.CacheWrite5m+got.CacheWrite1h != want.CacheCreated ||
			got.Output != want.OutputTokens {
			return false
		}
	}
	return true
}

type sessionLine struct {
	Type        string `json:"type"`
	SessionID   string `json:"sessionId"`
	IsSidechain bool   `json:"isSidechain"`
	CWD         string `json:"cwd"`
	Version     string `json:"version"`
	ID          string `json:"id"`
	RequestID   string `json:"requestId"`
	RequestRef  string `json:"requestRef"`
	Timestamp   string `json:"timestamp"`
	Message     struct {
		Model string `json:"model"`
		Usage usage  `json:"usage"`
	} `json:"message"`
}

// transcript is what the run's own session files hold.
type transcript struct {
	// path is the main session file. Empty means none was found.
	path string
	// cwd is the working directory the main session file states first.
	cwd      string
	requests []adapter.Request
}

// readSessionFiles walks the main transcript and every subagent's. Each
// content block of a response is its own line, so a requestId repeats; it
// counts once.
func (s *Session) readSessionFiles() (transcript, error) {
	if s.configDir == "" || !sessionIDPattern.MatchString(s.sessionID) {
		return transcript{}, nil
	}
	files, err := s.sessionFiles()
	if err != nil || len(files) == 0 {
		return transcript{}, err
	}
	builder := newRequestBuilder(s.keyBilled())
	var main fileFacts
	for i, file := range files {
		facts, err := builder.read(file)
		if err != nil {
			return transcript{}, err
		}
		if i == 0 {
			main = facts
		}
	}
	return transcript{path: files[0], cwd: main.cwd, requests: builder.requests()}, nil
}

func (s *Session) sessionFiles() ([]string, error) {
	main, err := filepath.Glob(filepath.Join(s.configDir, "projects", "*", s.sessionID+".jsonl"))
	if err != nil {
		return nil, fmt.Errorf("claude-code: find session file: %w", err)
	}
	if len(main) == 0 {
		return nil, nil
	}
	directory := strings.TrimSuffix(main[0], ".jsonl")
	subagents, err := filepath.Glob(filepath.Join(directory, "subagents", "*.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("claude-code: find subagent files: %w", err)
	}
	return append(main[:1:1], subagents...), nil
}

type requestBuilder struct {
	keyBilled bool
	byID      map[string]*adapter.Request
	order     []string
	startedAt map[string]time.Time
	refs      map[string]string
}

func newRequestBuilder(keyBilled bool) *requestBuilder {
	return &requestBuilder{
		keyBilled: keyBilled,
		byID:      map[string]*adapter.Request{},
		startedAt: map[string]time.Time{},
		refs:      map[string]string{},
	}
}

// read adds one file's requests and returns what the file says about itself.
func (b *requestBuilder) read(path string) (fileFacts, error) {
	facts := fileFacts{sessions: map[string]bool{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return facts, fmt.Errorf("claude-code: read session file: %w", err)
	}
	for number, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var parsed sessionLine
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			return facts, fmt.Errorf("claude-code: session file line %d: %w", number+1, err)
		}
		if err := b.add(parsed); err != nil {
			return facts, fmt.Errorf("claude-code: session file line %d: %w", number+1, err)
		}
		facts.observe(parsed)
	}
	return facts, nil
}

func (b *requestBuilder) add(line sessionLine) error {
	switch line.Type {
	case "api-request":
		at, err := time.Parse(time.RFC3339Nano, line.Timestamp)
		if err != nil {
			return err
		}
		b.startedAt[line.ID] = at
	case "assistant":
		if line.RequestID == "" {
			return nil
		}
		at, err := time.Parse(time.RFC3339Nano, line.Timestamp)
		if err != nil {
			return err
		}
		b.addResponse(line, at)
	}
	return nil
}

func (b *requestBuilder) addResponse(line sessionLine, at time.Time) {
	request, seen := b.byID[line.RequestID]
	if !seen {
		request = &adapter.Request{ID: line.RequestID, Provider: provider, Start: at, Grain: adapter.PerRequest}
		b.byID[line.RequestID] = request
		b.order = append(b.order, line.RequestID)
	}
	b.refs[line.RequestID] = line.RequestRef
	u := line.Message.Usage
	request.Model = line.Message.Model
	request.End = at
	request.ServiceTier = serviceTier(u)
	request.Region = region(u.InferenceGeo)
	request.ContextInputTokens = u.InputTokens + u.CacheRead + u.CacheCreated
	request.Tokens, request.AssumedWrite = b.tokens(u)
}

func (b *requestBuilder) tokens(u usage) (adapter.Tokens, focus.TokenClass) {
	tokens := adapter.Tokens{
		InputUncached:       u.InputTokens,
		CacheRead:           u.CacheRead,
		Output:              u.OutputTokens,
		ReasoningOutput:     u.Details.ThinkingTokens,
		CachedInputReported: u.CacheRead,
	}
	var reported cacheCreation
	if u.CacheCreation != nil {
		reported = *u.CacheCreation
	}
	var assumed focus.TokenClass
	tokens.CacheWrite5m, tokens.CacheWrite1h, assumed = splitWrites(u.CacheCreated, reported.FiveMinute, reported.OneHour, b.keyBilled)
	return tokens, assumed
}

func (b *requestBuilder) requests() []adapter.Request {
	requests := make([]adapter.Request, 0, len(b.order))
	for _, id := range b.order {
		request := *b.byID[id]
		if start, ok := b.startedAt[b.refs[id]]; ok {
			request.Start = start
		}
		requests = append(requests, request)
	}
	slices.SortStableFunc(requests, func(a, b adapter.Request) int {
		return a.Start.Compare(b.Start)
	})
	return requests
}

func serviceTier(u usage) string {
	if u.Speed == "fast" {
		return "fast"
	}
	if u.ServiceTier == "" {
		return catalog.TierStandard
	}
	return u.ServiceTier
}

func region(geo string) string {
	if geo == "" {
		return catalog.RegionGlobal
	}
	return geo
}
