package copilot_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/run"
)

// recordedStop is dogfood run 42945131550: costroid run --budget-usd 0.001
// around Copilot CLI 1.0.103 on the user's own Anthropic key,
// claude-haiku-5-5, stopped at the cap after its first request. Its README
// states the run's figures.
var recordedStop = filepath.Join("testdata", "1.0.103", "stop")

// stoppedCopilot replays the recorded stop: fakeCopilot on its events and
// stream, then FAKE_HOLD seconds of waiting, as the recorded run waited on
// its tool, then a second request, the recorded one under a new id. A run
// that is not stopped at the cap reads that second request.
const stoppedCopilot = fakeCopilot + `sleep "$FAKE_HOLD"
grep '"session.usage_record"' "$FAKE_EVENTS" | sed -e "s/` + recordedSession + `/$session/g" -e 's/_fixture_0001/_fixture_0002/g' >>"$dir/events.jsonl"
`

// TestCopilotRecordedStopAtTheCap replays the recorded stop under costroid
// run with the recorded cap, USD 0.001. The run is stopped at the cap before
// a second request: one request, USD 0.002566775 Measured, over the cap by
// USD 0.001566775, the request that was in flight when the cap was crossed.
// It keeps the grade wrapper-watchdog, and warns that the session file holds
// no session.shutdown, so a request in flight at the end reached no row.
func TestCopilotRecordedStopAtTheCap(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "copilot")
	if err := os.WriteFile(command, []byte(stoppedCopilot), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"COPILOT_HOME": filepath.Join(dir, "home"),
		"FAKE_PAUSE":   "0.2",
		"FAKE_HOLD":    "3",
		"FAKE_EVENTS":  absolute(t, filepath.Join(recordedStop, "events.jsonl")),
		"FAKE_STREAM":  absolute(t, filepath.Join(recordedStop, "stream.jsonl")),
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
	budget := decimal.MustParse("0.001")
	var written, stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	result, err := run.Run(ctx, run.Options{
		Argv:    []string{command, "-p", "a prompt"},
		Budget:  budget,
		Rows:    &written,
		Stdout:  &stdout,
		Stderr:  &stderr,
		Catalog: pinned,
		Grace:   time.Second,
	})
	if err != nil {
		t.Fatalf("the run failed: %v\n%s", err, stderr.String())
	}
	rows := rowsOf(t, written.Bytes())

	if result.Agent != "copilot" || !result.Stopped {
		t.Errorf("the run read %q, stopped %t", result.Agent, result.Stopped)
	}
	if len(result.Report.Requests) != 1 {
		t.Errorf("the stopped run holds %d requests, not the one recorded", len(result.Report.Requests))
	}
	want := decimal.MustParse("0.002566775")
	if result.ListTotal.Cmp(want) != 0 || listTotal(t, rows).Cmp(want) != 0 {
		t.Errorf("the run comes to USD %s, its rows to USD %s, not USD %s", result.ListTotal, listTotal(t, rows), want)
	}
	if over := result.ListTotal.Sub(budget); over.Cmp(decimal.MustParse("0.001566775")) != 0 {
		t.Errorf("the run passed the cap by USD %s", over)
	}
	if result.Enforcement.Grade != adapter.GradeWrapperWatchdog {
		t.Errorf("the run reads the grade %s", result.Enforcement.Grade)
	}
	for _, r := range rows {
		if r.Tags[focus.TagEnforcementGrade] != adapter.GradeWrapperWatchdog || r.Tags[focus.TagCostBasis] != string(focus.Measured) {
			t.Errorf("a row reads the grade %q and the basis %q", r.Tags[focus.TagEnforcementGrade], r.Tags[focus.TagCostBasis])
		}
	}
	if !slices.ContainsFunc(result.Report.Warnings, func(w string) bool {
		return strings.HasPrefix(w, "the session file holds no session.shutdown") && strings.Contains(w, "a request in flight at the end reached no row")
	}) {
		t.Errorf("the run does not say the session was never closed: %q", result.Report.Warnings)
	}
}
