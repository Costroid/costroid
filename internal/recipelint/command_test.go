package recipelint

import (
	"slices"
	"strings"
	"testing"
)

const copilotAgentOption = "costroid run --agent copilot "

// copilotByCommand is goodCopilotWorkflow with no --agent: costroid run picks
// Copilot CLI by the command's name.
var copilotByCommand = strings.Replace(goodCopilotWorkflow, copilotAgentOption, "costroid run ", 1)

// goodOpenCodeWorkflow runs OpenCode under costroid run with no --agent.
const goodOpenCodeWorkflow = `on:
  pull_request:
    types: [opened, synchronize, labeled]
permissions: {}
jobs:
  agent:
    if: >-
      github.event.pull_request.head.repo.full_name == github.repository
      && (contains(fromJSON('["OWNER"]'), github.event.pull_request.author_association)
      || contains(github.event.pull_request.labels.*.name, 'costroid-agent'))
    runs-on: ubuntu-24.04
    permissions:
      contents: read
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - run: npm install --global opencode-ai@1.19.4
      - env:
          MINIMUM_AGENT_VERSION: 1.19.4
          ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
        run: |
          costroid run --budget-usd 5 --out rows.jsonl -- \
            opencode run "Read pull-request.diff."
`

// goodOpenCodeGitLab is the same job on GitLab.
const goodOpenCodeGitLab = `.base:
  image: node:24-slim@sha256:0e0ff40c39bc087845bfb27465a0df4ea419520094bc35842ff83dd8cbe6f9b6
agent:
  extends: .base
  variables:
    MINIMUM_AGENT_VERSION: 1.19.4
  rules:
    - if: $CI_MERGE_REQUEST_SOURCE_PROJECT_ID != $CI_MERGE_REQUEST_PROJECT_ID
      when: never
    - if: $CI_MERGE_REQUEST_LABELS =~ /costroid-agent/
  script:
    - npm install --global opencode-ai@1.19.4
    - costroid run --budget-usd 5 --out rows.jsonl -- opencode run "$PROMPT"
`

// A step that runs copilot -p under costroid run with no --agent starts
// Copilot CLI, as costroid run picks it by the command's name, so Copilot's
// rules on its provider and its tools hold the job on either CI system, and
// costroid lint judges the workflow. copilot -p run without costroid run is
// no agent step, as before.
func TestLintKnowsCopilotByItsCommand(t *testing.T) {
	gitlab := strings.Replace(goodCopilotGitLab, copilotAgentOption, "costroid run ", 1)
	for name, source := range map[string]string{"github": copilotByCommand, "gitlab": gitlab} {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(source, "--agent") {
				t.Fatalf("the source still names --agent")
			}
			if report := LintBytes("x.yml", []byte(source)); len(report.Findings) != 0 {
				t.Fatalf("findings: %v", report.Findings)
			}
		})
	}
	for _, script := range []string{
		"costroid run --budget-usd 5 --out r -- copilot -p hi",
		"costroid run --budget-usd 5 --out r -- /usr/local/bin/copilot -p hi",
		"costroid run --budget-usd 5 \\\n  --out r -- \\\n  'copilot' -p hi",
	} {
		if got := stepAgent(scriptStep(t, script)); got != copilotAgent {
			t.Errorf("stepAgent = %q for %q, want %s", got, script, copilotAgent)
		}
	}
	if customer := LintCustomer("x.yml", []byte(copilotByCommand)); customer.Agents != 1 {
		t.Errorf("lint judged %d agent workflows, want 1", customer.Agents)
	}
	for _, change := range []edit{
		{"copilot at latest", "@github/copilot@1.0.103\n", "@github/copilot@latest\n", RuleAgentVersion},
		{"no provider", "          COPILOT_PROVIDER_BASE_URL: https://api.anthropic.com\n", "", RuleAgentProvider},
		{"no provider type", "          COPILOT_PROVIDER_TYPE: anthropic\n", "", RuleAgentProvider},
		{"no key", "          COPILOT_PROVIDER_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}\n", "", RuleAgentProvider},
		{"a key in the file", "${{ secrets.ANTHROPIC_API_KEY }}", "sk-ant-api03-written-here", RuleAgentProvider},
		{"the shell allowed", " --deny-tool='shell'", "", RuleAgentTools},
		{"one command denied", "--deny-tool='shell'", "--deny-tool='shell(git push)'", RuleAgentTools},
	} {
		t.Run("github/"+change.name, func(t *testing.T) {
			expectRule(t, copilotByCommand, change)
		})
	}
	for _, change := range []edit{
		{"no key", "    COPILOT_PROVIDER_BEARER_TOKEN: $ANTHROPIC_API_KEY\n", "", RuleAgentProvider},
		{"the shell allowed", " --deny-tool=shell", "", RuleAgentTools},
	} {
		t.Run("gitlab/"+change.name, func(t *testing.T) {
			expectRule(t, gitlab, change)
		})
	}

	direct := strings.Replace(copilotByCommand, "costroid run --budget-usd 5 --out rows.jsonl -- \\\n", "", 1)
	direct = strings.Replace(direct, " --deny-tool='shell'", "", 1)
	if strings.Contains(direct, "costroid run") || !strings.Contains(direct, "copilot -p") {
		t.Fatalf("the direct step is not copilot -p alone:\n%s", direct)
	}
	assertNoAgentStep(t, direct)
}

