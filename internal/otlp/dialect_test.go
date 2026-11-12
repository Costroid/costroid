package otlp

import (
	"slices"
	"testing"

	"github.com/Costroid/costroid/internal/focus"
)

// TestStandardDialectReadsAnExclusiveSenderWithANote reads the standard
// dialect's input as the GenAI convention asks, holding the cache reads and
// writes, until the cache counts pass it. Then only a sender that excludes
// them, as Anthropic's API reports input, could have stated it: the input is
// read as uncached, the cache counts as stated, and the request says so. An
// input that can hold its cache counts is read as holding them, with no note,
// and a recorded dialect whose input holds its cache still rejects counts
// that pass it.
func TestStandardDialectReadsAnExclusiveSenderWithANote(t *testing.T) {
	exclusive := parseSpan(t, nil, map[string]any{
		"gen_ai.provider.name": "anthropic", "gen_ai.response.model": "claude-haiku-5-5",
		keyInput: 4, keyOutput: 77, keyCacheRead: 0, keyCacheWrite: 21656,
	})
	if exclusive.Dialect != Standard {
		t.Fatalf("dialect %s", exclusive.Dialect)
	}
	assertClasses(t, exclusive.Counts, map[focus.TokenClass]int64{
		focus.InputUncached: 4, focus.CacheWrite5m: 21656, focus.Output: 77,
	})
	if exclusive.ContextInputTokens != 21660 {
		t.Errorf("context %d, want 21660", exclusive.ContextInputTokens)
	}
	if !slices.Contains(exclusive.Notes, noteExclusiveInput) {
		t.Errorf("an exclusive sender is not noted: %q", exclusive.Notes)
	}

	inclusive := parseSpan(t, nil, map[string]any{
		"gen_ai.provider.name": "anthropic", "gen_ai.response.model": "claude-haiku-5-5",
		keyInput: 21764, keyOutput: 73, keyCacheRead: 21656, keyCacheWrite: 106,
	})
	assertClasses(t, inclusive.Counts, map[focus.TokenClass]int64{
		focus.InputUncached: 2, focus.CacheRead: 21656, focus.CacheWrite5m: 106, focus.Output: 73,
		focus.CachedInputReported: 21656,
	})
	if slices.Contains(inclusive.Notes, noteExclusiveInput) {
		t.Errorf("an input that holds its cache is noted as exclusive: %q", inclusive.Notes)
	}

	batch, err := Parse(spanBody(t, map[string]any{"service.name": "github-copilot"}, map[string]any{
		"gen_ai.response.model": "claude-haiku-5-5", keyInput: 4, keyOutput: 77, keyCacheCreationOld: 21656,
	}), JSON, Traces)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Usage) != 0 || batch.Rejected != 1 {
		t.Errorf("a Copilot CLI record whose cache passes its input read as %+v", batch)
	}
}

// TestStandardDialectOwnsModelsByTheirName prices a record that names no
// provider at the sheet of the provider that publishes its model, read from
// the model's name: o3 and codex-mini-latest at OpenAI's, gemini-3.8-flash at
// Google's, Claude-Haiku-5-5 at Anthropic's. A model no provider with a sheet
// publishes is rejected.
func TestStandardDialectOwnsModelsByTheirName(t *testing.T) {
	for model, want := range map[string]string{
		"o3":                "openai",
		"codex-mini-latest": "openai",
		"gemini-3.8-flash":  "google",
		"Claude-Haiku-5-5":  "anthropic",
	} {
		usage := parseSpan(t, nil, map[string]any{"gen_ai.response.model": model, keyInput: 10, keyOutput: 1})
		if usage.Provider != want {
			t.Errorf("%s reads %q, want %q", model, usage.Provider, want)
		}
	}
	batch, err := Parse(spanBody(t, nil, map[string]any{"gen_ai.response.model": "mai-code-1.1-flash", keyInput: 10, keyOutput: 1}), JSON, Traces)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Rejected != 1 {
		t.Errorf("a model with no owner read as %+v", batch)
	}
}
