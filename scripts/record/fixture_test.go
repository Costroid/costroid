package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// artifact is a committed recording scrubbed again with the manifest the
// recording workflow writes, as gh run download leaves it.
func artifact(t *testing.T, committed, scenario string) string {
	t.Helper()
	dir := scrubbed(t, filepath.Join(moduleRoot(t), committed), nil)
	m, err := readManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	m.Scenario, m.Run, m.Date, m.Costroid = scenario, "19283746501", "2026-11-07", "f8a47fb"
	if err := writeManifest(dir, m); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestFixtureLayout(t *testing.T) {
	root := t.TempDir()
	from := artifact(t, "internal/adapter/claudecode/testdata/2.1.316/one-request", "claude-current")
	var stdout strings.Builder
	code := run(context.Background(), []string{"fixture", "--run", "19283746501", "--scenario", "claude-current", "--from", from, "--root", root}, os.Getenv, nil, &stdout, io.Discard)
	if code != 0 {
		t.Fatalf("fixture exited %d", code)
	}
	target := filepath.Join(root, "internal/adapter/claudecode/testdata/2.1.316/claude-current")
	if stdout.String() != "placed "+target+"\n" {
		t.Errorf("printed %q", stdout.String())
	}
	for _, name := range []string{"stream.jsonl", "session.jsonl"} {
		want, _ := os.ReadFile(filepath.Join(from, name))
		got, err := os.ReadFile(filepath.Join(target, name))
		if err != nil || string(got) != string(want) {
			t.Errorf("%s was not placed as downloaded: %v", name, err)
		}
	}
	m, err := readManifest(target)
	if err != nil || m.Run != "19283746501" || m.Agent != agentClaudeCode || m.Version != "2.1.316" {
		t.Errorf("manifest %+v %v", m, err)
	}
	readme, _ := os.ReadFile(filepath.Join(target, readmeName))
	for _, want := range []string{"# claude-current", "Claude Code 2.1.316", "workflow run 19283746501", "on 2026-11-07", "Costroid at `f8a47fb`", "The run's own total: 0.00060589 USD"} {
		if !strings.Contains(string(readme), want) {
			t.Errorf("README lacks %q:\n%s", want, readme)
		}
	}
	if problems, err := verifyDirectory(target, nil); err != nil || len(problems) > 0 {
		t.Errorf("the placed fixture fails verify: %v %v", problems, err)
	}
	if code := run(context.Background(), []string{"fixture", "--run", "19283746501", "--scenario", "claude-current", "--from", from, "--root", root}, os.Getenv, nil, io.Discard, io.Discard); code == 0 {
		t.Error("a second placement over the first one succeeded")
	}

	codex := artifact(t, "internal/adapter/codex/testdata/0.164.0-alpha.11.1/exec", "codex-stopped-current")
	codexTarget, err := placement{run: "19283746501", scenario: "codex-stopped-current", from: codex, root: root}.place()
	if err != nil {
		t.Fatal(err)
	}
	if codexTarget != filepath.Join(root, "internal/adapter/codex/testdata/0.164.0-alpha.11.1/codex-stopped-current") {
		t.Errorf("a Codex recording went to %s", codexTarget)
	}
	readme, _ = os.ReadFile(filepath.Join(codexTarget, readmeName))
	if want := "33907 input, 28032 cached input, 0 cache write, 111 output, 0 reasoning output tokens"; !strings.Contains(string(readme), want) {
		t.Errorf("Codex README lacks %q:\n%s", want, readme)
	}
}

func TestFixtureRefusesWhatVerifyRefuses(t *testing.T) {
	root := t.TempDir()
	from := artifact(t, "internal/adapter/claudecode/testdata/2.1.316/one-request", "claude-current")
	write(t, filepath.Join(from, "session.jsonl"), `{"type":"user","cwd":"/home/runner/work/app/app"}`)
	_, err := placement{run: "19283746501", scenario: "claude-current", from: from, root: root}.place()
	if err == nil || !strings.Contains(err.Error(), "failed verify") {
		t.Errorf("a dirty artifact was placed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "internal/adapter/claudecode/testdata/2.1.316/claude-current")); err == nil {
		t.Error("a refused artifact left files behind")
	}
	clean := artifact(t, "internal/adapter/claudecode/testdata/2.1.316/one-request", "claude-current")
	if _, err := (placement{run: "19283746501", scenario: "nested-hook", from: clean, root: root}).place(); err == nil {
		t.Error("an artifact of another scenario was placed")
	}
}
