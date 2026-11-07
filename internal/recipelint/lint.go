// Package recipelint holds the shipped CI recipes to their security rules: no
// privileged trigger, every action pinned by SHA, and an agent's job that holds
// nothing but read access to the code.
package recipelint

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// The rules a finding can name.
const (
	RuleUnreadable         = "unreadable"
	RulePullRequestTarget  = "pull-request-target"
	RuleUnpinned           = "unpinned-action"
	RuleUnrecordedPin      = "unrecorded-pin"
	RuleAgentIDToken       = "agent-id-token"
	RuleIDTokenScope       = "id-token-scope"
	RulePermissions        = "explicit-permissions"
	RuleAgentPermissions   = "agent-permissions"
	RuleAgentGuard         = "agent-guard"
	RuleAgentVersion       = "agent-version"
	RuleAgentContentFlags  = "agent-content-flags"
	RuleAgentPromptInput   = "agent-prompt-input"
	RuleAgentNoteToken     = "agent-note-token"
	RuleReportJob          = "report-job"
	RuleExpressionInScript = "expression-in-script"
	RuleFollowUpCode       = "follow-up-code"
	RuleFollowUpArtifact   = "follow-up-artifact"
)

const (
	costroidAction         = "Costroid/costroid/action"
	zeroSHA                = "0000000000000000000000000000000000000000"
	claudeAgent            = "claude"
	codexAgent             = "codex"
	codexContentFlag       = "otel.log_user_prompt=false"
	minimumVersionInput    = "minimum-agent-version"
	minimumVersionVariable = "MINIMUM_AGENT_VERSION"
	maximumAliasDepth      = 64
)

// claudeContentFlags are the telemetry variables that carry prompts, responses,
// tool input and API bodies. Each must read 0.
var claudeContentFlags = []string{
	"OTEL_LOG_USER_PROMPTS",
	"OTEL_LOG_ASSISTANT_RESPONSES",
	"OTEL_LOG_TOOL_DETAILS",
	"OTEL_LOG_TOOL_CONTENT",
	"OTEL_LOG_RAW_API_BODIES",
}

var agentInstall = map[string]*regexp.Regexp{
	claudeAgent: regexp.MustCompile(`@anthropic-ai/claude-code@[0-9]+\.[0-9]+\.[0-9]+`),
	codexAgent:  regexp.MustCompile(`@openai/codex@[0-9]+\.[0-9]+\.[0-9]+`),
}

// Finding is one line of a file and the rule it breaks. A note uses the same
// shape and rejects nothing. Severity is set by the customer profile alone.
type Finding struct {
	Path     string
	Line     int
	Severity Severity
	Rule     string
	Message  string
}

func (f Finding) String() string {
	if f.Severity != "" {
		return fmt.Sprintf("%s:%d: %s: %s: %s", f.Path, f.Line, f.Severity, f.Rule, f.Message)
	}
	return fmt.Sprintf("%s:%d: %s: %s", f.Path, f.Line, f.Rule, f.Message)
}

// Report is what one lint found. Agents counts the workflows the customer
// profile judged, the ones with an agent step.
type Report struct {
	Files    int
	Agents   int
	Findings []Finding
	Notes    []Finding
}

// Profile chooses the rules a lint holds files to.
type Profile int

const (
	// Recipes holds Costroid's shipped recipes to every rule.
	Recipes Profile = iota
	// Customer holds a team's own agent workflows to the rules on who can
	// start a run and what the agent's job holds, each with a severity.
	Customer
)

// Options are a lint's settings. The zero value lints recipes.
type Options struct {
	Profile Profile
}

// noun names what the profile reads, for its errors.
func (o Options) noun() string {
	if o.Profile == Customer {
		return "workflow"
	}
	return "recipe"
}

func (o Options) lintBytes(path string, data []byte) Report {
	if o.Profile == Customer {
		return LintCustomer(path, data)
	}
	return LintBytes(path, data)
}

