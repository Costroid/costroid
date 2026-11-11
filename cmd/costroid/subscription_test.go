package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const subscriptionLine = "subscription: list-equivalent, no provider report to reconcile"

// ingestRecordedAction runs ingest-run on a recorded claude-code-action run,
// with its session file where Claude Code wrote it, and previews the comment
// on the rows.
func ingestRecordedAction(t *testing.T, dir, sessionID string) string {
	t.Helper()
	clearCI(t)
	config := t.TempDir()
	project := filepath.Join(config, "projects", "-work")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	session, err := os.ReadFile(filepath.Join(dir, "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, sessionID+".jsonl"), session, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	out := filepath.Join(t.TempDir(), "rows.jsonl")
	if code, _, stderr := execute(t, "ingest-run", "--execution-file", filepath.Join(dir, "claude-execution-output.json"), "--out", out); code != 0 {
		t.Fatalf("ingest-run exit %d: %s", code, stderr)
	}
	code, stdout, stderr := execute(t, "report", "--preview", "--rows", out)
	if code != 0 {
		t.Fatalf("report exit %d: %s", code, stderr)
	}
	return stdout
}

// TestASubscriptionLoginSaysSoBesideItsFigure reads the recorded
// claude-code-action run on claude_code_oauth_token and the recorded run on
// an API key, end to end. The subscription's figure, the run's own
// total_cost_usd, carries the line; the API key's does not.
func TestASubscriptionLoginSaysSoBesideItsFigure(t *testing.T) {
	const recordings = "../../internal/adapter/claudecode/testdata/2.1.316/"
	cases := []struct {
		name, dir, figure string
		marked            bool
	}{
		{"claude_code_oauth_token", "action-subscription", "USD 0.0302309", true},
		{"an API key", "action-session", "USD 0.0391529", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			comment := ingestRecordedAction(t, recordings+tc.dir, "00000000-0000-4000-8000-000000000001")
			if !strings.Contains(comment, "This run cost "+tc.figure+" at list (") {
				t.Errorf("the first line does not state %s:\n%s", tc.figure, comment)
			}
			row := "| " + tc.figure + " | "
			if tc.marked {
				row = "| " + tc.figure + "; " + subscriptionLine + " | List-equivalent, not billed | "
			}
			if !strings.Contains(comment, row) {
				t.Errorf("no row %q:\n%s", row, comment)
			}
			if strings.Contains(comment, subscriptionLine) != tc.marked {
				t.Errorf("the comment states the line: %t, want %t", !tc.marked, tc.marked)
			}
		})
	}
}
