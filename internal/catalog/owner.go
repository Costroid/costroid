package catalog

import "strings"

// The providers whose sheets price a model by the model's own name.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
	ProviderGoogle    = "google"
)

// owners maps the start of a model's name, in lower case, to the provider
// that publishes the model. codex and chatgpt name OpenAI models that no
// sheet lists, which price at OpenAI's fallback.
var owners = []struct{ prefix, provider string }{
	{"claude", ProviderAnthropic},
	{"gpt-", ProviderOpenAI},
	{"chatgpt", ProviderOpenAI},
	{"chat-latest", ProviderOpenAI},
	{"codex", ProviderOpenAI},
	{"o1", ProviderOpenAI},
	{"o3", ProviderOpenAI},
	{"o4", ProviderOpenAI},
	{"gemini-", ProviderGoogle},
}

// OwnerOf is the provider whose sheet prices a model, read from the model's
// name: the provider that publishes it, whoever served the request. It is
// empty when the name belongs to no provider the catalog holds a sheet for.
func OwnerOf(model string) string {
	name := strings.ToLower(model)
	for _, owner := range owners {
		if strings.HasPrefix(name, owner.prefix) {
			return owner.provider
		}
	}
	return ""
}
