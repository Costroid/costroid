package otlp

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/focus"
)

// Dialect names how one agent spells its token counts.
type Dialect string

// The dialects of the November 2026 adapter matrix.
const (
	Standard   Dialect = "genai"
	ClaudeCode Dialect = "claude-code"
	Codex      Dialect = "codex"
	Copilot    Dialect = "copilot"
	Cursor     Dialect = "cursor"
	OpenCode   Dialect = "opencode"
	Gemini     Dialect = "gemini"
)

// Attribute keys the dialects read.
const (
	keyInput      = "gen_ai.usage.input_tokens"
	keyOutput     = "gen_ai.usage.output_tokens"
	keyCacheRead  = "gen_ai.usage.cache_read.input_tokens"
	keyCacheWrite = "gen_ai.usage.cache_write.input_tokens"
	keyReasoning  = "gen_ai.usage.reasoning.output_tokens"

	// Older names of the same attributes.
	keyCacheCreationOld    = "gen_ai.usage.cache_creation.input_tokens"
	keyPromptTokensOld     = "gen_ai.usage.prompt_tokens"
	keyCompletionTokensOld = "gen_ai.usage.completion_tokens"

	keyCodexReasoning = "codex.usage.reasoning_output_tokens"

	keyCursorInput         = "cursor.api.request.input_tokens"
	keyCursorOutput        = "cursor.api.request.output_tokens"
	keyCursorCacheRead     = "cursor.api.request.cache_read_tokens"
	keyCursorCacheCreation = "cursor.api.request.cache_creation_tokens"

	keyGeminiInput    = "input_token_count"
	keyGeminiOutput   = "output_token_count"
	keyGeminiCached   = "cached_content_token_count"
	keyGeminiThoughts = "thoughts_token_count"
	keyGeminiTool     = "tool_token_count"
	keyGeminiTotal    = "total_token_count"
)

// The log events that carry a request's usage.
const (
	eventClaudeRequest  = "claude_code.api_request"
	eventClaudeShort    = "api_request"
	eventCursorRequest  = "cursor.api.request"
	eventGeminiResponse = "gemini_cli.api_response"
)

// serviceGemini is Gemini CLI's service.name and gen_ai.agent.name.
const serviceGemini = "gemini-cli"

// eventNames are the log bodies that are an event's name, not free text.
var eventNames = set(eventClaudeRequest, eventClaudeShort, eventCursorRequest)

// NoteTotalDisagrees marks a request whose stated total is not the sum of its
// counts. The server logs a warning for each such request.
const NoteTotalDisagrees = "the agent's total token count is not the sum of its counts, so this figure may read low or high"

// MaxTokens bounds one class of one request, far above any model's context,
// so a sum of counts cannot overflow.
const MaxTokens = 1_000_000_000_000

const (
	providerAnthropic = catalog.ProviderAnthropic
	providerOpenAI    = catalog.ProviderOpenAI
	providerGoogle    = catalog.ProviderGoogle
	tierFast          = "fast"
	tierStandard      = "standard"
)

type status int

const (
	statusIgnored status = iota
	statusUsage
	statusRejected
)

// counts holds one request's raw figures. Cache writes are as the dialect
// reports them, split by lifetime when it does.
type counts struct {
	input, output, cacheRead, reasoning int64
	cacheWrite, write5m, write1h        int64
	// inclusive means input already holds cache reads and writes.
	inclusive bool
	// subscriptionTTL means an unsplit write on a subscription lasts an hour.
	subscriptionTTL bool
	notes           []string
}

// interpret maps one record to usage, or reports it carries none.
func interpret(r *record) (Usage, status) {
	dialect := dialectOf(r)
	raw, found, ok := readCounts(dialect, r)
	if !found {
		return Usage{}, statusIgnored
	}
	if !ok {
		return Usage{}, statusRejected
	}
	usage := usageShell(dialect, r)
	if usage.Provider == "" {
		return Usage{}, statusRejected
	}
	if !usage.applyCounts(raw) {
		return Usage{}, statusRejected
	}
	return usage, statusUsage
}

