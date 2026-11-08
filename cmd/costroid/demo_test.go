package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/comment"
)

const (
	docsExample  = "../../docs/comment-example.md"
	demoRowsFile = "demo/rows.jsonl"
	actionDir    = "../../internal/adapter/claudecode/testdata/2.1.316/action-session"
	// actionSession names the session file the recording's result points to.
	actionSession = "00000000-0000-4000-8000-000000000001"
	demoTotal     = "0.0391529"
)

func TestDemoPrintsTheDocsExample(t *testing.T) {
	outsideCI(t)
	var stdout, stderr bytes.Buffer
	if code := demoCommand(context.Background(), nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if stderr.Len() > 0 {
		t.Errorf("stderr: %s", stderr.String())
	}
	if want := readFile(t, docsExample); stdout.String() != want {
		t.Errorf("costroid demo does not print %s:\n%s", docsExample, stdout.String())
	}
	if !strings.Contains(stdout.String(), "This run cost USD "+demoTotal+" at list (Measured). Nothing held it to a budget.") {
		t.Errorf("the demo does not open on the recorded run's figure:\n%s", stdout.String())
	}
}

func TestDemoTakesNoArgument(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := demoCommand(context.Background(), []string{"rows.jsonl"}, &stdout, &stderr); code != exitFailure || stdout.Len() > 0 {
		t.Errorf("exit %d, stdout %q", code, stdout.String())
	}
}

// TestDemoRowsAreWhatIngestRunReadsFromTheRecording writes the demo's rows
// again from the recorded run, so the demo shows no figure the recording does
// not hold.
func TestDemoRowsAreWhatIngestRunReadsFromTheRecording(t *testing.T) {
	outsideCI(t)
	t.Setenv(billingAccountVariable, "")
	t.Setenv(credentialVariable, "")
	config := t.TempDir()
	copyInto(t, filepath.Join(actionDir, "session.jsonl"), filepath.Join(config, "projects", "-work", actionSession+".jsonl"))
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	work := t.TempDir()
	execution := filepath.Join(work, "claude-execution-output.json")
	copyInto(t, filepath.Join(actionDir, "claude-execution-output.json"), execution)
	saved := time.Date(2026, 11, 7, 11, 33, 30, 0, time.UTC)
	if err := os.Chtimes(execution, saved, saved); err != nil {
		t.Fatal(err)
	}
	rows := filepath.Join(work, "rows.jsonl")
	if code, _, stderr := execute(t, "ingest-run", "--execution-file", execution, "--out", rows); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if readFile(t, rows) != demoRows {
		t.Errorf("%s is not what ingest-run reads from %s", demoRowsFile, actionDir)
	}
	ledger, err := comment.ReadRows(strings.NewReader(demoRows))
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Figures) != 1 || ledger.Figures[0].Cost.String() != demoTotal {
		t.Errorf("the demo's rows read %+v, not the run's own total %s", ledger.Figures, demoTotal)
	}
}

func TestPreviewPrintsTheCommentWithoutAHost(t *testing.T) {
	t.Run("outside CI", func(t *testing.T) {
		outsideCI(t)
		t.Setenv(installationVariable, "")
		rows := absolute(t, demoRowsFile)
		t.Chdir(t.TempDir())
		code, stdout, stderr := execute(t, "report", "--preview", "--rows", rows)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		assertPreview(t, stdout)
		for _, want := range []string{
			"No rule applied: `costroid.yml` on the base branch sets no budget and no threshold.",
			"This is one run, not a total across runs.",
			"(https://docs.costroid.com/account)",
			"Machine block, the check run was not posted",
		} {
			if !strings.Contains(stdout, want) {
				t.Errorf("no %q in\n%s", want, stdout)
			}
		}
	})
	t.Run("inside GitHub Actions", func(t *testing.T) {
		stub := reportEnvironment(t, pullRequestEvent())
		outputs, summary := stepFiles(t)
		rows := absolute(t, demoRowsFile)
		directory := t.TempDir()
		if err := os.WriteFile(filepath.Join(directory, configFile), []byte(demoConfig), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Chdir(directory)
		code, stdout, stderr := execute(t, "report", "--preview", "--rows", rows, "--installation-id", demoInstallation)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		assertPreview(t, stdout)
		for _, want := range []string{"Run budget: unverified rows cannot satisfy a budget.", "installation=github%3A" + demoInstallation} {
			if !strings.Contains(stdout, want) {
				t.Errorf("no %q in\n%s", want, stdout)
			}
		}
		if len(stub.Requests) > 0 {
			t.Errorf("the preview reached the host: %v", stub.Requests)
		}
		if readFile(t, outputs) != "" || readFile(t, summary) != "" {
			t.Error("the preview wrote the step's outputs or its summary")
		}
	})
}

func assertPreview(t *testing.T, stdout string) {
	t.Helper()
	if !strings.HasPrefix(stdout, comment.Marker+"\n### Costroid\n\nThis run cost USD "+demoTotal+" at list (Measured). Nothing held it to a budget.\n\n**Verdict: neutral.**") {
		t.Errorf("the preview does not open as the comment does:\n%s", stdout)
	}
	if !strings.Contains(stdout, "```json\n{\n  \"schema\": \"costroid.result/0.7\"") {
		t.Errorf("no machine block in\n%s", stdout)
	}
}

func TestPreviewRefusesJSON(t *testing.T) {
	code, stdout, stderr := execute(t, "report", "--preview", "--json", "--rows", demoRowsFile)
	if code != exitFailure || stdout != "" || !strings.Contains(stderr, "--preview or --json") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func outsideCI(t *testing.T) {
	t.Helper()
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("GITLAB_CI", "")
}

func absolute(t *testing.T, path string) string {
	t.Helper()
	full, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return full
}
