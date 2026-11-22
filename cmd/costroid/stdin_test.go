package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// executeWith runs a command line with text on standard input.
func executeWith(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := dispatch(context.Background(), args, strings.NewReader(stdin), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestValidateReadsStandardInputForADash(t *testing.T) {
	t.Chdir(t.TempDir())
	code, stdout, stderr := executeWith(t, "policy:\n  run_budget_usd: \"5\"\n", "validate", "-")
	if code != 0 || stdout != "standard input: valid\n" || stderr != nextAfterValidate+"\n" {
		t.Errorf("a valid file: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	code, stdout, stderr = executeWith(t, "policy:\n  no_such_key: 1\n", "validate", "-")
	if code != exitRejected || !strings.HasPrefix(stdout, "standard input: ") || stderr != "" {
		t.Errorf("a file that breaks the schema: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestValidateTakesAFileWhoseNameOpensWithADashAfterTwoDashes(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, filepath.Join(dir, "-base.yml"), "policy:\n  run_budget_usd: \"5\"\n")
	if code, stdout, stderr := execute(t, "validate", "--", "-base.yml"); code != 0 || !strings.HasPrefix(stdout, "-base.yml: valid\n") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if code, _, stderr := execute(t, "validate", "-base.yml"); code != exitUsage || !strings.HasPrefix(stderr, "costroid: -base.yml is not a flag of costroid validate\n") {
		t.Errorf("without the two dashes: exit %d, stderr %q", code, stderr)
	}
}

// TestEmitOTLPReadsItsRowsFromStandardInputForADash gives emit-otlp a line
// that is no row on standard input. It must refuse that line, and not look
// for a file named with a dash.
func TestEmitOTLPReadsItsRowsFromStandardInputForADash(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	code, stdout, stderr := executeWith(t, "this line is no row\n", "emit-otlp", "--rows", "-")
	if code != exitFailure || stdout != "" || strings.Contains(stderr, "no such file") || !strings.Contains(stderr, "line 1") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	code, _, stderr = executeWith(t, "", "emit-otlp", "--rows", "-")
	if code != 0 || strings.Contains(stderr, "no such file") {
		t.Errorf("no rows: exit %d, stderr %q", code, stderr)
	}
}
