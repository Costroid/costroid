package codex

import (
	"context"
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
	model    string
	requests int
	tokens   adapter.Tokens
	grade    string
	warnings []string
}

// codexCorpus pins what every recording under testdata reads as, by its
// directory below testdata. TestCodexCorpus also checks each recording
// against the totals it states, which needs no entry here.
var codexCorpus = map[string]corpusRead{
	"0.164.0-alpha.11.1/action": {
		source: costSourceRollout, basis: focus.Measured, model: "gpt-6.1-sol", requests: 2, grade: adapter.GradeNone,
		tokens: adapter.Tokens{InputUncached: 6, CacheRead: 10770, CacheWrite30m: 10958, Output: 84, CachedInputReported: 10770},
	},
	"0.164.0-alpha.11.1/exec": {
		source: costSourceRollout, basis: focus.ListEquivalent, model: "gpt-5.6-sol", requests: 2, grade: adapter.GradeWrapperWatchdog,
		tokens: adapter.Tokens{InputUncached: 5875, CacheRead: 28032, Output: 111, CachedInputReported: 28032},
	},
	// The plan refuses `codex exec resume`. Read as a run all the same, the
	// recording holds only the stream, which states the whole thread's spend
	// under no model and no version, and nothing watched it.
	"0.164.0-alpha.11.1/resume": {
		source: costSourceStream, basis: focus.ListEquivalent, model: UnknownModel, requests: 1, grade: adapter.GradeNone,
		tokens: adapter.Tokens{InputUncached: 7246, CacheRead: 44928, Output: 116, CachedInputReported: 44928},
	},
	// The Codex SDK's program printed the SDK's events, which are codex
	// exec's stream, so the recording reads as a run of codex exec.
	"0.164.0-alpha.11.1/sdk-codex": {
		source: costSourceRollout, basis: focus.ListEquivalent, model: "gpt-6-luna", requests: 1, grade: adapter.GradeWrapperWatchdog,
		tokens: adapter.Tokens{InputUncached: 3, CacheWrite30m: 10035, Output: 5},
	},
	"0.164.0-alpha.11.1/stopped": {
		source: costSourceRollout, basis: focus.ListEquivalent, model: "gpt-5.6-sol", requests: 1, grade: adapter.GradeWrapperWatchdog,
		tokens:   adapter.Tokens{InputUncached: 5632, CacheRead: 11264, Output: 64, CachedInputReported: 11264},
		warnings: []string{"the run ended inside a turn, and the rollout files show no model request in flight"},
	},
	"0.167.0/stopped": {
		source: costSourceRollout, basis: focus.ListEquivalent, model: "gpt-6.1-sol", requests: 2, grade: adapter.GradeWrapperWatchdog,
		tokens: adapter.Tokens{InputUncached: 6, CacheRead: 13181, CacheWrite30m: 13255, Output: 56, CachedInputReported: 13181},
	},
}

// corpusThreadStart is when the corpus reads a stream's thread as started.
var corpusThreadStart = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

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

// optionalLines reads a file's lines, or nil when the recording has no such
// file.
func optionalLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// rolloutThread is the thread a rollout file's session_meta names.
func rolloutThread(t *testing.T, lines []string) string {
	t.Helper()
	for _, line := range lines {
		var entry rolloutEntry
		var meta sessionMeta
		if json.Unmarshal([]byte(line), &entry) == nil && entry.Type == "session_meta" && json.Unmarshal(entry.Payload, &meta) == nil {
			return meta.ID
		}
	}
	t.Fatal("the rollout file holds no session_meta")
	return ""
}

// streamThread is the thread a stream's thread.started names.
func streamThread(lines []string) string {
	for _, line := range lines {
		var started struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
		}
		if json.Unmarshal([]byte(line), &started) == nil && started.Type == "thread.started" {
			return started.ThreadID
		}
	}
	return ""
}

