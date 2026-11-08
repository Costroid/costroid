package claudecode

import (
	"encoding/json"
	"errors"
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

// settingsFlag takes a settings file's path or inline JSON. Claude Code applies
// it above the project's and the user's settings files.
const settingsFlag = "--settings"

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

// GatewayArgs is empty. Claude Code's command line names the gateway through
// the settings file GatewaySettings writes, because that file holds the key.
func (Agent) GatewayArgs(string) []string {
	return nil
}

// GatewaySettings writes the settings file a gateway run gives Claude Code, and
// returns the arguments that name it and the function that removes it.
//
// Claude Code applies a project's .claude/settings.json env over the
// environment it starts with, and a --settings file's env over both, one
// variable at a time. Of several --settings it reads the last alone. So the
// file holds the command's own last --settings, every key as written, with
// the gateway's base URL, the run's key, no API key and no other provider set
// in its env. The run places its arguments after every option the command
// holds. The file is new, and only the run's user can read it.
func (Agent) GatewaySettings(argv []string, base, key string) (args []string, remove func() error, err error) {
	settings := map[string]json.RawMessage{}
	if value, ok := lastSettings(argv); ok {
		if settings, _, err = readSettings(value); err != nil {
			return nil, nil, err
		}
	}
	data, err := withGatewayEnv(settings, base, key)
	if err != nil {
		return nil, nil, err
	}
	file, err := os.CreateTemp("", "costroid-claude-settings-*.json")
	if err != nil {
		return nil, nil, fmt.Errorf("claude-code: create the settings file that names the gateway: %w", err)
	}
	remove = func() error { return os.Remove(file.Name()) }
	_, err = file.Write(data)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, nil, fmt.Errorf("claude-code: write the settings file that names the gateway: %w", errors.Join(err, remove()))
	}
	return []string{settingsFlag, file.Name()}, remove, nil
}

// withGatewayEnv is settings with every routing variable in its env set: the
// base URL to the gateway, the bearer token to the run's key, and every other
// to empty, which Claude Code reads as unset. Its other variables stay.
func withGatewayEnv(settings map[string]json.RawMessage, base, key string) ([]byte, error) {
	var env map[string]json.RawMessage
	if raw, ok := settings["env"]; ok {
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, fmt.Errorf("claude-code: under a gateway the run sets env in --settings, and the command's env is not a JSON object: %w", err)
		}
	}
	if env == nil {
		env = map[string]json.RawMessage{}
	}
	set := map[string]string{apiKeyVariable: ""}
	for _, variable := range routingVariables {
		set[variable] = ""
	}
	set[baseURLVariable], set[authTokenVariable] = base, key
	for variable, value := range set {
		quoted, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		env[variable] = quoted
	}
	merged, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	settings["env"] = merged
	return json.Marshal(settings)
}

// option is one option of a command, with its value: the inline value after
// "=", or for --settings the next argument when there is none inline.
type option struct {
	flag, value string
}

// options is each option of the command up to a "--" that ends them.
func options(argv []string) []option {
	var found []option
	for i := 1; i < len(argv); i++ {
		if argv[i] == "--" {
			break
		}
		flag, value, inline := strings.Cut(argv[i], "=")
		if flag == settingsFlag && !inline && i+1 < len(argv) {
			i++
			value = argv[i]
		}
		found = append(found, option{flag: flag, value: value})
	}
	return found
}

// lastSettings is the value of the command's last --settings, the one Claude
// Code applies.
func lastSettings(argv []string) (string, bool) {
	found := options(argv)
	for i := len(found) - 1; i >= 0; i-- {
		if found[i].flag == settingsFlag {
			return found[i].value, true
		}
	}
	return "", false
}

// GatewayBypass names the first flag of the command that would send Claude
// Code's requests somewhere other than the gateway on the run's key: a flag
// that starts the session in the cloud, or a --settings value whose env sets
// a routing variable. It reads the command up to a "--" that ends the options.
func (Agent) GatewayBypass(argv []string) (string, error) {
	for _, o := range options(argv) {
		if slices.Contains(cloudFlags, o.flag) {
			return o.flag, nil
		}
		if o.flag != settingsFlag {
			continue
		}
		variable, err := settingsRoute(o.value)
		if err != nil {
			return "", err
		}
		if variable != "" {
			return settingsFlag + " with " + variable + " in its env", nil
		}
	}
	return "", nil
}

// readSettings is a --settings value's object, and where it was read from. The
// value is inline JSON or the path of a JSON file.
func readSettings(value string) (map[string]json.RawMessage, string, error) {
	data, source := []byte(value), "its inline JSON"
	if !strings.HasPrefix(strings.TrimSpace(value), "{") {
		read, err := os.ReadFile(value)
		if err != nil {
			return nil, value, fmt.Errorf("claude-code: under a gateway the run reads --settings to see where it sends requests, and %s could not be read: %w", value, err)
		}
		data, source = read, value
	}
	settings := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, source, fmt.Errorf("claude-code: under a gateway the run reads --settings to see where it sends requests, and %s is not a JSON object: %w", source, err)
	}
	if settings == nil {
		settings = map[string]json.RawMessage{}
	}
	return settings, source, nil
}

// settingsRoute is the first routing variable a --settings value's env sets,
// or empty.
func settingsRoute(value string) (string, error) {
	settings, source, err := readSettings(value)
	if err != nil {
		return "", err
	}
	var env map[string]json.RawMessage
	if raw, ok := settings["env"]; ok {
		if err := json.Unmarshal(raw, &env); err != nil {
			return "", fmt.Errorf("claude-code: under a gateway the run reads --settings to see where it sends requests, and %s is not a JSON object: %w", source, err)
		}
	}
	for _, variable := range routingVariables {
		if _, ok := env[variable]; ok {
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
