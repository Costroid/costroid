package claudecode

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
)

const (
	// SourceExecutionFile marks usage read from the execution_file output of
	// claude-code-action.
	SourceExecutionFile = "execution_file"

	unwatchedNote = "none: the run was not started by costroid, and the file states no budget"
)

// executionMessage is what the reader wants from one message besides usage.
type executionMessage struct {
	Timestamp string `json:"timestamp"`
	Duration  int64  `json:"duration_ms"`
}

// executionSpan is when a run took place.
type executionSpan struct {
	first, last time.Time
	duration    time.Duration
}

func (s *executionSpan) add(message executionMessage) {
	if message.Duration > 0 {
		s.duration = time.Duration(message.Duration) * time.Millisecond
	}
	at, err := time.Parse(time.RFC3339Nano, message.Timestamp)
	if err != nil {
		return
	}
	if s.first.IsZero() || at.Before(s.first) {
		s.first = at
	}
	if at.After(s.last) {
		s.last = at
	}
}

// settled falls back to the instant the file was saved and the result's own
// duration when no message carries a timestamp.
func (s *executionSpan) settled(saved time.Time) (time.Time, time.Time) {
	if s.first.IsZero() {
		return saved.Add(-s.duration), saved
	}
	return s.first, s.last
}

// ReadExecutionFile reads the array of SDK messages claude-code-action wrote.
// The file holds prompts and tool output, and only usage fields leave it.
//
// The messages are the ones Claude Code streams, so the same session reads
// them, and the last result's per-model totals are the run. Costroid did not
// start this run, so it holds no budget: the grade is none, whatever flags the
// workflow passed. The action does not state them in the file.
func (a Agent) ReadExecutionFile(file io.Reader, saved time.Time) (adapter.Report, error) {
	session := &Session{configDir: a.configDir()}
	span, err := feedExecutionFile(session, file, saved)
	if err != nil {
		return adapter.Report{}, err
	}
	session.first, session.last = span.settled(saved)
	session.RunWindow(span.window(saved))
	report, err := session.Finish()
	if err != nil {
		return adapter.Report{}, err
	}
	report.Enforcement = adapter.Enforcement{Grade: adapter.GradeNone, Note: unwatchedNote}
	if report.CostSource == SourceStream {
		report.CostSource = SourceExecutionFile
	}
	return report, nil
}

// window is when the run ran: from the earlier of its first message and the
// save time back by the result's duration, to the later of its last message
// and the save time.
func (s *executionSpan) window(saved time.Time) (time.Time, time.Time) {
	start, end := s.settled(saved)
	if begun := saved.Add(-s.duration); begun.Before(start) {
		start = begun
	}
	if saved.After(end) {
		end = saved
	}
	return start, end
}

func feedExecutionFile(session *Session, file io.Reader, saved time.Time) (executionSpan, error) {
	decoder := json.NewDecoder(file)
	opening, err := decoder.Token()
	if err != nil {
		return executionSpan{}, fmt.Errorf("claude-code: read the execution file: %w", err)
	}
	if opening != json.Delim('[') {
		return executionSpan{}, errors.New("claude-code: the execution file is not a JSON array of messages")
	}
	var span executionSpan
	for decoder.More() {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return executionSpan{}, fmt.Errorf("claude-code: read the execution file: %w", err)
		}
		var message executionMessage
		if err := json.Unmarshal(raw, &message); err != nil {
			return executionSpan{}, fmt.Errorf("claude-code: read the execution file: %w", err)
		}
		span.add(message)
		if err := session.Feed(raw, saved); err != nil {
			return executionSpan{}, err
		}
	}
	return span, nil
}
