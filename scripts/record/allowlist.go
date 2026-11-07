package main

import (
	"regexp"
	"slices"
	"strings"
)

// keptPaths lists every key a recording keeps, as a path from the line's root:
// keys joined by ".", "[]" for an array's elements and "*" for a model name
// under modelUsage. The list is the keys of the recordings committed on
// 2026-11-01, plus the keys an adapter reads that those runs did not write:
// cwd, Codex's thread_source and session_id, and Claude Code's error.
var keptPaths = []string{
	"apiBlockIndex",
	"apiKeySource",
	"cache_creation_1h_tokens",
	"cache_creation_5m_tokens",
	"cache_read_tokens",
	"claude_code_version",
	"duration_api_ms",
	"duration_ms",
	"errors",
	"errors[]",
	"event",
	"event.message",
	"event.message.content",
	"event.message.id",
	"event.message.model",
	"event.message.role",
	"event.message.stop_reason",
	"event.message.type",
	"event.message.usage",
	"event.message.usage.cache_creation",
	"event.message.usage.cache_creation.ephemeral_1h_input_tokens",
	"event.message.usage.cache_creation.ephemeral_5m_input_tokens",
	"event.message.usage.cache_creation_input_tokens",
	"event.message.usage.cache_read_input_tokens",
	"event.message.usage.inference_geo",
	"event.message.usage.input_tokens",
	"event.message.usage.output_tokens",
	"event.message.usage.service_tier",
	"event.message.usage.speed",
	"event.type",
	"event.usage",
	"event.usage.cache_creation_input_tokens",
	"event.usage.cache_read_input_tokens",
	"event.usage.input_tokens",
	"event.usage.iterations",
	"event.usage.iterations[]",
	"event.usage.iterations[].cache_creation",
	"event.usage.iterations[].cache_creation.ephemeral_1h_input_tokens",
	"event.usage.iterations[].cache_creation.ephemeral_5m_input_tokens",
	"event.usage.iterations[].cache_creation_input_tokens",
	"event.usage.iterations[].cache_read_input_tokens",
	"event.usage.iterations[].input_tokens",
	"event.usage.iterations[].output_tokens",
	"event.usage.iterations[].type",
	"event.usage.output_tokens",
	"event.usage.output_tokens_details",
	"event.usage.output_tokens_details.thinking_tokens",
	"id",
	"input_tokens",
	"isSidechain",
	"is_error",
	"item",
	"item.exit_code",
	"item.id",
	"item.status",
	"item.type",
	"last_token_usage",
	"last_token_usage.cache_write_input_tokens",
	"last_token_usage.cached_input_tokens",
	"last_token_usage.input_tokens",
	"last_token_usage.output_tokens",
	"last_token_usage.reasoning_output_tokens",
	"last_token_usage.total_tokens",
	"message",
	"message.content",
	"message.content[]",
	"message.content[].type",
	"message.id",
	"message.model",
	"message.role",
	"message.stop_reason",
	"message.type",
	"message.usage",
	"message.usage.cache_creation",
	"message.usage.cache_creation.ephemeral_1h_input_tokens",
	"message.usage.cache_creation.ephemeral_5m_input_tokens",
	"message.usage.cache_creation_input_tokens",
	"message.usage.cache_read_input_tokens",
	"message.usage.fallback_credit",
	"message.usage.inference_geo",
	"message.usage.input_tokens",
	"message.usage.iterations",
	"message.usage.iterations[]",
	"message.usage.iterations[].cache_creation",
	"message.usage.iterations[].cache_creation.ephemeral_1h_input_tokens",
	"message.usage.iterations[].cache_creation.ephemeral_5m_input_tokens",
	"message.usage.iterations[].cache_creation_input_tokens",
	"message.usage.iterations[].cache_read_input_tokens",
	"message.usage.iterations[].input_tokens",
	"message.usage.iterations[].output_tokens",
	"message.usage.iterations[].type",
	"message.usage.output_tokens",
	"message.usage.output_tokens_details",
	"message.usage.output_tokens_details.thinking_tokens",
	"message.usage.server_tool_use",
	"message.usage.server_tool_use.web_fetch_requests",
	"message.usage.server_tool_use.web_search_requests",
	"message.usage.service_tier",
	"message.usage.speed",
	"model",
	"modelUsage",
	"modelUsage.*",
	"modelUsage.*.cacheCreationEphemeral1hInputTokens",
	"modelUsage.*.cacheCreationEphemeral5mInputTokens",
	"modelUsage.*.cacheCreationInputTokens",
	"modelUsage.*.cacheReadInputTokens",
	"modelUsage.*.canonicalModel",
	"modelUsage.*.contextWindow",
	"modelUsage.*.costBasis",
	"modelUsage.*.costUSD",
	"modelUsage.*.inputTokens",
	"modelUsage.*.maxOutputTokens",
	"modelUsage.*.outputTokens",
	"modelUsage.*.provider",
	"modelUsage.*.thinkingTokens",
	"modelUsage.*.webSearchRequests",
	"model_usage_since_last_result",
	"model_usage_since_last_result.*",
	"model_usage_since_last_result.*.cacheCreationEphemeral1hInputTokens",
	"model_usage_since_last_result.*.cacheCreationEphemeral5mInputTokens",
	"model_usage_since_last_result.*.cacheCreationInputTokens",
	"model_usage_since_last_result.*.cacheReadInputTokens",
	"model_usage_since_last_result.*.canonicalModel",
	"model_usage_since_last_result.*.contextWindow",
	"model_usage_since_last_result.*.costBasis",
	"model_usage_since_last_result.*.costUSD",
	"model_usage_since_last_result.*.inputTokens",
	"model_usage_since_last_result.*.maxOutputTokens",
	"model_usage_since_last_result.*.outputTokens",
	"model_usage_since_last_result.*.provider",
	"model_usage_since_last_result.*.thinkingTokens",
	"model_usage_since_last_result.*.webSearchRequests",
	"num_turns",
	"output_tokens",
	"parent_tool_use_id",
	"payload",
	"payload.cli_version",
	"payload.id",
	"payload.info",
	"payload.info.last_token_usage",
	"payload.info.last_token_usage.cache_write_input_tokens",
	"payload.info.last_token_usage.cached_input_tokens",
	"payload.info.last_token_usage.input_tokens",
	"payload.info.last_token_usage.output_tokens",
	"payload.info.last_token_usage.reasoning_output_tokens",
	"payload.info.last_token_usage.total_tokens",
	"payload.info.total_token_usage",
	"payload.info.total_token_usage.cache_write_input_tokens",
	"payload.info.total_token_usage.cached_input_tokens",
	"payload.info.total_token_usage.input_tokens",
	"payload.info.total_token_usage.output_tokens",
	"payload.info.total_token_usage.reasoning_output_tokens",
	"payload.info.total_token_usage.total_tokens",
	"payload.model",
	"payload.response_id",
	"payload.thread_id",
	"payload.thread_token_usage",
	"payload.thread_token_usage.cache_write_input_tokens",
	"payload.thread_token_usage.cached_input_tokens",
	"payload.thread_token_usage.input_tokens",
	"payload.thread_token_usage.output_tokens",
	"payload.thread_token_usage.reasoning_output_tokens",
	"payload.thread_token_usage.total_tokens",
	"payload.turn_id",
	"payload.turn_token_usage",
	"payload.turn_token_usage.cache_write_input_tokens",
	"payload.turn_token_usage.cached_input_tokens",
	"payload.turn_token_usage.input_tokens",
	"payload.turn_token_usage.output_tokens",
	"payload.turn_token_usage.reasoning_output_tokens",
	"payload.turn_token_usage.total_tokens",
	"payload.type",
	"payload.usage",
	"payload.usage.cache_write_input_tokens",
	"payload.usage.cached_input_tokens",
	"payload.usage.input_tokens",
	"payload.usage.output_tokens",
	"payload.usage.reasoning_output_tokens",
	"payload.usage.total_tokens",
	"querySource",
	"requestId",
	"requestRef",
	"request_id",
	"sessionId",
	"session_id",
	"stop_reason",
	"subtype",
	"terminal_reason",
	"thread_id",
	"timestamp",
	"totalCostUSD",
	"total_cost_usd",
	"total_token_usage",
	"total_token_usage.cache_write_input_tokens",
	"total_token_usage.cached_input_tokens",
	"total_token_usage.input_tokens",
	"total_token_usage.output_tokens",
	"total_token_usage.reasoning_output_tokens",
	"total_token_usage.total_tokens",
	"type",
	"usage",
	"usage.cache_creation",
	"usage.cache_creation.ephemeral_1h_input_tokens",
	"usage.cache_creation.ephemeral_5m_input_tokens",
	"usage.cache_creation_input_tokens",
	"usage.cache_read_input_tokens",
	"usage.cache_write_input_tokens",
	"usage.cached_input_tokens",
	"usage.fallback_credit",
	"usage.inference_geo",
	"usage.input_tokens",
	"usage.iterations",
	"usage.iterations[]",
	"usage.iterations[].cache_creation",
	"usage.iterations[].cache_creation.ephemeral_1h_input_tokens",
	"usage.iterations[].cache_creation.ephemeral_5m_input_tokens",
	"usage.iterations[].cache_creation_input_tokens",
	"usage.iterations[].cache_read_input_tokens",
	"usage.iterations[].input_tokens",
	"usage.iterations[].output_tokens",
	"usage.iterations[].type",
	"usage.output_tokens",
	"usage.output_tokens_details",
	"usage.output_tokens_details.thinking_tokens",
	"usage.reasoning_output_tokens",
	"usage.server_tool_use",
	"usage.server_tool_use.web_fetch_requests",
	"usage.server_tool_use.web_search_requests",
	"usage.service_tier",
	"usage.speed",
	"uuid",
	"version",
	"error",
	"payload.session_id",
	"payload.thread_source",
}

