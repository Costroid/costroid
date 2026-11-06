// Command gatewayagent stands in for claude -p in the gateway journey. It sends
// Messages API requests to ANTHROPIC_BASE_URL on ANTHROPIC_AUTH_TOKEN until the
// gateway refuses one, then prints a result line in Claude Code's stream shape
// with the usage the accepted requests reported. It exits 3 when a provider key
// reached it.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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
}

func main() {
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		fmt.Fprintln(os.Stderr, "gatewayagent: a provider key reached the agent")
		os.Exit(3)
	}
	base, token := os.Getenv("ANTHROPIC_BASE_URL"), os.Getenv("ANTHROPIC_AUTH_TOKEN")
	var result outcome
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
