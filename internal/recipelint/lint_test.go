package recipelint

import (
	"slices"
	"strings"
	"testing"
)

const goodWorkflow = `on:
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
      - run: npm install --global @anthropic-ai/claude-code@2.1.316
      - uses: Costroid/costroid/action@1111111111111111111111111111111111111111 # v1.0.0
        env:
          OTEL_LOG_USER_PROMPTS: "0"
          OTEL_LOG_ASSISTANT_RESPONSES: "0"
          OTEL_LOG_TOOL_DETAILS: "0"
          OTEL_LOG_TOOL_CONTENT: "0"
          OTEL_LOG_RAW_API_BODIES: "0"
        with:
          stage: run
          agent: claude
          minimum-agent-version: 2.1.316
          prompt: Read pull-request.diff.
  ingest:
    needs: agent
    runs-on: ubuntu-24.04
    permissions:
      id-token: write
    steps:
      - uses: Costroid/costroid/action@1111111111111111111111111111111111111111 # v1.0.0
        with:
          stage: ingest
  report:
    needs: agent
    runs-on: ubuntu-24.04
    permissions:
      checks: write
    steps:
      - uses: Costroid/costroid/action@1111111111111111111111111111111111111111 # v1.0.0
        with:
          stage: report
`

const goodCodexJob = `  agent:
    if: >-
      github.event.pull_request.head.repo.full_name == github.repository
      && github.event.pull_request.author_association
      && github.event.pull_request.labels
    runs-on: ubuntu-24.04
    permissions:
      contents: read
    steps:
      - run: npm install --global @openai/codex@0.164.0-alpha.11.1
      - uses: Costroid/costroid/action@1111111111111111111111111111111111111111 # v1.0.0
        with:
          stage: run
          agent: codex
          minimum-agent-version: 0.164.0-alpha.11.1
          agent-args: |
            --config
            otel.log_user_prompt=false
`

const goodAction = `name: x
runs:
  using: composite
  steps:
    - uses: actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9 # v7.0.2
    - shell: bash
      env:
        VALUE: ${{ inputs.value }}
      run: echo "$VALUE"
`

const goodGitLab = `variables:
  OTEL_LOG_USER_PROMPTS: "0"
  OTEL_LOG_ASSISTANT_RESPONSES: "0"
  OTEL_LOG_TOOL_DETAILS: "0"
  OTEL_LOG_TOOL_CONTENT: "0"
  OTEL_LOG_RAW_API_BODIES: "0"
.base:
  image: node:24-slim@sha256:0e0ff40c39bc087845bfb27465a0df4ea419520094bc35842ff83dd8cbe6f9b6
agent:
  extends: .base
  variables:
    MINIMUM_AGENT_VERSION: 2.1.316
  rules:
    - if: $CI_MERGE_REQUEST_SOURCE_PROJECT_ID != $CI_MERGE_REQUEST_PROJECT_ID
      when: never
    - if: $CI_MERGE_REQUEST_LABELS =~ /costroid-agent/
  script:
    - npm install --global @anthropic-ai/claude-code@2.1.316
    - costroid run --budget-usd 5 --out rows.jsonl -- claude -p "$PROMPT"
ingest:
  extends: .base
  id_tokens:
    TOKEN:
      aud: costroid
  script:
    - curl --version
`

func rules(findings []Finding) []string {
	var names []string
	for _, finding := range findings {
		names = append(names, finding.Rule)
	}
	return names
}

func TestGoodFilesPass(t *testing.T) {
	for name, source := range map[string]string{
		"workflow": goodWorkflow,
		"codex":    goodWorkflow[:strings.Index(goodWorkflow, "  agent:\n")] + goodCodexJob,
		"action":   goodAction,
		"gitlab":   goodGitLab,
	} {
		t.Run(name, func(t *testing.T) {
			if report := LintBytes("x.yml", []byte(source)); len(report.Findings) != 0 {
				t.Fatalf("findings: %v", report.Findings)
			}
		})
	}
}

