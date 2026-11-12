package recipelint

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/Costroid/costroid/internal/cicontext"
)

// joinAttributeLine is the line the two action recipes set, cut at its value.
const joinAttributeLine = "          OTEL_RESOURCE_ATTRIBUTES: "

// actionRecipes start their agent through its own action, so costroid run
// does not start it.
var actionRecipes = []string{"github-claude-code-action.yml", "github-codex-action.yml", "github-claude-code-action-infrastructure.yml"}

// joinFindings lists what a recipe lint says under RuleJoinAttributes.
func joinFindings(findings []Finding) []Finding {
	return slices.DeleteFunc(slices.Clone(findings), func(finding Finding) bool { return finding.Rule != RuleJoinAttributes })
}

// TestJoinAttributes holds each agent that costroid run does not start to the
// six join attributes, and leaves alone each agent that costroid run starts.
func TestJoinAttributes(t *testing.T) {
	t.Run("each key dropped from an action recipe", func(t *testing.T) {
		for _, name := range actionRecipes {
			source := read(t, filepath.Join(recipesDir, name))
			if joinFindings(LintBytes(name, []byte(source)).Findings) != nil {
				t.Fatalf("%s: the shipped recipe fails the rule", name)
			}
			for _, key := range joinAttributes {
				pair := regexp.MustCompile(regexp.QuoteMeta(key) + `=[^,\n]*,?`)
				if len(pair.FindAllString(source, -1)) != 1 {
					t.Fatalf("%s sets %s %d times, want 1", name, key, len(pair.FindAllString(source, -1)))
				}
				found := joinFindings(LintBytes(name, []byte(pair.ReplaceAllString(source, ""))).Findings)
				if len(found) != 1 || !strings.Contains(found[0].Message, " must set "+key+" in OTEL_RESOURCE_ATTRIBUTES") {
					t.Errorf("%s without %s: %v", name, key, found)
				}
			}
		}
	})
	t.Run("the variable dropped from an action recipe", func(t *testing.T) {
		for _, name := range actionRecipes {
			source := read(t, filepath.Join(recipesDir, name))
			line := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(joinAttributeLine) + `.*\n`)
			edited := line.ReplaceAllString(source, "")
			if edited == source {
				t.Fatalf("%s sets no %s", name, cicontext.ResourceAttributesVariable)
			}
			found := joinFindings(LintBytes(name, []byte(edited)).Findings)
			if len(found) != 1 || !strings.HasSuffix(found[0].Message, "and vcs.repository.url.full in OTEL_RESOURCE_ATTRIBUTES, or its telemetry names no pull request: docs/adapters.md#join-attributes lists the values") {
				t.Errorf("%s: %v", name, found)
			}
			if found := joinFindings(LintCustomer(name, []byte(edited)).Notes); len(found) != 1 || found[0].Severity != Note {
				t.Errorf("%s under the customer profile: notes %v", name, found)
			}
			if found := joinFindings(LintCustomer(name, []byte(edited)).Findings); len(found) != 0 {
				t.Errorf("%s under the customer profile fails on %v", name, found)
			}
		}
	})
	t.Run("a key with no value", func(t *testing.T) {
		source := read(t, filepath.Join(recipesDir, actionRecipes[0]))
		edited := strings.Replace(source, "vcs.change.id=${{ github.event.pull_request.number }}", "vcs.change.id=", 1)
		if edited == source {
			t.Fatal("the edit changed nothing")
		}
		if found := joinFindings(LintBytes("x.yml", []byte(edited)).Findings); len(found) != 1 {
			t.Fatalf("findings: %v", found)
		}
	})
	t.Run("the job's env holds them", func(t *testing.T) {
		source := read(t, filepath.Join(recipesDir, actionRecipes[0]))
		line := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(joinAttributeLine) + `.*\n`).FindString(source)
		edited := strings.Replace(source, line, "", 1)
		moved := strings.Replace(edited, "    timeout-minutes: 30\n", "    timeout-minutes: 30\n    env:\n"+strings.TrimPrefix(line, "    "), 1)
		if line == "" || moved == edited || joinFindings(LintBytes("x.yml", []byte(edited)).Findings) == nil {
			t.Fatal("the edit moved nothing")
		}
		edited = moved
		if found := joinFindings(LintBytes("x.yml", []byte(edited)).Findings); found != nil {
			t.Fatalf("findings: %v", found)
		}
	})
	for name, test := range map[string]struct {
		step string
		want int
	}{
		"claude in a script":          {"      - run: claude -p hello\n", 1},
		"codex in a script":           {"      - run: codex exec hello\n", 1},
		"claude beside costroid run":  {"      - run: |\n          costroid run --budget-usd 1 --out r -- codex exec hello\n          claude -p hello\n", 1},
		"claude under costroid run":   {"      - run: costroid run --budget-usd 1 --out r -- claude -p hello\n", 0},
		"a command continued":         {"      - run: |\n          costroid run --budget-usd 1 --out r -- \\\n            claude -p hello\n", 0},
		"costroid run by its --agent": {"      - run: costroid run --agent claude-code-sdk --budget-usd 1 --out r -- node review.mjs\n", 0},
		"Costroid's action":           {"      - uses: Costroid/costroid/action@1111111111111111111111111111111111111111 # v1.0.0\n        with:\n          stage: run\n", 0},
	} {
		t.Run(name, func(t *testing.T) {
			source := "on: pull_request\npermissions: {}\njobs:\n  x:\n    runs-on: ubuntu-24.04\n    permissions:\n      contents: read\n    steps:\n" + test.step
			if found := joinFindings(LintBytes("x.yml", []byte(source)).Findings); len(found) != test.want {
				t.Fatalf("findings: %v", found)
			}
		})
	}
	t.Run("gitlab", func(t *testing.T) {
		const wrapped = `    - costroid run --budget-usd 5 --out rows.jsonl -- claude -p "$PROMPT"` + "\n"
		const direct = `    - claude -p "$PROMPT"` + "\n"
		outside := strings.Replace(goodGitLab, wrapped, direct, 1)
		if outside == goodGitLab {
			t.Fatal("the edit changed nothing")
		}
		if found := joinFindings(LintBytes("x.yml", []byte(goodGitLab)).Findings); found != nil {
			t.Fatalf("under costroid run: %v", found)
		}
		if found := joinFindings(LintBytes("x.yml", []byte(outside)).Findings); len(found) != 1 || !strings.Contains(found[0].Message, "so its job must set") {
			t.Fatalf("outside costroid run: %v", found)
		}
		const variables = "  variables:\n    MINIMUM_AGENT_VERSION: 2.1.316\n"
		set := strings.Replace(outside, variables, variables+"    OTEL_RESOURCE_ATTRIBUTES: cicd.pipeline.run.id=$CI_PIPELINE_ID,cicd.pipeline.task.run.id=$CI_JOB_ID,vcs.change.id=$CI_MERGE_REQUEST_IID,vcs.ref.base.revision=$CI_MERGE_REQUEST_DIFF_BASE_SHA,vcs.ref.head.revision=$CI_COMMIT_SHA,vcs.repository.url.full=$CI_PROJECT_URL\n", 1)
		if found := joinFindings(LintBytes("x.yml", []byte(set)).Findings); set == outside || found != nil {
			t.Fatalf("with the variable: %v", found)
		}
	})
	t.Run("the docs show the recipes' line", func(t *testing.T) {
		line := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(joinAttributeLine) + `.*$`).FindString(read(t, filepath.Join(recipesDir, actionRecipes[0])))
		value := strings.TrimPrefix(line, joinAttributeLine)
		for _, doc := range []string{"../../docs/adapters.md", existingWorkflowMD} {
			if !strings.Contains(read(t, doc), "  OTEL_RESOURCE_ATTRIBUTES: "+value+"\n") {
				t.Errorf("%s does not show the recipes' value", doc)
			}
		}
		for _, name := range actionRecipes[1:] {
			if !strings.Contains(read(t, filepath.Join(recipesDir, name)), line+"\n") {
				t.Errorf("%s sets another value than %s", name, actionRecipes[0])
			}
		}
	})
	t.Run("costroid run's agents add nothing", func(t *testing.T) {
		for _, file := range recipeFiles(t) {
			if slices.Contains(actionRecipes, filepath.Base(file)) {
				continue
			}
			if source := read(t, file); strings.Contains(source, cicontext.ResourceAttributesVariable) {
				t.Errorf("%s sets %s, which costroid run sets for its agent", filepath.Base(file), cicontext.ResourceAttributesVariable)
			}
		}
	})
}

