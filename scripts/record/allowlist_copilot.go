package main

import (
	"regexp"
	"slices"
)

// copilotStreamPaths are the keys of copilot -p --output-format json a
// recording keeps: each event's type, time and links, the model call
// lifecycle and the result line's session and counts. Every other event's
// data holds the prompt, the agent's text, tool output or paths, and is
// dropped. The result's codeChanges.filesModified lists paths and is dropped.
var copilotStreamPaths = []string{
	"type",
	"timestamp",
	"ephemeral",
	"data",
	"data.turnId",
	"data.model",
	"data.outcome",
	"data.dispatchDurationMs",
	"data.isByok",
	"exitCode",
	"usage",
	"usage.premiumRequests",
	"usage.totalApiDurationMs",
	"usage.sessionDurationMs",
	"usage.codeChanges",
	"usage.codeChanges.linesAdded",
	"usage.codeChanges.linesRemoved",
}

// copilotEventPaths are the keys of a session's events.jsonl a recording
// keeps: session.start's versions and model, each session.usage_record's
// counts, billing fields and provider, and session.shutdown's per-model
// totals and Copilot's own credits. session.start's context names the
// working directory, the repository, the branch and the commit, and is
// dropped. session.shutdown's codeChanges.filesModified lists paths and is
// dropped, with its accountingSnapshot, agentMetrics and
// providerModelMetrics, which repeat the totals kept.
var copilotEventPaths = []string{
	"data.version",
	"data.producer",
	"data.copilotVersion",
	"data.startTime",
	"data.selectedModel",
	"data.usage",
	"data.usage.model",
	"data.usage.modelDisplayName",
	"data.usage.inputTokens",
	"data.usage.outputTokens",
	"data.usage.cacheReadTokens",
	"data.usage.cacheWriteTokens",
	"data.usage.reasoningTokens",
	"data.usage.cost",
	"data.usage.duration",
	"data.usage.initiator",
	"data.usage.interactionType",
	"data.usage.isByok",
	"data.usage.byokKind",
	"data.usage.modelProvider",
	"data.usage.providerType",
	"data.usage.isAuto",
	"data.usage.accounting",
	"data.usage.accounting.sequence",
	"data.usage.aiCreditsStatus",
	"data.usage.cachePolicyTtlSeconds",
	"data.usage.cacheDetailsReported",
	"data.usage.finishReason",
	"data.usage.apiEndpoint",
	"data.usage.transport",
	"data.shutdownType",
	"data.totalPremiumRequests",
	"data.totalNanoAiu",
	"data.totalApiDurationMs",
	"data.sessionStartTime",
	"data.currentModel",
	"data.codeChanges",
	"data.codeChanges.linesAdded",
	"data.codeChanges.linesRemoved",
	"data.modelMetrics",
	"data.modelMetrics.*",
	"data.modelMetrics.*.requests",
	"data.modelMetrics.*.requests.count",
	"data.modelMetrics.*.requests.cost",
	"data.modelMetrics.*.usage",
	"data.modelMetrics.*.usage.inputTokens",
	"data.modelMetrics.*.usage.outputTokens",
	"data.modelMetrics.*.usage.cacheReadTokens",
	"data.modelMetrics.*.usage.cacheWriteTokens",
	"data.modelMetrics.*.usage.reasoningTokens",
	"data.modelMetrics.*.totalNanoAiu",
	"data.modelMetrics.*.aiCreditsStatus",
}

// copilotEventIDs are the event ids that link Copilot's events: every event's
// own and the one before it, on the stream and in the session file alike.
var copilotEventIDs = idKind{"event", regexp.MustCompile(`^00000000-0000-4000-a000-([0-9]{12})$`), "00000000-0000-4000-a000-%012d", false}

// copilotIDs are the keys that hold an identifier. A usage record's
// apiCallId and accounting.usageId hold the provider's message id, and its
// providerCallId the provider's request id.
var copilotIDs = map[string]*idKind{
	"id":                                    &copilotEventIDs,
	"parentId":                              &copilotEventIDs,
	"sessionId":                             &sessionIDs,
	"data.sessionId":                        &sessionIDs,
	"data.usage.accounting.sourceSessionId": &sessionIDs,
	"data.usage.apiCallId":                  &messageIDs,
	"data.usage.accounting.usageId":         &messageIDs,
	"data.usage.providerCallId":             &requestIDs,
}

// copilotModelParent is the object session.shutdown keys by model name.
const copilotModelParent = "modelMetrics"

func init() {
	modelKeyParents = append(modelKeyParents, copilotModelParent)
}

var copilotAllowlist = newAllowlist(slices.Concat(copilotStreamPaths, copilotEventPaths), nil, copilotIDs, nil)
