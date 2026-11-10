package recipelint

import (
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Costroid/costroid/internal/cicontext"
)

// The rules only a GitLab template can break.
const (
	RuleReportLabel     = "report-label"
	RuleCosignCommit    = "cosign-commit"
	RuleReportExitCodes = "report-exit-codes"
)

// neutralExit is the exit code of costroid report --exit-on-verdict on a
// neutral verdict, the one code the report's job may fail on and pass.
const neutralExit = "3"

var (
	imageDigest = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)
	fullSHA     = regexp.MustCompile(`^[0-9a-f]{40}$`)

	gitLabForbidden = regexp.MustCompile(`CI_MERGE_REQUEST_(TITLE|DESCRIPTION)\b|CI_COMMIT_(MESSAGE|TITLE|DESCRIPTION|BRANCH|REF_NAME)\b|/notes\b`)

	// gitLabReserved are the top-level keys that are not jobs.
	gitLabReserved = map[string]bool{
		"after_script": true, "before_script": true, "cache": true, "default": true,
		"image": true, "include": true, "services": true, "spec": true,
		"stages": true, "variables": true, "workflow": true,
	}
)

// gitLabGuards name what a job's rules must test before an agent runs: the fork
// and the maintainer's label.
var gitLabGuards = []string{"CI_MERGE_REQUEST_SOURCE_PROJECT_ID", "CI_MERGE_REQUEST_LABELS"}

const (
	// gitLabNoteToken writes to the merge request, so it stays out of every job
	// that runs an agent.
	gitLabNoteToken = "COSTROID_GITLAB_TOKEN"
	gitLabForkGuard = "CI_MERGE_REQUEST_SOURCE_PROJECT_ID"
	gitStrategy     = "GIT_STRATEGY"
)

var (
	reportCommand = regexp.MustCompile(`\bcostroid\s+report\b`)
	// gitLabProviderKeys are what an agent spends, and a report has no use for.
	gitLabProviderKeys = []string{"ANTHROPIC_API_KEY", "CODEX_API_KEY"}
	// gitLabCodeFree are the strategies that leave no checkout in the job.
	gitLabCodeFree = map[string]bool{"none": true, "empty": true}

	// gitLabLabel reads the pattern a rule holds the merge request's labels to.
	gitLabLabel = regexp.MustCompile(`\$CI_MERGE_REQUEST_LABELS\s*[=!]~\s*(/(?:[^/\\]|\\.)*/)`)

	// releaseWorkflow is the identity Costroid's releases are signed under.
	releaseWorkflow = "Costroid/costroid/.github/workflows/release.yml"
	// workflowSHA reads the commit a cosign check binds: a variable or a SHA.
	workflowSHA = regexp.MustCompile(`--certificate-github-workflow-sha[= ]+"?(?:\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?|([0-9a-f]{40}))`)
)

func isGitLab(root *yaml.Node) bool {
	for _, item := range entries(root) {
		if gitLabReserved[item.key.Value] {
			continue
		}
		if field(item.value, "script") != nil || field(item.value, "extends") != nil {
			return true
		}
	}
	return false
}

func (l *linter) gitlab(root *yaml.Node) {
	l.images(root)
	l.images(field(root, "default"))
	l.includes(field(root, "include"))
	for _, item := range entries(root) {
		if gitLabReserved[item.key.Value] {
			continue
		}
		l.images(item.value)
		if !strings.HasPrefix(item.key.Value, ".") {
			l.gitlabJob(root, item)
		}
	}
	l.gitlabCaps(root)
}

func (l *linter) images(holder *yaml.Node) {
	image := field(holder, "image")
	if image != nil {
		l.image(image)
	}
	for _, service := range sequence(field(holder, "services")) {
		l.image(service)
	}
}

func (l *linter) image(node *yaml.Node) {
	name := node.Value
	if node.Kind == yaml.MappingNode {
		name = scalarField(node, "name")
	}
	if !imageDigest.MatchString(name) {
		l.add(node, RuleUnpinned, "image %s is not pinned to a digest", name)
	}
}

func (l *linter) includes(include *yaml.Node) {
	for _, item := range sequence(include) {
		if item.Kind == yaml.ScalarNode {
			if !strings.HasPrefix(item.Value, "/") {
				l.add(item, RuleUnpinned, "include %s has no pin", item.Value)
			}
			continue
		}
		switch {
		case field(item, "local") != nil:
		case field(item, "project") != nil && fullSHA.MatchString(scalarField(item, "ref")):
		case field(item, "remote") != nil && field(item, "integrity") != nil:
		default:
			l.add(item, RuleUnpinned, "an include must be local, a project at a full SHA, or remote with integrity")
		}
	}
}

