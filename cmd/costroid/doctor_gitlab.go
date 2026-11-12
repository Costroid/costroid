package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Costroid/costroid/internal/recipelint"
	"go.yaml.in/yaml/v3"
)

// gitlabInitFix is what doctor sends a GitLab project with no template to.
const gitlabInitFix = "run costroid init gitlab --agent claude"

// gitlabTemplate is a GitLab template that installs Costroid: the release and
// the commit it pins, each with its line, and the agent it runs.
type gitlabTemplate struct {
	path            string
	source          []byte
	version, commit templateValue
	agent           string
}

// templateValue is a variable's value in a template, and the line it is on.
type templateValue struct {
	value string
	line  int
}

// templateVariable finds the variables of a template doctor reads.
var templateVariable = regexp.MustCompile(`(?m)^ +COSTROID_(VERSION|COMMIT|AGENT): *(.*?) *$`)

// readGitLabTemplates reads each file that installs Costroid on GitLab: the
// one init writes, and each local file .gitlab-ci.yml includes. A file that
// sets neither COSTROID_VERSION nor COSTROID_COMMIT is not one.
func readGitLabTemplates(root string) ([]gitlabTemplate, error) {
	var templates []gitlabTemplate
	for _, name := range gitlabTemplatePaths(root) {
		source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		template := gitlabTemplate{path: name, source: source}
		for _, match := range templateVariable.FindAllSubmatchIndex(source, -1) {
			value := templateValue{value: strings.Trim(string(source[match[4]:match[5]]), `"'`), line: 1 + strings.Count(string(source[:match[0]]), "\n")}
			switch string(source[match[2]:match[3]]) {
			case "VERSION":
				template.version = value
			case "COMMIT":
				template.commit = value
			case "AGENT":
				template.agent = value.value
			}
		}
		if template.version.line > 0 || template.commit.line > 0 {
			templates = append(templates, template)
		}
	}
	return templates, nil
}

// gitlabTemplatePaths are the template init writes, then each local file
// .gitlab-ci.yml includes, by path from the root.
func gitlabTemplatePaths(root string) []string {
	paths := []string{initOut[gitlabHost]}
	source, err := os.ReadFile(filepath.Join(root, gitlabCI))
	if err != nil {
		return paths
	}
	top, err := pipelineRoot(source)
	if err != nil {
		return paths
	}
	for _, local := range localIncludes(field(top, "include")) {
		if !slices.Contains(paths, local) {
			paths = append(paths, local)
		}
	}
	return paths
}

// localIncludes lists the files of the repository an include names.
func localIncludes(include *yaml.Node) []string {
	if include == nil {
		return nil
	}
	var name string
	switch include.Kind {
	case yaml.SequenceNode:
		var names []string
		for _, item := range include.Content {
			names = append(names, localIncludes(item)...)
		}
		return names
	case yaml.MappingNode:
		local := field(include, "local")
		if local == nil {
			return nil
		}
		name = local.Value
	case yaml.ScalarNode:
		name = include.Value
	}
	name = path.Clean(strings.TrimPrefix(name, "/"))
	if name == "." || strings.Contains(name, "://") || strings.HasPrefix(name, "../") {
		return nil
	}
	return []string{name}
}

// onGitLab says whether the repository runs Costroid on GitLab: a template
// installs it and no workflow calls Costroid's action, or no workflow calls it
// and the remote origin is on GitLab or the repository has .gitlab-ci.yml and
// no workflow.
func onGitLab(ctx context.Context, root string, host doctorHost, flows []workflow, templates []gitlabTemplate) bool {
	if slices.ContainsFunc(flows, func(flow workflow) bool { return len(flow.costroid) > 0 }) {
		return false
	}
	if len(templates) > 0 || originOnGitLab(ctx, root, host) {
		return true
	}
	_, err := os.Stat(filepath.Join(root, gitlabCI))
	return err == nil && len(flows) == 0
}

// originOnGitLab reads the address of the remote origin with git, when git
// is on PATH.
func originOnGitLab(ctx context.Context, root string, host doctorHost) bool {
	path, err := host.lookPath("git")
	if err != nil {
		return false
	}
	address, err := host.output(ctx, root, path, "remote", "get-url", "origin")
	return err == nil && strings.Contains(remoteServer(strings.TrimSpace(string(address))), gitlabHost)
}

// gitlabLines hold each template to the rules of lint-recipes and check its
// pins, as workflowLines and pinLines do on GitHub. On GitLab, a repository
// with no template is sent to init gitlab.
func gitlabLines(templates []gitlabTemplate, gitlab bool) []finding {
	if gitlab && len(templates) == 0 {
		return []finding{warn("no GitLab template installs Costroid", gitlabInitFix)}
	}
	var lines []finding
	for _, template := range templates {
		report := recipelint.LintBytes(template.path, template.source)
		for _, problem := range report.Findings {
			lines = append(lines, warn(problem.String(), "docs/recipes.md#what-the-recipes-hold-to says what the rule guards"))
		}
		if len(report.Findings) == 0 {
			lines = append(lines, ok("%s: costroid lint-recipes finds nothing", template.path))
		}
		lines = append(lines, templatePinLines(template)...)
	}
	return lines
}

// templatePinLines hold a template to a release's version and the full
// commit SHA its tag names: the template checks the binary it installs
// against both.
func templatePinLines(template gitlabTemplate) []finding {
	const fix = "set COSTROID_VERSION to a release and COSTROID_COMMIT to the full commit SHA its tag names, as costroid init gitlab writes them"
	version, commit := template.version, template.commit
	at := func(value templateValue) string { return fmt.Sprintf("%s:%d", template.path, value.line) }
	var lines []finding
	switch {
	case version.line == 0:
		lines = append(lines, fail(template.path+" sets no COSTROID_VERSION", fix))
	case !releaseVersion.MatchString(version.value):
		lines = append(lines, fail(at(version)+": COSTROID_VERSION "+version.value+" names no release", fix))
	}
	switch {
	case commit.line == 0:
		lines = append(lines, fail(template.path+" sets no COSTROID_COMMIT", fix))
	case !fullSHA.MatchString(commit.value):
		lines = append(lines, fail(at(commit)+": COSTROID_COMMIT "+commit.value+" is not a full commit SHA", fix))
	case strings.Trim(commit.value, "0") == "":
		lines = append(lines, fail(at(commit)+": COSTROID_COMMIT is the zero SHA, which names no release", fix))
	}
	if len(lines) == 0 {
		lines = append(lines, ok("%s: Costroid pinned to %s at the commit COSTROID_COMMIT names", at(version), version.value))
	}
	return lines
}

// gitlabVariableLines say what doctor does not read on GitLab: the CI/CD
// variables, which no gh lists.
func gitlabVariableLines(templates []gitlabTemplate) []finding {
	if len(templates) == 0 {
		return nil
	}
	key := gitlabKeys[templates[0].agent]
	if key == "" {
		key = gitlabKeys["claude"]
	}
	return []finding{ok("on GitLab doctor reads no CI/CD variables; the template needs %s scoped to the environment costroid-agent and %s to costroid-report, as docs/recipes.md#give-the-report-a-token says", key, gitLabTokenVariable)}
}
