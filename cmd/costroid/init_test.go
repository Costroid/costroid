package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	costroid "github.com/Costroid/costroid"
	"github.com/Costroid/costroid/internal/config"
	"github.com/Costroid/costroid/internal/recipelint"
	"go.yaml.in/yaml/v3"
)

const (
	testCommit  = "0123456789abcdef0123456789abcdef01234567"
	testRelease = "1.0.0-alpha.10"
	otherCommit = "fedcba9876543210fedcba9876543210fedcba98"
	workflowDir = ".github/workflows"
)

var releaseBuild = buildInfo{version: testRelease, commit: testCommit}

// testPR holds --pr to no terminal and to runs that show at once.
var testPR = prSettings{getenv: os.Getenv, terminal: filepath.Join(os.TempDir(), "costroid-no-terminal"), polls: 3}

// existingReview is a team's own review workflow, with no Costroid in it.
const existingReview = `# Claude reviews a pull request.
name: review

on:
  pull_request:
    types: [opened, synchronize]

permissions: {}

jobs:
  review:
    runs-on: ubuntu-24.04
    permissions:
      contents: read
      pull-requests: write
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: anthropics/claude-code-action@1d6de8cb0c237e7c15e9e1bdf973826ebae490cc # v1.0.248
        with:
          anthropic_api_key: ${{ secrets.ANTHROPIC_API_KEY }}
          github_token: ${{ github.token }}
          prompt: |
            Review this pull request.

            List the bugs.
`

// existingReviewFourSpaces is the same workflow indented by four spaces, with
// its steps' dashes at the key above them.
const existingReviewFourSpaces = `name: review

on:
    pull_request:

permissions: {}

jobs:
    review:
        runs-on: ubuntu-24.04
        permissions:
            contents: read
        steps:
        -   uses: anthropics/claude-code-action@1d6de8cb0c237e7c15e9e1bdf973826ebae490cc # v1.0.248
            with:
                anthropic_api_key: ${{ secrets.ANTHROPIC_API_KEY }}
                prompt: Review this pull request.
        -   run: echo reviewed
`

func runInit(t *testing.T, root string, build buildInfo, recipes fs.FS, args ...string) (int, string, string) {
	t.Helper()
	return runInitIn(t, repository{root: root, dir: root}, build, recipes, args...)
}

