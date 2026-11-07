package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

// propertyRecording is one recorded run a line is mutated in: a Claude Code
// run with the session its stream names, or a Codex run's rollout file.
type propertyRecording struct {
	agent, dir, session string
}

var propertyRecordings = []propertyRecording{
	{"claude", filepath.Join(claudeData, "one-request"), "00000000-0000-4000-8000-000000000001"},
	{"claude", filepath.Join(claudeData, "subagent"), subagentSession},
	{"claude", filepath.Join(claudeData, "budget-stop"), "00000000-0000-4000-8000-000000000003"},
	{"claude", filepath.Join("..", "adapter", "claudecode", "testdata", "2.1.319", "current"), "00000000-0000-4000-8000-000000000001"},
	{"codex", filepath.Join(codexData, "exec"), ""},
}

const (
	streamFile  = "stream.jsonl"
	sessionFile = "session.jsonl"
	rolloutFile = "rollout.jsonl"
	// largestCount is 2^63-1, the largest count a reader can hold.
	largestCount = "9223372036854775807"
)

// The mutations of one recorded value.
const (
	mutateNegative = "negative"
	mutateFraction = "2.0"
	mutateNull     = "null"
	mutateString   = "string"
	mutateLargest  = "2^63-1"
	mutateObject   = "object to string"
	mutateRemove   = "key removed"
)

var mutationKinds = []string{mutateNegative, mutateFraction, mutateNull, mutateString, mutateLargest, mutateObject, mutateRemove}

// variant is one line of a recording with one value changed.
type variant struct {
	path, kind string
	value      any
}

// variants lists every single change to a decoded line: each number made
// negative, 2.0, null, a string and 2^63-1; each object made a string; each
// key removed.
func variants(value any, path string) []variant {
	switch value := value.(type) {
	case json.Number:
		negated := "-" + value.String()
		if strings.HasPrefix(value.String(), "-") {
			negated = strings.TrimPrefix(value.String(), "-")
		}
		return []variant{
			{path, mutateNegative, json.Number(negated)},
			{path, mutateFraction, json.Number("2.0")},
			{path, mutateNull, nil},
			{path, mutateString, value.String()},
			{path, mutateLargest, json.Number(largestCount)},
		}
	case map[string]any:
		encoded, _ := json.Marshal(value)
		found := []variant{{path, mutateObject, string(encoded)}}
		for _, key := range slices.Sorted(maps.Keys(value)) {
			without := maps.Clone(value)
			delete(without, key)
			found = append(found, variant{path + "." + key, mutateRemove, without})
			for _, inner := range variants(value[key], path+"."+key) {
				with := maps.Clone(value)
				with[key] = inner.value
				found = append(found, variant{inner.path, inner.kind, with})
			}
		}
		return found
	case []any:
		var found []variant
		for index, element := range value {
			for _, inner := range variants(element, path+"["+strconv.Itoa(index)+"]") {
				with := slices.Clone(value)
				with[index] = inner.value
				found = append(found, variant{inner.path, inner.kind, with})
			}
		}
		return found
	}
	return nil
}

// encodeLine writes a mutated line as the agent would: JSON, with no HTML
// escaping and no trailing line break.
func encodeLine(value any) []byte {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		panic(err)
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n"))
}

// lineVariants decodes one recorded line and lists its variants. In short
// mode it keeps one variant of each kind, a different site on each line, so
// every kind reaches every line and the run stays a few seconds long. A
// number it changes is a token count where the line holds one.
func lineVariants(t *testing.T, line []byte, number int) []variant {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("recorded line %d: %v", number, err)
	}
	all := variants(decoded, "$")
	if !testing.Short() {
		return all
	}
	var kept []variant
	for index, kind := range mutationKinds {
		var ofKind, counts []variant
		for _, v := range all {
			if v.kind == kind {
				ofKind = append(ofKind, v)
			}
			if v.kind == kind && strings.Contains(strings.ToLower(v.path), "tokens") {
				counts = append(counts, v)
			}
		}
		if len(counts) > 0 {
			ofKind = counts
		}
		if len(ofKind) > 0 {
			kept = append(kept, ofKind[(number*len(mutationKinds)+index)%len(ofKind)])
		}
	}
	return kept
}