// placeholders are kept keys whose value is replaced: a working directory by a
// fixed path, and an agent's own text by a fixed word.
var placeholders = map[string]string{
	"cwd":                    placeholderPath,
	"payload.cwd":            placeholderPath,
	"item.text":              placeholderContent,
	"item.command":           placeholderContent,
	"item.aggregated_output": placeholderContent,
}

const (
	placeholderPath    = "/work"
	placeholderContent = "@content"
	placeholderPrompt  = "@prompt"
	placeholderSecret  = "@secret"
)

// idKind is one family of identifiers and the synthetic form the fixtures use.
// A numeric kind's ids are JSON numbers, and the others' are strings.
type idKind struct {
	name    string
	fixture *regexp.Regexp
	format  string
	numeric bool
}

// text is v as an id of this kind, false when v is not one.
func (k *idKind) text(v *value) (string, bool) {
	switch {
	case k.numeric && v.isNumber():
		return v.raw, true
	case !k.numeric && v.kind == kindString:
		return v.text, true
	}
	return "", false
}

// value is a synthetic id as JSON of this kind.
func (k *idKind) value(synthetic string) *value {
	if k.numeric {
		return &value{kind: kindLiteral, raw: synthetic}
	}
	return stringValue(synthetic)
}

var (
	sessionIDs  = idKind{"session", regexp.MustCompile(`^00000000-0000-4000-8000-([0-9]{12})$`), "00000000-0000-4000-8000-%012d", false}
	requestRefs = idKind{"request reference", regexp.MustCompile(`^00000000-0000-4000-9000-([0-9]{12})$`), "00000000-0000-4000-9000-%012d", false}
	requestIDs  = idKind{"request", regexp.MustCompile(`^req_fixture_([0-9]{4,})$`), "req_fixture_%04d", false}
	messageIDs  = idKind{"message", regexp.MustCompile(`^(?:msg_fixture_|example-message-)([0-9]+)$`), "msg_fixture_%04d", false}
	toolUseIDs  = idKind{"tool use", regexp.MustCompile(`^toolu_fixture_([0-9]{4,})$`), "toolu_fixture_%04d", false}
	responseIDs = idKind{"response", regexp.MustCompile(`^response-([0-9]+)$`), "response-%d", false}
	turnIDs     = idKind{"turn", regexp.MustCompile(`^turn-([0-9]+)$`), "turn-%d", false}
	itemIDs     = idKind{"item", regexp.MustCompile(`^item_([0-9]+)$`), "item_%d", false}
	checkRunIDs = idKind{"check run", regexp.MustCompile(`^9000([0-9]{4})$`), "9000%04d", true}
	ciRunIDs    = idKind{"workflow run", regexp.MustCompile(`^8000([0-9]{4})$`), "8000%04d", true}
	commitIDs   = idKind{"commit", regexp.MustCompile(`^0{32}([0-9]{8})$`), "%040d", false}
)

