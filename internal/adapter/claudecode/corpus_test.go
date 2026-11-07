package claudecode

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

// corpusManifest is the part of a recording's manifest.json this test reads.
// scripts/record writes and checks the whole of it.
type corpusManifest struct {
	Recorded bool   `json:"recorded"`
	Agent    string `json:"agent"`
	Version  string `json:"version"`
	Scenario string `json:"scenario"`
}

// corpusRead is what one recording reads as. Warnings holds a part of each
// warning, in order.
type corpusRead struct {
	source   string
	basis    focus.CostBasis
	requests int
	tokens   adapter.Tokens
	grade    string
	warnings []string
}

// claudeCorpus pins what every recording under testdata reads as, by its
// directory below testdata. TestClaudeCodeCorpus also checks each recording
// against the result it states, which needs no entry here.
var claudeCorpus = map[string]corpusRead{
	"2.1.316/one-request": {
		source: SourceSessionFile, basis: focus.ListEquivalent, requests: 1, grade: adapter.GradeAgentFlag,
		tokens: adapter.Tokens{InputUncached: 2, CacheRead: 649, CacheWrite1h: 2986, Output: 4, CachedInputReported: 649},
	},
	"2.1.316/subagent": {
		source: SourceSessionFile, basis: focus.ListEquivalent, requests: 4, grade: adapter.GradeAgentFlag,
		tokens: adapter.Tokens{InputUncached: 8, CacheRead: 15605, CacheWrite5m: 3495, CacheWrite1h: 4450, Output: 272, ReasoningOutput: 121, CachedInputReported: 15605},
	},
	"2.1.316/budget-stop": {
		source: SourceSessionFile, basis: focus.ListEquivalent, requests: 1, grade: adapter.GradeAgentFlag,
		tokens: adapter.Tokens{InputUncached: 2, CacheRead: 2290, CacheWrite1h: 3279, Output: 107, ReasoningOutput: 29, CachedInputReported: 2290},
	},
	"2.1.316/execution-file": {
		source: SourceExecutionFile, basis: focus.Measured, requests: 2, grade: adapter.GradeNone,
		tokens:   adapter.Tokens{InputUncached: 1227, CacheRead: 66419, CacheWrite5m: 5181, Output: 634, ReasoningOutput: 35, CachedInputReported: 66419},
		warnings: []string{"no session file: one set of rows per model, for the whole run"},
	},
	"2.1.316/action-session": {
		source: SourceSessionFile, basis: focus.Measured, requests: 3, grade: adapter.GradeNone,
		tokens: adapter.Tokens{InputUncached: 1204, CacheRead: 28767, CacheWrite5m: 5959, Output: 190, CachedInputReported: 28767},
	},
	"2.1.319/current": {
		source: SourceSessionFile, basis: focus.Measured, requests: 1, grade: adapter.GradeAgentFlag,
		tokens: adapter.Tokens{InputUncached: 2, CacheRead: 11507, CacheWrite5m: 6085, Output: 4, CachedInputReported: 11507},
	},
}

// corpusSaved is when the corpus reads an execution file as saved.
var corpusSaved = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

// recordingsIn lists every directory under root that a manifest.json marks,
// by its path below root. Written fixtures carry no manifest.
func recordingsIn(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || entry.Name() != "manifest.json" {
			return err
		}
		dir, err := filepath.Rel(root, filepath.Dir(path))
		found = append(found, filepath.ToSlash(dir))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// recordedDirectories lists every <version>/<scenario> directory under
// testdata, which is where a recording sits whether or not it has a manifest.
func recordedDirectories(t *testing.T) []string {
	t.Helper()
	versions, err := filepath.Glob(filepath.Join("testdata", "[0-9]*", "*"))
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, path := range versions {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			rel, _ := filepath.Rel("testdata", path)
			found = append(found, filepath.ToSlash(rel))
		}
	}
	return found
}

func readCorpusManifest(t *testing.T, dir string) corpusManifest {
	t.Helper()
	var manifest corpusManifest
	if err := json.Unmarshal(mustReadFile(t, filepath.Join(dir, "manifest.json")), &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

// initSession is the session id of the first system init message among
// messages, which are stream lines or the elements of an execution file.
func initSession(messages []json.RawMessage) string {
	for _, raw := range messages {
		var message struct {
			Type      string `json:"type"`
			Subtype   string `json:"subtype"`
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(raw, &message) == nil && message.Type == "system" && message.Subtype == "init" {
			return message.SessionID
		}
	}
	return ""
}

// streamLines reads a stream file's lines, or nil when the recording has none.
func streamLines(t *testing.T, dir string) []json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "stream.jsonl"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var lines []json.RawMessage
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		lines = append(lines, json.RawMessage(line))
	}
	return lines
}

// executionMessages reads an execution file's messages, or nil when the
// recording has none.
func executionMessages(t *testing.T, dir string) []json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "claude-execution-output.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(data, &messages); err != nil {
		t.Fatal(err)
	}
	return messages
}

