package codex

import (
	"fmt"
	"strings"

	"github.com/Costroid/costroid/internal/focus"
)

// The variables Codex reads its credentials from. codex exec takes
// CODEX_API_KEY before any other sign-in.
const (
	codexAPIKeyVariable  = "CODEX_API_KEY"
	openAIAPIKeyVariable = "OPENAI_API_KEY"
)

// GatewayEnv gives Codex the run's key as its API key. An OpenAI key beside it
// would let a request skip the gateway, so it is removed.
func (Agent) GatewayEnv(_, key string) []string {
	return []string{codexAPIKeyVariable + "=" + key, openAIAPIKeyVariable + "="}
}

// GatewayArgs points Codex's built-in OpenAI provider at the gateway's OpenAI
// routes, and selects that provider, so a provider set in a configuration
// file cannot send requests around the gateway. Codex reads its base URL from
// configuration alone, and a --config value overrides every configuration
// file.
func (Agent) GatewayArgs(base string) []string {
	return []string{
		"--config", "openai_base_url=" + tomlBasicString(strings.TrimSuffix(base, "/")+"/v1"),
		"--config", "model_provider=" + tomlBasicString(provider),
	}
}

// BilledThroughGateway reads the run as key-billed: Codex starts on the run's
// key, which the gateway bills per token upstream, whatever costroid's own
// environment holds.
func (s *Session) BilledThroughGateway() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.costBasis = focus.Measured
}

// tomlBasicString quotes a value as a TOML basic string, which Codex parses
// a --config value as.
func tomlBasicString(value string) string {
	var quoted strings.Builder
	quoted.WriteByte('"')
	for _, r := range value {
		switch {
		case r == '"' || r == '\\':
			quoted.WriteByte('\\')
			quoted.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&quoted, `\u%04X`, r)
		default:
			quoted.WriteRune(r)
		}
	}
	quoted.WriteByte('"')
	return quoted.String()
}
