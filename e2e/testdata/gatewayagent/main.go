// Command gatewayagent stands in for claude -p in the gateway journey. It sends
// Messages API requests to ANTHROPIC_BASE_URL on ANTHROPIC_AUTH_TOKEN until the
// gateway refuses one, then prints a result line in Claude Code's stream shape
// with the usage the accepted requests reported. It reads both variables as
// Claude Code does: from its environment, with the env of the working
// directory's .claude/settings.json, then .claude/settings.local.json, then
// the last --settings set over it. It exits 3 when a provider key reached it.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	model       = "claude-haiku-5-5"
	maxRequests = 20
	session     = "00000000-0000-4000-8000-0000000000aa"
)

type outcome struct {
	Accepted      int    `json:"accepted"`
	RefusedStatus int    `json:"refused_status"`
	Refusal       string `json:"refusal"`
	AfterPause    int    `json:"after_pause_status"`
	Settings      string `json:"settings"`
	SettingsMode  string `json:"settings_mode"`
}

func main() {
	var result outcome
	env := readEnv(&result)
	if env["ANTHROPIC_API_KEY"] != "" {
		fmt.Fprintln(os.Stderr, "gatewayagent: a provider key reached the agent")
		os.Exit(3)
	}
	base, token := env["ANTHROPIC_BASE_URL"], env["ANTHROPIC_AUTH_TOKEN"]
	var input, output int64
	for range maxRequests {
		status, body, usage := send(base, token)
		if status != http.StatusOK {
			result.RefusedStatus, result.Refusal = status, string(body)
			break
		}
		result.Accepted++
		input += usage.Input
		output += usage.Output
		if pause := os.Getenv("GATEWAY_AGENT_PAUSE"); pause != "" && result.Accepted == 1 {
			await(pause)
			status, _, usage := send(base, token)
			result.AfterPause = status
			if status == http.StatusOK {
				result.Accepted++
				input += usage.Input
				output += usage.Output
			}
			break
		}
	}
	report(result)
	fmt.Printf(`{"type":"system","subtype":"init","session_id":%q,"model":%q,"apiKeySource":"none","claude_code_version":"2.1.316"}`+"\n", session, model)
	fmt.Printf(`{"type":"result","subtype":"success","is_error":false,"session_id":%q,"total_cost_usd":0,"usage":{"input_tokens":%d,"output_tokens":%d},"modelUsage":{%q:{"inputTokens":%d,"outputTokens":%d,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"cacheCreationEphemeral5mInputTokens":0,"cacheCreationEphemeral1hInputTokens":0,"thinkingTokens":0}}}`+"\n",
		session, input, output, model, input, output)
}

// readEnv is the environment the agent runs on, with each settings file's env
// set over it in Claude Code's order, the last --settings on top. It notes the
// path and the mode of that --settings file.
func readEnv(result *outcome) map[string]string {
	env := map[string]string{}
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		env[name] = value
	}
	maps.Copy(env, settingsEnv(".claude/settings.json", true))
	maps.Copy(env, settingsEnv(".claude/settings.local.json", true))
	var flag string
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "--" {
			break
		}
		if os.Args[i] == "--settings" && i+1 < len(os.Args) {
			i++
			flag = os.Args[i]
		}
	}
	if flag == "" {
		return env
	}
	if !strings.HasPrefix(flag, "{") {
		info, err := os.Stat(flag)
		if err != nil {
			fail(err)
		}
		result.Settings, result.SettingsMode = flag, fmt.Sprintf("%o", info.Mode().Perm())
	}
	maps.Copy(env, settingsEnv(flag, false))
	return env
}

// settingsEnv is the env of a settings file, or of inline JSON. A project file
// may be missing.
func settingsEnv(value string, optional bool) map[string]string {
	data := []byte(value)
	if !strings.HasPrefix(value, "{") {
		read, err := os.ReadFile(value)
		if optional && os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			fail(err)
		}
		data = read
	}
	var settings struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		fail(fmt.Errorf("%s: %w", value, err))
	}
	return settings.Env
}

type usage struct {
	Input  int64 `json:"input_tokens"`
	Output int64 `json:"output_tokens"`
}

func send(base, token string) (int, []byte, usage) {
	body := []byte(`{"model":"` + model + `","max_tokens":4096,"messages":[{"role":"user","content":"Say ok."}]}`)
	request, err := http.NewRequest(http.MethodPost, base+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		fail(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("anthropic-version", "2023-06-01")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, []byte(err.Error()), usage{}
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	var answer struct {
		Usage usage `json:"usage"`
	}
	_ = json.Unmarshal(data, &answer)
	return response.StatusCode, data, answer.Usage
}

func await(path string) {
	for deadline := time.Now().Add(3 * time.Minute); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	fail(fmt.Errorf("no %s within 3 minutes", path))
}

func report(result outcome) {
	data, _ := json.Marshal(result)
	if err := os.WriteFile(os.Getenv("GATEWAY_AGENT_REPORT"), data, 0o600); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gatewayagent:", err)
	os.Exit(2)
}
