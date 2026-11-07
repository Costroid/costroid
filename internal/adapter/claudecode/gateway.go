package claudecode

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
)

// The variables Claude Code reads its endpoint and its credential from.
const (
	baseURLVariable   = "ANTHROPIC_BASE_URL"
	authTokenVariable = "ANTHROPIC_AUTH_TOKEN"
	apiKeyVariable    = "ANTHROPIC_API_KEY"
)

// cloudFlags start the session on a machine other than the one the run
// starts, which holds none of the run's environment.
var cloudFlags = []string{"--cloud", "--remote", "--environment"}

// routingVariables choose where Claude Code sends its requests, or with which
// bearer token: the Anthropic API's base URL and the run's key, a cloud
// provider in its place, or that provider's endpoint. A settings file's env
// overwrites the same variable in the environment the run gives the agent.
var routingVariables = []string{
	baseURLVariable, authTokenVariable,
	"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_MANTLE", "CLAUDE_CODE_USE_VERTEX",
	"CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CODE_USE_ANTHROPIC_AWS",
	"ANTHROPIC_BEDROCK_BASE_URL", "ANTHROPIC_BEDROCK_MANTLE_BASE_URL", "ANTHROPIC_VERTEX_BASE_URL",
	"ANTHROPIC_FOUNDRY_BASE_URL", "ANTHROPIC_FOUNDRY_RESOURCE", "ANTHROPIC_AWS_BASE_URL",
}

// GatewayEnv points Claude Code at the gateway, with the run's key as its
// bearer token. An API key beside it would let a request skip the gateway, so
// it is removed.
func (Agent) GatewayEnv(base, key string) []string {
	return []string{baseURLVariable + "=" + base, authTokenVariable + "=" + key, apiKeyVariable + "="}
}

// GatewayArgs is empty: Claude Code reads the gateway from its environment.
func (Agent) GatewayArgs(string) []string {
	return nil
}

// GatewayBypass names the first flag of the command that would send Claude
// Code's requests somewhere other than the gateway on the run's key: a flag
// that starts the session in the cloud, or a --settings value whose env sets
// a routing variable. It reads the command up to a "--" that ends the options.
func (Agent) GatewayBypass(argv []string) (string, error) {
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			break
		}
		flag, value, inline := strings.Cut(arg, "=")
		if slices.Contains(cloudFlags, flag) {
			return flag, nil
		}
		if flag != "--settings" {
			continue
		}
		if !inline && i+1 < len(argv) {
			i++
			value = argv[i]
		}
		variable, err := settingsRoute(value)
		if err != nil {
			return "", err
		}
		if variable != "" {
			return "--settings with " + variable + " in its env", nil
		}
	}
	return "", nil
}

// settingsRoute is the first routing variable a --settings value's env sets,
// or empty. The value is inline JSON or the path of a JSON file.
func settingsRoute(value string) (string, error) {
	data, source := []byte(value), "its inline JSON"
	if !strings.HasPrefix(strings.TrimSpace(value), "{") {
		read, err := os.ReadFile(value)
		if err != nil {
			return "", fmt.Errorf("claude-code: under a gateway the run reads --settings to see where it sends requests, and %s could not be read: %w", value, err)
		}
		data, source = read, value
	}
	var settings struct {
		Env map[string]json.RawMessage `json:"env"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return "", fmt.Errorf("claude-code: under a gateway the run reads --settings to see where it sends requests, and %s is not a JSON object: %w", source, err)
	}
	for _, variable := range routingVariables {
		if _, ok := settings.Env[variable]; ok {
			return variable, nil
		}
	}
	return "", nil
}

// BilledThroughGateway reads the run as key-billed: the gateway bills every
// request per token on its own provider key, whatever apiKeySource Claude Code
// states for the run's key.
func (s *Session) BilledThroughGateway() {
	s.billedThroughGateway = true
}
