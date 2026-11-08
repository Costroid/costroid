package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/agenttest"
)

// TestNoTestCanRunARealAgent runs claude by name, as a test once did, with a
// claude that stands for the machine's own placed on PATH after the stubs.
// The stub answers, and the other claude never starts.
func TestNoTestCanRunARealAgent(t *testing.T) {
	installed := t.TempDir()
	marker := filepath.Join(t.TempDir(), "claude-ran")
	script := "#!/bin/sh\ntouch '" + marker + "'\n"
	if err := os.WriteFile(filepath.Join(installed, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+installed)
	for _, variable := range []string{"GITHUB_ACTIONS", "GITLAB_CI", credentialVariable} {
		t.Setenv(variable, "")
	}
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Chdir(t.TempDir())
	code, _, stderr := execute(t, "run", "--budget-usd", "1", "--", "claude", "-p", "x")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("the claude after the stubs on PATH ran (exit %d): %s", code, stderr)
	}
	if !strings.Contains(stderr, agenttest.Refusal("claude")) {
		t.Errorf("the stub did not answer (exit %d): %s", code, stderr)
	}
}