// corpusHome lays a rollout file out under a new Codex home as Codex does,
// named for its thread.
func corpusHome(t *testing.T, rollout []string, thread string) string {
	t.Helper()
	home := t.TempDir()
	if rollout == nil {
		return home
	}
	day := filepath.Join(home, "sessions", "2026", "11", "07")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(day, "rollout-2026-11-07T12-00-00-"+thread+".jsonl")
	if err := os.WriteFile(name, []byte(strings.Join(rollout, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

// readRecording reads a recording as a run would leave it. With a stream,
// `codex exec` ran under costroid: the stream is fed line by line, and the
// thread's rollout file, when the recording holds one, is read from a Codex
// home as the tail reads it. Without a stream, costroid started nothing and
// reads the Codex home after the run. It returns the report and the session,
// nil for a home read.
func readRecording(t *testing.T, dir string) (adapter.Report, *Session) {
	t.Helper()
	t.Setenv("CODEX_API_KEY", "")
	stream := optionalLines(t, filepath.Join(dir, "stream.jsonl"))
	rollout := optionalLines(t, filepath.Join(dir, "rollout.jsonl"))
	if stream == nil {
		if rollout == nil {
			t.Fatal("the recording holds neither a stream nor a rollout file")
		}
		report, err := Agent{}.ReadHome(corpusHome(t, rollout, rolloutThread(t, rollout)), time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		return report, nil
	}
	t.Setenv("CODEX_HOME", corpusHome(t, rollout, streamThread(stream)))
	// The command names a codex that does not exist, so the plan asks no
	// installed Codex for its version: the files state it.
	plan, err := Agent{}.Plan([]string{filepath.Join(t.TempDir(), "codex"), "exec", "work"}, decimal.MustParse("5"))
	if err != nil {
		t.Fatal(err)
	}
	session := Agent{}.NewSession(plan).(*Session)
	for _, line := range stream {
		if err := session.Feed([]byte(line), corpusThreadStart); err != nil {
			t.Fatal(err)
		}
	}
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	session.Tail(ended, func([]adapter.Request) {})
	report, err := session.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return report, session
}

// rolloutTotal is the running total the rollout file's last token_count
// states, read without the session, and whether it states one.
func rolloutTotal(t *testing.T, lines []string) (adapter.Tokens, bool) {
	t.Helper()
	var last *usage
	for _, line := range lines {
		var entry rolloutEntry
		var message eventMessage
		if json.Unmarshal([]byte(line), &entry) != nil || entry.Type != "event_msg" || json.Unmarshal(entry.Payload, &message) != nil {
			continue
		}
		if message.Type == "token_count" && message.Info != nil {
			total := message.Info.Total
			last = &total
		}
	}
	if last == nil {
		return adapter.Tokens{}, false
	}
	tokens, err := last.tokens()
	if err != nil {
		t.Fatal(err)
	}
	return tokens, true
}

func requestSum(requests []adapter.Request) adapter.Tokens {
	var total adapter.Tokens
	for _, request := range requests {
		total = total.Add(request.Tokens)
	}
	return total
}

// TestCodexCorpus reads every recording a manifest.json marks under testdata.
// Each reads as its entry in codexCorpus states. Its requests add up to the
// last running total its rollout file states when the rollout file is the
// source, and to the stream's own total when a turn completed.
func TestCodexCorpus(t *testing.T) {
	recordings := recordingsIn(t, "testdata")
	for _, dir := range recordedDirectories(t) {
		if !slices.Contains(recordings, dir) {
			t.Errorf("testdata/%s holds a recording and no manifest.json", dir)
		}
	}
	for dir := range codexCorpus {
		if !slices.Contains(recordings, dir) {
			t.Errorf("codexCorpus names testdata/%s, which no manifest marks", dir)
		}
	}
	for _, dir := range recordings {
		t.Run(dir, func(t *testing.T) {
			path := filepath.Join("testdata", filepath.FromSlash(dir))
			var manifest corpusManifest
			if err := json.Unmarshal([]byte(fixture(t, filepath.Join(path, "manifest.json"))), &manifest); err != nil {
				t.Fatal(err)
			}
			if !manifest.Recorded || !slices.Contains([]string{name, SDKName}, manifest.Agent) || manifest.Scenario == "" || !strings.HasPrefix(dir, manifest.Version+"/") {
				t.Errorf("manifest %+v does not name the version of its directory", manifest)
			}
			want, ok := codexCorpus[dir]
			if !ok {
				t.Fatalf("no entry in codexCorpus for testdata/%s", dir)
			}
			report, session := readRecording(t, path)
			got := corpusRead{source: report.CostSource, basis: report.CostBasis, requests: len(report.Requests), tokens: requestSum(report.Requests), grade: report.Enforcement.Grade}
			for _, request := range report.Requests {
				if got.model == "" {
					got.model = request.Model
				} else if request.Model != got.model {
					got.model = "several"
				}
			}
			// A stream states no version; only the rollout file does.
			if report.AgentVersion != manifest.Version && (report.AgentVersion != "" || got.source != costSourceStream) {
				t.Errorf("agent version %q, the manifest says %q", report.AgentVersion, manifest.Version)
			}
			if got.source != want.source || got.basis != want.basis || got.model != want.model || got.requests != want.requests || got.grade != want.grade {
				t.Errorf("read %s, %s, %s, %d requests, grade %s; want %s, %s, %s, %d, %s", got.source, got.basis, got.model, got.requests, got.grade, want.source, want.basis, want.model, want.requests, want.grade)
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
			if stated, ok := rolloutTotal(t, optionalLines(t, filepath.Join(path, "rollout.jsonl"))); ok && got.source == costSourceRollout && got.tokens != stated {
				t.Errorf("tokens %+v, the rollout file's last total %+v", got.tokens, stated)
			}
			if session != nil && !session.turnOpen && len(session.turns) > 0 {
				if streamed := requestSum(session.turns); got.tokens != streamed {
					t.Errorf("tokens %+v, the stream's own total %+v", got.tokens, streamed)
				}
			}
		})
	}
}
