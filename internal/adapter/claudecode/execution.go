package claudecode

import (
	"bytes"
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
// Both fields only place the run in time, so each is read leniently and a
// form the reader does not know leaves the run's window to the others.
type executionMessage struct {
	Timestamp json.RawMessage `json:"timestamp"`
	Duration  json.RawMessage `json:"duration_ms"`
}

// executionSpan is when a run took place.
type executionSpan struct {
	first, last time.Time
	duration    time.Duration
	// notMessages counts the array's elements that are not objects.
	notMessages int
	// untimed counts the messages whose timestamp is not RFC 3339 text.
	untimed int
}

func (s *executionSpan) add(message executionMessage) {
	if duration, ok := milliseconds(message.Duration); ok && duration > 0 {
		s.duration = duration
	}
	if len(message.Timestamp) == 0 || string(message.Timestamp) == "null" {
		return
	}
	stamp, _ := jsonString(message.Timestamp)
	at, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		s.untimed++
		return
	}
	if s.first.IsZero() || at.Before(s.first) {
		s.first = at
	}
	if at.After(s.last) {
		s.last = at
	}
}

// milliseconds reads a duration stated as a number of milliseconds, or as
// that number in a string. A part of a nanosecond is dropped.
func milliseconds(raw json.RawMessage) (time.Duration, bool) {
	number, ok := readNumeral(raw)
	if !ok {
		return 0, false
	}
	number.exponent += 6
	if number.exponent < 0 {
		cut := min(-number.exponent, int64(len(number.digits)))
		number.digits, number.exponent = number.digits[:int64(len(number.digits))-cut], 0
	}
	nanoseconds, err := number.integer()
	if err != nil {
		return 0, false
	}
	return time.Duration(nanoseconds), true
}

// warnings says what the reader passed over.
func (s *executionSpan) warnings() []string {
	var warnings []string
	if s.notMessages > 0 {
		warnings = append(warnings, fmt.Sprintf("the execution file held %d %s that %s not a message, so %s skipped",
			s.notMessages, plural(s.notMessages, "element", "elements"), plural(s.notMessages, "is", "are"), plural(s.notMessages, "it was", "they were")))
	}
	if s.untimed > 0 {
		warnings = append(warnings, fmt.Sprintf("the execution file held %d %s whose timestamp is not RFC 3339 text, so the run's window comes from the others",
			s.untimed, plural(s.untimed, "message", "messages")))
	}
	return warnings
}

func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
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
	session := a.newSession()
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
	report.Warnings = append(report.Warnings, span.warnings()...)
	report.Enforcement = adapter.Enforcement{Grade: adapter.GradeNone, Note: unwatchedNote}
	if report.CostSource == SourceStream {
		report.CostSource = SourceExecutionFile
	}
	for i := range report.Requests {
		if report.Requests[i].CostSource == SourceStream {
			report.Requests[i].CostSource = SourceExecutionFile
		}
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

// errTruncated is an execution file cut off before its array closes.
var errTruncated = errors.New("claude-code: the execution file ends inside its array; a later result may be missing, so no rows were written")

// feedExecutionFile feeds every message to the session. An element that is
// not an object is skipped and counted. A file cut off inside its array is
// refused, because the run's last result may be the part that is missing.
func feedExecutionFile(session *Session, file io.Reader, saved time.Time) (executionSpan, error) {
	source := &endReader{reader: file}
	decoder := json.NewDecoder(source)
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
			return executionSpan{}, executionError(err, decoder, source)
		}
		if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '{' {
			span.notMessages++
			continue
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
	if _, err := decoder.Token(); err != nil {
		return executionSpan{}, executionError(err, decoder, source)
	}
	return span, nil
}

// endReader notes when its reader has no more to give.
type endReader struct {
	reader io.Reader
	ended  bool
}

func (r *endReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if errors.Is(err, io.EOF) {
		r.ended = true
	}
	return n, err
}

// executionError names a file that ends inside its array: the input ran out,
// and nothing but separators was left unread. Any other error is the file's
// own syntax.
func executionError(err error, decoder *json.Decoder, source *endReader) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return errTruncated
	}
	rest, readErr := io.ReadAll(decoder.Buffered())
	if readErr == nil && source.ended && len(bytes.Trim(rest, " \t\r\n,")) == 0 {
		return errTruncated
	}
	return fmt.Errorf("claude-code: read the execution file: %w", err)
}