// runInitIn runs init as if started in repo.dir, inside repo.root.
func runInitIn(t *testing.T, repo repository, build buildInfo, recipes fs.FS, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := initRepository(func() (repository, error) { return repo, nil }, build, recipes, testPR, args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func writeFile(t *testing.T, name, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// tree maps every file under root to its content, outside .git.
func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err == nil && entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if err != nil || entry.IsDir() {
			return err
		}
		files[name] = readFile(t, name)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func shippedRecipe(t *testing.T, name string) []byte {
	t.Helper()
	data, err := fs.ReadFile(costroid.Recipes, path.Join("recipes", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// everyTarget calls test once for each host and agent init writes.
func everyTarget(t *testing.T, test func(t *testing.T, host, agent, recipe string)) {
	for host, agents := range initRecipes {
		for agent, recipe := range agents {
			t.Run(host+"/"+agent, func(t *testing.T) { test(t, host, agent, recipe) })
		}
	}
}

// pinLine matches each line init may change in a shipped recipe.
var pinLine = regexp.MustCompile(regexp.QuoteMeta(costroidActionPath) + `@[0-9a-f]{40}|^ +COSTROID_(VERSION|COMMIT|AGENT): `)

func TestInitWritesLintCleanRecipe(t *testing.T) {
	everyTarget(t, func(t *testing.T, host, agent, recipe string) {
		root := t.TempDir()
		ran := filepath.Join(t.TempDir(), "ran")
		bin := t.TempDir()
		for _, tool := range []string{"gh", "git", "glab"} {
			writeFile(t, filepath.Join(bin, tool), "#!/bin/sh\necho \"$0 $*\" >>"+ran+"\n")
			if err := os.Chmod(filepath.Join(bin, tool), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("PATH", bin)

		code, stdout, stderr := runInit(t, root, releaseBuild, costroid.Recipes, host, "--agent", agent)
		if code != 0 {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		written := readFile(t, filepath.Join(root, initOut[host]))
		report := recipelint.LintBytes(initOut[host], []byte(written))
		if len(report.Findings) > 0 {
			t.Errorf("lint-recipes rejects what init wrote: %v", report.Findings)
		}
		for _, note := range report.Notes {
			if note.Rule == "unreleased" {
				t.Errorf("init wrote an unreleased pin: %s", note)
			}
		}
		_, shippedBody := recipeHeader(shippedRecipe(t, recipe))
		_, writtenBody := recipeHeader([]byte(written))
		shipped := strings.Split(string(shippedBody), "\n")
		got := strings.Split(string(writtenBody), "\n")
		if len(got) != len(shipped) {
			t.Fatalf("init wrote %d lines under the header, recipes/%s has %d", len(got), recipe, len(shipped))
		}
		for i := range got {
			if got[i] != shipped[i] && !pinLine.MatchString(shipped[i]) {
				t.Errorf("line %d under the header: init wrote %q where recipes/%s has %q", i+1, got[i], recipe, shipped[i])
			}
		}

		settings := readFile(t, filepath.Join(root, configFile))
		if problems, err := config.Validate([]byte(settings)); err != nil || len(problems) > 0 {
			t.Errorf("costroid.yml: %v %v", problems, err)
		}

		flat := strings.Join(strings.Fields(stdout), " ")
		sentence := perPushSentence(shippedRecipe(t, recipe))
		if sentence == "" || !strings.Contains(flat, sentence) {
			t.Errorf("stdout lacks the per-push sentence %q:\n%s", sentence, stdout)
		}
		wantKeys := secretNames([]byte(written))
		if host == gitlabHost {
			wantKeys = []string{gitlabKeys[agent]}
		} else if len(wantKeys) != 1 {
			t.Errorf("recipes/%s reads the secrets %v", recipe, wantKeys)
		}
		for _, key := range wantKeys {
			if !strings.Contains(stdout, key) {
				t.Errorf("stdout does not name the secret %s:\n%s", key, stdout)
			}
		}
		if _, err := os.Stat(ran); err == nil {
			t.Errorf("init ran a command: %s", readFile(t, ran))
		}
	})
}

// TestInitRefusesARecipeThatBreaksARule gives init a recipe the linter
// rejects. It exits 1 and writes nothing.
func TestInitRefusesARecipeThatBreaksARule(t *testing.T) {
	broken := strings.Replace(string(shippedRecipe(t, "github-claude-code.yml")), "  pull_request:\n", "  pull_request_target:\n", 1)
	recipes := fstest.MapFS{"recipes/github-claude-code.yml": {Data: []byte(broken)}}
	root := t.TempDir()
	code, _, stderr := runInit(t, root, releaseBuild, recipes, "github", "--agent", "claude")
	if code != exitRejected || !strings.Contains(stderr, recipelint.RulePullRequestTarget) || !strings.Contains(stderr, "wrote nothing") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if files := tree(t, root); len(files) != 0 {
		t.Errorf("init wrote %v", files)
	}
}

// TestInitConfigNamesEverySchemaKey holds the costroid.yml init writes to the
// schema: every key this release reads, at its depth, commented out.
func TestInitConfigNamesEverySchemaKey(t *testing.T) {
	settings, err := commentedConfig()
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(costroid.ConfigSchema, &schema); err != nil {
		t.Fatal(err)
	}
	count := 0
	var walk func(node map[string]any, depth int)
	walk = func(node map[string]any, depth int) {
		properties, _ := node["properties"].(map[string]any)
		for name, child := range properties {
			if depth == 0 && slices.Contains(reservedKeys, name) {
				continue
			}
			count++
			line := "\n# " + strings.Repeat("  ", depth) + name + ":"
			if !strings.Contains(string(settings), line) {
				t.Errorf("costroid.yml lacks %q", line)
			}
			walk(child.(map[string]any), depth+1)
		}
	}
	walk(schema, 0)
	if count < 10 {
		t.Fatalf("read %d keys from the schema", count)
	}
	for line := range strings.SplitSeq(strings.TrimSuffix(string(settings), "\n"), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			t.Errorf("an uncommented line: %q", line)
		}
		if len(line) > usageWidth && !strings.HasPrefix(line, "# yaml-language-server: ") {
			t.Errorf("a line of %d bytes: %q", len(line), line)
		}
	}
	if !strings.HasPrefix(string(settings), "# yaml-language-server: $schema="+schema["$id"].(string)+"\n") {
		t.Errorf("costroid.yml does not open with the schema line:\n%s", settings)
	}
}

func TestInitRefusesToOverwrite(t *testing.T) {
	root := t.TempDir()
	recipe := filepath.Join(root, initOut[githubHost])
	settings := filepath.Join(root, configFile)
	writeFile(t, recipe, "name: mine\n")
	writeFile(t, settings, "policy:\n  run_budget_usd: 5\n")

	code, stdout, stderr := runInit(t, root, releaseBuild, costroid.Recipes, "github", "--agent", "claude")
	if code != exitFailure || !strings.Contains(stderr, "exists: pass --force") || stdout != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if readFile(t, recipe) != "name: mine\n" {
		t.Fatal("init replaced the workflow without --force")
	}

	code, stdout, stderr = runInit(t, root, releaseBuild, costroid.Recipes, "github", "--agent", "claude", "--force")
	if code != 0 {
		t.Fatalf("--force: exit %d, stderr %q", code, stderr)
	}
	if readFile(t, recipe) == "name: mine\n" {
		t.Error("--force left the workflow as it was")
	}
	if readFile(t, settings) != "policy:\n  run_budget_usd: 5\n" {
		t.Error("init replaced costroid.yml")
	}
	if !strings.Contains(stdout, "costroid.yml exists, and init left it as it is") || strings.Contains(stdout, "git add "+initOut[githubHost]+" costroid.yml") {
		t.Errorf("stdout:\n%s", stdout)
	}

	custom := filepath.Join(root, "ci", "costroid.yml")
	writeFile(t, custom, "mine\n")
	code, _, stderr = runInit(t, root, releaseBuild, costroid.Recipes, "gitlab", "--agent", "codex", "--out", "ci/costroid.yml")
	if code != exitFailure || readFile(t, custom) != "mine\n" {
		t.Errorf("--out over a file: exit %d, stderr %q", code, stderr)
	}
}

func TestInitRefusesDevWithoutRef(t *testing.T) {
	refusals := []struct {
		name  string
		build buildInfo
		args  []string
		want  string
	}{
		{"dev", buildInfo{version: "dev"}, []string{"github", "--agent", "claude"}, "development build, version dev, which belongs to no release, so init pins nothing: run a released costroid, or name the commit to pin with --action-ref <40 hex>"},
		{"dev with a commit", buildInfo{version: "dev", commit: testCommit}, []string{"github", "--agent", "codex-action"}, "development build"},
		{"release with no commit", buildInfo{version: testRelease}, []string{"github", "--agent", "claude"}, "this build of " + testRelease + " records no commit"},
		{"gitlab dev", buildInfo{version: "dev"}, []string{"gitlab", "--agent", "claude"}, "pins nothing: run a released costroid\n"},
		{"gitlab ref", buildInfo{version: "dev"}, []string{"gitlab", "--agent", "claude", "--action-ref", otherCommit}, "the GitLab template installs a release by its version"},
		{"short ref", buildInfo{version: "dev"}, []string{"github", "--agent", "claude", "--action-ref", "0123456"}, "--action-ref must be a full commit SHA"},
		{"upper-case ref", buildInfo{version: "dev"}, []string{"github", "--agent", "claude", "--action-ref", strings.ToUpper(otherCommit)}, "--action-ref must be a full commit SHA"},
		{"zero ref", buildInfo{version: "dev"}, []string{"github", "--agent", "claude", "--action-ref", strings.Repeat("0", 40)}, "--action-ref must be a full commit SHA"},
		{"existing dev", buildInfo{version: "dev"}, []string{"github", "--agent", "claude-code-action", "--existing", "review.yml"}, "development build"},
	}
	for _, refusal := range refusals {
		t.Run(refusal.name, func(t *testing.T) {
			root := t.TempDir()
			code, stdout, stderr := runInit(t, root, refusal.build, costroid.Recipes, refusal.args...)
			if code != exitFailure || stdout != "" || !strings.Contains(stderr, refusal.want) {
				t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
			}
			if files := tree(t, root); len(files) != 0 {
				t.Errorf("init wrote %v", files)
			}
		})
	}

	t.Run("this test binary", func(t *testing.T) {
		root := gitRepository(t)
		t.Chdir(root)
		var stdout, stderr bytes.Buffer
		if code := initCommand([]string{"github", "--agent", "claude"}, &stdout, &stderr); code != exitFailure || !strings.Contains(stderr.String(), "development build") {
			t.Errorf("exit %d, stderr %q", code, stderr.String())
		}
		if files := tree(t, root); len(files) != 0 {
			t.Errorf("init wrote %v", files)
		}
	})

	t.Run("dev with a ref", func(t *testing.T) {
		root := t.TempDir()
		code, stdout, stderr := runInit(t, root, buildInfo{version: "dev"}, costroid.Recipes, "github", "--agent", "claude", "--action-ref", otherCommit)
		if code != 0 {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		written := readFile(t, filepath.Join(root, initOut[githubHost]))
		pins := actionPin.FindAllString(written, -1)
		if len(pins) == 0 {
			t.Fatal("no pin written")
		}
		for _, got := range pins {
			if got != costroidActionPath+"@"+otherCommit+" # set by costroid init --action-ref" {
				t.Errorf("pin %q", got)
			}
		}
		if !strings.Contains(stdout, "pinned to "+otherCommit+", which --action-ref named") {
			t.Errorf("stdout:\n%s", stdout)
		}
	})
}

func TestInitPinsTheBinarysOwnRelease(t *testing.T) {
	for _, version := range []string{testRelease, "v" + testRelease} {
		build := buildInfo{version: version, commit: testCommit}
		everyTarget(t, func(t *testing.T, host, agent, recipe string) {
			root := t.TempDir()
			code, stdout, stderr := runInit(t, root, build, costroid.Recipes, host, "--agent", agent)
			if code != 0 {
				t.Fatalf("exit %d, stderr %q", code, stderr)
			}
			if !strings.Contains(stdout, "pinned to v"+testRelease+" at "+testCommit) {
				t.Errorf("stdout does not name the release:\n%s", stdout)
			}
			written := readFile(t, filepath.Join(root, initOut[host]))
			if host == gitlabHost {
				for _, line := range []string{"  COSTROID_VERSION: " + testRelease + "\n", "  COSTROID_COMMIT: " + testCommit + "\n", "  COSTROID_AGENT: " + agent + "\n"} {
					if strings.Count(written, line) != 1 {
						t.Errorf("the template does not set %q once", line)
					}
				}
				return
			}
			want := len(actionPin.FindAll(shippedRecipe(t, recipe), -1))
			pins := regexp.MustCompile(regexp.QuoteMeta(costroidActionPath)+`@\S+(?: #[^\n]*)?`).FindAllString(written, -1)
			if want == 0 || len(pins) != want {
				t.Fatalf("init wrote %d pins, recipes/%s has %d", len(pins), recipe, want)
			}
			for _, got := range pins {
				if got != costroidActionPath+"@"+testCommit+" # v"+testRelease {
					t.Errorf("pin %q", got)
				}
			}
		})
	}
}

// TestTheBuildsStampTheCommit holds both builds that name a release to
// stamping main.commit beside main.version, so init can pin the release.
func TestTheBuildsStampTheCommit(t *testing.T) {
	stamps := regexp.MustCompile(`-ldflags "[^"]*-X main\.version=\$\(?VERSION\)? -X main\.commit=(\S+)"`)
	for file, commitValue := range map[string]string{"../../Makefile": "$(COMMIT)", "../../.github/workflows/release.yml": "$GITHUB_SHA"} {
		match := stamps.FindStringSubmatch(readFile(t, file))
		if match == nil || match[1] != commitValue {
			t.Errorf("%s does not stamp main.commit with %s: %v", file, commitValue, match)
		}
	}
	if !regexp.MustCompile(`(?m)^COMMIT \?= \$\(shell git rev-parse HEAD`).MatchString(readFile(t, "../../Makefile")) {
		t.Error("the Makefile does not default COMMIT to the checkout's commit")
	}
}

// applyPatch applies a patch with git apply to a copy of one file and returns
// the result.
func applyPatch(t *testing.T, name, original, patch string) string {
	t.Helper()
	directory := t.TempDir()
	writeFile(t, filepath.Join(directory, name), original)
	command := exec.Command("git", "apply", "--verbose", "-")
	command.Dir = directory
	command.Stdin = strings.NewReader(patch)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git apply: %v\n%s\npatch:\n%s", err, output, patch)
	}
	return readFile(t, filepath.Join(directory, name))
}

func existingPatch(t *testing.T, workflow string, args ...string) (string, string, map[string]string) {
	t.Helper()
	root := t.TempDir()
	name := filepath.Join(workflowDir, "review.yml")
	writeFile(t, filepath.Join(root, name), workflow)
	before := tree(t, root)
	all := append([]string{"github", "--agent", "claude-code-action", "--existing", name}, args...)
	code, stdout, stderr := runInit(t, root, releaseBuild, costroid.Recipes, all...)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if after := tree(t, root); !reflect.DeepEqual(before, after) {
		t.Fatalf("--existing changed the repository: %v", after)
	}
	return stdout, stderr, before
}

func decodeYAML(t *testing.T, text string) map[string]any {
	t.Helper()
	var document map[string]any
	if err := yaml.Unmarshal([]byte(text), &document); err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	return document
}

func TestInitExistingPrintsAPatchAndWritesNothing(t *testing.T) {
	name := workflowDir + "/review.yml"
	pinned := costroidActionPath + "@" + testCommit
	cases := []struct {
		label, workflow, stepID string
	}{
		{"two spaces", existingReview, "claude"},
		{"no final newline", strings.TrimSuffix(existingReview, "|\n            Review this pull request.\n\n            List the bugs.\n") + "Review this pull request.", "claude"},
		{"four spaces", existingReviewFourSpaces, "claude"},
		{"its own id", strings.Replace(existingReview, "      - uses: anthropics", "      - id: review-step\n        uses: anthropics", 1), "review-step"},
		{"a job after", existingReview + "\n  other:\n    runs-on: ubuntu-24.04\n    steps:\n      - run: echo done\n", "claude"},
		{"a key after the jobs", existingReview + "\nenv:\n  A: b\n", "claude"},
	}
	for _, test := range cases {
		t.Run(test.label, func(t *testing.T) {
			patch, stderr, _ := existingPatch(t, test.workflow)
			if !strings.HasPrefix(patch, "--- a/"+name+"\n+++ b/"+name+"\n@@ ") {
				t.Fatalf("not a patch of %s:\n%s", name, patch)
			}
			if flat := strings.Join(strings.Fields(stderr), " "); !strings.Contains(flat, "and wrote nothing") || !strings.Contains(flat, "Your job keeps the permissions it has") {
				t.Errorf("stderr %q", stderr)
			}
			patched := applyPatch(t, name, test.workflow, patch)
			report := recipelint.LintCustomer(name, []byte(patched))
			if report.Agents != 1 || len(report.Findings) > 0 {
				t.Errorf("costroid lint on the patched workflow: %d agents, %v", report.Agents, report.Findings)
			}
			if strings.Count(patched, pinned+" # v"+testRelease+"\n") != 2 {
				t.Errorf("the patch does not pin both blocks to the release:\n%s", patch)
			}
			document := decodeYAML(t, patched)
			original := decodeYAML(t, test.workflow)
			jobs := document["jobs"].(map[string]any)
			review := jobs["review"].(map[string]any)
			steps := review["steps"].([]any)
			at := slices.IndexFunc(steps, func(step any) bool {
				uses, _ := step.(map[string]any)["uses"].(string)
				return strings.HasPrefix(uses, claudeCodeAction+"@")
			})
			if at < 0 || at+1 >= len(steps) {
				t.Fatalf("the review job's steps: %v", steps)
			}
			claude := steps[at].(map[string]any)
			read := steps[at+1].(map[string]any)
			if claude["id"] != test.stepID || read["uses"] != pinned || read["with"].(map[string]any)["stage"] != "read" {
				t.Errorf("the review job's steps: %v", steps)
			}
			if read["if"] != "always() && steps."+test.stepID+".outputs.execution_file != ''" {
				t.Errorf("the read step's if: %v", read["if"])
			}
			reportJob := jobs["report"].(map[string]any)
			reportStep := reportJob["steps"].([]any)[0].(map[string]any)
			if reportJob["needs"] != "review" || reportStep["uses"] != pinned {
				t.Errorf("the report job: %v", reportJob)
			}
			claudeWith := claude["with"].(map[string]any)
			readWith := read["with"].(map[string]any)
			if claudeWith["claude_args"] != "--max-budget-usd 5" || readWith["budget-usd"] != "5" || readWith["claude-args"] != "--max-budget-usd 5" || reportStep["with"].(map[string]any)["budget-usd"] != "5" {
				t.Errorf("the patch does not hold the run to a cap of USD 5:\n%s", patched)
			}
			if test.stepID == addedStepID {
				delete(claude, "id")
			}
			delete(claudeWith, "claude_args")
			review["steps"] = slices.Delete(steps, at+1, at+2)
			delete(jobs, "report")
			if !reflect.DeepEqual(document, original) {
				t.Errorf("the patch changed more than the two blocks:\n%s", patched)
			}
		})
	}

	t.Run("already reports", func(t *testing.T) {
		workflow := existingReview + "      - uses: " + costroidActionPath + "@" + otherCommit + " # v1.0.0-alpha.9\n"
		patch, stderr, _ := existingPatch(t, workflow)
		if patch != "" || !strings.Contains(stderr, "already calls") {
			t.Errorf("stdout %q, stderr %q", patch, stderr)
		}
	})

	for _, refusal := range []struct{ label, workflow, want string }{
		{"no claude step", strings.Replace(existingReview, "anthropics/claude-code-action", "actions/other", 1), "in 0 steps"},
		{"two claude steps", existingReview + "      - uses: anthropics/claude-code-action@1d6de8cb0c237e7c15e9e1bdf973826ebae490cc # v1.0.248\n", "in 2 steps"},
		{"not YAML", "jobs: [\n", "not valid YAML"},
	} {
		t.Run(refusal.label, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "review.yml"), refusal.workflow)
			before := tree(t, root)
			code, stdout, stderr := runInit(t, root, releaseBuild, costroid.Recipes, "github", "--agent", "claude-code-action", "--existing", "review.yml")
			if code != exitFailure || stdout != "" || !strings.Contains(stderr, refusal.want) {
				t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
			}
			if !reflect.DeepEqual(before, tree(t, root)) {
				t.Error("init changed the repository")
			}
		})
	}
}

// TestInitExistingMatchesTheDocs takes docs/existing-workflow.md's example
// without its two blocks and the step's id, patches it, and gets the example
// back with the page's cap lines added as its diff shows them.
func TestInitExistingMatchesTheDocs(t *testing.T) {
	page := readFile(t, "../../docs/existing-workflow.md")
	blocks := regexp.MustCompile("(?s)```yaml\n(.*?)```").FindAllStringSubmatch(page, -1)
	if len(blocks) != 1 {
		t.Fatalf("the page holds %d YAML blocks", len(blocks))
	}
	example := blocks[0][1]
	cut := strings.Index(example, "\n\n      # Block 1")
	if cut < 0 || strings.Count(example, "        id: claude\n") != 1 {
		t.Fatal("the example's layout moved: update this test")
	}
	before := strings.Replace(example[:cut+1], "        id: claude\n", "", 1)
	docsPin := regexp.MustCompile(regexp.QuoteMeta(costroidActionPath) + `@([0-9a-f]{40}) # v(\S+)`).FindStringSubmatch(example)
	if docsPin == nil {
		t.Fatal("the example pins no release")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "review.yml"), before)
	code, patch, stderr := runInit(t, root, buildInfo{version: docsPin[2], commit: docsPin[1]}, costroid.Recipes, "github", "--agent", "claude-code-action", "--existing", "review.yml")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	patched := applyPatch(t, "review.yml", before, patch)
	capped := addDiffLines(t, example, page, "## Hold each run to a cap")
	if !reflect.DeepEqual(decodeYAML(t, patched), decodeYAML(t, capped)) {
		t.Errorf("the patched workflow differs from the page's example with its cap lines:\n%s\nwant:\n%s", patched, capped)
	}
}

// addDiffLines adds to text the + lines of the first diff block under the
// page's heading, each after the context line before it. A context line of
// ... skips lines.
func addDiffLines(t *testing.T, text, page, heading string) string {
	t.Helper()
	_, section, found := strings.Cut(page, heading+"\n")
	diff := regexp.MustCompile("(?s)```diff\n(.*?)```").FindStringSubmatch(section)
	if !found || diff == nil {
		t.Fatalf("the page has no diff under %s", heading)
	}
	lines := strings.Split(text, "\n")
	at, added := 0, 0
	for line := range strings.SplitSeq(strings.TrimSuffix(diff[1], "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "+"):
			lines = slices.Insert(lines, at, line[1:])
			at++
			added++
		case strings.TrimSpace(line) == "...":
		default:
			next := slices.Index(lines[at:], strings.TrimPrefix(line, " "))
			if next < 0 {
				t.Fatalf("the diff's context line %q is not in the example after line %d", line, at)
			}
			at += next + 1
		}
	}
	if added == 0 {
		t.Fatalf("the diff under %s adds no line", heading)
	}
	return strings.Join(lines, "\n")
}

func TestInitRefusesWhatItCannotWrite(t *testing.T) {
	for _, refusal := range []struct {
		args []string
		want string
	}{
		{nil, "name the host"},
		{[]string{"bitbucket", "--agent", "claude"}, `not "bitbucket"`},
		{[]string{"github"}, "--agent is one of claude, claude-code-action, codex, codex-action"},
		{[]string{"gitlab", "--agent", "claude-code-action"}, "--agent on gitlab is one of claude, codex"},
		{[]string{"github", "--agent", "claude", "--existing", "x.yml"}, "--existing reads a GitHub workflow that runs claude-code-action"},
		{[]string{"github", "--agent", "claude-code-action", "--existing", "x.yml", "--force"}, "--existing writes nothing"},
		{[]string{"github", "gitlab", "--agent", "claude"}, "init takes one host"},
		{[]string{"github", "--agent", "claude", "--budget", "5"}, "flag provided but not defined"},
	} {
		root := t.TempDir()
		code, stdout, stderr := runInit(t, root, releaseBuild, costroid.Recipes, refusal.args...)
		if code != exitFailure || stdout != "" || !strings.Contains(stderr, refusal.want) {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", refusal.args, code, stdout, stderr)
		}
	}
	code, _, stderr := runInit(t, t.TempDir(), releaseBuild, costroid.Recipes, "--agent", "codex", "github")
	if code != 0 {
		t.Errorf("flags before the host: exit %d, stderr %q", code, stderr)
	}
	code, _, stderr = runInit(t, t.TempDir(), releaseBuild, costroid.Recipes, "github", "--help")
	if code != 0 || stderr != initHelp {
		t.Errorf("--help after the host: exit %d, stderr %q", code, stderr)
	}
}

// TestInstallPageInitExamplesRun runs every costroid init line of
// docs/install.md, as a released binary would, in an empty repository.
func TestInstallPageInitExamplesRun(t *testing.T) {
	page := readFile(t, "../../docs/install.md")
	ran := 0
	for _, block := range regexp.MustCompile("(?s)```sh\n(.*?)```").FindAllStringSubmatch(page, -1) {
		for line := range strings.SplitSeq(block[1], "\n") {
			words := strings.Fields(line)
			if len(words) < 2 || words[0] != "costroid" || words[1] != "init" {
				continue
			}
			if redirect := slices.Index(words, ">"); redirect >= 0 {
				words = words[:redirect]
			}
			args := words[2:]
			root := t.TempDir()
			if at := slices.Index(args, "--existing"); at >= 0 && at+1 < len(args) {
				writeFile(t, filepath.Join(root, args[at+1]), existingReview)
			}
			if code, _, stderr := runInit(t, root, releaseBuild, costroid.Recipes, args...); code != 0 {
				t.Errorf("%s: exit %d, stderr %q", line, code, stderr)
			}
			ran++
		}
	}
	if ran != 3 {
		t.Errorf("ran %d examples, want 3", ran)
	}
}

// gitRepository makes a git repository with one empty commit, with git
// reading no user or system settings, and returns its root as git names it.
func gitRepository(t *testing.T) string {
	t.Helper()
	for name, value := range map[string]string{
		"HOME": t.TempDir(), "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.DevNull,
		"GIT_AUTHOR_NAME": "Test", "GIT_AUTHOR_EMAIL": "test@example.com",
		"GIT_COMMITTER_NAME": "Test", "GIT_COMMITTER_EMAIL": "test@example.com",
	} {
		t.Setenv(name, value)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "--quiet", "--initial-branch=main")
	runGit(t, root, "commit", "--quiet", "--allow-empty", "--message", "Start")
	return root
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	output, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

// printedCommands are the commands init lists as left, in order.
func printedCommands(t *testing.T, stdout string) []string {
	t.Helper()
	_, list, found := strings.Cut(stdout, "These commands are left, and init ran none of them:\n")
	if !found {
		t.Fatalf("init printed no commands:\n%s", stdout)
	}
	list, _, _ = strings.Cut(list, "\n\n")
	var commands []string
	for line := range strings.SplitSeq(strings.TrimSuffix(list, "\n"), "\n") {
		commands = append(commands, strings.TrimSpace(line))
	}
	return commands
}

func TestInitWritesAtTheRepositoryRoot(t *testing.T) {
	root := gitRepository(t)
	runGit(t, root, "remote", "add", "origin", "git@github.com:example/widgets.git")
	sub := filepath.Join(root, "services", "api")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	start := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := initRepository(locateRepository, releaseBuild, costroid.Recipes, testPR, args, &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}

	code, stdout, stderr := start("--agent", "codex")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	want := []string{filepath.Join(root, ".github", "workflows", "costroid.yml"), filepath.Join(root, configFile)}
	if got := slices.Sorted(maps.Keys(tree(t, root))); !slices.Equal(got, want) {
		t.Fatalf("init wrote %v, want %v", got, want)
	}
	if !strings.Contains(stdout, "costroid: wrote .github/workflows/costroid.yml from recipes/github-codex.yml") || strings.Contains(stdout, root) {
		t.Errorf("stdout does not name the workflow from the root:\n%s", stdout)
	}
	commands := printedCommands(t, stdout)
	if commands[0] != "cd ../.." || !slices.Contains(commands, "git add .github/workflows/costroid.yml costroid.yml") {
		t.Errorf("the commands do not run from the root: %q", commands)
	}

	code, stdout, stderr = start("--agent", "codex", "--out", "ci.yml")
	if code != 0 || !strings.Contains(stdout, "costroid: wrote services/api/ci.yml from ") {
		t.Errorf("--out beside the shell: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(sub, "ci.yml")); err != nil {
		t.Errorf("--out ci.yml is not in the directory init ran in: %v", err)
	}

	before := tree(t, root)
	outside := filepath.Join(t.TempDir(), "costroid.yml")
	for _, out := range []string{"../../../elsewhere.yml", outside, "../.."} {
		code, stdout, stderr = start("--agent", "codex", "--out", out, "--force")
		if code != exitFailure || stdout != "" || !strings.Contains(stderr, "is outside the repository") {
			t.Errorf("--out %s: exit %d, stdout %q, stderr %q", out, code, stdout, stderr)
		}
	}
	if !reflect.DeepEqual(before, tree(t, root)) {
		t.Error("init wrote a file after refusing --out")
	}
	if _, err := os.Stat(outside); err == nil {
		t.Errorf("init wrote %s, outside the repository", outside)
	}
}

func TestInitRefusesOutsideARepository(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	t.Chdir(dir)
	for _, args := range [][]string{{"github", "--agent", "claude"}, {"--agent", "claude"}, {"gitlab", "--agent", "codex"}} {
		var stdout, stderr bytes.Buffer
		code := initRepository(locateRepository, releaseBuild, costroid.Recipes, testPR, args, &stdout, &stderr)
		if code != exitNoRepository || stdout.Len() != 0 || stderr.String() != "costroid: init: "+errNoRepository.Error()+"\n" {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args, code, stdout.String(), stderr.String())
		}
	}
	if strings.Count(errNoRepository.Error(), ". ") != 0 {
		t.Errorf("the refusal is more than one sentence: %q", errNoRepository)
	}
	if files := tree(t, dir); len(files) != 0 {
		t.Errorf("init wrote %v", files)
	}
	var stdout, stderr bytes.Buffer
	if code := initRepository(locateRepository, releaseBuild, costroid.Recipes, testPR, []string{"--help"}, &stdout, &stderr); code != 0 || stderr.String() != initHelp {
		t.Errorf("--help outside a repository: exit %d, stderr %q", code, stderr.String())
	}
}

func TestInitReadsTheHostFromTheRemote(t *testing.T) {
	for _, test := range []struct {
		remotes [][2]string
		args    []string
		want    string
	}{
		{[][2]string{{"origin", "https://github.com/example/widgets.git"}}, nil, githubHost},
		{[][2]string{{"origin", "git@gitlab.com:example/widgets.git"}}, nil, gitlabHost},
		{[][2]string{{"origin", "ssh://git@gitlab.example.com:2222/widgets.git"}}, nil, gitlabHost},
		{[][2]string{{"upstream", "git@github.example.com:widgets.git"}}, nil, githubHost},
		{[][2]string{{"fork", "https://gitlab.com/a/b"}, {"origin", "https://github.com/a/b"}}, nil, githubHost},
		{[][2]string{{"origin", "https://github.com/example/widgets.git"}}, []string{"--host", "gitlab"}, gitlabHost},
		{[][2]string{{"origin", "https://github.com/example/widgets.git"}}, []string{"gitlab"}, gitlabHost},
		{nil, nil, "the repository has no remote"},
		{[][2]string{{"origin", "https://example.com/widgets.git"}}, nil, "the remote origin names neither"},
		{[][2]string{{"a", "https://github.com/a/b"}, {"b", "https://github.com/c/d"}}, nil, "2 remotes and none is origin"},
		{[][2]string{{"origin", "https://github.com/a/b"}}, []string{"github", "--host", "gitlab"}, "the host is github or --host gitlab, not both"},
	} {
		t.Run(test.want+strings.Join(test.args, " "), func(t *testing.T) {
			root := gitRepository(t)
			for _, remote := range test.remotes {
				runGit(t, root, "remote", "add", remote[0], remote[1])
			}
			code, stdout, stderr := runInit(t, root, releaseBuild, costroid.Recipes, append(test.args, "--agent", "codex")...)
			if test.want != githubHost && test.want != gitlabHost {
				if code != exitFailure || !strings.Contains(stderr, test.want) {
					t.Errorf("exit %d, stderr %q", code, stderr)
				}
				return
			}
			if code != 0 {
				t.Fatalf("exit %d, stderr %q", code, stderr)
			}
			if !strings.Contains(stdout, "costroid: wrote "+initOut[test.want]+" ") {
				t.Errorf("init did not write for %s:\n%s", test.want, stdout)
			}
		})
	}
}

// existingCodexReview is a team's own codex-action review workflow.
const existingCodexReview = `name: review

on:
  pull_request:

permissions: {}

jobs:
  review:
    runs-on: ubuntu-24.04
    permissions:
      contents: read
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: openai/codex-action@bdf19a4a223ec2549a3e2274a0cf61556bc07675 # v1.13
        with:
          openai-api-key: ${{ secrets.OPENAI_API_KEY }}
          prompt: Review this pull request.
          codex-home: ${{ runner.temp }}/codex-home
          allow-users: ""
`

// mentionWorkflow is a claude-code-action workflow that a mention in a
// comment or an issue starts as well as a pull request, signed in with a
// Claude subscription's token.
const mentionWorkflow = `name: Claude Code

on:
  issue_comment:
    types: [created]
  issues:
    types: [opened]
  pull_request:
    types: [opened, synchronize]

jobs:
  claude:
    runs-on: ubuntu-24.04
    permissions:
      contents: read
      pull-requests: read
      issues: read
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: anthropics/claude-code-action@1d6de8cb0c237e7c15e9e1bdf973826ebae490cc # v1.0.248
        with:
          claude_code_oauth_token: ${{ secrets.CLAUDE_CODE_OAUTH_TOKEN }}
          github_token: ${{ github.token }}
`

// lintRules lists the rules costroid lint finds broken in a workflow.
func lintRules(name, workflow string) []string {
	var rules []string
	for _, finding := range recipelint.LintCustomer(name, []byte(workflow)).Findings {
		rules = append(rules, finding.Rule)
	}
	slices.Sort(rules)
	return rules
}

func TestInitSwitchesToExistingForAnAgentWorkflow(t *testing.T) {
	for _, test := range []struct {
		label, file, workflow, action, stepID, readKey, readValue string
	}{
		{"claude-code-action", "claude.yml", existingReview, claudeCodeAction, "claude", "execution-file", "${{ steps.claude.outputs.execution_file }}"},
		{"codex-action", "codex.yaml", existingCodexReview, codexAction, "codex", "codex-home", "${{ runner.temp }}/codex-home"},
	} {
		t.Run(test.label, func(t *testing.T) {
			root := t.TempDir()
			name := workflowDir + "/" + test.file
			writeFile(t, filepath.Join(root, name), test.workflow)
			writeFile(t, filepath.Join(root, workflowDir, "lint.yml"), "name: lint\non: push\njobs:\n  lint:\n    runs-on: ubuntu-24.04\n    steps:\n      - run: make lint\n")
			before := tree(t, root)
			code, patch, stderr := runInit(t, root, releaseBuild, costroid.Recipes, "github")
			if code != 0 {
				t.Fatalf("exit %d, stderr %q", code, stderr)
			}
			if !reflect.DeepEqual(before, tree(t, root)) {
				t.Error("init wrote a file in place of the patch")
			}
			said := "costroid: " + name + " already runs " + test.action + ", so init prints the patch that reports from it, as --existing " + name + " does\n"
			if !strings.HasPrefix(stderr, said) || !strings.Contains(stderr, "costroid: printed a patch for "+name+", and wrote nothing") {
				t.Errorf("stderr does not say why init switched:\n%s", stderr)
			}
			patched := applyPatch(t, name, test.workflow, patch)
			if got, was := lintRules(name, patched), lintRules(name, test.workflow); !slices.Equal(got, was) {
				t.Errorf("costroid lint finds %v in the patched workflow, and %v before", got, was)
			}
			steps := decodeYAML(t, patched)["jobs"].(map[string]any)["review"].(map[string]any)["steps"].([]any)
			agent := steps[len(steps)-2].(map[string]any)
			read := steps[len(steps)-1].(map[string]any)
			with, _ := read["with"].(map[string]any)
			if agent["id"] != test.stepID || read["uses"] != costroidActionPath+"@"+testCommit || with["stage"] != "read" || with[test.readKey] != test.readValue {
				t.Errorf("the agent's step %v, then %v", agent, read)
			}
		})
	}

	t.Run("an explicit agent writes its recipe", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, workflowDir, "claude.yml"), existingReview)
		code, stdout, stderr := runInit(t, root, releaseBuild, costroid.Recipes, "github", "--agent", "claude")
		if code != 0 || !strings.Contains(stdout, "costroid: wrote .github/workflows/costroid.yml") || strings.Contains(stderr, "already runs") {
			t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
	})

	for _, refusal := range []struct {
		label string
		files map[string]string
		want  string
	}{
		{"none", map[string]string{"lint.yml": "on: push\njobs: {}\n"}, "--agent is one of claude, claude-code-action, codex, codex-action"},
		{"one that reports", map[string]string{"claude.yml": existingReview + "      - uses: " + costroidActionPath + "@" + otherCommit + " # v1.0.0-alpha.9\n"}, "--agent is one of claude, claude-code-action, codex, codex-action"},
		{"two", map[string]string{"claude.yml": existingReview, "codex.yml": existingCodexReview}, ".github/workflows/claude.yml and .github/workflows/codex.yml each run an agent's action: name the one to report from with --existing"},
		{"no codex-home", map[string]string{"codex.yml": strings.Replace(existingCodexReview, "          codex-home: ${{ runner.temp }}/codex-home\n", "", 1)}, "sets no codex-home"},
	} {
		t.Run(refusal.label, func(t *testing.T) {
			root := t.TempDir()
			for file, workflow := range refusal.files {
				writeFile(t, filepath.Join(root, workflowDir, file), workflow)
			}
			before := tree(t, root)
			code, stdout, stderr := runInit(t, root, releaseBuild, costroid.Recipes, "github")
			if code != exitFailure || stdout != "" || !strings.Contains(stderr, refusal.want) {
				t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
			}
			if !reflect.DeepEqual(before, tree(t, root)) {
				t.Error("init changed the repository")
			}
		})
	}
}

func TestInitNamesTheBasisForASubscriptionWorkflow(t *testing.T) {
	for _, test := range []struct {
		label, workflow string
		names           bool
	}{
		{"subscription", mentionWorkflow, true},
		{"API key", strings.Replace(mentionWorkflow, "claude_code_oauth_token: ${{ secrets.CLAUDE_CODE_OAUTH_TOKEN }}", "anthropic_api_key: ${{ secrets.ANTHROPIC_API_KEY }}", 1), false},
	} {
		t.Run(test.label, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, workflowDir, "claude.yml"), test.workflow)
			for _, args := range [][]string{{"github"}, {"github", "--existing", workflowDir + "/claude.yml"}} {
				code, _, stderr := runInit(t, root, releaseBuild, costroid.Recipes, args...)
				if code != 0 {
					t.Fatalf("%v: exit %d, stderr %q", args, code, stderr)
				}
				if flat := strings.Join(strings.Fields(stderr), " "); strings.Contains(flat, subscriptionBasis) != test.names {
					t.Errorf("%v: the basis line is there: %t, want %t:\n%s", args, !test.names, test.names, stderr)
				}
			}
		})
	}
	if subscriptionBasis != "Figures will read List-equivalent, not billed. No subscription run has been recorded yet, so that reading is unverified." {
		t.Errorf("the basis line reads %q", subscriptionBasis)
	}
}