func dialectOf(r *record) Dialect {
	service := strings.ToLower(r.attrs.text("service.name"))
	switch {
	case r.name == eventClaudeRequest, r.attrs.text("event.name") == eventClaudeShort, service == "claude-code":
		return ClaudeCode
	case r.name == eventCursorRequest, strings.HasPrefix(service, "cursor"), hasKey(r, keyCursorInput):
		return Cursor
	case hasKey(r, keyCodexReasoning), strings.HasPrefix(service, "codex"):
		return Codex
	case strings.Contains(service, "copilot"):
		return Copilot
	case strings.HasPrefix(service, "opencode"):
		return OpenCode
	case r.attrs.text("event.name") == eventGeminiResponse, service == serviceGemini, r.attrs.text("gen_ai.agent.name") == serviceGemini:
		return Gemini
	}
	return Standard
}

func hasKey(r *record, key string) bool {
	_, ok := r.attrs[key]
	return ok
}

// tokenKeys lists the attributes that mark a record as carrying usage.
var tokenKeys = []string{keyInput, keyOutput, keyPromptTokensOld, keyCompletionTokensOld}

func readCounts(dialect Dialect, r *record) (counts, bool, bool) {
	switch dialect {
	case ClaudeCode:
		if !claudeRequestEvent(r) {
			// A request is both an api_request event and a
			// claude_code.llm_request span. Only the event splits its cache
			// writes by lifetime, so it alone counts.
			return counts{}, false, true
		}
		return readClaude(r)
	case Cursor:
		return readCursor(r)
	case Codex:
		if r.log {
			// A Codex response is both a span and a codex.sse_event log. The
			// matrix names the span, so the log is left out to count once.
			return counts{}, false, true
		}
	case Gemini:
		return readGemini(r)
	}
	return readStandard(r)
}

func claudeRequestEvent(r *record) bool {
	return r.log && (r.name == eventClaudeRequest || r.name == eventClaudeShort || r.attrs.text("event.name") == eventClaudeShort)
}

type reader struct {
	r  *record
	ok bool
}

// int reads the first present key as a whole, non-negative count.
func (d *reader) int(keys ...string) int64 {
	text := d.r.attrs.text(keys...)
	if text == "" {
		return 0
	}
	value, err := parseCount(text)
	if err != nil {
		d.ok = false
		return 0
	}
	return value
}

// parseCount accepts digits, and the integral double a JavaScript exporter can
// write, such as 40.0. A count stays exact text: no float is involved.
func parseCount(text string) (int64, error) {
	whole, fraction, _ := strings.Cut(text, ".")
	if strings.Trim(fraction, "0") != "" {
		return 0, strconv.ErrSyntax
	}
	value, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, err
	}
	if value < 0 || value > MaxTokens {
		return 0, strconv.ErrRange
	}
	return value, nil
}

func readClaude(r *record) (counts, bool, bool) {
	if !hasKey(r, "input_tokens") && !hasKey(r, "output_tokens") {
		return counts{}, false, true
	}
	d := reader{r: r, ok: true}
	return counts{
		input:           d.int("input_tokens"),
		output:          d.int("output_tokens"),
		cacheRead:       d.int("cache_read_tokens"),
		cacheWrite:      d.int("cache_creation_tokens"),
		write5m:         d.int("cache_creation_5m_tokens"),
		write1h:         d.int("cache_creation_1h_tokens"),
		subscriptionTTL: true,
	}, true, d.ok
}

func readCursor(r *record) (counts, bool, bool) {
	if !hasKey(r, keyCursorInput) && !hasKey(r, keyCursorOutput) {
		return counts{}, false, true
	}
	d := reader{r: r, ok: true}
	return counts{
		input:      d.int(keyCursorInput),
		output:     d.int(keyCursorOutput),
		cacheRead:  d.int(keyCursorCacheRead),
		cacheWrite: d.int(keyCursorCacheCreation),
	}, true, d.ok
}