// Lint checks the files named and every YAML file under the directories named,
// under the recipe profile.
func Lint(paths []string) (Report, error) {
	return LintWith(paths, Options{})
}

// LintWith checks the files named and every YAML file under the directories
// named. Naming nothing to lint is an error, so a mistyped path cannot pass.
func LintWith(paths []string, options Options) (Report, error) {
	var report Report
	for _, path := range paths {
		files, err := yamlFiles(path)
		if err != nil {
			return report, fmt.Errorf("read a %s: %w", options.noun(), err)
		}
		for _, file := range files {
			data, err := os.ReadFile(file)
			if err != nil {
				return report, fmt.Errorf("read a %s: %w", options.noun(), err)
			}
			single := options.lintBytes(file, data)
			report.Files++
			report.Agents += single.Agents
			report.Findings = append(report.Findings, single.Findings...)
			report.Notes = append(report.Notes, single.Notes...)
		}
	}
	if report.Files == 0 {
		return report, fmt.Errorf("no %ss found", options.noun())
	}
	return report, nil
}

func yamlFiles(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{path}, nil
	}
	var files []string
	err = filepath.WalkDir(path, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if extension := filepath.Ext(file); !entry.IsDir() && (extension == ".yml" || extension == ".yaml") {
			files = append(files, file)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list the YAML files in %s: %w", path, err)
	}
	return files, nil
}

// LintBytes checks one file. A file that is no workflow, action or GitLab
// template is a finding, because a rule that sees nothing has proved nothing.
func LintBytes(path string, data []byte) Report {
	l := &linter{path: path}
	documents, err := parse(data)
	if err != nil {
		l.add(&yaml.Node{Line: 1}, RuleUnreadable, "not valid YAML: %v", err)
		return l.report
	}
	recognized := false
	for _, root := range documents {
		switch {
		case field(root, "jobs") != nil && field(root, "on") != nil:
			l.workflow(root)
		case field(root, "runs") != nil:
			l.action(root)
		case isGitLab(root):
			l.gitlab(root)
		default:
			continue
		}
		recognized = true
	}
	if !recognized {
		l.add(&yaml.Node{Line: 1}, RuleUnreadable, "not a workflow, an action or a GitLab template")
	}
	return l.report
}

func parse(data []byte) ([]*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var documents []*yaml.Node
	for {
		var document yaml.Node
		err := decoder.Decode(&document)
		if errors.Is(err, io.EOF) {
			return documents, nil
		}
		if err != nil {
			return nil, err
		}
		documents = append(documents, resolve(document.Content[0]))
	}
}

type linter struct {
	path   string
	report Report
}

func (l *linter) add(node *yaml.Node, rule, format string, args ...any) {
	l.report.Findings = append(l.report.Findings, l.finding(node, rule, format, args...))
}

func (l *linter) note(node *yaml.Node, rule, format string, args ...any) {
	l.report.Notes = append(l.report.Notes, l.finding(node, rule, format, args...))
}

func (l *linter) finding(node *yaml.Node, rule, format string, args ...any) Finding {
	return Finding{Path: l.path, Line: node.Line, Rule: rule, Message: fmt.Sprintf(format, args...)}
}

// agentRun is one agent job, read the same way on either CI system.
type agentRun struct {
	node    *yaml.Node
	agent   string
	text    string
	env     map[string]string
	minimum string
}

// checkAgent applies the rules both CI systems share.
func (l *linter) checkAgent(run agentRun, forbidden *regexp.Regexp) {
	if install := agentInstall[run.agent]; !install.MatchString(run.text) {
		l.add(run.node, RuleAgentVersion, "%s is not installed at an exact version", run.agent)
	}
	if !strings.Contains(run.text, run.minimum) {
		l.add(run.node, RuleAgentVersion, "%s states no minimum version in %s", run.agent, run.minimum)
	}
	if match := forbidden.FindString(run.text); match != "" {
		l.add(run.node, RuleAgentPromptInput, "%s reaches the agent's job, and the PR's author or a comment controls it", match)
	}
	for _, flag := range contentFlagsOff(run) {
		l.add(run.node, RuleAgentContentFlags, "%s", flag)
	}
}

