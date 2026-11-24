package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"github.com/Costroid/costroid/internal/policy"
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
	if code, _, _ := execute(t, "validate", "a", "b"); code != exitUsage {
		t.Fatalf("two paths: exit %d", code)
	}
}

// invalidPack calls http.send, a built-in a pack may not call, on its line 3.
const invalidPack = "package costroid.pack\n\nresults contains x if { x := http.send({}) }\n"

// TestValidateCompilesThePackItsFileNames runs validate, with and without
// --json, on a costroid.yml that names a policy pack. A pack beside it that
// compiles prints a second valid line. One that calls a built-in outside the
// pack's list, or holds more than 64 KiB, is not valid, as the check finds:
// validate prints the fault, names the next step and exits 1. Where no such
// file is beside it, or the file comes on standard input, a warning says
// validate did not read the pack and what does, and the file is valid.
func TestValidateCompilesThePackItsFileNames(t *testing.T) {
	const named = "policy_pack: .costroid/policy.rego\n"
	oversized := validPack + "# " + strings.Repeat("x", 64<<10) + "\n"
	unread := "costroid: warning: validate did not read the policy pack .costroid/policy.rego: %s. costroid policy --rows FILE --pack FILE reads it\n"
	for name, tc := range map[string]struct {
		pack                  *string
		code                  int
		stdout, stderr, state string
	}{
		"a pack that compiles": {pack: new(validPack), stdout: "costroid.yml: valid\n.costroid/policy.rego: valid\n", stderr: nextAfterValidate + "\n", state: packCompiles},
		"a pack that calls http.send": {pack: new(invalidPack), code: exitRejected, state: packNotValid,
			stdout: "costroid.yml: the policy pack it names is not valid, and its own keys are\n.costroid/policy.rego: pack_invalid at line 3: bundle activation failed: 1 error occurred: pack.rego:3: rego_type_error: undefined function http.send\n",
			stderr: policy.NextAfterInvalidPack + "\n"},
		"a pack over 64 KiB": {pack: &oversized, code: exitRejected, state: packNotValid,
			stdout: "costroid.yml: the policy pack it names is not valid, and its own keys are\n.costroid/policy.rego: pack_unread: policy: .costroid/policy.rego holds " + fmt.Sprint(len(oversized)) + " bytes, over 65536\n", stderr: policy.NextAfterInvalidPack + "\n"},
		"no pack beside it": {stdout: "costroid.yml: valid\n", stderr: fmt.Sprintf(unread, "there is no .costroid/policy.rego") + nextAfterValidate + "\n", state: packNotRead},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			writeFile(t, filepath.Join(dir, configFile), named)
			if tc.pack != nil {
				writeFile(t, filepath.Join(dir, ".costroid", "policy.rego"), *tc.pack)
			}
			code, stdout, stderr := execute(t, "validate")
			if code != tc.code || stdout != tc.stdout || stderr != tc.stderr {
				t.Errorf("exit %d, stdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}
			jsonCode, printed, jsonStderr := execute(t, "validate", "--json")
			object := oneObject(t, printed, validateSchema)
			pack, _ := object["pack"].(map[string]any)
			if jsonCode != code || jsonStderr != stderr || pack["state"] != tc.state || pack["path"] != ".costroid/policy.rego" {
				t.Errorf("--json exits %d, says %q and prints %s", jsonCode, jsonStderr, printed)
			}
			if message, _ := pack["message"].(string); tc.state == packNotValid && !strings.HasSuffix(tc.stdout, ": "+message+"\n") {
				t.Errorf("--json states the fault %q, and the line reads %q", message, tc.stdout)
			}
		})
	}
	code, stdout, stderr := executeWith(t, named, "validate", "-")
	if code != 0 || stdout != "standard input: valid\n" || stderr != fmt.Sprintf(unread, "standard input has no directory to read it from")+nextAfterValidate+"\n" {
		t.Errorf("standard input: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	for _, line := range []string{policy.NextAfterInvalidPack, "costroid: warning: validate did not read the policy pack <path>: <reason>. costroid policy --rows FILE --pack FILE reads it"} {
		troubleshootingLists(t, line)
	}
}

// validPack warns on a run that has rows.
const validPack = "package costroid.pack\n\nresults contains {\"rule\": \"team_rule\", \"outcome\": \"warn\", \"reason\": \"has_rows\"} if input.rows > 0\n"

// twoFaults is a pack the compiler finds two faults in, on its lines 3 and 5:
// each calls a built-in a pack may not call. twoFaultLines are the lines
// every reader of the pack is shown for it, after the name each puts first.
const (
	twoFaults     = "package costroid.pack\n\nresults contains x if { x := http.send({}) }\n\nresults contains y if { y := opa.runtime() }\n"
	twoFaultLines = "pack_invalid at line 3: bundle activation failed: 2 errors occurred:\n" +
		"pack.rego:3: rego_type_error: undefined function http.send\n" +
		"pack.rego:5: rego_type_error: undefined function opa.runtime\n"
)

// TestAPacksFaultsPrintOneToALineWhereverTheyArePrinted gives validate,
// policy and the report a pack with two faults. Each prints the same three
// lines for it: the reason and how many faults, then one fault a line, with
// no character standing in for a line feed. validate opens on the line that
// says the pack is not valid, never on valid, and exits 1. The object under
// --json states the same lines. A pack with more faults than a reader is
// shown ends on the line that says how many lines are left out.
func TestAPacksFaultsPrintOneToALineWhereverTheyArePrinted(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, filepath.Join(dir, configFile), "policy:\n  run_budget_usd: \"5\"\npolicy_pack: pack.rego\n")
	writeFile(t, filepath.Join(dir, "pack.rego"), twoFaults)
	code, stdout, stderr := execute(t, "validate")
	want := "costroid.yml: the policy pack it names is not valid, and its own keys are\npack.rego: " + twoFaultLines
	if code != exitRejected || stdout != want || stderr != policy.NextAfterInvalidPack+"\n" {
		t.Errorf("validate: exit %d, stdout\n%swant\n%sstderr %q", code, stdout, want, stderr)
	}
	_, printed, _ := execute(t, "validate", "--json")
	pack, _ := oneObject(t, printed, validateSchema)["pack"].(map[string]any)
	if message, _ := pack["message"].(string); message+"\n" != twoFaultLines {
		t.Errorf("validate --json states the fault as %q", message)
	}

	warning := "costroid: warning: policy: customer pack: " + twoFaultLines + policy.NextAfterInvalidPack + "\n"
	code, _, stderr = execute(t, "policy", "--rows", recordedRows)
	if code != 0 || stderr != warning {
		t.Errorf("policy: exit %d, stderr\n%swant\n%s", code, stderr, warning)
	}

	var many strings.Builder
	many.WriteString("package costroid.pack\n\n")
	for rule := range 12 {
		fmt.Fprintf(&many, "r%d contains x if { x := http.send({}) }\n", rule)
	}
	writeFile(t, filepath.Join(dir, "pack.rego"), many.String())
	_, stdout, _ = execute(t, "validate")
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 9 || !strings.HasSuffix(lines[1], " errors occurred:") || !regexp.MustCompile(`^and [0-9]+ more lines$`).MatchString(lines[8]) {
		t.Errorf("a pack with twelve faults prints %d lines, the last %q:\n%s", len(lines), lines[len(lines)-1], stdout)
	}
	for _, line := range lines {
		if strings.ContainsFunc(line, unicode.IsControl) || strings.Contains(line, "?") {
			t.Errorf("a line holds a control character, or a mark in a line feed's place: %q", line)
		}
	}

	stub := reportEnvironment(t, pullRequestEvent())
	stub.FilesAt[baseCommit] = map[string]string{configFile: "policy_pack: pack.rego\n", "pack.rego": twoFaults}
	stub.FilesAt[headCommit] = stub.FilesAt[baseCommit]
	code, _, stderr = execute(t, "report", "--rows", realRows)
	if code != 0 || len(stub.Checks) != 1 || !strings.Contains(stderr, warning) {
		t.Errorf("report: exit %d, and its stderr lacks the warning\n%sstderr\n%s", code, warning, stderr)
	}
}