// edit turns a good file into a bad one, one change at a time, so each row
// proves the change it makes is what the rule rejects.
type edit struct {
	name, from, to, rule string
}

func TestEachWorkflowEditIsRejectedByItsRule(t *testing.T) {
	const pin = "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1"
	const flag = `          OTEL_LOG_RAW_API_BODIES: "0"` + "\n"
	const onBlock = "on:\n  pull_request:\n    types: [opened, synchronize, labeled]\n"
	edits := []edit{
		{"target as a scalar", onBlock, "on: pull_request_target\n", RulePullRequestTarget},
		{"target in a list", onBlock, "on: [push, pull_request_target]\n", RulePullRequestTarget},
		{"target in a mapping", "  pull_request:\n", "  pull_request_target:\n", RulePullRequestTarget},
		{"quoted target", onBlock, "\"on\":\n  \"pull_request_target\":\n", RulePullRequestTarget},
		{"a tag", pin, "actions/checkout@v7", RuleUnpinned},
		{"a branch", pin, "actions/checkout@main", RuleUnpinned},
		{"no ref", pin, "actions/checkout", RuleUnpinned},
		{"a short SHA", pin, "actions/checkout@3d3c42e", RuleUnpinned},
		{"an uppercase SHA", pin, "actions/checkout@3D3C42E5AAC5BA805825DA76410C181273BA90B1", RuleUnpinned},
		{"a local action", pin, "./.github/actions/x", RuleUnpinned},
		{"an image by tag", pin, "docker://alpine:3", RuleUnpinned},
		{"a zero SHA on another action", pin, "actions/checkout@" + zeroSHA, RuleUnpinned},
		{"a pin with no release", pin + " # v7.0.1", pin, RuleUnrecordedPin},
		{"id-token on the agent", "    permissions:\n      contents: read\n", "    permissions:\n      contents: read\n      id-token: write\n", RuleAgentIDToken},
		{"write-all on the agent", "    permissions:\n      contents: read\n", "    permissions: write-all\n", RuleAgentIDToken},
		{"id-token on the report", "      checks: write\n", "      checks: write\n      id-token: write\n", RuleIDTokenScope},
		{"write-all on the report", "    permissions:\n      checks: write\n", "    permissions: write-all\n", RulePermissions},
		{"write on the agent", "      contents: read\n", "      contents: write\n", RuleAgentPermissions},
		{"more on the agent", "      contents: read\n", "      contents: read\n      pull-requests: write\n", RuleAgentPermissions},
		{"forks allowed", "github.event.pull_request.head.repo.full_name == github.repository\n      && ", "", RuleAgentGuard},
		{"no maintainer label", "\n      || contains(github.event.pull_request.labels.*.name, 'costroid-agent')", "", RuleAgentGuard},
		{"no author test", "contains(fromJSON('[\"OWNER\"]'), github.event.pull_request.author_association)\n      || ", "", RuleAgentGuard},
		{"agent at latest", "claude-code@2.1.316", "claude-code@latest", RuleAgentVersion},
		{"no minimum", "          minimum-agent-version: 2.1.316\n", "", RuleAgentVersion},
		{"a content flag missing", flag, "", RuleAgentContentFlags},
		{"a content flag on", flag, `          OTEL_LOG_RAW_API_BODIES: "1"` + "\n", RuleAgentContentFlags},
		{"a comment in the prompt", "prompt: Read pull-request.diff.", "prompt: ${{ github.event.comment.body }}", RuleAgentPromptInput},
		{"the PR body in the prompt", "prompt: Read pull-request.diff.", "prompt: ${{ github.event.pull_request.body }}", RuleAgentPromptInput},
		{"comments fetched for the agent", "npm install --global @anthropic-ai/claude-code@2.1.316", "gh pr view --comments && npm install --global @anthropic-ai/claude-code@2.1.316", RuleAgentPromptInput},
		{"started by a comment", onBlock, "on: issue_comment\n", RuleAgentPromptInput},
		{"an expression in a script", "run: npm install", "run: echo ${{ github.head_ref }} && npm install", RuleExpressionInScript},
	}
	for _, change := range edits {
		t.Run(change.name, func(t *testing.T) {
			expectRule(t, goodWorkflow, change)
		})
	}
}

