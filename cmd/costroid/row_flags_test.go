package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// rowsOf reads every row of a rows file, each column as its line states it.
func rowsOf(t *testing.T, path string) []map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]json.RawMessage
	for line := range bytes.Lines(data) {
		var row map[string]json.RawMessage
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatalf("%v: %s", err, line)
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		t.Fatalf("%s holds no rows", path)
	}
	return rows
}

func tagsOf(t *testing.T, row map[string]json.RawMessage) map[string]string {
	t.Helper()
	var tags map[string]string
	if err := json.Unmarshal(row["Tags"], &tags); err != nil {
		t.Fatal(err)
	}
	return tags
}

// TestTagFlag names the three keys on run and on ingest-run. Each lands on
// every row under costroid/. An unnamed key, a value shaped like a key and a
// value outside the allowlist each exit 125 before a rows file exists, and the
// refused value is never printed.
func TestTagFlag(t *testing.T) {
	tagged := []string{"--tag", "work_item=ENG-142", "--tag", "arm=b", "--tag", "team=platform"}
	want := map[string]string{"costroid/work_item": "ENG-142", "costroid/arm": "b", "costroid/team": "platform"}
	commands := map[string]func(t *testing.T, out string, flags ...string) []string{
		"run": func(t *testing.T, out string, flags ...string) []string {
			return append(append([]string{"run", "--budget-usd", "5", "--out", out}, flags...), "--", replayClaude(t), "-p", "work")
		},
		"ingest-run": func(_ *testing.T, out string, flags ...string) []string {
			return append([]string{"ingest-run", "--execution-file", executionFile, "--out", out}, flags...)
		},
	}
	for name, command := range commands {
		t.Run(name, func(t *testing.T) {
			clearCI(t)
			out := filepath.Join(t.TempDir(), "rows.jsonl")
			if code, _, stderr := execute(t, command(t, out, tagged...)...); code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			for i, row := range rowsOf(t, out) {
				tags := tagsOf(t, row)
				for key, value := range want {
					if tags[key] != value {
						t.Errorf("row %d: %s reads %q, want %q", i+1, key, tags[key], value)
					}
				}
			}
		})
		for refusal, tc := range map[string]struct {
			tag, stderr string
		}{
			"an unnamed key":         {"branch=main", "--tag takes the key work_item, arm or team"},
			"a key as a value":       {"team=sk-ant-api03-sentinel", "this value reads like a key"},
			"outside the allowlist":  {"arm=<b>", "--tag arm takes 1 to 128 letters"},
			"past the allowlist cap": {"team=" + strings.Repeat("a", 129), "--tag team takes 1 to 128 letters"},
			"an empty value":         {"team=", "--tag team takes 1 to 128 letters"},
			"no issue key":           {"work_item=eng-142", "--tag work_item takes an issue key"},
			"no equals sign":         {"team", "--tag takes KEY=VALUE"},
		} {
			t.Run(name+"/"+refusal, func(t *testing.T) {
				clearCI(t)
				out := filepath.Join(t.TempDir(), "rows.jsonl")
				code, stdout, stderr := execute(t, command(t, out, "--tag", tc.tag)...)
				if code != exitFailure || !strings.Contains(stderr, tc.stderr) {
					t.Errorf("exit %d, stderr %q", code, stderr)
				}
				if _, value, _ := strings.Cut(tc.tag, "="); value != "" && strings.Contains(stdout+stderr, value) {
					t.Errorf("the refused value is printed: %q %q", stdout, stderr)
				}
				if _, err := os.Stat(out); !os.IsNotExist(err) {
					t.Errorf("a rows file exists: %v", err)
				}
			})
		}
	}
	if code, _, stderr := execute(t, "ingest-run", "--execution-file", executionFile, "--out", filepath.Join(t.TempDir(), "rows.jsonl"), "--tag", "team=a", "--tag", "team=b"); code != exitFailure || !strings.Contains(stderr, "--tag team is given twice") {
		t.Errorf("a key given twice: exit %d, stderr %q", code, stderr)
	}
}

// measuredExecutionFile is a recorded claude-code-action run on an API key, so
// its rows carry a figure in EffectiveCost.
const measuredExecutionFile = "../../internal/adapter/claudecode/testdata/2.1.316/execution-file/claude-execution-output.json"

