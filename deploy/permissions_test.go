package deploy

import (
	"bufio"
	"bytes"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/Costroid/costroid/internal/docstest"
)

const (
	actionFile  = "../action/action.yml"
	checkWrites = "../cmd/costroid/testdata/check-writes.golden"
	// workflowToken is the token a GitHub recipe's job holds.
	workflowToken = "GITHUB_TOKEN"
)

// recipeStep is a step of a recipe's job or of the action.
type recipeStep struct {
	If   string            `yaml:"if"`
	Uses string            `yaml:"uses"`
	With map[string]string `yaml:"with"`
	Run  string            `yaml:"run"`
}

type recipeWorkflow struct {
	Permissions map[string]string `yaml:"permissions"`
	Jobs        map[string]struct {
		Permissions map[string]string `yaml:"permissions"`
		Steps       []recipeStep      `yaml:"steps"`
	} `yaml:"jobs"`
}

// stageCondition names a stage in a step's if.
var stageCondition = regexp.MustCompile(`inputs\.stage == '([a-z]+)'`)

// actionScript is a script of the action a step runs.
var actionScript = regexp.MustCompile(`\$GITHUB_ACTION_PATH/([a-z-]+\.sh)`)

// recipeRuns reads what a recipe or a person runs, as the manifest names
// each: costroid commands, scripts of the action, gh api, and the steps of
// other publishers the manifest lists.
type recipeRuns struct {
	manifest docstest.Manifest
	// makers are every command and script a call of the manifest names.
	makers []string
	// stages maps each stage of the action to its steps.
	stages map[string][]recipeStep
}

func newRecipeRuns(t *testing.T, manifest docstest.Manifest) recipeRuns {
	t.Helper()
	var action struct {
		Runs struct {
			Steps []recipeStep `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(read(t, actionFile), &action); err != nil {
		t.Fatal(err)
	}
	stages := map[string][]recipeStep{}
	for _, step := range action.Runs.Steps {
		named := stageCondition.FindAllStringSubmatch(step.If, -1)
		if len(named) == 0 {
			named = [][]string{{"", ""}}
		}
		for _, stage := range named {
			stages[stage[1]] = append(stages[stage[1]], step)
		}
	}
	var makers []string
	for _, call := range manifest.Calls {
		makers = append(makers, call.Commands...)
	}
	slices.Sort(makers)
	return recipeRuns{manifest: manifest, makers: slices.Compact(makers), stages: stages}
}

// ofStep answers what one step runs. A step of the action runs that stage's
// steps, and those of every stage, and each script they name.
func (r recipeRuns) ofStep(t *testing.T, step recipeStep) []string {
	t.Helper()
	uses, _, _ := strings.Cut(step.Uses, "@")
	if uses == "Costroid/costroid/action" {
		stage := step.With["stage"]
		if stage == "" {
			t.Errorf("a step runs the action with no stage")
		}
		var runs []string
		for _, inner := range slices.Concat(r.stages[""], r.stages[stage]) {
			runs = append(runs, r.ofStep(t, inner)...)
		}
		return runs
	}
	var runs []string
	for _, listed := range r.manifest.Steps {
		if uses == listed.Uses && (listed.Input == "" || step.With[listed.Input] != "") {
			runs = append(runs, listed.Uses)
		}
	}
	text := step.Run
	for _, script := range actionScript.FindAllStringSubmatch(step.Run, -1) {
		text += "\n" + string(read(t, filepath.Join("../action", script[1])))
	}
	return append(runs, r.inText(text)...)
}

// inText answers each command or script of the manifest a script runs. A
// command written with a flag, such as costroid export --platform-url, runs
// only where the flag is written too.
func (r recipeRuns) inText(text string) []string {
	var runs []string
	for _, maker := range r.makers {
		words, flag, flagged := strings.Cut(maker, " --")
		var found bool
		switch {
		case strings.HasPrefix(maker, "action/"):
			found = strings.Contains(text, "$GITHUB_ACTION_PATH/"+strings.TrimPrefix(maker, "action/"))
		case strings.HasPrefix(maker, "costroid "):
			command := strings.ReplaceAll(regexp.QuoteMeta(strings.TrimPrefix(words, "costroid ")), " ", `\s+`)
			found = regexp.MustCompile(`\bcostroid"?\s+` + command + `\b`).MatchString(text)
		default:
			found = strings.Contains(text, words)
		}
		if found && (!flagged || strings.Contains(text, "--"+flag)) {
			runs = append(runs, maker)
		}
	}
	return runs
}

func sortedUnique(names []string) []string {
	sorted := slices.Sorted(slices.Values(names))
	return slices.Compact(sorted)
}

// recipeJob is one job of a recipe: what it asks of its token and what it
// runs.
type recipeJob struct {
	file, name, host string
	asked            map[string]string
	runs             []string
}