func TestInitAuthSubscriptionWritesTheOAuthRecipe(t *testing.T) {
	root := t.TempDir()
	code, stdout, stderr := runInit(t, root, releaseBuild, costroid.Recipes, "github", "--agent", "claude-code-action", "--auth", "subscription")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	written := readFile(t, filepath.Join(root, initOut[githubHost]))
	if strings.Count(written, "claude_code_oauth_token: ${{ secrets.CLAUDE_CODE_OAUTH_TOKEN }}\n") != 1 || strings.Contains(written, "anthropic_api_key") || strings.Contains(written, "ANTHROPIC_API_KEY") {
		t.Errorf("the recipe does not sign in with the subscription's token alone:\n%s", written)
	}
	if report := recipelint.LintBytes(initOut[githubHost], []byte(written)); len(report.Findings) > 0 {
		t.Errorf("lint-recipes rejects what init wrote: %v", report.Findings)
	}
	if got := lintRules(initOut[githubHost], written); len(got) > 0 {
		t.Errorf("costroid lint finds %v in what init wrote", got)
	}
	commands := printedCommands(t, stdout)
	if !slices.Contains(commands, "gh secret set CLAUDE_CODE_OAUTH_TOKEN") || slices.Contains(commands, "gh secret set ANTHROPIC_API_KEY") {
		t.Errorf("the commands set the wrong secret: %q", commands)
	}
	if flat := strings.Join(strings.Fields(stdout), " "); !strings.Contains(flat, subscriptionBasis) {
		t.Errorf("stdout does not name the basis:\n%s", stdout)
	}

	apiKey := t.TempDir()
	if code, stdout, _ := runInit(t, apiKey, releaseBuild, costroid.Recipes, "github", "--agent", "claude-code-action", "--auth", "api-key"); code != 0 || strings.Contains(stdout, "List-equivalent") || !strings.Contains(readFile(t, filepath.Join(apiKey, initOut[githubHost])), "anthropic_api_key: ${{ secrets.ANTHROPIC_API_KEY }}") {
		t.Errorf("--auth api-key: exit %d, stdout %q", code, stdout)
	}

	for _, refusal := range []struct {
		args []string
		want string
	}{
		{[]string{"github", "--agent", "claude", "--auth", "subscription"}, "--auth subscription writes the claude-code-action recipe"},
		{[]string{"github", "--agent", "codex-action", "--auth", "subscription"}, "--auth subscription writes the claude-code-action recipe"},
		{[]string{"gitlab", "--agent", "claude", "--auth", "subscription"}, "--auth subscription writes the claude-code-action recipe"},
		{[]string{"github", "--existing", "claude.yml", "--auth", "subscription"}, "--existing keeps the sign-in the workflow has"},
		{[]string{"github", "--agent", "claude-code-action", "--auth", "oauth"}, `--auth is api-key or subscription, not "oauth"`},
	} {
		root := t.TempDir()
		code, stdout, stderr := runInit(t, root, releaseBuild, costroid.Recipes, refusal.args...)
		if code != exitFailure || stdout != "" || !strings.Contains(stderr, refusal.want) {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", refusal.args, code, stdout, stderr)
		}
		if files := tree(t, root); len(files) != 0 {
			t.Errorf("%v: init wrote %v", refusal.args, files)
		}
	}
}

