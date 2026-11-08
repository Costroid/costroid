package claudecode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// resultMessage is the part of a result message that holds the run's answer.
// The other messages carry what Claude Code wrote and read along the way, and
// the answer readers never keep them.
type resultMessage struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype"`
	Result  json.RawMessage `json:"result"`
}

// subtypeName is the form of a result subtype a log line can quote.
var subtypeName = regexp.MustCompile(`^[a-z_]{1,64}$`)

// errNoResult is the answer readers' error for a run with no result message.
var errNoResult = errors.New("claude-code: the run holds no result message, so it ended before Claude Code answered")

// ReadStreamAnswer returns the answer a claude -p run ended with: the result
// field of the last result message on its stream-json output. Lines that are
// not JSON objects are passed over, and so is every message but the result.
func ReadStreamAnswer(stream io.Reader) (string, error) {
	reader := bufio.NewReader(stream)
	var last *resultMessage
	for {
		line, err := reader.ReadBytes('\n')
		if message, ok := readResult(line); ok {
			last = message
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("claude-code: read the stream: %w", err)
		}
	}
	return answerOf(last)
}

// ReadExecutionAnswer returns the answer a claude-code-action run ended with:
// the result field of the last result message in its execution file, the
// array of SDK messages the action wrote.
func ReadExecutionAnswer(file io.Reader) (string, error) {
	decoder := json.NewDecoder(file)
	if token, err := decoder.Token(); err != nil || token != json.Delim('[') {
		return "", errors.New("claude-code: the execution file does not hold an array of messages")
	}
	var last *resultMessage
	for decoder.More() {
		var element json.RawMessage
		if err := decoder.Decode(&element); err != nil {
			return "", fmt.Errorf("claude-code: read the execution file: %w", err)
		}
		if message, ok := readResult(element); ok {
			last = message
		}
	}
	return answerOf(last)
}

// readResult decodes one message and keeps it only when it is a result.
func readResult(data []byte) (*resultMessage, bool) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return nil, false
	}
	var message resultMessage
	if json.Unmarshal(data, &message) != nil || message.Type != "result" {
		return nil, false
	}
	return &message, true
}

// answerOf reads the answer from the run's last result. A run that stopped at
// a cap or on an error ends with a result that holds no answer.
func answerOf(message *resultMessage) (string, error) {
	if message == nil {
		return "", errNoResult
	}
	if message.Subtype != "success" {
		subtype := message.Subtype
		if !subtypeName.MatchString(subtype) {
			subtype = "a subtype other than success"
		}
		return "", fmt.Errorf("claude-code: the run ended with %s, so it has no answer", subtype)
	}
	var text string
	if json.Unmarshal(message.Result, &text) != nil {
		return "", errors.New("claude-code: the run's result message holds no result text")
	}
	if strings.TrimSpace(text) == "" {
		return "", errors.New("claude-code: the run's answer is empty")
	}
	return text, nil
}