// recordedFiles reads the files a recording's agent leaves, by their path
// inside the recording: Claude Code's stream, session file and subagent
// files, or Codex's rollout file. The fake codex writes its own stream.
func recordedFiles(t *testing.T, recording propertyRecording) map[string][]byte {
	t.Helper()
	dir := recording.dir
	if recording.agent == "codex" {
		return map[string][]byte{rolloutFile: mustRead(t, filepath.Join(dir, rolloutFile))}
	}
	files := map[string][]byte{}
	subagents, err := filepath.Glob(filepath.Join(dir, "subagents", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range append([]string{filepath.Join(dir, streamFile), filepath.Join(dir, sessionFile)}, subagents...) {
		name, err := filepath.Rel(dir, path)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = mustRead(t, path)
	}
	return files
}

// runFiles lays the files out as installClaudeRun lays a recording out, or
// as codexRun does with no pause, runs the fake agent over them, and returns
// what the run came to and the rows it wrote.
func runFiles(t *testing.T, recording propertyRecording, files map[string][]byte) (Result, []map[string]any, error) {
	t.Helper()
	if recording.agent == "codex" {
		return runCodexFiles(t, files)
	}
	session := recording.session
	config := t.TempDir()
	project := filepath.Join(config, "projects", "-work")
	for name, data := range files {
		path := filepath.Join(project, session+".jsonl")
		switch {
		case name == streamFile:
			path = filepath.Join(config, streamFile)
		case strings.HasPrefix(name, "subagents"):
			path = filepath.Join(project, session, name)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(fakeMode, "replay")
	t.Setenv(fakeLog, filepath.Join(config, "argv"))
	t.Setenv("COSTROID_FAKE_STREAM", filepath.Join(config, streamFile))
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	return runArgv(t, agentNamed(t, "claude"), "-p", "work")
}

func runCodexFiles(t *testing.T, files map[string][]byte) (Result, []map[string]any, error) {
	t.Helper()
	dir := t.TempDir()
	rollout := filepath.Join(dir, rolloutFile)
	if err := os.WriteFile(rollout, files[rolloutFile], 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeMode, "replay")
	t.Setenv(fakeLog, filepath.Join(dir, "progress"))
	t.Setenv(fakeRollout, rollout)
	t.Setenv(fakeNoPause, "1")
	t.Setenv("CODEX_HOME", t.TempDir())
	return runArgv(t, agentNamed(t, "codex"), "exec", "-m", "gpt-5.6-sol", "work")
}

func runArgv(t *testing.T, argv ...string) (Result, []map[string]any, error) {
	t.Helper()
	var rows bytes.Buffer
	result, err := Run(context.Background(), Options{
		Argv: argv, Budget: decimal.MustParse("5"),
		Rows: &rows, Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard, Catalog: pinned(t),
	})
	return result, parseRows(t, rows.Bytes()), err
}

// requestLines names the strings that tie a session file line to its
// request: the request's id, and the id of the api-request line it answers.
func requestLines(t *testing.T, files map[string][]byte) map[string][]string {
	t.Helper()
	ties := map[string][]string{}
	for name, data := range files {
		if name == streamFile || name == rolloutFile {
			continue
		}
		for _, line := range bytes.Split(data, []byte("\n")) {
			var entry struct {
				RequestID  string `json:"requestId"`
				RequestRef string `json:"requestRef"`
			}
			if json.Unmarshal(line, &entry) == nil && entry.RequestID != "" {
				ties[entry.RequestID] = append(ties[entry.RequestID], `"`+entry.RequestID+`"`, `"`+entry.RequestRef+`"`)
			}
		}
	}
	return ties
}

// touched says which requests a line can change. A session file line changes
// the requests it names. A stream line of a type the reader reads changes
// every request, and any other stream line changes none. A rollout line that
// states the version, the model, a task's start or a running total changes
// every request, because each total is read against the one before; any
// other rollout line changes none.
func touched(name string, line []byte, ties map[string][]string) func(request string) bool {
	if name == streamFile || name == rolloutFile {
		var envelope struct {
			Type    string `json:"type"`
			Payload struct {
				Type string `json:"type"`
			} `json:"payload"`
		}
		_ = json.Unmarshal(line, &envelope)
		read := []string{"system", "assistant", "result"}
		if name == rolloutFile {
			read = []string{"session_meta", "turn_context", "token_usage_record", "event_msg/token_count", "event_msg/task_started"}
		}
		every := slices.Contains(read, envelope.Type) || slices.Contains(read, envelope.Type+"/"+envelope.Payload.Type)
		return func(string) bool { return every }
	}
	return func(request string) bool {
		return slices.ContainsFunc(ties[request], func(tie string) bool { return bytes.Contains(line, []byte(tie)) })
	}
}

// baselineRun is a recording's own run: its rows by row id, and the request
// each row belongs to.
type baselineRun struct {
	result  Result
	rows    map[string]string
	request map[string]string
}

func runBaseline(t *testing.T, recording propertyRecording, files map[string][]byte) baselineRun {
	t.Helper()
	result, rows, err := runFiles(t, recording, files)
	if err != nil || len(rows) == 0 {
		t.Fatalf("the recording itself: %d rows, %v", len(rows), err)
	}
	base := baselineRun{result: result, rows: map[string]string{}, request: map[string]string{}}
	for _, request := range result.Report.Requests {
		for _, count := range request.Tokens.Counts() {
			base.request[focus.RowIdentity(request.ID, count.Class)] = request.ID
		}
	}
	for _, row := range rows {
		id, _ := row["x_CostroidRowId"].(string)
		if base.request[id] == "" {
			t.Fatalf("row %s belongs to no request the run read", id)
		}
		base.rows[id] = string(encodeLine(row))
	}
	return base
}

// assertNoNegative holds every row's quantities and amounts to plain
// non-negative decimals.
func assertNoNegative(t *testing.T, where string, rows []map[string]any) {
	t.Helper()
	for _, row := range rows {
		for _, column := range []string{"ConsumedQuantity", "PricingQuantity", "ListCost", "ListUnitPrice", "ContractedCost", "BilledCost", "EffectiveCost"} {
			text, _ := row[column].(string)
			if _, err := decimal.Parse(text); err != nil {
				t.Fatalf("%s: %s reads %q", where, column, text)
			}
		}
	}
}

// statesAReason says whether the run said why its rows are not the
// recording's: a reason for writing none, an enforcement grade the
// recording's run did not fall to, with its note, or a warning the
// recording's run does not give.
func statesAReason(base, mutated Result) bool {
	if mutated.Reason != "" && mutated.Reason.Sentence() != "" {
		return true
	}
	if mutated.Enforcement.Grade != base.Enforcement.Grade && mutated.Enforcement.Note != "" {
		return true
	}
	return slices.ContainsFunc(mutated.Report.Warnings, func(warning string) bool {
		return warning != "" && !slices.Contains(base.Report.Warnings, warning)
	})
}

// TestOneBadLine changes one value of one line of each recorded run at a
// time: a number made negative, 2.0, null, a string or 2^63-1, an object made
// a string, a key removed. Whatever the change, the run either keeps every
// row of every request the line does not touch, byte for byte, or states a
// reason: a refusal that writes no rows and names the reader's line, or a
// warning the recording does not give. No row it writes holds a negative
// quantity or amount.
func TestOneBadLine(t *testing.T) {
	outsideCIForRun(t)
	t.Setenv("ENABLE_PROMPT_CACHING_1H", "")
	tally := map[string]int{}
	for _, recording := range propertyRecordings {
		files := recordedFiles(t, recording)
		base := runBaseline(t, recording, files)
		ties := requestLines(t, files)
		for _, name := range slices.Sorted(maps.Keys(files)) {
			lines := bytes.Split(files[name], []byte("\n"))
			for index, line := range lines {
				if len(bytes.TrimSpace(line)) == 0 {
					continue
				}
				changes := touched(name, line, ties)
				for _, change := range lineVariants(t, line, index) {
					where := fmt.Sprintf("%s/%s:%d %s %s", filepath.Base(recording.dir), name, index+1, change.path, change.kind)
					mutated := slices.Clone(lines)
					mutated[index] = encodeLine(change.value)
					edited := maps.Clone(files)
					edited[name] = bytes.Join(mutated, []byte("\n"))
					tally[checkOneBadLine(t, where, base, recording, edited, changes)]++
				}
			}
		}
	}
	t.Logf("outcomes over every change: %v", tally)
}

// checkOneBadLine runs one changed recording and holds it to TestOneBadLine's
// property. It returns which way the property held.
func checkOneBadLine(t *testing.T, where string, base baselineRun, recording propertyRecording, files map[string][]byte, changes func(string) bool) string {
	t.Helper()
	result, rows, err := runFiles(t, recording, files)
	assertNoNegative(t, where, rows)
	if err != nil {
		var refused *ReaderError
		if !errors.As(err, &refused) || result.Reason != ReasonReaderRefused || len(rows) != 0 || ReaderLine(err.Error()) == "" {
			t.Fatalf("%s: the run failed with %v, reason %q, %d rows", where, err, result.Reason, len(rows))
		}
		return "refused with a reason"
	}
	if len(rows) == 0 {
		if result.Reason.Sentence() == "" {
			t.Fatalf("%s: no rows and no reason", where)
		}
		return "no rows, with a reason"
	}
	written := map[string]string{}
	for _, row := range rows {
		id, _ := row["x_CostroidRowId"].(string)
		written[id] = string(encodeLine(row))
	}
	untouched := 0
	for id, row := range base.rows {
		if changes(base.request[id]) {
			continue
		}
		untouched++
		if written[id] == row {
			continue
		}
		if statesAReason(base.result, result) {
			return "rows changed, with a reason stated"
		}
		t.Fatalf("%s: request %s's row %s changed with no reason stated:\nwas  %s\nnow  %s", where, base.request[id], id, row, written[id])
	}
	if untouched == 0 {
		return "every request touched"
	}
	return "untouched rows kept"
}