func TestExistingReportJobRunsOnlyOnPullRequests(t *testing.T) {
	for _, test := range []struct{ label, workflow, job string }{
		{"claude-code-action on mentions", mentionWorkflow, "claude"},
		{"claude-code-action", existingReview, "review"},
		{"codex-action", existingCodexReview, "review"},
	} {
		t.Run(test.label, func(t *testing.T) {
			root := t.TempDir()
			name := workflowDir + "/review.yml"
			writeFile(t, filepath.Join(root, name), test.workflow)
			code, patch, stderr := runInit(t, root, releaseBuild, costroid.Recipes, "github", "--existing", name)
			if code != 0 {
				t.Fatalf("exit %d, stderr %q", code, stderr)
			}
			patched := applyPatch(t, name, test.workflow, patch)
			report := decodeYAML(t, patched)["jobs"].(map[string]any)["report"].(map[string]any)
			if report["if"] != "always() && github.event_name == 'pull_request'" || report["needs"] != test.job {
				t.Errorf("the report job runs on %v, after %v", report["if"], report["needs"])
			}
		})
	}
}

func TestGitLabInitWritesTheInclude(t *testing.T) {
	const entry = "- local: .gitlab/costroid.yml"
	for _, test := range []struct {
		label, before, after, said string
	}{
		{"no file", "", "include:\n  " + entry + "\n", "costroid: wrote .gitlab-ci.yml, which includes .gitlab/costroid.yml"},
		{"no include", "stages:\n  - build\n\nbuild:\n  script: make\n", "stages:\n  - build\n\nbuild:\n  script: make\n\ninclude:\n  " + entry + "\n", "costroid: added .gitlab/costroid.yml to the include of .gitlab-ci.yml"},
		{"no final newline", "build:\n  script: make", "build:\n  script: make\n\ninclude:\n  " + entry + "\n", "costroid: added"},
		{"comments alone", "# Nothing yet.\n", "# Nothing yet.\n\ninclude:\n  " + entry + "\n", "costroid: added"},
		{"a list", "include:\n  - project: example/templates\n    file: lint.yml\n  - local: ci/build.yml\n\nstages: [test]\n", "include:\n  - project: example/templates\n    file: lint.yml\n  - local: ci/build.yml\n  " + entry + "\n\nstages: [test]\n", "costroid: added"},
		{"a list at the key's indent", "include:\n- 'ci/build.yml'\nstages: [test]\n", "include:\n- 'ci/build.yml'\n" + entry + "\nstages: [test]\n", "costroid: added"},
		{"included", "include:\n  - local: /.gitlab/costroid.yml\n", "include:\n  - local: /.gitlab/costroid.yml\n", "costroid: .gitlab-ci.yml already includes .gitlab/costroid.yml"},
	} {
		t.Run(test.label, func(t *testing.T) {
			root := t.TempDir()
			if test.label != "no file" {
				writeFile(t, filepath.Join(root, gitlabCI), test.before)
			}
			code, stdout, stderr := runInit(t, root, releaseBuild, costroid.Recipes, "gitlab", "--agent", "claude")
			if code != 0 {
				t.Fatalf("exit %d, stderr %q", code, stderr)
			}
			if got := readFile(t, filepath.Join(root, gitlabCI)); got != test.after {
				t.Errorf(".gitlab-ci.yml reads\n%s\nwant\n%s", got, test.after)
			}
			if !strings.Contains(stdout, test.said) {
				t.Errorf("stdout does not say %q:\n%s", test.said, stdout)
			}
			add := "git add .gitlab/costroid.yml costroid.yml .gitlab-ci.yml"
			if test.label == "included" {
				add = "git add .gitlab/costroid.yml costroid.yml"
			}
			if !slices.Contains(printedCommands(t, stdout), add) {
				t.Errorf("the commands do not run %q:\n%s", add, stdout)
			}
			if strings.Contains(stdout, "include:") {
				t.Errorf("init asks for the include it wrote:\n%s", stdout)
			}
		})
	}

	t.Run("out", func(t *testing.T) {
		root := t.TempDir()
		code, stdout, stderr := runInit(t, root, releaseBuild, costroid.Recipes, "gitlab", "--agent", "codex", "--out", "ci/costroid.yml")
		if code != 0 || readFile(t, filepath.Join(root, gitlabCI)) != "include:\n  - local: ci/costroid.yml\n" {
			t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
	})

	t.Run("out over the pipeline", func(t *testing.T) {
		root := t.TempDir()
		code, stdout, stderr := runInit(t, root, releaseBuild, costroid.Recipes, "gitlab", "--agent", "codex", "--out", gitlabCI)
		if code != exitFailure || stdout != "" || !strings.Contains(stderr, "would replace the pipeline") || len(tree(t, root)) != 0 {
			t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
	})

	for _, refusal := range []struct{ label, before, want string }{
		{"not YAML", "stages: [\n", "init cannot read it as YAML"},
		{"two documents", "a: 1\n---\nb: 2\n", "more than one YAML document"},
		{"a list at the top", "- a\n", "not a block of keys"},
		{"one file named", "include: ci/build.yml\n", "its include is not a list init adds to"},
		{"a flow list", "include: [ci/build.yml]\n", "its include is not a list init adds to"},
		{"a mapping", "include:\n  local: ci/build.yml\n", "its include is not a list init adds to"},
	} {
		t.Run(refusal.label, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, gitlabCI), refusal.before)
			before := tree(t, root)
			code, stdout, stderr := runInit(t, root, releaseBuild, costroid.Recipes, "gitlab", "--agent", "claude")
			flat := strings.Join(strings.Fields(stderr), " ")
			if code != exitFailure || stdout != "" || !strings.Contains(flat, refusal.want) || !strings.Contains(flat, `so init wrote nothing: add "- local: .gitlab/costroid.yml" to its include by hand`) {
				t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
			}
			if !reflect.DeepEqual(before, tree(t, root)) {
				t.Error("init changed the repository")
			}
		})
	}
}

// TestGitLabPrintedCommandsRun runs the commands init prints, from the
// directory init ran in, and finds the three files on the pushed branch.
func TestGitLabPrintedCommandsRun(t *testing.T) {
	root := gitRepository(t)
	remote := t.TempDir()
	runGit(t, remote, "init", "--quiet", "--bare")
	runGit(t, root, "remote", "add", "origin", remote)
	sub := filepath.Join(root, "app")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runInitIn(t, repository{root: root, dir: sub}, releaseBuild, costroid.Recipes, "--agent", "claude")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	flat := strings.Join(strings.Fields(stdout), " ")
	if !strings.Contains(flat, "with a budget of USD 5 for each run") || strings.Contains(stdout, "COSTROID_BUDGET_USD") {
		t.Errorf("stdout does not state the budget as an amount:\n%s", stdout)
	}
	script := "set -e\n" + strings.Join(printedCommands(t, stdout), "\n") + "\n"
	command := exec.Command("sh", "-c", script)
	command.Dir = sub
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("the printed commands failed: %v\n%s\n%s", err, script, output)
	}
	pushed := strings.Fields(runGit(t, remote, "ls-tree", "-r", "--name-only", "try-costroid"))
	if want := []string{".gitlab-ci.yml", ".gitlab/costroid.yml", "costroid.yml"}; !slices.Equal(pushed, want) {
		t.Errorf("the pushed branch holds %v, want %v", pushed, want)
	}
}

func TestInitPrintsTheNoteAfterTheSteps(t *testing.T) {
	// The shipped recipe draws no note, so this one runs harden-runner in audit
	// mode, which draws one.
	shipped, err := fs.ReadFile(costroid.Recipes, "recipes/github-claude-code.yml")
	if err != nil {
		t.Fatal(err)
	}
	audit := regexp.MustCompile(`(?s)egress-policy: block\n.*?(\n      - uses: actions/checkout)`).ReplaceAll(shipped, []byte("egress-policy: audit$1"))
	if bytes.Equal(audit, shipped) {
		t.Fatal("the shipped recipe has no block-mode harden-runner step to turn into audit mode")
	}
	recipes := fstest.MapFS{"recipes/github-claude-code.yml": {Data: audit}}
	code, stdout, stderr := runInit(t, t.TempDir(), releaseBuild, recipes, "github", "--agent", "claude")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	note := strings.Index(stdout, "\nnote: .github/workflows/costroid.yml:")
	last := strings.Index(stdout, "  gh pr create --fill\n")
	label := strings.Index(stdout, "\nBefore a first contributor's pull request:\n  gh label create costroid-agent\n")
	if note < 0 || last < 0 || label < 0 || note < last || note < label {
		t.Errorf("the note does not follow the steps:\n%s", stdout)
	}
	for line := range strings.SplitSeq(stdout, "\n") {
		if strings.Contains(line, recipelint.RuleEgressAudit) && !strings.HasPrefix(line, "note: ") {
			t.Errorf("a note without its prefix: %q", line)
		}
	}
	if commands := printedCommands(t, stdout); slices.Contains(commands, "gh label create costroid-agent") || commands[len(commands)-1] != "gh pr create --fill" {
		t.Errorf("the label is among the first pull request's steps: %q", commands)
	}
}

