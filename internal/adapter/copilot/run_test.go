package copilot_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/adapter/copilot"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/run"
)

// fakeCopilot stands in for Copilot CLI 1.0.103. It writes the recorded
// session's events to the session --session-id names, pausing FAKE_PAUSE
// seconds before each usage record, then prints the recorded stream. Every
// session id the recording holds becomes the named one.
const fakeCopilot = `#!/bin/sh
session=""
while [ $# -gt 0 ]; do
	case "$1" in
	--session-id) session="$2"; shift ;;
	esac
	shift
done
dir="$COPILOT_HOME/session-state/$session"
mkdir -p "$dir"
while IFS= read -r line; do
	case "$line" in
	*'"session.usage_record"'*) sleep "$FAKE_PAUSE" ;;
	esac
	printf '%s\n' "$line" | sed "s/` + recordedSession + `/$session/g" >>"$dir/events.jsonl"
done <"$FAKE_EVENTS"
sed "s/` + recordedSession + `/$session/g" "$FAKE_STREAM"
`

// runFake runs the fake under costroid run with the budget given, each usage
// record written pause after the last.
func runFake(t *testing.T, budget, pause string) (run.Result, []row) {
	t.Helper()
	result, rows, err := runFakeAs(t, "copilot", "", budget, pause)
	if err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	if result.Agent != "copilot" {
		t.Fatalf("costroid run read the command as %s", result.Agent)
	}
	return result, rows
}

// runFakeAs runs the fake under the file name given, with --agent set to
// agent unless it is empty.
func runFakeAs(t *testing.T, name, agent, budget, pause string) (run.Result, []row, error) {
	t.Helper()
	return runFakeOn(t, name, agent, budget, pause, absolute(t, filepath.Join(recorded, "events.jsonl")))
}

// runFakeOn runs the fake as runFakeAs does, writing the session file at
// events in place of the recorded one.
func runFakeOn(t *testing.T, name, agent, budget, pause, events string) (run.Result, []row, error) {
	t.Helper()
	dir := t.TempDir()
	command := filepath.Join(dir, name)
	if err := os.WriteFile(command, []byte(fakeCopilot), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"COPILOT_HOME": filepath.Join(dir, "home"),
		"FAKE_PAUSE":   pause,
		"FAKE_EVENTS":  events,
		"FAKE_STREAM":  absolute(t, filepath.Join(recorded, "stream.jsonl")),
	} {
		t.Setenv(name, value)
	}
	for _, name := range providerVariables {
		t.Setenv(name, "")
	}
	pinned, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	var rows, stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	result, err := run.Run(ctx, run.Options{
		Agent:   agent,
		Argv:    []string{command, "-p", "a prompt"},
		Budget:  decimal.MustParse(budget),
		Rows:    &rows,
		Stdout:  &stdout,
		Stderr:  &stderr,
		Catalog: pinned,
		Grace:   time.Second,
	})
	if err != nil {
		return result, nil, fmt.Errorf("%w\n%s", err, stderr.String())
	}
	return result, rowsOf(t, rows.Bytes()), nil
}

func absolute(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// TestCopilotRunHoldsTheBudget runs a stand-in for Copilot CLI under costroid
// run, as `costroid run -- copilot -p` does. The plan names the session, and
// the watch reads its file during the run. Under a cap of USD 0.001 the run
// is stopped after the first usage record, a second later than the next
// would land: its rows hold that one request, USD 0.0027459 at list, and say
// the session was never closed. Under USD 1 the run ends on its own with all
// three requests, USD 0.00329423, checked against session.shutdown. Both keep
// the grade wrapper-watchdog.
func TestCopilotRunHoldsTheBudget(t *testing.T) {
	stopped, rows := runFake(t, "0.001", "1")
	if !stopped.Stopped || stopped.Enforcement.Grade != adapter.GradeWrapperWatchdog {
		t.Errorf("under the cap the run reads stopped %t at %s", stopped.Stopped, stopped.Enforcement.Grade)
	}
	if len(stopped.Report.Requests) != 1 || listTotal(t, rows).Cmp(decimal.MustParse("0.0027459")) != 0 {
		t.Errorf("the stopped run wrote %d requests at USD %s", len(stopped.Report.Requests), listTotal(t, rows))
	}
	if !slices.ContainsFunc(stopped.Report.Warnings, func(w string) bool { return strings.HasPrefix(w, "the session file holds no session.shutdown") }) {
		t.Errorf("the stopped run does not say the session was never closed: %q", stopped.Report.Warnings)
	}

	held, rows := runFake(t, "1", "0.05")
	if held.Stopped || held.AgentExit != 0 || held.Enforcement.Grade != adapter.GradeWrapperWatchdog {
		t.Errorf("under a budget it never reached the run reads stopped %t, exit %d, at %s", held.Stopped, held.AgentExit, held.Enforcement.Grade)
	}
	if len(held.Report.Requests) != 3 || listTotal(t, rows).Cmp(decimal.MustParse("0.00329423")) != 0 {
		t.Errorf("the whole run wrote %d requests at USD %s", len(held.Report.Requests), listTotal(t, rows))
	}
	for _, r := range rows {
		if r.Tags[focus.TagEnforcementGrade] != adapter.GradeWrapperWatchdog {
			t.Errorf("a row reads the grade %q", r.Tags[focus.TagEnforcementGrade])
		}
	}
}

// TestAgentCopilotReachesTheAdapter runs the stand-in under a name that is
// not copilot, as a wrapper script or a launcher would start Copilot CLI.
// With --agent copilot the run reads it as Copilot CLI: the watch on the
// named session, all three recorded requests at USD 0.00329423, and
// Copilot's own figure, 0 nano-AI units. Without --agent the command's name
// picks no adapter, and the error names copilot among the agents --agent
// takes.
func TestAgentCopilotReachesTheAdapter(t *testing.T) {
	result, rows, err := runFakeAs(t, "copilot-wrapper", copilot.Name, "1", "0.05")
	if err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	if result.Agent != copilot.Name || result.Enforcement.Grade != adapter.GradeWrapperWatchdog {
		t.Errorf("--agent copilot read the command as %q at %s", result.Agent, result.Enforcement.Grade)
	}
	if len(result.Report.Requests) != 3 || listTotal(t, rows).Cmp(decimal.MustParse("0.00329423")) != 0 {
		t.Errorf("--agent copilot wrote %d requests at USD %s", len(result.Report.Requests), listTotal(t, rows))
	}
	if stated := result.Report.AgentStated; stated == nil || *stated != (adapter.Stated{Unit: copilot.UnitNanoAIU, Quantity: "0", Source: copilot.Name}) {
		t.Errorf("--agent copilot states %+v", stated)
	}
	for _, r := range rows {
		if r.Tags[focus.TagAgent] != copilot.Name {
			t.Errorf("a row names the agent %q", r.Tags[focus.TagAgent])
		}
	}

	_, _, err = runFakeAs(t, "copilot-wrapper", "", "1", "0.05")
	if err == nil || !strings.Contains(err.Error(), `no adapter for "copilot-wrapper"`) || !strings.Contains(err.Error(), "opencode and copilot") {
		t.Errorf("without --agent the run reads %v", err)
	}
}
