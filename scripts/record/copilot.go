package main

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

const (
	agentCopilot = "copilot"
	// copilotEvents is the name a session's events.jsonl takes in a
	// recording.
	copilotEvents = "events.jsonl"
	// copilotSessions is the directory under COPILOT_HOME that holds one
	// directory per session.
	copilotSessions = "session-state"
)

// collectCopilot copies the events.jsonl of the session the stream's result
// line names, or, with a home made for the run and no stream, of the one
// session the home holds. Nothing else under the home is copied: the session
// store is a SQLite file of the session's content, and the other files name
// the working directory.
func collectCopilot(e *agentEntry, c *collected, src source, stream, _ []byte) error {
	session := firstField(stream, func(line *value) bool { return line.fieldText("type") == "result" }, "sessionId")
	if session == "" && src.freshHome {
		found, err := fs.Glob(src.home, path.Join(copilotSessions, "*", copilotEvents))
		if err != nil {
			return err
		}
		if len(found) != 1 {
			return fmt.Errorf("the home holds %d sessions, so the run's cannot be told: give the run's stream", len(found))
		}
		session = path.Base(path.Dir(found[0]))
	}
	if session == "" {
		return fmt.Errorf("no result line names the session: run %s, or collect from a home made for this run", e.stream)
	}
	if !safeWord.MatchString(session) {
		return fmt.Errorf("the session id %q is not a plain word", session)
	}
	err := copyFile(c, src.home, path.Join(copilotSessions, session, copilotEvents), copilotEvents)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("no %s for the run's session under %s/ in the agent's directory", copilotEvents, copilotSessions)
	}
	return err
}

// copilotVersion is the copilotVersion that session.start states.
func copilotVersion(files []*recordingFile) string {
	for _, file := range files {
		for _, line := range file.lines {
			if line.fieldText("type") == "session.start" {
				if version := line.field("data").fieldText("copilotVersion"); version != "" {
					return version
				}
			}
		}
	}
	return ""
}

// copilotTotal is Copilot CLI's own figures for the run: each model's tokens
// and Copilot's credits, as session.shutdown states them.
func copilotTotal(files []*recordingFile) string {
	shutdown := lastLine(files, copilotEvents, func(v *value) bool { return v.fieldText("type") == "session.shutdown" })
	if shutdown == nil {
		return "No session.shutdown line states the run's own total."
	}
	data := shutdown.field("data")
	var models []string
	if metrics := data.field("modelMetrics"); metrics != nil && metrics.kind == kindObject {
		for i, model := range metrics.keys {
			models = append(models, fmt.Sprintf("`%s` %s", model, copilotCounts(metrics.items[i].field("usage"))))
		}
	}
	if len(models) == 0 {
		models = append(models, "no model")
	}
	credits := "no `totalNanoAiu`"
	if total := data.field("totalNanoAiu"); total != nil && total.isNumber() {
		credits = "a `totalNanoAiu` of " + total.raw
	}
	return fmt.Sprintf("The run's own total, the `modelMetrics` of `session.shutdown` in `%s`: %s. Copilot states %s, its own unit, which Costroid does not convert.",
		copilotEvents, strings.Join(models, "; "), credits)
}

func copilotCounts(usage *value) string {
	var parts []string
	for _, field := range []struct{ key, words string }{
		{"inputTokens", "input, cache included"},
		{"cacheReadTokens", "cache read"},
		{"cacheWriteTokens", "cache write"},
		{"outputTokens", "output"},
		{"reasoningTokens", "reasoning"},
	} {
		if count := usage.field(field.key); count != nil && count.isNumber() {
			parts = append(parts, count.raw+" "+field.words)
		}
	}
	if len(parts) == 0 {
		return "none stated"
	}
	return strings.Join(parts, ", ") + " tokens"
}
