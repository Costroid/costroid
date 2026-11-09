package recipelint

import (
	"slices"
	"strings"
	"testing"
)

// goodCopilotWorkflow runs Copilot CLI under costroid run --agent copilot on
// the user's own Anthropic key, with its shell tool denied.
const goodCopilotWorkflow = `on:
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
      - run: npm install --global @github/copilot@1.0.103
      - env:
          MINIMUM_AGENT_VERSION: 1.0.103
          COPILOT_PROVIDER_BASE_URL: https://api.anthropic.com
          COPILOT_PROVIDER_TYPE: anthropic
          COPILOT_PROVIDER_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
        run: |
          costroid run --agent copilot --budget-usd 5 --out rows.jsonl -- \
            copilot -p "Read pull-request.diff." --allow-all-tools --deny-tool='shell'
`

// goodCopilotGitLab is the same job on GitLab, its key a masked variable.
const goodCopilotGitLab = `.base:
  image: node:24-slim@sha256:0e0ff40c39bc087845bfb27465a0df4ea419520094bc35842ff83dd8cbe6f9b6
agent:
  extends: .base
  variables:
    MINIMUM_AGENT_VERSION: 1.0.103
    COPILOT_PROVIDER_BASE_URL: https://api.anthropic.com
    COPILOT_PROVIDER_TYPE: anthropic
    COPILOT_PROVIDER_BEARER_TOKEN: $ANTHROPIC_API_KEY
  rules:
    - if: $CI_MERGE_REQUEST_SOURCE_PROJECT_ID != $CI_MERGE_REQUEST_PROJECT_ID
      when: never
    - if: $CI_MERGE_REQUEST_LABELS =~ /costroid-agent/
  script:
    - npm install --global @github/copilot@1.0.103
    - costroid run --agent copilot --budget-usd 5 --out rows.jsonl -- copilot -p "$PROMPT" --allow-all-tools --deny-tool=shell
`

// TestCopilotAgentJobRules holds a Copilot CLI job to its install, its
// provider variables, a key read from one secret and a denied shell tool, on
// either CI system. Each edit breaks one of them and draws its rule.
func TestCopilotAgentJobRules(t *testing.T) {
	for name, source := range map[string]string{"github": goodCopilotWorkflow, "gitlab": goodCopilotGitLab} {
		t.Run(name, func(t *testing.T) {
			if report := LintBytes("x.yml", []byte(source)); len(report.Findings) != 0 {
				t.Fatalf("findings: %v", report.Findings)
			}
		})
	}
	for _, change := range []edit{
		{"copilot at latest", "@github/copilot@1.0.103\n", "@github/copilot@latest\n", RuleAgentVersion},
		{"no minimum version", "          MINIMUM_AGENT_VERSION: 1.0.103\n", "", RuleAgentVersion},
		{"no provider", "          COPILOT_PROVIDER_BASE_URL: https://api.anthropic.com\n", "", RuleAgentProvider},
		{"no provider type", "          COPILOT_PROVIDER_TYPE: anthropic\n", "", RuleAgentProvider},
		{"no key", "          COPILOT_PROVIDER_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}\n", "", RuleAgentProvider},
		{"a key in the file", "${{ secrets.ANTHROPIC_API_KEY }}", "sk-ant-api03-written-here", RuleAgentProvider},
		{"a key beside text", "${{ secrets.ANTHROPIC_API_KEY }}", "x${{ secrets.ANTHROPIC_API_KEY }}", RuleAgentProvider},
		{"the shell allowed", " --deny-tool='shell'", "", RuleAgentTools},
		{"one command denied", "--deny-tool='shell'", "--deny-tool='shell(git push)'", RuleAgentTools},
		{"another tool denied", "--deny-tool='shell'", "--deny-tool='write'", RuleAgentTools},
	} {
		t.Run("github/"+change.name, func(t *testing.T) {
			expectRule(t, goodCopilotWorkflow, change)
		})
	}
	for _, change := range []edit{
		{"no key", "    COPILOT_PROVIDER_BEARER_TOKEN: $ANTHROPIC_API_KEY\n", "", RuleAgentProvider},
		{"a key in the file", "$ANTHROPIC_API_KEY\n", "written-here\n", RuleAgentProvider},
		{"the shell allowed", " --deny-tool=shell", "", RuleAgentTools},
	} {
		t.Run("gitlab/"+change.name, func(t *testing.T) {
			expectRule(t, goodCopilotGitLab, change)
		})
	}
	report := LintBytes("x.yml", []byte(strings.Replace(goodCopilotWorkflow, "          COPILOT_PROVIDER_BASE_URL: https://api.anthropic.com\n", "", 1)))
	if got := withRule(report.Findings, RuleAgentProvider); len(got) != 1 || got[0].Message != "COPILOT_PROVIDER_BASE_URL must be set on the agent, so Copilot CLI runs on your provider's key, not on its own routing" {
		t.Errorf("no provider: %v", got)
	}
	if got := rules(report.Findings); slices.Contains(got, RuleAgentContentFlags) {
		t.Errorf("a Copilot CLI job is held to another agent's content flags: %v", report.Findings)
	}
}