func TestAJobWithNoPermissionsOfItsOwnInheritsTheWorkflows(t *testing.T) {
	const reportGrant = "    permissions:\n      checks: write\n"
	inherited := strings.Replace(goodWorkflow, reportGrant, "", 1)
	if report := LintBytes("x.yml", []byte(inherited)); len(report.Findings) != 0 {
		t.Fatalf("the empty workflow grant counts as declared: %v", report.Findings)
	}
	expectRule(t, inherited, edit{"", "permissions: {}\n", "permissions:\n  id-token: write\n", RuleIDTokenScope})
	expectRule(t, inherited, edit{"", "permissions: {}\n", "", RulePermissions})
}

func TestAgentJobIsFoundHoweverTheAgentStarts(t *testing.T) {
	const agentStep = "      - uses: Costroid/costroid/action@1111111111111111111111111111111111111111 # v1.0.0\n"
	for name, step := range map[string]string{
		"claude in a script":    "      - run: claude -p hello\n",
		"costroid in a script":  "      - run: costroid run --budget-usd 1 --out r -- claude --print hello\n",
		"codex in a script":     "      - run: codex exec hello\n",
		"the action by default": agentStep,
		"claude-code-action":    "      - uses: anthropics/claude-code-action@1111111111111111111111111111111111111111 # v1\n",
		"codex-action":          "      - uses: openai/codex-action@1111111111111111111111111111111111111111 # v1\n",
	} {
		t.Run(name, func(t *testing.T) {
			source := "on: pull_request\njobs:\n  x:\n    runs-on: ubuntu-24.04\n    permissions:\n      id-token: write\n    steps:\n" + step
			report := LintBytes("x.yml", []byte(source))
			if !slices.Contains(rules(report.Findings), RuleAgentIDToken) {
				t.Fatalf("findings: %v", report.Findings)
			}
		})
	}
}

func TestCodexNeedsItsPromptFlagOff(t *testing.T) {
	source := goodWorkflow[:strings.Index(goodWorkflow, "  agent:\n")] + goodCodexJob
	expectRule(t, source, edit{"", "otel.log_user_prompt=false", "otel.log_user_prompt=true", RuleAgentContentFlags})
}

func TestAnAnchorCannotHideAPermission(t *testing.T) {
	source := strings.Replace(goodWorkflow, "permissions: {}\n", "x-shared: &shared\n  contents: read\n  id-token: write\npermissions: {}\n", 1)
	source = strings.Replace(source, "    permissions:\n      contents: read\n", "    permissions: *shared\n", 1)
	if report := LintBytes("x.yml", []byte(source)); !slices.Contains(rules(report.Findings), RuleAgentIDToken) {
		t.Fatalf("findings: %v", report.Findings)
	}
	source = strings.Replace(source, "    permissions: *shared\n", "    permissions:\n      <<: *shared\n", 1)
	if report := LintBytes("x.yml", []byte(source)); !slices.Contains(rules(report.Findings), RuleAgentIDToken) {
		t.Fatalf("findings through a merge key: %v", report.Findings)
	}
}

func TestReusableWorkflowsMustBePinnedToo(t *testing.T) {
	source := "on: pull_request\npermissions: {}\njobs:\n  x:\n    uses: octo/repo/.github/workflows/x.yml@main\n"
	if got := rules(LintBytes("x.yml", []byte(source)).Findings); !slices.Contains(got, RuleUnpinned) {
		t.Fatalf("rules: %v", got)
	}
}

