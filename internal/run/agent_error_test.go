package run

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/adapter/claudecode"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/reason"
)

// feedLog is a session that keeps every line it is fed and refuses each one
// that is not JSON, as the agents' sessions do.
type feedLog struct {
	lines []string
}

func (f *feedLog) Feed(line []byte, _ time.Time) error {
	f.lines = append(f.lines, string(line))
	if !bytes.HasPrefix(line, []byte("{")) {
		return errors.New("claude-code: stream line is not JSON")
	}
	return nil
}

func (f *feedLog) Finish() (adapter.Report, error) {
	return adapter.Report{}, nil
}

// TestBlankStdoutLinesAreQuiet feeds the reader blank lines, JSON lines and
// plain text. Blank lines never reach the session and print nothing; the
// plain lines are counted and named once, after the output ends.
func TestBlankStdoutLinesAreQuiet(t *testing.T) {
	stdout := "\n{\"type\":\"system\"}\n   \n\t\nnpm notice one\n{\"type\":\"result\"}\nnpm notice two\n\n"
	cases := map[string]struct {
		output string
		fed    int
		say    string
	}{
		"blank lines alone":    {"\n \n\t\n\n", 0, ""},
		"one plain line":       {"\nnpm notice one\n{\"type\":\"result\"}\n", 2, "costroid: warning: 1 line of the agent's stdout was not read: claude-code: stream line is not JSON\n"},
		"plain lines and JSON": {stdout, 4, "costroid: warning: 2 lines of the agent's stdout were not read; the first: claude-code: stream line is not JSON\n"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			var copied, stderr bytes.Buffer
			session := &feedLog{}
			options := Options{Stdout: &copied, Stderr: &stderr}.withDefaults()
			if err := options.read(strings.NewReader(test.output), session); err != nil {
				t.Fatal(err)
			}
			if len(session.lines) != test.fed {
				t.Errorf("fed %d lines, want %d: %q", len(session.lines), test.fed, session.lines)
			}
			for _, line := range session.lines {
				if strings.TrimSpace(line) == "" {
					t.Errorf("a blank line reached the session")
				}
			}
			if stderr.String() != test.say {
				t.Errorf("stderr:\n%q\nwant\n%q", stderr.String(), test.say)
			}
			if copied.String() != test.output {
				t.Errorf("the agent's output was not copied through unchanged:\n%q", copied.String())
			}
		})
	}
}

// TestMissingAgentNamesItsInstall runs each agent with nothing on PATH. The
// error names the agent and the one command that installs it, and the run
// starts nothing and writes no rows.
func TestMissingAgentNamesItsInstall(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	want := "claude is not on PATH: install it with npm install --global @anthropic-ai/claude-code@2.1.316"
	got := runAgent(t, []string{"claude", "-p", "work"}, "5", pinned(t))
	var missing *MissingAgentError
	if !errors.As(got.err, &missing) || got.err.Error() != want {
		t.Fatalf("err = %v, want %q", got.err, want)
	}
	if !errors.Is(got.err, exec.ErrNotFound) {
		t.Errorf("the error does not wrap exec.ErrNotFound: %v", got.err)
	}
	if len(got.rows) != 0 || got.result.Reason != "" {
		t.Errorf("%d rows, reason %q", len(got.rows), got.result.Reason)
	}
}

// TestInstallLinesAreTheActionsReleases holds the install lines to the
// releases the action installs, so the two cannot drift apart.
func TestInstallLinesAreTheActionsReleases(t *testing.T) {
	script, err := os.ReadFile("../../action/run-agent.sh")
	if err != nil {
		t.Fatal(err)
	}
	for agent, variable := range map[string]string{"claude": "claude_release", "codex": "codex_release"} {
		match := regexp.MustCompile(`(?m)^` + variable + `=(\S+)$`).FindSubmatch(script)
		if match == nil {
			t.Fatalf("action/run-agent.sh sets no %s", variable)
		}
		if want := "npm install --global " + string(match[1]); installLines[agent] != want {
			t.Errorf("%s: %q, and the action installs %q", agent, installLines[agent], want)
		}
	}
}

// TestAgentErrorReasonAndItsLine records reports that ended in an error. With
// no requests the reason is agent_error and its warning names the class and
// the fix; beside requests there is no reason, and a warning still names it.
func TestAgentErrorReasonAndItsLine(t *testing.T) {
	failed := adapter.Report{CostBasis: focus.ListEquivalent, AgentError: adapter.AgentErrorAuthentication}
	var none bytes.Buffer
	result, err := Record(Options{Rows: &none, Catalog: pinned(t)}, claudecode.Name, failed)
	if err != nil {
		t.Fatal(err)
	}
	want := "reason agent_error: the agent ended with authentication_failed: check the API key the agent runs with, the api-key input in the action"
	if result.Reason != reason.AgentError || result.AgentError != adapter.AgentErrorAuthentication || !slices.Contains(result.Report.Warnings, want) {
		t.Errorf("reason %q, class %q, warnings %q", result.Reason, result.AgentError, result.Report.Warnings)
	}
	spent := recordedReport()
	spent.AgentError = adapter.AgentErrorRateLimit
	var rows bytes.Buffer
	result, err = Record(Options{Rows: &rows, Catalog: pinned(t)}, claudecode.Name, spent)
	if err != nil {
		t.Fatal(err)
	}
	beside := "the agent ended with rate_limit: run the job again later, or raise the API key's rate limit with the provider; the rows hold the requests it made before"
	if result.Rows == 0 || result.Reason != "" || result.AgentError != adapter.AgentErrorRateLimit || !slices.Contains(result.Report.Warnings, beside) {
		t.Errorf("%d rows, reason %q, class %q, warnings %q", result.Rows, result.Reason, result.AgentError, result.Report.Warnings)
	}
	for _, class := range []adapter.AgentError{
		adapter.AgentErrorAuthentication, adapter.AgentErrorBilling, adapter.AgentErrorRateLimit,
		adapter.AgentErrorInvalidRequest, adapter.AgentErrorServer, adapter.AgentErrorUnknown,
	} {
		if reason.AgentErrorFix(class) == "" {
			t.Errorf("%s has no fix", class)
		}
	}
	if reason.AgentErrorFix("agent_crashed") != "" {
		t.Error("a class outside the list has a fix")
	}
}