func githubJobs(t *testing.T, runs recipeRuns) []recipeJob {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(recipesDir, "github-*.yml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no GitHub recipes: %v", err)
	}
	var jobs []recipeJob
	for _, file := range files {
		var workflow recipeWorkflow
		if err := yaml.Unmarshal(read(t, file), &workflow); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if workflow.Permissions == nil || len(workflow.Permissions) > 0 {
			t.Errorf("%s grants %v to every job; a recipe grants nothing at the top and each job asks for its own", file, workflow.Permissions)
		}
		for _, name := range slices.Sorted(maps.Keys(workflow.Jobs)) {
			job := workflow.Jobs[name]
			var made []string
			for _, step := range job.Steps {
				made = append(made, runs.ofStep(t, step)...)
			}
			jobs = append(jobs, recipeJob{"recipes/" + filepath.Base(file), name, "github", job.Permissions, sortedUnique(made)})
		}
	}
	return jobs
}

// gitlabJobs reads the template's jobs. A GitLab job asks nothing in the
// file: the person who sets up the template creates its token, so what the
// manifest states for it is what it asks.
func gitlabJobs(t *testing.T, runs recipeRuns) []recipeJob {
	t.Helper()
	var template map[string]struct {
		Script []string `yaml:"script"`
	}
	if err := yaml.Unmarshal(read(t, gitLabTemplate), &template); err != nil {
		t.Fatal(err)
	}
	var jobs []recipeJob
	for _, name := range slices.Sorted(maps.Keys(template)) {
		if len(template[name].Script) == 0 {
			continue
		}
		made := runs.inText(strings.Join(template[name].Script, "\n"))
		jobs = append(jobs, recipeJob{"recipes/gitlab-ci.yml", name, "gitlab", nil, sortedUnique(made)})
	}
	if len(jobs) == 0 {
		t.Fatal("the GitLab template holds no job with a script")
	}
	return jobs
}

// TestEveryRecipeAsksOnlyWhatItsCallsNeed reads every job of every recipe,
// what it asks of its token and what it runs, the action's stages and scripts
// included, and holds it to permissions.json: the manifest states each job's
// grant as the recipe asks it, each permission is needed at the level asked
// by a call or a step the job runs, and each call and step the job runs is
// covered by a permission it asks or one the manifest states every workflow
// token holds. The commands
// each call names are held to the writes report, follow-up and approve made
// in their own recorded walk.
func TestEveryRecipeAsksOnlyWhatItsCallsNeed(t *testing.T) {
	manifest := docstest.Permissions(t, "..")
	runs := newRecipeRuns(t, manifest)
	grants := map[[2]string]docstest.Grant{}
	for _, grant := range manifest.Grants {
		if grant.File != "" {
			grants[[2]string{grant.File, grant.Job}] = grant
		}
	}
	for _, job := range slices.Concat(githubJobs(t, runs), gitlabJobs(t, runs)) {
		key := [2]string{job.file, job.name}
		grant, found := grants[key]
		delete(grants, key)
		if !found {
			unstated := docstest.Grant{Name: job.file + ", job " + job.name, Runs: job.runs}
			if calls, steps := manifest.Under(unstated, job.host); len(job.asked) > 0 || len(calls) > 0 || len(steps) > 0 {
				t.Errorf("%s asks %v and runs %v, and %s states no grant for it", unstated.Name, job.asked, job.runs, docstest.ManifestFile)
			}
			continue
		}
		if !slices.Equal(sortedUnique(grant.Runs), job.runs) {
			t.Errorf("%s runs %v, and %s states %v", grant.Name, job.runs, docstest.ManifestFile, grant.Runs)
		}
		asked, held := job.asked, manifest.HeldBy(workflowToken)
		if job.host == "gitlab" {
			asked, held = map[string]string{}, nil
			for name, granted := range grant.Permissions {
				asked[name] = granted.Level
			}
		}
		for _, problem := range manifest.Problems(grant, job.host, asked, held) {
			t.Error(problem)
		}
	}
	for key := range grants {
		t.Errorf("%s states a grant for %s, job %s, which no recipe has", docstest.ManifestFile, key[0], key[1])
	}
	holdRecordedWrites(t, manifest)
}

// holdRecordedWrites holds the commands each call names to the writes the
// golden record of report, follow-up and approve holds.
func holdRecordedWrites(t *testing.T, manifest docstest.Manifest) {
	t.Helper()
	flow := regexp.MustCompile(`^== ([a-z-]+): `)
	command, inWrites, held := "", false, 0
	scanner := bufio.NewScanner(bytes.NewReader(read(t, checkWrites)))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case flow.MatchString(line):
			command, inWrites = "costroid "+flow.FindStringSubmatch(line)[1], false
		case line == "-- writes":
			inWrites = true
		case strings.HasPrefix(line, "-- "):
			inWrites = false
		case inWrites:
			method, path, _ := strings.Cut(line, " ")
			index := slices.IndexFunc(manifest.Calls, func(call docstest.Call) bool {
				return call.Host == "github" && call.Base == "api" && call.Matches(method, path)
			})
			if index < 0 || !slices.Contains(manifest.Calls[index].Commands, command) {
				t.Errorf("%s recorded %s, and %s lists no call it names for it", command, line, docstest.ManifestFile)
			}
			held++
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if held == 0 {
		t.Fatalf("%s holds no write", checkWrites)
	}
}
