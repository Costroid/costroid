package claudecode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/decimal"
)

// SourceExecutionFile marks usage read from the execution_file output of
// claude-code-action.
const SourceExecutionFile = "execution_file"

// executionMessage is what the reader wants from one message besides usage.
// Timestamp and Duration only place the run in time, and the rest only say
// how a result ended it, so each is read leniently: a form the reader does
// not know leaves the run's window to the others, and its end unproven.
type executionMessage struct {
	Timestamp json.RawMessage `json:"timestamp"`
	Duration  json.RawMessage `json:"duration_ms"`
	Type      json.RawMessage `json:"type"`
	Subtype   json.RawMessage `json:"subtype"`
	Errors    json.RawMessage `json:"errors"`
	TotalCost json.RawMessage `json:"total_cost_usd"`
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
// start this run and is told no cap, so the grade is none.
func (a Agent) ReadExecutionFile(file io.Reader, saved time.Time) (adapter.Report, error) {
	return a.ReadCappedExecutionFile(file, saved, Cap{})
}

// ReadCappedExecutionFile reads the file as ReadExecutionFile does, and
// grades the run by the cap the workflow states and what the file shows.
func (a Agent) ReadCappedExecutionFile(file io.Reader, saved time.Time, stated Cap) (adapter.Report, error) {
	session := a.newSession()
	span, end, err := feedExecutionFile(session, file, saved)
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
	report.Enforcement = stated.grade(end)
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
func feedExecutionFile(session *Session, file io.Reader, saved time.Time) (executionSpan, runEnding, error) {
	source := &endReader{reader: file}
	decoder := json.NewDecoder(source)
	opening, err := decoder.Token()
	if err != nil {
		return executionSpan{}, runEnding{}, fmt.Errorf("claude-code: read the execution file: %w", err)
	}
	if opening != json.Delim('[') {
		return executionSpan{}, runEnding{}, errors.New("claude-code: the execution file is not a JSON array of messages")
	}
	var span executionSpan
	var end runEnding
	for decoder.More() {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return executionSpan{}, runEnding{}, executionError(err, decoder, source)
		}
		if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '{' {
			span.notMessages++
			continue
		}
		var message executionMessage
		if err := json.Unmarshal(raw, &message); err != nil {
			return executionSpan{}, runEnding{}, fmt.Errorf("claude-code: read the execution file: %w", err)
		}
		span.add(message)
		end.read(message)
		if err := session.Feed(raw, saved); err != nil {
			return executionSpan{}, runEnding{}, err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return executionSpan{}, runEnding{}, executionError(err, decoder, source)
	}
	return span, end, nil
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

// budgetStopSubtype is the result subtype of a run Claude Code stopped at
// its --max-budget-usd.
const budgetStopSubtype = "error_max_budget_usd"

// budgetStopLine is the error a budget stop states, with the cap as Claude
// Code was given it: "Reached maximum budget ($0.0001)" in 2.1.316
// (recording action-budget-stop).
var budgetStopLine = regexp.MustCompile(`^Reached maximum budget \(\$([0-9]+(?:\.[0-9]+)?)\)$`)

// runEnding is what the last result says about how the run ended.
type runEnding struct {
	budgetStop bool
	// stoppedAt is the cap a budget stop names, nil when it names none.
	stoppedAt *decimal.Decimal
	// total is the run's own total_cost_usd, Claude Code's estimate at list
	// price, nil when the result states none.
	total *decimal.Decimal
}

// read keeps what a result says. A later result replaces an earlier one, as
// its per-model totals do.
func (e *runEnding) read(message executionMessage) {
	if kind, _ := jsonString(message.Type); kind != "result" {
		return
	}
	subtype, _ := jsonString(message.Subtype)
	*e = runEnding{budgetStop: subtype == budgetStopSubtype, total: statedUSD(message.TotalCost)}
	if e.budgetStop {
		e.stoppedAt = stoppedAt(message.Errors)
	}
}

// statedUSD reads a figure Claude Code wrote as a JSON number, exactly. Any
// other value, or a negative one, states nothing.
func statedUSD(raw json.RawMessage) *decimal.Decimal {
	if len(raw) == 0 || raw[0] == '"' {
		return nil
	}
	value, err := decimal.ParseNumber(string(raw))
	if err != nil || value.Sign() < 0 {
		return nil
	}
	return &value
}

// stoppedAt reads the cap from a budget stop's errors.
func stoppedAt(raw json.RawMessage) *decimal.Decimal {
	var lines []json.RawMessage
	if json.Unmarshal(raw, &lines) != nil {
		return nil
	}
	for _, line := range lines {
		text, _ := jsonString(line)
		if match := budgetStopLine.FindStringSubmatch(text); match != nil {
			value, err := decimal.Parse(match[1])
			if err == nil {
				return &value
			}
		}
	}
	return nil
}

// Cap is what a workflow states about the cap a claude-code-action run was
// held to. The read stage never sees the step's inputs, so both are the
// workflow's own statements.
type Cap struct {
	// USD is the cap the read stage was given. Nil when it was given none.
	USD *decimal.Decimal
	// Args is the claude_args the step was given, as the read stage was told
	// them. Empty when it was told none.
	Args string
}

// grade holds the run to the stated cap. The grade is agent-flag when Claude
// Code's --max-budget-usd equals the cap and the run either stopped at it or
// ended under it by its own total; otherwise none, with the reason.
func (c Cap) grade(end runEnding) adapter.Enforcement {
	none := func(format string, args ...any) adapter.Enforcement {
		return adapter.Enforcement{Grade: adapter.GradeNone, Note: "none: " + fmt.Sprintf(format, args...)}
	}
	if c.USD == nil {
		if end.budgetStop {
			return none("the run stopped at its own --max-budget-usd, and no cap was stated to hold it to")
		}
		return none("the run was not started by costroid, and the file states no budget")
	}
	flag, reason := c.flag(end)
	switch {
	case reason != "":
		return none("%s", reason)
	case flag.Cmp(*c.USD) != 0:
		return none("the stated cap is USD %s and --max-budget-usd is USD %s; the cap and the flag differ", c.USD, flag)
	case end.budgetStop:
		// Claude Code stopped the run at the cap.
	case end.total == nil:
		return none("the run did not stop at its budget, and its result states no total_cost_usd, so nothing shows it ended under USD %s", c.USD)
	case end.total.Cmp(*c.USD) > 0:
		return none("the run's own total of USD %s passed USD %s without a budget stop, so the flag did not hold it", end.total, c.USD)
	}
	return adapter.Enforcement{Grade: adapter.GradeAgentFlag, Note: enforcementNote}
}

// flag is the --max-budget-usd that held the run: the one a budget stop
// names, or else the one claude_args states. reason says why there is none.
func (c Cap) flag(end runEnding) (decimal.Decimal, string) {
	if end.stoppedAt != nil {
		return *end.stoppedAt, ""
	}
	value, stated, err := budgetFlag(c.Args)
	switch {
	case err != nil:
		return decimal.Decimal{}, fmt.Sprintf("the stated claude_args cannot be read: %v", err)
	case !stated && end.budgetStop:
		return decimal.Decimal{}, "the run stopped at its budget, and neither its result nor the stated claude_args give the figure"
	case !stated && strings.TrimSpace(c.Args) == "":
		return decimal.Decimal{}, fmt.Sprintf("the stated cap is USD %s, and no claude_args were stated, so nothing shows a flag held the run", c.USD)
	case !stated:
		return decimal.Decimal{}, fmt.Sprintf("the stated cap is USD %s, and the stated claude_args hold no --max-budget-usd, so nothing shows a flag held the run", c.USD)
	}
	flag, err := decimal.Parse(value)
	if err != nil {
		return decimal.Decimal{}, fmt.Sprintf("--max-budget-usd reads %q in the stated claude_args, which is not a plain decimal", value)
	}
	return flag, ""
}

const budgetFlagName = "--max-budget-usd"

// budgetFlag finds --max-budget-usd in claude_args as claude-code-action
// reads them: lines that start with # are dropped, the rest is split as a
// shell splits words, and the last value given wins (base-action
// parse-sdk-options.ts at v1.0.248).
func budgetFlag(args string) (string, bool, error) {
	words, err := shellWords(dropCommentLines(args))
	if err != nil {
		return "", false, err
	}
	value, stated := "", false
	for i, word := range words {
		if given, ok := strings.CutPrefix(word, budgetFlagName+"="); ok {
			value, stated = given, true
			continue
		}
		if word != budgetFlagName {
			continue
		}
		value, stated = "", true
		if i+1 < len(words) && !strings.HasPrefix(words[i+1], "--") {
			value = words[i+1]
		}
	}
	return value, stated, nil
}

func dropCommentLines(text string) string {
	lines := strings.Split(text, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// shellWords splits text into words as a POSIX shell does, with single and
// double quotes and backslashes, and without expansion.
func shellWords(text string) ([]string, error) {
	var words []string
	var word strings.Builder
	inWord := false
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case c == '\\' && i+1 < len(text):
			i++
			word.WriteByte(text[i])
		case c == '\'' || c == '"':
			end := closingQuote(text, i)
			if end < 0 {
				return nil, fmt.Errorf("a %c quote is not closed", c)
			}
			word.WriteString(unquote(text[i+1:end], c))
			i = end
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if inWord {
				words = append(words, word.String())
				word.Reset()
			}
			inWord = false
			continue
		default:
			word.WriteByte(c)
		}
		inWord = true
	}
	if inWord {
		words = append(words, word.String())
	}
	return words, nil
}

// closingQuote finds the quote that closes the one at open, or -1. Inside
// double quotes a backslash escapes the next byte.
func closingQuote(text string, open int) int {
	quote := text[open]
	for i := open + 1; i < len(text); i++ {
		switch {
		case quote == '"' && text[i] == '\\':
			i++
		case text[i] == quote:
			return i
		}
	}
	return -1
}

// unquote reads a quoted span: single quotes keep every byte, and double
// quotes drop the backslash before ", \, $ and `.
func unquote(span string, quote byte) string {
	if quote == '\'' {
		return span
	}
	var out strings.Builder
	for i := 0; i < len(span); i++ {
		if span[i] == '\\' && i+1 < len(span) && strings.IndexByte("\"\\$`", span[i+1]) >= 0 {
			i++
		}
		out.WriteByte(span[i])
	}
	return out.String()
}
