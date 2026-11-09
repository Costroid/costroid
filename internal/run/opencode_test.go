package run

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

const (
	openCodeData  = "../adapter/opencode/testdata/1.19.4/opencode-run"
	openCodeModel = "anthropic/claude-haiku-5-5"
	openCodePause = 10 * time.Second
)

// fakeOpenCode writes a script that stands in for opencode run: it logs its
// arguments, prints the recorded stream line by line, logs each step it
// finishes and, with a pause, sleeps after each. It returns the script and its
// log.
func fakeOpenCode(t *testing.T, pause time.Duration) (string, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "steps")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$FAKE_LOG.args"
while IFS= read -r line; do
  printf '%s\n' "$line"
  case $line in
    *'"step_finish"'*) echo step >> "$FAKE_LOG"; sleep "$FAKE_PAUSE" ;;
  esac
done < "$FAKE_STREAM"
`
	path := filepath.Join(dir, "opencode")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_LOG", log)
	t.Setenv("FAKE_STREAM", mustAbs(t, filepath.Join(openCodeData, "stream.jsonl")))
	t.Setenv("FAKE_PAUSE", strconv.Itoa(int(pause.Seconds())))
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	return path, log
}

func openCodeOptions(argv []string) []string {
	return append(argv, "run", "--model", openCodeModel, "work")
}

// runAgentAs runs argv under the adapter --agent names.
func runAgentAs(t *testing.T, agent string, argv []string, budget string) outcome {
	t.Helper()
	var rows, stdout, stderr bytes.Buffer
	result, err := Run(context.Background(), Options{
		Agent:   agent,
		Argv:    argv,
		Budget:  decimal.MustParse(budget),
		Rows:    &rows,
		Stdin:   strings.NewReader(""),
		Stdout:  &stdout,
		Stderr:  &stderr,
		Catalog: pinned(t),
		Grace:   200 * time.Millisecond,
	})
	return outcome{result: result, stdout: stdout.String(), stderr: stderr.String(), err: err, rows: parseRows(t, rows.Bytes())}
}

func TestOpenCodeWatchdogStopsAtTheCap(t *testing.T) {
	agent, log := fakeOpenCode(t, openCodePause)
	started := time.Now()
	got := runAgentAs(t, "opencode", openCodeOptions([]string{agent}), "0.001")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if elapsed := time.Since(started); elapsed >= openCodePause {
		t.Errorf("the run took %s: the agent ran out its pause", elapsed)
	}
	if !got.result.Stopped || got.result.AgentExit != 128+int(syscall.SIGTERM) {
		t.Fatalf("stopped %v, exit %d", got.result.Stopped, got.result.AgentExit)
	}
	if steps := logged(t, log); len(steps) != 1 {
		t.Errorf("the agent finished %d steps, want the run stopped after the first", len(steps))
	}
	// The recording's first step, at catalog 0.3.0's Haiku 5.5 prices per
	// million: 4 × 1.00 + 10532 × 1.25 + 75 × 5.00.
	if err := checkListTotal(got.result.ListTotal, "0.0013544"); err != nil {
		t.Error(err)
	}
	for _, row := range got.rows {
		if grade := tagOf(row, focus.TagEnforcementGrade); grade != "wrapper-watchdog" {
			t.Errorf("grade %s", grade)
		}
	}
	args, err := os.ReadFile(os.Getenv("FAKE_LOG") + ".args")
	if err != nil {
		t.Fatal(err)
	}
	if want := "run\n--format\njson\n--model\n" + openCodeModel + "\nwork\n"; string(args) != want {
		t.Errorf("the agent ran with %q, want %q", args, want)
	}
}

func TestOpenCodeRecordedRunCostsWhatItStated(t *testing.T) {
	agent, _ := fakeOpenCode(t, 0)
	got := runAgentAs(t, "opencode", openCodeOptions([]string{agent}), "5")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.result.Stopped {
		t.Fatal("the run was stopped under a budget of USD 5")
	}
	stated := got.result.Report.AgentStated
	if stated == nil || stated.Unit != "USD" || stated.Quantity != "0.00153317" || stated.Source != "opencode" {
		t.Fatalf("OpenCode's own figure %+v, want USD 0.00153317, its two step costs added", stated)
	}
	// The catalog prices the same tokens to the same figure, on its own.
	if err := checkListTotal(got.result.ListTotal, stated.Quantity); err != nil {
		t.Error(err)
	}
	if len(got.rows) != 7 {
		t.Errorf("%d rows, want 3 for the first step and 4 for the second", len(got.rows))
	}
	for _, row := range got.rows {
		if tagOf(row, focus.TagAgent) != "opencode" || tagOf(row, focus.TagCostBasis) != "measured" || tagOf(row, focus.TagModel) != "claude-haiku-5-5" {
			t.Errorf("tags %v", row["Tags"])
		}
	}
}
