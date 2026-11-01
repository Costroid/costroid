package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "costroid.yml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidateExitsZeroOnAValidFile(t *testing.T) {
	path := writeConfig(t, "comment:\n  show_cost: false\n")
	code, stdout, _ := execute(t, "validate", path)
	if code != 0 || !strings.Contains(stdout, "valid") {
		t.Fatalf("exit %d, %q", code, stdout)
	}
}

func TestValidateNamesEachProblemAndExitsOne(t *testing.T) {
	path := writeConfig(t, "comment:\n  show_cost: maybe\nnot_a_key: 1\n")
	code, stdout, _ := execute(t, "validate", path)
	if code != exitRejected || !strings.Contains(stdout, "/comment/show_cost") || !strings.Contains(stdout, "not_a_key") {
		t.Fatalf("exit %d, %q", code, stdout)
	}
}

func TestValidateWithNoFileHereSaysTheDefaultsApply(t *testing.T) {
	t.Chdir(t.TempDir())
	code, stdout, _ := execute(t, "validate")
	if code != 0 || !strings.Contains(stdout, "defaults apply") {
		t.Fatalf("exit %d, %q", code, stdout)
	}
}

func TestValidateReadsCostroidYmlHereByDefault(t *testing.T) {
	path := writeConfig(t, "catalog: latest\n")
	t.Chdir(filepath.Dir(path))
	if code, stdout, _ := execute(t, "validate"); code != exitRejected {
		t.Fatalf("exit %d, %q", code, stdout)
	}
}

func TestValidateCannotReadANamedFile(t *testing.T) {
	if code, _, _ := execute(t, "validate", filepath.Join(t.TempDir(), "gone.yml")); code != exitFailure {
		t.Fatalf("exit %d", code)
	}
	if code, _, _ := execute(t, "validate", "a", "b"); code != exitFailure {
		t.Fatalf("two paths: exit %d", code)
	}
}