func TestWrittenConfigLeavesOutReservedKeys(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(costroid.ConfigSchema, &schema); err != nil {
		t.Fatal(err)
	}
	var reserved []string
	for name, property := range schema["properties"].(map[string]any) {
		if strings.Contains(property.(map[string]any)["description"].(string), "Reserved:") {
			reserved = append(reserved, name)
		}
	}
	slices.Sort(reserved)
	if want := slices.Sorted(slices.Values(reservedKeys)); !slices.Equal(reserved, want) || len(reserved) != 5 {
		t.Errorf("the schema reserves %v, and init leaves out %v", reserved, want)
	}
	root := t.TempDir()
	if code, _, stderr := runInit(t, root, releaseBuild, costroid.Recipes, "github", "--agent", "claude"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	settings := readFile(t, filepath.Join(root, configFile))
	for _, name := range reservedKeys {
		if strings.Contains(settings, name) {
			t.Errorf("costroid.yml names the reserved key %s", name)
		}
	}
	if strings.Contains(settings, "Reserved") || !strings.Contains(settings, "\n# policy:\n") {
		t.Errorf("costroid.yml:\n%s", settings)
	}
}

func TestWrittenRecipeDropsTheCopyLine(t *testing.T) {
	everyTarget(t, func(t *testing.T, host, agent, recipe string) {
		root := t.TempDir()
		if code, _, stderr := runInit(t, root, releaseBuild, costroid.Recipes, host, "--agent", agent); code != 0 {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		written := readFile(t, filepath.Join(root, initOut[host]))
		header, _ := recipeHeader([]byte(written))
		shipped, _ := recipeHeader(shippedRecipe(t, recipe))
		if !strings.Contains(shipped, "Copy ") || strings.Contains(header, "Copy ") {
			t.Errorf("the header keeps its copy sentence: %q", header)
		}
		for sentence := range strings.SplitSeq(shipped, ". ") {
			sentence = strings.TrimSuffix(sentence, ".")
			if _, then, found := strings.Cut(sentence, ", then "); found && strings.HasPrefix(sentence, "Copy ") {
				sentence = strings.ToUpper(then[:1]) + then[1:]
			} else if strings.HasPrefix(sentence, "Copy ") {
				continue
			}
			if !strings.Contains(header, sentence) {
				t.Errorf("the header lost %q: %q", sentence, header)
			}
		}
		for line := range strings.SplitSeq(written, "\n") {
			if !strings.HasPrefix(line, "#") {
				break
			}
			if !strings.HasPrefix(line, "# ") || len(line) > usageWidth {
				t.Errorf("a header line %q", line)
			}
		}
	})
}

// readPoint is where costroid.yml is read, in the words of every page.
const readPoint = "the head of the pull request's base branch"

func TestSchemaAndDocsNameOneReadPoint(t *testing.T) {
	var descriptions []string
	var walk func(node map[string]any)
	walk = func(node map[string]any) {
		if text, ok := node["description"].(string); ok {
			descriptions = append(descriptions, text)
		}
		properties, _ := node["properties"].(map[string]any)
		for _, child := range properties {
			walk(child.(map[string]any))
		}
	}
	var schema map[string]any
	if err := json.Unmarshal(costroid.ConfigSchema, &schema); err != nil {
		t.Fatal(err)
	}
	walk(schema)
	stated := 0
	for _, text := range descriptions {
		for sentence := range strings.SplitSeq(text, ". ") {
			if strings.Contains(sentence, "server") || strings.Contains(sentence, "base commit") {
				t.Errorf("the schema says %q", sentence)
			}
			if regexp.MustCompile(`\breads?\b`).MatchString(sentence) && strings.Contains(sentence, "base") {
				stated++
				if !strings.Contains(sentence, readPoint) {
					t.Errorf("the schema states another read point: %q", sentence)
				}
			}
		}
	}
	if stated < 3 {
		t.Errorf("the schema states the read point %d times, want the file, policy and approval", stated)
	}

	page := readFile(t, "../../docs/configuration.md")
	sentence := regexp.MustCompile(`(?m)^[^\n]*The report reads it at [^.]*\.`).FindString(page)
	if !strings.Contains(sentence, "The report reads it at "+readPoint) {
		t.Errorf("docs/configuration.md states the read point as %q", sentence)
	}
	if strings.Contains(page, "base commit") {
		t.Error("docs/configuration.md names the base commit")
	}

	settings, err := commentedConfig()
	if err != nil {
		t.Fatal(err)
	}
	_, described, _ := strings.Cut(string(settings), "\n")
	words := slices.DeleteFunc(strings.Fields(described), func(word string) bool { return word == "#" })
	if flat := strings.Join(words, " "); !strings.Contains(flat, readPoint) || strings.Contains(flat, "server") {
		t.Errorf("the costroid.yml init writes does not state the read point:\n%s", settings)
	}
}

// fakeGH, fakeGit and fakeStty stand in for gh, git and stty in the tests of
// --pr. Each prints "ran:" and its arguments, a tab before each, to stderr,
// and appends the same line without the prefix to $FAKE_LOG. They use only
// the shell's builtins, so with PATH set to their directory alone nothing
// else runs.
const (
	fakeGH = `#!/bin/sh
printf 'ran:\tgh' >&2
printf 'gh' >> "$FAKE_LOG"
for arg in "$@"; do printf '\t%s' "$arg" >&2; printf '\t%s' "$arg" >> "$FAKE_LOG"; done
printf '\n' >&2
printf '\n' >> "$FAKE_LOG"
[ "$FAKE_FAIL" = "gh $1 $2" ] && exit 1
case "$1 $2" in
"secret list") printf '%s\n' "$FAKE_SECRETS" ;;
"secret set") while IFS= read -r line || [ -n "$line" ]; do printf '%s' "$line" >> "$FAKE_STDIN"; done ;;
"pr create") echo "https://github.com/example/widgets/pull/7" ;;
"run list") printf '%s\n' "$FAKE_RUNS" ;;
"run watch") echo "the run completed" ;;
"pr view") printf '%s\n' "$FAKE_COMMENTS" ;;
esac
exit 0
`
	fakeGit = `#!/bin/sh
printf 'ran:\tgit' >&2
printf 'git' >> "$FAKE_LOG"
for arg in "$@"; do printf '\t%s' "$arg" >&2; printf '\t%s' "$arg" >> "$FAKE_LOG"; done
printf '\n' >&2
printf '\n' >> "$FAKE_LOG"
[ "$1" = "-C" ] && shift 2
[ "$FAKE_FAIL" = "git $1" ] && exit 1
case "$1" in
remote) echo origin ;;
rev-parse) echo 0123456789abcdef0123456789abcdef01234567 ;;
esac
exit 0
`
	fakeStty = `#!/bin/sh
printf 'stty\t%s\n' "$1" >> "$FAKE_LOG"
exit 0
`
	fakePull    = "https://github.com/example/widgets/pull/7"
	fakeComment = fakePull + "#issuecomment-9"
)

// fakes are the fake tools' directory and the files they write.
type fakes struct {
	log, stdin string
}

// installFakes puts fake gh, git and stty alone on PATH, so neither the real
// gh nor the real git can run, and sets what they answer.
func installFakes(t *testing.T) fakes {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	for name, script := range map[string]string{"gh": fakeGH, "git": fakeGit, "stty": fakeStty} {
		writeFile(t, filepath.Join(bin, name), script)
		if err := os.Chmod(filepath.Join(bin, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f := fakes{log: filepath.Join(dir, "calls.log"), stdin: filepath.Join(dir, "stdin")}
	for name, value := range map[string]string{
		"PATH": bin, "FAKE_LOG": f.log, "FAKE_STDIN": f.stdin, "FAKE_FAIL": "", "FAKE_SECRETS": "[]",
		"FAKE_RUNS":     `[{"databaseId":42,"url":"https://github.com/example/widgets/actions/runs/42","workflowName":"costroid"}]`,
		"FAKE_COMMENTS": `{"comments":[{"body":"thanks","url":"` + fakePull + `#issuecomment-8"},{"body":"<!-- costroid:comment -->\n### Costroid","url":"` + fakeComment + `"}]}`,
	} {
		t.Setenv(name, value)
	}
	return f
}

// calls are the fake tools' runs in order, each as its arguments.
func (f fakes) calls(t *testing.T) [][]string {
	t.Helper()
	data, err := os.ReadFile(f.log)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	for line := range strings.SplitSeq(strings.TrimSuffix(string(data), "\n"), "\n") {
		calls = append(calls, strings.Split(line, "\t"))
	}
	return calls
}

// runPR runs init with --pr in a directory with no repository, where only
// the fake tools answer, and returns its exit code and everything it printed,
// stdout and stderr in the order they came.
func runPR(t *testing.T, root string, settings prSettings, args ...string) (int, string) {
	t.Helper()
	var output bytes.Buffer
	repo := repository{root: root, dir: root}
	code := initRepository(func() (repository, error) { return repo, nil }, releaseBuild, costroid.Recipes, settings, args, &output, &output)
	return code, output.String()
}

// prSteps are the commands init --pr runs on the claude recipe with
// --platform-url, in order.
var prSteps = []string{
	"gh secret list --json name",
	"gh secret set ANTHROPIC_API_KEY",
	"gh variable set COSTROID_PLATFORM_URL --body https://app.costroid.com",
	"git switch --create try-costroid",
	"git add -- .github/workflows/costroid.yml .github/workflows/costroid-connect.yml costroid.yml",
	"git commit --message 'Add the costroid check' -- .github/workflows/costroid.yml .github/workflows/costroid-connect.yml costroid.yml",
	"git rev-parse HEAD",
	"git push --set-upstream origin try-costroid",
	"gh pr create --fill --head try-costroid",
	"gh run list --commit " + testCommit + " --event pull_request --json databaseId,url,workflowName",
	"gh run watch 42",
	"gh pr view " + fakePull + " --json comments",
}

func TestInitPRNamesEachStepBeforeRunningIt(t *testing.T) {
	installFakes(t)
	t.Setenv("TEST_AGENT_KEY", "sk-ant-test-0123456789")
	code, output := runPR(t, t.TempDir(), testPR, "github", "--agent", "claude", "--platform-url", "https://app.costroid.com", "--pr", "--key-env", "TEST_AGENT_KEY")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, output)
	}
	var named, ran []string
	pending := ""
	for line := range strings.SplitSeq(output, "\n") {
		switch {
		case strings.HasPrefix(line, "$ "):
			if pending != "" {
				t.Errorf("init named %q and ran nothing before it named %q", pending, line[2:])
			}
			pending = line[2:]
			named = append(named, pending)
		case strings.HasPrefix(line, "ran:\t"):
			command := shellJoin(strings.Split(line, "\t")[1:])
			if command != pending {
				t.Errorf("%q ran, and the line before it named %q", command, pending)
			}
			ran = append(ran, command)
			pending = ""
		}
	}
	if !slices.Equal(named, prSteps) || !slices.Equal(ran, prSteps) {
		t.Errorf("named:\n%s\nran:\n%s\nwant:\n%s", strings.Join(named, "\n"), strings.Join(ran, "\n"), strings.Join(prSteps, "\n"))
	}
	if at := strings.Index(output, "costroid: the run posted its comment at "+fakeComment+"\n"); at < strings.LastIndex(output, "ran:\t") {
		t.Errorf("init does not print the comment's address after the last command:\n%s", output)
	}
	if strings.Contains(output, "These commands are left") {
		t.Errorf("init lists commands as left after running them:\n%s", output)
	}
}

func TestInitPRNeverPrintsTheKey(t *testing.T) {
	const key = "sk-ant-test-never-printed-5550"
	for _, test := range []struct {
		label string
		// terminal is what the terminal holds, and args what init is given
		// beyond the host and the agent.
		terminal string
		args     []string
	}{
		{"from --key-env", "", []string{"--pr", "--key-env", "TEST_AGENT_KEY"}},
		{"from the prompt", key + "\n", []string{"--pr"}},
	} {
		t.Run(test.label, func(t *testing.T) {
			fake := installFakes(t)
			t.Setenv("TEST_AGENT_KEY", key)
			settings := testPR
			if test.terminal != "" {
				settings.terminal = filepath.Join(t.TempDir(), "tty")
				writeFile(t, settings.terminal, test.terminal)
			}
			root := t.TempDir()
			code, output := runPR(t, root, settings, append([]string{"github", "--agent", "claude"}, test.args...)...)
			if code != 0 {
				t.Fatalf("exit %d:\n%s", code, output)
			}
			if strings.Contains(output, key) {
				t.Errorf("init printed the key:\n%s", output)
			}
			calls := fake.calls(t)
			for _, call := range calls {
				if slices.ContainsFunc(call, func(arg string) bool { return strings.Contains(arg, key) }) {
					t.Errorf("the key is in the arguments of %v", call)
				}
			}
			if sent := readFile(t, fake.stdin); sent != key {
				t.Errorf("gh secret set read %q on its standard input, want the key", sent)
			}
			for name, content := range tree(t, root) {
				if strings.Contains(content, key) {
					t.Errorf("the key is in %s", name)
				}
			}
			if test.terminal == "" {
				return
			}
			order := make([]string, len(calls))
			for i, call := range calls {
				order[i] = strings.Join(call, " ")
			}
			off, on, set := slices.Index(order, "stty -echo"), slices.Index(order, "stty echo"), slices.Index(order, "gh secret set ANTHROPIC_API_KEY")
			if off < 0 || on < off || set < on {
				t.Errorf("the terminal's echo is not off while the key is read:\n%s", strings.Join(order, "\n"))
			}
			if !strings.Contains(output, "The value of ANTHROPIC_API_KEY, which init passes to gh and never prints: ") {
				t.Errorf("init asked for no key:\n%s", output)
			}
		})
	}
}

func TestInitPRSkipsAnExistingSecret(t *testing.T) {
	fake := installFakes(t)
	t.Setenv("FAKE_SECRETS", `[{"name":"OPENAI_API_KEY"},{"name":"ANTHROPIC_API_KEY"}]`)
	code, output := runPR(t, t.TempDir(), testPR, "github", "--agent", "claude", "--pr")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, output)
	}
	if !strings.Contains(output, "costroid: the repository has the secret ANTHROPIC_API_KEY already, so init leaves it as it is\n") {
		t.Errorf("init does not say it left the secret:\n%s", output)
	}
	for _, call := range fake.calls(t) {
		if len(call) > 2 && call[0] == "gh" && call[1] == "secret" && call[2] == "set" {
			t.Errorf("init ran %v over a secret the repository has", call)
		}
	}
	if _, err := os.Stat(fake.stdin); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("gh secret set was given a value: %v", err)
	}
}

