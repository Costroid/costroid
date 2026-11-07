package recipelint

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const (
	gitLabRecipe = "gitlab-ci.yml"
	agentLabel   = "/(^|,)costroid-agent(,|$)/"
	reportRule   = `    - if: $CI_PIPELINE_SOURCE != "merge_request_event" || $CI_MERGE_REQUEST_LABELS !~ ` + agentLabel + "\n"
	reportStage  = "costroid:report:\n  extends: .costroid-install\n  stage: .post\n"
	commitFlag   = ` \` + "\n" + `        --certificate-github-workflow-sha "${COSTROID_COMMIT}"`
)

func gitLabTemplate(t *testing.T) (string, *yaml.Node) {
	t.Helper()
	source := read(t, filepath.Join(recipesDir, gitLabRecipe))
	documents, err := parse([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	return source, documents[0]
}

func findingRules(source string) []string {
	return rules(LintBytes(gitLabRecipe, []byte(source)).Findings)
}

// On GitLab the agent's jobs run only with the label. A report that ran without
// it would post, on every merge request, a note about a run that never started.
func TestGitLabReportFollowsTheLabelRule(t *testing.T) {
	source, root := gitLabTemplate(t)
	if labels := agentLabels(root); !slices.Equal(labels, []string{agentLabel}) {
		t.Fatalf("the agent's jobs test the labels %v", labels)
	}
	for _, job := range []string{"costroid:report", "costroid:ingest"} {
		if got := text(field(field(root, job), "rules")); !strings.Contains(got, "$CI_MERGE_REQUEST_LABELS !~ "+agentLabel) {
			t.Errorf("%s does not wait for the label: %s", job, got)
		}
	}
	reportRules := source[strings.Index(source, "costroid:report:"):]
	if !strings.Contains(reportRules, reportRule) {
		t.Fatal("the report's first rule is not the label rule")
	}
	at := strings.Index(source, "costroid:report:") + strings.Index(reportRules, reportRule)
	for name, swap := range map[string]string{
		"no label":        `    - if: $CI_PIPELINE_SOURCE != "merge_request_event"` + "\n",
		"another label":   strings.Replace(reportRule, "costroid-agent", "costroid-other", 1),
		"no label at all": strings.Replace(reportRule, "$CI_MERGE_REQUEST_LABELS", "$CI_MERGE_REQUEST_TITLE", 1),
	} {
		t.Run(name, func(t *testing.T) {
			broken := source[:at] + swap + source[at+len(reportRule):]
			if got := findingRules(broken); !slices.Contains(got, RuleReportLabel) {
				t.Fatalf("rules: %v", got)
			}
		})
	}
}

// A project whose own stages list lacks test or deploy could not create its
// pipeline. .post exists in every pipeline, and the agent's stage is one key a
// project can override.
func TestGitLabUsesPostStage(t *testing.T) {
	source, root := gitLabTemplate(t)
	if !strings.Contains(source, reportStage) {
		t.Fatal("the report's stage is not .post")
	}
	agentStage := field(field(root, ".costroid-agent"), "stage")
	if agentStage == nil {
		t.Fatal(".costroid-agent names no stage")
	}
	jobs := 0
	for _, item := range entries(root) {
		if gitLabReserved[item.key.Value] || strings.HasPrefix(item.key.Value, ".") {
			continue
		}
		jobs++
		stage := firstField(chain(root, item.value), "stage")
		switch {
		case stage == nil:
			t.Errorf("%s names no stage", item.key.Value)
		case stage.Value == ".post":
		case stage != agentStage:
			t.Errorf("%s runs in %s, which only .costroid-agent may set", item.key.Value, stage.Value)
		}
	}
	if jobs != 4 {
		t.Fatalf("read %d jobs, want 4", jobs)
	}
	for _, job := range []string{"costroid:report", "costroid:ingest"} {
		if got := scalarField(field(root, job), "stage"); got != ".post" {
			t.Errorf("%s runs in %q", job, got)
		}
	}
	for _, job := range []string{"costroid:claude", "costroid:codex"} {
		if got := firstField(chain(root, field(root, job)), "stage"); got != agentStage {
			t.Errorf("%s does not take its stage from .costroid-agent", job)
		}
	}
	snippet := includeSnippet(t)
	if local := scalarField(sequence(field(snippet, "include"))[0], "local"); local == "" {
		t.Error("the docs' include names no local file")
	}
	if field(field(snippet, ".costroid-agent"), "stage") == nil {
		t.Error("the docs' snippet does not show how to set the agent's stage")
	}
}

var yamlBlock = regexp.MustCompile("(?s)```yaml\n(.*?)```")

// includeSnippet is the YAML block in docs/recipes.md that includes the template.
func includeSnippet(t *testing.T) *yaml.Node {
	t.Helper()
	for _, block := range yamlBlock.FindAllStringSubmatch(read(t, recipesDoc), -1) {
		documents, err := parse([]byte(block[1]))
		if err != nil {
			t.Fatalf("a YAML block in docs/recipes.md does not parse: %v", err)
		}
		if len(documents) == 1 && field(documents[0], "include") != nil {
			return documents[0]
		}
	}
	t.Fatal("docs/recipes.md shows no include")
	return nil
}

// cosign checks the release's signature at its tag. A tag can be moved, so the
// template pins the tag's commit too, the commit the GitHub recipes pin.
func TestGitLabCosignBindsTheCommit(t *testing.T) {
	source, root := gitLabTemplate(t)
	variables := mergeVariables(field(root, "variables"))
	version, commit := variables["COSTROID_VERSION"], variables["COSTROID_COMMIT"]
	if !fullSHA.MatchString(commit) {
		t.Fatalf("COSTROID_COMMIT reads %q", commit)
	}
	pin := "Costroid/costroid/action@" + commit + " # v" + version
	for _, name := range []string{"github-claude-code.yml", "github-claude-code-action.yml", "github-codex.yml"} {
		if !strings.Contains(read(t, filepath.Join(recipesDir, name)), pin) {
			t.Errorf("%s does not pin %s", name, pin)
		}
	}
	if !strings.Contains(source, commitFlag) {
		t.Fatal("cosign is not given COSTROID_COMMIT")
	}
	if got := findingRules(source); len(got) != 0 {
		t.Fatalf("rules: %v", got)
	}
	for _, change := range []edit{
		{"no flag", commitFlag, "", RuleCosignCommit},
		{"a short commit", "COSTROID_COMMIT: " + commit, "COSTROID_COMMIT: " + commit[:7], RuleCosignCommit},
		{"an unset variable", `"${COSTROID_COMMIT}"`, `"${COSTROID_RELEASE_COMMIT}"`, RuleCosignCommit},
	} {
		t.Run(change.name, func(t *testing.T) {
			expectRule(t, source, change)
		})
	}
	literal := strings.Replace(source, `"${COSTROID_COMMIT}"`, commit, 1)
	if got := findingRules(literal); len(got) != 0 {
		t.Fatalf("a literal commit is refused: %v", got)
	}
}

const (
	reportExitCodes = "  allow_failure:\n    exit_codes: [3]\n"
	gateRule        = "    - if: $COSTROID_GITLAB_TOKEN && $COSTROID_GATE\n      when: always\n"
	openRule        = "    - if: $COSTROID_GITLAB_TOKEN\n      when: always\n      allow_failure: true\n"
	gateFlag        = "${COSTROID_GATE:+--exit-on-verdict}"
)

// With COSTROID_GATE set, the report's job fails the pipeline on a failure
// verdict, exit 1, and passes with a warning on a neutral one, exit 3. Without
// it, the job's rule allows every failure, and the report is not asked for an
// exit code.
func TestGitLabRecipeAllowsOnlyThree(t *testing.T) {
	source, root := gitLabTemplate(t)
	report := source[strings.Index(source, "costroid:report:"):]
	for _, want := range []string{reportExitCodes, gateRule + openRule, gateFlag} {
		if !strings.Contains(report, want) {
			t.Errorf("the report's job does not hold %q", want)
		}
	}
	job := field(root, "costroid:report")
	if codes := sequence(field(field(job, "allow_failure"), "exit_codes")); len(codes) != 1 || codes[0].Value != neutralExit {
		t.Errorf("the report's job may fail on %v", text(codes...))
	}
	if got := findingRules(source); len(got) != 0 {
		t.Fatalf("rules: %v", got)
	}
	for _, change := range []edit{
		{"exit 1 too", reportExitCodes, "  allow_failure:\n    exit_codes: [1, 3]\n", RuleReportExitCodes},
		{"exit 1 alone", reportExitCodes, "  allow_failure:\n    exit_codes: 1\n", RuleReportExitCodes},
		{"another code", reportExitCodes, "  allow_failure:\n    exit_codes: [3, 137]\n", RuleReportExitCodes},
		{"no code", reportExitCodes, "  allow_failure:\n    exit_codes: []\n", RuleReportExitCodes},
		{"a nested list", reportExitCodes, "  allow_failure:\n    exit_codes: [[3]]\n", RuleReportExitCodes},
	} {
		t.Run(change.name, func(t *testing.T) {
			expectRule(t, source, change)
		})
	}
	scalar := strings.Replace(source, reportExitCodes, "  allow_failure:\n    exit_codes: 3\n", 1)
	if got := findingRules(scalar); len(got) != 0 {
		t.Fatalf("exit_codes: 3 is refused: %v", got)
	}
}
