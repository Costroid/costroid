package main

import (
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// watchedFS records every file opened through it. Listing a directory and
// reading a file's size and time are not counted: neither reads its content.
type watchedFS struct {
	fs.FS
	opened *[]string
}

func (w watchedFS) Open(name string) (fs.File, error) {
	if info, err := fs.Stat(w.FS, name); err == nil && !info.IsDir() {
		*w.opened = append(*w.opened, name)
	}
	return w.FS.Open(name)
}

func (w watchedFS) ReadDir(name string) ([]fs.DirEntry, error) { return fs.ReadDir(w.FS, name) }
func (w watchedFS) Stat(name string) (fs.FileInfo, error)      { return fs.Stat(w.FS, name) }

// Every value below is planted by the test. The other session's secret must
// never reach the recording, and its file must never be opened.
const (
	runSession   = "5b0e3f6a-1c2d-4e5f-8a9b-0c1d2e3f4a5b"
	otherSession = "9e8d7c6b-5a4f-4e3d-8c2b-1a0f9e8d7c6b"
	otherSecret  = "planted text of another session on this machine"
	runPrompt    = "Reply with the word ok. This prompt is planted by a test."
)

func TestRecordLocalReadsOnlyItsSession(t *testing.T) {
	cat, err := exec.LookPath("cat")
	if err != nil {
		t.Skip("no cat on this machine")
	}
	home := t.TempDir()
	project := filepath.Join(home, "projects", "-work-app")
	write(t, filepath.Join(project, runSession+".jsonl"),
		`{"type":"user","sessionId":"`+runSession+`","cwd":"/home/someone/app","gitBranch":"main","message":{"role":"user","content":"`+runPrompt+`"}}`,
		`{"type":"assistant","sessionId":"`+runSession+`","requestId":"req_011CXyZplanted","cwd":"/home/someone/app","timestamp":"2026-11-07T10:00:01.000Z","version":"2.1.320","message":{"model":"claude-haiku-5-5","id":"msg_01planted","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":3,"output_tokens":1}}}`)
	write(t, filepath.Join(project, runSession, "subagents", "agent-a1b2c3.jsonl"),
		`{"type":"assistant","sessionId":"`+runSession+`","requestId":"req_011CXyZsub","isSidechain":true,"message":{"model":"claude-haiku-5-5","id":"msg_01sub","content":[{"type":"text","text":"from the subagent"}],"usage":{"input_tokens":2,"output_tokens":1}}}`)
	for _, other := range []string{
		filepath.Join(project, otherSession+".jsonl"),
		filepath.Join(home, "projects", "-elsewhere", otherSession+".jsonl"),
		filepath.Join(project, otherSession, "subagents", "agent-ffff.jsonl"),
	} {
		write(t, other, `{"type":"user","sessionId":"`+otherSession+`","message":{"content":"`+otherSecret+`"}}`)
	}
	stream := filepath.Join(t.TempDir(), "agent-output.jsonl")
	write(t, stream,
		`{"type":"system","subtype":"init","session_id":"`+runSession+`","cwd":"/home/someone/app","model":"claude-haiku-5-5","apiKeySource":"none","claude_code_version":"2.1.320"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"ok","total_cost_usd":0.0000155,"session_id":"`+runSession+`"}`)

	scratch := t.TempDir()
	t.Setenv("TMPDIR", scratch)
	var opened []string
	out := filepath.Join(t.TempDir(), "subscription")
	local := localRun{
		agent: agentClaudeCode, scenario: "subscription", out: out, command: []string{cat, stream},
		home: watchedFS{FS: os.DirFS(home), opened: &opened}, stdin: nil, stderr: io.Discard,
		now: func() time.Time { return time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC) },
	}
	if err := local.run(); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"projects/-work-app/" + runSession + ".jsonl",
		"projects/-work-app/" + runSession + "/subagents/agent-a1b2c3.jsonl",
	}
	if !slices.Equal(opened, want) {
		t.Errorf("opened %v, want only %v", opened, want)
	}
	var names []string
	err = filepath.WalkDir(out, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			name, _ := filepath.Rel(out, path)
			names = append(names, filepath.ToSlash(name))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if wantNames := []string{"manifest.json", "session.jsonl", "stream.jsonl", "subagents/agent-1.jsonl"}; !slices.Equal(names, wantNames) {
		t.Errorf("the recording holds %v, want %v", names, wantNames)
	}
	for _, leaked := range []string{otherSecret, otherSession, runSession, runPrompt, "/home/", "gitBranch", "from the subagent"} {
		if hit := grepDir(t, out, leaked); hit != "" {
			t.Errorf("%s holds %q", hit, leaked)
		}
	}
	if problems, err := verifyDirectory(out, []string{runPrompt}); err != nil || len(problems) > 0 {
		t.Errorf("the recording fails verify: %v %v", problems, err)
	}
	if left, _ := os.ReadDir(scratch); len(left) > 0 {
		t.Errorf("the raw files were left in %s: %v", scratch, left)
	}
}

