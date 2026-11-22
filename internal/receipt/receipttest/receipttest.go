// Package receipttest writes synthetic Claude Code session files for the
// tests of costroid receipt. Each is a copy of a session recorded under the
// Claude Code adapter's test data, with the working directory and the git
// branch planted on its lines: the recordings' scrubber drops the branch, so
// every copy that names one is synthetic.
package receipttest

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The recorded sessions a test copies, as paths under the adapter's test
// data. Subagent's directory holds one subagent's file beside it.
const (
	// OneRequest is Claude Code 2.1.319's session of one request.
	OneRequest = "2.1.319/current/session.jsonl"
	// Subagent is Claude Code 2.1.316's session of three requests and a
	// subagent's one.
	Subagent = "2.1.316/subagent/session.jsonl"
)

// Place says what one line of a copy records: its working directory and its
// branch. An empty value leaves the key out of the line.
type Place func(line Line) (dir, branch string)

// Line is one line of a recorded file, as a copy plants it.
type Line struct {
	// File is the file's name, Index the line's place in it from 0.
	File  string
	Index int
	// Fields are the line's own fields, which an edit may change.
	Fields map[string]any
}

// projectName is what Claude Code replaces in a working directory to name its
// project directory.
var projectName = regexp.MustCompile(`[^A-Za-z0-9]`)

// testdata is the Claude Code adapter's test data.
func testdata() string {
	_, here, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(here), "..", "..", "adapter", "claudecode", "testdata")
}

// Write copies the recorded session into config, under the project
// directory Claude Code names for project, as session id, with its
// subagents' files. place plants each line's working directory and branch,
// and edit, when given, changes the line after.
func Write(t testing.TB, recorded, config, project, id string, place Place, edit func(Line)) {
	t.Helper()
	source := filepath.Join(testdata(), recorded)
	directory := filepath.Join(config, "projects", projectName.ReplaceAllString(project, "-"))
	copyFile(t, source, filepath.Join(directory, id+".jsonl"), place, edit)
	subagents, err := filepath.Glob(filepath.Join(filepath.Dir(source), "subagents", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range subagents {
		copyFile(t, file, filepath.Join(directory, id, "subagents", filepath.Base(file)), place, edit)
	}
}

func copyFile(t testing.TB, source, target string, place Place, edit func(Line)) {
	t.Helper()
	file, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var out strings.Builder
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 1<<24)
	for index := 0; scanner.Scan(); index++ {
		line := Line{File: filepath.Base(source), Index: index}
		if err := json.Unmarshal(scanner.Bytes(), &line.Fields); err != nil {
			t.Fatal(err)
		}
		dir, branch := place(line)
		delete(line.Fields, "cwd")
		delete(line.Fields, "gitBranch")
		if dir != "" {
			line.Fields["cwd"] = dir
		}
		if branch != "" {
			line.Fields["gitBranch"] = branch
		}
		if edit != nil {
			edit(line)
		}
		encoded, err := json.Marshal(line.Fields)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(encoded)
		out.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(out.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Requests counts the distinct requests a recorded session's main file and
// its subagents' files answer, and adds up their input and output tokens as
// the files state them, for a test to hold a receipt to.
func Requests(t testing.TB, recorded string) (requests int, input, output int64) {
	t.Helper()
	more, err := filepath.Glob(filepath.Join(testdata(), filepath.Dir(recorded), "subagents", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	files := append([]string{filepath.Join(testdata(), recorded)}, more...)
	last := map[string][2]int64{}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, text := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var line struct {
				Type      string `json:"type"`
				RequestID string `json:"requestId"`
				Message   struct {
					Usage struct {
						Input  int64 `json:"input_tokens"`
						Output int64 `json:"output_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal([]byte(text), &line) == nil && line.Type == "assistant" && line.RequestID != "" {
				last[line.RequestID] = [2]int64{line.Message.Usage.Input, line.Message.Usage.Output}
			}
		}
	}
	for _, counts := range last {
		input += counts[0]
		output += counts[1]
	}
	return len(last), input, output
}

// readKeys are the keys of a session line the reader reads, beside the
// message's model and usage.
var readKeys = []string{"type", "sessionId", "isSidechain", "version", "id", "requestId", "requestRef", "timestamp", "cwd", "gitBranch"}

// Plant puts canary in every string a line holds outside what the reader
// reads, in every array and object under it.
func Plant(canary string) func(Line) {
	var walk func(value any) any
	walk = func(value any) any {
		switch value := value.(type) {
		case string:
			return canary
		case []any:
			for index := range value {
				value[index] = walk(value[index])
			}
		case map[string]any:
			for key := range value {
				value[key] = walk(value[key])
			}
		}
		return value
	}
	return func(line Line) {
		for key, value := range line.Fields {
			if slices.Contains(readKeys, key) {
				continue
			}
			message, isMessage := value.(map[string]any)
			if key != "message" || !isMessage {
				line.Fields[key] = walk(value)
				continue
			}
			for inner := range message {
				if inner != "model" && inner != "usage" {
					message[inner] = walk(message[inner])
				}
			}
		}
		line.Fields["toolUseResult"] = map[string]any{"stdout": canary, "file": map[string]any{"content": canary}}
		line.Fields["summary"] = canary
	}
}
