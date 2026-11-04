package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