func TestRecordLocalOpensOnlyCodexFilesOfItsRun(t *testing.T) {
	cat, err := exec.LookPath("cat")
	if err != nil {
		t.Skip("no cat on this machine")
	}
	const thread, subagent, older = "019a0b1c-2d3e-7f40-8a1b-2c3d4e5f6a7b", "019a0b1c-2d3e-7f40-8a1b-2c3d4e5f6a7c", "019a0b1c-2d3e-7f40-8a1b-2c3d4e5f6a7d"
	start := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	home := t.TempDir()
	day := filepath.Join(home, "sessions", "2026", "11", "07")
	usage := `"usage":{"input_tokens":100,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":5,"reasoning_output_tokens":0,"total_tokens":105}`
	write(t, filepath.Join(day, "rollout-2026-11-07T10-00-01-"+thread+".jsonl"),
		`{"timestamp":"2026-11-07T10:00:01.000Z","type":"session_meta","payload":{"id":"`+thread+`","cli_version":"0.164.0-alpha.11.1","cwd":"/home/someone/app","instructions":"planted"}}`,
		`{"timestamp":"2026-11-07T10:00:02.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"`+runPrompt+`"}]}}`,
		`{"timestamp":"2026-11-07T10:00:03.000Z","type":"token_usage_record","payload":{"response_id":"resp_planted1","thread_id":"`+thread+`","turn_id":"0199-turn",`+usage+`}}`)
	write(t, filepath.Join(day, "rollout-2026-11-07T10-00-02-"+subagent+".jsonl"),
		`{"timestamp":"2026-11-07T10:00:02.000Z","type":"session_meta","payload":{"id":"`+subagent+`","cli_version":"0.164.0-alpha.11.1","thread_source":"subagent","session_id":"`+thread+`"}}`)
	olderFile := filepath.Join(day, "rollout-2026-11-07T09-00-00-"+older+".jsonl")
	write(t, olderFile, `{"type":"session_meta","payload":{"id":"`+older+`","instructions":"`+otherSecret+`"}}`)
	if err := os.Chtimes(olderFile, start.Add(-time.Hour), start.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	stream := filepath.Join(t.TempDir(), "agent-output.jsonl")
	write(t, stream, `{"type":"thread.started","thread_id":"`+thread+`"}`, `{"type":"turn.completed",`+usage+`}`)

	var opened []string
	out := filepath.Join(t.TempDir(), "codex")
	local := localRun{
		agent: agentCodex, scenario: "subscription-codex", out: out, command: []string{cat, stream},
		home: watchedFS{FS: os.DirFS(home), opened: &opened}, stderr: io.Discard,
		now: func() time.Time { return start },
	}
	if err := local.run(); err != nil {
		t.Fatal(err)
	}
	for _, name := range opened {
		if strings.Contains(name, older) {
			t.Errorf("opened %s, written before the run began", name)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "subagents", "rollout-1.jsonl")); err != nil {
		t.Errorf("the subagent's rollout file was not kept: %v", err)
	}
	for _, leaked := range []string{otherSecret, runPrompt, thread, "resp_planted1", "/home/", "planted\""} {
		if hit := grepDir(t, out, leaked); hit != "" {
			t.Errorf("%s holds %q", hit, leaked)
		}
	}
}
