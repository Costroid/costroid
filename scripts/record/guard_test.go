package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// recordingsUnder finds every recording in a testdata directory under root: a
// directory a manifest.json marks, or one laid out as testdata/<agent
// version>/<scenario>/ from before manifests. Written fixtures, under
// synthetic/ or not-recorded/, are not recordings and are skipped.
func recordingsUnder(root string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			return err
		}
		switch entry.Name() {
		case ".git", "synthetic", "not-recorded":
			return filepath.SkipDir
		}
		if !strings.Contains(filepath.ToSlash(path), "/testdata/") {
			return nil
		}
		_, err = os.Stat(filepath.Join(path, manifestName))
		if err == nil || legacyRecording(path) {
			found = append(found, path)
			return filepath.SkipDir
		}
		return nil
	})
	return found, err
}

func TestRecordingsHoldNoContent(t *testing.T) {
	root := moduleRoot(t)
	recordings, err := recordingsUnder(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range committedRecordings(t) {
		if !slices.Contains(recordings, want) {
			t.Errorf("the guard does not reach %s", want)
		}
	}
	for _, dir := range recordings {
		problems, err := verifyDirectory(dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		name, _ := filepath.Rel(root, dir)
		for _, problem := range problems {
			t.Errorf("%s: %s", name, problem)
		}
	}
}

func TestTheGuardFindsAPlantedPath(t *testing.T) {
	committed := filepath.Join(moduleRoot(t), "internal/adapter/claudecode/testdata/2.1.316/subagent")
	root := t.TempDir()
	planted := filepath.Join(root, "internal/adapter/claudecode/testdata/2.1.316/subagent")
	transformDir(t, committed, planted, func(line *value) {
		if line.fieldText("querySource") == "agent:builtin:general-purpose" {
			setField(line, "querySource", stringValue("/home/someone/.claude"))
		}
	})
	written := filepath.Join(root, "internal/adapter/claudecode/testdata/not-recorded/nested")
	write(t, filepath.Join(written, "session.jsonl"), `{"cwd":"/home/someone"}`)
	recordings, err := recordingsUnder(root)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(recordings, []string{planted}) {
		t.Fatalf("recordings %v, want only %s", recordings, planted)
	}
	problems, _ := verifyDirectory(planted, nil)
	if !slices.ContainsFunc(problems, func(p string) bool {
		return strings.HasPrefix(p, "subagents/agent-1.jsonl") && strings.Contains(p, "home directory")
	}) {
		t.Errorf("a planted home directory in a subagent's file passed: %v", problems)
	}
}
