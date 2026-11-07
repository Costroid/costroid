package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const executionFile = "../../internal/adapter/claudecode/testdata/not-recorded/execution-file/claude-execution-output.json"

func clearCI(t *testing.T) {
	t.Helper()
	for _, variable := range []string{"GITHUB_ACTIONS", "GITLAB_CI", credentialVariable, billingAccountVariable} {
		t.Setenv(variable, "")
	}
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
}

func TestIngestRunWritesTheActionsRowsAtTheRecordedTotal(t *testing.T) {
	clearCI(t)
	out := filepath.Join(t.TempDir(), "rows.jsonl")
	code, _, stderr := execute(t, "ingest-run", "--execution-file", executionFile, "--out", out, "--credential", "apikey_01ExampleCi")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{"USD 0.001619725 at list", "List-equivalent, not billed", "not started by costroid"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("no %q in %s", want, stderr)
		}
	}
	row := firstRow(t, out)
	if row["SubAccountId"] != "apikey_01ExampleCi" {
		t.Errorf("SubAccountId %v", row["SubAccountId"])
	}
	tags, _ := row["Tags"].(map[string]any)
	for tag, want := range map[string]string{
		"costroid/enforcement_grade": "none", "costroid/cost_source": "execution_file",
		"costroid/run_identity": "unverified", "costroid/attribution_grade": "unattributed",
	} {
		if tags[tag] != want {
			t.Errorf("%s reads %v", tag, tags[tag])
		}
	}
}

func TestIngestRunStatesTheCIContextAsClaims(t *testing.T) {
	clearCI(t)
	event := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(event, []byte(pullRequestEvent()), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY": "example/widgets", "GITHUB_RUN_ID": "1234",
		"GITHUB_SHA": mergeSHA, "GITHUB_EVENT_PATH": event, "GITHUB_SERVER_URL": "https://github.com",
	} {
		t.Setenv(name, value)
	}
	out := filepath.Join(t.TempDir(), "rows.jsonl")
	if code, _, stderr := execute(t, "ingest-run", "--execution-file", executionFile, "--out", out); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	tags, _ := firstRow(t, out)["Tags"].(map[string]any)
	for tag, want := range map[string]string{
		"costroid/repo": "github.com/example/widgets", "costroid/pr": "7", "costroid/commit_sha": headCommit,
		"costroid/workflow_run_id": "1234", "costroid/run_identity": "unverified", "costroid/attribution_grade": "unattributed",
	} {
		if tags[tag] != want {
			t.Errorf("%s reads %v", tag, tags[tag])
		}
	}
}

