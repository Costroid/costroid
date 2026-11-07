package recipelint

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const auditStep = `      - uses: step-security/harden-runner@46b06cea07f9e64621fdf066ba77ac6915163d43 # v2.22.3
        with:
          egress-policy: audit
`

// Audit mode blocks no outbound call, so an injected agent can still send its
// key out. The recipes keep it, so it is a note and never a rejection.
func TestAuditModeWarns(t *testing.T) {
	const firstStep = "    steps:\n      - uses: actions/checkout@"
	at := strings.Index(goodWorkflow, firstStep) + len("    steps:\n")
	audit := goodWorkflow[:at] + auditStep + goodWorkflow[at:]
	report := LintBytes("x.yml", []byte(audit))
	if len(report.Findings) != 0 {
		t.Fatalf("audit mode is rejected: %v", report.Findings)
	}
	if got := rules(report.Notes); !slices.Equal(got, []string{RuleEgressAudit}) {
		t.Fatalf("notes: %v", got)
	}
	if line := report.Notes[0].Line; line != strings.Count(goodWorkflow[:at], "\n")+3 {
		t.Errorf("the note names line %d, not the policy's", line)
	}
	block := strings.Replace(audit, "egress-policy: audit", "egress-policy: block", 1)
	if report := LintBytes("x.yml", []byte(block)); len(report.Notes) != 0 || len(report.Findings) != 0 {
		t.Fatalf("block mode: notes %v, findings %v", report.Notes, report.Findings)
	}

	shipped, err := Lint([]string{recipesDir})
	if err != nil {
		t.Fatal(err)
	}
	audits := 0
	for _, file := range recipeFiles(t) {
		audits += strings.Count(read(t, file), "egress-policy: audit")
	}
	if audits == 0 || len(slices.DeleteFunc(rules(shipped.Notes), func(rule string) bool { return rule != RuleEgressAudit })) != audits {
		t.Fatalf("%d steps in audit mode, notes %v", audits, shipped.Notes)
	}
}

// A report that is not told the agent's job was skipped says the agent printed
// no usage, and that job has no log to read.
func TestReportJobsPassAgentResult(t *testing.T) {
	for _, name := range []string{"github-claude-code.yml", "github-codex.yml", "github-claude-code-litellm.yml", "github-claude-code-action.yml"} {
		t.Run(name, func(t *testing.T) {
			documents, err := parse([]byte(read(t, filepath.Join(recipesDir, name))))
			if err != nil {
				t.Fatal(err)
			}
			jobs := field(documents[0], "jobs")
			var agents []string
			for _, job := range entries(jobs) {
				if slices.ContainsFunc(sequence(field(job.value, "steps")), func(step *yaml.Node) bool { return stepAgent(step) != "" }) {
					agents = append(agents, job.key.Value)
				}
			}
			if len(agents) != 1 {
				t.Fatalf("agent jobs %v", agents)
			}
			want := "${{ needs." + agents[0] + ".result }}"
			reports := 0
			for _, job := range entries(jobs) {
				for _, step := range sequence(field(job.value, "steps")) {
					if costroidStage(step) != "report" {
						continue
					}
					reports++
					if !slices.Contains(strings.Fields(text(field(job.value, "needs"))), agents[0]) {
						t.Errorf("job %s does not need %s", job.key.Value, agents[0])
					}
					if got := scalarField(field(step, "with"), "agent-result"); got != want {
						t.Errorf("job %s passes agent-result %q, want %q", job.key.Value, got, want)
					}
				}
			}
			if reports != 1 {
				t.Fatalf("%d report steps", reports)
			}
		})
	}
}

// A reader meets the header before the budget, and each push is a new run.
func TestEachRecipeSaysEachPushRunsTheAgentAgain(t *testing.T) {
	for _, name := range []string{"github-claude-code.yml", "github-codex.yml", "github-claude-code-litellm.yml", "github-claude-code-action.yml", gitLabRecipe} {
		var header []string
		for _, line := range strings.Split(read(t, filepath.Join(recipesDir, name)), "\n") {
			comment, found := strings.CutPrefix(line, "#")
			if !found {
				break
			}
			header = append(header, comment)
		}
		if !strings.Contains(strings.Join(strings.Fields(strings.Join(header, " ")), " "), "runs the agent again") {
			t.Errorf("%s's header does not say each push runs the agent again", name)
		}
	}
	if doc := read(t, recipesDoc); strings.Count(doc, "runs the agent again") < 3 {
		t.Error("docs/recipes.md does not say, for each CI system, that each push runs the agent again")
	}
}

func TestUnknownAgentIsAFinding(t *testing.T) {
	for name, named := range map[string]string{"aider": `"aider"`, `"line\nbreak"`: `"line\nbreak"`, "Claude": `"Claude"`} {
		source := strings.Replace(goodWorkflow, "agent: claude", "agent: "+name, 1)
		findings := LintBytes("x.yml", []byte(source)).Findings
		if len(findings) != 1 || findings[0].Rule != RuleUnknownAgent {
			t.Fatalf("%s: want one %s, got %v", name, RuleUnknownAgent, findings)
		}
		message := findings[0].String()
		for _, want := range []string{named, "claude or codex"} {
			if !strings.Contains(message, want) {
				t.Errorf("%q does not name %s", message, want)
			}
		}
		if strings.ContainsAny(message, "\r\n") {
			t.Errorf("the finding spans lines: %q", message)
		}
	}
	seed := "on: push\njobs:\n  agent:\n    steps:\n      - uses: Costroid/costroid/action@v1\n        with:\n          agent: aider\n"
	if !slices.Contains(rules(LintBytes("x.yml", []byte(seed)).Findings), RuleUnknownAgent) {
		t.Error("the fuzz seed's agent is not named")
	}
}
