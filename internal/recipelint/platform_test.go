package recipelint

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const connectRecipe = "github-connect.yml"

// recipesWithoutRows keep no rows of their own, so they have nothing to send:
// the fork follow-up posts a check from another run's rows, the approval
// re-posts a check, and the connect recipe binds the owner.
var recipesWithoutRows = []string{"github-approval.yml", connectRecipe, "github-follow-up.yml"}

// workflowFile is the part of a GitHub recipe these tests read.
type workflowFile struct {
	On          map[string]any `yaml:"on"`
	Permissions map[string]any `yaml:"permissions"`
	Jobs        map[string]struct {
		If          string            `yaml:"if"`
		Permissions map[string]string `yaml:"permissions"`
		Steps       []struct {
			Uses string            `yaml:"uses"`
			Run  string            `yaml:"run"`
			Env  map[string]string `yaml:"env"`
			With map[string]string `yaml:"with"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func readWorkflow(t *testing.T, name string) workflowFile {
	t.Helper()
	var file workflowFile
	if err := yaml.Unmarshal([]byte(read(t, filepath.Join(recipesDir, name))), &file); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return file
}

// keepsRows reports a recipe that keeps the rows as the artifact
// costroid-rows: through the action's run or read stage, or its own upload.
func keepsRows(file workflowFile) bool {
	for _, job := range file.Jobs {
		for _, step := range job.Steps {
			stage := step.With["stage"]
			if strings.HasPrefix(step.Uses, costroidAction+"@") && (stage == "run" || stage == "read") ||
				strings.HasPrefix(step.Uses, "actions/upload-artifact@") && step.With["name"] == "costroid-rows" {
				return true
			}
		}
	}
	return false
}

// TestRecipesSendToThePlatform holds every GitHub recipe that keeps rows to an
// ingest job that runs when either destination is named and passes the three
// platform inputs, and holds the lint to the same shape.
func TestRecipesSendToThePlatform(t *testing.T) {
	names, err := filepath.Glob(filepath.Join(recipesDir, "github-*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var withoutRows []string
	for _, path := range names {
		name := filepath.Base(path)
		file := readWorkflow(t, name)
		if !keepsRows(file) {
			withoutRows = append(withoutRows, name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			ingest, found := file.Jobs["ingest"]
			if !found || len(ingest.Steps) != 1 {
				t.Fatal("no ingest job of one step")
			}
			condition := strings.Join(strings.Fields(ingest.If), " ")
			if !strings.Contains(condition, "&& (vars.COSTROID_INGEST_URL != '' || vars.COSTROID_PLATFORM_URL != '')") {
				t.Errorf("the ingest job's if does not run on either variable: %s", condition)
			}
			want := map[string]string{
				"stage":           "ingest",
				"ingest-url":      "${{ vars.COSTROID_INGEST_URL }}",
				"ingest-audience": "${{ vars.COSTROID_INGEST_AUDIENCE }}",
				"platform-url":    "${{ vars.COSTROID_PLATFORM_URL }}",
				"installation-id": "github-owner:${{ github.repository_owner_id }}",
				"platform-token":  "${{ secrets.COSTROID_PLATFORM_TOKEN }}",
			}
			if got := ingest.Steps[0].With; !maps.Equal(got, want) {
				t.Errorf("the ingest step's inputs:\n%v\nwant\n%v", got, want)
			}
			if got := ingest.Permissions; !maps.Equal(got, map[string]string{"id-token": "write"}) {
				t.Errorf("the ingest job holds %v", got)
			}
		})
	}
	if !slices.Equal(withoutRows, recipesWithoutRows) {
		t.Errorf("the recipes that keep no rows are %v, want %v", withoutRows, recipesWithoutRows)
	}

	report, err := Lint([]string{recipesDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings)+len(report.Notes) != 0 {
		t.Errorf("findings %v, notes %v", report.Findings, report.Notes)
	}

	source := read(t, filepath.Join(recipesDir, "github-claude-code.yml"))
	const token = "          platform-token: ${{ secrets.COSTROID_PLATFORM_TOKEN }}\n"
	for _, change := range []edit{
		{"no installation", "          installation-id: github-owner:${{ github.repository_owner_id }}\n", "", RuleIngestPlatform},
		{"no token", token, "", RuleIngestPlatform},
		{"a token in the file", token, "          platform-token: costroid_it_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_AbCdE\n", RuleIngestPlatform},
		{"a token beside a secret", token, "          platform-token: costroid_it_${{ secrets.TAIL }}\n", RuleIngestPlatform},
		{"a GitLab installation", "github-owner:${{ github.repository_owner_id }}", "gitlab-namespace:4410287", RuleIngestPlatform},
		{"a ledger-only condition", "      && (vars.COSTROID_INGEST_URL != '' || vars.COSTROID_PLATFORM_URL != '')\n", "      && vars.COSTROID_INGEST_URL != ''\n", RuleIngestPlatform},
	} {
		t.Run(change.name, func(t *testing.T) {
			expectRule(t, source, change)
		})
	}
}

// TestConnectRecipeHoldsOnlyIdToken holds the GitHub connect recipe to a daily
// schedule and a push, a job that runs only on the schedule or the default
// branch with id-token: write and no other permission, and no step beyond
// the install and costroid connect. The GitLab job takes its proof through
// id_tokens, in the variable connect reads, in an environment of its own.
func TestConnectRecipeHoldsOnlyIdToken(t *testing.T) {
	file := readWorkflow(t, connectRecipe)
	if events := slices.Sorted(maps.Keys(file.On)); !slices.Equal(events, []string{"push", "schedule"}) {
		t.Errorf("events %v", events)
	}
	schedule, _ := file.On["schedule"].([]any)
	if len(schedule) != 1 || !strings.HasSuffix(schedule[0].(map[string]any)["cron"].(string), " * * *") {
		t.Errorf("schedule %v is not once a day", schedule)
	}
	if len(file.Permissions) != 0 || len(file.Jobs) != 1 {
		t.Fatalf("workflow permissions %v, %d jobs", file.Permissions, len(file.Jobs))
	}
	job := file.Jobs["connect"]
	if !maps.Equal(job.Permissions, map[string]string{"id-token": "write"}) {
		t.Errorf("the connect job holds %v", job.Permissions)
	}
	for _, want := range []string{"vars.COSTROID_PLATFORM_URL != ''", "github.event_name == 'schedule'", "github.ref == format('refs/heads/{0}', github.event.repository.default_branch)"} {
		if !strings.Contains(job.If, want) {
			t.Errorf("the job's if does not test %s", want)
		}
	}
	if len(job.Steps) != 2 || job.Steps[0].With["stage"] != "install" ||
		job.Steps[1].Run != `costroid connect --platform-url "$COSTROID_PLATFORM_URL"` ||
		job.Steps[1].Env["COSTROID_PLATFORM_TOKEN"] != "${{ secrets.COSTROID_PLATFORM_TOKEN }}" {
		t.Errorf("steps %+v", job.Steps)
	}
	source := read(t, filepath.Join(recipesDir, connectRecipe))
	if strings.Contains(strings.ToLower(source), "dogfood") {
		t.Error("the shipped recipe names an internal test")
	}
	for _, change := range []edit{
		{"contents", "      id-token: write\n", "      id-token: write\n      contents: read\n", RuleConnectJob},
		{"no id-token", "      id-token: write\n", "      contents: read\n", RuleConnectJob},
		{"id-token without connect", "        run: costroid connect --platform-url \"$COSTROID_PLATFORM_URL\"\n", "        run: echo \"$COSTROID_PLATFORM_URL\"\n", RuleIDTokenScope},
	} {
		t.Run(change.name, func(t *testing.T) {
			expectRule(t, source, change)
		})
	}

	var gitlab struct {
		Connect struct {
			Stage       string `yaml:"stage"`
			Environment struct {
				Name string `yaml:"name"`
			} `yaml:"environment"`
			IDTokens map[string]struct {
				Aud string `yaml:"aud"`
			} `yaml:"id_tokens"`
			Rules []struct {
				If string `yaml:"if"`
			} `yaml:"rules"`
			Script []string `yaml:"script"`
		} `yaml:"costroid:connect"`
	}
	if err := yaml.Unmarshal([]byte(read(t, filepath.Join(recipesDir, "gitlab-ci.yml"))), &gitlab); err != nil {
		t.Fatal(err)
	}
	connect := gitlab.Connect
	if len(connect.IDTokens) != 1 || connect.IDTokens["COSTROID_PLATFORM_PROOF"].Aud != "$COSTROID_PLATFORM_AUDIENCE" {
		t.Errorf("the GitLab job's id_tokens: %v", connect.IDTokens)
	}
	if connect.Stage != ".post" || connect.Environment.Name != "costroid-connect" {
		t.Errorf("the GitLab job runs in stage %q, environment %q", connect.Stage, connect.Environment.Name)
	}
	if len(connect.Rules) != 2 || !strings.Contains(connect.Rules[0].If, `$CI_PIPELINE_SOURCE == "schedule"`) ||
		!strings.Contains(connect.Rules[1].If, `$CI_PIPELINE_SOURCE == "push" && $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH`) {
		t.Errorf("the GitLab job's rules: %v", connect.Rules)
	}
	if !slices.Equal(connect.Script, []string{`costroid connect --platform-url "$COSTROID_PLATFORM_URL"`}) {
		t.Errorf("the GitLab job's script: %v", connect.Script)
	}
}

// TestRecipesPrintTheRunStatus runs the last step of each agent job that runs
// costroid run: it prints the status file as it is, and says when there is
// none, and prints nothing else.
func TestRecipesPrintTheRunStatus(t *testing.T) {
	for _, name := range []string{"github-claude-code.yml", "github-claude-code-litellm.yml", "github-codex.yml"} {
		t.Run(name, func(t *testing.T) {
			steps := readWorkflow(t, name).Jobs["agent"].Steps
			last := steps[len(steps)-1]
			if !strings.Contains(read(t, filepath.Join(recipesDir, name)), "      - name: Print the run's status\n        if: always()\n") {
				t.Fatal("the agent job's status step does not run always")
			}
			temp := t.TempDir()
			run := func(env ...string) string {
				command := exec.Command("bash", "-c", last.Run)
				command.Env = append([]string{"PATH=" + os.Getenv("PATH"), "RUNNER_TEMP=" + temp}, env...)
				output, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("%v: %s", err, output)
				}
				return string(output)
			}
			if got, want := run(), "costroid: the run wrote no status file at "+temp+"/costroid-status.json\n"; got != want {
				t.Errorf("with no file: %q, want %q", got, want)
			}
			const status = `{"spend":[{"basis":"measured","amount":"0.12","currency":"USD"}]}` + "\n"
			if err := os.WriteFile(filepath.Join(temp, "costroid-status.json"), []byte(status), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := run(); got != status {
				t.Errorf("with the file: %q", got)
			}
			other := filepath.Join(temp, "other.json")
			if err := os.WriteFile(other, []byte("{}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := run("COSTROID_STATUS_FILE=" + other); got != "{}\n" {
				t.Errorf("with COSTROID_STATUS_FILE: %q", got)
			}
		})
	}
}
