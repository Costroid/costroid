package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/adapter/claudecode"
	"github.com/Costroid/costroid/internal/adapter/codex"
	"github.com/Costroid/costroid/internal/cicontext"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/gateway"
)

// launcherScript stands in for npx, bunx or node. It writes its arguments
// beside log, one per line, and prints nothing.
const launcherScript = `#!/bin/sh
printf '%%s\n' "$@" > %q
`

// writeScript writes an executable script named name and returns its path.
func writeScript(t *testing.T, name, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// With --agent claude-code, a command that starts Claude Code through npx,
// bunx or node gets the stream flags and --max-budget-usd after the package
// or the script, and the launcher keeps its own options before it. A run
// hands that command to the launcher as planned.
func TestRunAgentFlagPlansNpx(t *testing.T) {
	budget := decimal.MustParse("0.5")
	added := []string{"--output-format", "stream-json", "--verbose", "--max-budget-usd", "0.5", "-p", "work"}
	for _, c := range []struct {
		name         string
		argv, prefix []string
	}{
		{"npx", []string{"npx", "-y", "@anthropic-ai/claude-code@2.1.316", "-p", "work"}, []string{"npx", "-y", "@anthropic-ai/claude-code@2.1.316"}},
		{"npx --package", []string{"npx", "--package", "@anthropic-ai/claude-code", "claude", "-p", "work"}, []string{"npx", "--package", "@anthropic-ai/claude-code", "claude"}},
		{"npx --", []string{"npx", "--yes", "--", "@anthropic-ai/claude-code", "-p", "work"}, []string{"npx", "--yes", "@anthropic-ai/claude-code"}},
		{"bunx", []string{"/usr/local/bin/bunx", "@anthropic-ai/claude-code", "-p", "work"}, []string{"/usr/local/bin/bunx", "@anthropic-ai/claude-code"}},
		{"node", []string{"node", "--require", "./trace.js", "node_modules/@anthropic-ai/claude-code/cli.js", "-p", "work"}, []string{"node", "--require", "./trace.js", "node_modules/@anthropic-ai/claude-code/cli.js"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			agent, err := AgentNamed(claudecode.Name)
			if err != nil {
				t.Fatal(err)
			}
			plan, session, err := planCommand(agent, c.argv, budget)
			if err != nil {
				t.Fatal(err)
			}
			if want := slices.Concat(c.prefix, added); !slices.Equal(plan.Argv, want) {
				t.Errorf("planned %q, want %q", plan.Argv, want)
			}
			if plan.Enforcement.Grade != adapter.GradeAgentFlag || !slices.Equal(session.Argv[1:], added) {
				t.Errorf("grade %s, the session's arguments %q", plan.Enforcement.Grade, session.Argv)
			}
		})
	}

	t.Run("run", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
		log := filepath.Join(t.TempDir(), "argv")
		npx := writeScript(t, "npx", fmt.Sprintf(launcherScript, log))
		var rows, stdout, stderr bytes.Buffer
		_, err := Run(context.Background(), Options{
			Agent: claudecode.Name, Argv: []string{npx, "-y", "@anthropic-ai/claude-code@2.1.316", "-p", "work"},
			Budget: budget, Rows: &rows, Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, Catalog: pinned(t),
		})
		var reader *ReaderError
		if !errors.As(err, &reader) {
			t.Fatalf("a launcher that printed nothing: %v", err)
		}
		if got, want := logged(t, log), slices.Concat([]string{"-y", "@anthropic-ai/claude-code@2.1.316"}, added); !slices.Equal(got, want) {
			t.Errorf("npx was given %q, want %q", got, want)
		}
	})

	t.Run("codex", func(t *testing.T) {
		agent, err := AgentNamed(codex.Agent{}.Name())
		if err != nil {
			t.Fatal(err)
		}
		plan, session, err := planCommand(agent, []string{"npx", "-y", "@openai/codex@0.164.0-alpha.11.1", "exec", "work"}, budget)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"npx", "-y", "@openai/codex@0.164.0-alpha.11.1", "exec", "--json", "work"}; !slices.Equal(plan.Argv, want) {
			t.Errorf("planned %q, want %q", plan.Argv, want)
		}
		if want := []string{"", "exec", "--json", "work"}; !slices.Equal(session.Argv, want) {
			t.Errorf("the session reads %q, want %q: npx's version is not Codex's", session.Argv, want)
		}
	})

	for name, argv := range map[string][]string{
		"npx --call":       {"npx", "--call", "claude -p work"},
		"npx -c":           {"npx", "-c", "claude -p work"},
		"node -e":          {"node", "-e", "require('x')", "-p", "work"},
		"no package":       {"npx", "-y"},
		"a value missing":  {"npx", "--package"},
		"nothing after --": {"bunx", "--"},
	} {
		t.Run("refused/"+name, func(t *testing.T) {
			if _, _, err := planCommand(claudecode.Agent{}, argv, budget); err == nil {
				t.Errorf("%q was planned", argv)
			}
		})
	}
	t.Run("without --agent", func(t *testing.T) {
		if _, err := (Options{Argv: []string{"npx", "@anthropic-ai/claude-code", "-p", "work"}}).agent(); err == nil || !strings.Contains(err.Error(), `no adapter for "npx"`) {
			t.Errorf("err = %v", err)
		}
		if _, err := (Options{Agent: "aider", Argv: []string{"aider"}}).agent(); err == nil || !strings.Contains(err.Error(), "--agent takes claude-code, claude-code-sdk, codex, codex-sdk") {
			t.Errorf("err = %v", err)
		}
	})
}

