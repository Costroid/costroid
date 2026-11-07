package recipelint

import (
	"maps"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// An approval workflow runs costroid approve on a pull request review. It
// runs with this repository's token on an event any reviewer starts, so it
// holds no code and starts no agent.
const (
	// RuleApprovalCode holds an approval workflow to its event, its token and
	// no code.
	RuleApprovalCode = "approval-code"

	approvalEvent = "pull_request_review"
)

// approveCommand is the step that posts the check with the approval.
var approveCommand = regexp.MustCompile(`\bcostroid\s+approve\b`)

// isApproval reports a workflow with a step that runs costroid approve.
func isApproval(root *yaml.Node) bool {
	for _, job := range entries(field(root, "jobs")) {
		for _, step := range sequence(field(job.value, "steps")) {
			if approveCommand.MatchString(scalarField(step, "run")) {
				return true
			}
		}
	}
	return false
}

// approval holds every job of an approval workflow to no code, no agent and
// one write, checks.
func (l *linter) approval(root *yaml.Node) {
	on := field(root, "on")
	if names := eventNames(on); !slices.Equal(names, []string{approvalEvent}) {
		node := on
		if node == nil {
			node = root
		}
		l.add(node, RuleApprovalCode, "an approval workflow runs on %s alone", approvalEvent)
	}
	for _, job := range entries(field(root, "jobs")) {
		if field(job.value, "uses") != nil {
			l.add(job.key, RuleApprovalCode, "job %s calls another workflow, and an approval workflow runs only its own steps", job.key.Value)
		}
		l.approvalPermissions(root, job)
		for _, step := range sequence(field(job.value, "steps")) {
			l.approvalStep(step)
		}
	}
}

// approvalPermissions holds a job to checks as its one write. The job that
// posts reads the pull request's reviews, so it needs pull-requests: read.
func (l *linter) approvalPermissions(root *yaml.Node, job entry) {
	permissions := field(job.value, "permissions")
	if permissions == nil {
		permissions = field(root, "permissions")
	}
	granted := grants(permissions)
	for _, scope := range slices.Sorted(maps.Keys(granted)) {
		if granted[scope] != "write" || scope == "checks" {
			continue
		}
		held := scope + ": write"
		if scope == "*" {
			held = "write-all"
		}
		l.add(job.key, RuleApprovalCode, "job %s holds %s, and an approval workflow writes checks alone", job.key.Value, held)
	}
	posts := slices.ContainsFunc(sequence(field(job.value, "steps")), func(step *yaml.Node) bool {
		return approveCommand.MatchString(scalarField(step, "run"))
	})
	if posts && granted["pull-requests"] != "read" && granted["*"] != "read" {
		l.add(job.key, RuleApprovalCode, "job %s runs costroid approve without pull-requests: read, so it cannot read the reviews", job.key.Value)
	}
}

func (l *linter) approvalStep(step *yaml.Node) {
	uses := scalarField(step, "uses")
	ref, _, _ := strings.Cut(strings.ToLower(uses), "@")
	switch {
	case ref == checkoutAction:
		l.add(step, RuleApprovalCode, "an approval workflow checks out no code")
	case stepAgent(step) != "":
		l.add(step, RuleApprovalCode, "an approval workflow runs no agent: a review would start a paid run")
	case strings.HasPrefix(uses, "./") || strings.HasPrefix(uses, "../"):
		l.add(step, RuleApprovalCode, "%s is an action on disk, and an approval workflow checks out no code", uses)
	}
	if run := field(step, "run"); run != nil && fetchCommand.MatchString(run.Value) {
		l.add(run, RuleApprovalCode, "an approval workflow fetches no code")
	}
}
