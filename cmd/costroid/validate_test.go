package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
			stdout: "costroid.yml: valid\n.costroid/policy.rego: pack_invalid at line 3: bundle activation failed: 1 error occurred: pack.rego:3: rego_type_error: undefined function http.send\n",
			stderr: policy.NextAfterInvalidPack + "\n"},
		"a pack over 64 KiB": {pack: &oversized, code: exitRejected, state: packNotValid,
			stdout: "costroid.yml: valid\n.costroid/policy.rego: pack_unread: policy: .costroid/policy.rego holds " + fmt.Sprint(len(oversized)) + " bytes, over 65536\n", stderr: policy.NextAfterInvalidPack + "\n"},
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