func TestActionStepsAreHeldToThePinAndScriptRules(t *testing.T) {
	expectRule(t, goodAction, edit{"", "cf430e030ddbb5b0abf93d22962f4752f3646cd9", "v7", RuleUnpinned})
	expectRule(t, goodAction, edit{"", `run: echo "$VALUE"`, `run: echo "${{ inputs.value }}"`, RuleExpressionInScript})
}

func TestEachGitLabEditIsRejectedByItsRule(t *testing.T) {
	const image = "node:24-slim@sha256:0e0ff40c39bc087845bfb27465a0df4ea419520094bc35842ff83dd8cbe6f9b6"
	edits := []edit{
		{"an image by tag", image, "node:24-slim", RuleUnpinned},
		{"an image with no tag", image, "node", RuleUnpinned},
		{"id_tokens on the agent", "  extends: .base\n  variables:\n", "  extends: .base\n  id_tokens:\n    TOKEN:\n      aud: x\n  variables:\n", RuleAgentIDToken},
		{"id_tokens through extends", "  image: node", "  id_tokens:\n    TOKEN:\n      aud: x\n  image: node", RuleAgentIDToken},
		{"forks allowed", "    - if: $CI_MERGE_REQUEST_SOURCE_PROJECT_ID != $CI_MERGE_REQUEST_PROJECT_ID\n      when: never\n", "", RuleAgentGuard},
		{"no label", "    - if: $CI_MERGE_REQUEST_LABELS =~ /costroid-agent/\n", "", RuleAgentGuard},
		{"agent at latest", "claude-code@2.1.316", "claude-code@latest", RuleAgentVersion},
		{"no minimum", "  variables:\n    MINIMUM_AGENT_VERSION: 2.1.316\n", "", RuleAgentVersion},
		{"a content flag on", `OTEL_LOG_USER_PROMPTS: "0"`, `OTEL_LOG_USER_PROMPTS: "1"`, RuleAgentContentFlags},
		{"the title in the prompt", `"$PROMPT"`, `"$CI_MERGE_REQUEST_TITLE"`, RuleAgentPromptInput},
		{"a floating include", "variables:\n  OTEL_LOG_USER", "include:\n  - project: a/b\n    ref: main\n    file: x.yml\nvariables:\n  OTEL_LOG_USER", RuleUnpinned},
		{"a remote include", "variables:\n  OTEL_LOG_USER", "include: https://example.com/x.yml\nvariables:\n  OTEL_LOG_USER", RuleUnpinned},
	}
	for _, change := range edits {
		t.Run(change.name, func(t *testing.T) {
			expectRule(t, goodGitLab, change)
		})
	}
}

func expectRule(t *testing.T, source string, change edit) {
	t.Helper()
	if !strings.Contains(source, change.from) {
		t.Fatalf("the edit's text is not in the source: %q", change.from)
	}
	report := LintBytes("x.yml", []byte(strings.Replace(source, change.from, change.to, 1)))
	if !slices.Contains(rules(report.Findings), change.rule) {
		t.Fatalf("want %s, got %v", change.rule, report.Findings)
	}
}

func TestAFileThatIsNoRecipeIsRejected(t *testing.T) {
	for name, source := range map[string]string{
		"plain mapping": "a: 1\n",
		"empty":         "",
		"broken":        "a: [1\n",
	} {
		t.Run(name, func(t *testing.T) {
			if got := rules(LintBytes("x.yml", []byte(source)).Findings); !slices.Contains(got, RuleUnreadable) {
				t.Fatalf("rules: %v", got)
			}
		})
	}
}

func TestTheZeroSHAOnCostroidsActionIsANoteAndNeverARejection(t *testing.T) {
	source := strings.ReplaceAll(goodWorkflow, "1111111111111111111111111111111111111111 # v1.0.0", zeroSHA+" # unreleased")
	report := LintBytes("x.yml", []byte(source))
	if len(report.Findings) != 0 || len(report.Notes) != 3 {
		t.Fatalf("findings %v, notes %v", report.Findings, report.Notes)
	}
}
