package main

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// recordingVariable names the checkout of the repository that holds the
// recording workflows. The test skips without it.
const recordingVariable = "COSTROID_RECORDING_DIR"

// scenarios are the runs the recording workflow can record, in its order.
var scenarios = []string{
	"action-session", "action-subscription", "action-budget-stop", "nested-hook", "codex-subagent",
	"codex-action", "codex-openai-key", "claude-current", "claude-1h-cache", "claude-budget-stop",
	"codex-stopped-current", "sdk-claude-ts", "sdk-claude-py", "sdk-codex",
}

// The one other upload a recording workflow may make: the rows file of a run
// under Costroid, under a name that starts with rowsArtifact, kept for
// maxRowsDays at most.
const (
	rowsArtifact = "costroid-rows-"
	rowsPath     = "${{ runner.temp }}/rows.jsonl"
	maxRowsDays  = 14
)

type workflow struct {
	On          map[string]any `yaml:"on"`
	Permissions any            `yaml:"permissions"`
	Jobs        map[string]struct {
		Permissions map[string]string `yaml:"permissions"`
		Strategy    struct {
			Matrix map[string]any `yaml:"matrix"`
		} `yaml:"strategy"`
		Steps []struct {
			Uses string            `yaml:"uses"`
			With map[string]string `yaml:"with"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func readWorkflow(t *testing.T, dir, name string) workflow {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	var w workflow
	if err := yaml.Unmarshal(data, &w); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return w
}

var (
	pinnedAction = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+@[0-9a-f]{40}$`)
	weeklyCron   = regexp.MustCompile(`^[0-9]{1,2} [0-9]{1,2} \* \* [0-6]$`)
)

// checkSteps holds both workflows to the same rules: every action pinned to a
// commit, no permission that writes, every upload of the scrubbed directory
// alone, kept 3 days, and every other upload the rows file alone, under a
// name that starts with costroid-rows-, kept 14 days at most.
func checkSteps(t *testing.T, name string, w workflow) (recordings, rows int) {
	t.Helper()
	if fmt.Sprint(w.Permissions) != "map[]" {
		t.Errorf("%s: the workflow's permissions are %v, want {}", name, w.Permissions)
	}
	for jobName, job := range w.Jobs {
		for scope, level := range job.Permissions {
			if level != "read" {
				t.Errorf("%s: job %s grants %s: %s", name, jobName, scope, level)
			}
		}
		for _, step := range job.Steps {
			if step.Uses == "" || strings.HasPrefix(step.Uses, "./") {
				continue
			}
			if !pinnedAction.MatchString(step.Uses) {
				t.Errorf("%s: %s is not pinned to a commit", name, step.Uses)
			}
			if !strings.HasPrefix(step.Uses, "actions/upload-artifact@") {
				continue
			}
			if strings.HasPrefix(step.With["name"], rowsArtifact) {
				rows++
				checkRowsUpload(t, name, step.With)
				continue
			}
			recordings++
			if step.With["path"] != "${{ runner.temp }}/recording" {
				t.Errorf("%s: an upload's path is %q, not the scrubbed directory", name, step.With["path"])
			}
			if step.With["retention-days"] != "3" {
				t.Errorf("%s: an upload is kept %q days, want 3", name, step.With["retention-days"])
			}
			if !strings.HasPrefix(step.With["name"], "recording-") {
				t.Errorf("%s: an upload is named %q, which make fixture does not download", name, step.With["name"])
			}
		}
	}
	return recordings, rows
}

// checkRowsUpload holds an upload of the rows to the rows file alone, kept
// from 1 to maxRowsDays days.
func checkRowsUpload(t *testing.T, name string, with map[string]string) {
	t.Helper()
	if with["path"] != rowsPath {
		t.Errorf("%s: the upload %s holds %q, not the rows file alone", name, with["name"], with["path"])
	}
	if days, err := strconv.Atoi(with["retention-days"]); err != nil || days < 1 || days > maxRowsDays {
		t.Errorf("%s: the upload %s is kept %q days, want 1 to %d", name, with["name"], with["retention-days"], maxRowsDays)
	}
}

func TestRecordWorkflowShape(t *testing.T) {
	dir := os.Getenv(recordingVariable)
	if dir == "" {
		t.Skip(recordingVariable + " names no checkout of the recording workflows")
	}

	record := readWorkflow(t, dir, "record.yml")
	if triggers := slices.Sorted(maps.Keys(record.On)); !slices.Equal(triggers, []string{"workflow_dispatch"}) {
		t.Errorf("record.yml runs on %v, want workflow_dispatch alone", triggers)
	}
	dispatch, _ := record.On["workflow_dispatch"].(map[string]any)
	inputs, _ := dispatch["inputs"].(map[string]any)
	if len(inputs) != 1 || inputs["scenario"] == nil {
		t.Errorf("record.yml takes inputs %v, want scenario alone", slices.Sorted(maps.Keys(inputs)))
	}
	scenario, _ := inputs["scenario"].(map[string]any)
	if scenario["type"] != "choice" || fmt.Sprint(scenario["options"]) != fmt.Sprint(scenarios) {
		t.Errorf("record.yml's scenario is %v of %v, want a choice of %v", scenario["type"], scenario["options"], scenarios)
	}
	if recordings, rows := checkSteps(t, "record.yml", record); recordings != 1 || rows > 1 {
		t.Errorf("record.yml makes %d recording uploads and %d rows uploads, want 1 and at most 1", recordings, rows)
	}
	for _, name := range scenarios {
		if data, err := os.ReadFile(filepath.Join(dir, "record", "prompts", name+".txt")); err != nil || len(strings.TrimSpace(string(data))) < minimumPromptLine {
			t.Errorf("record/prompts/%s.txt is missing or shorter than %d bytes: %v", name, minimumPromptLine, err)
		}
	}

	canary := readWorkflow(t, dir, "canary.yml")
	for trigger := range canary.On {
		if trigger != "schedule" && trigger != "workflow_dispatch" {
			t.Errorf("canary.yml runs on %s", trigger)
		}
	}
	schedule, _ := canary.On["schedule"].([]any)
	if len(schedule) != 1 || !weeklyCron.MatchString(cron(schedule[0])) {
		t.Errorf("canary.yml's schedule is %v, want one weekly cron", schedule)
	}
	var matrix []string
	for _, job := range canary.Jobs {
		listed, _ := job.Strategy.Matrix["scenario"].([]any)
		for _, s := range listed {
			matrix = append(matrix, fmt.Sprint(s))
		}
	}
	if !slices.Equal(matrix, []string{"claude-current", "codex-stopped-current"}) {
		t.Errorf("canary.yml runs %v, want claude-current and codex-stopped-current", matrix)
	}
	if recordings, rows := checkSteps(t, "canary.yml", canary); recordings != 1 || rows > 1 {
		t.Errorf("canary.yml makes %d recording uploads and %d rows uploads, want 1 and at most 1", recordings, rows)
	}
}

// cron is a schedule entry's cron expression.
func cron(entry any) string {
	fields, _ := entry.(map[string]any)
	text, _ := fields["cron"].(string)
	return text
}
