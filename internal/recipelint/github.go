package recipelint

import (
	"maps"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

var (
	shaPin      = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._/-]+@[0-9a-f]{40}$`)
	digestPin   = regexp.MustCompile(`^docker://[^@]+@sha256:[0-9a-f]{64}$`)
	releaseNote = regexp.MustCompile(`^#\s*\S`)

	// commentEvents hand an agent's job text anyone can write.
	commentEvents = map[string]bool{
		"issue_comment":               true,
		"discussion_comment":          true,
		"pull_request_review":         true,
		"pull_request_review_comment": true,
	}

	gitHubForbidden = regexp.MustCompile(`github\.event\.(comment|review|issue|discussion)\b|github\.event\.pull_request\.(title|body)\b|github\.head_ref\b|--comments\b|/comments\b`)
)

// gitHubGuards name what a job's if must test before an agent runs: the fork,
// the author's standing and the maintainer's label.
var gitHubGuards = []string{
	"github.event.pull_request.head.repo.full_name == github.repository",
	"github.event.pull_request.author_association",
	"github.event.pull_request.labels",
}

func (l *linter) workflow(root *yaml.Node) {
	l.events(field(root, "on"))
	workflowPermissions := field(root, "permissions")
	for _, item := range entries(field(root, "jobs")) {
		l.job(root, item, workflowPermissions)
	}
}

func (l *linter) events(on *yaml.Node) {
	check := func(node *yaml.Node, name string) {
		if name == "pull_request_target" {
			l.add(node, RulePullRequestTarget, "runs with the base repository's secrets on code a PR controls: use pull_request")
		}
		if commentEvents[name] {
			l.add(node, RuleAgentPromptInput, "%s starts a run from a comment: use pull_request", name)
		}
	}
	if pairs := entries(on); len(pairs) > 0 {
		for _, item := range pairs {
			check(item.key, item.key.Value)
		}
		return
	}
	for _, event := range sequence(on) {
		check(event, event.Value)
	}
}

func (l *linter) job(root *yaml.Node, job entry, workflowPermissions *yaml.Node) {
	if uses := field(job.value, "uses"); uses != nil {
		l.uses(job.value, uses)
	}
	steps := sequence(field(job.value, "steps"))
	permissions := field(job.value, "permissions")
	if permissions == nil {
		permissions = workflowPermissions
	}
	l.permissions(job.key, permissions)

	agent, agentStep := "", (*yaml.Node)(nil)
	ingest := false
	for _, step := range steps {
		l.step(step)
		if name := stepAgent(step); name != "" {
			agent, agentStep = name, step
		}
		ingest = ingest || costroidStage(step) == "ingest"
	}
	granted := grants(permissions)
	if agent == "" {
		if granted["id-token"] == "write" && !ingest {
			l.add(job.key, RuleIDTokenScope, "id-token: write belongs on the ingest job alone")
		}
		return
	}
	l.agentJob(root, job, agent, agentStep, granted)
}

func (l *linter) step(step *yaml.Node) {
	if uses := field(step, "uses"); uses != nil {
		l.uses(step, uses)
	}
	if run := field(step, "run"); run != nil && strings.Contains(run.Value, "${{") {
		l.add(run, RuleExpressionInScript, "an expression in a script is a shell injection: pass it through env")
	}
}

// uses holds one action reference to a SHA and to a recorded release. The
// comment that records it sits on the key or the value, by how YAML parsed it.
func (l *linter) uses(holder, uses *yaml.Node) {
	ref := uses.Value
	switch {
	case digestPin.MatchString(ref):
		return
	case !shaPin.MatchString(ref):
		l.add(uses, RuleUnpinned, "%s is not pinned to a full commit SHA", ref)
		return
	case strings.HasSuffix(ref, "@"+zeroSHA):
		if ref != costroidAction+"@"+zeroSHA {
			l.add(uses, RuleUnpinned, "%s is pinned to the zero SHA, which names no commit", ref)
			return
		}
		l.note(uses, "unreleased", "%s has no release yet, so this recipe fails at start until one is pinned", costroidAction)
	}
	if !releaseNote.MatchString(lineComment(holder, uses)) {
		l.add(uses, RuleUnrecordedPin, "%s does not record the release its SHA came from", ref)
	}
}

func lineComment(holder, uses *yaml.Node) string {
	if uses.LineComment != "" {
		return uses.LineComment
	}
	for _, item := range entries(holder) {
		if item.value == uses {
			return item.key.LineComment
		}
	}
	return ""
}

func (l *linter) permissions(job, permissions *yaml.Node) {
	switch {
	case permissions == nil:
		l.add(job, RulePermissions, "job %s declares no permissions", job.Value)
	case permissions.Kind == yaml.ScalarNode && permissions.Value != "read-all":
		l.add(permissions, RulePermissions, "job %s asks for %s", job.Value, permissions.Value)
	}
}

func grants(permissions *yaml.Node) map[string]string {
	granted := map[string]string{}
	if permissions != nil && permissions.Kind == yaml.ScalarNode {
		if access, found := strings.CutSuffix(permissions.Value, "-all"); found {
			granted["id-token"] = access
			granted["*"] = access
		}
		return granted
	}
	for _, item := range entries(permissions) {
		granted[item.key.Value] = item.value.Value
	}
	return granted
}

func (l *linter) agentJob(root *yaml.Node, job entry, agent string, step *yaml.Node, granted map[string]string) {
	if granted["id-token"] == "write" {
		l.add(job.key, RuleAgentIDToken, "the agent's job must not hold id-token: write")
	}
	rest := maps.Clone(granted)
	delete(rest, "id-token")
	if len(rest) != 1 || rest["contents"] != "read" {
		l.add(job.key, RuleAgentPermissions, "the agent's job must hold contents: read and nothing else")
	}
	guard := scalarField(job.value, "if")
	for _, want := range gitHubGuards {
		if !strings.Contains(guard, want) {
			l.add(job.key, RuleAgentGuard, "the job's if must test %s", want)
		}
	}
	steps := field(job.value, "steps")
	l.checkAgent(agentRun{
		node:    job.key,
		agent:   agent,
		text:    text(steps),
		env:     mergeVariables(field(root, "env"), field(job.value, "env"), field(step, "env")),
		minimum: minimumVersionInput,
	}, gitHubForbidden)
}

// stepAgent names the agent a step starts, or nothing.
func stepAgent(step *yaml.Node) string {
	uses := strings.ToLower(scalarField(step, "uses"))
	switch {
	case strings.HasPrefix(uses, "anthropics/claude-code-action"):
		return claudeAgent
	case strings.HasPrefix(uses, "openai/codex-action"):
		return codexAgent
	}
	if stage := costroidStage(step); stage == "run" || stage == "all" {
		if name := scalarField(field(step, "with"), "agent"); name != "" {
			return name
		}
		return claudeAgent
	}
	if agents := agentsNamed(scalarField(step, "run")); len(agents) > 0 {
		return agents[0]
	}
	return ""
}

// costroidStage reads the stage of a step that uses Costroid's action. The
// action's default is all, so a step that states none runs the agent.
func costroidStage(step *yaml.Node) string {
	ref, _, _ := strings.Cut(scalarField(step, "uses"), "@")
	if !strings.EqualFold(ref, costroidAction) {
		return ""
	}
	if stage := scalarField(field(step, "with"), "stage"); stage != "" {
		return stage
	}
	return "all"
}

// action holds a composite action's own steps to the pin and script rules.
func (l *linter) action(root *yaml.Node) {
	for _, step := range sequence(field(field(root, "runs"), "steps")) {
		l.step(step)
	}
}
