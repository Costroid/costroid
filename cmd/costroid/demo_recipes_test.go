package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/comment"
	"go.yaml.in/yaml/v3"
)

// demoVariable names a checkout of Costroid/costroid-demo. Without it the
// tests read costroid-demo beside this repository's root.
const demoVariable = "COSTROID_DEMO_DIR"

// demoAgentWorkflow is the demo's live workflow, which GitHub runs, and
// demoRecordedWorkflow the one that posts the recording on a fork's pull
// request.
const (
	demoAgentWorkflow    = ".github/workflows/costroid.yml"
	demoRecordedWorkflow = ".github/workflows/costroid-recorded.yml"
	demoRecording        = "recorded/rows.jsonl"
)

// demoRecipes are the files in the demo that costroid init writes, each with
// the agent init was given. The second is kept out of .github/workflows, so a
// pull request pays for one agent run.
var demoRecipes = map[string]string{
	demoAgentWorkflow:          "claude-code-action",
	"recipes/costroid-run.yml": "claude",
}

// demoEnvironment is the one change the demo makes to what init writes: each
// agent job names the environment that holds the key, so no other job reads
// it.
const demoEnvironment = "    # The environment demo holds ANTHROPIC_API_KEY, so no other job can read it.\n    environment: demo\n"

// demoReleasePin is the pin init writes for a release.
var demoReleasePin = regexp.MustCompile(regexp.QuoteMeta(costroidActionPath) + `@([0-9a-f]{40}) # v([0-9]+\.[0-9]+\.[0-9]+[0-9A-Za-z.-]*)`)

// demoDir is the demo's checkout, or "" when there is none.
func demoDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv(demoVariable)
	if dir == "" {
		dir = filepath.Join("..", "..", "..", "costroid-demo")
	}
	if _, err := os.Stat(filepath.Join(dir, demoAgentWorkflow)); err != nil {
		t.Skipf("no checkout of Costroid/costroid-demo at %s: set %s", dir, demoVariable)
	}
	return dir
}

// demoRecorded is the recording the demo's rows come from, as its manifest
// names it.
type demoRecorded struct {
	Run  string `json:"run"`
	Date string `json:"date"`
}

func readDemoRecorded(t *testing.T) demoRecorded {
	t.Helper()
	var recorded demoRecorded
	if err := readJSON(filepath.Join(actionDir, "manifest.json"), &recorded); err != nil {
		t.Fatal(err)
	}
	if recorded.Run == "" || recorded.Date == "" {
		t.Fatalf("the recording's manifest names no run and date: %+v", recorded)
	}
	return recorded
}

