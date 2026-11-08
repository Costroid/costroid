package codex

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	execStream    = "testdata/0.164.0-alpha.11.1/exec/stream.jsonl"
	stoppedStream = "testdata/0.164.0-alpha.11.1/stopped/stream.jsonl"
	finalAnswer   = "No bugs in the change. One question: is the retry in client.go:88 meant to be unbounded?"
)

// The recorded exec run completes two agent messages: one before it runs a
// command and one after. codex exec keeps the last as its final message, and
// so does the reader. The command and its output are never kept.
func TestTheStreamAnswerIsTheLastAgentMessageOfTheCompletedTurn(t *testing.T) {
	answer, err := ReadStreamAnswer(strings.NewReader(fixture(t, execStream)))
	if err != nil || answer != "done" {
		t.Fatalf("answer %q, error %v", answer, err)
	}
}

func TestAStreamWithNoCompletedTurnHasNoAnswer(t *testing.T) {
	failed := `{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"half an answer"}}
{"type":"turn.failed","error":{"message":"stream disconnected"}}
`
	emptyTurn := `{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"command_execution","aggregated_output":"secret file"}}
{"type":"turn.completed","usage":{}}
`
	for name, stream := range map[string]string{
		"the recorded run stopped before its turn completed": fixture(t, stoppedStream),
		"a failed turn":                       failed,
		"a turn with no agent message":        emptyTurn,
		"a completed turn, then a failed one": fixture(t, execStream) + failed,
	} {
		t.Run(name, func(t *testing.T) {
			if answer, err := ReadStreamAnswer(strings.NewReader(stream)); answer != "" || err == nil {
				t.Fatalf("answer %q, error %v", answer, err)
			}
		})
	}
}

// withAnswer puts an answer into a recorded rollout file's task_complete, and
// marks the file a subagent's when asked.
func withAnswer(t *testing.T, content, answer string, subagent bool) string {
	t.Helper()
	var out strings.Builder
	for _, line := range strings.SplitAfter(content, "\n") {
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) != nil {
			out.WriteString(line)
			continue
		}
		payload, _ := entry["payload"].(map[string]any)
		switch {
		case entry["type"] == "event_msg" && payload["type"] == "task_complete":
			payload["last_agent_message"] = answer
		case entry["type"] == "session_meta" && subagent:
			payload["thread_source"] = "subagent"
		}
		data, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(data)
		out.WriteString("\n")
	}
	return out.String()
}

func TestTheHomeAnswerIsTheRunsOwnLastCompletedTurn(t *testing.T) {
	recorded := fixture(t, actionFixture)
	later := actionEnd.Add(time.Minute)
	home := homeWith(t,
		rollout{actionThread, withAnswer(t, recorded, finalAnswer, false), actionEnd},
		// A subagent answers the thread that started it, not the run, even
		// when it ends last.
		rollout{"00000000-0000-4000-8000-000000000002", withAnswer(t, recorded, "a subagent's answer", true), later},
	)
	answer, err := ReadHomeAnswer(home, actionJobStart)
	if err != nil || answer != finalAnswer {
		t.Fatalf("answer %q, error %v", answer, err)
	}
}

func TestTheHomeAnswerSkipsAnEarlierJobsFile(t *testing.T) {
	earlier := actionJobStart.Add(-time.Hour)
	home := homeWith(t, rollout{actionThread, withAnswer(t, fixture(t, actionFixture), "an earlier job's answer", false), earlier})
	answer, err := ReadHomeAnswer(home, actionJobStart)
	if answer != "" || err == nil || !strings.Contains(err.Error(), filepath.Join(home, "sessions")) {
		t.Fatalf("answer %q, error %v", answer, err)
	}
	if answer, err := ReadHomeAnswer(home, time.Time{}); err != nil || answer != "an earlier job's answer" {
		t.Fatalf("with no start: answer %q, error %v", answer, err)
	}
}

func TestAHomeWithNoAnswerSaysSo(t *testing.T) {
	for name, home := range map[string]string{
		"the recorded run, which keeps no text": homeWith(t, rollout{actionThread, fixture(t, actionFixture), actionEnd}),
		"no rollout file":                       t.TempDir(),
		"no such home":                          filepath.Join(t.TempDir(), "absent"),
	} {
		t.Run(name, func(t *testing.T) {
			if answer, err := ReadHomeAnswer(home, actionJobStart); answer != "" || err == nil {
				t.Fatalf("answer %q, error %v", answer, err)
			}
		})
	}
}
