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
	"slices"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/agenttest"
)

const (
	replayStream = "COSTROID_REPLAY_STREAM"
	fixtures     = "../../internal/adapter/claudecode/testdata/2.1.316"
	sessionID    = "00000000-0000-4000-8000-000000000002"
)

// TestMain replays a recorded stream when the harness runs the test binary
// under the name claude. Otherwise it runs the tests with a stub first on PATH
// for every agent, so no test can start the machine's own.
func TestMain(m *testing.M) {
	if stream := os.Getenv(replayStream); stream != "" {
		data, err := os.ReadFile(stream)
		if err != nil {
			os.Exit(97)
		}
		os.Stdout.Write(data)
		return
	}
	os.Exit(agenttest.Main(m.Run))
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

// TestUnknownBillingAccountIsAWarningInCIAndANoteOutside holds the line for
// rows with no billing account to a warning where a CI system is detected,
// and to one note on a run outside CI.
func TestUnknownBillingAccountIsAWarningInCIAndANoteOutside(t *testing.T) {
	const (
		warning = "costroid: warning: BillingAccountId reads unknown: pass --billing-account or set " + billingAccountVariable + "\n"
		note    = "costroid: note: BillingAccountId reads unknown: pass --billing-account or set " + billingAccountVariable + "\n"
	)
	for name, check := range map[string]struct {
		system string
		want   string
	}{
		"outside CI":     {"", note},
		"GitHub Actions": {"GITHUB_ACTIONS", warning},
		"GitLab CI":      {"GITLAB_CI", warning},
	} {
		t.Run(name, func(t *testing.T) {
			claude := replayClaude(t)
			t.Setenv(billingAccountVariable, "")
			if check.system != "" {
				t.Setenv(check.system, "true")
			}
			out := filepath.Join(t.TempDir(), "rows.jsonl")
			code, _, stderr := execute(t, "run", "--budget-usd", "5", "--out", out, "--", claude, "-p", "work")
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			if strings.Count(stderr, "BillingAccountId reads unknown") != 1 || !strings.Contains(stderr, check.want) {
				t.Errorf("stderr does not hold %q once:\n%s", check.want, stderr)
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
	if code != 0 || !regexp.MustCompile(`^costroid \S+, catalog 0\.4\.1, prices read 2026-11-12\n$`).MatchString(stdout) {
		t.Errorf("exit %d, %q", code, stdout)
	}
}

func TestRunRefusesAMissingOrZeroBudget(t *testing.T) {
	out := filepath.Join(t.TempDir(), "rows.jsonl")
	for _, args := range [][]string{
		{"run", "--out", out, "--", "claude", "-p", "x"},
		{"run", "--budget-usd", "0", "--out", out, "--", "claude", "-p", "x"},
		{"run", "--budget-usd", "1e3", "--out", out, "--", "claude", "-p", "x"},
		{"run", "--budget-usd", "1", "--out", out},
	} {
		if code, _, _ := execute(t, args...); code != exitFailure {
			t.Errorf("%v exited %d", args, code)
		}
	}
}

// TestRefusedRunLeavesNoMisleadingRowsFile runs a command no adapter reads.
// run refuses it before it creates a file: no rows file and no result object
// is left, at the default path or at --out, and run never says where rows
// went. The report then finds no rows file, and never says the agent printed
// no usage.
func TestRefusedRunLeavesNoMisleadingRowsFile(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, args := range [][]string{
		{"run", "--budget-usd", "5", "--", "echo", "hi"},
		{"run", "--budget-usd", "5", "--out", "rows.jsonl", "--json", "--", "echo", "hi"},
	} {
		code, stdout, stderr := execute(t, args...)
		if code != exitFailure || stdout != "" || stderr != "costroid: run: no adapter for \"echo\": costroid runs claude-code, claude-code-sdk, codex, codex-sdk, opencode and copilot; --agent names the one the command starts\n" {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args, code, stdout, stderr)
		}
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		t.Errorf("the refused runs left %s", entry.Name())
	}
	_, _, stderr := execute(t, "report", "--preview", "--rows", defaultRowsFile)
	if !strings.Contains(stderr, "no rows file at "+defaultRowsFile) || strings.Contains(stderr, "no usage") {
		t.Errorf("report --preview after the refused run: %q", stderr)
	}
}

func TestTheEgressCommandIsDispatched(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := dispatch(context.Background(), []string{"egress", "--help"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "costroid egress report [--json]") {
		t.Fatalf("stdout %q", stdout.String())
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

var update = flag.Bool("update", false, "rewrite testdata/usage.golden and testdata/existing-claude.golden")

const maxUsageLines = 18

func TestBareCommandPrintsShortUsage(t *testing.T) {
	code, stdout, stderr := execute(t)
	if code != exitUsage || stderr != "" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if stdout != usage.String() {
		t.Errorf("stdout is not the short usage:\n%s", stdout)
	}
	if lines := strings.Count(stdout, "\n"); lines > maxUsageLines {
		t.Errorf("the short usage has %d lines", lines)
	}
	for _, line := range []string{"  costroid run --budget-usd N -- <agent command>\n", "  costroid report --rows rows.jsonl\n", "  costroid validate [costroid.yml]\n", "  costroid help <command>\n", "  costroid --version\n"} {
		if !strings.Contains(stdout, line) {
			t.Errorf("the short usage lacks %q", line)
		}
	}
	for _, entry := range commands {
		if !regexp.MustCompile(`\b` + regexp.QuoteMeta(entry.name) + `\b`).MatchString(stdout) {
			t.Errorf("the short usage does not name %s", entry.name)
		}
	}
}

// TestShortUsageListsTheFirstCommandsFirst holds the usage block to the
// order a first reader meets the commands: demo and lint, which need no key,
// then init and doctor, which add the check to a repository, then run. None
// of the four is named again under other commands.
func TestShortUsageListsTheFirstCommandsFirst(t *testing.T) {
	text := usage.String()
	block, others, _ := strings.Cut(text, "\nOther commands:")
	_, block, _ = strings.Cut(block, "Usage:\n")
	var named []string
	for line := range strings.SplitSeq(block, "\n") {
		if fields := strings.Fields(line); len(fields) > 1 && fields[0] == "costroid" {
			named = append(named, fields[1])
		}
	}
	if want := []string{"demo", "lint", "init", "doctor", "run"}; len(named) < len(want) || !slices.Equal(named[:len(want)], want) {
		t.Errorf("the usage block names %v, want it to open with %v:\n%s", named, want, text)
	}
	others, _, _ = strings.Cut(others, "Ledger commands:")
	for _, name := range []string{"demo", "lint", "init", "doctor"} {
		if regexp.MustCompile(`[ ,]` + name + `[,.]`).MatchString(others) {
			t.Errorf("%s is named under other commands:\n%s", name, text)
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
	if code != 0 || stdout != runHelp || stderr != "" {
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
