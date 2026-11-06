package recipelint

import (
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// A follow-up is a workflow_run workflow. It runs in the base repository with
// that repository's token, after a workflow a fork's pull request could edit,
// so it holds no code and runs nothing that workflow left behind.
const (
	followUpEvent    = "workflow_run"
	downloadArtifact = "actions/download-artifact"
	checkoutAction   = "actions/checkout"
)

var (
	// followUpHead names the pull request's head inside a follow-up. The
	// command reads the head from the event file and never fetches it.
	followUpHead = regexp.MustCompile(`github\.event\.workflow_run\.(head_sha|head_branch|head_commit|head_repository|pull_requests)\b|github\.event\.pull_request\b|github\.head_ref\b`)

	// fetchCommand brings code onto the runner.
	fetchCommand = regexp.MustCompile(`\bgit\s+(checkout|switch|fetch|pull|clone|worktree)\b|\bgh\s+(pr\s+checkout|repo\s+clone)\b`)

	// runnerDirectory names a directory a downloaded artifact can sit in.
	runnerDirectory = regexp.MustCompile(`\$\{\{\s*(runner\.temp|github\.workspace)\s*\}\}|\$\{?(RUNNER_TEMP|GITHUB_WORKSPACE)\}?`)

	// rowsLine is the one line that may name the artifact: costroid reads the
	// rows file in it as data.
	rowsLine = regexp.MustCompile(`^\s*costroid\s+follow-up\s+--rows\s+"?(\$\{?RUNNER_TEMP\}?/)?[A-Za-z0-9._/-]+"?\s*$`)
)

// isFollowUp reports a workflow that a workflow_run event starts.
func isFollowUp(on *yaml.Node) bool {
	if on = resolve(on); on == nil {
		return false
	}
	if on.Kind == yaml.ScalarNode {
		return on.Value == followUpEvent
	}
	if pairs := entries(on); len(pairs) > 0 {
		for _, item := range pairs {
			if item.key.Value == followUpEvent {
				return true
			}
		}
		return false
	}
	for _, event := range sequence(on) {
		if event.Value == followUpEvent {
			return true
		}
	}
	return false
}

// followUp holds every job of a follow-up to its two rules: no code from the
// pull request, and nothing run from the artifact.
func (l *linter) followUp(root *yaml.Node) {
	if env := field(root, "env"); env != nil && followUpHead.MatchString(text(env)) {
		l.add(env, RuleFollowUpCode, "the workflow's env names the pull request's head: a follow-up never fetches it")
	}
	for _, job := range entries(field(root, "jobs")) {
		if env := field(job.value, "env"); env != nil && followUpHead.MatchString(text(env)) {
			l.add(env, RuleFollowUpCode, "job %s's env names the pull request's head: a follow-up never fetches it", job.key.Value)
		}
		steps := sequence(field(job.value, "steps"))
		artifacts := l.artifactDirectories(steps)
		for _, step := range steps {
			l.followUpStep(step, artifacts)
		}
	}
}

func (l *linter) followUpStep(step *yaml.Node, artifacts []string) {
	uses := scalarField(step, "uses")
	ref, _, _ := strings.Cut(strings.ToLower(uses), "@")
	switch {
	case ref == checkoutAction:
		l.add(step, RuleFollowUpCode, "a follow-up checks out no code")
	case stepAgent(step) != "":
		l.add(step, RuleFollowUpCode, "a follow-up runs no agent: it holds the base repository's token")
	case strings.HasPrefix(uses, "./") || strings.HasPrefix(uses, "../"):
		l.add(step, RuleFollowUpArtifact, "%s is an action on disk, where the finished run's files can sit", uses)
	}
	if match := followUpHead.FindString(text(step)); match != "" {
		l.add(step, RuleFollowUpCode, "%s names the pull request's head: a follow-up never fetches it", match)
	}
	if ref == downloadArtifact {
		return
	}
	for _, name := range []string{"with", "env"} {
		if value := field(step, name); value != nil && namesArtifact(text(value), artifacts) {
			l.add(value, RuleFollowUpArtifact, "a step's %s names the finished run's files: only costroid follow-up --rows reads them", name)
		}
	}
	run := field(step, "run")
	if run == nil {
		return
	}
	if fetchCommand.MatchString(run.Value) {
		l.add(run, RuleFollowUpCode, "a follow-up fetches no code")
	}
	for _, line := range strings.Split(run.Value, "\n") {
		if namesArtifact(line, artifacts) && !rowsLine.MatchString(line) {
			l.add(run, RuleFollowUpArtifact, "a script names the finished run's files: only costroid follow-up --rows reads them")
			return
		}
	}
}

// artifactDirectories lists the directory part each download names after the
// runner's own directory. A download with no path of its own lands in the
// workspace, where a later step can run it, and is refused.
func (l *linter) artifactDirectories(steps []*yaml.Node) []string {
	var directories []string
	for _, step := range steps {
		ref, _, _ := strings.Cut(strings.ToLower(scalarField(step, "uses")), "@")
		if ref != downloadArtifact {
			continue
		}
		path := strings.Trim(runnerDirectory.ReplaceAllString(scalarField(field(step, "with"), "path"), ""), "/. ")
		if path == "" {
			l.add(step, RuleFollowUpArtifact, "the download needs a path of its own under runner.temp")
			continue
		}
		directories = append(directories, path)
	}
	return directories
}

// namesArtifact reports text that names a download's directory or the
// runner's directories, where that download sits.
func namesArtifact(text string, artifacts []string) bool {
	if runnerDirectory.MatchString(text) {
		return true
	}
	for _, directory := range artifacts {
		if strings.Contains(text, directory) {
			return true
		}
	}
	return false
}
