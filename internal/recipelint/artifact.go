package recipelint

import (
	"path"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// RuleArtifactPath is a download that unpacks an artifact into the runner's
// temporary directory itself. Costroid's binary and the event file sit there,
// and the job that uploaded the artifact chose every name in it.
const RuleArtifactPath = "artifact-path"

// RuleAgentCheckoutCredentials is a checkout in an agent's job that leaves the
// job's token in the clone's git config, where the agent can read it.
const RuleAgentCheckoutCredentials = "agent-checkout-credentials"

// runnerTemp names the runner's temporary directory at the start of a path.
var runnerTemp = regexp.MustCompile(`^(\$\{\{\s*runner\.temp\s*\}\}|\$RUNNER_TEMP\b|\$\{RUNNER_TEMP\})`)

// downloadPath refuses a download whose path is runner.temp, or a path that
// cleans to it or above it.
func (l *linter) downloadPath(step *yaml.Node) {
	ref, _, _ := strings.Cut(strings.ToLower(scalarField(step, "uses")), "@")
	if ref != downloadArtifact {
		return
	}
	target := strings.TrimSpace(scalarField(field(step, "with"), "path"))
	prefix := runnerTemp.FindString(target)
	rest := target[len(prefix):]
	if prefix == "" || (rest != "" && !strings.HasPrefix(rest, "/")) {
		return
	}
	if inside := path.Clean(strings.TrimLeft(rest, "/")); inside == "." || inside == ".." || strings.HasPrefix(inside, "../") {
		l.add(step, RuleArtifactPath, "the download unpacks into runner.temp itself or above it, where costroid and the event file sit: give it a directory of its own under runner.temp")
	}
}

// agentCheckout refuses a checkout in an agent's job unless it sets
// persist-credentials: false.
func (l *linter) agentCheckout(job entry) {
	for _, step := range sequence(field(job.value, "steps")) {
		ref, _, _ := strings.Cut(strings.ToLower(scalarField(step, "uses")), "@")
		if ref != checkoutAction {
			continue
		}
		if !strings.EqualFold(scalarField(field(step, "with"), "persist-credentials"), "false") {
			l.add(step, RuleAgentCheckoutCredentials, "the agent's checkout must set persist-credentials: false, or the job's token stays in .git/config where the agent can read it")
		}
	}
}
