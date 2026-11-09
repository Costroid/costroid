package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// copilotFixture is the recorded Copilot CLI run beside its adapter.
func copilotFixture(t *testing.T) string {
	t.Helper()
	return filepath.Join(moduleRoot(t), "internal", "adapter", "copilot", "testdata", "1.0.103", "byok")
}

// The raw ids a planted Copilot run carries in place of the fixture's
// synthetic ones. None came from a run.
const (
	plantedCopilotSession = "5d1c9e7a-planted-copilot-session"
	plantedMessageID      = "msg_01PlantedCopilotMessage"
)

// plantedModifiedFiles are paths as Copilot lists them in codeChanges.
var plantedModifiedFiles = []string{plantedPath + "/internal/planted_change.go", "docs/planted-notes.md"}

// replaceText replaces every string value that reads from with to.
func replaceText(v *value, from, to string) {
	switch v.kind {
	case kindString:
		if v.text == from {
			*v = *stringValue(to)
		}
	case kindObject, kindArray:
		for _, item := range v.items {
			replaceText(item, from, to)
		}
	}
}

func pathList(paths []string) *value {
	list := &value{kind: kindArray}
	for _, path := range paths {
		list.items = append(list.items, stringValue(path))
	}
	return list
}

// plantCopilotRun lays the fixture out as Copilot CLI leaves a run: the
// stream beside a home that holds the session's events.jsonl and a session
// store. It plants what a raw run holds: the prompt, the working directory,
// modified files in both codeChanges, raw ids and the store's content.
func plantCopilotRun(t *testing.T) (stream, home string) {
	t.Helper()
	files, err := readRecording(copilotFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	raw := t.TempDir()
	home = filepath.Join(raw, "home")
	stream = filepath.Join(raw, "stream.jsonl")
	for _, file := range files {
		for _, line := range file.lines {
			replaceText(line, "00000000-0000-4000-8000-000000000001", plantedCopilotSession)
			replaceText(line, "msg_fixture_0001", plantedMessageID)
			data := line.field("data")
			switch line.fieldText("type") {
			case "session.start":
				setField(data, "context", &value{kind: kindObject, keys: []string{"cwd", "branch"}, items: []*value{stringValue(plantedPath), stringValue(plantedBranch)}})
			case "user.message":
				setField(data, "content", stringValue(plantedPrompt))
			case "session.shutdown":
				setField(data.field("codeChanges"), "filesModified", pathList(plantedModifiedFiles))
				snapshot := &value{kind: kindObject}
				setField(snapshot, "codeChanges", &value{kind: kindObject, keys: []string{"filesModified"}, items: []*value{pathList(plantedModifiedFiles)}})
				setField(data, "accountingSnapshot", snapshot)
			case "result":
				setField(line.field("usage").field("codeChanges"), "filesModified", pathList(plantedModifiedFiles))
			}
		}
		target := stream
		if file.name == copilotEvents {
			target = filepath.Join(home, copilotSessions, plantedCopilotSession, copilotEvents)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, file.encode(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(home, "session-store.db"), "SQLite format 3 "+plantedPrompt)
	write(t, filepath.Join(home, copilotSessions, plantedCopilotSession, "workspace.yaml"), "cwd: "+plantedPath)
	return stream, home
}

// TestCopilotScrubDropsFilesModified collects and scrubs a Copilot CLI run
// whose session.shutdown, its accounting snapshot and the stream's result
// line each list modified files. The scrub drops every list by its key, the
// recording holds no path, prompt or raw id, verify passes, and the session
// store and the workspace file are never copied. Scrubbed again, the
// committed fixture is unchanged.
func TestCopilotScrubDropsFilesModified(t *testing.T) {
	stream, home := plantCopilotRun(t)
	raw := filepath.Join(t.TempDir(), "raw")
	var stderr bytes.Buffer
	if code := run(t.Context(), []string{"collect", "--agent", agentCopilot, "--stream", stream, "--home", home, "--scenario", "byok", "--out", raw}, func(string) string { return "" }, nil, io.Discard, &stderr); code != 0 {
		t.Fatalf("collect exited %d: %s", code, stderr.String())
	}
	collected, err := readRecording(raw)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, file := range collected {
		names = append(names, file.name)
	}
	if !slices.Equal(names, []string{copilotEvents, "stream.jsonl"}) {
		t.Errorf("collected %q", names)
	}

	out := filepath.Join(t.TempDir(), "scrubbed")
	stderr.Reset()
	if code := run(t.Context(), []string{"scrub", "--out", out, raw}, nil, nil, io.Discard, &stderr); code != 0 {
		t.Fatalf("scrub exited %d: %s", code, stderr.String())
	}
	for _, dropped := range []string{
		"record: dropped data.codeChanges.filesModified outside the allowlist (1)",
		"record: dropped data.accountingSnapshot outside the allowlist (1)",
		"record: dropped usage.codeChanges.filesModified outside the allowlist (1)",
		"record: dropped data.context outside the allowlist (1)",
		"record: dropped data.content outside the allowlist",
	} {
		if !strings.Contains(stderr.String(), dropped) {
			t.Errorf("the scrub did not report %q:\n%s", dropped, stderr.String())
		}
	}
	for _, planted := range append([]string{"filesModified", plantedPath, plantedBranch, plantedPrompt, plantedCopilotSession, plantedMessageID, "SQLite"}, plantedModifiedFiles...) {
		if hit := grepDir(t, out, planted); hit != "" {
			t.Errorf("%s still holds %q", hit, planted)
		}
	}
	if problems, err := verifyDirectory(out, []string{plantedPrompt}); err != nil || len(problems) > 0 {
		t.Errorf("the scrubbed run fails verify: %v %v", problems, err)
	}
	if m, err := readManifest(out); err != nil || m.Agent != agentCopilot || m.Version != "1.0.103" {
		t.Errorf("the manifest reads %+v, %v", m, err)
	}
	sameFiles(t, scrubbed(t, copilotFixture(t), nil), copilotFixture(t))
}
