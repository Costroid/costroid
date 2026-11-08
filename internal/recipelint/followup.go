package recipelint

import (
	"maps"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// A follow-up is a workflow_run workflow. It runs in the base repository with
// that repository's token, after a workflow a fork's pull request could edit,
// so it holds no code and runs nothing that workflow left behind.
const (
	// RuleFollowUpPermissions holds a follow-up's token to what it needs.
	RuleFollowUpPermissions = "follow-up-permissions"

	followUpEvent    = "workflow_run"
	downloadArtifact = "actions/download-artifact"
	uploadArtifact   = "actions/upload-artifact"
	// keptRowsName is the artifact the action's ingest stage fetches.
	keptRowsName   = "costroid-rows"
	checkoutAction = "actions/checkout"
)

var (
	// followUpHead names the pull request's head inside a follow-up. The
	// command reads the head from the event file and never fetches it.
	followUpHead = regexp.MustCompile(`github\.event\.workflow_run\.(head_sha|head_branch|head_commit|head_repository|pull_requests)\b|github\.event\.pull_request\b|github\.head_ref\b`)

	// fetchCommand brings code onto the runner.
	fetchCommand = regexp.MustCompile(`\bgit\s+(checkout|switch|fetch|pull|clone|worktree)\b|\bgh\s+(pr\s+checkout|repo\s+clone)\b`)

	// runnerDirectory names a directory a downloaded artifact can sit in.
	runnerDirectory = regexp.MustCompile(`\$\{\{\s*(runner\.temp|github\.workspace)\s*\}\}|\$\{?(RUNNER_TEMP|GITHUB_WORKSPACE)\}?`)

	// followUpCommand is the step that posts the check.
	followUpCommand = regexp.MustCompile(`\bcostroid\s+follow-up\b`)

	// readLines are the lines that may name the artifact: costroid follow-up
	// reads the rows file in it as data, and costroid ingest-run reads a gh-aw
	// usage artifact as data and writes rows outside it.
	readLines = []*regexp.Regexp{
		regexp.MustCompile(`^\s*costroid\s+follow-up\s+(--ingested\s+)?--rows\s+` + runnerPath + `\s*$`),
		regexp.MustCompile(`^\s*costroid\s+ingest-run\s+--from\s+gh-aw=` + runnerPath + `\s+--out\s+` + runnerPath + `\s*$`),
	}

	// keptRows is the one upload a follow-up may make from the runner's
	// directory: the rows and the run's object ingest-run wrote, under the name
	// the action's ingest stage fetches.
	keptRows = regexp.MustCompile(`^\$\{\{\s*runner\.temp\s*\}\}/costroid-rows\.(jsonl|run\.json)$`)
)

// runnerPath is a path under the runner's directory, quoted or not.
const runnerPath = `"?(\$\{?RUNNER_TEMP\}?/)?[A-Za-z0-9._/-]+"?`

// isFollowUp reports a workflow that a workflow_run event starts.
func isFollowUp(on *yaml.Node) bool {
	return slices.Contains(eventNames(on), followUpEvent)
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
		l.followUpPermissions(root, job, steps)
		artifacts := l.artifactDirectories(steps)
		for _, step := range steps {
			l.followUpStep(step, artifacts)
		}
	}
}

// followUpPermissions holds a follow-up job to one write, checks, because it
// runs with the base repository's token after a workflow a fork can edit. A
// job whose one step is the action's ingest stage may also write id-token,
// which proves the run to the ledger. The job that posts reads the head
// commit's pull requests, so it needs pull-requests: read, without which
// costroid follow-up posts nothing.
func (l *linter) followUpPermissions(root *yaml.Node, job entry, steps []*yaml.Node) {
	permissions := field(job.value, "permissions")
	if permissions == nil {
		permissions = field(root, "permissions")
	}
	granted := grants(permissions)
	for _, scope := range slices.Sorted(maps.Keys(granted)) {
		if granted[scope] != "write" || scope == "checks" || scope == "id-token" && sendsRowsAlone(steps) {
			continue
		}
		held := scope + ": write"
		if scope == "*" {
			held = "write-all"
		}
		l.add(job.key, RuleFollowUpPermissions, "job %s holds %s, and a follow-up writes checks alone", job.key.Value, held)
	}
	posts := false
	for _, step := range steps {
		if run := field(step, "run"); run != nil && followUpCommand.MatchString(run.Value) {
			posts = true
		}
	}
	if posts && granted["pull-requests"] != "read" && granted["*"] != "read" {
		l.add(job.key, RuleFollowUpPermissions, "job %s runs costroid follow-up without pull-requests: read, so it can bind the check to no pull request", job.key.Value)
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
	if ref == downloadArtifact || ref == uploadArtifact && uploadsKeptRows(step) {
		return
	}
	for _, name := range []string{"with", "env"} {
		if value := field(step, name); value != nil && namesArtifact(text(value), artifacts) {
			l.add(value, RuleFollowUpArtifact, "a step's %s names the finished run's files: only costroid follow-up --rows and costroid ingest-run --from gh-aw read them", name)
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
		if namesArtifact(line, artifacts) && !slices.ContainsFunc(readLines, func(read *regexp.Regexp) bool { return read.MatchString(line) }) {
			l.add(run, RuleFollowUpArtifact, "a script names the finished run's files: only costroid follow-up --rows and costroid ingest-run --from gh-aw read them")
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

// uploadsKeptRows reports an upload of the rows and the run's object alone,
// under the name the action's ingest stage fetches.
func uploadsKeptRows(step *yaml.Node) bool {
	with := field(step, "with")
	if scalarField(with, "name") != keptRowsName {
		return false
	}
	paths := strings.Split(strings.TrimSpace(scalarField(with, "path")), "\n")
	return !slices.ContainsFunc(paths, func(path string) bool { return !keptRows.MatchString(strings.TrimSpace(path)) })
}

// sendsRowsAlone reports a job whose one step is the action's ingest stage.
func sendsRowsAlone(steps []*yaml.Node) bool {
	if len(steps) != 1 {
		return false
	}
	ref, _, _ := strings.Cut(scalarField(steps[0], "uses"), "@")
	return strings.EqualFold(ref, costroidAction) && scalarField(field(steps[0], "with"), "stage") == "ingest"
}
