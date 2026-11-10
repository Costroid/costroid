package recipelint

import (
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Costroid/costroid/internal/cicontext"
)

// RuleJoinAttributes names an agent that costroid run does not start, such as
// claude-code-action's, whose environment does not give its telemetry the
// pull request's join keys. costroid run sets them for every agent it starts.
const RuleJoinAttributes = "join-attributes"

// joinAttributes are the OpenTelemetry vcs.* and cicd.* attributes costroid
// run sets in OTEL_RESOURCE_ATTRIBUTES, in the order it writes them.
var joinAttributes = []string{
	"cicd.pipeline.run.id",
	"cicd.pipeline.task.run.id",
	"vcs.change.id",
	"vcs.ref.base.revision",
	"vcs.ref.head.revision",
	"vcs.repository.url.full",
}

// missingJoinAttributes lists the join attributes an OTEL_RESOURCE_ATTRIBUTES
// value does not set, or sets to nothing.
func missingJoinAttributes(value string) []string {
	set := map[string]bool{}
	for pair := range strings.SplitSeq(value, ",") {
		key, attribute, _ := strings.Cut(pair, "=")
		set[strings.TrimSpace(key)] = strings.TrimSpace(attribute) != ""
	}
	return slices.DeleteFunc(slices.Clone(joinAttributes), func(key string) bool { return set[key] })
}

// joinAttributesMessage says what the step or the job that starts an agent
// lacks, and where the values are listed.
func joinAttributesMessage(agent, holder string, missing []string) string {
	return fmt.Sprintf("%s starts outside costroid run, so its %s must set %s in %s, or its telemetry names no pull request: docs/adapters.md#join-attributes lists the values",
		agent, holder, andList(missing), cicontext.ResourceAttributesVariable)
}

// stepAgentOutsideRun names the agent a GitHub step starts without costroid
// run, or nothing: the agent of claude-code-action or codex-action, or one a
// script starts by its command on a line costroid run does not wrap.
// Costroid's action starts its agent under costroid run.
func stepAgentOutsideRun(step *yaml.Node) string {
	switch {
	case agentAction(step) != "":
		return stepAgent(step)
	case costroidStage(step) != "":
		return ""
	}
	return agentOutsideRun(scalarField(step, "run"))
}

// agentOutsideRun names the agent a script starts with claude -p or codex exec
// on a line that is not a costroid run command, or nothing. A backslash at a
// line's end continues the command.
func agentOutsideRun(script string) string {
	for line := range strings.SplitSeq(strings.ReplaceAll(script, "\\\n", " "), "\n") {
		switch {
		case costroidRun.MatchString(line):
		case claudeCommand.MatchString(line):
			return claudeAgent
		case codexCommand.MatchString(line):
			return codexAgent
		}
	}
	return ""
}

// gitHubJoinAttributes calls report for each agent step of a GitHub job that
// starts its agent outside costroid run and lacks a join attribute in the
// environment the step runs in.
func gitHubJoinAttributes(root, job *yaml.Node, report func(step *yaml.Node, message string)) {
	for _, step := range sequence(field(job, "steps")) {
		agent := stepAgentOutsideRun(step)
		if agent == "" {
			continue
		}
		env := mergeVariables(field(root, "env"), field(job, "env"), field(step, "env"))
		if missing := missingJoinAttributes(env[cicontext.ResourceAttributesVariable]); len(missing) > 0 {
			report(step, joinAttributesMessage(agent, "step", missing))
		}
	}
}

// andList joins names as a sentence does: a, b and c.
func andList(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