// A step that runs opencode run under costroid run with no --agent starts
// OpenCode, as costroid run picks it by the command's name, so the agent's
// rules hold the job on either CI system, and costroid lint judges the
// workflow. opencode run without costroid run is no agent step, as before.
func TestLintKnowsOpenCodeByItsCommand(t *testing.T) {
	for name, source := range map[string]string{"github": goodOpenCodeWorkflow, "gitlab": goodOpenCodeGitLab} {
		t.Run(name, func(t *testing.T) {
			if report := LintBytes("x.yml", []byte(source)); len(report.Findings) != 0 {
				t.Fatalf("findings: %v", report.Findings)
			}
		})
	}
	if got := stepAgent(scriptStep(t, "costroid run --out r -- opencode run hi")); got != openCodeAgent {
		t.Errorf("stepAgent = %q, want %s", got, openCodeAgent)
	}
	if customer := LintCustomer("x.yml", []byte(goodOpenCodeWorkflow)); customer.Agents != 1 {
		t.Errorf("lint judged %d agent workflows, want 1", customer.Agents)
	}
	const lockdown = "    permissions:\n      contents: read\n"
	for _, change := range []edit{
		{"opencode at latest", "opencode-ai@1.19.4\n", "opencode-ai@latest\n", RuleAgentVersion},
		{"no minimum version", "          MINIMUM_AGENT_VERSION: 1.19.4\n", "", RuleAgentVersion},
		{"id-token", lockdown, lockdown + "      id-token: write\n", RuleAgentIDToken},
		{"more access", lockdown, lockdown + "      pull-requests: write\n", RuleAgentPermissions},
		{"forks", "github.event.pull_request.head.repo.full_name == github.repository\n      && ", "", RuleAgentGuard},
		{"the title", "          ANTHROPIC_API_KEY:", "          TITLE: ${{ github.event.pull_request.title }}\n          ANTHROPIC_API_KEY:", RuleAgentPromptInput},
	} {
		t.Run("github/"+change.name, func(t *testing.T) {
			expectRule(t, goodOpenCodeWorkflow, change)
		})
	}
	t.Run("gitlab/opencode at latest", func(t *testing.T) {
		expectRule(t, goodOpenCodeGitLab, edit{"opencode at latest", "opencode-ai@1.19.4\n", "opencode-ai@latest\n", RuleAgentVersion})
	})

	direct := strings.Replace(goodOpenCodeWorkflow, "costroid run --budget-usd 5 --out rows.jsonl -- \\\n", "", 1)
	direct = strings.Replace(direct, "opencode-ai@1.19.4", "opencode-ai@latest", 1)
	if strings.Contains(direct, "costroid run") || !strings.Contains(direct, "opencode run") {
		t.Fatalf("the direct step is not opencode run alone:\n%s", direct)
	}
	assertNoAgentStep(t, direct)
}

// A step that names --agent starts the agent it names, whatever command
// follows --, so the command's name adds no agent to it. A command no agent
// is known by, and a -- after a shell operator, start none.
func TestLintKeepsAgentFlagSteps(t *testing.T) {
	for script, want := range map[string][]string{
		"costroid run --agent codex --out r -- copilot -p hi":                 {codexAgent},
		"costroid run --agent=claude-code-sdk --out r -- opencode run hi":     {claudeSDKAgent},
		"costroid run --agent copilot --out r -- npx -y @github/copilot -p x": {copilotAgent},
		"costroid run --agent opencode --out r -- copilot -p hi":              {openCodeAgent},
		"costroid run --out r -- node agent/review.mjs":                       nil,
		"costroid run --out r; echo -- copilot -p hi":                         nil,
		"costroid run --out r -- npx -y @github/copilot@1.0.103 -p hi":        nil,
		"costroid run --out r && copilot -p hi":                               nil,
	} {
		if got := agentsNamed(script); !slices.Equal(got, want) {
			t.Errorf("agentsNamed(%q) = %q, want %q", script, got, want)
		}
	}
	if report := LintBytes("x.yml", []byte(goodCopilotWorkflow)); len(report.Findings) != 0 {
		t.Fatalf("--agent copilot: findings %v", report.Findings)
	}
	report := LintBytes("x.yml", []byte(strings.Replace(goodCopilotWorkflow, " --deny-tool='shell'", "", 1)))
	if got := withRule(report.Findings, RuleAgentTools); len(got) != 1 {
		t.Errorf("--agent copilot with the shell allowed: %v", report.Findings)
	}
	unknown := withRule(LintBytes("x.yml", []byte(idTokenJob("costroid run --agent aider --out r -- copilot -p hi"))).Findings, RuleUnknownAgent)
	if len(unknown) != 1 || !strings.Contains(unknown[0].Message, `"aider" is not one Costroid runs`) {
		t.Errorf("--agent aider before copilot: %v", unknown)
	}
}

// TestLintKeepsClaudeAndCodexCommandNames holds claude -p and codex exec under
// costroid run with no --agent to the names lint gave them before it read the
// command's name, so their findings read as they did.
func TestLintKeepsClaudeAndCodexCommandNames(t *testing.T) {
	for script, want := range map[string][]string{
		"costroid run --out r -- claude -p hi":  {claudeAgent},
		"costroid run --out r -- codex exec hi": {codexAgent},
		"claude -p hi":                          {claudeAgent},
	} {
		if got := agentsNamed(script); !slices.Equal(got, want) {
			t.Errorf("agentsNamed(%q) = %q, want %q", script, got, want)
		}
	}
}

// assertNoAgentStep holds a workflow to no agent step under either profile.
func assertNoAgentStep(t *testing.T, source string) {
	t.Helper()
	for _, finding := range LintBytes("x.yml", []byte(source)).Findings {
		if slices.Contains([]string{RuleAgentProvider, RuleAgentTools, RuleAgentVersion, RuleUnknownAgent}, finding.Rule) {
			t.Errorf("an agent's rule holds a step that runs no costroid run: %s", finding)
		}
	}
	if customer := LintCustomer("x.yml", []byte(source)); customer.Agents != 0 {
		t.Errorf("lint judged %d agent workflows, want 0", customer.Agents)
	}
}
