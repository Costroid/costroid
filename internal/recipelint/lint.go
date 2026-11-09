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
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

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
	RuleUnknownAgent       = "unknown-agent"
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
	claudeCodeAgent        = "claude-code"
	claudeSDKAgent         = "claude-code-sdk"
	codexSDKAgent          = "codex-sdk"
	openCodeAgent          = "opencode"
	codexContentFlag       = "otel.log_user_prompt=false"
	codexSDKContentFlag    = "log_user_prompt = false under [otel] in CODEX_HOME/config.toml"
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

var (
	claudeRelease = regexp.MustCompile(`@anthropic-ai/claude-code@[0-9]+\.[0-9]+\.[0-9]+`)
	codexRelease  = regexp.MustCompile(`@openai/codex@[0-9]+\.[0-9]+\.[0-9]+`)
	// openCodeRelease is OpenCode's npm package at an exact version.
	openCodeRelease = regexp.MustCompile(`\bopencode-ai@[0-9]+\.[0-9]+\.[0-9]+`)
	// lockInstall installs a program's packages at the versions its lock file
	// pins, the agent's among them, and fails when the lock file disagrees.
	lockInstall = regexp.MustCompile(`\bnpm\s+ci\b`)
	// codexConfigFlag is the line of Codex's config.toml that keeps prompts
	// out of its telemetry. No command line reaches the Codex an SDK starts.
	// A script that writes the file can hold it after a printf's \n, so no
	// word boundary is asked for before it.
	codexConfigFlag = regexp.MustCompile(`log_user_prompt\s*=\s*false\b`)
)

// agentKind is what the rules hold an agent to: the provider whose content
// flags and host it has, the install that fixes its version, and whether its
// job states the lowest version it accepts. A program that runs an agent
// through its SDK takes the agent from its lock file, so nothing checks a
// floor.
type agentKind struct {
	provider string
	install  *regexp.Regexp
	minimum  bool
}

// agentKinds holds every agent a step can name: claude and codex as the
// action's agent input and as the commands claude -p and codex exec, and each
// value of costroid run --agent.
var agentKinds = map[string]agentKind{
	claudeAgent:     {provider: claudeAgent, install: claudeRelease, minimum: true},
	claudeCodeAgent: {provider: claudeAgent, install: claudeRelease, minimum: true},
	claudeSDKAgent:  {provider: claudeAgent, install: lockInstall},
	codexAgent:      {provider: codexAgent, install: codexRelease, minimum: true},
	codexSDKAgent:   {provider: codexAgent, install: lockInstall},
	openCodeAgent:   {provider: openCodeAgent, install: openCodeRelease, minimum: true},
}

// actionAgents are the values the action's agent input takes, and runAgents
// the values costroid run --agent takes.
var (
	actionAgents = []string{claudeAgent, codexAgent}
	runAgents    = []string{claudeCodeAgent, claudeSDKAgent, codexAgent, codexSDKAgent, openCodeAgent}
)

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
	return Finding{Path: l.path, Line: node.Line, Rule: rule, Message: message(format, args...)}
}

// message formats a finding's message on one line. A value read from the file
// can hold a line break or another control character, so each one is written
// as its Go escape, the way %q writes it.
func message(format string, args ...any) string {
	text := fmt.Sprintf(format, args...)
	if !strings.ContainsFunc(text, breaksLine) {
		return text
	}
	var escaped strings.Builder
	for rest := text; rest != ""; {
		r, size := utf8.DecodeRuneInString(rest)
		if breaksLine(r) {
			quoted := strconv.QuoteRune(r)
			escaped.WriteString(quoted[1 : len(quoted)-1])
		} else {
			escaped.WriteString(rest[:size])
		}
		rest = rest[size:]
	}
	return escaped.String()
}

func breaksLine(r rune) bool {
	return unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
}

// agentRun is one agent job, read the same way on either CI system. action
// is set when Costroid's action starts the agent, which takes fewer agents
// than costroid run does.
type agentRun struct {
	node    *yaml.Node
	agent   string
	text    string
	env     map[string]string
	minimum string
	action  bool
}

