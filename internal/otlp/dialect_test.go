package otlp

import "testing"

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
