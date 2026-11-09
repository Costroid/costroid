package recipelint

import (
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// capFindings lists the findings and notes of RuleTwoCaps in a report.
func capFindings(report Report) []Finding {
	var found []Finding
	for _, finding := range slices.Concat(report.Findings, report.Notes) {
		if finding.Rule == RuleTwoCaps {
			found = append(found, finding)
		}
	}
	return found
}

// TestTwoDifferentCapsAreFlagged gives a shipped recipe a second cap in each
// place a cap is written, and keeps every way of writing the same cap clean.
func TestTwoDifferentCapsAreFlagged(t *testing.T) {
	const report = "          stage: report\n"
	for _, test := range []struct {
		name, file, from, to string
		flagged              bool
	}{
		{"the report step restates another cap", "github-claude-code.yml", report, report + "          budget-usd: \"2\"\n", true},
		{"the read step is told another cap", "github-claude-code-action.yml", "budget-usd: ${{ env.COSTROID_BUDGET_USD }}\n", "budget-usd: \"2\"\n", true},
		{"claude_args hold another cap", "github-claude-code-action.yml", "claude_args: --max-turns 10 --max-budget-usd ${{ env.COSTROID_BUDGET_USD }}", "claude_args: --max-turns 10 --max-budget-usd 3", true},
		{"the read step's claude-args hold another cap", "github-claude-code-action.yml", "claude-args: --max-turns 10 --max-budget-usd ${{ env.COSTROID_BUDGET_USD }}", "claude-args: --max-turns 10 --max-budget-usd=3", true},
		{"the gateway key holds another cap", "github-claude-code-litellm.yml", "gateway mint --budget-usd 5", "gateway mint --budget-usd 9", true},
		{"the run holds another cap", "github-claude-code-litellm.yml", "costroid run --budget-usd 5", "costroid run --budget-usd=2", true},
		{"GitLab's agent holds another cap", "gitlab-ci.yml", `costroid run --budget-usd "$COSTROID_BUDGET_USD"`, "costroid run --budget-usd 2", true},
		{"GitLab's report restates another cap", "gitlab-ci.yml", "costroid report --rows costroid-rows.jsonl", "costroid report --rows costroid-rows.jsonl --budget-usd 9", true},
		{"the same cap written another way", "github-claude-code.yml", report, report + "          budget-usd: \"5.00\"\n", false},
		{"a cap the file does not set", "github-claude-code.yml", report, report + "          budget-usd: ${{ vars.CAP }}\n", false},
		{"the workflow's env read in the report step", "github-claude-code-action.yml", report, report + "          budget-usd: ${{ env.COSTROID_BUDGET_USD }}\n", false},
		{"another cap behind a comment", "github-claude-code-action.yml", "claude_args: --max-turns 10 --max-budget-usd ${{ env.COSTROID_BUDGET_USD }}", "claude_args: |\n            # --max-budget-usd 9\n            --max-turns 10 --max-budget-usd ${{ env.COSTROID_BUDGET_USD }}", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := read(t, filepath.Join(recipesDir, test.file))
			if !strings.Contains(source, test.from) {
				t.Fatalf("the edit's text is not in %s: %q", test.file, test.from)
			}
			edited := []byte(strings.Replace(source, test.from, test.to, 1))
			found := capFindings(LintBytes(test.file, edited))
			if flagged := len(found) == 1; flagged != test.flagged || len(found) > 1 {
				t.Fatalf("%s findings %v, want flagged %v", RuleTwoCaps, found, test.flagged)
			}
			if test.flagged && strings.HasPrefix(test.file, "github-") {
				customer := capFindings(LintCustomer(test.file, edited))
				if len(customer) != 1 || customer[0].Severity != Medium {
					t.Errorf("costroid lint: %v, want one medium %s", customer, RuleTwoCaps)
				}
			}
		})
	}

	t.Run("the finding names both caps", func(t *testing.T) {
		source := read(t, filepath.Join(recipesDir, "github-claude-code.yml"))
		found := capFindings(LintBytes("costroid.yml", []byte(strings.Replace(source, report, report+"          budget-usd: \"2\"\n", 1))))
		run := strings.Count(source[:strings.Index(source, "budget-usd: \"5\"")], "\n") + 1
		want := "budget-usd states a cap of USD 2, and budget-usd at line " + strconv.Itoa(run) + " states USD 5: state one cap, so the run, its grade and the report hold the same one"
		if len(found) != 1 || found[0].Message != want {
			t.Fatalf("got %v, want %q", found, want)
		}
	})

	t.Run("a script's variable from the step's env", func(t *testing.T) {
		workflow := capWorkflow("    needs: second\n", "          CAP: \"2\"\n")
		found := capFindings(LintCustomer("w.yml", []byte(workflow)))
		if len(found) != 1 || !strings.HasPrefix(found[0].Message, "the script states a cap of USD 2, and claude_args at line 14 states USD 5:") {
			t.Fatalf("got %v, want the script's cap of USD 2 against claude_args' USD 5", found)
		}
	})

	t.Run("agents that never share a run", func(t *testing.T) {
		workflow := capWorkflow("", "          CAP: \"2\"\n")
		if found := capFindings(LintCustomer("w.yml", []byte(workflow))); len(found) != 0 {
			t.Fatalf("got %v, want none: no needs links the two jobs", found)
		}
	})
}

