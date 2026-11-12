package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/adapter"
)

// TestOpenCodeClaimsNoGradeWithoutARecordedStop plans the grade none, with
// the watchdog on: the wrapper stops the run at the cap between steps, and
// the note says no recorded run has proven it. The one recording under
// testdata never reached its cap. A recorded stop placed there fails this
// test with what to do next, because the grade can then be claimed.
func TestOpenCodeClaimsNoGradeWithoutARecordedStop(t *testing.T) {
	manifests, err := filepath.Glob(filepath.Join(testdata, "*", "*", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) == 0 {
		t.Fatalf("no recording under %s", testdata)
	}
	for _, path := range manifests {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var manifest struct {
			Recorded bool   `json:"recorded"`
			Scenario string `json:"scenario"`
		}
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		dir := filepath.Dir(path)
		if manifest.Recorded && (strings.Contains(manifest.Scenario, "stop") || strings.Contains(filepath.Base(dir), "stop")) {
			t.Fatalf("%s records a stop at the cap. Replay it under costroid run in a test that holds the run to its recorded cap, plan wrapper-watchdog with a note naming that run, state the grade in docs/adapters.md, docs/recipes.md and recipes/github-opencode.yml, and replace this test", dir)
		}
	}

	isolate(t)
	plan, err := Agent{}.Plan([]string{"opencode", "run", "--model", recordedModel, "work"}, budget)
	if err != nil {
		t.Fatal(err)
	}
	want := adapter.Enforcement{
		Grade:    adapter.GradeNone,
		Note:     "none: the wrapper stops the run at the cap between steps, and no recorded run has proven that stop yet",
		Watchdog: true,
	}
	if plan.Enforcement != want {
		t.Errorf("the plan claims %+v, want %+v", plan.Enforcement, want)
	}
}