// checkAgent applies the rules both CI systems share. An agent the lint has no
// rules for is the one finding, because none of the rules can be judged.
func (l *linter) checkAgent(run agentRun, forbidden *regexp.Regexp) {
	kind, known := agentKinds[run.agent]
	takes := runAgents
	if run.action {
		takes = actionAgents
		known = known && slices.Contains(actionAgents, run.agent)
	}
	if !known {
		l.add(run.node, RuleUnknownAgent, "the agent %q is not one Costroid runs: it runs %s", run.agent, orList(takes))
		return
	}
	switch {
	case !kind.install.MatchString(run.text) && kind.minimum:
		l.add(run.node, RuleAgentVersion, "%s is not installed at an exact version", run.agent)
	case !kind.install.MatchString(run.text):
		l.add(run.node, RuleAgentVersion, "%s's program is not installed at the versions its lock file pins: install its packages with npm ci", run.agent)
	}
	if kind.minimum && !strings.Contains(run.text, run.minimum) {
		l.add(run.node, RuleAgentVersion, "%s states no minimum version in %s", run.agent, run.minimum)
	}
	if match := forbidden.FindString(run.text); match != "" {
		l.add(run.node, RuleAgentPromptInput, "%s reaches the agent's job, and the PR's author or a comment controls it", match)
	}
	for _, flag := range contentFlagsOff(run, kind) {
		l.add(run.node, RuleAgentContentFlags, "%s", flag)
	}
}

func contentFlagsOff(run agentRun, kind agentKind) []string {
	var missing []string
	switch {
	case kind.provider == claudeAgent:
		for _, name := range claudeContentFlags {
			if run.env[name] != "0" {
				missing = append(missing, name+" must be set to 0 on the agent")
			}
		}
	case run.agent == openCodeAgent:
		// OpenCode 1.19.4 exports logs and traces only to an endpoint
		// OTEL_EXPORTER_OTLP_ENDPOINT names, so it has no flag to turn off.
	case run.agent == codexSDKAgent:
		if !codexConfigFlag.MatchString(run.text) {
			missing = append(missing, codexSDKAgent+" needs "+codexSDKContentFlag)
		}
	case !strings.Contains(run.text, codexContentFlag):
		missing = append(missing, run.agent+" needs "+codexContentFlag)
	}
	return missing
}

// orList joins names as a sentence does: a, b or c.
func orList(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
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

// agentsNamed lists the agents a text starts, in a stable order: each one
// costroid run --agent names, then claude -p and codex exec, unless an agent
// named already runs on that provider.
func agentsNamed(body string) []string {
	agents := runAgentNames(body)
	for _, command := range []struct {
		agent   string
		pattern *regexp.Regexp
	}{{claudeAgent, claudeCommand}, {codexAgent, codexCommand}} {
		named := slices.ContainsFunc(agents, func(agent string) bool { return agentKinds[agent].provider == command.agent })
		if !named && command.pattern.MatchString(body) {
			agents = append(agents, command.agent)
		}
	}
	return agents
}

// runAgentNames lists the values of --agent in each costroid run command of a
// script. A backslash at a line's end continues the command. Its options end
// at --, where the program's own arguments start, or at a shell operator.
func runAgentNames(script string) []string {
	var names []string
	for line := range strings.SplitSeq(strings.ReplaceAll(script, "\\\n", " "), "\n") {
		for _, at := range costroidRun.FindAllStringIndex(line, -1) {
			if name, found := agentOption(strings.Fields(line[at[1]:])); found && !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	return names
}

// agentOption reads --agent NAME or --agent=NAME from a command's options.
func agentOption(words []string) (string, bool) {
	for i, word := range words {
		if word == "--" || strings.ContainsAny(word[:1], ";&|") {
			break
		}
		if name, found := strings.CutPrefix(word, "--agent="); found {
			return strings.Trim(name, `"'`), true
		}
		if word == "--agent" && i+1 < len(words) {
			return strings.Trim(words[i+1], `"'`), true
		}
	}
	return "", false
}

var (
	claudeCommand = regexp.MustCompile(`\bclaude\s+(-p|--print)\b`)
	codexCommand  = regexp.MustCompile(`\bcodex\s+exec\b`)
	costroidRun   = regexp.MustCompile(`\bcostroid[ \t]+run\b`)
)
