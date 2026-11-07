package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/comment"
)

const releasedBytes = "the released bytes"

// installRefusing runs the action's install in stage report, on a runner
// whose curl serves one release from disk, over an artifact that holds a
// binary and an event file beside the rows. It returns the runner's temporary
// directory and the step outputs the install wrote.
func installRefusing(t *testing.T) (string, map[string]string) {
	t.Helper()
	temp, release, bin := t.TempDir(), t.TempDir(), t.TempDir()
	for name, body := range map[string]string{
		"curl": `while [ $# -gt 0 ]; do
  case $1 in --output) out=$2; shift ;; https://*) url=$1 ;; esac
  shift
done
cp "` + release + `/${url##*/}" "$out"`,
		"uname": "echo x86_64",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/usr/bin/env bash\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(release, "costroid-linux-amd64"), []byte(releasedBytes), 0o644); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(temp, "costroid-rows-in")
	copyInto(t, realRows, filepath.Join(artifact, "costroid-rows.jsonl"))
	for name, body := range map[string]string{
		"costroid-bin/costroid":       "#!/bin/sh\necho pwned\n",
		"_github_workflow/event.json": "{}\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(artifact, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(artifact, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sum := sha256.Sum256([]byte(releasedBytes))
	outputs := filepath.Join(temp, "github-output")
	command := exec.Command("bash", "../../action/install.sh")
	command.Env = []string{
		"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + temp, "RUNNER_TEMP=" + temp,
		"GITHUB_PATH=" + filepath.Join(temp, "github-path"), "GITHUB_OUTPUT=" + outputs,
		"COSTROID_STAGE=report", "COSTROID_VERSION=1.2.3", "COSTROID_SHA256=" + hex.EncodeToString(sum[:]),
	}
	var log bytes.Buffer
	command.Stdout, command.Stderr = &log, &log
	if err := command.Run(); err != nil {
		t.Fatalf("the install failed on a refused artifact: %v: %s", err, log.String())
	}
	installed, err := os.ReadFile(filepath.Join(temp, "costroid-bin", "costroid"))
	if err != nil || string(installed) != releasedBytes {
		t.Fatalf("installed %q, %v, not the release's binary", installed, err)
	}
	if left, err := os.ReadDir(artifact); err != nil || len(left) != 0 {
		t.Fatalf("the refused artifact is still there: %v, %v", left, err)
	}
	data, err := os.ReadFile(outputs)
	if err != nil {
		t.Fatal(err)
	}
	written := map[string]string{}
	for line := range strings.Lines(string(data)) {
		name, value, _ := strings.Cut(strings.TrimSuffix(line, "\n"), "=")
		written[name] = value
	}
	return temp, written
}

// TestRefusedArtifactFailsTheCheck follows a costroid-rows artifact that holds
// more than the rows from the action's install to the report: the install
// takes the release's binary and tells the report, and the report fails the
// check with the run reason artifact_refused and names the entries, whatever
// rows file it is given.
func TestRefusedArtifactFailsTheCheck(t *testing.T) {
	temp, outputs := installRefusing(t)
	if outputs["refused_count"] != "4" {
		t.Fatalf("the install wrote %v", outputs)
	}
	rows := filepath.Join(temp, "costroid-rows-in", "costroid-rows.jsonl")
	if err := os.WriteFile(rows, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"the file the action touches": rows, "rows that hold figures": realRows} {
		t.Run(name, func(t *testing.T) {
			stub := reportEnvironment(t, pullRequestEvent())
			t.Setenv(refusedVariable, outputs["refused"])
			t.Setenv(refusedCountVariable, outputs["refused_count"])
			code, _, stderr := execute(t, "report", "--rows", path)
			if code != 0 || len(stub.Checks) != 1 || len(stub.Comments) != 1 {
				t.Fatalf("exit %d, %d checks, %d comments: %s", code, len(stub.Checks), len(stub.Comments), stderr)
			}
			check := stub.Checks[0]
			if check.Conclusion != "failure" {
				t.Errorf("the check concludes %s", check.Conclusion)
			}
			for _, want := range []string{`"run_reason": "artifact_refused"`, `"state": "rows_unreadable"`, `"reason": "no_rows_file"`, `"rows": 0`, `"conclusion": "failure"`} {
				if !strings.Contains(check.Text, want) {
					t.Errorf("no %s in the check's result:\n%s", want, check.Text)
				}
			}
			body := stub.Comments[0].Body
			for _, want := range []string{
				"Reason `artifact_refused`: the `costroid-rows` artifact held 4 entries besides the rows and the run object, so the check fails and this report read none of it: ",
				"`costroid-bin`", "`costroid-bin/costroid`", "`_github_workflow`", "`_github_workflow/event.json`",
			} {
				if !strings.Contains(body, want) {
					t.Errorf("no %q in\n%s", want, body)
				}
			}
			if strings.Contains(body, realTotal) {
				t.Errorf("the refused rows were read:\n%s", body)
			}
		})
	}
}

func TestARefusalNamesWhatItCanAndCountsAtLeastOne(t *testing.T) {
	for _, tc := range []struct {
		names, count string
		want         comment.RefusedArtifact
		warns        bool
	}{
		{"a, b", "12", comment.RefusedArtifact{Count: 12, Entries: []string{"a", "b"}}, false},
		{"a, b", "", comment.RefusedArtifact{Count: 2, Entries: []string{"a", "b"}}, true},
		{"a, b", "1", comment.RefusedArtifact{Count: 2, Entries: []string{"a", "b"}}, true},
		{"", "3", comment.RefusedArtifact{Count: 3}, false},
		{"", "x", comment.RefusedArtifact{Count: 1}, true},
	} {
		var stderr bytes.Buffer
		got := refusedArtifact(func(name string) string {
			return map[string]string{refusedVariable: tc.names, refusedCountVariable: tc.count}[name]
		}, &stderr)
		if got == nil || got.Count != tc.want.Count || strings.Join(got.Entries, "|") != strings.Join(tc.want.Entries, "|") || (stderr.Len() > 0) != tc.warns {
			t.Errorf("%q, %q: %+v, warned %q", tc.names, tc.count, got, stderr.String())
		}
	}
	if refusedArtifact(func(string) string { return "" }, &bytes.Buffer{}) != nil {
		t.Error("a report with no refusal reads one")
	}
	body, err := comment.Comment(comment.Input{State: comment.Unreadable, Reason: comment.NoRowsFile, Refused: &comment.RefusedArtifact{Count: 13, Entries: []string{"a`b", "c\nd", strings.Repeat("e", 200)}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "13 entries besides the rows and the run object, so the check fails and this report read none of it: `a?b`, `c?d`, `" + strings.Repeat("e", 128) + "` and 10 more."; !strings.Contains(body, want) {
		t.Errorf("no %q in\n%s", want, body)
	}
}
