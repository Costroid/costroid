package claudecode

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/focus"
)

// fileFacts is what one session file says about the session that wrote it.
type fileFacts struct {
	sessions    map[string]bool
	sidechain   bool
	cwd         string
	version     string
	first, last time.Time
}

func (f *fileFacts) observe(line sessionLine) {
	if line.SessionID != "" {
		f.sessions[line.SessionID] = true
	}
	f.sidechain = f.sidechain || line.IsSidechain
	if f.cwd == "" {
		f.cwd = line.CWD
	}
	if f.version == "" {
		f.version = line.Version
	}
	at, err := time.Parse(time.RFC3339Nano, line.Timestamp)
	if err != nil {
		return
	}
	if f.first.IsZero() || at.Before(f.first) {
		f.first = at
	}
	if at.After(f.last) {
		f.last = at
	}
}

// RunWindow records when the agent's process ran. Without it, the window is
// the span of the stream lines the session was fed.
func (s *Session) RunWindow(start, end time.Time) {
	s.ranFrom, s.ranTo = start, end
}

func (s *Session) window() (time.Time, time.Time) {
	if s.ranFrom.IsZero() {
		return s.first, s.last
	}
	return s.ranFrom, s.ranTo
}

// runCWD is where the run started: the stream's init line, then the main
// session file.
func (s *Session) runCWD(run transcript) string {
	if s.cwd != "" {
		return s.cwd
	}
	return run.cwd
}

// nestedSessions reads the other sessions in the run's project directory. A
// hook or a plugin that starts its own claude -p leaves one there, under its
// own session id. One counts when every rule holds:
//
//   - no line names the run's session id or is a subagent's, because those
//     lines are the run's own record;
//   - its first and last events fall inside the run's window;
//   - the first working directory it states is the run's.
//
// A request id the run or an earlier nested session already holds is not
// counted again. A session that started inside the window and fails a later
// rule is named in a warning.
func (s *Session) nestedSessions(run transcript) ([]adapter.Report, []string) {
	if run.path == "" {
		return nil, nil
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(run.path), "*.jsonl"))
	if err != nil {
		return nil, []string{fmt.Sprintf("find nested sessions: %v", err)}
	}
	start, _ := s.window()
	counted := map[string]bool{}
	for _, request := range run.requests {
		counted[request.ID] = true
	}
	var reports []adapter.Report
	var warnings []string
	for _, file := range files {
		id := strings.TrimSuffix(filepath.Base(file), ".jsonl")
		if file == run.path || !sessionIDPattern.MatchString(id) || writtenBefore(file, start) {
			continue
		}
		report, warning := s.nestedSession(file, id, s.runCWD(run), counted)
		if warning != "" {
			warnings = append(warnings, warning)
		}
		if len(report.Requests) > 0 {
			reports = append(reports, report)
		}
	}
	return reports, warnings
}

// writtenBefore skips a file last written well before the run began without
// reading it: its events are older still. A file's time can trail the clock
// its events were stamped by, so the test allows a minute. A file that cannot
// be stated is read, so its error is reported.
func writtenBefore(file string, start time.Time) bool {
	info, err := os.Stat(file)
	return err == nil && info.ModTime().Before(start.Add(-time.Minute))
}

// nestedSession reads one candidate and its subagents. It returns no
// requests when a rule fails, and a warning when the session started inside
// the window.
func (s *Session) nestedSession(file, id, cwd string, counted map[string]bool) (adapter.Report, string) {
	start, end := s.window()
	builder := newRequestBuilder(false)
	facts, err := builder.read(file)
	if err != nil {
		return adapter.Report{}, fmt.Sprintf("session %s was not counted: %v", id, err)
	}
	if facts.sessions[s.sessionID] || facts.sidechain {
		return adapter.Report{}, ""
	}
	if facts.first.IsZero() || facts.first.Before(start) || facts.first.After(end) {
		return adapter.Report{}, ""
	}
	last, err := readSubagents(builder, file, facts.last)
	switch {
	case err != nil:
		return adapter.Report{}, fmt.Sprintf("session %s was not counted: %v", id, err)
	case last.After(end):
		return adapter.Report{}, fmt.Sprintf("session %s started inside the run and wrote after it ended, so it was not counted", id)
	case cwd == "":
		return adapter.Report{}, fmt.Sprintf("session %s started inside the run, which states no working directory, so it was not counted", id)
	case facts.cwd != cwd:
		return adapter.Report{}, fmt.Sprintf("session %s started inside the run in another working directory, so it was not counted", id)
	}
	report := adapter.Report{
		SessionID:    id,
		AgentVersion: facts.version,
		CostBasis:    focus.ListEquivalent,
		CostSource:   SourceSessionFile,
	}
	for _, request := range builder.requests() {
		if !counted[request.ID] {
			counted[request.ID] = true
			report.Requests = append(report.Requests, request)
		}
	}
	return report, ""
}

// readSubagents adds a nested session's subagent files and returns the last
// event across them and the session's own file.
func readSubagents(builder *requestBuilder, file string, last time.Time) (time.Time, error) {
	files, err := filepath.Glob(filepath.Join(strings.TrimSuffix(file, ".jsonl"), "subagents", "*.jsonl"))
	if err != nil {
		return last, fmt.Errorf("claude-code: find subagent files: %w", err)
	}
	for _, path := range files {
		facts, err := builder.read(path)
		if err != nil {
			return last, err
		}
		if facts.last.After(last) {
			last = facts.last
		}
	}
	return last, nil
}
