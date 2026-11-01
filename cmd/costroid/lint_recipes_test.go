package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const rejectedWorkflow = `on: pull_request_target
permissions: {}
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - run: make
`

func TestLintRecipesPassesTheShippedRecipes(t *testing.T) {
	code, stdout, stderr := execute(t, "lint-recipes", "../../recipes", "../../action/action.yml")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "costroid: 4 files, 0 rejected lines") {
		t.Fatalf("stdout: %s", stdout)
	}
}

func TestLintRecipesExitsOneAndNamesTheRule(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yml")
	if err := os.WriteFile(path, []byte(rejectedWorkflow), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := execute(t, "lint-recipes", path)
	if code != exitRejected {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stderr, "bad.yml:1: pull-request-target:") {
		t.Fatalf("stderr: %s", stderr)
	}
}

func TestLintRecipesRefusesAPathWithNoRecipes(t *testing.T) {
	for name, args := range map[string][]string{
		"no path":         {"lint-recipes"},
		"empty directory": {"lint-recipes", t.TempDir()},
		"missing file":    {"lint-recipes", filepath.Join(t.TempDir(), "absent.yml")},
	} {
		t.Run(name, func(t *testing.T) {
			if code, _, _ := execute(t, args...); code != exitFailure {
				t.Fatalf("exit %d", code)
			}
		})
	}
}
