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
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/cicontext"
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
	return a.newSession()
}

// newSession reads the CI system once: a runner inside one hosts one job.
func (a Agent) newSession() *Session {
	return &Session{configDir: a.configDir(), oneJob: cicontext.Detect(os.Getenv) != cicontext.NoSystem}
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

// Session reads one Claude Code run.
type Session struct {
	configDir    string
	sessionID    string
	version      string
	apiKeySource string
	cwd          string
	totals       map[string]modelUsage
	sawResult    bool
	// unreadResult is why the last result line could not be read. Nil when it
	// was read, or when no result line came.
	unreadResult error
	// usageAlone is true when the last result states usage and no per-model
	// totals, which no row can be built from.
	usageAlone bool
	// endedInError is true when the last result says the run ended in an
	// error rather than at a limit the run set.
	endedInError bool
	// stated is the error class the last assistant line stated, if any.
	stated adapter.AgentError
	// skipped counts the lines whose shape this reader does not know.
	skipped int
	// answered names every model an assistant message on the stream came from.
	answered    map[string]bool
	first, last time.Time
	// ranFrom and ranTo are when the agent's process ran, when known.
	ranFrom, ranTo time.Time
	// oneJob is true inside a CI system, where the runner hosts this job alone.
	oneJob bool
}

// limitSubtypes are the result subtypes of a run that stopped at a limit its
// command set. Such a run did not fail, so it carries no error class.
var limitSubtypes = []string{"error_max_budget_usd", "error_max_turns", "error_max_structured_output_retries"}

// Feed reads one stdout line. A line that is not JSON is not the agent's
// stream and carries no usage. The line's type is read first, then the fields
// that type carries. A line whose shape this reader does not know is skipped
// and counted, unless it is a result: the result holds the run's figures, so
// a result that cannot be read stops the run's reading at Finish.
func (s *Session) Feed(line []byte, at time.Time) error {
	if s.first.IsZero() {
		s.first = at
	}
	s.last = at
	if !json.Valid(line) {
		return fmt.Errorf("claude-code: stream line is not JSON")
	}
	var envelope struct {
		Type    json.RawMessage `json:"type"`
		Subtype json.RawMessage `json:"subtype"`
	}
	kind, ok := "", false
	if json.Unmarshal(line, &envelope) == nil {
		kind, ok = jsonString(envelope.Type)
	}
	if !ok {
		s.skipped++
		return nil
	}
	subtype, _ := jsonString(envelope.Subtype)
	switch kind {
	case "system":
		if subtype == "init" {
			s.readInit(line)
		}
	case "assistant":
		s.readAssistant(line)
	case "result":
		s.readResult(line, subtype)
	}
	return nil
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

func (s *Session) readInit(line []byte) {
	var init struct {
		SessionID    string `json:"session_id"`
		APIKeySource string `json:"apiKeySource"`
		Version      string `json:"claude_code_version"`
		CWD          string `json:"cwd"`
	}
	if json.Unmarshal(line, &init) != nil {
		s.skipped++
		return
	}
	s.sessionID, s.version, s.apiKeySource = init.SessionID, init.Version, init.APIKeySource
	if s.cwd == "" {
		s.cwd = init.CWD
	}
}

func (s *Session) readAssistant(line []byte) {
	var assistant struct {
		// Message is an object on an assistant line and can be a plain string
		// on other lines, so it is decoded only where a model is read from it.
		Message json.RawMessage `json:"message"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal(line, &assistant) != nil {
		s.skipped++
		return
	}
	var message struct {
		Model string `json:"model"`
	}
	// A message that is not an object names no model; the line still counts.
	if json.Unmarshal(assistant.Message, &message) == nil && message.Model != "" {
		if s.answered == nil {
			s.answered = map[string]bool{}
		}
		s.answered[message.Model] = true
	}
	if len(assistant.Error) > 0 && string(assistant.Error) != "null" {
		class, _ := jsonString(assistant.Error)
		s.stated = adapter.ParseAgentError(class)
	}
}

// readResult keeps the run's figures. A run can hold several results.
// modelUsage is cumulative, so the last one wins and adding them would double
// count; a later result that reads replaces an earlier one that did not.
func (s *Session) readResult(line []byte, subtype string) {
	s.sawResult = true
	var result struct {
		SessionID  json.RawMessage `json:"session_id"`
		IsError    json.RawMessage `json:"is_error"`
		Usage      json.RawMessage `json:"usage"`
		ModelUsage json.RawMessage `json:"modelUsage"`
	}
	if err := json.Unmarshal(line, &result); err != nil {
		s.unreadResult = err
		return
	}
	totals, err := readModelUsage(result.ModelUsage)
	if err != nil {
		s.unreadResult = err
		return
	}
	s.unreadResult, s.totals = nil, totals
	s.usageAlone = len(totals) == 0 && statesUsage(result.Usage)
	s.endedInError = string(result.IsError) == "true" && !slices.Contains(limitSubtypes, subtype)
	if id, ok := jsonString(result.SessionID); ok && id != "" {
		s.sessionID = id
	}
}

// modelUsageFields are the counts the result states per model, by name.
var modelUsageFields = []struct {
	name  string
	field func(*modelUsage) *int64
}{
	{"inputTokens", func(m *modelUsage) *int64 { return &m.InputTokens }},
	{"outputTokens", func(m *modelUsage) *int64 { return &m.OutputTokens }},
	{"cacheReadInputTokens", func(m *modelUsage) *int64 { return &m.CacheRead }},
	{"cacheCreationInputTokens", func(m *modelUsage) *int64 { return &m.CacheCreated }},
	{"cacheCreationEphemeral5mInputTokens", func(m *modelUsage) *int64 { return &m.FiveMinute }},
	{"cacheCreationEphemeral1hInputTokens", func(m *modelUsage) *int64 { return &m.OneHour }},
	{"thinkingTokens", func(m *modelUsage) *int64 { return &m.Thinking }},
}

// readModelUsage reads the result's per-model totals. Absent or null is no
// models. Every count is read whole; a field the reader does not price is
// left unread, whatever its type.
func readModelUsage(raw json.RawMessage) (map[string]modelUsage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var models map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &models); err != nil {
		return nil, errors.New("modelUsage is not an object of models")
	}
	totals := make(map[string]modelUsage, len(models))
	for model, fields := range models {
		var totalsOfModel modelUsage
		for _, field := range modelUsageFields {
			value, err := wholeCount(fields[field.name])
			if err != nil {
				return nil, fmt.Errorf("modelUsage %s %s: %w", model, field.name, err)
			}
			*field.field(&totalsOfModel) = value
		}
		totals[model] = totalsOfModel
	}
	return totals, nil
}

// statesUsage says whether a result's usage object holds any tokens. A usage
// that cannot be read counts as stating some, so the run is never read as
// one that used nothing.
func statesUsage(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return true
	}
	for _, name := range []string{"input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"} {
		value, err := wholeCount(fields[name])
		if err != nil || value != 0 {
			return true
		}
	}
	return false
}

// numberPattern is a JSON number: sign, integer part, fraction, exponent.
var numberPattern = regexp.MustCompile(`^(-?)(0|[1-9][0-9]*)(?:\.([0-9]+))?(?:[eE]([+-]?[0-9]+))?$`)

// numeral is a JSON number held exactly, as digits times a power of ten.
type numeral struct {
	negative bool
	// digits has no leading zero. Empty means zero.
	digits   string
	exponent int64
}

var (
	errNotWhole    = errors.New("is not a whole number")
	errPastLargest = errors.New("is past the largest count")
)

const (
	// farthestExponent stands in for an exponent too large to hold. Any
	// count it scales is past the largest or below one.
	farthestExponent = 1 << 40
	// largestCountDigits is the most digits an int64 count can hold.
	largestCountDigits = 19
)

// readNumeral reads a JSON number, or a string that holds one.
func readNumeral(raw json.RawMessage) (numeral, bool) {
	literal := string(raw)
	if text, ok := jsonString(raw); ok {
		literal = text
	}
	parts := numberPattern.FindStringSubmatch(literal)
	if parts == nil {
		return numeral{}, false
	}
	exponent, err := strconv.ParseInt(parts[4], 10, 32)
	switch {
	case parts[4] == "":
		exponent = 0
	case err != nil && strings.HasPrefix(parts[4], "-"):
		exponent = -farthestExponent
	case err != nil:
		exponent = farthestExponent
	}
	return numeral{
		negative: parts[1] == "-",
		digits:   strings.TrimLeft(parts[2]+parts[3], "0"),
		exponent: exponent - int64(len(parts[3])),
	}, true
}

// integer is the numeral's value, when it is a whole number that fits.
func (n numeral) integer() (int64, error) {
	digits, exponent := n.digits, n.exponent
	if digits == "" {
		return 0, nil
	}
	for exponent < 0 && strings.HasSuffix(digits, "0") {
		digits, exponent = digits[:len(digits)-1], exponent+1
	}
	if exponent < 0 {
		return 0, errNotWhole
	}
	if int64(len(digits))+exponent > largestCountDigits {
		return 0, errPastLargest
	}
	value, err := strconv.ParseInt(digits+strings.Repeat("0", int(exponent)), 10, 64)
	if err != nil {
		return 0, errPastLargest
	}
	if n.negative {
		return -value, nil
	}
	return value, nil
}

// wholeCount reads a token count stated as 10, 10.0 or "10". Absent and null
// are zero. A fraction, a negative, a figure past the largest count and any
// other value are refused, because a count is a whole number of tokens.
func wholeCount(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}
	number, ok := readNumeral(raw)
	if !ok {
		return 0, fmt.Errorf("%s is not a number", shown(raw))
	}
	if number.negative && number.digits != "" {
		return 0, fmt.Errorf("%s is negative", shown(raw))
	}
	value, err := number.integer()
	if err != nil {
		return 0, fmt.Errorf("%s %w", shown(raw), err)
	}
	return value, nil
}

// shown is a value as an error states it, cut short so one line holds it.
func shown(raw json.RawMessage) string {
	const most = 32
	if len(raw) > most {
		return string(raw[:most]) + "..."
	}
	return string(raw)
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
	if s.skipped > 0 {
		report.Warnings = append(report.Warnings, skippedLines(s.skipped))
	}
	return report, nil
}

func skippedLines(count int) string {
	if count == 1 {
		return "1 stream line was skipped: its shape is not one this reader knows, and it is not a result object"
	}
	return fmt.Sprintf("%d stream lines were skipped: their shape is not one this reader knows, and none is a result object", count)
}

// agentError is the class the run ended in. A result that says the run
// ended in an error takes the class the last assistant line stated, or
// unknown when none did. With no result, a stated class stands alone.
func (s *Session) agentError() adapter.AgentError {
	switch {
	case s.sawResult && !s.endedInError:
		return ""
	case s.stated != "":
		return s.stated
	case s.sawResult:
		return adapter.AgentErrorUnknown
	default:
		return ""
	}
}

// checkResult refuses a result no row can be built from: one that could not
// be read, one that states usage with no per-model totals, and one whose
// totals come to a negative class.
func (s *Session) checkResult() error {
	if s.unreadResult != nil {
		return fmt.Errorf("claude-code: the result object could not be read: %w", s.unreadResult)
	}
	if s.usageAlone {
		version := s.version
		if version == "" {
			version = "of an unstated version"
		}
		return fmt.Errorf("claude-code: the result states usage but no per-model totals; Claude Code %s is not read", version)
	}
	for _, model := range slices.Sorted(maps.Keys(s.totals)) {
		tokens, _ := s.totals[model].tokens(s.keyBilled())
		if err := tokens.Check(); err != nil {
			return fmt.Errorf("claude-code: the result's totals for %s do not add up: %w", model, err)
		}
	}
	return nil
}

func (s *Session) finishRun() (adapter.Report, transcript, error) {
	report := adapter.Report{SessionID: s.sessionID, AgentVersion: s.version, CostBasis: s.costBasis(), AgentError: s.agentError()}
	if err := s.checkResult(); err != nil {
		return report, transcript{}, err
	}
	run, err := s.readSessionFiles()
	requests := run.requests
	if err != nil {
		report.Warnings = append(report.Warnings, err.Error())
	}
	side := s.sideModels(requests)
	switch {
	case len(requests) > 0 && !s.sawResult:
		report.Warnings = append(report.Warnings, "no result object: the session files are the only record of this run")
	case len(requests) > 0 && !agrees(requests, s.totals, side):
		report.Warnings = append(report.Warnings, "the session files disagree with the result object; the result's per-model totals were used")
		requests = nil
	case len(requests) > 0:
		requests = append(requests, s.sideRequests(side)...)
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
	report.CostSource, report.Requests = SourceStream, s.perModel(nil)
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

// sideModels are the models the result counts that no session file line and
// no assistant message on the stream names. Claude Code makes such requests
// outside the conversation, and writes them nowhere but the result's totals.
func (s *Session) sideModels(requests []adapter.Request) map[string]bool {
	named := map[string]bool{}
	for _, request := range requests {
		named[request.Model] = true
	}
	side := map[string]bool{}
	for model := range s.totals {
		if !named[model] && !s.answered[model] {
			side[model] = true
		}
	}
	return side
}

// sideRequests are one entry per side model, from the result's totals. The
// model's whole input bounds each of its requests', so it selects the context
// tier.
func (s *Session) sideRequests(side map[string]bool) []adapter.Request {
	requests := s.perModel(side)
	for i := range requests {
		total := s.totals[requests[i].Model]
		requests[i].ContextInputTokens = total.InputTokens + total.CacheRead + total.CacheCreated
		requests[i].CostSource = SourceStream
		requests[i].Note = "a request outside the conversation: the result counts it, and no session file or stream message holds it"
	}
	return requests
}

// perModel is one entry per model in the result's totals. A non-nil only
// keeps the models it names.
func (s *Session) perModel(only map[string]bool) []adapter.Request {
	models := make([]string, 0, len(s.totals))
	for model := range s.totals {
		if only == nil || only[model] {
			models = append(models, model)
		}
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

// agrees compares every billed class per model, except the side models, which
// no file holds. The informational classes do not decide it: thinking counts
// are re-tokenized and may differ by a few.
func agrees(requests []adapter.Request, totals map[string]modelUsage, side map[string]bool) bool {
	summed := map[string]adapter.Tokens{}
	for _, request := range requests {
		summed[request.Model] = summed[request.Model].Add(request.Tokens)
	}
	if len(summed) != len(totals)-len(side) {
		return false
	}
	for model, want := range totals {
		if side[model] {
			continue
		}
		got, ok := summed[model]
		if !ok {
			return false
		}
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
