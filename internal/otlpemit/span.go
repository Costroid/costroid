package otlpemit

import (
	"crypto/sha256"
	"strconv"
	"time"

	"github.com/Costroid/costroid/internal/focus"
)

// The attributes a span carries. The gen_ai.* names are the OpenTelemetry
// GenAI semantic conventions (github.com/open-telemetry/semantic-conventions-genai,
// model/gen-ai/registry.yaml at e6658f14, stability development, read
// 2026-11-08), and the vcs.* and cicd.* names those of semantic conventions
// v1.45.0 (release candidate). The cost has no convention, and travels under
// Costroid's own names as strings. The other names are the row contract's
// FOCUS columns.
const (
	keyOperation      = "gen_ai.operation.name"
	keyProvider       = "gen_ai.provider.name"
	keyModel          = "gen_ai.request.model"
	keyInputTokens    = "gen_ai.usage.input_tokens"
	keyOutputTokens   = "gen_ai.usage.output_tokens"
	keyCacheRead      = "gen_ai.usage.cache_read.input_tokens"
	keyCacheWrite     = "gen_ai.usage.cache_write.input_tokens"
	keyReasoning      = "gen_ai.usage.reasoning.output_tokens"
	keyConversation   = "gen_ai.conversation.id"
	keyAgentName      = "gen_ai.agent.name"
	keyAgentVersion   = "gen_ai.agent.version"
	keyRepository     = "vcs.repository.url.full"
	keyChange         = "vcs.change.id"
	keyHead           = "vcs.ref.head.revision"
	keyRun            = "cicd.pipeline.run.id"
	keyCostAmount     = "costroid.cost.amount"
	keyCostCurrency   = "costroid.cost.currency"
	keyCostBasis      = "costroid.cost.basis"
	keyEnforcement    = "costroid.enforcement_grade"
	keyServiceTier    = "x_CostroidServiceTier"
	keyCatalogVersion = "x_CostroidCatalogVersion"
	keyRowID          = "x_CostroidRowId"

	keyServiceName    = "service.name"
	keyServiceVersion = "service.version"
)

// operationChat is the GenAI operation of an agent's model request.
const operationChat = "chat"

// spanKindClient is the span kind the conventions give a GenAI inference.
const spanKindClient = 3

// conventionProviders maps a row's provider to the value of
// gen_ai.provider.name where the two differ. A Google row does not say which
// endpoint served it, and gcp.gen_ai is the value for that case.
var conventionProviders = map[string]string{"google": "gcp.gen_ai"}

// kind is the type of an attribute's value. No kind is a float.
type kind int

const (
	kindString kind = iota
	kindInt
	kindStrings
)

type attribute struct {
	key     string
	kind    kind
	text    string
	integer int64
	list    []string
}

func text(key, value string) attribute {
	return attribute{key: key, kind: kindString, text: value}
}

func integer(key string, value int64) attribute {
	return attribute{key: key, kind: kindInt, integer: value}
}

// span is one request as OTLP carries it.
type span struct {
	traceID    [16]byte
	spanID     [8]byte
	name       string
	start, end time.Time
	attributes []attribute
}

// spanOf states one request as a span. A tag the request lacks adds no
// attribute, and a token class it lacks adds none where the conventions ask
// for it only when non-zero.
func spanOf(q *request) span {
	var written int64
	for class, tokens := range q.tokens {
		if class.Lifetime() != "" {
			written += tokens
		}
	}
	read := q.tokens[focus.CacheRead]
	provider := q.provider
	if named, ok := conventionProviders[provider]; ok {
		provider = named
	}
	attributes := []attribute{
		text(keyOperation, operationChat),
		text(keyProvider, provider),
		text(keyModel, q.model),
		integer(keyInputTokens, q.tokens[focus.InputUncached]+read+written),
		integer(keyOutputTokens, q.tokens[focus.Output]),
	}
	attributes = withCount(attributes, keyCacheRead, read)
	attributes = withCount(attributes, keyCacheWrite, written)
	attributes = withCount(attributes, keyReasoning, q.tokens[focus.ReasoningOutput])
	attributes = withTag(attributes, keyConversation, q.tags[focus.TagSessionID])
	attributes = withTag(attributes, keyAgentName, q.tags[focus.TagAgent])
	attributes = withTag(attributes, keyAgentVersion, q.tags[focus.TagAgentVersion])
	if repository := q.tags[focus.TagRepo]; repository != "" {
		attributes = append(attributes, text(keyRepository, "https://"+repository))
	}
	attributes = withTag(attributes, keyChange, q.tags[focus.TagPR])
	attributes = withTag(attributes, keyHead, q.tags[focus.TagCommitSHA])
	attributes = withTag(attributes, keyRun, q.tags[focus.TagWorkflowRunID])
	attributes = append(attributes, text(keyCostAmount, q.cost.String()), text(keyCostCurrency, q.currency))
	attributes = withTag(attributes, keyCostBasis, q.tags[focus.TagCostBasis])
	attributes = withTag(attributes, keyEnforcement, q.tags[focus.TagEnforcementGrade])
	attributes = withTag(attributes, keyServiceTier, q.serviceTier)
	attributes = withTag(attributes, keyCatalogVersion, q.catalogVersion)
	attributes = append(attributes, attribute{key: keyRowID, kind: kindStrings, list: q.rowIDs})
	return span{
		traceID:    traceIDOf(q),
		spanID:     spanIDOf(q),
		name:       operationChat + " " + q.model,
		start:      q.start,
		end:        q.end,
		attributes: attributes,
	}
}

func withCount(attributes []attribute, key string, tokens int64) []attribute {
	if tokens == 0 {
		return attributes
	}
	return append(attributes, integer(key, tokens))
}

func withTag(attributes []attribute, key, value string) []attribute {
	if value == "" {
		return attributes
	}
	return append(attributes, text(key, value))
}

// traceIDOf puts every request of one CI run in one trace, named by the
// repository and the run. A request with no run is a trace of its own. The ids
// are hashes of what the rows state, so sending the same rows again sends the
// same ids.
func traceIDOf(q *request) [16]byte {
	name := "request\x00" + q.rowIDs[0]
	if run := q.tags[focus.TagWorkflowRunID]; run != "" {
		name = "run\x00" + q.tags[focus.TagRepo] + "\x00" + run
	}
	sum := sha256.Sum256([]byte("costroid trace\x00" + name))
	return [16]byte(sum[:16])
}

func spanIDOf(q *request) [8]byte {
	sum := sha256.Sum256([]byte("costroid span\x00" + q.rowIDs[0]))
	return [8]byte(sum[:8])
}

// nanos is an instant as OTLP's fixed64 count of nanoseconds since 1970.
func nanos(at time.Time) uint64 {
	return uint64(at.UnixNano())
}

func nanosText(at time.Time) string {
	return strconv.FormatUint(nanos(at), 10)
}