// readRecording reads a recording as a run of Claude Code would leave it: its
// session files under a config directory, and then its execution file, or
// its stream line by line. It returns the report and the grade the run
// earned, which is the plan's unless the report states its own.
func readRecording(t *testing.T, dir string) (adapter.Report, string) {
	t.Helper()
	outsideCI(t)
	t.Setenv(oneHourCacheVariable, "")
	stream, execution := streamLines(t, dir), executionMessages(t, dir)
	sessionID := initSession(append(slices.Clone(stream), execution...))
	if sessionID == "" {
		t.Fatal("the recording holds no system init message")
	}
	config := t.TempDir()
	if _, err := os.Stat(filepath.Join(dir, "session.jsonl")); err == nil {
		config = installFrom(t, dir, sessionID)
	}
	agent := Agent{ConfigDir: config}
	plan, err := agent.Plan([]string{"claude", "-p", "work"}, decimal.MustParse("5"))
	if err != nil {
		t.Fatal(err)
	}
	var report adapter.Report
	switch {
	case execution != nil:
		file, err := os.Open(filepath.Join(dir, "claude-execution-output.json"))
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		report, err = agent.ReadExecutionFile(file, corpusSaved)
		if err != nil {
			t.Fatal(err)
		}
	case stream != nil:
		session := agent.NewSession(plan)
		for _, line := range stream {
			if err := session.Feed(line, recorded); err != nil {
				t.Fatal(err)
			}
		}
		report, err = session.Finish()
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("the recording holds neither a stream nor an execution file")
	}
	grade := plan.Enforcement.Grade
	if report.Enforcement.Grade != "" {
		grade = report.Enforcement.Grade
	}
	return report, grade
}

// statedTotals sums, over every model, the per-model totals of the last
// result message, read without the adapter: the counts the run states.
func statedTotals(t *testing.T, messages []json.RawMessage) (adapter.Tokens, int64) {
	t.Helper()
	var last map[string]providerTotals
	for _, raw := range messages {
		var result struct {
			Type       string                    `json:"type"`
			ModelUsage map[string]providerTotals `json:"modelUsage"`
		}
		if json.Unmarshal(raw, &result) == nil && result.Type == "result" {
			last = result.ModelUsage
		}
	}
	if last == nil {
		t.Fatal("no result message states per-model totals")
	}
	var tokens adapter.Tokens
	var written int64
	for _, totals := range last {
		tokens = tokens.Add(adapter.Tokens{
			InputUncached: totals.InputTokens, CacheRead: totals.CacheRead, Output: totals.OutputTokens,
			ReasoningOutput: totals.Thinking, CachedInputReported: totals.CacheRead,
		})
		written += totals.CacheCreated
	}
	return tokens, written
}

// TestClaudeCodeCorpus reads every recording a manifest.json marks under
// testdata. Each reads as its entry in claudeCorpus states, and its requests
// add up to the per-model totals of the run's last result. A recording with
// no manifest, or a manifest with no entry, fails.
func TestClaudeCodeCorpus(t *testing.T) {
	recordings := recordingsIn(t, "testdata")
	for _, dir := range recordedDirectories(t) {
		if !slices.Contains(recordings, dir) {
			t.Errorf("testdata/%s holds a recording and no manifest.json", dir)
		}
	}
	for dir := range claudeCorpus {
		if !slices.Contains(recordings, dir) {
			t.Errorf("claudeCorpus names testdata/%s, which no manifest marks", dir)
		}
	}
	for _, dir := range recordings {
		t.Run(dir, func(t *testing.T) {
			path := filepath.Join("testdata", filepath.FromSlash(dir))
			manifest := readCorpusManifest(t, path)
			if !manifest.Recorded || manifest.Agent != Name || manifest.Scenario == "" || !strings.HasPrefix(dir, manifest.Version+"/") {
				t.Errorf("manifest %+v does not name the version of its directory", manifest)
			}
			want, ok := claudeCorpus[dir]
			if !ok {
				t.Fatalf("no entry in claudeCorpus for testdata/%s", dir)
			}
			report, grade := readRecording(t, path)
			got := corpusRead{source: report.CostSource, basis: report.CostBasis, requests: len(report.Requests), tokens: sum(report.Requests), grade: grade}
			if report.AgentVersion != manifest.Version {
				t.Errorf("agent version %q, the manifest says %q", report.AgentVersion, manifest.Version)
			}
			if got.source != want.source || got.basis != want.basis || got.requests != want.requests || got.grade != want.grade {
				t.Errorf("read %s, %s, %d requests, grade %s; want %s, %s, %d, %s", got.source, got.basis, got.requests, got.grade, want.source, want.basis, want.requests, want.grade)
			}
			if got.tokens != want.tokens {
				t.Errorf("tokens %+v, want %+v", got.tokens, want.tokens)
			}
			if len(report.Warnings) != len(want.warnings) {
				t.Errorf("warnings %q, want %d", report.Warnings, len(want.warnings))
			}
			for i := range min(len(report.Warnings), len(want.warnings)) {
				if !strings.Contains(report.Warnings[i], want.warnings[i]) {
					t.Errorf("warning %d %q holds no %q", i+1, report.Warnings[i], want.warnings[i])
				}
			}
			messages := streamLines(t, path)
			if messages == nil {
				messages = executionMessages(t, path)
			}
			stated, written := statedTotals(t, messages)
			read := got.tokens
			if read.CacheWrite5m+read.CacheWrite1h != written {
				t.Errorf("cache writes %d, the result states %d", read.CacheWrite5m+read.CacheWrite1h, written)
			}
			read.CacheWrite5m, read.CacheWrite1h = 0, 0
			if read != stated {
				t.Errorf("tokens %+v, the result states %+v", read, stated)
			}
		})
	}
}