func contentFlagsOff(run agentRun) []string {
	var missing []string
	switch run.agent {
	case claudeAgent:
		for _, name := range claudeContentFlags {
			if run.env[name] != "0" {
				missing = append(missing, name+" must be set to 0 on the agent")
			}
		}
	case codexAgent:
		if !strings.Contains(run.text, codexContentFlag) {
			missing = append(missing, "codex needs "+codexContentFlag)
		}
	}
	return missing
}

// resolve follows aliases.
func resolve(node *yaml.Node) *yaml.Node {
	for depth := 0; node != nil && node.Kind == yaml.AliasNode && depth < maximumAliasDepth; depth++ {
		node = node.Alias
	}
	return node
}

// entry is one key and value of a mapping.
type entry struct {
	key   *yaml.Node
	value *yaml.Node
}

// entries lists a mapping in order, with merge keys expanded.
func entries(node *yaml.Node) []entry {
	return collect(resolve(node), map[*yaml.Node]bool{})
}

func collect(node *yaml.Node, seen map[*yaml.Node]bool) []entry {
	if node == nil || node.Kind != yaml.MappingNode || seen[node] {
		return nil
	}
	seen[node] = true
	var all []entry
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i], resolve(node.Content[i+1])
		if key.Tag != "!!merge" {
			all = append(all, entry{key, value})
			continue
		}
		merged := []*yaml.Node{value}
		if value.Kind == yaml.SequenceNode {
			merged = value.Content
		}
		for _, source := range merged {
			all = append(all, collect(resolve(source), seen)...)
		}
	}
	return all
}

func field(node *yaml.Node, key string) *yaml.Node {
	for _, item := range entries(node) {
		if item.key.Value == key {
			return item.value
		}
	}
	return nil
}

func scalarField(node *yaml.Node, key string) string {
	if value := field(node, key); value != nil && value.Kind == yaml.ScalarNode {
		return value.Value
	}
	return ""
}

func sequence(node *yaml.Node) []*yaml.Node {
	node = resolve(node)
	if node == nil {
		return nil
	}
	if node.Kind == yaml.SequenceNode {
		return node.Content
	}
	return []*yaml.Node{node}
}

// text joins every scalar under a node, keys included.
func text(nodes ...*yaml.Node) string {
	var parts []string
	seen := map[*yaml.Node]bool{}
	var walk func(*yaml.Node)
	walk = func(node *yaml.Node) {
		node = resolve(node)
		if node == nil || seen[node] {
			return
		}
		seen[node] = true
		if node.Kind == yaml.ScalarNode {
			parts = append(parts, node.Value)
		}
		for _, child := range node.Content {
			walk(child)
		}
	}
	for _, node := range nodes {
		walk(node)
	}
	return strings.Join(parts, "\n")
}

// mergeVariables overlays each mapping on the last.
func mergeVariables(nodes ...*yaml.Node) map[string]string {
	merged := map[string]string{}
	for _, node := range nodes {
		for _, item := range entries(node) {
			merged[item.key.Value] = item.value.Value
		}
	}
	return merged
}

// agentsNamed lists the agents a text starts, in a stable order.
func agentsNamed(body string) []string {
	var agents []string
	if claudeCommand.MatchString(body) {
		agents = append(agents, claudeAgent)
	}
	if codexCommand.MatchString(body) {
		agents = append(agents, codexAgent)
	}
	return agents
}

var (
	claudeCommand = regexp.MustCompile(`\bclaude\s+(-p|--print)\b`)
	codexCommand  = regexp.MustCompile(`\bcodex\s+exec\b`)
)
