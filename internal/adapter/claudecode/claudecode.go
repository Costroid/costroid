// Package claudecode reads Claude Code runs.
//
// The stream supplies the session id, the billing source and the result's
// per-model totals. Its assistant messages carry a placeholder output count,
// so per-request rows come from the session files Claude Code writes, checked
// against those totals. Without the files a run falls to one set of rows per
// model.
package claudecode

import (
	"bytes"
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

	// oneHourCacheVariable makes Claude Code write its cache for one hour on
	// an API key, where it writes for five minutes without it.
	oneHourCacheVariable = "ENABLE_PROMPT_CACHING_1H"

	// requestBound is the most tokens one request's input may hold. No model
	// takes near it, and below it no sum of counts can wrap.
	requestBound = 1_000_000_000_000
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

// newSession reads the CI system once: a runner inside one hosts one job. It
// reads ENABLE_PROMPT_CACHING_1H from the environment the agent inherits.
func (a Agent) newSession() *Session {
	return &Session{
		configDir:    a.configDir(),
		oneJob:       cicontext.Detect(os.Getenv) != cicontext.NoSystem,
		oneHourCache: os.Getenv(oneHourCacheVariable) != "",
	}
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

// modelUsage is the result object's total for one model, subagents included.
type modelUsage struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
	CacheRead    int64 `json:"cacheReadInputTokens"`
	CacheCreated int64 `json:"cacheCreationInputTokens"`
	FiveMinute   int64 `json:"cacheCreationEphemeral5mInputTokens"`
	OneHour      int64 `json:"cacheCreationEphemeral1hInputTokens"`
	Thinking     int64 `json:"thinkingTokens"`
	// context is the model's whole input: uncached, read and written.
	context int64
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
	answered map[string]bool
	// routes holds, per model, the tiers and regions its assistant messages
	// on the stream stated.
	routes      map[string]*route
	first, last time.Time
	// ranFrom and ranTo are when the agent's process ran, when known.
	ranFrom, ranTo time.Time
	// oneJob is true inside a CI system, where the runner hosts this job alone.
	oneJob bool
	// oneHourCache is true when ENABLE_PROMPT_CACHING_1H is set.
	oneHourCache bool
	// billedThroughGateway is true when the run starts the agent on a
	// gateway's key, which the gateway bills per token upstream.
	billedThroughGateway bool
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
		Model string          `json:"model"`
		Usage json.RawMessage `json:"usage"`
	}
	// A message that is not an object names no model; the line still counts.
	if json.Unmarshal(assistant.Message, &message) == nil && message.Model != "" {
		if s.answered == nil {
			s.answered, s.routes = map[string]bool{}, map[string]*route{}
		}
		s.answered[message.Model] = true
		if s.routes[message.Model] == nil {
			s.routes[message.Model] = &route{tiers: map[string]bool{}, regions: map[string]bool{}}
		}
		s.routes[message.Model].observe(message.Usage)
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
		context, err := contextInput(totalsOfModel.InputTokens, totalsOfModel.CacheRead, totalsOfModel.CacheCreated)
		if err != nil {
			return nil, fmt.Errorf("modelUsage %s: %w", model, err)
		}
		totalsOfModel.context = context
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

// errPastBound marks an input no request can hold. A run that states one is
// refused, because its context tier and its figure cannot be trusted.
var errPastBound = fmt.Errorf("the input is past the bound of %d tokens one request can hold", int64(requestBound))

// contextInput is a request's whole input. A count or a sum past
// requestBound is refused; each count is first held to the bound, so the sum
// cannot wrap.
func contextInput(input, read, written int64) (int64, error) {
	if input > requestBound || read > requestBound || written > requestBound || input+read+written > requestBound {
		return 0, fmt.Errorf("%w: input %d, cache reads %d, cache writes %d", errPastBound, input, read, written)
	}
	return input + read + written, nil
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
// cache lifetimes sum past its written total.
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
		if _, _, err := s.totals[model].tokens(s.unsplitWrite()); err != nil {
			return fmt.Errorf("claude-code: the result's totals for %s do not add up: %w", model, err)
		}
	}
	return nil
}

// finishRun reads the session files beside the result. A count in them past
// the bound refuses the run. Any other count they cannot give discards them:
// the result's totals stand, and with no result the run is refused.
func (s *Session) finishRun() (adapter.Report, transcript, error) {
	report := adapter.Report{SessionID: s.sessionID, AgentVersion: s.version, CostBasis: s.costBasis(), AgentError: s.agentError()}
	if err := s.checkResult(); err != nil {
		return report, transcript{}, err
	}
	run, err := s.readSessionFiles()
	switch {
	case errors.Is(err, errPastBound):
		return report, transcript{}, fmt.Errorf("claude-code: %w", err)
	case err != nil && !s.sawResult:
		return report, transcript{}, fmt.Errorf("claude-code: no result object, and the session files could not be read: %w", err)
	case err != nil:
		report.Warnings = append(report.Warnings, fmt.Sprintf("claude-code: the session files were not used, and the result's totals stand: %v", err))
	}
	requests := run.requests
	side := s.sideModels(requests)
	switch {
	case len(requests) > 0 && !s.sawResult:
		report.Warnings = append(report.Warnings, "no result object: the session files are the only record of this run")
	case len(requests) > 0 && !agrees(requests, s.totals, side):
		report.Warnings = append(report.Warnings, "the session files disagree with the result object; the result's per-model totals were used")
		requests = nil
	case len(requests) > 0:
		more, err := s.sideRequests(side)
		if err != nil {
			return report, run, err
		}
		requests = append(requests, more...)
	}
	if !s.sawResult {
		reportInFlight(&report, run.inFlight)
	}
	if len(requests) > 0 {
		report.Warnings = append(report.Warnings, run.warnings()...)
		report.CostSource, report.Requests = SourceSessionFile, requests
		return report, run, nil
	}
	if len(s.totals) == 0 {
		switch {
		case s.sawResult || run.inFlight > 0:
			return report, run, nil
		case run.path == "":
			return report, run, fmt.Errorf("claude-code: the stream held no result object and no session file was found")
		default:
			return report, run, fmt.Errorf("claude-code: the stream held no result object, and the session files hold no request")
		}
	}
	if err == nil && len(report.Warnings) == 0 {
		report.Warnings = append(report.Warnings, "no session file: one set of rows per model, for the whole run")
	}
	perModel, err := s.perModel(nil)
	if err != nil {
		return report, run, err
	}
	report.CostSource, report.Requests = SourceStream, perModel
	return report, run, nil
}

// reportInFlight says what a run with no result lost. A session file that
// ends inside a line was cut while Claude Code wrote it, and that line is the
// request in flight. No result counts it, so no row holds it.
func reportInFlight(report *adapter.Report, inFlight int) {
	if inFlight == 0 {
		return
	}
	report.Unrecorded = inFlight
	if inFlight == 1 {
		report.Warnings = append(report.Warnings, "the session file ends inside a line, which is the request in flight: no result counts it, so its tokens reach no row")
		return
	}
	report.Warnings = append(report.Warnings, fmt.Sprintf("%d session files end inside a line, each the request in flight: no result counts them, so their tokens reach no row", inFlight))
}

func (s *Session) keyBilled() bool {
	return slices.Contains(keyBilledSources, s.apiKeySource)
}

// unsplitWrite is the class that takes cache writes whose lifetime the
// source did not state: five minutes on an API key unless
// ENABLE_PROMPT_CACHING_1H is set, and one hour on a subscription.
func (s *Session) unsplitWrite() focus.TokenClass {
	if s.keyBilled() && !s.oneHourCache {
		return focus.CacheWrite5m
	}
	return focus.CacheWrite1h
}

func (s *Session) costBasis() focus.CostBasis {
	if s.keyBilled() || s.billedThroughGateway {
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

// sideRequests are one entry per side model, from the result's totals. No
// stream message names a side model, and the note says so in place of the
// note on a tier no message stated.
func (s *Session) sideRequests(side map[string]bool) ([]adapter.Request, error) {
	requests, err := s.perModel(side)
	if err != nil {
		return nil, err
	}
	for i := range requests {
		requests[i].CostSource = SourceStream
		requests[i].Note = "a request outside the conversation: the result counts it, and no session file or stream message holds it"
	}
	return requests, nil
}

// route is what one model's assistant messages on the stream stated about
// where they ran: the service tier, fast mode read as the fast tier, and the
// inference region.
type route struct {
	tiers, regions map[string]bool
}

// observe reads one message's usage. The stream's counts are placeholders,
// so only the routing fields are read, and a field that is not text states
// nothing.
func (r *route) observe(raw json.RawMessage) {
	var fields struct {
		ServiceTier  json.RawMessage `json:"service_tier"`
		InferenceGeo json.RawMessage `json:"inference_geo"`
		Speed        json.RawMessage `json:"speed"`
	}
	if !isObject(raw) || json.Unmarshal(raw, &fields) != nil {
		return
	}
	tier, tierStated := jsonString(fields.ServiceTier)
	speed, speedStated := jsonString(fields.Speed)
	if tierStated || speedStated {
		r.tiers[serviceTier(usage{ServiceTier: tier, Speed: speed})] = true
	}
	if geo, ok := jsonString(fields.InferenceGeo); ok {
		r.regions[region(geo)] = true
	}
}

// settle is the tier and region one model's whole-run rows take, with a note
// when the stream does not state them alone. Fast mode or a residency stated
// on any message prices the whole run: exact when every message stated it,
// an upper bound when some did not. Neither ever reads under the run.
func (r *route) settle() (tier, where, note string) {
	if r == nil {
		r = &route{}
	}
	tier, tierNote, tierStated := r.tier()
	where, regionNote, regionStated := r.region()
	switch {
	case !tierStated && !regionStated:
		return tier, where, "no stream message states this model's service tier or region, so the standard tier and global price it"
	case !tierStated:
		tierNote = "no stream message states this model's service tier, so the standard tier prices it"
	case !regionStated:
		regionNote = "no stream message states this model's region, so global prices it"
	}
	return tier, where, joinNotes(tierNote, regionNote)
}

func (r *route) tier() (tier, note string, stated bool) {
	tiers := slices.Sorted(maps.Keys(r.tiers))
	switch {
	case len(tiers) == 0:
		return catalog.TierStandard, "", false
	case r.tiers[catalog.TierFast] && len(tiers) > 1:
		return catalog.TierFast, "fast mode shows on some of this model's stream messages, so the fast tier prices all of them, an upper bound", true
	case len(tiers) == 1:
		return tiers[0], "", true
	}
	return catalog.TierStandard, fmt.Sprintf("the stream states the %s tiers for this model, and one total cannot split them, so the standard tier prices it", strings.Join(tiers, " and ")), true
}

func (r *route) region() (where, note string, stated bool) {
	if len(r.regions) == 0 {
		return catalog.RegionGlobal, "", false
	}
	var resident []string
	for _, where := range slices.Sorted(maps.Keys(r.regions)) {
		if where != catalog.RegionGlobal {
			resident = append(resident, where)
		}
	}
	switch {
	case len(resident) == 0:
		return catalog.RegionGlobal, "", true
	case len(resident) == 1 && r.regions[catalog.RegionGlobal]:
		return resident[0], fmt.Sprintf("data residency in %s shows on some of this model's stream messages, so it prices all of them, an upper bound", resident[0]), true
	case len(resident) == 1:
		return resident[0], "", true
	}
	return catalog.RegionGlobal, fmt.Sprintf("the stream states the regions %s for this model, and one total cannot split them, so global prices it", strings.Join(resident, " and ")), true
}

// joinNotes joins the notes that say something, as a row's description does.
func joinNotes(notes ...string) string {
	return strings.Join(slices.DeleteFunc(notes, func(note string) bool { return note == "" }), "; ")
}

// perModel is one entry per model in the result's totals. A non-nil only
// keeps the models it names. The model's whole input bounds each of its
// requests', so it selects the context tier as an upper bound. The tier and
// region are what the model's stream messages stated.
func (s *Session) perModel(only map[string]bool) ([]adapter.Request, error) {
	models := make([]string, 0, len(s.totals))
	for model := range s.totals {
		if only == nil || only[model] {
			models = append(models, model)
		}
	}
	slices.Sort(models)
	requests := make([]adapter.Request, 0, len(models))
	for _, model := range models {
		tokens, assumed, err := s.totals[model].tokens(s.unsplitWrite())
		if err != nil {
			return nil, fmt.Errorf("claude-code: the result's totals for %s do not add up: %w", model, err)
		}
		tier, where, note := s.routes[model].settle()
		requests = append(requests, adapter.Request{
			ID:                 s.sessionID + ":" + model,
			Provider:           provider,
			Model:              model,
			Start:              s.first,
			End:                s.last,
			ServiceTier:        tier,
			Region:             where,
			ContextInputTokens: s.totals[model].context,
			Grain:              adapter.PerRun,
			Tokens:             tokens,
			AssumedWrite:       assumed,
			Note:               note,
		})
	}
	return requests, nil
}

func (m modelUsage) tokens(unsplit focus.TokenClass) (adapter.Tokens, focus.TokenClass, error) {
	tokens := adapter.Tokens{
		InputUncached:       m.InputTokens,
		CacheRead:           m.CacheRead,
		Output:              m.OutputTokens,
		ReasoningOutput:     m.Thinking,
		CachedInputReported: m.CacheRead,
	}
	var assumed focus.TokenClass
	var err error
	tokens.CacheWrite5m, tokens.CacheWrite1h, assumed, err = splitWrites(m.CacheCreated, m.FiveMinute, m.OneHour, unsplit)
	return tokens, assumed, err
}

// splitWrites keeps the reported lifetimes. Tokens with no reported lifetime
// go to the unsplit class, which comes back as the class that took them,
// empty when none did. Lifetimes that sum past the written total are refused.
// The counts are never negative, so the comparisons cannot wrap.
func splitWrites(created, fiveMinute, oneHour int64, unsplit focus.TokenClass) (int64, int64, focus.TokenClass, error) {
	if fiveMinute > created || oneHour > created-fiveMinute {
		return 0, 0, "", fmt.Errorf("cache writes of %d tokens for five minutes and %d for one hour pass the written total of %d", fiveMinute, oneHour, created)
	}
	rest := created - fiveMinute - oneHour
	switch {
	case rest == 0:
		return fiveMinute, oneHour, "", nil
	case unsplit == focus.CacheWrite5m:
		return fiveMinute + rest, oneHour, focus.CacheWrite5m, nil
	default:
		return fiveMinute, oneHour + rest, focus.CacheWrite1h, nil
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

// sessionEnvelope is a session file line's own fields, each kept raw so that
// a line of any shape decodes.
type sessionEnvelope struct {
	Type        json.RawMessage `json:"type"`
	SessionID   json.RawMessage `json:"sessionId"`
	IsSidechain json.RawMessage `json:"isSidechain"`
	CWD         json.RawMessage `json:"cwd"`
	Version     json.RawMessage `json:"version"`
	ID          json.RawMessage `json:"id"`
	RequestID   json.RawMessage `json:"requestId"`
	RequestRef  json.RawMessage `json:"requestRef"`
	Timestamp   json.RawMessage `json:"timestamp"`
	Message     json.RawMessage `json:"message"`
}

// sessionLine is what a session file line states about itself. A field that
// is absent or of another type reads as empty.
type sessionLine struct {
	kind       string
	sessionID  string
	sidechain  bool
	cwd        string
	version    string
	id         string
	requestRef string
	// requestID is kept raw, so a value that is not text is told from none.
	requestID json.RawMessage
	// at is zero when the line states no time that reads as RFC 3339.
	at      time.Time
	message json.RawMessage
}

// parseSessionLine reads a line's envelope. It is false for a line that is
// not a JSON object, or that states no type as text.
func parseSessionLine(raw []byte) (sessionLine, bool) {
	var envelope sessionEnvelope
	if json.Unmarshal(raw, &envelope) != nil {
		return sessionLine{}, false
	}
	kind, ok := jsonString(envelope.Type)
	if !ok {
		return sessionLine{}, false
	}
	text := func(raw json.RawMessage) string {
		value, _ := jsonString(raw)
		return value
	}
	line := sessionLine{
		kind:       kind,
		sessionID:  text(envelope.SessionID),
		sidechain:  string(envelope.IsSidechain) == "true",
		cwd:        text(envelope.CWD),
		version:    text(envelope.Version),
		id:         text(envelope.ID),
		requestRef: text(envelope.RequestRef),
		requestID:  envelope.RequestID,
		message:    envelope.Message,
	}
	if at, err := time.Parse(time.RFC3339Nano, text(envelope.Timestamp)); err == nil {
		line.at = at
	}
	return line, true
}

// transcript is what the run's own session files hold.
type transcript struct {
	// path is the main session file. Empty means none was found.
	path string
	// cwd is the working directory the main session file states first.
	cwd      string
	requests []adapter.Request
	fileCounts
}

// fileCounts are the lines the reader passed over in a run's files.
type fileCounts struct {
	// skipped counts the lines whose shape the reader does not know.
	skipped int
	// untimed counts the responses that state no time.
	untimed int
	// inFlight counts the files that end inside a line.
	inFlight int
}

func (c *fileCounts) add(other fileCounts) {
	c.skipped += other.skipped
	c.untimed += other.untimed
	c.inFlight += other.inFlight
}

// warnings says what the reader passed over in files whose requests it used.
func (c fileCounts) warnings() []string {
	var warnings []string
	if c.skipped > 0 {
		warnings = append(warnings, fmt.Sprintf("%d session file %s skipped: %s shape is not one this reader knows",
			c.skipped, plural(c.skipped, "line was", "lines were"), plural(c.skipped, "its", "their")))
	}
	if c.untimed > 0 {
		warnings = append(warnings, fmt.Sprintf("%d %s in the session files %s no time, so %s the time of the line before it",
			c.untimed, plural(c.untimed, "response", "responses"), plural(c.untimed, "states", "state"), plural(c.untimed, "it takes", "each takes")))
	}
	return warnings
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
	builder := newRequestBuilder(s.unsplitWrite())
	run := transcript{path: files[0]}
	for i, file := range files {
		facts, err := builder.read(file)
		if err != nil {
			return transcript{}, err
		}
		if i == 0 {
			run.cwd = facts.cwd
		}
		run.add(facts.fileCounts)
	}
	run.requests = builder.requests(s.window())
	return run, nil
}

func (s *Session) sessionFiles() ([]string, error) {
	main, err := filepath.Glob(filepath.Join(s.configDir, "projects", "*", s.sessionID+".jsonl"))
	if err != nil {
		return nil, fmt.Errorf("find session file: %w", err)
	}
	if len(main) == 0 {
		return nil, nil
	}
	directory := strings.TrimSuffix(main[0], ".jsonl")
	subagents, err := filepath.Glob(filepath.Join(directory, "subagents", "*.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("find subagent files: %w", err)
	}
	return append(main[:1:1], subagents...), nil
}

type requestBuilder struct {
	// unsplit is the class that takes writes with no stated lifetime.
	unsplit   focus.TokenClass
	byID      map[string]*adapter.Request
	order     []string
	startedAt map[string]time.Time
	refs      map[string]string
}

func newRequestBuilder(unsplit focus.TokenClass) *requestBuilder {
	return &requestBuilder{
		unsplit:   unsplit,
		byID:      map[string]*adapter.Request{},
		startedAt: map[string]time.Time{},
		refs:      map[string]string{},
	}
}

// read adds one file's requests and returns what the file says about itself.
func (b *requestBuilder) read(path string) (fileFacts, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return fileFacts{sessions: map[string]bool{}}, fmt.Errorf("read session file: %w", err)
	}
	return b.readData(data)
}

// readData adds the requests in one file's text. Only api-request and
// assistant lines are read; a line of any other type is passed over whatever
// its shape, and a line with no type it can read is counted. Text after the
// last line break that does not parse is a line Claude Code was writing when
// the run stopped: the request in flight.
func (b *requestBuilder) readData(data []byte) (fileFacts, error) {
	facts := fileFacts{sessions: map[string]bool{}}
	lines := strings.Split(string(data), "\n")
	var previous time.Time
	for number, text := range lines {
		if strings.TrimSpace(text) == "" {
			continue
		}
		line, ok := parseSessionLine([]byte(text))
		switch {
		case !ok && number == len(lines)-1 && !json.Valid([]byte(text)):
			facts.inFlight++
			continue
		case !ok:
			facts.skipped++
			continue
		}
		if err := b.add(line, previous, &facts); err != nil {
			return facts, fmt.Errorf("session file line %d: %w", number+1, err)
		}
		if !line.at.IsZero() {
			previous = line.at
		}
		facts.observe(line)
	}
	return facts, nil
}

// add reads one line of a type the reader reads. A response that states no
// time takes the time of the line before it.
func (b *requestBuilder) add(line sessionLine, previous time.Time, facts *fileFacts) error {
	switch line.kind {
	case "api-request":
		if line.id == "" || line.at.IsZero() {
			facts.skipped++
			return nil
		}
		b.startedAt[line.id] = line.at
	case "assistant":
		id, err := responseID(line.requestID)
		if err != nil || id == "" {
			return err
		}
		if line.at.IsZero() {
			line.at = previous
			facts.untimed++
		}
		if err := b.addResponse(id, line); err != nil {
			return fmt.Errorf("request %s: %w", id, err)
		}
	}
	return nil
}

// responseID is the request an assistant line answers. Empty means the line
// answers none, as a message Claude Code writes itself does not.
func responseID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	id, ok := jsonString(raw)
	if !ok {
		return "", fmt.Errorf("requestId %s is not text", shown(raw))
	}
	return id, nil
}

// addResponse reads the usage of one response. A count it cannot read fails
// the line, and a count past the bound fails it with errPastBound.
func (b *requestBuilder) addResponse(id string, line sessionLine) error {
	u, model, err := readResponse(line.message)
	if err != nil {
		return err
	}
	context, err := contextInput(u.InputTokens, u.CacheRead, u.CacheCreated)
	if err != nil {
		return err
	}
	tokens, assumed, err := b.tokens(u)
	if err != nil {
		return err
	}
	request, seen := b.byID[id]
	if !seen {
		request = &adapter.Request{ID: id, Provider: provider, Grain: adapter.PerRequest}
		b.byID[id] = request
		b.order = append(b.order, id)
	}
	b.refs[id] = line.requestRef
	if request.Start.IsZero() {
		request.Start = line.at
	}
	request.Model = model
	request.End = line.at
	request.ServiceTier = serviceTier(u)
	request.Region = region(u.InferenceGeo)
	request.ContextInputTokens = context
	request.Tokens, request.AssumedWrite = tokens, assumed
	return nil
}

// cacheCreation is the written tokens' stated lifetimes.
type cacheCreation struct {
	FiveMinute int64
	OneHour    int64
}

// usage is one response's counts and the fields that select its price.
type usage struct {
	InputTokens    int64
	OutputTokens   int64
	CacheRead      int64
	CacheCreated   int64
	CacheCreation  *cacheCreation
	ThinkingTokens int64
	ServiceTier    string
	InferenceGeo   string
	Speed          string
}

// readResponse reads the model and the usage of an assistant line's message.
// Every count is read whole. A message that is not an object, or states no
// usage, holds no figure, and fails.
func readResponse(raw json.RawMessage) (usage, string, error) {
	var message struct {
		Model json.RawMessage `json:"model"`
		Usage json.RawMessage `json:"usage"`
	}
	if !isObject(raw) || json.Unmarshal(raw, &message) != nil {
		return usage{}, "", errors.New("the message is not an object")
	}
	model, err := optionalText("model", message.Model)
	if err != nil {
		return usage{}, "", err
	}
	if !isObject(message.Usage) {
		return usage{}, "", errors.New("the message states no usage object")
	}
	var fields struct {
		InputTokens   json.RawMessage `json:"input_tokens"`
		OutputTokens  json.RawMessage `json:"output_tokens"`
		CacheRead     json.RawMessage `json:"cache_read_input_tokens"`
		CacheCreated  json.RawMessage `json:"cache_creation_input_tokens"`
		CacheCreation json.RawMessage `json:"cache_creation"`
		Details       json.RawMessage `json:"output_tokens_details"`
		ServiceTier   json.RawMessage `json:"service_tier"`
		InferenceGeo  json.RawMessage `json:"inference_geo"`
		Speed         json.RawMessage `json:"speed"`
	}
	if err := json.Unmarshal(message.Usage, &fields); err != nil {
		return usage{}, "", fmt.Errorf("usage: %w", err)
	}
	var u usage
	counts := []struct {
		name  string
		raw   json.RawMessage
		value *int64
	}{
		{"input_tokens", fields.InputTokens, &u.InputTokens},
		{"output_tokens", fields.OutputTokens, &u.OutputTokens},
		{"cache_read_input_tokens", fields.CacheRead, &u.CacheRead},
		{"cache_creation_input_tokens", fields.CacheCreated, &u.CacheCreated},
	}
	if u.CacheCreation, err = readCacheCreation(fields.CacheCreation); err != nil {
		return usage{}, "", err
	}
	if u.ThinkingTokens, err = readThinking(fields.Details); err != nil {
		return usage{}, "", err
	}
	for _, count := range counts {
		if *count.value, err = wholeCount(count.raw); err != nil {
			return usage{}, "", fmt.Errorf("usage %s: %w", count.name, err)
		}
	}
	for _, text := range []struct {
		name  string
		raw   json.RawMessage
		value *string
	}{
		{"service_tier", fields.ServiceTier, &u.ServiceTier},
		{"inference_geo", fields.InferenceGeo, &u.InferenceGeo},
		{"speed", fields.Speed, &u.Speed},
	} {
		if *text.value, err = optionalText("usage "+text.name, text.raw); err != nil {
			return usage{}, "", err
		}
	}
	return u, model, nil
}

// readCacheCreation reads the written tokens' stated lifetimes. Absent or
// null states none.
func readCacheCreation(raw json.RawMessage) (*cacheCreation, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var fields struct {
		FiveMinute json.RawMessage `json:"ephemeral_5m_input_tokens"`
		OneHour    json.RawMessage `json:"ephemeral_1h_input_tokens"`
	}
	if !isObject(raw) || json.Unmarshal(raw, &fields) != nil {
		return nil, fmt.Errorf("usage cache_creation %s is not an object", shown(raw))
	}
	var lifetimes cacheCreation
	var err error
	if lifetimes.FiveMinute, err = wholeCount(fields.FiveMinute); err != nil {
		return nil, fmt.Errorf("usage cache_creation ephemeral_5m_input_tokens: %w", err)
	}
	if lifetimes.OneHour, err = wholeCount(fields.OneHour); err != nil {
		return nil, fmt.Errorf("usage cache_creation ephemeral_1h_input_tokens: %w", err)
	}
	return &lifetimes, nil
}

// readThinking reads the thinking tokens the output states. Absent or null
// states none.
func readThinking(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}
	var fields struct {
		Thinking json.RawMessage `json:"thinking_tokens"`
	}
	if !isObject(raw) || json.Unmarshal(raw, &fields) != nil {
		return 0, fmt.Errorf("usage output_tokens_details %s is not an object", shown(raw))
	}
	thinking, err := wholeCount(fields.Thinking)
	if err != nil {
		return 0, fmt.Errorf("usage output_tokens_details thinking_tokens: %w", err)
	}
	return thinking, nil
}

// isObject says whether a raw value is a JSON object.
func isObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

// optionalText reads a text field. Absent and null are empty; any other value
// that is not text fails, because the field selects a price.
func optionalText(name string, raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	value, ok := jsonString(raw)
	if !ok {
		return "", fmt.Errorf("%s %s is not text", name, shown(raw))
	}
	return value, nil
}

func (b *requestBuilder) tokens(u usage) (adapter.Tokens, focus.TokenClass, error) {
	tokens := adapter.Tokens{
		InputUncached:       u.InputTokens,
		CacheRead:           u.CacheRead,
		Output:              u.OutputTokens,
		ReasoningOutput:     u.ThinkingTokens,
		CachedInputReported: u.CacheRead,
	}
	var reported cacheCreation
	if u.CacheCreation != nil {
		reported = *u.CacheCreation
	}
	var assumed focus.TokenClass
	var err error
	tokens.CacheWrite5m, tokens.CacheWrite1h, assumed, err = splitWrites(u.CacheCreated, reported.FiveMinute, reported.OneHour, b.unsplit)
	return tokens, assumed, err
}

// requests lists the requests in the order they started. A request no line
// timed takes the given window.
func (b *requestBuilder) requests(from, to time.Time) []adapter.Request {
	requests := make([]adapter.Request, 0, len(b.order))
	for _, id := range b.order {
		request := *b.byID[id]
		if start, ok := b.startedAt[b.refs[id]]; ok {
			request.Start = start
		}
		if request.Start.IsZero() {
			request.Start = from
		}
		if request.End.IsZero() {
			request.End = to
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
