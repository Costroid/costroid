package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
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
	if code != 0 || !regexp.MustCompile(`^costroid \S+, catalog 0\.2\.0\n$`).MatchString(stdout) {
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

func TestTheEgressCommandIsDispatched(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := dispatch(context.Background(), []string{"egress", "--help"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "costroid egress report [--json]") {
		t.Fatalf("stderr %q", stderr.String())
	}
}

func TestUnknownCommandExitsAsAFailureAndNamesHelp(t *testing.T) {
	for _, args := range [][]string{{"diff"}, {"help", "diff"}} {
		code, _, stderr := execute(t, args...)
		if code != exitFailure || stderr != "costroid: unknown command \"diff\"\nRun costroid help to list the commands.\n" {
			t.Errorf("%v: exit %d, %q", args, code, stderr)
		}
	}
}

var update = flag.Bool("update", false, "rewrite testdata/usage.golden")

const maxUsageLines = 16

func TestBareCommandPrintsShortUsage(t *testing.T) {
	code, stdout, stderr := execute(t)
	if code != exitFailure || stdout != "" {
		t.Fatalf("exit %d, stdout %q", code, stdout)
	}
	if stderr != usage.String() {
		t.Errorf("stderr is not the short usage:\n%s", stderr)
	}
	if lines := strings.Count(stderr, "\n"); lines > maxUsageLines {
		t.Errorf("the short usage has %d lines", lines)
	}
	for _, line := range []string{"  costroid run --budget-usd N --out rows.jsonl -- <agent command>\n", "  costroid report --rows rows.jsonl\n", "  costroid validate [costroid.yml]\n", "  costroid help <command>\n", "  costroid --version\n"} {
		if !strings.Contains(stderr, line) {
			t.Errorf("the short usage lacks %q", line)
		}
	}
	for _, entry := range commands {
		if !regexp.MustCompile(`\b` + regexp.QuoteMeta(entry.name) + `\b`).MatchString(stderr) {
			t.Errorf("the short usage does not name %s", entry.name)
		}
	}
}

func TestHelpPerCommand(t *testing.T) {
	for _, entry := range commands {
		t.Run(entry.name, func(t *testing.T) {
			code, stdout, stderr := execute(t, "help", entry.name)
			if code != 0 {
				t.Fatalf("help %s: exit %d, %q", entry.name, code, stderr)
			}
			if !strings.Contains(stdout+stderr, "costroid "+entry.name) {
				t.Errorf("help %s does not show its usage:\n%s%s", entry.name, stdout, stderr)
			}
			if entry.help == "" {
				return
			}
			if stdout != entry.help || !strings.HasPrefix(stdout, "Usage:\n  costroid "+entry.name) {
				t.Errorf("help %s printed:\n%s", entry.name, stdout)
			}
			for _, option := range []string{"-h", "-help", "--help"} {
				code, flagOut, flagErr := execute(t, entry.name, option)
				if code != 0 || flagOut != entry.help || flagErr != "" {
					t.Errorf("%s %s: exit %d, stdout %q, stderr %q", entry.name, option, code, flagOut, flagErr)
				}
			}
		})
	}
}

func TestRunHelpAfterAFlagPrintsRunsHelp(t *testing.T) {
	code, stdout, stderr := execute(t, "run", "--out", "rows.jsonl", "--help")
	if code != 0 || stdout != "" || stderr != runHelp {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestVersionFlag(t *testing.T) {
	code, stdout, stderr := execute(t, "--version")
	_, want, _ := execute(t, "version")
	if code != 0 || stderr != "" || stdout != want {
		t.Errorf("exit %d, stdout %q, stderr %q, version prints %q", code, stdout, stderr, want)
	}
}

func TestUsageGolden(t *testing.T) {
	golden := filepath.Join("testdata", "usage.golden")
	if *update {
		if err := os.WriteFile(golden, []byte(usage.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"help"}, {"-h"}, {"-help"}, {"--help"}} {
		code, stdout, stderr := execute(t, args...)
		if code != 0 || stderr != "" || stdout != string(want) {
			t.Errorf("%v: exit %d, stderr %q, stdout differs from %s:\n%s", args, code, stderr, golden, stdout)
		}
	}
}