// readGemini reads Gemini CLI's api_response event. Its prompt count holds the
// cached tokens and leaves out the tool results the model reads, and its
// output leaves out the thoughts, which Google bills as output. The total is
// the sum of prompt, output, tool results and thoughts.
func readGemini(r *record) (counts, bool, bool) {
	if !hasKey(r, keyGeminiInput) && !hasKey(r, keyGeminiOutput) {
		// A response is also a gen_ai.client.inference.operation.details log
		// and an llm_call span, which state gen_ai.usage.* without cached
		// tokens or thoughts. The api_response event alone counts.
		return counts{}, false, true
	}
	d := reader{r: r, ok: true}
	prompt, candidates := d.int(keyGeminiInput), d.int(keyGeminiOutput)
	thoughts, tool := d.int(keyGeminiThoughts), d.int(keyGeminiTool)
	c := counts{
		input:     prompt + tool,
		output:    candidates + thoughts,
		cacheRead: d.int(keyGeminiCached),
		reasoning: thoughts,
		inclusive: true,
	}
	if hasKey(r, keyGeminiTotal) && d.int(keyGeminiTotal) != prompt+candidates+thoughts+tool {
		c.notes = append(c.notes, NoteTotalDisagrees)
	}
	return c, true, d.ok
}

func readStandard(r *record) (counts, bool, bool) {
	present := false
	for _, key := range tokenKeys {
		present = present || hasKey(r, key)
	}
	if !present {
		return counts{}, false, true
	}
	d := reader{r: r, ok: true}
	return counts{
		input:      d.int(keyInput, keyPromptTokensOld),
		output:     d.int(keyOutput, keyCompletionTokensOld),
		cacheRead:  d.int(keyCacheRead),
		cacheWrite: d.int(keyCacheWrite, keyCacheCreationOld),
		reasoning:  d.int(keyReasoning, keyCodexReasoning),
		inclusive:  true,
	}, true, d.ok
}

func usageShell(dialect Dialect, r *record) Usage {
	model := r.attrs.text("gen_ai.response.model", "gen_ai.request.model", "model")
	if model == "" {
		model = "unknown"
	}
	usage := Usage{
		Dialect:          dialect,
		Provider:         providerOf(dialect, r, model),
		Model:            model,
		ServiceTier:      tierStandard,
		Session:          r.attrs.text("gen_ai.conversation.id", "session.id", "cursor.conversation.id"),
		Agent:            agentOf(dialect, r),
		AgentVersion:     agentVersion(dialect, r),
		CostBasis:        costBasis(r),
		EnforcementGrade: r.attrs.text("costroid.enforcement_grade"),
		Repo:             r.attrs.text("vcs.repository.url.full"),
		PullRequest:      r.attrs.text("vcs.change.id"),
		CommitSHA:        r.attrs.text("vcs.ref.head.revision"),
		RunID:            r.attrs.text("cicd.pipeline.run.id"),
		Email:            r.attrs.text(keyEmail),
		Start:            instant(r.start),
		End:              instant(r.end),
	}
	if r.attrs.text("speed") == tierFast {
		usage.ServiceTier = tierFast
	}
	usage.RequestID = r.attrs.text("request_id", "gen_ai.response.id")
	usage.ID = cmp.Or(usage.RequestID, standInID(r))
	return usage
}

func instant(nanos uint64) time.Time {
	if nanos == 0 || nanos > 1<<63-1 {
		return time.Time{}
	}
	return time.Unix(0, int64(nanos)).UTC()
}

// The cost basis Costroid states on telemetry. costroid emit-otlp writes
// keyCostBasis on each span; keyCostBasisOld is the resource attribute a
// wrapper sets through OTEL_RESOURCE_ATTRIBUTES.
const (
	keyCostBasis    = "costroid.cost.basis"
	keyCostBasisOld = "costroid.cost_basis"
)

// costBasis defaults to the weaker claim. Telemetry does not say how a run is
// billed, so only Costroid's own attribute can say measured.
func costBasis(r *record) focus.CostBasis {
	if r.attrs.text(keyCostBasis, keyCostBasisOld) == string(focus.Measured) {
		return focus.Measured
	}
	return focus.ListEquivalent
}

// agentVersion leaves Gemini CLI's service.version out, because it holds the
// version of Node.js that runs the agent.
func agentVersion(dialect Dialect, r *record) string {
	if dialect == Gemini {
		return r.attrs.text("gen_ai.agent.version")
	}
	return r.attrs.text("gen_ai.agent.version", "service.version", "app.version")
}

var nameRepair = regexp.MustCompile(`[^a-z0-9]+`)

