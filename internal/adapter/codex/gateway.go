package codex

import (
	"fmt"
	"slices"
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

// localProviderFlags select a local open-source provider. Codex sets the
// provider they name above every configuration layer, the run's --config
// model_provider included, so a gateway cannot hold such a run.
var localProviderFlags = []string{"--oss", "--local-provider"}

// routingKeys are the configuration keys that choose a provider or a base
// URL. The run sets the first two itself.
var routingKeys = []string{"model_provider", "openai_base_url", "model_providers"}

// GatewayBypass names the first flag of the command that would send Codex's
// requests somewhere other than the gateway: a local provider, or a --config
// value for a key that chooses the provider or its base URL. It reads the
// command as Codex does, up to a "--" that ends the options.
func (Agent) GatewayBypass(argv []string) (string, error) {
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			break
		}
		flag, value, inline := strings.Cut(arg, "=")
		if short, attached := strings.CutPrefix(arg, "-c"); attached && short != "" && !strings.HasPrefix(arg, "--") {
			flag, value, inline = "-c", strings.TrimPrefix(short, "="), true
		}
		switch {
		case slices.Contains(localProviderFlags, flag):
			return flag, nil
		case flag == "-c" || flag == "--config":
			if !inline && i+1 < len(argv) {
				i++
				value = argv[i]
			}
			if key := routingKey(value); key != "" {
				return flag + " " + key, nil
			}
		}
	}
	return "", nil
}

// routingKey is the key of a --config value when its first segment is one of
// routingKeys, or empty. Codex splits a key on every dot, with no quoting.
func routingKey(value string) string {
	key, _, ok := strings.Cut(value, "=")
	if !ok {
		return ""
	}
	key = strings.TrimSpace(key)
	first, _, _ := strings.Cut(key, ".")
	if slices.Contains(routingKeys, first) {
		return key
	}
	return ""
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
