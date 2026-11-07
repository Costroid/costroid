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