func agentOf(dialect Dialect, r *record) string {
	if dialect != Standard {
		return string(dialect)
	}
	name := strings.ToLower(r.attrs.text("gen_ai.agent.name", "service.name"))
	name = strings.Trim(nameRepair.ReplaceAllString(name, "-"), "-")
	if name == "" {
		return "unknown"
	}
	return name
}

func providerOf(dialect Dialect, r *record, model string) string {
	switch dialect {
	case ClaudeCode:
		return providerAnthropic
	case Codex:
		return providerOpenAI
	case Gemini:
		return providerGoogle
	}
	switch strings.ToLower(r.attrs.text("gen_ai.provider.name", "gen_ai.system")) {
	case providerAnthropic:
		return providerAnthropic
	case providerOpenAI:
		return providerOpenAI
	}
	return catalog.OwnerOf(model)
}

// standInID names a record that carries no provider request id. The
// provider's id is preferred, because it lets the same request's rows from
// the result stream and from OTLP collapse by row id.
func standInID(r *record) string {
	if r.spanID != "" {
		return "span:" + r.traceID + ":" + r.spanID
	}
	sum := sha256.New()
	for _, part := range []string{
		strconv.FormatUint(r.start, 10), r.name,
		r.attrs.text("gen_ai.conversation.id", "session.id", "cursor.conversation.id"),
		r.attrs.text("model", "gen_ai.response.model", "gen_ai.request.model"),
		r.attrs.text("input_tokens", keyCursorInput, keyInput, keyGeminiInput),
		r.attrs.text("output_tokens", keyCursorOutput, keyOutput, keyGeminiOutput),
	} {
		sum.Write([]byte(part))
		sum.Write([]byte{0})
	}
	return "record:" + hex.EncodeToString(sum.Sum(nil))
}

// applyCounts turns raw figures into disjoint classes. It reports false when
// the figures cannot be one request, such as cache counts above an inclusive
// input.
func (u *Usage) applyCounts(c counts) bool {
	written := c.cacheWrite
	if c.write5m+c.write1h > written {
		written = c.write5m + c.write1h
	}
	uncached := c.input
	if c.inclusive {
		uncached = c.input - c.cacheRead - written
		if uncached < 0 {
			return false
		}
	}
	if c.reasoning > c.output {
		return false
	}
	u.ContextInputTokens = uncached + c.cacheRead + written
	u.add(focus.InputUncached, uncached)
	u.add(focus.CacheRead, c.cacheRead)
	u.addWrites(c, written)
	u.add(focus.Output, c.output)
	u.add(focus.ReasoningOutput, c.reasoning)
	u.add(focus.CachedInputReported, c.cacheRead)
	u.Notes = append(u.Notes, c.notes...)
	if u.Dialect == OpenCode {
		u.Notes = append(u.Notes, "OpenCode telemetry reports no cache writes, so any are counted as uncached input")
	}
	return true
}

func (u *Usage) add(class focus.TokenClass, tokens int64) {
	if tokens > 0 {
		u.Counts = append(u.Counts, Count{Class: class, Tokens: tokens})
	}
}

// addWrites assigns cache writes to a lifetime. The dialect's own split wins.
// What it leaves unsplit takes the provider's rule, and the row says so.
func (u *Usage) addWrites(c counts, written int64) {
	u.add(focus.CacheWrite5m, c.write5m)
	u.add(focus.CacheWrite1h, c.write1h)
	rest := written - c.write5m - c.write1h
	if rest <= 0 {
		return
	}
	u.Notes = append(u.Notes, "cache lifetime assumed")
	class := focus.CacheWrite5m
	switch {
	case u.Provider == providerOpenAI:
		class = focus.CacheWrite30m
	case c.subscriptionTTL && u.CostBasis == focus.ListEquivalent:
		class = focus.CacheWrite1h
	}
	u.AssumedWrite = class
	u.mergeCount(class, rest)
}

func (u *Usage) mergeCount(class focus.TokenClass, tokens int64) {
	for i := range u.Counts {
		if u.Counts[i].Class == class {
			u.Counts[i].Tokens += tokens
			return
		}
	}
	u.add(class, tokens)
}
