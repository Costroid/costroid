package main

import (
	"os"
	"strings"
	"testing"
)

// TestRunDefaultsAreStated runs an agent with no --out. The rows and the run's
// object land in the working directory under the action's names, and stderr
// says where before the agent starts. A cap is never assumed.
func TestRunDefaultsAreStated(t *testing.T) {
	agent := replayClaude(t)
	t.Chdir(t.TempDir())
	stated := "costroid: no --out was given, so the rows go to " + defaultRowsFile + " in the working directory\n"
	code, _, stderr := execute(t, "run", "--budget-usd", "5", "--", agent, "-p", "work")
	if code != 0 || !strings.HasPrefix(stderr, stated) {
		t.Fatalf("exit %d, stderr does not open with %q:\n%s", code, stated, stderr)
	}
	if !strings.Contains(stderr, "rows in "+defaultRowsFile+", catalog ") {
		t.Errorf("the summary names another file:\n%s", stderr)
	}
	for _, file := range []string{defaultRowsFile, runResultPath(defaultRowsFile)} {
		if info, err := os.Stat(file); err != nil || info.Size() == 0 {
			t.Errorf("%s: %v", file, err)
		}
	}

	code, _, stderr = execute(t, "run", "--budget-usd", "5", "--out", "named.jsonl", "--", agent, "-p", "work")
	if code != 0 || strings.Contains(stderr, "no --out") {
		t.Errorf("--out given: exit %d:\n%s", code, stderr)
	}
	if code, _, stderr := execute(t, "run", "--", agent, "-p", "work"); code != exitFailure || stderr != "costroid: run needs --budget-usd\n" {
		t.Errorf("no budget: exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(runHelp, "Without it, run writes\ncostroid-rows.jsonl in the working directory") {
		t.Error("run's help does not state the default rows file")
	}
}
