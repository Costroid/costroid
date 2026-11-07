package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The values below are planted by the tests. None came from GitHub.
const (
	plantedOwner     = "planted-owner"
	plantedCommit    = "8e3b5a1c2d4f60718293a4b5c6d7e8f901a2b3c4"
	plantedBase      = "7d2a4b0c1e3f5f607182930a4b5c6d7e8f901a2b"
	plantedWorkflow  = 42000000017
	plantedCheckRuns = 128000000101
)

// plantedResult is a fenced result object as a check run's text holds it.
func plantedResult(checkRun int64) string {
	return fmt.Sprintf("```json\n"+`{
  "schema": "costroid.result/0.5",
  "check_run_id": %d,
  "state": "reported",
  "head_sha": %q,
  "ci_run": {
    "id": %d,
    "attempt": 1
  },
  "amounts": "shown",
  "figures": [
    {
      "currency": "USD",
      "list_cost": "0.25",
      "cost_basis": "measured",
      "tokens": {
        "output": "300"
      }
    }
  ],
  "policy": {
    "config_ref": %q
  }
}`+"\n```", checkRun, plantedCommit, plantedWorkflow, plantedBase)
}

func plantedCheckRun(id int64) string {
	return fmt.Sprintf(`{"id":%d,"name":"costroid","node_id":"CR_planted","head_sha":%q,"html_url":"https://github.com/%s/widgets/runs/%d",`+
		`"status":"completed","conclusion":"neutral","output":{"title":"Verdict: neutral","summary":"[CI run](https://github.com/%s/widgets/actions/runs/%d)","text":%q},`+
		`"app":{"slug":"github-actions","owner":{"login":%q}},"pull_requests":[{"number":4,"url":"https://api.github.com/repos/%s/widgets/pulls/4"}]}`,
		id, plantedCommit, plantedOwner, id, plantedOwner, plantedWorkflow, plantedResult(id), plantedOwner, plantedOwner)
}

func plantedList(t *testing.T) string {
	t.Helper()
	raw := t.TempDir()
	list := fmt.Sprintf(`{"total_count":2,"check_runs":[%s,%s]}`, plantedCheckRun(plantedCheckRuns), plantedCheckRun(plantedCheckRuns+1))
	write(t, filepath.Join(raw, "check-runs.json"), list)
	if err := writeManifest(raw, manifest{Recorded: true, Host: hostGitHub, Version: "2022-11-28", Scenario: "check-runs"}); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestScrubCutsACheckRunListToItsAllowlist(t *testing.T) {
	out := scrubbed(t, plantedList(t), nil)
	data, err := os.ReadFile(filepath.Join(out, "check-runs.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, planted := range []string{plantedOwner, "https://", "CR_planted", plantedCommit, plantedBase, fmt.Sprint(plantedWorkflow), fmt.Sprint(plantedCheckRuns), "pull_requests", "summary"} {
		if bytes.Contains(data, []byte(planted)) {
			t.Errorf("the recording still holds %q:\n%s", planted, data)
		}
	}
	parsed, err := parseValue(data)
	if err != nil {
		t.Fatal(err)
	}
	runs := parsed.field("check_runs").items
	if len(runs) != 2 {
		t.Fatalf("%d check runs", len(runs))
	}
	for index, run := range runs {
		id := fmt.Sprintf("9000%04d", index+1)
		if got := run.field("id").raw; got != id {
			t.Errorf("check run %d has id %s, want %s", index, got, id)
		}
		if got := run.fieldText("head_sha"); got != fmt.Sprintf("%040d", 1) {
			t.Errorf("check run %d sits on %s", index, got)
		}
		object, ok := fencedObject(run.field("output").fieldText("text"))
		if !ok {
			t.Fatalf("check run %d's text is no longer a fenced object", index)
		}
		for key, want := range map[string]string{
			"check_run_id": id,
			"head_sha":     fmt.Sprintf("%q", fmt.Sprintf("%040d", 1)),
			"amounts":      `"shown"`,
		} {
			if got := object.field(key).raw; got != want {
				t.Errorf("check run %d's object has %s %s, want %s", index, key, got, want)
			}
		}
		if got := object.field("ci_run").field("id").raw; got != "80000001" {
			t.Errorf("the workflow run is %s", got)
		}
		if got := object.field("policy").fieldText("config_ref"); got != fmt.Sprintf("%040d", 2) {
			t.Errorf("the settings' commit is %s", got)
		}
		if got := object.field("figures").items[0].field("list_cost").text; got != "0.25" {
			t.Errorf("the figure became %s", got)
		}
	}
	if problems, err := verifyDirectory(out, nil); err != nil || len(problems) > 0 {
		t.Errorf("verify refused the scrubbed list: %v %v", problems, err)
	}
	sameFiles(t, scrubbed(t, out, nil), out)
}

func TestVerifyRefusesARawCheckRunList(t *testing.T) {
	clean := scrubbed(t, plantedList(t), nil)
	textOf := func(line *value) *value { return line.field("check_runs").items[0].field("output") }
	cases := []struct {
		name   string
		change func(*value)
		want   string
	}{
		{"a raw check run id", func(line *value) {
			setField(line.field("check_runs").items[0], "id", &value{kind: kindLiteral, raw: "128000000101"})
		}, "check_runs[].id is not a synthetic check run id"},
		{"a check run id as a string", func(line *value) {
			setField(line.field("check_runs").items[0], "id", stringValue("128000000101"))
		}, "check_runs[].id is not a synthetic check run id"},
		{"a raw id in the object", func(line *value) {
			setField(textOf(line), "text", stringValue(plantedResult(plantedCheckRuns)))
		}, "result.check_run_id is not a synthetic check run id"},
		{"text that is no object", func(line *value) {
			setField(textOf(line), "text", stringValue("the run's own words"))
		}, "check_runs[].output.text is not a fenced result object"},
		{"a URL beside the run", func(line *value) {
			setField(line.field("check_runs").items[0], "html_url", stringValue("https://github.com/"+plantedOwner))
		}, "key outside the allowlist: check_runs[].html_url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "recording")
			transformDir(t, clean, dir, tc.change)
			copyManifest(t, clean, dir)
			problems, err := verifyDirectory(dir, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, tc.want) }) {
				t.Errorf("verify did not say %q; it said %v", tc.want, problems)
			}
		})
	}
	t.Run("a host beside an agent", func(t *testing.T) {
		if problems := (manifest{Recorded: true, Host: hostGitHub, Agent: agentCodex, Version: "2022-11-28"}).problems(); len(problems) != 1 {
			t.Errorf("problems %v", problems)
		}
	})
}
