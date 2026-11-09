package recipelint

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/Costroid/costroid/internal/run"
)

var sdkRecipes = []string{"github-claude-agent-sdk.yml", "github-codex-sdk.yml"}

// A step that runs costroid run --agent starts the agent it names, however
// the option is written, so every agent-job rule holds its job. Both SDK
// recipes pass those rules with nothing to note, and each rule still bites
// them when it is broken.
func TestRunAgentStepIsAnAgentJob(t *testing.T) {
	if !slices.Equal(runAgents, slices.Sorted(slices.Values(run.Agents))) {
		t.Fatalf("lint knows the --agent values %v, and costroid run takes %v", runAgents, run.Agents)
	}
	for name, test := range map[string]struct{ script, agent string }{
		"the Agent SDK":         {"costroid run --agent claude-code-sdk --budget-usd 5 --out r -- node agent/review.mjs", claudeSDKAgent},
		"the Codex SDK, with =": {"costroid run --budget-usd 5 --agent=codex-sdk --out r -- node agent/review.mjs", codexSDKAgent},
		"a continued line":      {"costroid run --budget-usd 5 \\\n  --agent \"codex-sdk\" --out r -- \\\n  node agent/review.mjs", codexSDKAgent},
		"npx":                   {"costroid run --agent claude-code --budget-usd 5 --out r -- npx -y @anthropic-ai/claude-code@2.1.316 -p hi", claudeCodeAgent},
		"after another command": {"mkdir -p out && costroid run --agent codex --budget-usd 5 --out r -- npx -y @openai/codex@0.164.0 exec hi", codexAgent},
	} {
		t.Run(name, func(t *testing.T) {
			if got := stepAgent(scriptStep(t, test.script)); got != test.agent {
				t.Fatalf("stepAgent = %q, want %q", got, test.agent)
			}
			if !slices.Contains(rules(LintBytes("x.yml", []byte(idTokenJob(test.script))).Findings), RuleAgentIDToken) {
				t.Errorf("a job that holds id-token: write and runs %q draws no %s", test.script, RuleAgentIDToken)
			}
		})
	}
	for name, script := range map[string]string{
		"the program's own option": "costroid run --budget-usd 5 --out r -- node agent/review.mjs --agent codex-sdk",
		"another command's option": "costroid report --agent codex-sdk; node agent/review.mjs",
		"after a shell operator":   "costroid run --budget-usd 5 --out r -- true; echo --agent codex-sdk",
	} {
		t.Run(name, func(t *testing.T) {
			if got := stepAgent(scriptStep(t, script)); got != "" {
				t.Fatalf("stepAgent = %q for %q, want no agent", got, script)
			}
		})
	}

	unknown := withRule(LintBytes("x.yml", []byte(idTokenJob("costroid run --agent aider --budget-usd 5 --out r -- aider"))).Findings, RuleUnknownAgent)
	if len(unknown) != 1 || !strings.Contains(unknown[0].Message, `"aider" is not one Costroid runs: it runs claude-code, claude-code-sdk, codex, codex-sdk or opencode`) {
		t.Errorf("costroid run --agent aider: %v", unknown)
	}
	action := LintBytes("x.yml", []byte(strings.Replace(goodWorkflow, "agent: claude", "agent: "+claudeSDKAgent, 1))).Findings
	if len(action) != 1 || action[0].Rule != RuleUnknownAgent || !strings.Contains(action[0].Message, "it runs claude or codex") {
		t.Errorf("the action's agent input %s: %v", claudeSDKAgent, action)
	}

	for _, name := range sdkRecipes {
		source := read(t, filepath.Join(recipesDir, name))
		t.Run(name, func(t *testing.T) {
			recipe := LintBytes(name, []byte(source))
			if len(recipe.Findings) != 0 || len(recipe.Notes) != 0 {
				t.Fatalf("lint-recipes: findings %v, notes %v", recipe.Findings, recipe.Notes)
			}
			customer := LintCustomer(name, []byte(source))
			if customer.Agents != 1 || len(customer.Findings) != 0 || len(customer.Notes) != 0 {
				t.Fatalf("lint: %d agents, findings %v, notes %v", customer.Agents, customer.Findings, customer.Notes)
			}
		})
		const lockdown = "    permissions:\n      contents: read\n"
		edits := []edit{
			{"id-token", lockdown, lockdown + "      id-token: write\n", RuleAgentIDToken},
			{"more access", lockdown, lockdown + "      pull-requests: write\n", RuleAgentPermissions},
			{"forks", "github.event.pull_request.head.repo.full_name == github.repository\n      && ", "", RuleAgentGuard},
			{"no lock file", "npm ci --ignore-scripts", "npm install --ignore-scripts", RuleAgentVersion},
			{"the title", "          COSTROID_ROWS:", "          TITLE: ${{ github.event.pull_request.title }}\n          COSTROID_ROWS:", RuleAgentPromptInput},
			{"credentials kept", "persist-credentials: false", "persist-credentials: true", RuleAgentCheckoutCredentials},
		}
		if name == "github-codex-sdk.yml" {
			edits = append(edits, edit{"prompts in telemetry", "log_user_prompt = false", "log_user_prompt = true", RuleAgentContentFlags})
		} else {
			edits = append(edits, edit{"API bodies in telemetry", `OTEL_LOG_RAW_API_BODIES: "0"`, `OTEL_LOG_RAW_API_BODIES: "1"`, RuleAgentContentFlags})
		}
		for _, change := range edits {
			t.Run(name+"/"+change.name, func(t *testing.T) {
				expectRule(t, source, change)
			})
		}
	}
}