// chain lists a job and everything it extends, the job first.
func chain(root *yaml.Node, job *yaml.Node) []*yaml.Node {
	members := []*yaml.Node{job}
	seen := map[*yaml.Node]bool{job: true}
	for i := 0; i < len(members); i++ {
		for _, parent := range sequence(field(members[i], "extends")) {
			if target := field(root, parent.Value); target != nil && !seen[target] {
				seen[target] = true
				members = append(members, target)
			}
		}
	}
	return members
}

func firstField(members []*yaml.Node, key string) *yaml.Node {
	for _, member := range members {
		if value := field(member, key); value != nil {
			return value
		}
	}
	return nil
}

func (l *linter) gitlabJob(root *yaml.Node, job entry) {
	members := chain(root, job.value)
	body := jobBody(members)
	l.cosignCommit(job, body, mergeVariables(variableLayers(root, members)...))
	if reportCommand.MatchString(body) {
		l.gitlabReport(root, job, members)
	}
	agents := agentsNamed(body)
	if len(agents) == 0 {
		return
	}
	defaults := field(root, "default")
	if holdsIDToken(defaults, members) {
		l.add(job.key, RuleAgentIDToken, "the agent's job must not hold id_tokens")
	}
	rules := text(firstField(members, "rules"))
	for _, want := range gitLabGuards {
		if !strings.Contains(rules, want) {
			l.add(job.key, RuleAgentGuard, "the job's rules must test %s", want)
		}
	}
	layers := variableLayers(root, members)
	variables := mergeVariables(layers...)
	held := body + "\n" + text(layers...)
	if strings.Contains(held, gitLabNoteToken) {
		l.add(job.key, RuleAgentNoteToken, "%s reaches the agent's job, and it can write to the merge request", gitLabNoteToken)
	}
	for _, agent := range agents {
		l.checkAgent(agentRun{
			node:    job.key,
			agent:   agent,
			text:    held,
			env:     variables,
			minimum: minimumVersionVariable,
		}, gitLabForbidden)
	}
	if agent := agentOutsideRun(body); agent != "" {
		if missing := missingJoinAttributes(variables[cicontext.ResourceAttributesVariable]); len(missing) > 0 {
			l.add(job.key, RuleJoinAttributes, "%s", joinAttributesMessage(agent, "job", missing))
		}
	}
}

func jobBody(members []*yaml.Node) string {
	return text(firstField(members, "before_script"), firstField(members, "script"))
}

func holdsIDToken(defaults *yaml.Node, members []*yaml.Node) bool {
	return firstField(members, "id_tokens") != nil || field(defaults, "id_tokens") != nil
}

// variableLayers lists every mapping a job's variables come from, weakest first.
func variableLayers(root *yaml.Node, members []*yaml.Node) []*yaml.Node {
	layers := []*yaml.Node{field(root, "variables"), field(field(root, "default"), "variables")}
	for i := len(members) - 1; i >= 0; i-- {
		layers = append(layers, field(members[i], "variables"))
	}
	return layers
}

func environmentName(members []*yaml.Node) string {
	environment := firstField(members, "environment")
	if environment != nil && environment.Kind == yaml.ScalarNode {
		return environment.Value
	}
	return scalarField(environment, "name")
}

// gitlabReport holds the job that carries the note token. It gets the rows from
// the agent's job and nothing else: no code, no provider key, no id token.
func (l *linter) gitlabReport(root *yaml.Node, job entry, members []*yaml.Node) {
	if len(agentsNamed(jobBody(members))) > 0 {
		l.add(job.key, RuleReportJob, "the report must run in a job of its own, apart from the agent")
	}
	if holdsIDToken(field(root, "default"), members) {
		l.add(job.key, RuleReportJob, "the report's job must not hold id_tokens")
	}
	if !strings.Contains(text(firstField(members, "rules")), gitLabForkGuard) {
		l.add(job.key, RuleReportJob, "the report's rules must test %s, so a fork never reaches the token", gitLabForkGuard)
	}
	if firstField(members, "needs") == nil || firstField(members, "cache") != nil || field(field(root, "default"), "cache") != nil {
		l.add(job.key, RuleReportJob, "the report must take the rows through needs, and hold no cache")
	}
	layers := variableLayers(root, members)
	if !gitLabCodeFree[mergeVariables(layers...)[gitStrategy]] {
		l.add(job.key, RuleReportJob, "the report's job must set %s to none or empty, so no pull request code is on disk", gitStrategy)
	}
	held := jobBody(members) + "\n" + text(layers...)
	for _, key := range gitLabProviderKeys {
		if strings.Contains(held, key) {
			l.add(job.key, RuleReportJob, "%s reaches the report's job", key)
		}
	}
	l.reportEnvironment(root, job, environmentName(members))
	l.reportLabel(root, job, members)
	l.reportExitCodes(members)
}

