package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// exampleAgent is an agent no adapter reads, added by a test alone.
func exampleAgent() *agentEntry {
	return &agentEntry{
		name: "example-agent", title: "Example Agent", adapter: "example",
		stream: "example --json", homeVariable: "EXAMPLE_HOME", homeDefault: ".example",
		collect: func(_ *agentEntry, c *collected, src source, _, _ []byte) error {
			return copyFile(c, src.home, "usage.jsonl", "usage.jsonl")
		},
		allowlist:     newAllowlist([]string{"kind", "example_version", "tokens"}, nil, map[string]*idKind{"request_id": &requestIDs}, nil),
		allowlistFile: "registry_test.go",
		recognizes:    func(line *value) bool { return line.fieldText("kind") == "example.usage" },
		version: func(files []*recordingFile) string {
			for _, file := range files {
				for _, line := range file.lines {
					if version := line.fieldText("example_version"); version != "" {
						return version
					}
				}
			}
			return ""
		},
		total: func([]*recordingFile) string { return "The example's own total." },
	}
}

func TestRegistryAddsAnAgentInOneEntry(t *testing.T) {
	saved := agents
	agents = append(slices.Clone(agents), exampleAgent())
	t.Cleanup(func() { agents = saved })

	home := t.TempDir()
	write(t, filepath.Join(home, "usage.jsonl"),
		`{"kind":"example.usage","example_version":"1.2.3","request_id":"raw-request-7","tokens":12,"prompt":"`+plantedPrompt+`"}`)
	getenv := func(name string) string {
		if name == "EXAMPLE_HOME" {
			return home
		}
		return ""
	}
	ctx := context.Background()
	var stderr bytes.Buffer

	raw := filepath.Join(t.TempDir(), "raw")
	if code := run(ctx, []string{"collect", "--agent", "example-agent", "--scenario", "first", "--out", raw}, getenv, nil, io.Discard, &stderr); code != 0 {
		t.Fatalf("collect exited %d: %s", code, stderr.String())
	}
	if m, err := readManifest(raw); err != nil || m.Agent != "example-agent" {
		t.Fatalf("collect wrote the manifest %+v, %v", m, err)
	}

	out := filepath.Join(t.TempDir(), "scrubbed")
	stderr.Reset()
	if code := run(ctx, []string{"scrub", "--out", out, raw}, getenv, nil, io.Discard, &stderr); code != 0 {
		t.Fatalf("scrub exited %d: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "dropped prompt outside the allowlist (1)") {
		t.Errorf("scrub did not drop the prompt by the entry's allowlist: %s", stderr.String())
	}
	got, _ := os.ReadFile(filepath.Join(out, "usage.jsonl"))
	if want := `{"kind":"example.usage","example_version":"1.2.3","request_id":"req_fixture_0001","tokens":12}` + "\n"; string(got) != want {
		t.Errorf("scrubbed\n got %s\nwant %s", got, want)
	}

	var stdout bytes.Buffer
	if code := run(ctx, []string{"verify", out}, getenv, nil, &stdout, io.Discard); code != 0 || stdout.String() != "ok\n" {
		t.Errorf("verify exited %d and printed %q", code, stdout.String())
	}
	planted := filepath.Join(t.TempDir(), "planted")
	transformDir(t, out, planted, func(line *value) { setField(line, "cwd", stringValue(placeholderPath)) })
	copyManifest(t, out, planted)
	if problems, _ := verifyDirectory(planted, nil); !slices.Contains(problems, "usage.jsonl:1: key outside the allowlist: cwd") {
		t.Errorf("verify did not hold the recording to the entry's allowlist: %v", problems)
	}

	root := t.TempDir()
	stdout.Reset()
	if code := run(ctx, []string{"fixture", "--run", "19283746501", "--scenario", "first", "--from", out, "--root", root}, getenv, nil, &stdout, io.Discard); code != 0 {
		t.Fatalf("fixture exited %d", code)
	}
	target := filepath.Join(root, "internal/adapter/example/testdata/1.2.3/first")
	if stdout.String() != "placed "+target+"\n" {
		t.Errorf("printed %q", stdout.String())
	}
	readme, _ := os.ReadFile(filepath.Join(target, readmeName))
	for _, want := range []string{"A recording of Example Agent 1.2.3, workflow run 19283746501", "The example's own total."} {
		if !strings.Contains(string(readme), want) {
			t.Errorf("README lacks %q:\n%s", want, readme)
		}
	}

	unmarked := t.TempDir()
	write(t, filepath.Join(unmarked, "usage.jsonl"), `{"kind":"example.usage","example_version":"1.2.3","tokens":3}`)
	if m, err := readManifest(scrubbed(t, unmarked, nil)); err != nil || m.Agent != "example-agent" || m.Version != "1.2.3" {
		t.Errorf("a run with no manifest was not recognized: %+v %v", m, err)
	}

	cat, err := exec.LookPath("cat")
	if err != nil {
		t.Skip("no cat on this machine")
	}
	stream := filepath.Join(t.TempDir(), "stream.jsonl")
	write(t, stream, `{"kind":"example.start"}`)
	local := filepath.Join(t.TempDir(), "local")
	stdout.Reset()
	stderr.Reset()
	if code := run(ctx, []string{"local", "--agent", "example-agent", "--scenario", "first", "--out", local, "--", cat, stream}, getenv, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("local exited %d: %s", code, stderr.String())
	}
	if problems, err := verifyDirectory(local, []string{plantedPrompt}); err != nil || len(problems) > 0 {
		t.Errorf("the local recording fails verify: %v %v", problems, err)
	}
}

// Each SDK entry collects exactly what its agent's entry collects from the
// same run, and only the manifest names it apart.
func TestSDKEntriesCollectAsTheirAgents(t *testing.T) {
	const session = "5b0e3f6a-1c2d-4e5f-8a9b-0c1d2e3f4a5b"
	claudeHome := t.TempDir()
	write(t, filepath.Join(claudeHome, "projects", "-work-app", session+".jsonl"),
		`{"type":"assistant","sessionId":"`+session+`","requestId":"req_011CXyZ","message":{"id":"msg_01","usage":{"input_tokens":3,"output_tokens":1}}}`)
	claudeStream := filepath.Join(t.TempDir(), "stream.jsonl")
	write(t, claudeStream, `{"type":"system","subtype":"init","session_id":"`+session+`","claude_code_version":"2.1.320"}`)

	const thread = "019a0b1c-2d3e-7f40-8a1b-2c3d4e5f6a7b"
	codexHome := t.TempDir()
	write(t, filepath.Join(codexHome, "sessions", "2026", "11", "07", "rollout-2026-11-07T10-00-01-"+thread+".jsonl"),
		`{"type":"session_meta","payload":{"id":"`+thread+`","cli_version":"0.164.0-alpha.11.1"}}`)
	codexStream := filepath.Join(t.TempDir(), "stream.jsonl")
	write(t, codexStream, `{"type":"thread.started","thread_id":"`+thread+`"}`)

	for _, pair := range []struct {
		agent, sdk, stream, home string
	}{
		{agentClaudeCode, "claude-agent-sdk", claudeStream, claudeHome},
		{agentCodex, "codex-sdk", codexStream, codexHome},
	} {
		t.Run(pair.sdk, func(t *testing.T) {
			since := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
			want, err := collect(source{agent: pair.agent, stream: pair.stream, home: os.DirFS(pair.home), since: since})
			if err != nil {
				t.Fatal(err)
			}
			got, err := collect(source{agent: pair.sdk, stream: pair.stream, home: os.DirFS(pair.home), since: since})
			if err != nil {
				t.Fatal(err)
			}
			if len(got.files) != len(want.files) || len(got.files) < 2 {
				t.Fatalf("collected %d files, %s collected %d", len(got.files), pair.agent, len(want.files))
			}
			for name, data := range want.files {
				if !bytes.Equal(got.files[name], data) {
					t.Errorf("%s differs from what %s collects", name, pair.agent)
				}
			}
			sdk, _ := agentFor(pair.sdk)
			agent, _ := agentFor(pair.agent)
			if sdk.allowlist != agent.allowlist || sdk.adapter != agent.adapter || sdk.homeVariable != agent.homeVariable {
				t.Errorf("%s is not held, placed and homed as %s", pair.sdk, pair.agent)
			}
		})
	}
}

// Each entry names an allowlist file that exists here and a name a manifest
// and a fixture's directory can hold.
func TestRegistryEntriesAreWhole(t *testing.T) {
	for _, entry := range agents {
		if !safeWord.MatchString(entry.name) || entry.title == "" || entry.adapter == "" {
			t.Errorf("%q lacks a plain name, a title or an adapter", entry.name)
		}
		if entry.collect == nil || entry.allowlist == nil || entry.version == nil || entry.total == nil {
			t.Errorf("%s lacks a collector, an allowlist, a version or a total", entry.name)
		}
		if _, err := os.Stat(entry.allowlistFile); err != nil {
			t.Errorf("%s names the allowlist file %q: %v", entry.name, entry.allowlistFile, err)
		}
	}
	if _, err := agentFor("cursor"); err == nil || err.Error() != `agent "cursor" is not one of claude-code, codex, gh-aw, claude-agent-sdk, codex-sdk, copilot or opencode` {
		t.Errorf("an unknown agent: %v", err)
	}
}