// sdkProgramScript stands in for a program that runs Claude Code through the
// Agent SDK: Claude Code writes the recorded session in its config directory,
// timed now, and the program prints the recorded stream as its own output.
//
// The time is written as Claude Code writes it, in whole milliseconds, cut
// down. It is cut from date's nine digits of nanoseconds, never taken from
// %3N: uutils coreutils 0.10.0's date drops the leading zeros of %3N, so in
// the first 100 ms of every second it printed a time up to 0.9 s ahead, often
// after the program had ended, and the session then read as outside the run.
// A date that prints no nine digits stops the program with a message.
const sdkProgramScript = `#!/bin/sh
printf '%%s\n' "$@" > %[1]q.argv
env > %[1]q.env
stamp=$(date -u +%%Y-%%m-%%dT%%H:%%M:%%S.%%N)
case $stamp in
*.[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]) ;;
*) echo "date printed $stamp, not nine digits of nanoseconds" >&2; exit 3 ;;
esac
now=${stamp%%??????}Z
mkdir -p "$CLAUDE_CONFIG_DIR/projects/-work"
sed "s/\"timestamp\":\"[^\"]*\"/\"timestamp\":\"$now\"/g" %[2]q > "$CLAUDE_CONFIG_DIR/projects/-work/00000000-0000-4000-8000-000000000001.jsonl"
cat %[3]q
`

// sdkRun runs a fake Agent SDK program that leaves the recording in dir, on
// an API key, in CI. It returns the outcome and the program's log.
func sdkRun(t *testing.T, dir string, session *gateway.Session) (outcome, string) {
	t.Helper()
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITLAB_CI", "")
	t.Setenv("ENABLE_PROMPT_CACHING_1H", "")
	t.Setenv("ANTHROPIC_API_KEY", providerKey)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	log := filepath.Join(t.TempDir(), "program")
	recording := mustAbs(t, dir)
	program := writeScript(t, "node", fmt.Sprintf(sdkProgramScript, log, filepath.Join(recording, "session.jsonl"), filepath.Join(recording, "stream.jsonl")))
	var rows, stdout, stderr bytes.Buffer
	result, err := Run(context.Background(), Options{
		Agent: claudecode.SDKName, Argv: []string{program, "dist/agent.js"},
		Budget: decimal.MustParse("0.01"), CI: cicontext.Context{Run: gatewayRun},
		Rows: &rows, Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, Catalog: pinned(t), Gateway: session,
	})
	return outcome{result: result, stdout: stdout.String(), stderr: stderr.String(), err: err, rows: parseRows(t, rows.Bytes())}, log
}

// sdkTotal checks a recorded Agent SDK run's rows against the total_cost_usd
// its program printed, digit for digit, and returns the rows' token counts.
func sdkTotal(t *testing.T, got outcome, want string) map[string]int64 {
	t.Helper()
	if got.err != nil || got.result.Agent != claudecode.SDKName {
		t.Fatalf("run %s: %v, %s", got.result.Agent, got.err, got.stderr)
	}
	if total := got.result.ListTotal.String(); total != want {
		t.Errorf("the rows come to USD %s at list; the program's own total_cost_usd is %s; warnings %q; %s", total, want, got.result.Report.Warnings, got.stderr)
	}
	for _, row := range got.rows {
		if tagOf(row, focus.TagSessionID) != oneRequestID || tagOf(row, focus.TagCostSource) != claudecode.SourceSessionFile ||
			tagOf(row, focus.TagCostBasis) != string(focus.Measured) || tagOf(row, focus.TagAgent) != claudecode.SDKName || tagOf(row, focus.TagAgentVersion) != "2.1.316" {
			t.Errorf("%v", row["Tags"])
		}
	}
	return consumed(t, got.rows)
}

