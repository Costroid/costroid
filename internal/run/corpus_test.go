package run

import (
	"bytes"
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
	"github.com/Costroid/costroid/internal/adapter/claudecode"
	"github.com/Costroid/costroid/internal/adapter/codex"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

// corpusRoots are the testdata directories of the two adapters, where every
// recording sits.
var corpusRoots = map[string]string{
	claudecode.Name:      "../adapter/claudecode/testdata",
	codex.Agent{}.Name(): "../adapter/codex/testdata",
}

// corpusRows is what the rows of one recording come to: how many, their list
// total in USD, how many the catalog's fallback priced, and the grade on
// every row.
type corpusRows struct {
	rows     int
	total    string
	fallback int
	grade    string
}

// runCorpus pins the rows every recording writes, by its agent and its
// directory below that agent's testdata. TestRunCorpus also holds each Claude
// Code recording to the total_cost_usd it states, which needs no entry here.
var runCorpus = map[string]corpusRows{
	// Each Claude Code total is the run's own total_cost_usd. Each Codex total
	// is the recording README's, or derived beside it.
	"claude-code/2.1.316/one-request":    {rows: 5, total: "0.00060589", grade: adapter.GradeAgentFlag},
	"claude-code/2.1.316/subagent":       {rows: 19, total: subagentList, grade: adapter.GradeAgentFlag},
	"claude-code/2.1.316/budget-stop":    {rows: 6, total: "0.0007324", grade: adapter.GradeAgentFlag},
	"claude-code/2.1.316/execution-file": {rows: 8, total: "0.0516717", grade: adapter.GradeNone},
	"claude-code/2.1.316/action-session": {rows: 12, total: "0.0391529", grade: adapter.GradeNone},
	"claude-code/2.1.319/current":        {rows: 5, total: "0.000877895", grade: adapter.GradeAgentFlag},
	"codex/0.164.0-alpha.11.1/action":    {rows: 8, total: "0.029324", grade: adapter.GradeNone},
	// The Agent SDK's programs, read as Claude Code's own stream; each total
	// is the program's own total_cost_usd. Each is two requests from the
	// session file and one outside the conversation from the result.
	"claude-code/2.1.316/sdk-claude-ts": {rows: 10, total: "0.000468745", grade: adapter.GradeAgentFlag},
	"claude-code/2.1.316/sdk-claude-py": {rows: 12, total: "0.000303215", grade: adapter.GradeAgentFlag},
	// gpt-6-luna at 0.10, 0.125 and 0.50 USD per million: 3 uncached input,
	// 10035 cache write and 5 output tokens.
	"codex/0.164.0-alpha.11.1/sdk-codex": {rows: 3, total: "0.001257175", grade: adapter.GradeWrapperWatchdog},
	// gpt-5.6-sol at 4.00, 0.40 and 20.00 USD per million: 5875 uncached,
	// 28032 cached input and 111 output tokens.
	"codex/0.164.0-alpha.11.1/exec": {rows: 8, total: "0.0369328", grade: adapter.GradeWrapperWatchdog},
	// The stream alone names no model, so the fallback prices it, at 150.00,
	// 12.00 and 600.00 USD per million: 7246 uncached, 44928 cached input and
	// 116 output tokens. The plan refuses this command; the corpus reads the
	// recording all the same.
	"codex/0.164.0-alpha.11.1/resume":  {rows: 4, total: "1.695636", fallback: 3, grade: adapter.GradeNone},
	"codex/0.164.0-alpha.11.1/stopped": {rows: 4, total: "0.0283136", grade: adapter.GradeWrapperWatchdog},
	"codex/0.167.0/stopped":            {rows: 8, total: "0.0350276", grade: adapter.GradeWrapperWatchdog},
	// Stopped at --max-budget-usd 0.0001. Its one model, claude-haiku-5-5, had
	// spent USD 0.0001285, and its session file holds no request.
	"claude-code/2.1.316/action-budget-stop": {rows: 2, total: "0.0001285", grade: adapter.GradeNone},
}

// corpusAt is when the corpus reads a recording as run or saved.
var corpusAt = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

// corpusRecordings lists every recording a manifest.json marks under the
// adapters' testdata, as agent/<directory below testdata>.
func corpusRecordings(t *testing.T) []string {
	t.Helper()
	var found []string
	for agent, root := range corpusRoots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || entry.Name() != "manifest.json" {
				return err
			}
			dir, err := filepath.Rel(root, filepath.Dir(path))
			found = append(found, agent+"/"+filepath.ToSlash(dir))
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(found)
	return found
}

// optionalFile reads a file of a recording, nil when it has none.
func optionalFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func nonEmptyLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// firstField is the value of a string field in the first of messages whose
// type is kind, empty when none states it.
func firstField(messages []json.RawMessage, kind, field string) string {
	for _, raw := range messages {
		var message map[string]any
		if json.Unmarshal(raw, &message) != nil || message["type"] != kind {
			continue
		}
		if value, ok := message[field].(string); ok {
			return value
		}
	}
	return ""
}

func asMessages(lines []string) []json.RawMessage {
	messages := make([]json.RawMessage, len(lines))
	for i, line := range lines {
		messages[i] = json.RawMessage(line)
	}
	return messages
}

// readClaudeRecording reads a Claude Code recording as its run left it: its
// session files under a config directory, then its execution file or its
// stream. A stream's run held the plan's grade unless the report states one.
func readClaudeRecording(t *testing.T, dir string) adapter.Report {
	t.Helper()
	t.Setenv("ENABLE_PROMPT_CACHING_1H", "")
	stream := asMessages(nonEmptyLines(optionalFile(t, filepath.Join(dir, "stream.jsonl"))))
	execution := optionalFile(t, filepath.Join(dir, "claude-execution-output.json"))
	var messages []json.RawMessage
	if execution != nil {
		if err := json.Unmarshal(execution, &messages); err != nil {
			t.Fatal(err)
		}
	}
	sessionID := firstField(append(slices.Clone(stream), messages...), "system", "session_id")
	config := t.TempDir()
	project := filepath.Join(config, "projects", "-work")
	if err := os.MkdirAll(filepath.Join(project, sessionID, "subagents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if session := optionalFile(t, filepath.Join(dir, "session.jsonl")); session != nil {
		if err := os.WriteFile(filepath.Join(project, sessionID+".jsonl"), session, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	agents, _ := filepath.Glob(filepath.Join(dir, "subagents", "*.jsonl"))
	for _, path := range agents {
		if err := os.WriteFile(filepath.Join(project, sessionID, "subagents", filepath.Base(path)), mustRead(t, path), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	agent := claudecode.Agent{ConfigDir: config}
	if execution != nil {
		report, err := agent.ReadExecutionFile(bytes.NewReader(execution), corpusAt)
		if err != nil {
			t.Fatal(err)
		}
		return report
	}
	plan, err := agent.Plan([]string{"claude", "-p", "work"}, decimal.MustParse("5"))
	if err != nil {
		t.Fatal(err)
	}
	session := agent.NewSession(plan)
	for _, line := range stream {
		if err := session.Feed(line, corpusAt); err != nil {
			t.Fatal(err)
		}
	}
	report, err := session.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if report.Enforcement.Grade == "" {
		report.Enforcement = plan.Enforcement
	}
	return report
}

// readCodexRecording reads a Codex recording as its run left it. With a
// stream, the run was `codex exec` under the wrapper, whose tail read the
// thread's rollout file from the Codex home. Without one, the wrapper read
// the Codex home after a run it did not start.
func readCodexRecording(t *testing.T, dir string) adapter.Report {
	t.Helper()
	t.Setenv("CODEX_API_KEY", "")
	stream := nonEmptyLines(optionalFile(t, filepath.Join(dir, "stream.jsonl")))
	rollout := optionalFile(t, filepath.Join(dir, "rollout.jsonl"))
	thread := firstField(asMessages(stream), "thread.started", "thread_id")
	if stream == nil {
		var meta struct {
			Payload struct {
				ID string `json:"id"`
			} `json:"payload"`
		}
		if err := json.Unmarshal([]byte(nonEmptyLines(rollout)[0]), &meta); err != nil {
			t.Fatal(err)
		}
		thread = meta.Payload.ID
	}
	home := t.TempDir()
	if rollout != nil {
		day := filepath.Join(home, "sessions", "2026", "11", "07")
		if err := os.MkdirAll(day, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(day, "rollout-2026-11-07T12-00-00-"+thread+".jsonl"), rollout, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if stream == nil {
		report, err := codex.Agent{}.ReadHome(home, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		return report
	}
	t.Setenv("CODEX_HOME", home)
	plan, err := codex.Agent{}.Plan([]string{filepath.Join(t.TempDir(), "codex"), "exec", "work"}, decimal.MustParse("5"))
	if err != nil {
		t.Fatal(err)
	}
	session := codex.Agent{}.NewSession(plan)
	for _, line := range stream {
		if err := session.Feed([]byte(line), corpusAt); err != nil {
			t.Fatal(err)
		}
	}
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	session.(adapter.Tailer).Tail(ended, func([]adapter.Request) {})
	report, err := session.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return report
}

// statedRunTotal is the total_cost_usd of a Claude Code recording's last
// result, as the run wrote it, empty when it states none.
func statedRunTotal(t *testing.T, dir string) string {
	t.Helper()
	messages := asMessages(nonEmptyLines(optionalFile(t, filepath.Join(dir, "stream.jsonl"))))
	if execution := optionalFile(t, filepath.Join(dir, "claude-execution-output.json")); execution != nil {
		if err := json.Unmarshal(execution, &messages); err != nil {
			t.Fatal(err)
		}
	}
	stated := ""
	for _, raw := range messages {
		var result struct {
			Type  string      `json:"type"`
			Total json.Number `json:"total_cost_usd"`
		}
		if json.Unmarshal(raw, &result) == nil && result.Type == "result" && result.Total != "" {
			stated = result.Total.String()
		}
	}
	return stated
}

// floatNoise bounds how far a total Claude Code wrote as a binary float may
// sit from the exact decimal: its own rounding, far below any price step.
var floatNoise = decimal.MustParse("0.000000000001")

// TestRunCorpus writes the rows of every recording a manifest.json marks, at
// the pinned catalog and the grade the run earned. Each comes to its entry in
// runCorpus, every row carries the grade, and a Claude Code recording's rows
// come to the total_cost_usd the run states, to within its float rounding.
func TestRunCorpus(t *testing.T) {
	outsideCIForRun(t)
	recordings := corpusRecordings(t)
	for name := range runCorpus {
		if !slices.Contains(recordings, name) {
			t.Errorf("runCorpus names %s, which no manifest marks", name)
		}
	}
	for _, name := range recordings {
		t.Run(name, func(t *testing.T) {
			want, ok := runCorpus[name]
			if !ok {
				t.Fatalf("no entry in runCorpus for %s", name)
			}
			agent, dir, _ := strings.Cut(name, "/")
			path := filepath.Join(corpusRoots[agent], filepath.FromSlash(dir))
			var report adapter.Report
			if agent == claudecode.Name {
				report = readClaudeRecording(t, path)
			} else {
				report = readCodexRecording(t, path)
			}
			var rows bytes.Buffer
			result, err := Record(Options{Rows: &rows, Catalog: pinned(t)}, agent, report)
			if err != nil {
				t.Fatal(err)
			}
			written := parseRows(t, rows.Bytes())
			fallback := 0
			for _, row := range written {
				if strings.Contains(row["SkuPriceId"].(string), ":fallback:") {
					fallback++
				}
				if grade := tagOf(row, focus.TagEnforcementGrade); grade != want.grade {
					t.Errorf("a %s row reads grade %s", row["SkuMeter"], grade)
				}
			}
			if result.Rows != want.rows || len(written) != want.rows || fallback != want.fallback || result.Enforcement.Grade != want.grade {
				t.Errorf("%d rows (%d written), %d at the fallback, grade %s; want %d, %d, %s", result.Rows, len(written), fallback, result.Enforcement.Grade, want.rows, want.fallback, want.grade)
			}
			if err := checkListTotal(result.ListTotal, want.total); err != nil {
				t.Error(err)
			}
			if agent != claudecode.Name {
				return
			}
			stated := statedRunTotal(t, path)
			if stated == "" {
				t.Fatal("the recording states no total_cost_usd")
			}
			gap := result.ListTotal.Sub(decimal.MustParse(stated))
			if gap.Sign() < 0 {
				gap = decimal.FromInt(0).Sub(gap)
			}
			if gap.Cmp(floatNoise) > 0 {
				t.Errorf("rows come to USD %s, the run states %s", result.ListTotal, stated)
			}
		})
	}
}
