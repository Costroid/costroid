// Package reason holds the words a run states about itself: why it wrote no
// rows, what to do about the error its agent ended in, and the reader's error
// line. The runner writes them and a comment shows them, so they live apart
// from both.
package reason

import (
	"fmt"
	"strings"

	"github.com/Costroid/costroid/internal/adapter"
)

// Reason says why a run wrote no rows. Its values are a closed list, each
// with one sentence, so a reader of an empty rows file can name the cause.
type Reason string

// The reasons a run writes no rows.
const (
	// NoUsage is a run whose agent ended and reported no usage.
	NoUsage Reason = "no_usage"
	// ConcurrentRun is a run refused before its agent started, because
	// another run held the agent's state directory.
	ConcurrentRun Reason = "concurrent_run"
	// ReaderRefused is a run whose reader refused the agent's stream or
	// its execution file, so no usage was read.
	ReaderRefused Reason = "reader_refused"
	// AgentError is a run whose agent ended in an error before it
	// reported any usage. The run's result names the error's class.
	AgentError Reason = "agent_error"
)

var reasonSentences = map[Reason]string{
	NoUsage:       "the agent reported no usage, so the run wrote no rows",
	ConcurrentRun: "another costroid run holds the agent's state directory, so this run did not start and wrote no rows",
	ReaderRefused: "the reader refused the agent's stream, so the run wrote no rows",
	AgentError:    "the agent ended in an error before it reported usage, so the run wrote no rows",
}

// Sentence is the reason in words.
func (r Reason) Sentence() string {
	return reasonSentences[r]
}

// agentErrorFixes says what to do about each class of agent error.
var agentErrorFixes = map[adapter.AgentError]string{
	adapter.AgentErrorAuthentication: "check the API key the agent runs with, the api-key input in the action",
	adapter.AgentErrorBilling:        "check the credit balance of the provider account the API key belongs to",
	adapter.AgentErrorRateLimit:      "run the job again later, or raise the API key's rate limit with the provider",
	adapter.AgentErrorInvalidRequest: "check the model name and the arguments the agent runs with",
	adapter.AgentErrorServer:         "run the job again, because the provider failed the request",
	adapter.AgentErrorUnknown:        "run the agent alone with the same arguments to read its error",
}

// AgentErrorFix says what to do about an agent error of the class given,
// empty for a class outside the closed list.
func AgentErrorFix(class adapter.AgentError) string {
	return agentErrorFixes[class]
}

// AgentErrorLine is an agent error's class and its fix, in one line.
func AgentErrorLine(class adapter.AgentError) string {
	return fmt.Sprintf("the agent ended with %s: %s", class, AgentErrorFix(class))
}

// maxReaderLine bounds the reader's error line a result object carries.
const maxReaderLine = 160

// readerLineCharacters is the allowlist of a reader's error line. It holds no
// character that can close a code span, open a tag or start a mention.
const readerLineCharacters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 .,:;()'=_/+-"

// ReaderLine is the first line of a reader's error, as a result object and a
// comment carry it. The error can quote the agent's stream, so every character
// outside readerLineCharacters becomes ?, and the line is cut at maxReaderLine
// bytes.
func ReaderLine(text string) string {
	first, _, _ := strings.Cut(text, "\n")
	cleaned := strings.Map(func(r rune) rune {
		if strings.ContainsRune(readerLineCharacters, r) {
			return r
		}
		return '?'
	}, strings.TrimSpace(first))
	if len(cleaned) > maxReaderLine {
		cleaned = cleaned[:maxReaderLine]
	}
	return cleaned
}
