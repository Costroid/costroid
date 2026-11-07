package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	costroid "github.com/Costroid/costroid"
	"github.com/Costroid/costroid/internal/recipelint"
	"go.yaml.in/yaml/v3"
)

// commit is the commit a release was built from, set at link time. A
// development build has none.
var commit string

const initHelp = `Usage:
  costroid init github --agent claude|codex|claude-code-action|codex-action [--out PATH] [--force] [--action-ref SHA]
  costroid init gitlab --agent claude|codex [--out PATH] [--force]
  costroid init github --agent claude-code-action --existing PATH [--action-ref SHA]

init writes the recipe for a host and an agent as this release ships it, with
Costroid pinned to the commit and version of the release this binary belongs
to. It writes .github/workflows/costroid.yml on GitHub and .gitlab/costroid.yml
on GitLab, or --out, and refuses to replace a file without --force. It writes
costroid.yml, with every key commented out, only when there is none.

It holds the recipe to the rules of lint-recipes before it writes it. Then it
prints what each push can spend and the commands that are left. It runs none
of them.

A development build belongs to no release, so it pins nothing and refuses.
--action-ref names the commit of Costroid's action to pin on GitHub instead.

--existing reads a workflow that runs claude-code-action and prints a patch
that adds a read step after that step and a report job. It writes nothing.
docs/existing-workflow.md shows the result.

Exit codes of init:
  0                 written, or the patch printed
  1                 the recipe breaks a rule, which is a defect in costroid
  125               costroid could not read or write a file, or refused to
`

const (
	githubHost         = "github"
	gitlabHost         = "gitlab"
	costroidActionPath = "Costroid/costroid/action"
	claudeCodeAction   = "anthropics/claude-code-action"
	existingAgent      = "claude-code-action"
	agentLabel         = "costroid-agent"
	addedStepID        = "claude"
)

// initRecipes names the shipped recipe for each host and agent.
var initRecipes = map[string]map[string]string{
	githubHost: {
		"claude":             "github-claude-code.yml",
		"codex":              "github-codex.yml",
		"claude-code-action": "github-claude-code-action.yml",
		"codex-action":       "github-codex-action.yml",
	},
	gitlabHost: {
		"claude": "gitlab-ci.yml",
		"codex":  "gitlab-ci.yml",
	},
}

// initOut is where each host reads the file init writes.
var initOut = map[string]string{
	githubHost: ".github/workflows/costroid.yml",
	gitlabHost: ".gitlab/costroid.yml",
}

// gitlabKeys names the provider key each agent reads on GitLab, where the
// template takes it from the job's environment.
var gitlabKeys = map[string]string{"claude": "ANTHROPIC_API_KEY", "codex": "CODEX_API_KEY"}

