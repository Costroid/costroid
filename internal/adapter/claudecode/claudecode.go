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
	// stated is Claude Code's own figure for these tokens at list price, its
	// costUSD, nil when it states none.
	stated *decimal.Decimal
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
	// sawRateLimit is true when a rate_limit_event message came.
	sawRateLimit bool
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
	report := adapter.Report{SessionID: s.sessionID, AgentVersion: s.version, CostBasis: s.costBasis(), AgentError: s.agentError(), Subscription: s.onSubscription()}
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
	outside, agree := outsideConversation(requests, s.totals, side)
	switch {
	case len(requests) > 0 && !s.sawResult:
		report.Warnings = append(report.Warnings, "no result object: the session files are the only record of this run")
	case len(requests) > 0 && (!agree || (len(outside) > 0 && !run.complete())):
		report.Warnings = append(report.Warnings, "the session files disagree with the result object; the result's per-model totals were used")
		requests = nil
	case len(requests) > 0:
		more, err := s.sideRequests(side, outside, SourceStream)
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
	perModel, err := s.perModel()
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

// noKeySource is the apiKeySource of a run that names no API key. Alone it
// does not prove a subscription login.
const noKeySource = "none"

// rateLimitEvent is the type of a message Claude Code sent on the recorded
// subscription login, action-subscription. No recorded run on an API key
// holds one.
const rateLimitEvent = "rate_limit_event"

// onSubscription says the run proves it was signed in on a Claude
// subscription: it names no API key, a rate_limit_event came, and no gateway
// bills it. Anything less leaves the run unmarked.
func (s *Session) onSubscription() bool {
	return s.apiKeySource == noKeySource && s.sawRateLimit && !s.billedThroughGateway
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

// The notes on entries the result counts beside the session files.
const (
	sideNote    = "a request outside the conversation: the result counts it, and no session file or stream message holds it"
	outsideNote = "requests outside the conversation: Claude Code's totals for this model count them, and no session file line holds them"
)

// sideRequests are the entries the totals count beside the session files'
// requests: one per side model, and one per conversation model for what its
// totals count beyond the files. No stream message or session file line
// holds them, so the standard tier and global price them, and their note
// says so in place of the note on a tier no message stated. source names
// where the totals were read.
func (s *Session) sideRequests(side map[string]bool, outside map[string]modelUsage, source string) ([]adapter.Request, error) {
	var requests []adapter.Request
	for _, model := range slices.Sorted(maps.Keys(side)) {
		request, err := s.modelEntry(model, s.totals[model], catalog.TierStandard, catalog.RegionGlobal, sideNote)
		if err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	for _, model := range slices.Sorted(maps.Keys(outside)) {
		request, err := s.modelEntry(model, outside[model], catalog.TierStandard, catalog.RegionGlobal, outsideNote)
		if err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	for i := range requests {
		requests[i].CostSource = source
	}
	return requests, nil
}

// perModel is one entry per model in the result's totals, at the tier and
// region the model's stream messages stated.
func (s *Session) perModel() ([]adapter.Request, error) {
	requests := make([]adapter.Request, 0, len(s.totals))
	for _, model := range slices.Sorted(maps.Keys(s.totals)) {
		tier, where, note := s.routes[model].settle()
		request, err := s.modelEntry(model, s.totals[model], tier, where, note)
		if err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	return requests, nil
}

// modelEntry is one entry for one model's tokens over the run. It states the
// whole input of its requests and Claude Code's own figure for them, from
// which the rows read its context tier.
func (s *Session) modelEntry(model string, usage modelUsage, tier, where, note string) (adapter.Request, error) {
	tokens, assumed, err := usage.tokens(s.unsplitWrite())
	if err != nil {
		return adapter.Request{}, fmt.Errorf("claude-code: the result's totals for %s do not add up: %w", model, err)
	}
	request := adapter.Request{
		ID:                 s.sessionID + ":" + model,
		Provider:           provider,
		Model:              model,
		Start:              s.first,
		End:                s.last,
		ServiceTier:        tier,
		Region:             where,
		ContextInputTokens: usage.context,
		Grain:              adapter.PerRun,
		Tokens:             tokens,
		AssumedWrite:       assumed,
		Note:               note,
		TierEvidence:       &adapter.TierEvidence{Agent: "Claude Code", USD: usage.stated},
	}
	return request, nil
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

// outsideConversation compares every billed class per model, except the side
// models, which no file holds. The files agree with the totals when every
// class is equal. They also agree when the totals count the same cache reads
// and writes and more uncached input: Claude Code sends requests outside the
// conversation on the conversation's own model, which read and write no
// cache, and no session file line holds them. The recorded Agent SDK runs
// show one, 1200 input tokens beside the files' 4. What the totals count
// beyond the files is returned per model. Every request sends input, so more
// output alone is a disagreement, as is a model the totals do not name. The
// informational classes do not decide it: thinking counts are re-tokenized
// and may differ by a few.
func outsideConversation(requests []adapter.Request, totals map[string]modelUsage, side map[string]bool) (map[string]modelUsage, bool) {
	summed := map[string]adapter.Tokens{}
	for _, request := range requests {
		summed[request.Model] = summed[request.Model].Add(request.Tokens)
	}
	if len(summed) != len(totals)-len(side) {
		return nil, false
	}
	outside := map[string]modelUsage{}
	for model, want := range totals {
		if side[model] {
			continue
		}
		got, ok := summed[model]
		if !ok || got.CacheRead != want.CacheRead || got.CacheWrite5m+got.CacheWrite1h != want.CacheCreated ||
			got.InputUncached > want.InputTokens || got.Output > want.OutputTokens {
			return nil, false
		}
		input, output := want.InputTokens-got.InputUncached, want.OutputTokens-got.Output
		if input == 0 && output > 0 {
			return nil, false
		}
		if input > 0 {
			outside[model] = modelUsage{
				InputTokens:  input,
				OutputTokens: output,
				Thinking:     max(want.Thinking-got.ReasoningOutput, 0),
				context:      input,
			}
		}
	}
	return outside, true
}

// isObject says whether a raw value is a JSON object.
func isObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{'
}