// capWorkflow is a customer workflow with two agent jobs, the second holding a
// cap of USD 5 and the first a cap that its step's env names.
func capWorkflow(needs, env string) string {
	return `name: review
on:
  pull_request:
permissions: {}
jobs:
  second:
    runs-on: ubuntu-24.04
    permissions:
      contents: read
    steps:
      - uses: anthropics/claude-code-action@1d6de8cb0c237e7c15e9e1bdf973826ebae490cc # v1.0.248
        with:
          anthropic_api_key: ${{ secrets.ANTHROPIC_API_KEY }}
          claude_args: --max-budget-usd 5
          prompt: Review this pull request.
  first:
` + needs + `    runs-on: ubuntu-24.04
    permissions:
      contents: read
    steps:
      - env:
` + env + `          ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
        run: |
          costroid run --budget-usd "$CAP" --out rows.jsonl -- claude -p "Review this pull request."
`
}

// TestReportStepNeedsNoBudget holds every recipe's report to the cap the run
// recorded: no report step states one, and a recipe that caps its runs gives
// the cap to the step that runs or reads the agent, which records it.
func TestReportStepNeedsNoBudget(t *testing.T) {
	gitLabReport := regexp.MustCompile(`costroid\s+report\b[^\n]*`)
	recorded := regexp.MustCompile(`costroid\s+run\b[^\n]*--budget-usd`)
	capped := 0
	for _, file := range recipeFiles(t) {
		name := filepath.Base(file)
		source := read(t, file)
		documents, err := parse([]byte(source))
		if err != nil {
			t.Fatal(err)
		}
		root := documents[0]
		if isGitLab(root) {
			for _, line := range gitLabReport.FindAllString(source, -1) {
				if strings.Contains(line, "budget-usd") {
					t.Errorf("%s: the report states a cap: %s", name, line)
				}
			}
			if !recorded.MatchString(source) {
				t.Errorf("%s: no costroid run records the cap", name)
			}
			capped++
			continue
		}
		records, stated := false, 0
		for _, job := range entries(field(root, "jobs")) {
			stated += len(jobCaps(root, job))
			for _, step := range sequence(field(job.value, "steps")) {
				budget := field(field(step, "with"), "budget-usd")
				switch stage := costroidStage(step); {
				case stage == "report" && budget != nil:
					t.Errorf("%s:%d: the report step states budget-usd %q", name, budget.Line, budget.Value)
				case (stage == "run" || stage == "all" || stage == "read") && budget != nil:
					records = true
				}
				records = records || recorded.MatchString(scalarField(step, "run"))
			}
		}
		if records {
			capped++
		}
		if stated > 0 && !records {
			t.Errorf("%s states a cap that no run or read step records", name)
		}
	}
	if capped != 8 {
		t.Errorf("%d recipes record a cap, want 8: Claude Code, its action, its gateway, Codex, GitLab, the two SDK programs and OpenCode", capped)
	}
}

// TestLintDocNamesTwoCaps keeps the customer rule on docs/lint.md.
func TestLintDocNamesTwoCaps(t *testing.T) {
	row := regexp.MustCompile("(?m)^\\| medium +\\| `" + RuleTwoCaps + "`")
	if !row.MatchString(read(t, lintDoc)) {
		t.Errorf("docs/lint.md has no medium row for %s", RuleTwoCaps)
	}
}
