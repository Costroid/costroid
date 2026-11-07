package claudecode

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The recordings keep no text, so these tests put it back: every message the
// agent wrote or read on the way gets a line that must never reach the answer,
// and the last result gets the answer.
const (
	finalAnswer = "Two bugs: main.go:12 divides by zero, and auth.go:40 logs the token."
	onTheWay    = "ON THE WAY: a reply, a tool's input or a file the agent read"
)

// withText fills a recorded message: the answer into a result, and the
// on-the-way line into every content block of any other message.
func withText(t *testing.T, raw []byte, answer string) []byte {
	t.Helper()
	var message map[string]any
	if err := json.Unmarshal(raw, &message); err != nil {
		t.Fatal(err)
	}
	if message["type"] == "result" {
		message["result"] = answer
	}
	if inner, ok := message["message"].(map[string]any); ok {
		if blocks, ok := inner["content"].([]any); ok {
			for _, block := range blocks {
				block.(map[string]any)["text"] = onTheWay
				block.(map[string]any)["input"] = map[string]string{"command": onTheWay}
				block.(map[string]any)["content"] = onTheWay
			}
		}
	}
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// filledStream is a recorded stream with its text put back.
func filledStream(t *testing.T, scenario, answer string) string {
	t.Helper()
	data, err := os.ReadFile(fixture(scenario, "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for line := range bytes.Lines(data) {
		out.Write(withText(t, bytes.TrimSpace(line), answer))
		out.WriteString("\n")
	}
	return out.String()
}

// filledExecutionFile is a recorded execution file with its text put back.
func filledExecutionFile(t *testing.T, scenario, answer string) string {
	t.Helper()
	data, err := os.ReadFile(fixture(scenario, "claude-execution-output.json"))
	if err != nil {
		t.Fatal(err)
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(data, &messages); err != nil {
		t.Fatal(err)
	}
	for i := range messages {
		messages[i] = withText(t, messages[i], answer)
	}
	filled, err := json.MarshalIndent(messages, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(filled)
}

func TestTheAnswerIsTheLastResultAndNothingWrittenOnTheWay(t *testing.T) {
	for name, read := range map[string]func() (string, error){
		"stream": func() (string, error) {
			return ReadStreamAnswer(strings.NewReader(filledStream(t, "subagent", finalAnswer)))
		},
		"execution file": func() (string, error) {
			return ReadExecutionAnswer(strings.NewReader(filledExecutionFile(t, "action-session", finalAnswer)))
		},
	} {
		t.Run(name, func(t *testing.T) {
			answer, err := read()
			if err != nil || answer != finalAnswer {
				t.Fatalf("answer %q, error %v", answer, err)
			}
		})
	}
	// Only the last result answers: a stream that resumed holds two.
	stream := filledStream(t, "one-request", "an earlier answer") + filledStream(t, "one-request", finalAnswer)
	if answer, err := ReadStreamAnswer(strings.NewReader(stream)); err != nil || answer != finalAnswer {
		t.Fatalf("answer %q, error %v", answer, err)
	}
}

func TestARunThatStoppedHasNoAnswer(t *testing.T) {
	for name, read := range map[string]func() (string, error){
		"stream at the cap": func() (string, error) {
			return ReadStreamAnswer(strings.NewReader(filledStream(t, "budget-stop", finalAnswer)))
		},
		"execution file at the cap": func() (string, error) {
			return ReadExecutionAnswer(strings.NewReader(filledExecutionFile(t, "action-budget-stop", finalAnswer)))
		},
	} {
		t.Run(name, func(t *testing.T) {
			answer, err := read()
			if answer != "" || err == nil || !strings.Contains(err.Error(), "ended with error_max_budget_usd, so it has no answer") {
				t.Fatalf("answer %q, error %v", answer, err)
			}
		})
	}
}

func TestAStreamWithNoUsableResultHasNoAnswer(t *testing.T) {
	for name, stream := range map[string]string{
		"no result":         `{"type":"assistant","message":{"content":[{"type":"text","text":"` + onTheWay + `"}]}}` + "\n",
		"a result not text": `{"type":"result","subtype":"success","result":{"text":"x"}}`,
		"an empty answer":   `{"type":"result","subtype":"success","result":"  \n"}`,
		"a strange subtype": `{"type":"result","subtype":"Error: <b>","result":"x"}`,
		"nothing":           "",
	} {
		t.Run(name, func(t *testing.T) {
			answer, err := ReadStreamAnswer(strings.NewReader(stream))
			if answer != "" || err == nil {
				t.Fatalf("answer %q, error %v", answer, err)
			}
			if strings.Contains(err.Error(), "<b>") || strings.Contains(err.Error(), onTheWay) {
				t.Fatalf("the error quotes the file: %v", err)
			}
		})
	}
}

func TestAnExecutionFileThatIsNoArrayIsRefused(t *testing.T) {
	for _, file := range []string{`{"type":"result","subtype":"success","result":"x"}`, "", `[{"type":"result"`} {
		if answer, err := ReadExecutionAnswer(strings.NewReader(file)); answer != "" || err == nil {
			t.Errorf("%q: answer %q, error %v", file, answer, err)
		}
	}
}
