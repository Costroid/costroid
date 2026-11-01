package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	replayStream = "COSTROID_REPLAY_STREAM"
	fixtures     = "../../internal/adapter/claudecode/testdata/2.1.316"
	sessionID    = "00000000-0000-4000-8000-000000000002"
)

// TestMain replays a recorded stream when the harness runs the test binary
// under the name claude.
func TestMain(m *testing.M) {
	if stream := os.Getenv(replayStream); stream != "" {
		data, err := os.ReadFile(stream)
		if err != nil {
			os.Exit(97)
		}
		os.Stdout.Write(data)
		return
	}
	os.Exit(m.Run())
}

func execute(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := dispatch(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func copyInto(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// replayClaude installs the recorded subagent run and returns a fake claude.
func replayClaude(t *testing.T) string {
	t.Helper()
	config := t.TempDir()
	project := filepath.Join(config, "projects", "-work")
	copyInto(t, filepath.Join(fixtures, "subagent", "session.jsonl"), filepath.Join(project, sessionID+".jsonl"))
	copyInto(t, filepath.Join(fixtures, "subagent", "subagents", "agent-1.jsonl"), filepath.Join(project, sessionID, "subagents", "agent-1.jsonl"))
	stream, err := filepath.Abs(filepath.Join(fixtures, "subagent", "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(replayStream, stream)
	for _, variable := range []string{"GITHUB_ACTIONS", "GITLAB_CI", credentialVariable} {
		t.Setenv(variable, "")
	}
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "claude")
	if err := os.Symlink(self, path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadmeFirstCommandShowsWhatTheProgramPrints(t *testing.T) {
	claude := replayClaude(t)
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	t.Chdir(directory)
	code, stdout, stderr := execute(t, "run", "--budget-usd", "5", "--out", "rows.jsonl", "--", claude, "-p", "Fix the failing test.")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, `"type":"result"`) {
		t.Error("the agent's stream did not pass through")
	}
	if !bytes.Contains(readme, []byte(stderr)) {
		t.Errorf("README.md does not show this output:\n%s", stderr)
	}
	first := firstRow(t, filepath.Join(directory, "rows.jsonl"))
	if !bytes.Contains(readme, []byte(fmt.Sprintf(`"SkuMeter":"%s"`, first["SkuMeter"]))) {
		t.Errorf("README.md does not show the first row's class %v", first["SkuMeter"])
	}
}

func firstRow(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	line, _, _ := bytes.Cut(data, []byte("\n"))
	if err := json.Unmarshal(line, &row); err != nil {
		t.Fatal(err)
	}
	return row
}

func TestSummarySaysWhenAnAgentFlagRunPassedTheBudget(t *testing.T) {
	claude := replayClaude(t)
	out := filepath.Join(t.TempDir(), "rows.jsonl")
	_, _, stderr := execute(t, "run", "--budget-usd", "0.0001", "--out", out, "--", claude, "-p", "work")
	if !strings.Contains(stderr, "over budget by USD 0.001519725") {
		t.Errorf("stderr: %s", stderr)
	}
}

func TestBillingAccountComesFromTheFlagThenTheVariableAndWarnsOnlyWhenAbsent(t *testing.T) {
	const warning = "BillingAccountId reads unknown"
	cases := []struct {
		name, variable string
		args           []string
		want           string
		warned         bool
	}{
		{name: "neither", want: "unknown", warned: true},
		{name: "variable", variable: "org-variable", want: "org-variable"},
		{name: "flag", args: []string{"--billing-account", "org-flag"}, want: "org-flag"},
		{name: "flag over variable", variable: "org-variable", args: []string{"--billing-account", "org-flag"}, want: "org-flag"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claude := replayClaude(t)
			t.Setenv(billingAccountVariable, tc.variable)
			out := filepath.Join(t.TempDir(), "rows.jsonl")
			args := append([]string{"run", "--budget-usd", "5", "--out", out}, tc.args...)
			code, _, stderr := execute(t, append(args, "--", claude, "-p", "work")...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			if account := firstRow(t, out)["BillingAccountId"]; account != tc.want {
				t.Errorf("account %v", account)
			}
			if strings.Contains(stderr, warning) != tc.warned {
				t.Errorf("stderr: %s", stderr)
			}
		})
	}
}

func TestBillingAccountPastTheContractsLimitIsRefused(t *testing.T) {
	out := filepath.Join(t.TempDir(), "rows.jsonl")
	code, _, stderr := execute(t, "run", "--budget-usd", "1", "--out", out, "--billing-account", strings.Repeat("a", maxAccount+1), "--", "claude", "-p", "x")
	if code != exitFailure || !strings.Contains(stderr, "--billing-account") {
		t.Errorf("exit %d, %q", code, stderr)
	}
}

func TestVersionNamesTheBinaryAndTheCatalog(t *testing.T) {
	code, stdout, _ := execute(t, "version")
	if code != 0 || !regexp.MustCompile(`^costroid \S+, catalog 0\.1\.0\n$`).MatchString(stdout) {
		t.Errorf("exit %d, %q", code, stdout)
	}
}

func TestRunRefusesAMissingOrZeroBudget(t *testing.T) {
	out := filepath.Join(t.TempDir(), "rows.jsonl")
	for _, args := range [][]string{
		{"run", "--out", out, "--", "claude", "-p", "x"},
		{"run", "--budget-usd", "0", "--out", out, "--", "claude", "-p", "x"},
		{"run", "--budget-usd", "1e3", "--out", out, "--", "claude", "-p", "x"},
		{"run", "--budget-usd", "1", "--", "claude", "-p", "x"},
		{"run", "--budget-usd", "1", "--out", out},
	} {
		if code, _, _ := execute(t, args...); code != exitFailure {
			t.Errorf("%v exited %d", args, code)
		}
	}
}

func TestUnknownCommandExitsAsAFailure(t *testing.T) {
	if code, _, stderr := execute(t, "diff"); code != exitFailure || !strings.Contains(stderr, "diff") {
		t.Errorf("exit %d, %q", code, stderr)
	}
}