// recipeExpression is one ${{ }} expression in a workflow.
var recipeExpression = regexp.MustCompile(`\$\{\{\s*(.*?)\s*\}\}`)

// TestJoinAttributesMatchCicontext evaluates the attributes each action recipe
// sets on a pull request run, and holds them to what cicontext gives an agent
// that costroid run starts in the same run, byte for byte. An expression the
// table below does not hold fails, so no value comes from a secret or from a
// pull request's text.
func TestJoinAttributesMatchCicontext(t *testing.T) {
	const (
		head   = "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d"
		base   = "1111111111111111111111111111111111111111"
		server = "https://github.example.com"
		repo   = "Octo-Org/Hello.World"
		runID  = "8812345678"
	)
	event := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(event, []byte(`{"pull_request":{"number":42,"head":{"sha":"`+head+`"},"base":{"sha":"`+base+`"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, name := range actionRecipes {
		root := parseWorkflow(t, filepath.Join(recipesDir, name))
		for _, job := range entries(field(root, "jobs")) {
			for _, step := range sequence(field(job.value, "steps")) {
				if stepAgentOutsideRun(step) == "" {
					continue
				}
				env := mergeVariables(field(root, "env"), field(job.value, "env"), field(step, "env"))
				contexts := map[string]string{
					"github.run_id":                      runID,
					"github.job":                         job.key.Value,
					"github.event.pull_request.number":   "42",
					"github.event.pull_request.base.sha": base,
					"github.event.pull_request.head.sha": head,
					"github.server_url":                  server,
					"github.repository":                  repo,
				}
				got := recipeExpression.ReplaceAllStringFunc(env[cicontext.ResourceAttributesVariable], func(expression string) string {
					value, known := contexts[recipeExpression.FindStringSubmatch(expression)[1]]
					if !known {
						t.Errorf("%s: %s is not a value cicontext reads", name, expression)
					}
					return value
				})
				variables := map[string]string{
					"GITHUB_ACTIONS": "true", "GITHUB_SERVER_URL": server, "GITHUB_REPOSITORY": repo,
					"GITHUB_RUN_ID": runID, "GITHUB_JOB": job.key.Value, "GITHUB_SHA": base,
					"GITHUB_EVENT_NAME": "pull_request", "GITHUB_EVENT_PATH": event,
				}
				context, err := cicontext.Read(func(key string) string { return variables[key] })
				if err != nil {
					t.Fatal(err)
				}
				want := context.ResourceAttributes("")
				if got != want {
					t.Errorf("%s sets\n%s\ncicontext gives\n%s", name, got, want)
				}
				var keys []string
				for pair := range strings.SplitSeq(want, ",") {
					key, _, _ := strings.Cut(pair, "=")
					keys = append(keys, key)
				}
				if !slices.Equal(keys, joinAttributes) {
					t.Errorf("cicontext sets %v, the rule asks for %v", keys, joinAttributes)
				}
				checked++
			}
		}
	}
	if checked != len(actionRecipes) {
		t.Fatalf("checked %d agent steps, want %d", checked, len(actionRecipes))
	}
}

func parseWorkflow(t *testing.T, path string) *yaml.Node {
	t.Helper()
	documents, err := parse([]byte(read(t, path)))
	if err != nil || len(documents) != 1 {
		t.Fatalf("%s: %d documents, %v", path, len(documents), err)
	}
	return documents[0]
}
