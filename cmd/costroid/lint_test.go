package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const customerTestdata = "../../internal/recipelint/testdata/customer"

func runLint(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := lintCommand(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestLintPassesACleanWorkflow(t *testing.T) {
	code, stdout, stderr := runLint(t, filepath.Join(customerTestdata, "clean.yml"))
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "costroid: 1 file, 1 with an agent: 0 high, 0 medium, 0 notes") {
		t.Fatalf("stdout: %s", stdout)
	}
}

func TestLintExitsOneOnAHighFinding(t *testing.T) {
	code, stdout, stderr := runLint(t, filepath.Join(customerTestdata, "mention.yml"))
	if code != exitRejected {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stderr, "mention.yml:17: high: agent-id-token: ") {
		t.Fatalf("stderr: %s", stderr)
	}
	if !strings.Contains(stdout, ": note: subscription-token: ") || !strings.Contains(stdout, "2 high, 2 medium, 2 notes\n") {
		t.Fatalf("stdout: %s", stdout)
	}
}

func TestLintFailsOnAMediumFindingOnlyWhenStrict(t *testing.T) {
	medium := filepath.Join(customerTestdata, "medium.yml")
	if code, _, stderr := runLint(t, medium); code != 0 {
		t.Fatalf("exit %d without --strict: %s", code, stderr)
	}
	code, _, stderr := runLint(t, "--strict", medium)
	if code != exitRejected {
		t.Fatalf("exit %d with --strict", code)
	}
	if !strings.Contains(stderr, ": medium: unpinned-action: openai/codex-action@v1 ") {
		t.Fatalf("stderr: %s", stderr)
	}
}

func TestLintReadsGitHubsWorkflowsByDefault(t *testing.T) {
	root := t.TempDir()
	workflows := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(workflows, 0o755); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join(customerTestdata, "high-id-token.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workflows, "review.yml"), source, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	code, _, stderr := runLint(t)
	if code != exitRejected {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stderr, filepath.Join(".github", "workflows", "review.yml")+":11: high: agent-id-token: ") {
		t.Fatalf("stderr: %s", stderr)
	}
}

func TestLintRefusesAPathItCannotRead(t *testing.T) {
	t.Chdir(t.TempDir())
	for name, args := range map[string][]string{
		"no workflows directory": nil,
		"empty directory":        {t.TempDir()},
		"missing file":           {filepath.Join(t.TempDir(), "absent.yml")},
		"unknown flag":           {"--loose"},
	} {
		t.Run(name, func(t *testing.T) {
			if code, _, stderr := runLint(t, args...); code != exitFailure {
				t.Fatalf("exit %d: %s", code, stderr)
			}
		})
	}
}