var (
	releaseVersion = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$`)
	fullSHA        = regexp.MustCompile(`^[0-9a-f]{40}$`)
	actionPin      = regexp.MustCompile(regexp.QuoteMeta(costroidActionPath) + `@[0-9a-f]{40}(?: #[^\n]*)?`)
	gitlabVersion  = regexp.MustCompile(`(?m)^( +COSTROID_VERSION: ).*$`)
	gitlabCommit   = regexp.MustCompile(`(?m)^( +COSTROID_COMMIT: ).*$`)
	gitlabAgent    = regexp.MustCompile(`(?m)^( +COSTROID_AGENT: ).*$`)
	secretName     = regexp.MustCompile(`\$\{\{\s*secrets\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)
)

// buildInfo is what the linker stamped on this binary.
type buildInfo struct {
	version, commit string
}

// pin is the commit init pins Costroid to, and the release it belongs to.
// version is empty when --action-ref named the commit.
type pin struct {
	commit, version string
}

func (p pin) uses() string {
	if p.version == "" {
		return costroidActionPath + "@" + p.commit + " # set by costroid init --action-ref"
	}
	return costroidActionPath + "@" + p.commit + " # v" + p.version
}

func (p pin) String() string {
	if p.version == "" {
		return "pinned to " + p.commit + ", which --action-ref named"
	}
	return "pinned to v" + p.version + " at " + p.commit
}

type initFlags struct {
	host, agent, out, existing, actionRef string
	force                                 bool
}

func initCommand(args []string, stdout, stderr io.Writer) int {
	return initRepository(".", buildInfo{version: version, commit: commit}, costroid.Recipes, args, stdout, stderr)
}

// initRepository runs init against the repository at root, with the recipes
// recipes holds under recipes/.
func initRepository(root string, build buildInfo, recipes fs.FS, args []string, stdout, stderr io.Writer) int {
	flags, err := parseInit(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return initFailed(stderr, err)
	}
	release, err := resolvePin(build, flags.actionRef, flags.host)
	if err != nil {
		return initFailed(stderr, err)
	}
	if flags.existing != "" {
		return printExistingPatch(root, flags.existing, release, stdout, stderr)
	}
	return writeRecipe(root, flags, release, recipes, stdout, stderr)
}

func initFailed(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "costroid: init: %v\n", err)
	return exitFailure
}

func parseInit(args []string, stderr io.Writer) (initFlags, error) {
	set := flag.NewFlagSet("init", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	set.Usage = func() { fmt.Fprint(stderr, initHelp) }
	var flags initFlags
	set.StringVar(&flags.agent, "agent", "", "the agent the recipe runs or reads")
	set.StringVar(&flags.out, "out", "", "the file to write the recipe to")
	set.StringVar(&flags.existing, "existing", "", "a workflow to print a patch for")
	set.StringVar(&flags.actionRef, "action-ref", "", "the commit of Costroid's action to pin")
	set.BoolVar(&flags.force, "force", false, "replace a file that exists")
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		flags.host, args = args[0], args[1:]
	}
	if err := set.Parse(args); err != nil {
		return flags, err
	}
	rest := set.Args()
	if flags.host == "" && len(rest) > 0 {
		flags.host, rest = rest[0], rest[1:]
	}
	if len(rest) > 0 {
		return flags, fmt.Errorf("unexpected %q: init takes one host", rest[0])
	}
	return flags, flags.check()
}

func (f initFlags) check() error {
	agents, ok := initRecipes[f.host]
	switch {
	case f.host == "":
		return errors.New("name the host: github or gitlab")
	case !ok:
		return fmt.Errorf("the host is github or gitlab, not %q", f.host)
	case f.agent == "":
		return fmt.Errorf("--agent is one of %s", agentNames(agents))
	case agents[f.agent] == "":
		return fmt.Errorf("--agent on %s is one of %s, not %q", f.host, agentNames(agents), f.agent)
	case f.existing == "":
		return nil
	case f.host != githubHost || f.agent != existingAgent:
		return errors.New("--existing reads a GitHub workflow that runs claude-code-action: pass github --agent claude-code-action")
	case f.out != "" || f.force:
		return errors.New("--existing writes nothing, so --out and --force do not apply")
	}
	return nil
}

func agentNames(agents map[string]string) string {
	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

// resolvePin finds the commit to pin: --action-ref's, or the release's this
// binary was built from. A binary that names no release pins nothing.
func resolvePin(build buildInfo, actionRef, host string) (pin, error) {
	if actionRef != "" {
		if host != githubHost {
			return pin{}, errors.New("--action-ref names a commit of Costroid's GitHub action, and the GitLab template installs a release by its version")
		}
		if !fullSHA.MatchString(actionRef) || strings.Trim(actionRef, "0") == "" {
			return pin{}, fmt.Errorf("--action-ref must be a full commit SHA, 40 lowercase hex characters, not %q", actionRef)
		}
		return pin{commit: actionRef}, nil
	}
	fix := "run a released costroid"
	if host == githubHost {
		fix += ", or name the commit to pin with --action-ref <40 hex>"
	}
	if !releaseVersion.MatchString(build.version) {
		return pin{}, fmt.Errorf("this is a development build, version %s, which belongs to no release, so init pins nothing: %s", build.version, fix)
	}
	if !fullSHA.MatchString(build.commit) {
		return pin{}, fmt.Errorf("this build of %s records no commit, so init pins nothing: %s", build.version, fix)
	}
	return pin{commit: build.commit, version: strings.TrimPrefix(build.version, "v")}, nil
}

func writeRecipe(root string, flags initFlags, release pin, recipes fs.FS, stdout, stderr io.Writer) int {
	name := initRecipes[flags.host][flags.agent]
	source, err := fs.ReadFile(recipes, path.Join("recipes", name))
	if err != nil {
		return initFailed(stderr, fmt.Errorf("read the shipped recipe: %w", err))
	}
	recipe, err := pinRecipe(name, source, release, flags.agent)
	if err != nil {
		return initFailed(stderr, err)
	}
	out := flags.out
	if out == "" {
		out = initOut[flags.host]
	}
	report := recipelint.LintBytes(out, recipe)
	if len(report.Findings) > 0 {
		for _, finding := range report.Findings {
			fmt.Fprintln(stderr, finding)
		}
		fmt.Fprintf(stderr, "costroid: init wrote nothing: recipes/%s breaks a rule of lint-recipes, which is a defect in costroid\n", name)
		return exitRejected
	}
	switch err := createFile(within(root, out), recipe, flags.force); {
	case errors.Is(err, fs.ErrExist):
		return initFailed(stderr, fmt.Errorf("%s exists: pass --force to replace it", out))
	case err != nil:
		return initFailed(stderr, err)
	}
	fmt.Fprintf(stdout, "costroid: wrote %s from recipes/%s, %s\n", out, name, release)
	for _, note := range report.Notes {
		fmt.Fprintln(stdout, note)
	}
	written := []string{out}
	settings, err := commentedConfig()
	if err != nil {
		return initFailed(stderr, err)
	}
	switch err := createFile(within(root, configFile), settings, false); {
	case err == nil:
		written = append(written, configFile)
		fmt.Fprintf(stdout, "costroid: wrote %s, with every key commented out\n", configFile)
	case errors.Is(err, fs.ErrExist):
		fmt.Fprintf(stdout, "costroid: %s exists, and init left it as it is\n", configFile)
	default:
		return initFailed(stderr, err)
	}
	printNextSteps(stdout, flags, written, recipe)
	return 0
}

// pinRecipe rewrites a shipped recipe's Costroid pins to release. On GitLab
// it also sets the template's agent.
func pinRecipe(name string, source []byte, release pin, agent string) ([]byte, error) {
	if !strings.HasPrefix(name, gitlabHost) {
		count := len(actionPin.FindAllIndex(source, -1))
		if count == 0 {
			return nil, fmt.Errorf("recipes/%s pins no %s", name, costroidActionPath)
		}
		return actionPin.ReplaceAllLiteral(source, []byte(release.uses())), nil
	}
	for _, set := range []struct {
		line  *regexp.Regexp
		value string
	}{{gitlabVersion, release.version}, {gitlabCommit, release.commit}, {gitlabAgent, agent}} {
		if count := len(set.line.FindAllIndex(source, -1)); count != 1 {
			return nil, fmt.Errorf("recipes/%s has %d lines that match %s, want 1", name, count, set.line)
		}
		source = set.line.ReplaceAll(source, []byte("${1}"+set.value))
	}
	return source, nil
}

// within places a path the user named, or a default, under the repository.
func within(root, name string) string {
	if filepath.IsAbs(name) {
		return name
	}
	return filepath.Join(root, name)
}

// createFile writes data to a new file, or over an old one when replace is
// set. Without replace, a file that exists answers fs.ErrExist.
func createFile(name string, data []byte, replace bool) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return fmt.Errorf("make the directory for %s: %w", name, err)
	}
	mode := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if !replace {
		mode |= os.O_EXCL
	}
	file, err := os.OpenFile(name, mode, 0o644)
	if err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

// perPushSentence is the recipe header's statement of what each push runs.
func perPushSentence(recipe []byte) string {
	var header []string
	for line := range strings.SplitSeq(string(recipe), "\n") {
		if !strings.HasPrefix(line, "#") {
			break
		}
		header = append(header, strings.TrimSpace(strings.TrimPrefix(line, "#")))
	}
	_, sentence, found := strings.Cut(strings.Join(header, " "), "Each push")
	if !found {
		return ""
	}
	return "Each push" + sentence
}

func printNextSteps(stdout io.Writer, flags initFlags, written []string, recipe []byte) {
	if sentence := perPushSentence(recipe); sentence != "" {
		fmt.Fprint(stdout, "\n"+wrap(sentence, usageWidth))
	}
	var commands []string
	if flags.host == gitlabHost {
		fmt.Fprintf(stdout, "\nInclude the template from .gitlab-ci.yml:\n  include:\n    - local: %s\n\n", filepath.ToSlash(written[0]))
		fmt.Fprint(stdout, wrap(fmt.Sprintf("Set %s as a CI/CD variable scoped to the environment costroid-agent, and COSTROID_GITLAB_TOKEN scoped to costroid-report. docs/recipes.md#give-the-report-a-token says which token. Every merge request needs the label %s.", gitlabKeys[flags.agent], agentLabel), usageWidth))
		written = append(written, ".gitlab-ci.yml")
	} else {
		for _, name := range secretNames(recipe) {
			commands = append(commands, "gh secret set "+name)
		}
		if bytes.Contains(recipe, []byte(agentLabel)) {
			fmt.Fprint(stdout, "\n"+wrap(fmt.Sprintf("The label %s lets a maintainer start the agent on a first contributor's pull request.", agentLabel), usageWidth))
			commands = append(commands, "gh label create "+agentLabel)
		}
	}
	commands = append(commands, "git switch -c try-costroid", "git add "+strings.Join(slashed(written), " "), `git commit -m "Add the costroid check"`)
	if flags.host == gitlabHost {
		commands = append(commands, "git push --set-upstream origin try-costroid")
	} else {
		commands = append(commands, "gh pr create --fill")
	}
	fmt.Fprint(stdout, "\nThese commands are left, and init ran none of them:\n")
	for _, command := range commands {
		fmt.Fprintf(stdout, "  %s\n", command)
	}
}

// secretNames lists the repository secrets a GitHub recipe reads, in order.
func secretNames(recipe []byte) []string {
	var names []string
	for _, match := range secretName.FindAllSubmatch(recipe, -1) {
		if name := string(match[1]); name != "GITHUB_TOKEN" && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

func slashed(names []string) []string {
	out := make([]string, len(names))
	for i, name := range names {
		out[i] = filepath.ToSlash(name)
	}
	return out
}

// schemaNode is the part of costroid.schema.json init writes from: each key
// in the file's order, its description and its default.
type schemaNode struct {
	ID          string           `json:"$id"`
	Description string           `json:"description"`
	Default     json.RawMessage  `json:"default"`
	Properties  schemaProperties `json:"properties"`
}

type schemaProperty struct {
	name string
	node schemaNode
}

// schemaProperties keeps an object's keys in the order the file lists them.
type schemaProperties []schemaProperty

func (p *schemaProperties) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if _, err := decoder.Token(); err != nil {
		return err
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := token.(string)
		if !ok {
			return fmt.Errorf("a property name reads %v", token)
		}
		var node schemaNode
		if err := decoder.Decode(&node); err != nil {
			return fmt.Errorf("property %s: %w", name, err)
		}
		*p = append(*p, schemaProperty{name: name, node: node})
	}
	return nil
}

// commentedConfig is a costroid.yml that sets nothing and names every key
// of the schema, each under its description and commented out.
func commentedConfig() ([]byte, error) {
	var schema schemaNode
	if err := json.Unmarshal(costroid.ConfigSchema, &schema); err != nil {
		return nil, fmt.Errorf("read the embedded schema of costroid.yml: %w", err)
	}
	var text strings.Builder
	fmt.Fprintf(&text, "# yaml-language-server: $schema=%s\n", schema.ID)
	writeComment(&text, "# ", schema.Description+" To set a key, remove the # before it and before each key it sits under. costroid validate checks the file.")
	for _, property := range schema.Properties {
		text.WriteString("\n")
		writeCommentedKey(&text, property, 0)
	}
	return []byte(text.String()), nil
}

func writeCommentedKey(text *strings.Builder, property schemaProperty, depth int) {
	indent := strings.Repeat("  ", depth)
	prefix := "# "
	if depth > 0 {
		prefix = "# " + indent + "# "
	}
	writeComment(text, prefix, property.node.Description)
	line := "# " + indent + property.name + ":"
	if len(property.node.Default) > 0 {
		line += " " + string(property.node.Default)
	}
	text.WriteString(line + "\n")
	for _, child := range property.node.Properties {
		writeCommentedKey(text, child, depth+1)
	}
}

func writeComment(text *strings.Builder, prefix, comment string) {
	if comment == "" {
		return
	}
	for line := range strings.SplitSeq(strings.TrimSuffix(wrap(comment, usageWidth-len(prefix)), "\n"), "\n") {
		text.WriteString(prefix + line + "\n")
	}
}

// insertion is text added before one line of a file. before is the line's
// index, and the line count when the text goes at the end.
type insertion struct {
	before int
	lines  []string
}

// claudeStep is where a workflow runs claude-code-action.
type claudeStep struct {
	jobKey, job, steps, step *yaml.Node
}

func printExistingPatch(root, name string, release pin, stdout, stderr io.Writer) int {
	source, err := os.ReadFile(within(root, name))
	if err != nil {
		return initFailed(stderr, err)
	}
	if bytes.Contains(source, []byte(costroidActionPath+"@")) {
		fmt.Fprintf(stderr, "costroid: %s already calls %s, so init has nothing to add\n", name, costroidActionPath)
		return 0
	}
	lines, endsWithNewline := splitLines(source)
	insertions, err := existingInsertions(source, lines, release)
	if err != nil {
		return initFailed(stderr, fmt.Errorf("%s: %w", name, err))
	}
	fmt.Fprint(stdout, unifiedDiff(diffName(root, name), lines, endsWithNewline, insertions))
	fmt.Fprintf(stderr, "costroid: printed a patch for %s, and wrote nothing\n", name)
	fmt.Fprint(stderr, wrap("Apply it from the repository's root with git apply. Your job keeps the permissions it has, and the read step asks for nothing more. The report job holds checks: write and pull-requests: write, and runs nothing the pull request wrote. docs/existing-workflow.md says more.", usageWidth))
	return 0
}

// existingInsertions finds the one claude-code-action step and the end of the
// jobs block, and returns the read step and the report job to add there, in
// the indentation the file already uses.
func existingInsertions(source []byte, lines []string, release pin) ([]insertion, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(source, &document); err != nil {
		return nil, fmt.Errorf("not valid YAML: %w", err)
	}
	if len(document.Content) != 1 {
		return nil, errors.New("holds no workflow")
	}
	jobsKey, jobs := mappingPair(document.Content[0], "jobs")
	if jobs == nil || jobs.Kind != yaml.MappingNode || jobs.Style&yaml.FlowStyle != 0 {
		return nil, errors.New("has no jobs block to add to")
	}
	found := claudeSteps(jobs)
	if len(found) != 1 {
		return nil, fmt.Errorf("runs %s in %d steps, and init adds to exactly one: docs/existing-workflow.md shows the two blocks to add by hand", claudeCodeAction, len(found))
	}
	claude := found[0]
	stepID, addID, err := claudeStepID(claude)
	if err != nil {
		return nil, err
	}
	dash, err := dashIndent(lines, claude.step)
	if err != nil {
		return nil, err
	}
	jobIndent := claude.jobKey.Column - 1
	unit := claude.job.Content[0].Column - 1 - jobIndent
	if claude.job.Style&yaml.FlowStyle != 0 || unit <= 0 {
		return nil, errors.New("indents the job in a way init does not write into")
	}
	reportName, err := freeJobName(jobs)
	if err != nil {
		return nil, err
	}
	var step []string
	if addID {
		step = append(step, strings.Repeat(" ", claude.step.Column-1)+"id: "+addedStepID)
	}
	step = append(step, readStep(dash, unit, stepID, release)...)
	report := append([]string{""}, reportJob(jobIndent, unit, reportName, claude.jobKey.Value, release)...)
	return []insertion{
		{before: blockEnd(lines, claude.step.Line-1, dash) + 1, lines: step},
		{before: blockEnd(lines, jobsKey.Line-1, jobsKey.Column-1) + 1, lines: report},
	}, nil
}

func claudeSteps(jobs *yaml.Node) []claudeStep {
	var found []claudeStep
	for i := 0; i+1 < len(jobs.Content); i += 2 {
		_, steps := mappingPair(jobs.Content[i+1], "steps")
		if steps == nil || steps.Kind != yaml.SequenceNode {
			continue
		}
		for _, step := range steps.Content {
			_, uses := mappingPair(step, "uses")
			if uses != nil && strings.HasPrefix(strings.ToLower(uses.Value), claudeCodeAction+"@") {
				found = append(found, claudeStep{jobKey: jobs.Content[i], job: jobs.Content[i+1], steps: steps, step: step})
			}
		}
	}
	return found
}

// claudeStepID is the id the read step names: the step's own, or one init
// adds when it has none.
func claudeStepID(claude claudeStep) (string, bool, error) {
	if _, id := mappingPair(claude.step, "id"); id != nil {
		return id.Value, false, nil
	}
	for _, other := range claude.steps.Content {
		if _, id := mappingPair(other, "id"); id != nil && id.Value == addedStepID {
			return "", false, fmt.Errorf("another step of job %s has the id %s: give the claude-code-action step an id and run init again", claude.jobKey.Value, addedStepID)
		}
	}
	return addedStepID, true, nil
}

// dashIndent is the column, from 0, of the dash that opens a step.
func dashIndent(lines []string, step *yaml.Node) (int, error) {
	if step.Style&yaml.FlowStyle != 0 || step.Line < 1 || step.Line > len(lines) || step.Column < 3 || len(lines[step.Line-1]) < step.Column-1 {
		return 0, errors.New("writes the claude-code-action step in a layout init does not write into")
	}
	prefix := lines[step.Line-1][:step.Column-1]
	if strings.TrimSpace(prefix) != "-" {
		return 0, errors.New("writes the claude-code-action step in a layout init does not write into")
	}
	return strings.Index(prefix, "-"), nil
}

func freeJobName(jobs *yaml.Node) (string, error) {
	for _, name := range []string{"report", "costroid-report"} {
		if _, job := mappingPair(jobs, name); job == nil {
			return name, nil
		}
	}
	return "", errors.New("already has jobs named report and costroid-report")
}

func mappingPair(node *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i], node.Content[i+1]
		}
	}
	return nil, nil
}

// blockEnd is the index of the last line of the block that opens on line
// start: the last line before one indented no deeper than indent.
func blockEnd(lines []string, start, indent int) int {
	last := start
	for i := start + 1; i < len(lines); i++ {
		trimmed := strings.TrimLeft(lines[i], " ")
		if trimmed == "" {
			continue
		}
		if len(lines[i])-len(trimmed) <= indent {
			break
		}
		last = i
	}
	return last
}

// readStep is docs/existing-workflow.md's first block.
func readStep(dash, unit int, stepID string, release pin) []string {
	key := strings.Repeat(" ", dash+2)
	child := key + strings.Repeat(" ", unit)
	return []string{
		strings.Repeat(" ", dash) + "- name: Read what the run cost",
		key + "if: always() && steps." + stepID + ".outputs.execution_file != ''",
		key + "uses: " + release.uses(),
		key + "with:",
		child + "stage: read",
		child + "execution-file: ${{ steps." + stepID + ".outputs.execution_file }}",
		child + "billing-account: ${{ github.repository_owner }}",
	}
}

// reportJob is docs/existing-workflow.md's second block.
func reportJob(indent, unit int, name, agentJob string, release pin) []string {
	at := func(level int) string { return strings.Repeat(" ", indent+level*unit) }
	step := at(2) + "  "
	return []string{
		at(0) + name + ":",
		at(1) + "needs: " + agentJob,
		at(1) + "if: always()",
		at(1) + "runs-on: ubuntu-24.04",
		at(1) + "timeout-minutes: 5",
		at(1) + "permissions:",
		at(2) + "checks: write",
		at(2) + "contents: read",
		at(2) + "pull-requests: write",
		at(1) + "steps:",
		at(2) + "- uses: " + release.uses(),
		step + "with:",
		step + strings.Repeat(" ", unit) + "stage: report",
		step + strings.Repeat(" ", unit) + "agent-result: ${{ needs." + agentJob + ".result }}",
	}
}

// splitLines splits a file into its lines, and says whether the last one
// ends with a newline.
func splitLines(source []byte) ([]string, bool) {
	text := string(source)
	endsWithNewline := strings.HasSuffix(text, "\n")
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n"), endsWithNewline
}

// diffName is the path the patch names, relative to the repository's root so
// that git apply finds it.
func diffName(root, name string) string {
	if filepath.IsAbs(name) {
		if base, err := filepath.Abs(root); err == nil {
			if relative, err := filepath.Rel(base, name); err == nil && !strings.HasPrefix(relative, "..") {
				name = relative
			}
		}
	}
	return strings.TrimPrefix(filepath.ToSlash(filepath.Clean(name)), "./")
}

const diffContext = 3

// unifiedDiff prints the insertions as a unified diff with three lines of
// context. Insertions are in the order of their lines.
func unifiedDiff(name string, lines []string, endsWithNewline bool, insertions []insertion) string {
	var out strings.Builder
	fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n", name, name)
	added := 0
	for len(insertions) > 0 {
		start := max(0, insertions[0].before-diffContext)
		end := min(len(lines), insertions[0].before+diffContext)
		count := 1
		for count < len(insertions) && insertions[count].before-diffContext <= end {
			end = min(len(lines), insertions[count].before+diffContext)
			count++
		}
		hunk := insertions[:count]
		insertions = insertions[count:]
		inserted := 0
		for _, insert := range hunk {
			inserted += len(insert.lines)
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", start+1, end-start, start+1+added, end-start+inserted)
		for index := start; index <= end; index++ {
			for _, insert := range hunk {
				if insert.before == index {
					for _, line := range insert.lines {
						out.WriteString("+" + line + "\n")
					}
				}
			}
			if index < end {
				writeContextLine(&out, lines, index, endsWithNewline, hunk)
			}
		}
		added += inserted
	}
	return out.String()
}

// writeContextLine prints one unchanged line. The last line of a file with no
// final newline gains one when text follows it, so the diff replaces it.
func writeContextLine(out *strings.Builder, lines []string, index int, endsWithNewline bool, hunk []insertion) {
	if index != len(lines)-1 || endsWithNewline {
		out.WriteString(" " + lines[index] + "\n")
		return
	}
	for _, insert := range hunk {
		if insert.before == len(lines) {
			out.WriteString("-" + lines[index] + "\n\\ No newline at end of file\n+" + lines[index] + "\n")
			return
		}
	}
	out.WriteString(" " + lines[index] + "\n\\ No newline at end of file\n")
}
