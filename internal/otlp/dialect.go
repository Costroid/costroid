package otlp

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"time"

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
)

// The log events that carry a request's usage.
const (
	eventClaudeRequest = "claude_code.api_request"
	eventCursorRequest = "cursor.api.request"
)

// eventNames are the log bodies that are an event's name, not free text.
var eventNames = set(eventClaudeRequest, "api_request", eventCursorRequest)

// maxTokens bounds one class of one request, far above any model's context,
// so a sum of counts cannot overflow.
const maxTokens = 1_000_000_000_000

const (
	providerAnthropic = "anthropic"
	providerOpenAI    = "openai"
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
	case r.name == eventClaudeRequest, r.attrs.text("event.name") == "api_request", service == "claude-code":
		return ClaudeCode
	case r.name == eventCursorRequest, strings.HasPrefix(service, "cursor"), hasKey(r, keyCursorInput):
		return Cursor
	case hasKey(r, keyCodexReasoning), strings.HasPrefix(service, "codex"):
		return Codex
	case strings.Contains(service, "copilot"):
		return Copilot
	case strings.HasPrefix(service, "opencode"):
		return OpenCode
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
		return readClaude(r)
	case Cursor:
		return readCursor(r)
	case Codex:
		if r.log {
			// A Codex response is both a span and a codex.sse_event log. The
			// matrix names the span, so the log is left out to count once.
			return counts{}, false, true
		}
	}
	return readStandard(r)
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

// parseCount accepts digits, and the integral doubles a JavaScript exporter
// writes.
func parseCount(text string) (int64, error) {
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		var whole float64
		whole, err = strconv.ParseFloat(text, 64)
		if err != nil {
			return 0, err
		}
		if whole != float64(int64(whole)) {
			return 0, strconv.ErrSyntax
		}
		value = int64(whole)
	}
	if value < 0 || value > maxTokens {
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
		AgentVersion:     r.attrs.text("gen_ai.agent.version", "service.version", "app.version"),
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
	usage.ID = requestID(r)
	return usage
}

func instant(nanos uint64) time.Time {
	if nanos == 0 || nanos > 1<<63-1 {
		return time.Time{}
	}
	return time.Unix(0, int64(nanos)).UTC()
}

// costBasis defaults to the weaker claim. Telemetry does not say how a run is
// billed, so only the wrapper's own resource attribute can say measured.
func costBasis(r *record) focus.CostBasis {
	if r.attrs.text("costroid.cost_basis") == string(focus.Measured) {
		return focus.Measured
	}
	return focus.ListEquivalent
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
	}
	switch strings.ToLower(r.attrs.text("gen_ai.provider.name", "gen_ai.system")) {
	case providerAnthropic:
		return providerAnthropic
	case providerOpenAI:
		return providerOpenAI
	}
	switch {
	case strings.HasPrefix(model, "claude"):
		return providerAnthropic
	case strings.HasPrefix(model, "gpt-"), strings.HasPrefix(model, "chatgpt"), strings.HasPrefix(model, "codex"):
		return providerOpenAI
	}
	return ""
}

// requestID prefers the id the provider's request carries, which lets the
// same request's rows from the result stream and from OTLP collapse by row id.
func requestID(r *record) string {
	if id := r.attrs.text("request_id", "gen_ai.response.id"); id != "" {
		return id
	}
	if r.spanID != "" {
		return "span:" + r.traceID + ":" + r.spanID
	}
	sum := sha256.New()
	for _, part := range []string{
		strconv.FormatUint(r.start, 10), r.name,
		r.attrs.text("gen_ai.conversation.id", "session.id", "cursor.conversation.id"),
		r.attrs.text("model", "gen_ai.response.model", "gen_ai.request.model"),
		r.attrs.text("input_tokens", keyCursorInput, keyInput),
		r.attrs.text("output_tokens", keyCursorOutput, keyOutput),
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