// The TypeScript program's own result stated total_cost_usd 0.000468745 for
// run 42445899154. Its stdout is the program's, passed through and never
// read; the rows come from the session Claude Code wrote, and add up to the
// program's own figure to the last digit.
func TestSDKRowsEqualTheProgramsModelUsage(t *testing.T) {
	got, log := sdkRun(t, filepath.Join(claudeData, "sdk-claude-ts"), nil)
	counts := sdkTotal(t, got, "0.000468745")
	want := map[string]int64{"input_uncached": 1204, "cache_read": 1522, "cache_write_5m": 2205, "output": 115, "cached_input_reported": 1522}
	for class, tokens := range want {
		if counts[class] != tokens {
			t.Errorf("%s: %d tokens, the program's modelUsage states %d", class, counts[class], tokens)
		}
	}
	if !strings.Contains(got.stdout, `"total_cost_usd":0.000468745`) {
		t.Error("the program's own output did not reach the run's stdout")
	}
	if got, want := logged(t, log+".argv"), []string{"dist/agent.js"}; !slices.Equal(got, want) {
		t.Errorf("the program was given %q, want %q", got, want)
	}
}

// The Python program prints its messages in a shape of its own, and its run
// counted thinking tokens. Its stdout is not read, so the shape changes
// nothing: the rows come to its total_cost_usd, 0.000303215, run 42445901806.
func TestSDKPythonRecording(t *testing.T) {
	got, _ := sdkRun(t, filepath.Join(claudeData, "sdk-claude-py"), nil)
	counts := sdkTotal(t, got, "0.000303215")
	want := map[string]int64{"input_uncached": 1204, "cache_read": 3044, "cache_write_5m": 683, "output": 134, "reasoning_output": 22, "cached_input_reported": 3044}
	for class, tokens := range want {
		if counts[class] != tokens {
			t.Errorf("%s: %d tokens, the program's modelUsage states %d", class, counts[class], tokens)
		}
	}
}

// With no gateway the budget sits in the program, so every row reads none and
// says so. With a gateway key the run holds the program to the key: its
// Claude Code gets the gateway and the key in place of the provider key, and
// the rows read gateway-precheck.
func TestSDKGradeIsNoneWithoutAGatewayKey(t *testing.T) {
	const note = "none: the budget sits in the program's maxBudgetUsd, which Costroid cannot see"
	got, _ := sdkRun(t, filepath.Join(claudeData, "sdk-claude-ts"), nil)
	if got.err != nil || len(got.rows) == 0 || got.result.Enforcement.Grade != adapter.GradeNone || got.result.Enforcement.Note != note {
		t.Fatalf("%v, %d rows, %+v, warnings %q, %s", got.err, len(got.rows), got.result.Enforcement, got.result.Report.Warnings, got.stderr)
	}
	for _, row := range got.rows {
		if tagOf(row, focus.TagEnforcementGrade) != adapter.GradeNone || !strings.Contains(row["ChargeDescription"].(string), note) {
			t.Errorf("%s, %v", tagOf(row, focus.TagEnforcementGrade), row["ChargeDescription"])
		}
	}

	held, log := sdkRun(t, filepath.Join(claudeData, "sdk-claude-ts"), fakeGateway(t, keyInfoHolding, `{"status":"healthy","db":"connected"}`))
	if held.err != nil || len(held.rows) == 0 || held.result.Enforcement.Grade != adapter.GradeGatewayPrecheck {
		t.Fatalf("%v, %d rows, %+v, warnings %q, %s", held.err, len(held.rows), held.result.Enforcement, held.result.Report.Warnings, held.stderr)
	}
	for _, row := range held.rows {
		if tagOf(row, focus.TagEnforcementGrade) != adapter.GradeGatewayPrecheck || row["SubAccountId"] != gateway.KeyID(gatewayKey) {
			t.Errorf("%s, %v", tagOf(row, focus.TagEnforcementGrade), row["SubAccountId"])
		}
	}
	env := strings.Split(string(mustRead(t, log+".env")), "\n")
	if slices.Contains(env, "ANTHROPIC_API_KEY="+providerKey) || !slices.Contains(env, "ANTHROPIC_AUTH_TOKEN="+gatewayKey) || !slices.ContainsFunc(env, func(line string) bool { return strings.HasPrefix(line, "ANTHROPIC_BASE_URL=http://") }) {
		t.Errorf("the program's Claude credentials: %q", slices.DeleteFunc(env, func(line string) bool { return !strings.HasPrefix(line, "ANTHROPIC_") }))
	}
	if got, want := logged(t, log+".argv"), []string{"dist/agent.js"}; !slices.Equal(got, want) {
		t.Errorf("the program was given %q under a gateway, want %q", got, want)
	}
}

