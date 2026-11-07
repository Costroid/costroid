package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/scm/scmtest"
)

const (
	defaultBranchCommit = "3333333333333333333333333333333333333333"
	finishedRunID       = 19203384756
	agentWorkflow       = ".github/workflows/costroid.yml"
)

// followUpEnvironment is a workflow_run job in example/widgets, after the
// agent's workflow finished on a pull request from headRepository.
func followUpEnvironment(t *testing.T, headRepository string) *scmtest.Stub {
	t.Helper()
	event, err := json.Marshal(map[string]any{"workflow_run": map[string]any{
		"id":              finishedRunID,
		"event":           "pull_request",
		"head_sha":        headCommit,
		"head_branch":     "main",
		"path":            agentWorkflow,
		"pull_requests":   []any{},
		"head_repository": map[string]string{"full_name": headRepository},
		"repository":      map[string]string{"full_name": "example/widgets"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	stub := reportEnvironment(t, string(event))
	t.Setenv("GITHUB_EVENT_NAME", "workflow_run")
	t.Setenv("GITHUB_SHA", defaultBranchCommit)
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GITHUB_RUN_ID", "1")
	return stub
}

func absentRows(t *testing.T) string {
	return filepath.Join(t.TempDir(), "costroid-follow-up", "costroid-rows.jsonl")
}

func TestForkFollowUpPostsOneNeutralCheckWithTheReason(t *testing.T) {
	stub := followUpEnvironment(t, "stranger/widgets")
	stub.Files[configFile] = overBudget + "  enforce: true\n"
	code, _, stderr := execute(t, "follow-up", "--rows", absentRows(t))
	if code != 0 || len(stub.Checks) != 1 {
		t.Fatalf("exit %d, %d checks: %s", code, len(stub.Checks), stderr)
	}
	check := stub.Checks[0]
	if check.Name != "costroid" || check.Conclusion != "neutral" || check.HeadSHA != headCommit {
		t.Errorf("check = %+v", check)
	}
	if !strings.Contains(check.Summary, "Reason: `fork_no_rows`.") || !strings.Contains(check.Text, `"reason": "fork_no_rows"`) {
		t.Errorf("no reason:\n%s\n%s", check.Summary, check.Text)
	}
	if !strings.Contains(check.Summary, "[CI run](https://github.com/example/widgets/actions/runs/19203384756)") {
		t.Errorf("the summary does not link the finished run:\n%s", check.Summary)
	}
	if len(stub.Comments) != 0 {
		t.Errorf("a follow-up posted %d comments", len(stub.Comments))
	}
	if verdict := checkVerdict(t, check.Text); verdict.ConfigRef != defaultBranchCommit || !verdict.ConfigFromBase {
		t.Errorf("the settings came from %s, not the default branch's commit", verdict.ConfigRef)
	}
	for _, request := range stub.Requests {
		if strings.Contains(request, headCommit) {
			t.Errorf("the follow-up read the head: %s", request)
		}
	}
}

func TestForkFollowUpReadsAForksRowsAsData(t *testing.T) {
	stub := followUpEnvironment(t, "stranger/widgets")
	stub.Commits[agentWorkflow] = []time.Time{time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	stub.Files[configFile] = "policy:\n  run_budget_usd: \"5\"\n"
	data, err := os.ReadFile(realRows)
	if err != nil {
		t.Fatal(err)
	}
	hostile := strings.ReplaceAll(string(data), `"costroid/model":"claude-haiku-5-5"`, `"costroid/model":"[x](https://example.com) @admin"`)
	if hostile == string(data) {
		t.Fatal("the fixture names no model to replace")
	}
	rows := filepath.Join(t.TempDir(), "costroid-rows.jsonl")
	if err := os.WriteFile(rows, []byte(hostile), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := execute(t, "follow-up", "--rows", rows)
	if code != 0 || len(stub.Checks) != 1 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	check := stub.Checks[0]
	if check.Conclusion != "neutral" || strings.Contains(check.Summary, "fork_no_rows") {
		t.Errorf("rows from a fork concluded %s:\n%s", check.Conclusion, check.Summary)
	}
	if strings.Contains(check.Summary+check.Text, "example.com") || !strings.Contains(check.Text, `"not-shown"`) {
		t.Errorf("a string from the fork's rows passed the allowlist:\n%s\n%s", check.Summary, check.Text)
	}
	if !strings.Contains(check.Text, `"rows": 19`) {
		t.Errorf("the rows were not read:\n%s", check.Text)
	}
}

func TestForkFollowUpRefusesRowsPastTheCap(t *testing.T) {
	stub := followUpEnvironment(t, "stranger/widgets")
	rows := filepath.Join(t.TempDir(), "costroid-rows.jsonl")
	if err := os.WriteFile(rows, make([]byte, maxFollowUpRows+1), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := execute(t, "follow-up", "--rows", rows)
	if code != 0 || len(stub.Checks) != 1 || stub.Checks[0].Conclusion != "neutral" {
		t.Fatalf("exit %d, %+v: %s", code, stub.Checks, stderr)
	}
	if !strings.Contains(stub.Checks[0].Text, `"reason": "rows_file_unread"`) || !strings.Contains(stderr, "over the follow-up's cap") {
		t.Errorf("stderr: %s\n%s", stderr, stub.Checks[0].Text)
	}
}

func TestForkFollowUpPostsNothingOutsideAForksPullRequest(t *testing.T) {
	cases := map[string]struct {
		headRepository, eventName string
	}{
		"a branch of the repository":     {"example/widgets", "workflow_run"},
		"a branch named in another case": {"Example/Widgets", "workflow_run"},
		"another event":                  {"stranger/widgets", "pull_request"},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			stub := followUpEnvironment(t, run.headRepository)
			t.Setenv("GITHUB_EVENT_NAME", run.eventName)
			code, _, stderr := execute(t, "follow-up", "--rows", absentRows(t))
			if code != 0 || len(stub.Requests) != 0 || !strings.Contains(stderr, "costroid: not posted: ") {
				t.Errorf("exit %d, requests %v: %s", code, stub.Requests, stderr)
			}
		})
	}
}

func TestForkFollowUpRefusesOutsideGitHubActions(t *testing.T) {
	followUpEnvironment(t, "stranger/widgets")
	t.Setenv("GITHUB_ACTIONS", "")
	if code, _, stderr := execute(t, "follow-up", "--rows", absentRows(t)); code != exitFailure || !strings.Contains(stderr, "GitHub Actions was not detected") {
		t.Errorf("exit %d: %s", code, stderr)
	}
	if code, _, _ := execute(t, "follow-up"); code != exitFailure {
		t.Errorf("no --rows: exit %d", code)
	}
}

// TestForkWithNoRowsYieldsForkNoRowsInTheMachineBlock reads the reason from the
// check run's machine block, where an agent reads it, and holds the block to
// result/v0.4.
func TestForkWithNoRowsYieldsForkNoRowsInTheMachineBlock(t *testing.T) {
	stub := followUpEnvironment(t, "stranger/widgets")
	outputs, _ := stepFiles(t)
	if code, _, stderr := execute(t, "follow-up", "--rows", absentRows(t)); code != 0 || len(stub.Checks) != 1 {
		t.Fatalf("exit %d, %d checks: %s", code, len(stub.Checks), stderr)
	}
	block := checkBlock(t, stub.Checks[0].Text)
	validateResult(t, block)
	var object struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(block), &object); err != nil {
		t.Fatal(err)
	}
	if object.State != "rows_unreadable" || object.Reason != "fork_no_rows" {
		t.Errorf("state %q, reason %q:\n%s", object.State, object.Reason, block)
	}
	if result := readOutputs(t, outputs)["result"]; result != compactJSON(t, block) {
		t.Errorf("the result output differs from the check's block:\n%s", result)
	}
}
