package otlp

import "strings"

// Counter labels for fields the edge drops. Labels are a closed set, so a
// sender cannot grow the counter table by naming new attributes.
const (
	// DropClientCost counts a sender's own cost figure.
	DropClientCost = "client_cost"
	// DropContent counts a field whose name reads as content but is not listed.
	DropContent = "content_other"
	// DropUnlisted counts anything else outside the allowlist.
	DropUnlisted = "unlisted"
	// DropOversized counts an allowed attribute over maxValueBytes.
	DropOversized = "oversized_value"
	// DropNonScalar counts an allowed attribute that is an array or a map.
	DropNonScalar = "non_scalar_value"
	// DropLogBody counts a log body, which is free text.
	DropLogBody = "log.body"
	// DropStatusMessage counts a span status message, which is free text.
	DropStatusMessage = "span.status.message"
	// DropEmail counts a user.email, replaced by a login or a pseudonym.
	DropEmail = "user.email"
	// DropMetrics counts a metrics export. Rows come from logs and spans.
	DropMetrics = "metrics_export"
)

// keyEmail is replaced at the edge. The address leaves this package only as
// Usage.Email, for the server to turn into a login or a pseudonym.
const keyEmail = "user.email"

const maxValueBytes = 1024

// allowed lists every attribute the edge reads. Anything else is dropped and
// counted, whatever the sender set, so a new content field cannot get through
// by having a name nobody listed.
var allowed = set(
	"service.name", "service.version", "app.version",
	"vcs.repository.url.full", "vcs.change.id", "vcs.ref.head.revision", "cicd.pipeline.run.id",
	keyCostBasis, keyCostBasisOld, "costroid.enforcement_grade",
	"gen_ai.provider.name", "gen_ai.system", "gen_ai.operation.name",
	"gen_ai.request.model", "gen_ai.response.model", "gen_ai.response.id",
	"gen_ai.conversation.id", "session.id", "gen_ai.agent.name", "gen_ai.agent.version",
	keyInput, keyOutput, keyCacheRead, keyCacheWrite, keyReasoning,
	keyCacheCreationOld, keyPromptTokensOld, keyCompletionTokensOld,
	"event.name", "model", "request_id", "speed",
	"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens",
	"cache_creation_5m_tokens", "cache_creation_1h_tokens",
	keyCodexReasoning,
	keyCursorInput, keyCursorOutput, keyCursorCacheRead, keyCursorCacheCreation, "cursor.conversation.id",
)

// namedContent lists the content fields the specification and the agents name.
// Each gets its own counter. The allowlist, not this list, is what drops them.
var namedContent = set(
	"gen_ai.input.messages", "gen_ai.output.messages", "gen_ai.system_instructions",
	"gen_ai.tool.definitions", "gen_ai.tool.call.arguments", "gen_ai.tool.call.result",
	"gen_ai.prompt", "gen_ai.completion", "gen_ai.retrieval.query.text", "gen_ai.retrieval.documents",
	"prompt", "user_prompt", "tool_parameters", "tool_input", "tool_output", "tool_result",
	"body", "request_body", "response_body", "http.request.body.content", "http.response.body.content",
)

var clientCost = set(
	"cost_usd", "gen_ai.usage.cost", "gen_ai.cost", "cursor.cost.usage", "github.copilot.nano_aiu",
	"github.copilot.cost", "claude_code.cost.usage",
)

// Old-style indexed attributes, such as gen_ai.prompt.0.content.
var contentPrefixes = []string{"gen_ai.prompt.", "gen_ai.completion.", "llm.input_messages", "llm.output_messages", "llm.prompts"}

var contentWords = []string{"prompt", "message", "instruction", "content", "body", "argument", "completion", "tool"}

// DropLabel names the counter for a field the edge will not keep.
func DropLabel(key string) string {
	if key == keyEmail {
		return DropEmail
	}
	if namedContent[key] {
		return key
	}
	if clientCost[key] {
		return DropClientCost
	}
	lower := strings.ToLower(key)
	for _, prefix := range contentPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return DropContent
		}
	}
	for _, word := range contentWords {
		if strings.Contains(lower, word) {
			return DropContent
		}
	}
	return DropUnlisted
}

func set(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, key := range keys {
		m[key] = true
	}
	return m
}

// value is one scalar attribute. An integer that arrived as a number keeps
// its digits, because the conventions and the agents disagree on the type.
type value struct {
	text string
}

// attributes holds what the allowlist let through.
type attributes map[string]value

func (a attributes) text(keys ...string) string {
	for _, key := range keys {
		if v, ok := a[key]; ok && v.text != "" {
			return v.text
		}
	}
	return ""
}

// collector applies the allowlist while a decoder walks a payload.
type collector struct {
	drops map[string]int64
	// rejected counts records that did not decode.
	rejected int
}

func (c *collector) drop(label string) {
	c.drops[label]++
}

// keep stores key in into when the allowlist admits it, and counts it when not.
func (c *collector) keep(into attributes, key string, text string, scalar bool) {
	if !allowed[key] && key != keyEmail {
		c.drop(DropLabel(key))
		return
	}
	if !scalar {
		c.drop(DropNonScalar)
		return
	}
	if len(text) > maxValueBytes {
		c.drop(DropOversized)
		return
	}
	into[key] = value{text: text}
}

// discard counts attributes on parts of a payload the edge never reads: span
// events, links, scopes.
func (c *collector) discard(key string) {
	c.drop(DropLabel(key))
}