// reportExitCodes holds the codes the report's job may fail on to a neutral
// verdict's. Any other code there would let a failure verdict, exit 1, or a
// report that could not run pass the pipeline as a warning.
func (l *linter) reportExitCodes(members []*yaml.Node) {
	codes := field(firstField(members, "allow_failure"), "exit_codes")
	if codes == nil {
		return
	}
	var listed []string
	for _, code := range sequence(codes) {
		listed = append(listed, code.Value)
	}
	if len(listed) != 1 || listed[0] != neutralExit {
		l.add(codes, RuleReportExitCodes, "the report's job may fail on exit codes [%s]: list %s alone, so a failure verdict, exit 1, fails the pipeline", strings.Join(listed, ", "), neutralExit)
	}
}

// reportLabel holds the report to the label the agent's jobs wait for. A
// report that runs without it posts on every merge request a note about a run
// that never started.
func (l *linter) reportLabel(root *yaml.Node, job entry, members []*yaml.Node) {
	held := map[string]bool{}
	for _, match := range gitLabLabel.FindAllStringSubmatch(text(firstField(members, "rules")), -1) {
		held[match[1]] = true
	}
	for _, label := range agentLabels(root) {
		if !held[label] {
			l.add(job.key, RuleReportLabel, "the report's rules must test the label the agent's jobs test, %s, or a merge request without it gets a note about a run that never started", label)
		}
	}
}

// agentLabels lists, once each, the label patterns the agent's jobs test.
func agentLabels(root *yaml.Node) []string {
	var labels []string
	seen := map[string]bool{}
	for _, other := range entries(root) {
		if gitLabReserved[other.key.Value] || strings.HasPrefix(other.key.Value, ".") {
			continue
		}
		members := chain(root, other.value)
		if len(agentsNamed(jobBody(members))) == 0 {
			continue
		}
		for _, match := range gitLabLabel.FindAllStringSubmatch(text(firstField(members, "rules")), -1) {
			if !seen[match[1]] {
				seen[match[1]] = true
				labels = append(labels, match[1])
			}
		}
	}
	return labels
}

// cosignCommit holds a check of Costroid's release signature to one commit. The
// identity names the tag, and a tag can be moved; the commit cannot.
func (l *linter) cosignCommit(job entry, body string, variables map[string]string) {
	if !strings.Contains(body, "verify-blob") || !strings.Contains(body, releaseWorkflow) {
		return
	}
	match := workflowSHA.FindStringSubmatch(body)
	if match == nil {
		l.add(job.key, RuleCosignCommit, "cosign checks the release's tag and not its commit: pass --certificate-github-workflow-sha")
		return
	}
	commit := match[2]
	if match[1] != "" {
		commit = variables[match[1]]
	}
	if !fullSHA.MatchString(commit) {
		l.add(job.key, RuleCosignCommit, "--certificate-github-workflow-sha reads %q, not a full commit SHA", commit)
	}
}

// reportEnvironment holds the note token to the report. The token is scoped to
// this environment, so an agent's job in it would hold the token too.
func (l *linter) reportEnvironment(root *yaml.Node, job entry, name string) {
	if name == "" {
		l.add(job.key, RuleReportJob, "the report's job must name an environment, the only one %s is scoped to", gitLabNoteToken)
		return
	}
	for _, other := range entries(root) {
		if gitLabReserved[other.key.Value] || strings.HasPrefix(other.key.Value, ".") {
			continue
		}
		members := chain(root, other.value)
		if len(agentsNamed(jobBody(members))) > 0 && environmentName(members) == name {
			l.add(job.key, RuleReportJob, "the report's environment %s is also the agent's", name)
		}
	}
}