// demoWorkflows lists the YAML files under .github/workflows and recipes, as
// paths from the demo's root.
func demoWorkflows(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	for _, sub := range []string{defaultWorkflows, "recipes"} {
		err := filepath.WalkDir(filepath.Join(dir, sub), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if extension := filepath.Ext(path); !entry.IsDir() && (extension == ".yml" || extension == ".yaml") {
				relative, err := filepath.Rel(dir, path)
				found = append(found, filepath.ToSlash(relative))
				return err
			}
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
	}
	slices.Sort(found)
	return found
}

// demoRelease reads the release the demo's live workflow pins, and holds the
// commit to the one the release's tag names in this checkout.
func demoRelease(t *testing.T, dir string) pin {
	t.Helper()
	match := demoReleasePin.FindStringSubmatch(readFile(t, filepath.Join(dir, demoAgentWorkflow)))
	if match == nil {
		t.Fatalf("%s pins no release of %s", demoAgentWorkflow, costroidActionPath)
	}
	tagged, err := exec.Command("git", "rev-parse", "v"+match[2]+"^{commit}").Output()
	if err != nil {
		t.Fatalf("the tag v%s: %v", match[2], err)
	}
	if got := strings.TrimSpace(string(tagged)); got != match[1] {
		t.Fatalf("the demo pins v%s at %s, and its tag names %s", match[2], match[1], got)
	}
	return pin{commit: match[1], version: match[2]}
}

// initWrites is what costroid init writes for an agent at a release: the
// release's own recipe, read at its tag, pinned and headed as init does.
func initWrites(t *testing.T, release pin, agent string) string {
	t.Helper()
	name := initRecipes[githubHost][agent]
	source, err := exec.Command("git", "show", "v"+release.version+":recipes/"+name).Output()
	if err != nil {
		t.Fatalf("recipes/%s at v%s: %v", name, release.version, err)
	}
	written, err := buildRecipe(name, source, release, initFlags{agent: agent, host: githubHost})
	if err != nil {
		t.Fatal(err)
	}
	return string(written)
}

// withDemoEnvironment names the environment on the agent job, before its
// runner.
func withDemoEnvironment(t *testing.T, recipe string) string {
	t.Helper()
	job := strings.Index(recipe, "\n  agent:\n")
	runner := strings.Index(recipe[max(job, 0):], "\n    runs-on: ")
	if job < 0 || runner < 0 {
		t.Fatal("the recipe has no agent job with a runner")
	}
	at := job + runner + 1
	return recipe[:at] + demoEnvironment + recipe[at:]
}

// TestDemoRecipesMatchRelease holds every workflow in Costroid/costroid-demo
// to the release it pins: each recipe byte for byte as costroid init writes
// it at that release from the release's own recipe, with the environment that
// holds the key on its agent job; and the recorded workflow on the same pin,
// posting the recording the demo command renders, under the run and the day
// its manifest names. Any other workflow there fails.
func TestDemoRecipesMatchRelease(t *testing.T) {
	dir := demoDir(t)
	release := demoRelease(t, dir)
	want := slices.Sorted(func(yield func(string) bool) {
		for path := range demoRecipes {
			if !yield(path) {
				return
			}
		}
		yield(demoRecordedWorkflow)
	})
	if got := demoWorkflows(t, dir); !slices.Equal(got, want) {
		t.Errorf("the demo holds the workflows %v, want %v", got, want)
	}
	for path, agent := range demoRecipes {
		expected := withDemoEnvironment(t, initWrites(t, release, agent))
		if got := readFile(t, filepath.Join(dir, path)); got != expected {
			t.Errorf("%s is not what costroid init --agent %s writes at v%s, with the environment demo:\n%s", path, agent, release.version, lineDiff(expected, got))
		}
	}
	holdAgentJob(t, dir)
	holdRecordedWorkflow(t, dir, release)
	recording, err := os.ReadFile(filepath.Join(dir, demoRecording))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(recording, []byte(demoRows)) {
		t.Errorf("%s is not %s, the rows costroid demo renders", demoRecording, demoRowsFile)
	}
}

// holdAgentJob reads the condition on the live workflow's agent job: a branch
// of this repository, from a member or with the label a maintainer adds.
func holdAgentJob(t *testing.T, dir string) {
	t.Helper()
	var workflow struct {
		Jobs map[string]struct {
			If          string `yaml:"if"`
			Environment string `yaml:"environment"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(readFile(t, filepath.Join(dir, demoAgentWorkflow))), &workflow); err != nil {
		t.Fatal(err)
	}
	agent := workflow.Jobs["agent"]
	for _, part := range []string{"github.event.pull_request.head.repo.full_name == github.repository", "author_association", "'costroid-agent'"} {
		if !strings.Contains(agent.If, part) {
			t.Errorf("the agent job's condition holds no %s: %s", part, agent.If)
		}
	}
	if agent.Environment != "demo" {
		t.Errorf("the agent job's environment is %q, want demo", agent.Environment)
	}
}

// holdRecordedWorkflow reads the recorded workflow: it follows the live
// workflow on a fork's pull request alone, installs the release the demo
// pins, and reports the recording under its run and day.
func holdRecordedWorkflow(t *testing.T, dir string, release pin) {
	t.Helper()
	source := readFile(t, filepath.Join(dir, demoRecordedWorkflow))
	var workflow struct {
		On struct {
			WorkflowRun struct {
				Workflows []string `yaml:"workflows"`
				Types     []string `yaml:"types"`
			} `yaml:"workflow_run"`
		} `yaml:"on"`
		Jobs map[string]struct {
			If    string `yaml:"if"`
			Steps []struct {
				Uses string `yaml:"uses"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(source), &workflow); err != nil {
		t.Fatal(err)
	}
	var live struct {
		Name string `yaml:"name"`
	}
	if err := yaml.Unmarshal([]byte(readFile(t, filepath.Join(dir, demoAgentWorkflow))), &live); err != nil {
		t.Fatal(err)
	}
	if run := workflow.On.WorkflowRun; !slices.Equal(run.Workflows, []string{live.Name}) || !slices.Equal(run.Types, []string{"completed"}) {
		t.Errorf("the recorded workflow follows %v on %v, want [%s] on [completed]", run.Workflows, run.Types, live.Name)
	}
	pins := demoReleasePin.FindAllStringSubmatch(source, -1)
	if len(pins) != 1 || pins[0][1] != release.commit || pins[0][2] != release.version {
		t.Errorf("the recorded workflow pins %v, want v%s at %s once", pins, release.version, release.commit)
	}
	recorded := readDemoRecorded(t)
	command := fmt.Sprintf("costroid report --rows %s --recorded %s,%s", demoRecording, recorded.Run, recorded.Date)
	reports := 0
	for name, job := range workflow.Jobs {
		if !strings.Contains(job.If, "github.event.workflow_run.head_repository.full_name != github.repository") {
			t.Errorf("the job %s runs on a branch of this repository too: %s", name, job.If)
		}
		for _, step := range job.Steps {
			if strings.Contains(step.Run, "costroid report") {
				reports++
				if strings.TrimSpace(step.Run) != command {
					t.Errorf("the job %s runs %q, want %q", name, strings.TrimSpace(step.Run), command)
				}
			}
		}
	}
	if reports != 1 {
		t.Errorf("the recorded workflow reports %d times, want once", reports)
	}
}

// lineDiff names the first line where two texts part.
func lineDiff(want, got string) string {
	wanted, have := strings.Split(want, "\n"), strings.Split(got, "\n")
	for index := range max(len(wanted), len(have)) {
		var w, g string
		if index < len(wanted) {
			w = wanted[index]
		}
		if index < len(have) {
			g = have[index]
		}
		if w != g {
			return fmt.Sprintf("line %d is %q, want %q", index+1, g, w)
		}
	}
	return "the texts are equal"
}

// recordedLine is the line a report on the recording opens with.
func recordedLine(recorded demoRecorded) string {
	return fmt.Sprintf("These figures are a recorded run's, workflow run %s on %s, not this pull request's.", recorded.Run, recorded.Date)
}

// TestRecordedRowsSayRecordedAndTheDate reports the demo's recording as the
// recorded workflow does, on a fork's pull request after its run: the check,
// the comment and the job summary open with the recorded line, the result
// object names the run and the day, the run line names no commit of the pull
// request, and the pull request's total is not read. The same rows reported
// as the pull request's own say none of it, and a value that names no run
// and day is refused.
func TestRecordedRowsSayRecordedAndTheDate(t *testing.T) {
	recorded := readDemoRecorded(t)
	value := recorded.Run + "," + recorded.Date
	line := recordedLine(recorded)
	object := fmt.Sprintf("  \"recorded\": {\n    \"run_id\": %s,\n    \"date\": %q\n  },", recorded.Run, recorded.Date)

	t.Run("on a fork's pull request after its run", func(t *testing.T) {
		stub := followUpEnvironment(t, "stranger/widgets")
		stub.Files[configFile] = "policy:\n  run_budget_usd: 5\n"
		stub.Branches["main"] = defaultBranchCommit
		t.Setenv("GITHUB_REF_NAME", "main")
		_, summary := stepFiles(t)
		code, stdout, stderr := execute(t, "report", "--rows", demoRowsFile, "--recorded", value, "--json")
		if code != 0 || len(stub.Checks) != 1 || len(stub.Comments) != 1 {
			t.Fatalf("exit %d, %d checks, %d comments: %s", code, len(stub.Checks), len(stub.Comments), stderr)
		}
		if posted := requestsWith(stub.Requests, "POST /repos/example/widgets/issues/"); len(posted) != 1 || !strings.HasSuffix(posted[0], "/issues/7/comments") {
			t.Errorf("the comment went to %v, not pull request 7", posted)
		}
		check, body := stub.Checks[0], stub.Comments[0].Body
		if check.HeadSHA != headCommit || !strings.HasPrefix(check.Summary, line+"\n\n") {
			t.Errorf("the check on %s does not open with the recorded line:\n%s", check.HeadSHA, check.Summary)
		}
		if !strings.HasPrefix(body, comment.Marker+"\n### Costroid\n\n"+line+"\n\n") || strings.Count(body, line) != 1 {
			t.Errorf("the comment does not open with the recorded line once:\n%s", body)
		}
		if !strings.HasPrefix(readFile(t, summary), "### Costroid\n\n"+line+"\n\n") {
			t.Errorf("the job summary does not open with the recorded line:\n%s", readFile(t, summary))
		}
		for name, text := range map[string]string{"check": check.Text, "comment": body, "result": stdout} {
			if !strings.Contains(text, object) {
				t.Errorf("the %s's object does not name the recorded run:\n%s", name, text)
			}
			if !strings.Contains(text, `"lifetime": null`) {
				t.Errorf("the %s's object states a total for the pull request:\n%s", name, text)
			}
		}
		validateResult(t, stdout)
		if strings.Contains(body, headCommit[:7]+"`") {
			t.Errorf("the comment names the pull request's commit beside the recorded figures:\n%s", body)
		}
		if read := requestsWith(stub.Requests, "/pulls/7/commits"); len(read) != 0 {
			t.Errorf("the report read the pull request's total: %v", read)
		}
		if !strings.Contains(body, "This run cost USD "+demoTotal+" at list (Measured).") {
			t.Errorf("the comment does not state the recorded figure:\n%s", body)
		}
	})

	t.Run("the same rows as the pull request's own", func(t *testing.T) {
		stub := reportEnvironment(t, pullRequestEvent())
		code, stdout, stderr := execute(t, "report", "--rows", demoRowsFile, "--json")
		if code != 0 || len(stub.Comments) != 1 {
			t.Fatalf("exit %d, %d comments: %s", code, len(stub.Comments), stderr)
		}
		if body := stub.Comments[0].Body; strings.Contains(body, "recorded run") || !strings.Contains(body, "at commit `"+headCommit[:7]+"`") {
			t.Errorf("a report on the pull request's own rows reads as recorded:\n%s", body)
		}
		if !strings.Contains(stdout, `"recorded": null,`) {
			t.Errorf("the object names a recorded run:\n%s", stdout)
		}
	})

	t.Run("a value that names no run and day", func(t *testing.T) {
		reportEnvironment(t, pullRequestEvent())
		for _, bad := range []string{recorded.Run, recorded.Date, "0," + recorded.Date, "run," + recorded.Date, recorded.Run + ",2026-13-01", recorded.Run + ",07/11/2026", recorded.Run + "," + recorded.Date + "T11:33:17Z"} {
			code, _, stderr := execute(t, "report", "--rows", demoRowsFile, "--recorded", bad)
			if code != exitFailure || !strings.Contains(stderr, "--recorded takes RUN_ID,DATE") {
				t.Errorf("--recorded %s: exit %d: %s", bad, code, stderr)
			}
		}
	})

	t.Run("the manifest says the run was recorded", func(t *testing.T) {
		var manifest map[string]any
		if err := json.Unmarshal([]byte(readFile(t, filepath.Join(actionDir, "manifest.json"))), &manifest); err != nil {
			t.Fatal(err)
		}
		if manifest["recorded"] != true {
			t.Errorf("the manifest of %s does not say the run was recorded: %v", actionDir, manifest)
		}
	})
}