// repositoryWins is what init --pr says once before it sets the key.
const repositoryWins = "costroid: a repository secret takes precedence over an organization secret of the same name, so the run reads the ANTHROPIC_API_KEY set here. Pass --keep-org-secret to keep the organization's\n"

// TestInitPRSaysARepositorySecretWins holds the line init --pr prints once
// before it sets the key, and --keep-org-secret, which lists and sets no
// repository secret, so the run reads the organization's.
func TestInitPRSaysARepositorySecretWins(t *testing.T) {
	t.Run("it sets the key", func(t *testing.T) {
		installFakes(t)
		t.Setenv("TEST_AGENT_KEY", "sk-ant-test-0123456789")
		code, output := runPR(t, t.TempDir(), testPR, "github", "--agent", "claude", "--pr", "--key-env", "TEST_AGENT_KEY")
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, output)
		}
		if strings.Count(output, repositoryWins) != 1 {
			t.Fatalf("init says %d times that a repository secret wins:\n%s", strings.Count(output, repositoryWins), output)
		}
		said := strings.Index(output, repositoryWins)
		if named, ran := strings.Index(output, "$ gh secret set ANTHROPIC_API_KEY\n"), strings.Index(output, "ran:\tgh\tsecret\tset"); named < 0 || said < named || ran < said {
			t.Errorf("init does not say it between naming gh secret set and running it:\n%s", output)
		}
	})

	t.Run("the repository has the secret", func(t *testing.T) {
		installFakes(t)
		t.Setenv("FAKE_SECRETS", `[{"name":"ANTHROPIC_API_KEY"}]`)
		code, output := runPR(t, t.TempDir(), testPR, "github", "--agent", "claude", "--pr")
		if code != 0 || strings.Contains(output, "takes precedence") {
			t.Errorf("exit %d, and init sets no key yet says a repository secret wins:\n%s", code, output)
		}
	})

	t.Run("--keep-org-secret", func(t *testing.T) {
		fake := installFakes(t)
		code, output := runPR(t, t.TempDir(), testPR, "github", "--agent", "claude", "--platform-url", "https://app.costroid.com", "--pr", "--keep-org-secret")
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, output)
		}
		if !strings.HasPrefix(output, "costroid: wrote ") || !strings.Contains(output, "costroid: init sets no repository secret ANTHROPIC_API_KEY, so the run reads the organization's\n") || strings.Contains(output, "takes precedence") {
			t.Errorf("init does not say it leaves the key to the organization:\n%s", output)
		}
		var ran []string
		for _, call := range fake.calls(t) {
			if (call[0] == "gh" || call[0] == "git") && (len(call) < 2 || call[1] != "-C") {
				ran = append(ran, shellJoin(call))
			}
		}
		if want := prSteps[2:]; !slices.Equal(ran, want) {
			t.Errorf("ran:\n%s\nwant:\n%s", strings.Join(ran, "\n"), strings.Join(want, "\n"))
		}
		if _, err := os.Stat(fake.stdin); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("gh was given a key: %v", err)
		}
	})

	for _, refusal := range []struct {
		args []string
		want string
	}{
		{[]string{"github", "--agent", "claude", "--keep-org-secret"}, "--keep-org-secret stops --pr from setting the key, so it needs --pr"},
		{[]string{"github", "--agent", "claude", "--pr", "--keep-org-secret", "--key-env", "TEST_AGENT_KEY"}, "--keep-org-secret sets no key, so --key-env does not apply"},
	} {
		installFakes(t)
		root := t.TempDir()
		code, output := runPR(t, root, testPR, refusal.args...)
		if code != exitFailure || !strings.Contains(output, refusal.want) || len(tree(t, root)) != 0 {
			t.Errorf("%v: exit %d:\n%s", refusal.args, code, output)
		}
	}
}

