package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
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
	var stdout, stderr bytes.Buffer
	code := initRepository(root, build, recipes, args, &stdout, &stderr)
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

// tree maps every file under root to its content.
func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
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
		shipped := strings.Split(string(shippedRecipe(t, recipe)), "\n")
		got := strings.Split(written, "\n")
		if len(got) != len(shipped) {
			t.Fatalf("init wrote %d lines, recipes/%s has %d", len(got), recipe, len(shipped))
		}
		for i := range got {
			if got[i] != shipped[i] && !pinLine.MatchString(shipped[i]) {
				t.Errorf("line %d: init wrote %q where recipes/%s has %q", i+1, got[i], recipe, shipped[i])
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
// schema: every key, at its depth, commented out.
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
		root := t.TempDir()
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
			if reportJob["needs"] != "review" || reportJob["steps"].([]any)[0].(map[string]any)["uses"] != pinned {
				t.Errorf("the report job: %v", reportJob)
			}
			if test.stepID == addedStepID {
				delete(claude, "id")
			}
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
// back.
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
	if !reflect.DeepEqual(decodeYAML(t, patched), decodeYAML(t, example)) {
		t.Errorf("the patched workflow differs from the page's example:\n%s", patched)
	}
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
