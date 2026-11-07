// Command gatewaycodex stands in for codex exec in the gateway journey. It
// sends Responses API requests to the openai_base_url its last --config names,
// on CODEX_API_KEY, until the gateway refuses one. Then it prints Codex's
// stream with the usage the accepted requests reported. It exits 3 when an
// OpenAI key reached it, and 4 when its command line does not select the
// built-in OpenAI provider, which is the provider the base URL moves.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

const (
	model       = "gpt-5-nano"
	maxRequests = 20
	thread      = "019a0000-0000-7000-8000-0000000000cc"
)

type outcome struct {
	Accepted      int    `json:"accepted"`
	RefusedStatus int    `json:"refused_status"`
	Refusal       string `json:"refusal"`
}

type usage struct {
	Input  int64 `json:"input_tokens"`
	Output int64 `json:"output_tokens"`
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("codex-cli 0.164.0-alpha.11.1")
		return
	}
	if os.Getenv("OPENAI_API_KEY") != "" {
		fmt.Fprintln(os.Stderr, "gatewaycodex: an OpenAI key reached the agent")
		os.Exit(3)
	}
	if config("model_provider") != "openai" {
		fmt.Fprintf(os.Stderr, "gatewaycodex: the command line selects the provider %q\n", config("model_provider"))
		os.Exit(4)
	}
	base, key := config("openai_base_url"), os.Getenv("CODEX_API_KEY")
	var result outcome
	var total usage
	for range maxRequests {
		status, body, used := send(base, key)
		if status != http.StatusOK {
			result.RefusedStatus, result.Refusal = status, string(body)
			break
		}
		result.Accepted++
		total.Input += used.Input
		total.Output += used.Output
	}
	report(result)
	fmt.Printf(`{"type":"thread.started","thread_id":%q}`+"\n", thread)
	fmt.Println(`{"type":"turn.started"}`)
	fmt.Printf(`{"type":"turn.completed","usage":{"input_tokens":%d,"cached_input_tokens":0,"output_tokens":%d}}`+"\n", total.Input, total.Output)
}

// config is the value of the last --config or -c for key, as Codex takes it,
// with its TOML quotes removed.
func config(key string) string {
	var value string
	for i := 1; i+1 < len(os.Args); i++ {
		if os.Args[i] != "--config" && os.Args[i] != "-c" {
			continue
		}
		if name, setting, ok := strings.Cut(os.Args[i+1], "="); ok && name == key {
			value = setting
		}
	}
	if unquoted, err := strconv.Unquote(value); err == nil {
		return unquoted
	}
	return value
}

// send makes one streamed request, as Codex does, and reads the usage of its
// response.completed event.
func send(base, key string) (int, []byte, usage) {
	body := []byte(`{"model":"` + model + `","input":"Say ok.","stream":true}`)
	request, err := http.NewRequest(http.MethodPost, base+"/responses", bytes.NewReader(body))
	if err != nil {
		fail(err)
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, []byte(err.Error()), usage{}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(response.Body)
		return response.StatusCode, data, usage{}
	}
	var used usage
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		var event struct {
			Type     string `json:"type"`
			Response struct {
				Usage usage `json:"usage"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(data), &event) == nil && event.Type == "response.completed" {
			used = event.Response.Usage
		}
	}
	return http.StatusOK, nil, used
}

func report(result outcome) {
	data, _ := json.Marshal(result)
	if err := os.WriteFile(os.Getenv("GATEWAY_AGENT_REPORT"), data, 0o600); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gatewaycodex:", err)
	os.Exit(2)
}
