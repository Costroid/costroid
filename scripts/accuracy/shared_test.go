package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// keepShared saves d as the reports job does on a day other runs spent on the
// key outside Costroid, naming those runs in -shared.
func keepShared(t *testing.T, inputs string, d day, runs []sharedRun) result {
	t.Helper()
	data, err := json.Marshal(runs)
	if err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(t.TempDir(), "shared.json")
	if err := os.WriteFile(shared, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return runAccuracy(t, reportsWorld(t, d), saveArgs(inputs, d, rowsFileOf(t, d), "-shared", shared)...)
}

func TestRunsOutsideCostroidAreNamedAndFailTheDay(t *testing.T) {
	runs := []sharedRun{
		{Workflow: "record", Run: "202", Step: "Run claude-code-action"},
		{Workflow: "canary", Run: "1001", Step: "Run the agent"},
		{Workflow: "record", Run: "202", Step: "Run claude-code-action"},
	}

	missed := passingDay(firstDay)
	missed.outputReported = 52000
	missed.amountCents, missed.listCents = "83.000000", "83.000000"
	inputs := t.TempDir()
	if got := keepShared(t, inputs, missed, runs); got.code != 0 {
		t.Fatalf("-save exited %d: %s", got.code, got.stderr)
	}
	page, got := writeAndCheck(t, inputs, 1)
	if got.code != exitRefused {
		t.Fatalf("-check exited %d on a day the key was used outside Costroid, want %d", got.code, exitRefused)
	}
	text := dayLine(t, page, firstDay)
	if want := failTokens + ", " + failShared; verdictOf(t, text) != want {
		t.Errorf("verdict %q, want %q: %s", verdictOf(t, text), want, text)
	}
	if want := "| shared_key 0.02 from runs 202 (record), 1001 (canary) |"; !strings.Contains(text, want) {
		t.Errorf("the residual's cause does not name the runs, %q: %s", want, text)
	}

	matched := passingDay(firstDay)
	inputs = t.TempDir()
	if got := keepShared(t, inputs, matched, runs[:1]); got.code != 0 {
		t.Fatalf("-save exited %d: %s", got.code, got.stderr)
	}
	page, got = writeAndCheck(t, inputs, 1)
	if got.code != exitRefused {
		t.Fatalf("-check exited %d on a day whose figures matched with the key used outside Costroid, want %d", got.code, exitRefused)
	}
	text = dayLine(t, page, firstDay)
	if verdictOf(t, text) != failShared || !strings.Contains(text, "| shared_key from run 202 (record) |") {
		t.Errorf("a day that matched with the key used outside Costroid does not fail as one: %s", text)
	}
}

func TestSharedRunsNameAWorkflowAndANumericRun(t *testing.T) {
	for name, runs := range map[string][]sharedRun{
		"no workflow":         {{Run: "202"}},
		"a run that is no id": {{Workflow: "record", Run: "latest"}},
		"no run":              {{Workflow: "record"}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := keepShared(t, t.TempDir(), passingDay(firstDay), runs); got.code != exitFailure {
				t.Errorf("-save exited %d, want %d: %s", got.code, exitFailure, got.stderr)
			}
		})
	}
}
