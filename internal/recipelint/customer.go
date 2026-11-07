package recipelint

import (
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Severity ranks a finding of the customer profile. A high finding fails
// costroid lint, a medium one fails it under --strict, and a note never does.
type Severity string

// The severities, from the most to the least serious.
const (
	High   Severity = "high"
	Medium Severity = "medium"
	Note   Severity = "note"
)

// The rules only the customer profile names. It also names
// RulePullRequestTarget, RuleAgentIDToken, RuleExpressionInScript,
// RuleUnpinned, RuleAgentPromptInput and RuleUnreadable.
const (
	RuleCommentTrigger    = "comment-trigger"
	RuleAgentOpen         = "agent-open"
	RuleAgentContentWrite = "agent-contents-write"
	RuleSubscriptionToken = "subscription-token"
)

var (
	// textEvents start a run from text that anyone with a GitHub account can
	// write: a comment, a review, an issue or a discussion.
	textEvents = map[string]bool{
		"issue_comment":               true,
		"pull_request_review":         true,
		"pull_request_review_comment": true,
		"discussion_comment":          true,
		"issues":                      true,
		"discussion":                  true,
	}

	// authorTest is what a job's if must test before a text event starts the
	// agent: the author's association with the repository, or the account.
	authorTest = regexp.MustCompile(`author_association\b|github\.(actor|triggering_actor)\b`)

	// headRef is a checkout ref that names the pull request's own code.
	headRef = regexp.MustCompile(`github\.event\.pull_request\.head\b|github\.head_ref\b|refs/pull/`)
)

// subscriptionInput and subscriptionVariable carry a Claude subscription's
// token to claude-code-action and to Claude Code.
const (
	subscriptionInput    = "claude_code_oauth_token"
	subscriptionVariable = "CLAUDE_CODE_OAUTH_TOKEN"
)

// LintCustomer checks one of a team's own workflows. Only a workflow with an
// agent step is judged; any other file passes with nothing said, and Agents
// stays 0. A file that is not YAML is a high finding, because nothing in it
// could be judged.
func LintCustomer(path string, data []byte) Report {
	l := &linter{path: path}
	documents, err := parse(data)
	if err != nil {
		l.flag(&yaml.Node{Line: 1}, High, RuleUnreadable, "not valid YAML: %v", err)
		return l.report
	}
	for _, root := range documents {
		if field(root, "jobs") != nil && field(root, "on") != nil && l.customerWorkflow(root) {
			l.report.Agents++
		}
	}
	return l.report
}

// flag records a finding with its severity. A note goes with the notes.
func (l *linter) flag(node *yaml.Node, severity Severity, rule, format string, args ...any) {
	finding := l.finding(node, rule, format, args...)
	finding.Severity = severity
	if severity == Note {
		l.report.Notes = append(l.report.Notes, finding)
		return
	}
	l.report.Findings = append(l.report.Findings, finding)
}

// customerAgent is one job of a customer workflow that starts an agent.
type customerAgent struct {
	job   entry
	step  *yaml.Node
	agent string
}

// customerWorkflow judges a workflow and reports whether it starts an agent.
func (l *linter) customerWorkflow(root *yaml.Node) bool {
	var agents []customerAgent
	for _, job := range entries(field(root, "jobs")) {
		for _, step := range sequence(field(job.value, "steps")) {
			if name := stepAgent(step); name != "" {
				agents = append(agents, customerAgent{job: job, step: step, agent: name})
				break
			}
		}
	}
	if len(agents) == 0 {
		return false
	}
	on := field(root, "on")
	target, texts := l.customerEvents(on)
	for _, job := range entries(field(root, "jobs")) {
		for _, step := range sequence(field(job.value, "steps")) {
			if run := field(step, "run"); run != nil && strings.Contains(run.Value, "${{") {
				l.flag(run, High, RuleExpressionInScript, "an expression in a script is a shell injection: pass it through env")
			}
			if target {
				l.headCheckout(step)
			}
		}
	}
	for _, agent := range agents {
		l.customerAgentJob(root, agent, texts)
	}
	return true
}

// customerEvents flags pull_request_target and lists the text events.
func (l *linter) customerEvents(on *yaml.Node) (target bool, texts []string) {
	check := func(node *yaml.Node, name string) {
		if name == "pull_request_target" {
			target = true
			l.flag(node, High, RulePullRequestTarget, "pull_request_target runs the agent with this repository's secrets on a pull request anyone can open: use pull_request")
		}
		if textEvents[name] {
			texts = append(texts, name)
		}
	}
	if pairs := entries(on); len(pairs) > 0 {
		for _, item := range pairs {
			check(item.key, item.key.Value)
		}
		return target, texts
	}
	for _, event := range sequence(on) {
		check(event, event.Value)
	}
	return target, texts
}

// headCheckout flags a checkout of the pull request's head under
// pull_request_target, which runs the pull request's code with this
// repository's secrets.
func (l *linter) headCheckout(step *yaml.Node) {
	if ref, _, _ := strings.Cut(strings.ToLower(scalarField(step, "uses")), "@"); ref != checkoutAction {
		return
	}
	if ref := field(field(step, "with"), "ref"); ref != nil && headRef.MatchString(ref.Value) {
		l.flag(ref, High, RulePullRequestTarget, "checks out the pull request's head under pull_request_target, so its code runs with this repository's secrets")
	}
}

func (l *linter) customerAgentJob(root *yaml.Node, agent customerAgent, texts []string) {
	job := agent.job
	permissions := field(job.value, "permissions")
	if permissions == nil {
		permissions = field(root, "permissions")
	}
	granted := grants(permissions)
	if granted["id-token"] == "write" {
		l.flag(job.key, High, RuleAgentIDToken, "job %s holds id-token: write, so an injected agent can mint a token that claims this repository's identity", job.key.Value)
	}
	open := agentOpenTo(agent.step)
	if len(texts) > 0 && !authorTest.MatchString(scalarField(job.value, "if")) && !actionGuards(agent.step, open) {
		l.flag(job.key, High, RuleCommentTrigger, "%s can start job %s, and its if tests neither author_association nor the actor, so nothing in the workflow limits who starts the agent on your key", strings.Join(texts, ", "), job.key.Value)
	}
	for _, step := range sequence(field(job.value, "steps")) {
		if uses := field(step, "uses"); uses != nil && !customerPinned(uses.Value) {
			l.flag(uses, Medium, RuleUnpinned, "%s is not pinned to a full commit SHA, so whoever moves the tag changes what runs beside your key", uses.Value)
		}
	}
	if match := gitHubForbidden.FindString(text(field(job.value, "steps"))); match != "" {
		l.flag(job.key, Medium, RuleAgentPromptInput, "%s reaches the agent's job, and a pull request's author or a comment writes it", match)
	}
	for _, input := range open {
		if severity := openSeverity(agent.step, input); severity != "" {
			l.flag(input.value, severity, RuleAgentOpen, "%s: %s lets %s start the agent on your key", input.key.Value, input.value.Value, openedTo(input))
		}
	}
	switch {
	case permissions == nil:
		l.flag(job.key, Note, RuleAgentContentWrite, "job %s declares no permissions, so it holds the repository's default token, which can write to your code", job.key.Value)
	case granted["contents"] == "write" || granted["*"] == "write":
		l.flag(job.key, Note, RuleAgentContentWrite, "job %s can write to your code: an injected agent can push to the pull request's branch", job.key.Value)
	}
	if node := subscriptionToken(root, job, agent.step); node != nil {
		l.flag(node, Note, RuleSubscriptionToken, "the agent signs in with a Claude subscription's token, so Costroid reports its tokens at API list price as list-equivalent, never as billed")
	}
}

// customerPinned accepts a full commit SHA, an image digest, and an action in
// this repository, which runs at the workflow's own commit.
func customerPinned(ref string) bool {
	return shaPin.MatchString(ref) || digestPin.MatchString(ref) || strings.HasPrefix(ref, "./")
}

// actionGuards reports whether the agent's action itself refuses a run that
// someone without write access, or a bot, starts. claude-code-action does so
// while allowed_non_write_users and allowed_bots are both empty, by its
// action.yml as read on 2026-11-07. codex-action's check is not read, so it
// never counts.
func actionGuards(step *yaml.Node, open []entry) bool {
	return agentAction(step) == claudeCodeAction && len(open) == 0
}

// openSeverity ranks an opening input. On claude-code-action "*" admits
// anyone and is high, and a list admits those it names and is medium. On
// codex-action, whose inputs are unread, "*" on allow-users and any allow-bots
// are medium, and a list of users is not flagged.
func openSeverity(step *yaml.Node, input entry) Severity {
	value := strings.TrimSpace(input.value.Value)
	switch {
	case agentAction(step) == claudeCodeAction && value == "*":
		return High
	case agentAction(step) == claudeCodeAction:
		return Medium
	case input.key.Value == "allow-users" && value != "*":
		return ""
	}
	return Medium
}

// openedTo says who an opening input admits.
func openedTo(input entry) string {
	kind := "account"
	if strings.Contains(input.key.Value, "bot") {
		kind = "bot"
	}
	if strings.TrimSpace(input.value.Value) == "*" {
		return "any " + kind
	}
	return "each " + kind + " it names"
}

// subscriptionToken finds a subscription's token on the agent's step or in
// the environment it runs in.
func subscriptionToken(root *yaml.Node, job entry, step *yaml.Node) *yaml.Node {
	if input := field(field(step, "with"), subscriptionInput); input != nil {
		return input
	}
	for _, env := range []*yaml.Node{field(step, "env"), field(job.value, "env"), field(root, "env")} {
		for _, item := range entries(env) {
			if item.key.Value == subscriptionVariable {
				return item.key
			}
		}
	}
	return nil
}