// idPaths are the kept keys that hold an identifier. Each is replaced by its
// kind's synthetic form, the same raw value by the same synthetic one across
// every file of a run.
var idPaths = map[string]*idKind{
	"session_id":          &sessionIDs,
	"sessionId":           &sessionIDs,
	"thread_id":           &sessionIDs,
	"uuid":                &sessionIDs,
	"payload.id":          &sessionIDs,
	"payload.thread_id":   &sessionIDs,
	"payload.session_id":  &sessionIDs,
	"id":                  &requestRefs,
	"requestRef":          &requestRefs,
	"request_id":          &requestIDs,
	"requestId":           &requestIDs,
	"message.id":          &messageIDs,
	"event.message.id":    &messageIDs,
	"parent_tool_use_id":  &toolUseIDs,
	"payload.response_id": &responseIDs,
	"payload.turn_id":     &turnIDs,
	"item.id":             &itemIDs,

	// GitHub's list of a commit's check runs, and the result object in each.
	"check_runs[].id":          &checkRunIDs,
	"check_runs[].head_sha":    &commitIDs,
	"result.check_run_id":      &checkRunIDs,
	"result.head_sha":          &commitIDs,
	"result.ci_run.id":         &ciRunIDs,
	"result.policy.config_ref": &commitIDs,
}

