package recipelint

import (
	"maps"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// RuleEgressAudit names harden-runner in audit mode. It is a note and rejects
// nothing: audit mode records a job's outbound calls and blocks none.
const RuleEgressAudit = "egress-audit"

const hardenRunner = "step-security/harden-runner@"

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

// unguardedEvents start a run that no pull request, comment or fork can start.
// An agent workflow that only these start has no pull request to guard
// against. Every other event, a reusable workflow's caller included, keeps
// the guard.
var unguardedEvents = map[string]bool{
	"schedule":          true,
	"workflow_dispatch": true,
}

// gitHubGuards name what a job's if must test before an agent runs: the fork,
// the author's standing and the maintainer's label.
var gitHubGuards = []string{
	"github.event.pull_request.head.repo.full_name == github.repository",
	"github.event.pull_request.author_association",
	"github.event.pull_request.labels",
}

func (l *linter) workflow(root *yaml.Node) {
	l.events(field(root, "on"))
	if isFollowUp(field(root, "on")) {
		l.followUp(root)
	}
	workflowPermissions := field(root, "permissions")
	guarded := needsGuard(field(root, "on"))
	for _, item := range entries(field(root, "jobs")) {
		l.job(root, item, workflowPermissions, guarded)
	}
}

// needsGuard reports whether a pull request could start the workflow. A
// workflow whose events cannot be read is held to the guard.
func needsGuard(on *yaml.Node) bool {
	names := eventNames(on)
	return len(names) == 0 || slices.ContainsFunc(names, func(name string) bool { return !unguardedEvents[name] })
}

// eventNames lists the events on names, written as one name, a list or a
// mapping.
func eventNames(on *yaml.Node) []string {
	on = resolve(on)
	if on == nil {
		return nil
	}
	if on.Kind == yaml.ScalarNode {
		return []string{on.Value}
	}
	var names []string
	if pairs := entries(on); len(pairs) > 0 {
		for _, item := range pairs {
			names = append(names, item.key.Value)
		}
		return names
	}
	for _, event := range sequence(on) {
		names = append(names, event.Value)
	}
	return names
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

func (l *linter) job(root *yaml.Node, job entry, workflowPermissions *yaml.Node, guarded bool) {
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
	ingest, mint := false, false
	for _, step := range steps {
		l.step(step)
		if name := stepAgent(step); name != "" {
			agent, agentStep = name, step
		}
		ingest = ingest || costroidStage(step) == "ingest"
		mint = mint || mintsKeys(step)
	}
	granted := grants(permissions)
	if agent == "" {
		attests := mint && field(job.value, "environment") != nil
		if granted["id-token"] == "write" && !ingest && !attests {
			l.add(job.key, RuleIDTokenScope, "id-token: write belongs on the ingest job, and on a job that mints gateway keys in an environment")
		}
		return
	}
	l.agentJob(root, job, agent, agentStep, granted, guarded)
}

func (l *linter) step(step *yaml.Node) {
	if uses := field(step, "uses"); uses != nil {
		l.uses(step, uses)
		l.downloadPath(step)
		l.egressAudit(step, uses)
	}
	if run := field(step, "run"); run != nil && strings.Contains(run.Value, "${{") {
		l.add(run, RuleExpressionInScript, "an expression in a script is a shell injection: pass it through env")
	}
}

// egressAudit notes harden-runner in audit mode, which lets an injected agent
// send what its job holds to any host the runner reaches.
func (l *linter) egressAudit(step, uses *yaml.Node) {
	if !strings.HasPrefix(strings.ToLower(uses.Value), hardenRunner) {
		return
	}
	if policy := field(field(step, "with"), "egress-policy"); policy != nil && policy.Value == "audit" {
		l.note(policy, RuleEgressAudit, "egress-policy audit records the job's outbound calls and blocks none, so an injected agent can still send what its job holds to any host the runner reaches")
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
		l.note(uses, "unreleased", "%s is pinned to the zero SHA, so this recipe fails at start until a release is pinned", costroidAction)
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

func (l *linter) agentJob(root *yaml.Node, job entry, agent string, step *yaml.Node, granted map[string]string, guarded bool) {
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
		if guarded && !strings.Contains(guard, want) {
			l.add(job.key, RuleAgentGuard, "the job's if must test %s", want)
		}
	}
	steps := field(job.value, "steps")
	minimum := minimumVersionInput
	if scalarField(step, "run") != "" {
		minimum = minimumVersionVariable
	}
	l.checkAgent(agentRun{
		node:    job.key,
		agent:   agent,
		text:    text(steps),
		env:     mergeVariables(field(root, "env"), field(job.value, "env"), field(step, "env")),
		minimum: minimum,
	}, gitHubForbidden)
	l.agentAdminKey(root, job)
	l.agentCheckout(job)
}

const (
	claudeCodeAction = "anthropics/claude-code-action"
	codexAction      = "openai/codex-action"
)

// openingInputs are the inputs of each agent's action that let more people or
// bots start it than the action admits by default.
var openingInputs = map[string][]string{
	claudeCodeAction: {"allowed_non_write_users", "allowed_bots"},
	codexAction:      {"allow-users", "allow-bots"},
}

// stepAgent names the agent a step starts, or nothing. A claude-code-action
// step starts Claude whether its prompt input or a @claude mention in a
// comment gives it the task, so the mention workflow is an agent workflow.
func stepAgent(step *yaml.Node) string {
	switch agentAction(step) {
	case claudeCodeAction:
		return claudeAgent
	case codexAction:
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

// agentAction names the agent's action a step uses, or nothing.
func agentAction(step *yaml.Node) string {
	uses := strings.ToLower(scalarField(step, "uses"))
	for _, action := range []string{claudeCodeAction, codexAction} {
		if strings.HasPrefix(uses, action) {
			return action
		}
	}
	return ""
}

// agentOpenTo lists the opening inputs a step sets: every one that is not
// empty, and not false.
func agentOpenTo(step *yaml.Node) []entry {
	opening := openingInputs[agentAction(step)]
	var open []entry
	for _, input := range entries(field(step, "with")) {
		value := strings.TrimSpace(input.value.Value)
		if slices.Contains(opening, input.key.Value) && value != "" && !strings.EqualFold(value, "false") {
			open = append(open, input)
		}
	}
	return open
}

// mintsKeys reports whether a step runs costroid gateway mint, which attests
// the key it registers under the job's OIDC token. The token names the job's
// environment, and only that name lets the ledger take the attestation.
func mintsKeys(step *yaml.Node) bool {
	return strings.Contains(scalarField(step, "run"), "costroid gateway mint")
}

// costroidStage reads the stage of a step that uses Costroid's action. The
// action has no default stage, so a step that states none fails at start. It
// is read as all, the stage that runs the agent, so the agent's rules still
// hold its job.
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