// A copy of either SDK recipe whose harden-runner records calls and blocks
// none draws the egress note under both profiles, and nothing else.
func TestSDKRecipeInAuditModeDrawsTheEgressNote(t *testing.T) {
	for _, name := range sdkRecipes {
		t.Run(name, func(t *testing.T) {
			source := read(t, filepath.Join(recipesDir, name))
			if strings.Count(source, "egress-policy: block") != 1 {
				t.Fatalf("%s does not hold one step in block mode", name)
			}
			audit := []byte(strings.Replace(source, "egress-policy: block", "egress-policy: audit", 1))
			recipe := LintBytes(name, audit)
			if len(recipe.Findings) != 0 || !slices.Equal(rules(recipe.Notes), []string{RuleEgressAudit}) {
				t.Errorf("lint-recipes: findings %v, notes %v", recipe.Findings, recipe.Notes)
			}
			customer := LintCustomer(name, audit)
			if customer.Agents != 1 || len(customer.Findings) != 0 || !slices.Equal(rules(customer.Notes), []string{RuleEgressAudit}) {
				t.Fatalf("lint: %d agents, findings %v, notes %v", customer.Agents, customer.Findings, customer.Notes)
			}
			if note := customer.Notes[0]; note.Severity != Note || !strings.HasSuffix(note.Message, "set egress-policy: block and list the hosts the job reaches in allowed-endpoints") {
				t.Errorf("the note: %s", note)
			}
		})
	}
}

// withRule keeps the findings of one rule.
func withRule(findings []Finding, rule string) []Finding {
	var kept []Finding
	for _, finding := range findings {
		if finding.Rule == rule {
			kept = append(kept, finding)
		}
	}
	return kept
}

// scriptStep is a step that runs script.
func scriptStep(t *testing.T, script string) *yaml.Node {
	t.Helper()
	documents, err := parse([]byte("run: |\n" + indent(script, "  ")))
	if err != nil {
		t.Fatal(err)
	}
	return documents[0]
}

// idTokenJob is a pull request workflow whose one job holds id-token: write
// and runs script.
func idTokenJob(script string) string {
	return "on: pull_request\njobs:\n  x:\n    runs-on: ubuntu-24.04\n    permissions:\n      id-token: write\n    steps:\n      - run: |\n" + indent(script, "          ")
}

// indent puts margin before each line of text, and ends it with a line break.
func indent(text, margin string) string {
	return margin + strings.ReplaceAll(text, "\n", "\n"+margin) + "\n"
}
