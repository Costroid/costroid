package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The values below are planted by the tests. None came from a run.
const (
	plantedPrompt = "Review the pull request, then reply with the word ok. Planted by a test."
	plantedKey    = "sk-ant-api03-planted0123456789abcdefghijklmnopqrstuvwxyz"
	plantedPath   = "/srv/checkout/planted-app"
	plantedBranch = "feature/planted-branch-name"
)

// moduleRoot is the repository's root, two levels above this package.
func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("no go.mod at %s: %v", root, err)
	}
	return root
}

// committedRecordings are the recorded runs this repository held on 2026-11-01.
func committedRecordings(t *testing.T) []string {
	t.Helper()
	root := moduleRoot(t)
	var dirs []string
	for _, pattern := range []string{
		"internal/adapter/claudecode/testdata/2.1.316/*",
		"internal/adapter/claudecode/testdata/2.1.319/*",
		"internal/adapter/codex/testdata/0.164.0-alpha.11.1/*",
	} {
		found, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, found...)
	}
	if len(dirs) != 9 {
		t.Fatalf("found %d committed recordings, want 9: %v", len(dirs), dirs)
	}
	return dirs
}

// transformDir copies every data file under from to to, passing each line
// through change first, and writes each file as the recordings hold it.
func transformDir(t *testing.T, from, to string, change func(line *value)) {
	t.Helper()
	files, err := readRecording(from)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		for _, line := range file.lines {
			change(line)
		}
		target := filepath.Join(to, filepath.FromSlash(file.name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, file.encode(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func setField(v *value, key string, item *value) {
	for i, k := range v.keys {
		if k == key {
			v.items[i] = item
			return
		}
	}
	v.keys = append(v.keys, key)
	v.items = append(v.items, item)
}

// plant adds what a raw run carries and a recording must not: a working
// directory, a branch, a key, and prompt text in every content block and every
// Codex payload. With raw false it adds the working directories alone, as the
// scrub should leave them.
func plant(raw bool) func(*value) {
	return func(line *value) {
		cwd := placeholderPath
		if raw {
			cwd = plantedPath
		}
		setField(line, "cwd", stringValue(cwd))
		payload := line.field("payload")
		if payload != nil {
			setField(payload, "cwd", stringValue(cwd))
		}
		if !raw {
			return
		}
		setField(line, "gitBranch", stringValue(plantedBranch))
		setField(line, "plantedCredential", stringValue(plantedKey))
		for _, block := range elements(line.field("message").field("content")) {
			setField(block, "text", stringValue(plantedPrompt))
		}
		if payload != nil {
			setField(payload, "content", &value{kind: kindArray, items: []*value{stringValue(plantedPrompt)}})
		}
	}
}

// replacePlaceholders puts each placeholder key's placeholder in place of its value.
func replacePlaceholders(v *value, path string) {
	switch v.kind {
	case kindObject:
		for i, key := range v.keys {
			p := child(path, key)
			if placeholder, ok := placeholders[p]; ok && v.items[i].kind == kindString {
				v.items[i] = stringValue(placeholder)
				continue
			}
			replacePlaceholders(v.items[i], p)
		}
	case kindArray:
		for _, item := range v.items {
			replacePlaceholders(item, path+"[]")
		}
	}
}

func scrubbed(t *testing.T, raw string, prompts []string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "out")
	if _, err := scrubDirectory(raw, out, prompts); err != nil {
		t.Fatal(err)
	}
	return out
}

// sameFiles fails on every data file of want that got lacks or holds otherwise.
func sameFiles(t *testing.T, got, want string) {
	t.Helper()
	files, err := readRecording(want)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		wantData, _ := os.ReadFile(filepath.Join(want, file.name))
		gotData, err := os.ReadFile(filepath.Join(got, file.name))
		if err != nil {
			t.Errorf("%s: %v", file.name, err)
			continue
		}
		if !bytes.Equal(gotData, wantData) {
			t.Errorf("%s differs:\n got %s\nwant %s", file.name, firstDifferentLine(gotData, wantData), firstDifferentLine(wantData, gotData))
		}
	}
}

func firstDifferentLine(a, b []byte) string {
	al, bl := strings.Split(string(a), "\n"), strings.Split(string(b), "\n")
	for i := range al {
		if i >= len(bl) || al[i] != bl[i] {
			return al[i]
		}
	}
	return "(no more lines)"
}

func TestScrubReproducesCommittedFixtures(t *testing.T) {
	for _, dir := range committedRecordings(t) {
		name := filepath.Base(filepath.Dir(dir)) + "/" + filepath.Base(dir)
		t.Run(name, func(t *testing.T) {
			// The committed Codex exec and resume streams kept the agent's
			// own text and a command. The scrub replaces those values, and
			// nothing else, with @content.
			want := filepath.Join(t.TempDir(), "want")
			transformDir(t, dir, want, func(line *value) { replacePlaceholders(line, "") })
			sameFiles(t, scrubbed(t, dir, nil), want)

			raw := filepath.Join(t.TempDir(), "raw")
			transformDir(t, dir, raw, plant(true))
			wantPlanted := filepath.Join(t.TempDir(), "want-planted")
			transformDir(t, want, wantPlanted, plant(false))
			got := scrubbed(t, raw, []string{plantedPrompt})
			sameFiles(t, got, wantPlanted)
			for _, planted := range []string{plantedPrompt, plantedKey, plantedPath, plantedBranch, "gitBranch", "plantedCredential"} {
				if hit := grepDir(t, got, planted); hit != "" {
					t.Errorf("%s still holds %q", hit, planted)
				}
			}
		})
	}
}

func grepDir(t *testing.T, dir, needle string) string {
	t.Helper()
	var hit string
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err == nil && bytes.Contains(data, []byte(needle)) {
			hit = path
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return hit
}

func TestScrubGivesEachRawIDOneSyntheticID(t *testing.T) {
	raw := t.TempDir()
	write(t, filepath.Join(raw, "stream.jsonl"),
		`{"type":"system","subtype":"init","session_id":"3f1c9a52-7b1d-4e1f-9a7e-0c2b5d6e7f80"}`,
		`{"type":"assistant","session_id":"3f1c9a52-7b1d-4e1f-9a7e-0c2b5d6e7f80","request_id":"req_011CXyZ","message":{"id":"msg_01Abc","usage":{"input_tokens":3}}}`,
		`{"type":"assistant","session_id":"3f1c9a52-7b1d-4e1f-9a7e-0c2b5d6e7f80","request_id":"req_fixture_0001","message":{"id":"msg_fixture_0001","usage":{"input_tokens":4}}}`)
	write(t, filepath.Join(raw, "session.jsonl"),
		`{"type":"assistant","sessionId":"3f1c9a52-7b1d-4e1f-9a7e-0c2b5d6e7f80","requestId":"req_011CXyZ","message":{"id":"msg_01Abc"}}`)
	out := scrubbed(t, raw, nil)
	stream, _ := os.ReadFile(filepath.Join(out, "stream.jsonl"))
	session, _ := os.ReadFile(filepath.Join(out, "session.jsonl"))
	for _, want := range []string{`"session_id":"00000000-0000-4000-8000-000000000001"`, `"request_id":"req_fixture_0002"`, `"id":"msg_fixture_0002"`, `"request_id":"req_fixture_0001"`} {
		if !bytes.Contains(stream, []byte(want)) {
			t.Errorf("stream lacks %s:\n%s", want, stream)
		}
	}
	want := `{"type":"assistant","sessionId":"00000000-0000-4000-8000-000000000001","requestId":"req_fixture_0002","message":{"id":"msg_fixture_0002"}}` + "\n"
	if string(session) != want {
		t.Errorf("session\n got %s\nwant %s", session, want)
	}
}

func write(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRefusesContent(t *testing.T) {
	committed := filepath.Join(moduleRoot(t), "internal/adapter/claudecode/testdata/2.1.316/one-request")
	clean := scrubbed(t, committed, nil)
	if problems, err := verifyDirectory(clean, []string{plantedPrompt}); err != nil || len(problems) > 0 {
		t.Fatalf("a clean recording was refused: %v %v", problems, err)
	}
	// Each case plants one thing into the stream's init line, or one file.
	setModelValue := func(model *value) func(*value) {
		return func(line *value) {
			if isInit(line) {
				setField(line, "model", model)
			}
		}
	}
	setModel := func(text string) func(*value) { return setModelValue(stringValue(text)) }
	cases := []struct {
		name   string
		change func(*value)
		file   string
		want   string
	}{
		{"an Anthropic key", setModel("claude " + plantedKey), "", "holds an Anthropic key"},
		{"an OpenAI key", setModel("sk-proj-planted0123456789"), "", "holds an API key"},
		{"a GitHub token", setModel("ghp_planted0123456789"), "", "holds a GitHub token"},
		{"an Actions token", setModel("ghs_planted0123456789"), "", "holds a GitHub token"},
		{"a fine-grained token", setModel("github_pat_planted0123456789"), "", "holds a GitHub token"},
		{"a Linux home", setModel("/home/someone/project"), "", "holds a home directory"},
		{"a macOS home", setModel("/Users/someone/project"), "", "holds a home directory"},
		{"a runner workspace", setModel("D:/a/runner/work/app"), "", "holds a runner's workspace"},
		{"an escaped home", setModelValue(&value{kind: kindString, raw: `"\/home\/someone"`, text: "/home/someone"}), "", "holds a home directory"},
		{"the prompt", setModel("said: " + plantedPrompt), "", "holds the run's prompt"},
		{"a key outside the allowlist", func(line *value) {
			if isInit(line) {
				setField(line, "gitBranch", stringValue("main"))
			}
		}, "", "key outside the allowlist: gitBranch"},
		{"a raw session id", func(line *value) {
			setField(line, "session_id", stringValue("3f1c9a52-7b1d-4e1f-9a7e-0c2b5d6e7f80"))
		}, "", "session_id is not a synthetic session id"},
		{"a message as text", func(line *value) {
			if line.fieldText("type") == "assistant" {
				setField(line, "message", stringValue("the agent's reply"))
			}
		}, "", "content at message"},
		{"content blocks with text", func(line *value) {
			for _, block := range elements(line.field("message").field("content")) {
				setField(block, "text", stringValue("the agent's reply"))
			}
		}, "", "key outside the allowlist: message.content[].text"},
		{"a file that is not JSON", func(*value) {}, "notes.txt", "is not a .jsonl or .json file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "recording")
			transformDir(t, clean, dir, tc.change)
			copyManifest(t, clean, dir)
			if tc.file != "" {
				write(t, filepath.Join(dir, tc.file), "a raw note")
			}
			problems, err := verifyDirectory(dir, []string{plantedPrompt})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, tc.want) }) {
				t.Errorf("verify did not say %q; it said %v", tc.want, problems)
			}
			promptFile := filepath.Join(t.TempDir(), "prompt.txt")
			write(t, promptFile, plantedPrompt)
			if code := run(context.Background(), []string{"verify", "--prompt-file", promptFile, dir}, os.Getenv, nil, io.Discard, io.Discard); code != exitRefused {
				t.Errorf("verify exited %d, want %d", code, exitRefused)
			}
		})
	}
	t.Run("a directory no manifest marks", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "recording")
		transformDir(t, clean, dir, func(*value) {})
		problems, _ := verifyDirectory(dir, nil)
		if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, "holds no manifest.json") }) {
			t.Errorf("verify passed a directory with no manifest: %v", problems)
		}
	})
	var stdout bytes.Buffer
	if code := run(context.Background(), []string{"verify", clean}, os.Getenv, nil, &stdout, io.Discard); code != 0 || stdout.String() != "ok\n" {
		t.Errorf("verify on a clean recording exited %d and printed %q", code, stdout.String())
	}
}

func copyManifest(t *testing.T, from, to string) {
	t.Helper()
	m, err := readManifest(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(to, m); err != nil {
		t.Fatal(err)
	}
}

// elements is an array's items, or none when v is absent.
func elements(v *value) []*value {
	if v == nil {
		return nil
	}
	return v.items
}
