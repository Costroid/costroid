package opencode_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/adapter/opencode"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/run"
)

// recordedStop is GitHub Actions run 43223648675: costroid run --budget-usd
// 0.001 around OpenCode 1.19.4 on anthropic/claude-haiku-5-5 and an Anthropic
// API key, stopped at the cap after its first step. Its README states the
// run's figures.
var recordedStop = filepath.Join("testdata", "1.19.4", "stop")

// stoppedOpenCode stands in for OpenCode 1.19.4. For run it prints the
// recorded stream, then waits FAKE_HOLD seconds, as OpenCode would before the
// step its tool calls asked for, then prints that step: the recorded one under
// new part and message ids. It logs each step before it prints the step's
// step_finish line.
const stoppedOpenCode = `#!/bin/sh
if [ "$1" = --version ]; then echo 1.19.4; exit 0; fi
while IFS= read -r line; do
  case $line in *'"step_finish"'*) echo step >> "$FAKE_LOG" ;; esac
  printf '%s\n' "$line"
done < "$FAKE_STREAM"
sleep "$FAKE_HOLD"
echo step >> "$FAKE_LOG"
sed -e 's/prt_fixture_0/prt_fixture_1/g' -e 's/msg_fixture_0001/msg_fixture_0002/g' "$FAKE_STREAM"
`

// TestOpenCodeRecordedStopHoldsTheCap replays the recorded stop under costroid
// run with its cap, USD 0.001. The run is stopped at the cap after its first
// step and before a second is printed: one step, four rows, USD 0.001438275
// Measured, the figure OpenCode stated for it, and over the cap by USD
// 0.000438275, the step that crossed it. The run reads wrapper-watchdog, and
// the grade's note states that limit with these figures.
func TestOpenCodeRecordedStopHoldsTheCap(t *testing.T) {
	var manifest struct {
		Recorded bool   `json:"recorded"`
		Run      string `json:"run"`
		Version  string `json:"version"`
		Scenario string `json:"scenario"`
	}
	data, err := os.ReadFile(filepath.Join(recordedStop, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if !manifest.Recorded || manifest.Run != "43223648675" || manifest.Version != "1.19.4" || manifest.Scenario != "stop" {
		t.Fatalf("the fixture's manifest reads %+v", manifest)
	}

	dir := t.TempDir()
	command := filepath.Join(dir, "opencode")
	if err := os.WriteFile(command, []byte(stoppedOpenCode), 0o755); err != nil {
		t.Fatal(err)
	}
	stream, err := filepath.Abs(filepath.Join(recordedStop, "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "steps")
	const hold = 5 * time.Second
	for name, value := range map[string]string{
		"FAKE_STREAM": stream, "FAKE_LOG": log, "FAKE_HOLD": strconv.Itoa(int(hold.Seconds())),
		"ANTHROPIC_API_KEY": "test-key", "OPENCODE_AUTH_CONTENT": "",
		"HOME": t.TempDir(), "XDG_CONFIG_HOME": t.TempDir(), "XDG_DATA_HOME": t.TempDir(),
	} {
		t.Setenv(name, value)
	}
	t.Chdir(t.TempDir())
	pinned, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	budget := decimal.MustParse("0.001")
	var written, stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	started := time.Now()
	result, err := run.Run(ctx, run.Options{
		Agent:   opencode.Name,
		Argv:    []string{command, "run", "--model", "anthropic/claude-haiku-5-5", "work"},
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

	if !result.Stopped {
		t.Errorf("the run was not stopped at the cap")
	}
	if elapsed := time.Since(started); elapsed >= hold {
		t.Errorf("the run took %s: the agent ran out its hold", elapsed)
	}
	steps, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(steps), "step"); count != 1 || len(result.Report.Requests) != 1 {
		t.Errorf("the agent printed %d steps and the run read %d, want the run stopped after the first", count, len(result.Report.Requests))
	}

	want := decimal.MustParse("0.001438275")
	rows := stoppedRows(t, written.Bytes())
	var total decimal.Decimal
	for _, row := range rows {
		cost, err := decimal.Parse(row.ListCost)
		if err != nil {
			t.Fatal(err)
		}
		total = total.Add(cost)
	}
	if result.ListTotal.Cmp(want) != 0 || total.Cmp(want) != 0 {
		t.Errorf("the run comes to USD %s, its rows to USD %s, not USD %s", result.ListTotal, total, want)
	}
	over := result.ListTotal.Sub(budget)
	if over.Cmp(decimal.MustParse("0.000438275")) != 0 {
		t.Errorf("the run passed the cap by USD %s", over)
	}
	if stated := result.Report.AgentStated; stated == nil || *stated != (adapter.Stated{Unit: opencode.StatedUnit, Quantity: want.String(), Source: opencode.Name}) {
		t.Errorf("OpenCode's own figure %+v, want USD %s", stated, want)
	}

	// 4 uncached input, 10543 five-minute cache writes and 173 output with 67
	// reasoning, at catalog 0.4.1's Haiku 5.5 prices per million tokens:
	// 4 × 0.10 + 10543 × 0.125 + 240 × 0.50. Reasoning is informational.
	classes := map[focus.TokenClass]string{}
	for _, row := range rows {
		classes[row.SkuMeter] = row.ConsumedQuantity
		if row.Tags[focus.TagEnforcementGrade] != adapter.GradeWrapperWatchdog || row.Tags[focus.TagCostBasis] != string(focus.Measured) {
			t.Errorf("a row reads the grade %q and the basis %q", row.Tags[focus.TagEnforcementGrade], row.Tags[focus.TagCostBasis])
		}
	}
	wantClasses := map[focus.TokenClass]string{focus.InputUncached: "4", focus.CacheWrite5m: "10543", focus.Output: "240", focus.ReasoningOutput: "67"}
	if len(rows) != len(wantClasses) || len(classes) != len(wantClasses) {
		t.Errorf("%d rows over the classes %v, want %v", len(rows), classes, wantClasses)
	}
	for class, quantity := range wantClasses {
		if classes[class] != quantity {
			t.Errorf("%s reads %q tokens, want %s", class, classes[class], quantity)
		}
	}

	note := result.Enforcement.Note
	if result.Enforcement.Grade != adapter.GradeWrapperWatchdog || !result.Enforcement.Watchdog {
		t.Errorf("the run earned %+v", result.Enforcement)
	}
	for _, said := range []string{"between steps", "one step can pass it", "USD " + want.String(), "USD " + budget.String() + " cap", "USD " + over.String()} {
		if !strings.Contains(note, said) {
			t.Errorf("the grade's note %q does not say %q", note, said)
		}
	}
}

// stoppedRow is the part of a written row the stop's test reads.
type stoppedRow struct {
	ListCost         string            `json:"ListCost"`
	ConsumedQuantity string            `json:"ConsumedQuantity"`
	SkuMeter         focus.TokenClass  `json:"SkuMeter"`
	Tags             map[string]string `json:"Tags"`
}

func stoppedRows(t *testing.T, written []byte) []stoppedRow {
	t.Helper()
	var rows []stoppedRow
	for _, line := range bytes.Split(bytes.TrimSpace(written), []byte("\n")) {
		var row stoppedRow
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	return rows
}