func TestRunTakesTheCredentialFromTheVariableAndTheFlagBeatsIt(t *testing.T) {
	claude := replayClaude(t)
	t.Setenv(credentialVariable, "apikey_variable")
	out := filepath.Join(t.TempDir(), "rows.jsonl")
	if code, _, stderr := execute(t, "run", "--budget-usd", "5", "--out", out, "--", claude, "-p", "work"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if got := firstRow(t, out)["SubAccountId"]; got != "apikey_variable" {
		t.Errorf("variable: %v", got)
	}
	if code, _, stderr := execute(t, "run", "--budget-usd", "5", "--out", out, "--credential", "apikey_flag", "--", claude, "-p", "work"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if got := firstRow(t, out)["SubAccountId"]; got != "apikey_flag" {
		t.Errorf("flag: %v", got)
	}
}

func TestAKeyPastedAsACredentialIsRefusedWithoutBeingEchoed(t *testing.T) {
	out := filepath.Join(t.TempDir(), "rows.jsonl")
	const key = "sk-ant-api03-sentinel"
	for _, args := range [][]string{
		{"run", "--budget-usd", "1", "--out", out, "--credential", key, "--", "claude", "-p", "x"},
		{"ingest-run", "--execution-file", executionFile, "--out", out, "--credential", key},
	} {
		code, stdout, stderr := execute(t, args...)
		if code != exitFailure || strings.Contains(stdout+stderr, key) {
			t.Errorf("exit %d, %q %q", code, stdout, stderr)
		}
	}
}

func TestIngestRunRefusesAMissingFileAndWritesNoRows(t *testing.T) {
	clearCI(t)
	out := filepath.Join(t.TempDir(), "rows.jsonl")
	code, _, _ := execute(t, "ingest-run", "--execution-file", filepath.Join(t.TempDir(), "gone.json"), "--out", out)
	if code != exitFailure {
		t.Fatalf("exit %d", code)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("a rows file exists: %v", err)
	}
}

func TestIngestRunNeedsItsTwoFlags(t *testing.T) {
	for _, args := range [][]string{{"ingest-run"}, {"ingest-run", "--out", "x"}, {"ingest-run", "--execution-file", "x"}} {
		if code, _, _ := execute(t, args...); code != exitFailure {
			t.Errorf("%v exited %d", args, code)
		}
	}
}

func TestRowsAfterAnEmptyResultAreAFileWithNothingInIt(t *testing.T) {
	clearCI(t)
	empty := filepath.Join(t.TempDir(), "execution.json")
	body, err := json.Marshal([]map[string]any{{"type": "result", "session_id": "s"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(empty, body, 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "rows.jsonl")
	if code, _, stderr := execute(t, "ingest-run", "--execution-file", empty, "--out", out); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if info, err := os.Stat(out); err != nil || info.Size() != 0 {
		t.Fatalf("%v, %v", info, err)
	}
}

// The first file read from a real claude-code-action run, on an API key: one
// run, two models, and a total equal to the run's own figure to the last digit.
func TestIngestRunReadsARecordedActionRunWithTwoModels(t *testing.T) {
	clearCI(t)
	out := filepath.Join(t.TempDir(), "rows.jsonl")
	code, _, stderr := execute(t, "ingest-run", "--execution-file",
		"../../internal/adapter/claudecode/testdata/2.1.316/execution-file/claude-execution-output.json", "--out", out)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{"claude-code, one run, 2 models", "USD 0.0516717 at list", "Measured"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("no %q in %s", want, stderr)
		}
	}
}

// codexActionHome is the recorded openai/codex-action run in a Codex home made
// for its job, written at the run's last second.
func codexActionHome(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../internal/adapter/codex/testdata/0.164.0-alpha.11.1/action/rollout.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	day := filepath.Join(home, "sessions", "2026", "11", "07")
	path := filepath.Join(day, "rollout-2026-11-07T13-28-51-00000000-0000-4000-8000-000000000001.jsonl")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	written := time.Date(2026, 11, 7, 13, 28, 57, 0, time.UTC)
	if err := os.Chtimes(path, written, written); err != nil {
		t.Fatal(err)
	}
	return home
}

// Each alias reads as its row of the table: the same rows, the same object
// beside them and the same lines, byte for byte.
func TestIngestRunAliasesStillWork(t *testing.T) {
	clearCI(t)
	home := codexActionHome(t)
	const since = "2026-11-07T13:27:51Z"
	for _, tc := range []struct {
		name         string
		alias, table []string
		want         string
	}{
		{"execution-file", []string{"--execution-file", executionFile}, []string{"--from", "execution-file=" + executionFile}, "USD 0.001619725 at list"},
		{"codex-home", []string{"--codex-home", home, "--since", since}, []string{"--from", "codex-home=" + home, "--since", since}, "USD 0.029324"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			read := func(name string, args []string) (string, []byte, []byte) {
				out := filepath.Join(dir, name, "rows.jsonl")
				if err := os.Mkdir(filepath.Dir(out), 0o755); err != nil {
					t.Fatal(err)
				}
				code, _, stderr := execute(t, append(append([]string{"ingest-run"}, args...), "--out", out)...)
				if code != 0 {
					t.Fatalf("%v: exit %d: %s", args, code, stderr)
				}
				rows, _ := os.ReadFile(out)
				object, _ := os.ReadFile(runResultPath(out))
				return strings.ReplaceAll(stderr, out, "rows.jsonl"), rows, object
			}
			aliasErr, aliasRows, aliasObject := read("alias", tc.alias)
			tableErr, tableRows, tableObject := read("table", tc.table)
			if !strings.Contains(aliasErr, tc.want) {
				t.Errorf("the alias read no %q: %s", tc.want, aliasErr)
			}
			if aliasErr != tableErr {
				t.Errorf("the lines differ:\nalias %s\ntable %s", aliasErr, tableErr)
			}
			if len(aliasRows) == 0 || !bytes.Equal(aliasRows, tableRows) || !bytes.Equal(aliasObject, tableObject) {
				t.Errorf("the rows or the object differ:\nalias %s\ntable %s", aliasRows, tableRows)
			}
		})
	}
}

// Two sources are refused by one rule, whichever names them, a kind added to
// the table included, and nothing is written.
func TestIngestRunFromTableRefusesTwoSources(t *testing.T) {
	clearCI(t)
	saved := sourceKinds
	t.Cleanup(func() { sourceKinds = saved })
	sourceKinds = append(slices.Clone(sourceKinds), sourceKind{
		name: "example", what: "a directory a test names",
		open: func(string, time.Time) (endedRun, error) { return nil, errors.New("never opened") },
	})
	home := codexActionHome(t)
	for _, args := range [][]string{
		{"--execution-file", executionFile, "--codex-home", home},
		{"--from", "execution-file=" + executionFile, "--from", "codex-home=" + home},
		{"--from", "codex-home=" + home, "--execution-file", executionFile},
		{"--execution-file", executionFile, "--execution-file", executionFile},
		{"--from", "example=" + home, "--codex-home", home},
	} {
		out := filepath.Join(t.TempDir(), "rows.jsonl")
		code, _, stderr := execute(t, append(append([]string{"ingest-run"}, args...), "--out", out)...)
		if code != exitFailure || !strings.Contains(stderr, "costroid: ingest-run reads one source, and was given 2: ") {
			t.Errorf("%v: exit %d: %s", args, code, stderr)
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Errorf("%v: a rows file exists: %v", args, err)
		}
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--from", "gh-aw=dir"}, "takes KIND=PATH, with KIND one of execution-file, codex-home or example"},
		{[]string{"--from", executionFile}, "takes KIND=PATH, with KIND one of execution-file, codex-home or example"},
		{[]string{"--from", "codex-home="}, "codex-home takes a path"},
		{[]string{"--from", "example=" + home, "--since", "2026-11-07T13:27:51Z"}, "ingest-run takes --since only with codex-home"},
		{[]string{}, "ingest-run needs --from KIND=PATH and --out, with KIND one of execution-file, codex-home or example"},
	} {
		code, _, stderr := execute(t, append(append([]string{"ingest-run"}, tc.args...), "--out", filepath.Join(t.TempDir(), "rows.jsonl"))...)
		if code != exitFailure || !strings.Contains(stderr, tc.want) {
			t.Errorf("%v: exit %d, want %q: %s", tc.args, code, tc.want, stderr)
		}
	}
}

func TestIngestRunHelpNamesEveryKind(t *testing.T) {
	for _, kind := range sourceKinds {
		if !strings.Contains(ingestRunHelp, "\n  "+kind.name+" ") || !strings.Contains(ingestRunHelp, kind.what) {
			t.Errorf("costroid help ingest-run does not list %s as %q", kind.name, kind.what)
		}
	}
}