func TestInitPRStopsAtTheFirstFailedStep(t *testing.T) {
	failures := []struct {
		label, fail, runs, comments, want string
		// at is the index in prSteps of the command that fails.
		at int
	}{
		{label: "secret list", fail: "gh secret list", at: 0, want: "exit status 1"},
		{label: "secret set", fail: "gh secret set", at: 1, want: "exit status 1"},
		{label: "variable", fail: "gh variable set", at: 2, want: "exit status 1"},
		{label: "branch", fail: "git switch", at: 3, want: "exit status 1"},
		{label: "push", fail: "git push", at: 7, want: "exit status 1"},
		{label: "pull request", fail: "gh pr create", at: 8, want: "exit status 1"},
		{label: "no run", runs: "[]", at: 9, want: "no run of the workflow costroid started on the commit"},
		{label: "watch", fail: "gh run watch", at: 10, want: "exit status 1"},
		{label: "no comment", comments: `{"comments":[]}`, at: 11, want: "the run ended and the pull request holds no Costroid comment, so read the run's log at https://github.com/example/widgets/actions/runs/42"},
	}
	for _, test := range failures {
		t.Run(test.label, func(t *testing.T) {
			fake := installFakes(t)
			t.Setenv("FAKE_FAIL", test.fail)
			t.Setenv("TEST_AGENT_KEY", "sk-ant-test-0123456789")
			if test.runs != "" {
				t.Setenv("FAKE_RUNS", test.runs)
			}
			if test.comments != "" {
				t.Setenv("FAKE_COMMENTS", test.comments)
			}
			code, output := runPR(t, t.TempDir(), testPR, "github", "--agent", "claude", "--platform-url", "https://app.costroid.com", "--pr", "--key-env", "TEST_AGENT_KEY")
			if code != exitFailure {
				t.Fatalf("exit %d:\n%s", code, output)
			}
			if failed := "costroid: init: " + prSteps[test.at] + " failed: " + test.want; !strings.Contains(output, failed) {
				t.Errorf("init does not say %q:\n%s", failed, output)
			}
			last := fake.calls(t)[len(fake.calls(t))-1]
			if shellJoin(last) != prSteps[test.at] {
				t.Errorf("the last command that ran is %v, want %s", last, prSteps[test.at])
			}
			_, list, found := strings.Cut(output, "init stopped at the first command that failed. These commands are left, that one first:\n")
			if !found {
				t.Fatalf("init lists nothing as left:\n%s", output)
			}
			list, _, _ = strings.Cut(list, "costroid: init:")
			var left []string
			for line := range strings.SplitSeq(strings.TrimSpace(list), "\n") {
				left = append(left, strings.TrimSpace(line))
			}
			want := placeholders(prSteps[test.at:], test.at)
			if !slices.Equal(left, want) {
				t.Errorf("left:\n%s\nwant:\n%s", strings.Join(left, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}

// placeholders are the steps from at on as init lists them when the step at
// failed: a value the steps before it did not find reads as its placeholder.
// git rev-parse, gh pr create and gh run list are steps 6, 8 and 9.
func placeholders(steps []string, at int) []string {
	out := slices.Clone(steps)
	for i := range out {
		if at <= 6 {
			out[i] = strings.Replace(out[i], testCommit, "<commit>", 1)
		}
		if at <= 8 {
			out[i] = strings.Replace(out[i], fakePull, prBranch, 1)
		}
		if at <= 9 {
			out[i] = strings.Replace(out[i], "watch 42", "watch <run id>", 1)
		}
	}
	return out
}

func TestInitPRRefusesWhatItCannotRun(t *testing.T) {
	installFakes(t)
	for _, refusal := range []struct {
		args []string
		want string
	}{
		{[]string{"gitlab", "--agent", "claude", "--pr"}, "--pr opens a pull request with gh, which works on GitHub alone"},
		{[]string{"github", "--agent", "claude-code-action", "--existing", "x.yml", "--pr"}, "--existing writes nothing, so --pr and --skills do not apply"},
		{[]string{"github", "--agent", "claude", "--key-env", "KEY"}, "--key-env names the key --pr sets as a secret, so it needs --pr"},
		{[]string{"github", "--agent", "claude", "--pr", "--key-env", "NOT A NAME"}, `not "NOT A NAME"`},
		{[]string{"github", "--agent", "claude", "--pr", "--key-env", "COSTROID_TEST_UNSET"}, "gh secret set ANTHROPIC_API_KEY failed: the environment variable COSTROID_TEST_UNSET is empty"},
		{[]string{"github", "--agent", "claude", "--pr"}, "gh secret set ANTHROPIC_API_KEY failed: no terminal to ask for the key on, so name the environment variable that holds it with --key-env"},
	} {
		code, output := runPR(t, t.TempDir(), testPR, refusal.args...)
		if code != exitFailure || !strings.Contains(output, refusal.want) {
			t.Errorf("%v: exit %d:\n%s", refusal.args, code, output)
		}
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, workflowDir, "review.yml"), existingReview)
	code, output := runPR(t, root, testPR, "github", "--pr")
	if want := ".github/workflows/review.yml already runs anthropics/claude-code-action, and init reads it as --existing does, which writes nothing: pass --agent to write a new workflow with --pr or --skills"; code != exitFailure || !strings.Contains(output, want) {
		t.Errorf("--pr beside a claude-code-action workflow: exit %d:\n%s", code, output)
	}
}

// writtenSteps decodes a written workflow and returns each step that uses
// Costroid's action, by its stage.
func writtenSteps(t *testing.T, workflow string) map[string]map[string]any {
	t.Helper()
	steps := map[string]map[string]any{}
	for _, job := range decodeYAML(t, workflow)["jobs"].(map[string]any) {
		list, _ := job.(map[string]any)["steps"].([]any)
		for _, step := range list {
			uses, _ := step.(map[string]any)["uses"].(string)
			if !strings.HasPrefix(uses, costroidActionPath+"@") {
				continue
			}
			with, _ := step.(map[string]any)["with"].(map[string]any)
			stage, _ := with["stage"].(string)
			steps[stage] = with
		}
	}
	return steps
}

func TestInitPostReviewWritesTheOptIn(t *testing.T) {
	for agent, recipe := range initRecipes[githubHost] {
		t.Run(agent, func(t *testing.T) {
			root := t.TempDir()
			code, stdout, stderr := runInit(t, root, releaseBuild, costroid.Recipes, "github", "--agent", agent, "--post-review")
			if code != 0 {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
			}
			written := readFile(t, filepath.Join(root, workflowDir, "costroid.yml"))
			if report := recipelint.LintBytes("costroid.yml", []byte(written)); len(report.Findings) > 0 {
				t.Errorf("the written recipe breaks a rule: %v", report.Findings)
			}
			steps := writtenSteps(t, written)
			agentStage := "run"
			if strings.HasSuffix(agent, "-action") {
				agentStage = "read"
			}
			for stage, want := range map[string]any{agentStage: "true", "report": "true", "ingest": nil} {
				if steps[stage]["post-review"] != want {
					t.Errorf("the %s step's post-review is %v, want %v", stage, steps[stage]["post-review"], want)
				}
			}
			var kept []string
			for line := range strings.SplitSeq(written, "\n") {
				if strings.TrimSpace(line) != `post-review: "true"` {
					kept = append(kept, line)
				}
			}
			if strings.Join(kept, "\n") != string(writtenRecipe(t, recipe)) {
				t.Errorf("--post-review changed more than the two lines:\n%s", written)
			}
		})
	}
	for _, test := range []struct{ agent, workflow, stage string }{
		{"claude-code-action", existingReview, "read"},
		{"codex-action", existingCodexReview, "read"},
	} {
		t.Run("existing "+test.agent, func(t *testing.T) {
			patched, _ := patchWorkflow(t, test.workflow, "--agent", test.agent, "--post-review")
			steps := writtenSteps(t, patched)
			if steps[test.stage]["post-review"] != "true" || steps["report"]["post-review"] != "true" {
				t.Errorf("the patch does not turn the answer on in both steps:\n%s", patched)
			}
			if rules := lintRules("review.yml", patched); len(rules) > 0 {
				t.Errorf("costroid lint on the patched workflow: %v", rules)
			}
		})
	}
	code, _, stderr := runInit(t, t.TempDir(), releaseBuild, costroid.Recipes, "gitlab", "--agent", "claude", "--post-review")
	if code != exitFailure || !strings.Contains(stderr, "--post-review posts the agent's answer on GitHub alone") {
		t.Errorf("gitlab: exit %d, stderr %q", code, stderr)
	}
}

// writtenRecipe is a shipped recipe as init writes it without flags.
func writtenRecipe(t *testing.T, name string) []byte {
	t.Helper()
	recipe, err := buildRecipe(name, shippedRecipe(t, name), pin{commit: testCommit, version: testRelease}, initFlags{})
	if err != nil {
		t.Fatal(err)
	}
	return recipe
}

// patchWorkflow prints the patch for workflow, applies it, and returns the
// patched workflow and what init said on stderr.
func patchWorkflow(t *testing.T, workflow string, args ...string) (string, string) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "review.yml"), workflow)
	code, patch, stderr := runInit(t, root, releaseBuild, costroid.Recipes, append([]string{"github", "--existing", "review.yml"}, args...)...)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	return applyDiff(t, workflow, patch), stderr
}

func TestInitPlatformURLWritesTheIngestInputs(t *testing.T) {
	inputs := map[string]any{
		"stage":           "ingest",
		"platform-url":    "${{ vars.COSTROID_PLATFORM_URL }}",
		"installation-id": "github-owner:${{ github.repository_owner_id }}",
		"platform-token":  "${{ secrets.COSTROID_PLATFORM_TOKEN }}",
	}
	command := "gh variable set COSTROID_PLATFORM_URL --body https://app.costroid.com"
	for agent := range initRecipes[githubHost] {
		t.Run(agent, func(t *testing.T) {
			root := t.TempDir()
			code, stdout, stderr := runInit(t, root, releaseBuild, costroid.Recipes, "github", "--agent", agent, "--platform-url", "https://app.costroid.com/")
			if code != 0 {
				t.Fatalf("exit %d, stderr %q", code, stderr)
			}
			ingest := writtenSteps(t, readFile(t, filepath.Join(root, workflowDir, "costroid.yml")))["ingest"]
			for key, want := range inputs {
				if ingest[key] != want {
					t.Errorf("the ingest step's %s is %v, want %v", key, ingest[key], want)
				}
			}
			commands := printedCommands(t, stdout)
			if at := slices.Index(commands, command); at < 0 || at > slices.Index(commands, "git switch -c try-costroid") {
				t.Errorf("the commands left do not set the variable before the branch: %v", commands)
			}
			if !strings.Contains(strings.Join(strings.Fields(stdout), " "), connectNeeds) {
				t.Errorf("init does not say what else the platform needs:\n%s", stdout)
			}
		})
	}
	t.Run("existing", func(t *testing.T) {
		patched, stderr := patchWorkflow(t, existingReview, "--agent", "claude-code-action", "--platform-url", "https://app.costroid.com")
		job := decodeYAML(t, patched)["jobs"].(map[string]any)["ingest"].(map[string]any)
		if job["needs"] != "review" || !strings.Contains(job["if"].(string), "vars.COSTROID_PLATFORM_URL != ''") || !strings.Contains(job["if"].(string), "needs.review.result != 'skipped'") {
			t.Errorf("the ingest job: %v", job)
		}
		if permissions := job["permissions"].(map[string]any); !reflect.DeepEqual(permissions, map[string]any{"id-token": "write"}) {
			t.Errorf("the ingest job holds %v", permissions)
		}
		if ingest := writtenSteps(t, patched)["ingest"]; !reflect.DeepEqual(ingest, inputs) {
			t.Errorf("the ingest step's inputs: %v", ingest)
		}
		if rules := lintRules("review.yml", patched); len(rules) > 0 {
			t.Errorf("costroid lint on the patched workflow: %v", rules)
		}
		if !strings.Contains(stderr, "\n  "+command+"\n") {
			t.Errorf("init does not print the variable's command:\n%s", stderr)
		}
	})
	for _, refusal := range []struct {
		args []string
		want string
	}{
		{[]string{"github", "--agent", "claude", "--platform-url", "http://app.costroid.com"}, "--platform-url must be an https origin"},
		{[]string{"github", "--agent", "claude", "--platform-url", "https://app.costroid.com/v1"}, "--platform-url must be an https origin"},
		{[]string{"gitlab", "--agent", "claude", "--platform-url", "https://app.costroid.com"}, "on GitLab, set the CI/CD variables docs/export.md lists"},
	} {
		root := t.TempDir()
		code, _, stderr := runInit(t, root, releaseBuild, costroid.Recipes, refusal.args...)
		if code != exitFailure || !strings.Contains(stderr, refusal.want) || len(tree(t, root)) != 0 {
			t.Errorf("%v: exit %d, stderr %q", refusal.args, code, stderr)
		}
	}
}

// TestInitPlatformURLWritesTheConnectWorkflow holds the workflow that binds
// the owner: --platform-url writes recipes/github-connect.yml beside the
// agent's recipe, pinned as that recipe is, without its copy sentence, and
// lint-clean. It replaces a file only with --force.
func TestInitPlatformURLWritesTheConnectWorkflow(t *testing.T) {
	connect := filepath.FromSlash(connectWorkflow)
	for _, test := range []struct {
		label string
		build buildInfo
		args  []string
		pin   string
	}{
		{"a release", releaseBuild, nil, costroidActionPath + "@" + testCommit + " # v" + testRelease},
		{"--action-ref", buildInfo{version: "dev"}, []string{"--action-ref", otherCommit}, costroidActionPath + "@" + otherCommit + " # set by costroid init --action-ref"},
	} {
		for agent := range initRecipes[githubHost] {
			t.Run(test.label+"/"+agent, func(t *testing.T) {
				root := t.TempDir()
				args := append([]string{"github", "--agent", agent, "--platform-url", "https://app.costroid.com"}, test.args...)
				code, stdout, stderr := runInit(t, root, test.build, costroid.Recipes, args...)
				if code != 0 {
					t.Fatalf("exit %d, stderr %q", code, stderr)
				}
				written := readFile(t, filepath.Join(root, connect))
				recipe := readFile(t, filepath.Join(root, workflowDir, "costroid.yml"))
				pins := actionPin.FindAllString(written, -1)
				if len(pins) == 0 || slices.ContainsFunc(pins, func(pin string) bool { return pin != test.pin }) {
					t.Errorf("the connect workflow pins %v, want %s", pins, test.pin)
				}
				if !strings.Contains(recipe, test.pin) {
					t.Errorf("the agent's recipe does not pin %s, so the two differ", test.pin)
				}
				_, shippedBody := recipeHeader(shippedRecipe(t, connectRecipeName))
				header, writtenBody := recipeHeader([]byte(written))
				if got, want := actionPin.ReplaceAllString(string(writtenBody), "PIN"), actionPin.ReplaceAllString(string(shippedBody), "PIN"); got != want {
					t.Errorf("the connect workflow differs from recipes/%s beyond its pin:\n%s", connectRecipeName, written)
				}
				if strings.Contains(header, "Copy ") || !strings.Contains(header, "docs/connect.md says what the proof is and what it binds.") {
					t.Errorf("the connect workflow's header: %q", header)
				}
				if report := recipelint.LintBytes(connectWorkflow, []byte(written)); len(report.Findings) > 0 {
					t.Errorf("lint-recipes rejects the connect workflow: %v", report.Findings)
				}
				if rules := lintRules(connectWorkflow, written); len(rules) > 0 {
					t.Errorf("costroid lint on the connect workflow: %v", rules)
				}
				said := "costroid: wrote " + connectWorkflow + " from recipes/" + connectRecipeName + ", "
				if !strings.Contains(stdout, said) {
					t.Errorf("init does not say it wrote the connect workflow:\n%s", stdout)
				}
				if want := "git add .github/workflows/costroid.yml " + connectWorkflow + " costroid.yml"; !slices.Contains(printedCommands(t, stdout), want) {
					t.Errorf("the commands left do not add the connect workflow: %v", printedCommands(t, stdout))
				}
			})
		}
	}

	t.Run("no --platform-url", func(t *testing.T) {
		root := t.TempDir()
		if code, _, stderr := runInit(t, root, releaseBuild, costroid.Recipes, "github", "--agent", "claude"); code != 0 {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		if _, err := os.Stat(filepath.Join(root, connect)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("init wrote %s without --platform-url: %v", connectWorkflow, err)
		}
	})

	t.Run("a connect workflow that exists", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, connect), "a team's own connect job\n")
		before := tree(t, root)
		code, stdout, stderr := runInit(t, root, releaseBuild, costroid.Recipes, "github", "--agent", "claude", "--platform-url", "https://app.costroid.com")
		if code != exitFailure || stdout != "" || !strings.Contains(stderr, "costroid: init: "+connectWorkflow+" exists: pass --force to replace it\n") {
			t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		if !reflect.DeepEqual(before, tree(t, root)) {
			t.Error("init wrote a file before it refused")
		}
		code, _, stderr = runInit(t, root, releaseBuild, costroid.Recipes, "github", "--agent", "claude", "--platform-url", "https://app.costroid.com", "--force")
		if code != 0 || !strings.Contains(readFile(t, filepath.Join(root, connect)), "costroid connect --platform-url") {
			t.Errorf("--force: exit %d, stderr %q", code, stderr)
		}
	})

	t.Run("--out names its path", func(t *testing.T) {
		root := t.TempDir()
		code, _, stderr := runInit(t, root, releaseBuild, costroid.Recipes, "github", "--agent", "claude", "--platform-url", "https://app.costroid.com", "--out", connectWorkflow)
		if code != exitFailure || !strings.Contains(stderr, "names the workflow --platform-url writes to bind the owner") || len(tree(t, root)) != 0 {
			t.Errorf("exit %d, stderr %q", code, stderr)
		}
	})

	t.Run("--existing writes nothing", func(t *testing.T) {
		existingPatch(t, existingReview, "--platform-url", "https://app.costroid.com")
	})
}

func TestInitSkillsWritesBothSkillFiles(t *testing.T) {
	skills := []string{".agents/skills/costroid/SKILL.md", ".claude/skills/costroid/SKILL.md"}
	root := t.TempDir()
	code, stdout, stderr := runInit(t, root, releaseBuild, costroid.Recipes, "github", "--agent", "codex", "--skills")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	for _, name := range skills {
		if got := readFile(t, filepath.Join(root, name)); got != costroidSkill {
			t.Errorf("%s holds:\n%s", name, got)
		}
		if !strings.Contains(stdout, "costroid: wrote "+name+", the skill that reads what a run cost\n") {
			t.Errorf("init does not say it wrote %s:\n%s", name, stdout)
		}
	}
	want := "git add .github/workflows/costroid.yml costroid.yml " + strings.Join(skills, " ")
	if !slices.Contains(printedCommands(t, stdout), want) {
		t.Errorf("the commands left do not add the skills: %v", printedCommands(t, stdout))
	}

	edited := filepath.Join(root, skills[1])
	writeFile(t, edited, "a team's own skill\n")
	code, stdout, stderr = runInit(t, root, releaseBuild, costroid.Recipes, "github", "--agent", "codex", "--skills", "--force")
	if code != 0 {
		t.Fatalf("again: exit %d, stderr %q", code, stderr)
	}
	if got := readFile(t, edited); got != "a team's own skill\n" || !strings.Contains(stdout, "costroid: "+skills[1]+" exists, and init left it as it is\n") {
		t.Errorf("init replaced a skill file that exists: %q\n%s", got, stdout)
	}

	code, _, stderr = runInit(t, t.TempDir(), releaseBuild, costroid.Recipes, "github", "--agent", "claude-code-action", "--existing", "review.yml", "--skills")
	if code != exitFailure || !strings.Contains(stderr, "--existing writes nothing, so --pr and --skills do not apply") {
		t.Errorf("--existing --skills: exit %d, stderr %q", code, stderr)
	}
}

func TestSkillFrontMatterNamesAndDescribes(t *testing.T) {
	front, body, found := strings.Cut(strings.TrimPrefix(costroidSkill, "---\n"), "\n---\n")
	if !strings.HasPrefix(costroidSkill, "---\n") || !found {
		t.Fatalf("the skill opens with no front matter:\n%s", costroidSkill)
	}
	var matter map[string]string
	if err := yaml.Unmarshal([]byte(front), &matter); err != nil {
		t.Fatal(err)
	}
	if keys := slices.Sorted(maps.Keys(matter)); !slices.Equal(keys, []string{"description", "name"}) {
		t.Errorf("the front matter's keys are %v, want name and description", keys)
	}
	for _, name := range skillPaths {
		if directory := path.Base(path.Dir(name)); matter["name"] != directory {
			t.Errorf("the skill's name is %q, and %s is in %s", matter["name"], name, directory)
		}
	}
	if !regexp.MustCompile(`^[a-z0-9-]{1,64}$`).MatchString(matter["name"]) {
		t.Errorf("the name %q is not 1 to 64 lower-case letters, digits and hyphens", matter["name"])
	}
	description := matter["description"]
	if len(description) == 0 || len(description) > 1024 {
		t.Errorf("the description is %d bytes, want 1 to 1024", len(description))
	}
	for _, says := range []string{"status file", "how the costroid check concluded", "never estimates", "never reads the pull request comment"} {
		if !strings.Contains(description, says) {
			t.Errorf("the description does not say %q: %s", says, description)
		}
	}
	for _, command := range []string{"costroid result --pr NUMBER --repo OWNER/NAME --json", `cat "${COSTROID_STATUS_FILE:-$RUNNER_TEMP/costroid-status.json}"`} {
		if !strings.Contains(body, "    "+command+"\n") {
			t.Errorf("the skill does not show %s", command)
		}
	}
}

// TestSkillsPassTheClaudeValidator runs Claude Code's own validator, with
// warnings as errors, on the skills init writes into a directory under the
// system's temporary directory. It needs claude on PATH.
func TestSkillsPassTheClaudeValidator(t *testing.T) {
	claude, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude is not on PATH")
	}
	root := t.TempDir()
	if code, _, stderr := runInit(t, root, releaseBuild, costroid.Recipes, "github", "--agent", "claude", "--skills"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	for _, profile := range [][]string{nil, {"--strict"}} {
		command := exec.Command(claude, append(append([]string{"plugin", "validate"}, profile...), filepath.Join(root, ".claude", "skills"))...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Errorf("claude plugin validate %v: %v\n%s", profile, err, output)
		}
	}
}

// claudeStep is a claude-code-action step whose with block ends in
// claude_args, which each case of TestExistingAddsMaxBudgetToClaudeArgs
// fills.
const claudeStep = `name: review

on:
  pull_request:

permissions: {}

jobs:
  review:
    runs-on: ubuntu-24.04
    permissions:
      contents: read
    steps:
      - uses: anthropics/claude-code-action@1d6de8cb0c237e7c15e9e1bdf973826ebae490cc # v1.0.248
        with:
          anthropic_api_key: ${{ secrets.ANTHROPIC_API_KEY }}
          prompt: Review this pull request.
`

func TestExistingAddsMaxBudgetToClaudeArgs(t *testing.T) {
	for _, test := range []struct {
		label, workflow string
		// args is the step's claude_args after the patch, and budget what
		// the read step and the report job are told; "" means no cap.
		args, budget string
		changed      bool
	}{
		{"no claude_args", claudeStep, "--max-budget-usd 5", "5", true},
		{"no with", strings.Replace(claudeStep, "        with:\n          anthropic_api_key: ${{ secrets.ANTHROPIC_API_KEY }}\n          prompt: Review this pull request.\n", "        env:\n          ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}\n", 1), "--max-budget-usd 5", "5", true},
		{"one line", claudeStep + "          claude_args: --max-turns 10\n", "--max-turns 10 --max-budget-usd 5", "5", true},
		{"one line with a comment", claudeStep + "          claude_args: --max-turns 10 # turns\n", "--max-turns 10 --max-budget-usd 5", "5", true},
		{"one line, no final newline", claudeStep + "          claude_args: --max-turns 10", "--max-turns 10 --max-budget-usd 5", "5", true},
		{"quoted", claudeStep + "          claude_args: \"--model 'claude-opus-4-1'\"\n", "--model 'claude-opus-4-1' --max-budget-usd 5", "5", true},
		{"a literal block", claudeStep + "          claude_args: |\n            --max-turns 10\n            # a comment\n            --model claude-opus-4-1\n", "--max-turns 10\n# a comment\n--model claude-opus-4-1\n--max-budget-usd 5\n", "5", true},
		{"a folded block", claudeStep + "          claude_args: >-\n            --max-turns 10\n            --model claude-opus-4-1\n", "--max-turns 10 --model claude-opus-4-1 --max-budget-usd 5", "5", true},
		{"its own cap", claudeStep + "          claude_args: --max-budget-usd 2.50 --max-turns 10\n", "--max-budget-usd 2.50 --max-turns 10", "2.50", false},
		{"two caps, the last one holds", claudeStep + "          claude_args: --max-budget-usd 9 --max-budget-usd=3\n", "--max-budget-usd 9 --max-budget-usd=3", "3", false},
		{"its own cap as an expression", claudeStep + "          claude_args: --max-budget-usd ${{ env.CAP }}\n", "--max-budget-usd ${{ env.CAP }}", "${{ env.CAP }}", false},
		{"one expression", claudeStep + "          claude_args: ${{ inputs.claude-args }}\n", "${{ inputs.claude-args }}", "", false},
		{"a cap behind a comment", claudeStep + "          claude_args: |\n            # --max-budget-usd 9\n            --max-turns 10\n", "# --max-budget-usd 9\n--max-turns 10\n--max-budget-usd 5\n", "5", true},
	} {
		t.Run(test.label, func(t *testing.T) {
			patched, stderr := patchWorkflow(t, test.workflow, "--agent", "claude-code-action")
			steps := decodeYAML(t, patched)["jobs"].(map[string]any)["review"].(map[string]any)["steps"].([]any)
			with, _ := steps[0].(map[string]any)["with"].(map[string]any)
			if with["claude_args"] != test.args {
				t.Errorf("claude_args %q, want %q", with["claude_args"], test.args)
			}
			written := writtenSteps(t, patched)
			flat := strings.Join(strings.Fields(stderr), " ")
			switch {
			case test.budget == "":
				if _, stated := written["read"]["budget-usd"]; stated || written["report"]["budget-usd"] != nil {
					t.Errorf("a cap is stated where none can be read:\n%s", patched)
				}
				if !strings.Contains(flat, "so the patch states no cap") {
					t.Errorf("init does not say the patch states no cap: %s", flat)
				}
			case written["read"]["budget-usd"] != test.budget || written["read"]["claude-args"] != test.args || written["report"]["budget-usd"] != test.budget:
				t.Errorf("read %v, report %v, want budget-usd %q and claude-args %q", written["read"], written["report"], test.budget, test.args)
			}
			if adds := strings.Contains(flat, "The patch adds --max-budget-usd 5 to the claude_args"); adds != test.changed {
				t.Errorf("init says it adds the cap: %v, want %v: %s", adds, test.changed, flat)
			}
			if rules := lintRules("review.yml", patched); test.budget != "" && len(rules) > 0 {
				t.Errorf("costroid lint on the patched workflow: %v", rules)
			}
			if test.changed && strings.Contains(test.workflow, "# turns") && !strings.Contains(patched, "--max-budget-usd 5 # turns\n") {
				t.Errorf("the patch drops the line's comment:\n%s", patched)
			}
		})
	}
}

// TestExistingTakesTheBudgetFlag holds --budget-usd: the amount the patch adds
// to claude_args that hold no cap and gives the read step and the report job,
// USD 5 without it, and refused where it cannot apply.
func TestExistingTakesTheBudgetFlag(t *testing.T) {
	for _, test := range []struct {
		label, workflow string
		flag            []string
		args, budget    string
	}{
		{"no flag", claudeStep, nil, "--max-budget-usd 5", "5"},
		{"no claude_args", claudeStep, []string{"--budget-usd", "2.50"}, "--max-budget-usd 2.50", "2.50"},
		{"one line", claudeStep + "          claude_args: --max-turns 10\n", []string{"--budget-usd", "12"}, "--max-turns 10 --max-budget-usd 12", "12"},
		{"a literal block", claudeStep + "          claude_args: |\n            --max-turns 10\n", []string{"--budget-usd=0.75"}, "--max-turns 10\n--max-budget-usd 0.75\n", "0.75"},
	} {
		t.Run(test.label, func(t *testing.T) {
			patched, stderr := patchWorkflow(t, test.workflow, append([]string{"--agent", "claude-code-action"}, test.flag...)...)
			steps := decodeYAML(t, patched)["jobs"].(map[string]any)["review"].(map[string]any)["steps"].([]any)
			if with := steps[0].(map[string]any)["with"].(map[string]any); with["claude_args"] != test.args {
				t.Errorf("claude_args %q, want %q", with["claude_args"], test.args)
			}
			written := writtenSteps(t, patched)
			if written["read"]["budget-usd"] != test.budget || written["read"]["claude-args"] != test.args || written["report"]["budget-usd"] != test.budget {
				t.Errorf("read %v, report %v, want budget-usd %q", written["read"], written["report"], test.budget)
			}
			said := "The patch adds --max-budget-usd " + test.budget + " to the claude_args of the claude-code-action step, so Claude Code stops a run once its own estimate passes USD " + test.budget + ","
			if flat := strings.Join(strings.Fields(stderr), " "); !strings.Contains(flat, said) {
				t.Errorf("init does not say it adds USD %s: %s", test.budget, flat)
			}
			if rules := lintRules("review.yml", patched); len(rules) > 0 {
				t.Errorf("costroid lint on the patched workflow: %v", rules)
			}
		})
	}

	t.Run("a step that holds its own cap", func(t *testing.T) {
		patched, stderr := patchWorkflow(t, claudeStep+"          claude_args: --max-budget-usd 3\n", "--agent", "claude-code-action", "--budget-usd", "9")
		if written := writtenSteps(t, patched); written["read"]["budget-usd"] != "3" || written["report"]["budget-usd"] != "3" {
			t.Errorf("read %v, report %v, want the step's own cap", written["read"], written["report"])
		}
		if flat := strings.Join(strings.Fields(stderr), " "); !strings.Contains(flat, "--budget-usd 9 is not used, because init adds a cap only to claude_args it can read that hold none.") {
			t.Errorf("init does not say --budget-usd is not used: %s", flat)
		}
	})

	t.Run("switched to --existing", func(t *testing.T) {
		root := t.TempDir()
		name := workflowDir + "/claude.yml"
		writeFile(t, filepath.Join(root, name), claudeStep)
		code, patch, stderr := runInit(t, root, releaseBuild, costroid.Recipes, "github", "--budget-usd", "4")
		if code != 0 {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		if written := writtenSteps(t, applyDiff(t, claudeStep, patch)); written["report"]["budget-usd"] != "4" {
			t.Errorf("the report job's budget-usd is %v, want 4", written["report"]["budget-usd"])
		}
	})

	for _, refusal := range []struct {
		label, workflow string
		args            []string
		want            string
	}{
		{"zero", claudeStep, []string{"--existing", "review.yml", "--budget-usd", "0.00"}, `--budget-usd is an amount in USD above 0, such as 5 or 2.50, not "0.00"`},
		{"a negative amount", claudeStep, []string{"--existing", "review.yml", "--budget-usd", "-1"}, `not "-1"`},
		{"an expression", claudeStep, []string{"--existing", "review.yml", "--budget-usd", "${{ env.CAP }}"}, `not "${{ env.CAP }}"`},
		{"a word", claudeStep, []string{"--existing", "review.yml", "--budget-usd", "five"}, `not "five"`},
		{"an exponent", claudeStep, []string{"--existing", "review.yml", "--budget-usd", "1e3"}, `not "1e3"`},
		{"a recipe", "", []string{"--agent", "claude", "--budget-usd", "3"}, "--budget-usd sets the cap the --existing patch adds to claude_args: the recipes init writes hold their own"},
		{"codex-action", existingCodexReview, []string{"--existing", "review.yml", "--budget-usd", "3"}, "--budget-usd sets the cap init adds to the claude_args of anthropics/claude-code-action, and the step runs openai/codex-action, which takes none"},
	} {
		t.Run(refusal.label, func(t *testing.T) {
			root := t.TempDir()
			if refusal.workflow != "" {
				writeFile(t, filepath.Join(root, "review.yml"), refusal.workflow)
			}
			before := tree(t, root)
			code, stdout, stderr := runInit(t, root, releaseBuild, costroid.Recipes, append([]string{"github"}, refusal.args...)...)
			if code != exitFailure || stdout != "" || !strings.Contains(stderr, refusal.want) {
				t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
			}
			if !reflect.DeepEqual(before, tree(t, root)) {
				t.Error("init changed the repository")
			}
		})
	}
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+),(\d+) \+(\d+),(\d+) @@$`)

// applyDiff applies a unified diff of one file to original. Every context
// and removed line must match the original, and every hunk its counts.
func applyDiff(t *testing.T, original, patch string) string {
	t.Helper()
	lines, endsWithNewline := splitLines([]byte(original))
	diff := strings.Split(strings.TrimSuffix(patch, "\n"), "\n")
	if len(diff) < 3 || !strings.HasPrefix(diff[0], "--- a/") || !strings.HasPrefix(diff[1], "+++ b/") {
		t.Fatalf("not a patch of one file:\n%s", patch)
	}
	var out []string
	next, noNewline := 0, false
	for at := 2; at < len(diff); {
		header := hunkHeader.FindStringSubmatch(diff[at])
		if header == nil {
			t.Fatalf("line %d is not a hunk's header: %q", at+1, diff[at])
		}
		counts := make([]int, 4)
		for i := range counts {
			counts[i], _ = strconv.Atoi(header[i+1])
		}
		if counts[0]-1 < next || counts[0]-1 > len(lines) {
			t.Fatalf("hunk %q starts outside the file", diff[at])
		}
		out = append(out, lines[next:counts[0]-1]...)
		next = counts[0] - 1
		if counts[2] != len(out)+1 {
			t.Errorf("hunk %q starts at new line %d, want %d", diff[at], counts[2], len(out)+1)
		}
		old, added := 0, 0
		for at++; at < len(diff) && !strings.HasPrefix(diff[at], "@@ "); at++ {
			line := diff[at]
			switch {
			case line == `\ No newline at end of file`:
				noNewline = diff[at-1][0] != '-'
			case line != "" && (line[0] == ' ' || line[0] == '-'):
				if next >= len(lines) || lines[next] != line[1:] {
					t.Fatalf("line %q does not match the original's line %d", line, next+1)
				}
				if marked := at+1 < len(diff) && diff[at+1] == `\ No newline at end of file`; marked != (next == len(lines)-1 && !endsWithNewline) {
					t.Fatalf("line %q and the original's line %d disagree on whether a newline ends it", line, next+1)
				}
				if line[0] == ' ' {
					out = append(out, line[1:])
					added++
				}
				next++
				old++
			case line != "" && line[0] == '+':
				out = append(out, line[1:])
				added++
			default:
				t.Fatalf("line %q is not a diff line", line)
			}
		}
		if old != counts[1] || added != counts[3] {
			t.Errorf("hunk %q holds %d old and %d new lines", diff[2], old, added)
		}
	}
	if next < len(lines) {
		out = append(out, lines[next:]...)
	} else {
		endsWithNewline = !noNewline
	}
	text := strings.Join(out, "\n")
	if endsWithNewline {
		text += "\n"
	}
	return text
}