// TestFocusProfileFlagWritesStrictRows reads one recorded run in each profile.
// The strict rows are the default rows with the figure moved from
// EffectiveCost to x_CostroidMeasuredCost, and ingest-run states the same
// figure for both.
func TestFocusProfileFlagWritesStrictRows(t *testing.T) {
	clearCI(t)
	directory := t.TempDir()
	summaries := map[string]string{}
	for _, profile := range []string{"default", "strict"} {
		out := filepath.Join(directory, profile+".jsonl")
		code, _, stderr := execute(t, "ingest-run", "--execution-file", measuredExecutionFile, "--out", out, "--focus-profile", profile)
		if code != 0 {
			t.Fatalf("%s: exit %d: %s", profile, code, stderr)
		}
		summaries[profile] = strings.ReplaceAll(stderr, profile+".jsonl", "rows.jsonl")
	}
	if summaries["default"] != summaries["strict"] || !strings.Contains(summaries["strict"], "at list, Measured") {
		t.Errorf("ingest-run states the profiles differently:\n%s\n%s", summaries["default"], summaries["strict"])
	}
	defaults, stricts := rowsOf(t, filepath.Join(directory, "default.jsonl")), rowsOf(t, filepath.Join(directory, "strict.jsonl"))
	if len(defaults) != len(stricts) {
		t.Fatalf("%d default rows, %d strict", len(defaults), len(stricts))
	}
	measured := 0
	for i := range defaults {
		strict, plain := stricts[i], defaults[i]
		if string(strict["EffectiveCost"]) != `"0"` || string(strict["BilledCost"]) != `"0"` || !bytes.Equal(strict["x_CostroidMeasuredCost"], plain["EffectiveCost"]) {
			t.Errorf("row %d: BilledCost %s, EffectiveCost %s, x_CostroidMeasuredCost %s; default EffectiveCost %s",
				i+1, strict["BilledCost"], strict["EffectiveCost"], strict["x_CostroidMeasuredCost"], plain["EffectiveCost"])
		}
		if string(plain["EffectiveCost"]) != `"0"` {
			measured++
		}
	}
	if measured == 0 {
		t.Fatal("no default row carries a figure in EffectiveCost, so the test proves nothing")
	}
	if code, _, stderr := execute(t, "ingest-run", "--execution-file", measuredExecutionFile, "--out", filepath.Join(directory, "x.jsonl"), "--focus-profile", "loose"); code != exitFailure || !strings.Contains(stderr, "--focus-profile takes default or strict") {
		t.Errorf("an unknown profile: exit %d, stderr %q", code, stderr)
	}
}

// TestTagsDocExampleRuns runs docs/tags.md's command on a recorded run. Every
// row holds the tags the page shows.
func TestTagsDocExampleRuns(t *testing.T) {
	page := readFile(t, "../../docs/tags.md")
	flags, agent, found := strings.Cut(docBlock(t, page, "sh", "costroid run "), " -- ")
	if !found || !strings.HasPrefix(agent, "claude -p ") {
		t.Fatalf("the example runs %q", agent)
	}
	var want map[string]string
	if err := json.Unmarshal([]byte(docBlock(t, page, "json", "{")), &want); err != nil || len(want) != 3 {
		t.Fatalf("the page's tags %v: %v", want, err)
	}
	claude := replayClaude(t)
	t.Chdir(t.TempDir())
	args := append(strings.Fields(flags)[1:], "--", claude, "-p", "work")
	if code, _, stderr := execute(t, args...); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for i, row := range rowsOf(t, "rows.jsonl") {
		tags := tagsOf(t, row)
		for key, value := range want {
			if tags[key] != value {
				t.Errorf("row %d: %s reads %q, the page %q", i+1, key, tags[key], value)
			}
		}
	}
}

// TestFocusProfileDocTableIsWhatTheProfilesWrite runs docs/focus-profile.md's
// command on the recorded run the page names, with and without the profile,
// and reads the page's row in each.
func TestFocusProfileDocTableIsWhatTheProfilesWrite(t *testing.T) {
	clearCI(t)
	page := readFile(t, "../../docs/focus-profile.md")
	words := strings.Fields(docBlock(t, page, "sh", "costroid ingest-run "))
	if !slices.Equal(words[2:4], []string{"--from", "execution-file=claude-execution-output.json"}) || !slices.Equal(words[6:], []string{"--focus-profile", "strict"}) {
		t.Fatalf("the example reads %q", words)
	}
	sku := regexp.MustCompile("For the `([^`]+)` row").FindStringSubmatch(page)
	if sku == nil {
		t.Fatal("the page names no row")
	}
	source := measuredExecution(t)
	t.Chdir(t.TempDir())
	columns := map[string][]json.RawMessage{}
	for _, profile := range [][]string{nil, words[6:]} {
		args := append([]string{"ingest-run", "--from", "execution-file=" + source, "--out", "rows.jsonl"}, profile...)
		if code, _, stderr := execute(t, args...); code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		row := rowNamed(t, rowsOf(t, "rows.jsonl"), sku[1])
		for _, column := range []string{"BilledCost", "EffectiveCost", "ListCost", "x_CostroidMeasuredCost"} {
			columns[column] = append(columns[column], row[column])
		}
		if err := os.Remove("rows.jsonl"); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(runResultPath("rows.jsonl")); err != nil {
			t.Fatal(err)
		}
	}
	lines := regexp.MustCompile("(?m)^\\| `(\\w+)` +\\| ([^|]+?) +\\| ([^|]+?) +\\|$").FindAllStringSubmatch(page, -1)
	if len(lines) != len(columns) {
		t.Fatalf("the page's table has %d rows, want %d", len(lines), len(columns))
	}
	for _, line := range lines {
		for i, cell := range line[2:] {
			want := strings.Trim(cell, "`")
			if cell == "absent" {
				want = ""
			}
			if got := string(columns[line[1]][i]); got != want {
				t.Errorf("%s, profile %d: %s, the page %s", line[1], i+1, got, cell)
			}
		}
	}
}

func measuredExecution(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(measuredExecutionFile)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func rowNamed(t *testing.T, rows []map[string]json.RawMessage, sku string) map[string]json.RawMessage {
	t.Helper()
	for _, row := range rows {
		if string(row["SkuId"]) == `"`+sku+`"` {
			return row
		}
	}
	t.Fatalf("no row of SkuId %s", sku)
	return nil
}
