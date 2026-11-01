package recipelint

import (
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

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
	body := text(firstField(members, "before_script"), firstField(members, "script"))
	agents := agentsNamed(body)
	if len(agents) == 0 {
		return
	}
	defaults := field(root, "default")
	if firstField(members, "id_tokens") != nil || field(defaults, "id_tokens") != nil {
		l.add(job.key, RuleAgentIDToken, "the agent's job must not hold id_tokens")
	}
	rules := text(firstField(members, "rules"))
	for _, want := range gitLabGuards {
		if !strings.Contains(rules, want) {
			l.add(job.key, RuleAgentGuard, "the job's rules must test %s", want)
		}
	}
	layers := []*yaml.Node{field(root, "variables"), field(defaults, "variables")}
	for i := len(members) - 1; i >= 0; i-- {
		layers = append(layers, field(members[i], "variables"))
	}
	variables := mergeVariables(layers...)
	for _, agent := range agents {
		l.checkAgent(agentRun{
			node:    job.key,
			agent:   agent,
			text:    body + "\n" + text(layers...),
			env:     variables,
			minimum: minimumVersionVariable,
		}, gitLabForbidden)
	}
}