// A gateway run of an SDK program keeps the gateway's holds. A settings file
// the program's Claude Code may read that routes around the gateway refuses
// the run before the gateway is asked for the key. Codex reads its base URL
// from configuration alone, so a Codex SDK program cannot be held by a key.
func TestSDKGatewayRunRefusesWhatRoutesAround(t *testing.T) {
	session := fakeGateway(t, keyInfoHolding, `{"status":"healthy","db":"connected"}`)
	options := Options{Gateway: session, Budget: decimal.MustParse("0.01"), CI: cicontext.Context{Run: gatewayRun}}.withDefaults()
	root := t.TempDir()
	settings := filepath.Join(root, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"env":{"ANTHROPIC_BASE_URL":"https://elsewhere.test"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	held, err := options.openGateway(context.Background(), claudecode.SDKAgent{ConfigDir: t.TempDir(), Dir: root})
	if err == nil || held != nil || !strings.Contains(err.Error(), settings+" with ANTHROPIC_BASE_URL in its env sends claude-code-sdk's requests around the gateway") {
		t.Errorf("held %v, err %v", held, err)
	}
	held, err = options.openGateway(context.Background(), codex.SDKAgent{})
	if err == nil || held != nil || !strings.Contains(err.Error(), "codex-sdk cannot be pointed at a gateway") {
		t.Errorf("held %v, err %v", held, err)
	}
}

// A Codex SDK program is watched through the rollout files its Codex writes,
// and stopped at the cap, as codex exec is.
func TestCodexSDKProgramIsStoppedAtTheCap(t *testing.T) {
	log := filepath.Join(t.TempDir(), "progress")
	t.Setenv(fakeMode, "replay")
	t.Setenv(fakeLog, log)
	t.Setenv(fakeRollout, mustAbs(t, filepath.Join(codexData, "exec", "rollout.jsonl")))
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("GITHUB_ACTIONS", "true")
	// The fake Codex stands in for the program and its Codex at once: it
	// writes the rollout file, and its stdout is not read.
	got := runAgentWith(t, Options{Agent: codex.SDKName, Argv: []string{agentNamed(t, "codex"), "work"}, Budget: decimal.MustParse("0.02")})
	if got.err != nil {
		t.Fatal(got.err)
	}
	if !got.result.Stopped || got.result.Enforcement.Grade != adapter.GradeWrapperWatchdog {
		t.Fatalf("stopped %v, grade %s, %s", got.result.Stopped, got.result.Enforcement.Grade, got.stderr)
	}
	if progress := logged(t, log); len(progress) != 1 {
		t.Errorf("progress %v, want the program stopped after its first of two requests", progress)
	}
	if err := checkListTotal(got.result.ListTotal, "0.0291216"); err != nil {
		t.Error(err)
	}
	if !strings.Contains(got.stdout, "thread.started") {
		t.Error("the program's own output did not reach the run's stdout")
	}
}

// runAgentWith runs options with the test's catalog and buffers.
func runAgentWith(t *testing.T, options Options) outcome {
	t.Helper()
	var rows, stdout, stderr bytes.Buffer
	options.Rows, options.Stdin, options.Stdout, options.Stderr = &rows, strings.NewReader(""), &stdout, &stderr
	options.Catalog, options.Grace = pinned(t), 200*time.Millisecond
	result, err := Run(context.Background(), options)
	return outcome{result: result, stdout: stdout.String(), stderr: stderr.String(), err: err, rows: parseRows(t, rows.Bytes())}
}