// modelKeyParents hold one object per model, keyed by the model's name.
var modelKeyParents = []string{"modelUsage", "model_usage_since_last_result"}

var modelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,99}$`)

// allowlist is what one kind of recording keeps: the keys it keeps as they
// are, the keys whose value a placeholder replaces, the keys that hold an id,
// and the keys whose string is a fenced result object.
type allowlist struct {
	kept         map[string]bool
	containers   map[string]bool
	placeholders map[string]string
	ids          map[string]*idKind
	embedded     map[string]string
}

// sessionAllowlist holds Claude Code's and Codex's files and GitHub's list of
// a commit's check runs.
var sessionAllowlist = newAllowlist(slices.Concat(keptPaths, checkRunPaths), placeholders, idPaths, embeddedResults)

func newAllowlist(paths []string, replaced map[string]string, ids map[string]*idKind, embedded map[string]string) *allowlist {
	list := &allowlist{
		kept: map[string]bool{}, containers: map[string]bool{},
		placeholders: replaced, ids: ids, embedded: embedded,
	}
	for _, path := range paths {
		list.kept[path] = true
		for i := range path {
			if path[i] == '.' || strings.HasPrefix(path[i:], "[]") {
				list.containers[path[:i]] = true
			}
		}
	}
	for path := range replaced {
		list.kept[path] = true
	}
	for path := range ids {
		list.kept[path] = true
	}
	for path := range embedded {
		list.kept[path] = true
	}
	return list
}

// child is the path of key inside the object at parent.
func child(parent, key string) string {
	for _, models := range modelKeyParents {
		if parent == models || strings.HasSuffix(parent, "."+models) {
			key = "*"
		}
	}
	if parent == "" {
		return key
	}
	return parent + "." + key
}

// countUnderUsage says whether a number at path is a token or request count:
// a key under an object whose name holds "usage". Such a number is kept even
// when the list does not name it, so a new count reaches the fixture.
func countUnderUsage(path string) bool {
	segments := strings.Split(path, ".")
	for _, segment := range segments[:len(segments)-1] {
		if strings.Contains(strings.ToLower(segment), "usage") {
			return true
		}
	}
	return false
}
