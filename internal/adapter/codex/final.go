package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// streamEvent is the part of a codex exec --json event that holds the answer.
type streamEvent struct {
	Type string `json:"type"`
	Item struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
}

// turnComplete is the payload of a rollout file's task_complete event. Codex
// accepts turn_complete as the same event.
type turnComplete struct {
	Type             string  `json:"type"`
	LastAgentMessage *string `json:"last_agent_message"`
}

// ReadStreamAnswer returns the answer a codex exec --json run ended with, as
// codex exec keeps its final message: the last agent message completed in a
// turn that completed. A turn that failed leaves no answer, and nothing else
// the stream carries is kept.
func ReadStreamAnswer(stream io.Reader) (string, error) {
	reader := bufio.NewReader(stream)
	var inTurn, answer string
	completed, failed := false, false
	for {
		line, err := reader.ReadBytes('\n')
		var event streamEvent
		if json.Unmarshal(bytes.TrimSpace(line), &event) == nil {
			switch event.Type {
			case "turn.started":
				inTurn = ""
			case "item.completed":
				if event.Item.Type == "agent_message" {
					inTurn = event.Item.Text
				}
			case "turn.completed":
				answer, completed, failed = inTurn, true, false
			case "turn.failed":
				answer, failed = "", true
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("codex: read the stream: %w", err)
		}
	}
	switch {
	case failed:
		return "", errors.New("codex: the run's last turn failed, so it has no answer")
	case !completed:
		return "", errors.New("codex: the stream holds no completed turn, so the run ended before Codex answered")
	case strings.TrimSpace(answer) == "":
		return "", errors.New("codex: the run's last turn completed with no agent message")
	}
	return answer, nil
}

// ReadHomeAnswer returns the answer a Codex run that already ended left in its
// Codex home: the last agent message of the latest completed turn, from the
// rollout files of threads no other thread started. A subagent's answer went
// to the thread that started it, not to the run. A file last written before
// since belongs to an earlier job and is not read; a zero since reads every
// file.
func ReadHomeAnswer(home string, since time.Time) (string, error) {
	names, err := fs.Glob(os.DirFS(home), rolloutPattern)
	if err != nil {
		return "", fmt.Errorf("codex: list the rollout files under %s: %w", home, err)
	}
	var answer string
	var at time.Time
	for _, name := range names {
		text, when, err := fileAnswer(filepath.Join(home, filepath.FromSlash(name)), since)
		if err != nil {
			return "", fmt.Errorf("codex: rollout file %s: %w", name, err)
		}
		if text != "" && !when.Before(at) {
			answer, at = text, when
		}
	}
	if strings.TrimSpace(answer) == "" {
		return "", fmt.Errorf("codex: no rollout file under %s holds a completed turn with an agent message", filepath.Join(home, "sessions"))
	}
	return answer, nil
}

// fileAnswer reads one rollout file's last completed turn's answer, empty for
// a subagent's file, an earlier job's file or a file with none.
func fileAnswer(path string, since time.Time) (string, time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", time.Time{}, err
	}
	if !since.IsZero() && info.ModTime().Before(since.Add(-homeSlack)) {
		return "", time.Time{}, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", time.Time{}, err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	var answer string
	var at time.Time
	for first := true; ; first = false {
		line, err := reader.ReadBytes('\n')
		var entry rolloutEntry
		if json.Unmarshal(bytes.TrimSpace(line), &entry) == nil {
			if first && entry.Type == "session_meta" && isSubagent(entry.Payload) {
				return "", time.Time{}, nil
			}
			if text, ok := completedTurn(entry); ok {
				answer, at = text, entry.Timestamp
			}
		}
		if errors.Is(err, io.EOF) {
			return answer, at, nil
		}
		if err != nil {
			return "", time.Time{}, err
		}
	}
}

func isSubagent(payload json.RawMessage) bool {
	var meta sessionMeta
	return json.Unmarshal(payload, &meta) == nil && string(meta.ThreadSource) == subagentSource
}

// completedTurn reads the answer from a task_complete event. A turn that
// completed with no agent message answers nothing.
func completedTurn(entry rolloutEntry) (string, bool) {
	if entry.Type != "event_msg" {
		return "", false
	}
	var event turnComplete
	if json.Unmarshal(entry.Payload, &event) != nil || (event.Type != "task_complete" && event.Type != "turn_complete") {
		return "", false
	}
	if event.LastAgentMessage == nil {
		return "", true
	}
	return *event.LastAgentMessage, true
}
