package recipelint

import (
	"maps"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// RuleEgressAudit names harden-runner that leaves a job's egress open or
// broken: audit mode, block mode with no allowed-endpoints, or a list that
// harden-runner cannot read. RuleEgressEndpoint names a host a recipe allows
// that is not one of egressHosts, and telemetry left on, which allows
// step-security's own hosts. Both are notes and reject nothing.
const (
	RuleEgressAudit    = "egress-audit"
	RuleEgressEndpoint = "egress-endpoint"
)

const hardenRunner = "step-security/harden-runner@"

// egressHosts are the hosts an agent's job in the recipes may allow: the
// agent's provider, GitHub, and the hosts the install steps reach. A recipe
// that allows another host adds it here with the step that needs it, and to
// the table in docs/recipes.md, or lint-recipes notes it.
var egressHosts = map[string]bool{
	// Claude Code, and the run's key on the LiteLLM gateway.
	"api.anthropic.com:443":      true,
	"vars.COSTROID_GATEWAY_HOST": true,
	// Codex, and the proxy codex-action starts.
	"api.openai.com:443": true,
	// actions/checkout, Costroid's install, cosign-installer and setup-bun.
	"github.com:443": true,
	// claude-code-action and codex-action read the pull request and the actor.
	"api.github.com:443": true,
	// actions/upload-artifact, and the run and read stages that keep the rows.
	"results-receiver.actions.githubusercontent.com:443": true,
	"productionresultssa*.blob.core.windows.net:443":     true,
	// Where github.com sends a release download: Costroid, cosign and Bun.
	"release-assets.githubusercontent.com:443": true,
	// cosign reads Sigstore's trusted root here before it checks a release.
	"tuf-repo-cdn.sigstore.dev:443": true,
	// npm installs the agent at its pinned version; bun installs
	// claude-code-action's packages.
	"registry.npmjs.org:443": true,
}

// endpointToken is one entry of allowed-endpoints: an expression, which may
// hold spaces, or a run of other characters.
var endpointToken = regexp.MustCompile(`\$\{\{.*?\}\}|\S+`)

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
	l.events(field(root, "on"), startsAgent(root))
	if isFollowUp(field(root, "on")) {
		l.followUp(root)
	}
	if isApproval(root) {
		l.approval(root)
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

// events refuses the privileged trigger, and a comment's or a review's
// trigger in a workflow that starts an agent. A workflow with no agent step,
// such as the approval recipe, may run on a review: nobody's text reaches an
// agent there.
func (l *linter) events(on *yaml.Node, agent bool) {
	check := func(node *yaml.Node, name string) {
		if name == "pull_request_target" {
			l.add(node, RulePullRequestTarget, "runs with the base repository's secrets on code a PR controls: use pull_request")
		}
		if agent && commentEvents[name] {
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

// startsAgent reports a workflow with an agent step, or a job that calls
// another workflow, whose steps this file does not show. A workflow whose
// jobs cannot be read is held to the agent's rules.
func startsAgent(root *yaml.Node) bool {
	jobs := entries(field(root, "jobs"))
	if len(jobs) == 0 {
		return true
	}
	for _, job := range jobs {
		if field(job.value, "uses") != nil || slices.ContainsFunc(sequence(field(job.value, "steps")), func(step *yaml.Node) bool { return stepAgent(step) != "" }) {
			return true
		}
	}
	return false
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
	ingest, gateway := false, false
	for _, step := range steps {
		l.step(step)
		if name := stepAgent(step); name != "" {
			agent, agentStep = name, step
		}
		ingest = ingest || costroidStage(step) == "ingest"
		gateway = gateway || attestsKeys(step)
	}
	granted := grants(permissions)
	l.platform(job, steps, granted)
	if agent == "" {
		attests := gateway && namesEnvironment(job.value)
		if granted["id-token"] == "write" && !ingest && !attests && !slices.ContainsFunc(steps, connects) {
			l.add(job.key, RuleIDTokenScope, "id-token: write belongs on the ingest job, the connect job, and a job that mints or closes gateway keys in an environment")
		}
		return
	}
	l.agentJob(root, job, agent, agentStep, granted, guarded)
}

func (l *linter) step(step *yaml.Node) {
	if uses := field(step, "uses"); uses != nil {
		l.uses(step, uses)
		l.downloadPath(step)
		l.egress(step, uses)
	}
	if run := field(step, "run"); run != nil && strings.Contains(run.Value, "${{") {
		l.add(run, RuleExpressionInScript, "an expression in a script is a shell injection: pass it through env")
	}
}

// egress notes a harden-runner step that leaves the job's egress open or
// broken, and in block mode each host it allows beyond egressHosts.
func (l *linter) egress(step, uses *yaml.Node) {
	if !isHardenRunner(uses) {
		return
	}
	if node, message := egressGap(step); node != nil {
		l.note(node, RuleEgressAudit, "%s", message)
		return
	}
	with := field(step, "with")
	if !blocks(with) {
		return
	}
	if telemetry := field(with, "disable-telemetry"); telemetry == nil || !strings.EqualFold(telemetry.Value, "true") {
		l.note(firstOf(telemetry, uses), RuleEgressEndpoint, "disable-telemetry is not true, so harden-runner also allows agent.api.stepsecurity.io and prod.app-api.stepsecurity.io: set disable-telemetry: true")
	}
	list := field(with, "allowed-endpoints")
	for _, endpoint := range allowedEndpoints(list.Value) {
		if !egressHosts[endpoint] {
			l.note(list, RuleEgressEndpoint, "harden-runner allows %s, which is not the agent's provider, GitHub or a host the install steps reach, so an injected agent can send to it: keep it only for a step that needs it", endpoint)
		}
	}
}

func isHardenRunner(uses *yaml.Node) bool {
	return strings.HasPrefix(strings.ToLower(uses.Value), hardenRunner)
}

// blocks reports whether a harden-runner step is in block mode. An empty
// policy is block, the default of the action's input at v2.22.3.
func blocks(with *yaml.Node) bool {
	policy := scalarField(with, "egress-policy")
	return policy == "" || policy == "block"
}

// egressGap names what leaves a harden-runner step's egress open or broken,
// with its fix, or returns nil. Audit mode blocks no call. Block mode with no
// list allows GitHub's own hosts alone. A line break in the list hides the
// hosts on either side of it, because harden-runner's agent splits the list on
// spaces alone (step-security/agent v0.18.0, config.go, parseEndpoints).
func egressGap(step *yaml.Node) (*yaml.Node, string) {
	with := field(step, "with")
	if policy := field(with, "egress-policy"); policy != nil && policy.Value == "audit" {
		return policy, "egress-policy audit records the job's outbound calls and blocks none, so an injected agent can send what its job holds to any host: set egress-policy: block and list the hosts the job reaches in allowed-endpoints"
	}
	if !blocks(with) {
		return nil, ""
	}
	list := field(with, "allowed-endpoints")
	switch {
	case list == nil || strings.TrimSpace(list.Value) == "":
		return firstOf(list, field(with, "egress-policy"), step), "egress-policy block names no allowed-endpoints, so harden-runner allows GitHub's own hosts alone and the agent cannot reach its provider: list the hosts the job reaches in allowed-endpoints"
	case strings.Contains(strings.TrimSpace(list.Value), "\n"):
		return list, "allowed-endpoints holds a line break, and harden-runner splits the list on spaces alone, so the hosts on either side of it are not allowed: write the list as a folded block, allowed-endpoints: >-"
	}
	return nil, ""
}

// firstOf picks the node a note names: the first one the step has.
func firstOf(nodes ...*yaml.Node) *yaml.Node {
	for _, node := range nodes {
		if node != nil {
			return node
		}
	}
	return nil
}

// allowedEndpoints lists the entries of allowed-endpoints. An expression reads
// as the name inside it, such as vars.COSTROID_GATEWAY_HOST.
func allowedEndpoints(value string) []string {
	var endpoints []string
	for _, token := range endpointToken.FindAllString(value, -1) {
		if inner, found := strings.CutPrefix(token, "${{"); found {
			token = strings.TrimSpace(strings.TrimSuffix(inner, "}}"))
		}
		endpoints = append(endpoints, token)
	}
	return endpoints
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

// attestsKeys reports whether a step runs costroid gateway mint, which attests
// the key it registers, or costroid gateway close, which attests what the
// gateway counted on it. Both attest under the job's OIDC token. The token
// names the job's environment, and only that name lets the ledger take the
// attestation.
func attestsKeys(step *yaml.Node) bool {
	run := scalarField(step, "run")
	return strings.Contains(run, "costroid gateway mint") || strings.Contains(run, "costroid gateway close")
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
