package ghaw

import (
	"fmt"
	"strings"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/focus"
)

// The providers a record can name that the catalog prices, and Copilot, which
// serves models of both.
const (
	providerAnthropic = "anthropic"
	providerOpenAI    = "openai"
	providerCopilot   = "copilot"
)

// copilotNote says how a Copilot request's rows were priced.
const copilotNote = "Copilot served this request; priced at the list rate of the model's own provider; how its input counts cached tokens is unverified"

// request maps one record. priced is false when the catalog holds no sheet
// for the record's provider, so no row can state a price for it.
func (r record) request() (adapter.Request, bool, error) {
	start, end, err := r.times()
	if err != nil {
		return adapter.Request{}, false, err
	}
	request := adapter.Request{
		ID:       r.RequestID,
		Provider: r.Provider,
		Model:    r.Model,
		Start:    start,
		End:      end,
		// The record states no tier or region: gh-aw's engines call the
		// provider's standard tier at its global endpoint.
		ServiceTier: catalog.TierStandard,
		Region:      catalog.RegionGlobal,
		Grain:       adapter.PerRequest,
	}
	if r.Purpose == routingPurpose {
		request.Note = routingNote
	}
	counts := r.counts()
	if err := counts.check(); err != nil {
		return adapter.Request{}, false, err
	}
	switch r.Provider {
	case providerAnthropic:
		request.Tokens, request.AssumedWrite, request.ContextInputTokens = counts.anthropic()
	case providerOpenAI:
		request.Tokens, request.AssumedWrite, request.ContextInputTokens, err = counts.openAI()
	case providerCopilot:
		owner := modelOwner(r.Model)
		if owner == "" {
			return request, false, nil
		}
		request.Provider = owner
		request.Note = joinNotes(request.Note, copilotNote)
		request.Tokens, request.AssumedWrite, request.ContextInputTokens = counts.copilot(owner)
	default:
		return request, false, nil
	}
	if err != nil {
		return adapter.Request{}, false, err
	}
	return request, true, nil
}

// tokenCounts are a record's counts as the record states them.
type tokenCounts struct {
	input, output, cacheRead, cacheWrite, reasoning int64
}

func (r record) counts() tokenCounts {
	return tokenCounts{
		input:      *r.InputTokens,
		output:     *r.OutputTokens,
		cacheRead:  *r.CacheReadTokens,
		cacheWrite: *r.CacheWriteTokens,
		reasoning:  r.ReasoningTokens,
	}
}

// check refuses a negative count. The schema allows none, and one would price
// as a negative cost.
func (c tokenCounts) check() error {
	for _, count := range []int64{c.input, c.output, c.cacheRead, c.cacheWrite, c.reasoning} {
		if count < 0 {
			return fmt.Errorf("a token count of %d is below zero", count)
		}
	}
	return nil
}

// anthropic maps a request the proxy took from Anthropic's usage: input_tokens
// is the uncached input, and cache reads and writes are counted apart from
// it. The record does not say how long a write lives. gh-aw runs Claude on an
// API key, where Anthropic's default is five minutes.
func (c tokenCounts) anthropic() (adapter.Tokens, focus.TokenClass, int64) {
	tokens := adapter.Tokens{
		InputUncached:   c.input,
		CacheRead:       c.cacheRead,
		CacheWrite5m:    c.cacheWrite,
		Output:          c.output,
		ReasoningOutput: c.reasoning,
	}
	return tokens, assumed(c.cacheWrite, focus.CacheWrite5m), c.input + c.cacheRead + c.cacheWrite
}

// openAI maps a request the proxy took from OpenAI's usage: input_tokens is
// OpenAI's own, which holds the cached tokens and the cache writes, so the
// uncached input is what remains. Writes take OpenAI's default lifetime of 30
// minutes. Output holds the reasoning tokens.
func (c tokenCounts) openAI() (adapter.Tokens, focus.TokenClass, int64, error) {
	if c.cacheRead > c.input || c.cacheWrite > c.input-c.cacheRead {
		return adapter.Tokens{}, "", 0, fmt.Errorf("input of %d tokens is below the %d cached and %d written to the cache", c.input, c.cacheRead, c.cacheWrite)
	}
	tokens := adapter.Tokens{
		InputUncached:       c.input - c.cacheRead - c.cacheWrite,
		CacheRead:           c.cacheRead,
		CacheWrite30m:       c.cacheWrite,
		Output:              c.output,
		ReasoningOutput:     c.reasoning,
		CachedInputReported: c.cacheRead,
	}
	return tokens, assumed(c.cacheWrite, focus.CacheWrite30m), c.input, nil
}

// copilot maps a request Copilot served. The proxy states Copilot's input as
// Copilot's response gives it: with the cached tokens when the response lumps
// them in, without them when it splits them out, and the record does not say
// which. Input that can hold the cached tokens and writes is read as holding
// them, as OpenAI's does; smaller input can only be the split form. Writes
// take the default lifetime of the model's own provider.
func (c tokenCounts) copilot(owner string) (adapter.Tokens, focus.TokenClass, int64) {
	uncached := c.input
	if c.cacheRead+c.cacheWrite <= c.input {
		uncached = c.input - c.cacheRead - c.cacheWrite
	}
	tokens := adapter.Tokens{
		InputUncached:   uncached,
		CacheRead:       c.cacheRead,
		Output:          c.output,
		ReasoningOutput: c.reasoning,
	}
	class := focus.CacheWrite30m
	if owner == providerAnthropic {
		class = focus.CacheWrite5m
		tokens.CacheWrite5m = c.cacheWrite
	} else {
		tokens.CacheWrite30m = c.cacheWrite
	}
	return tokens, assumed(c.cacheWrite, class), uncached + c.cacheRead + c.cacheWrite
}

func assumed(writes int64, class focus.TokenClass) focus.TokenClass {
	if writes == 0 {
		return ""
	}
	return class
}

// modelOwner is the provider whose catalog sheet prices a model Copilot
// served, read from the model's name, empty when no sheet does.
func modelOwner(model string) string {
	name := strings.ToLower(model)
	switch {
	case strings.HasPrefix(name, "claude-"):
		return providerAnthropic
	case strings.HasPrefix(name, "gpt-"), strings.HasPrefix(name, "o1"), strings.HasPrefix(name, "o3"), strings.HasPrefix(name, "o4"):
		return providerOpenAI
	}
	return ""
}
