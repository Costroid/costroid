package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/config"
	"github.com/Costroid/costroid/internal/recipelint"
	"github.com/Costroid/costroid/internal/run"
	"go.yaml.in/yaml/v3"
)

const doctorHelp = `Usage:
  costroid doctor [--ci]

doctor checks a repository, from its root, before a pull request runs
Costroid: this binary's release, costroid.yml, the workflows and their pins,
the agents and keys on this machine, and the repository's secrets and
variables. It prints one line per check: ok, warn or fail, the fact and, after
warn or fail, the fix. It reads secret names through gh secret list when gh is
on PATH, and it never prints a secret's value, prefix or length.

--ci runs it as a step of a GitHub Actions job, and adds what only the job
sees: the event, whether the head is a fork, the OIDC token and the job's
declared permissions.

Exit codes of doctor:
  0                 no fail
  1                 a fail
  125               costroid could not run the checks
`

// verdict is how one check ended.
type verdict string

const (
	verdictOK   verdict = "ok"
	verdictWarn verdict = "warn"
	verdictFail verdict = "fail"
)

// finding is one line of doctor's table. fix is empty on an ok line.
type finding struct {
	verdict   verdict
	fact, fix string
}

func (f finding) String() string {
	if f.fix == "" {
		return string(f.verdict) + ": " + f.fact
	}
	return string(f.verdict) + ": " + f.fact + ": " + f.fix
}

func ok(format string, args ...any) finding {
	return finding{verdict: verdictOK, fact: fmt.Sprintf(format, args...)}
}

func warn(fact, fix string) finding { return finding{verdict: verdictWarn, fact: fact, fix: fix} }

func fail(fact, fix string) finding { return finding{verdict: verdictFail, fact: fact, fix: fix} }

// doctorHost is what doctor reads outside the repository's files: the
// environment, the programs on PATH and their output. Tests replace each.
type doctorHost struct {
	getenv   func(string) string
	lookPath func(string) (string, error)
	// output runs a program in dir and returns its stdout.
	output func(ctx context.Context, dir, name string, args ...string) ([]byte, error)
}

// doctorTimeout bounds each program doctor runs.
const doctorTimeout = 30 * time.Second

func runOutput(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, doctorTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	out, err := command.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out, fmt.Errorf("%s exited %d: %s", name, exit.ExitCode(), firstLineOf(string(exit.Stderr)))
	}
	return out, err
}

func firstLineOf(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return line
}

func doctorCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	host := doctorHost{getenv: os.Getenv, lookPath: exec.LookPath, output: runOutput}
	return doctor(ctx, ".", buildInfo{version: version, commit: commit}, host, args, stdout, stderr)
}

// doctor checks the repository at root and prints its table.
func doctor(ctx context.Context, root string, build buildInfo, host doctorHost, args []string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("doctor", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	set.Usage = func() { fmt.Fprint(stderr, doctorHelp) }
	ci := set.Bool("ci", false, "add the checks only a GitHub Actions job can make")
	err := set.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err == nil && set.NArg() > 0 {
		err = fmt.Errorf("unexpected %q: doctor takes no path, and reads the repository it runs in", set.Arg(0))
	}
	if err == nil && *ci && host.getenv("GITHUB_ACTIONS") != "true" {
		err = errors.New("--ci runs as a step of a GitHub Actions job, and GITHUB_ACTIONS is not true here")
	}
	var lines []finding
	if err == nil {
		lines, err = examine(ctx, root, build, host, *ci)
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: doctor: %v\n", err)
		return exitFailure
	}
	counts := map[verdict]int{}
	for _, line := range lines {
		fmt.Fprintln(stdout, line)
		counts[line.verdict]++
	}
	fmt.Fprintf(stdout, "costroid: doctor: %d fail, %d warn, %d ok\n", counts[verdictFail], counts[verdictWarn], counts[verdictOK])
	if counts[verdictFail] > 0 {
		return exitRejected
	}
	return 0
}

// examine runs every check in the order the table prints them.
func examine(ctx context.Context, root string, build buildInfo, host doctorHost, ci bool) ([]finding, error) {
	flows, err := readWorkflows(root)
	if err != nil {
		return nil, err
	}
	lines := []finding{releaseLine(build, flows)}
	settings, err := configLines(root)
	if err != nil {
		return nil, err
	}
	lines = append(lines, settings...)
	lines = append(lines, workflowLines(root, flows)...)
	agents := agentsRun(flows)
	for _, agent := range agents {
		lines = append(lines, agentLine(ctx, root, host, agent))
	}
	lines = append(lines, keyLines(host, agents)...)
	lines = append(lines, repositoryLines(ctx, root, host, flows)...)
	if ci {
		lines = append(lines, jobLines(root, host)...)
	}
	return lines, nil
}

// workflow is one file under .github/workflows and what doctor reads from it.
type workflow struct {
	path   string
	source []byte
	root   *yaml.Node
	// costroid lists the refs of Costroid's action the file calls, with the
	// release each records.
	costroid []costroidRef
	// agent is set when the file starts a coding agent.
	agent bool
}

type costroidRef struct {
	line                 int
	action, ref, release string
}

var (
	costroidUses = regexp.MustCompile(`(?i)^(costroid/costroid/action|costroid/costroid-action)@(.*)$`)
	recordedPin  = regexp.MustCompile(`^#\s*v?([0-9]+\.[0-9]+\.[0-9]+\S*)`)
	agentVersion = regexp.MustCompile(`[0-9]+\.[0-9]+\.[0-9]+[0-9A-Za-z.-]*`)
)

// readWorkflows reads every workflow GitHub would run: the YAML files at the
// top of .github/workflows.
func readWorkflows(root string) ([]workflow, error) {
	entries, err := os.ReadDir(filepath.Join(root, defaultWorkflows))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", defaultWorkflows, err)
	}
	var flows []workflow
	for _, entry := range entries {
		extension := filepath.Ext(entry.Name())
		if entry.IsDir() || extension != ".yml" && extension != ".yaml" {
			continue
		}
		name := filepath.ToSlash(filepath.Join(defaultWorkflows, entry.Name()))
		source, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		flows = append(flows, readWorkflow(name, source))
	}
	return flows, nil
}

func readWorkflow(name string, source []byte) workflow {
	flow := workflow{path: name, source: source, agent: recipelint.LintCustomer(name, source).Agents > 0}
	var document yaml.Node
	if yaml.Unmarshal(source, &document) != nil || len(document.Content) == 0 {
		return flow
	}
	flow.root = document.Content[0]
	walkUses(flow.root, func(key, value *yaml.Node) {
		if match := costroidUses.FindStringSubmatch(value.Value); match != nil {
			comment := value.LineComment
			if comment == "" {
				comment = key.LineComment
			}
			pin := costroidRef{line: value.Line, action: match[1], ref: match[2]}
			if recorded := recordedPin.FindStringSubmatch(comment); recorded != nil {
				pin.release = recorded[1]
			}
			flow.costroid = append(flow.costroid, pin)
		}
	})
	return flow
}

// walkUses calls visit for every uses: key under node.
func walkUses(node *yaml.Node, visit func(key, value *yaml.Node)) {
	if node == nil {
		return
	}
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			if key, value := node.Content[i], node.Content[i+1]; key.Value == "uses" && value.Kind == yaml.ScalarNode {
				visit(key, value)
			}
		}
	}
	for _, child := range node.Content {
		walkUses(child, visit)
	}
}

// releaseLine states this binary's release beside the releases the
// workflows pin.
func releaseLine(build buildInfo, flows []workflow) finding {
	if !releaseVersion.MatchString(build.version) {
		return warn("costroid "+build.version+" is a development build, which belongs to no release", "install a release, as README.md#install shows")
	}
	have := strings.TrimPrefix(build.version, "v")
	for _, flow := range flows {
		for _, pin := range flow.costroid {
			if pin.release != "" && pin.release != have {
				return warn(fmt.Sprintf("costroid %s, and %s:%d pins %s", have, flow.path, pin.line, pin.release),
					"install "+pin.release+", the release the workflow runs, as README.md#install shows")
			}
		}
	}
	return ok("costroid %s", have)
}

// configLines holds costroid.yml to its schema, as costroid validate does.
func configLines(root string) ([]finding, error) {
	data, err := os.ReadFile(filepath.Join(root, configFile))
	if errors.Is(err, fs.ErrNotExist) {
		return []finding{ok("%s: no file, so the defaults apply", configFile)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", configFile, err)
	}
	problems, err := config.Validate(data)
	if err != nil {
		return nil, err
	}
	if len(problems) == 0 {
		return []finding{ok("%s: valid", configFile)}, nil
	}
	var lines []finding
	for _, problem := range problems {
		lines = append(lines, fail(configFile+": "+problem.String(), "docs/configuration.md lists each key"))
	}
	return lines, nil
}

// workflowLines judges the workflows as costroid lint does, checks each pin
// of Costroid's action, and finds a pull request that pays for two agents.
func workflowLines(root string, flows []workflow) []finding {
	if !slices.ContainsFunc(flows, func(flow workflow) bool { return len(flow.costroid) > 0 }) {
		fix := "run costroid init github --agent claude"
		if _, err := os.Stat(filepath.Join(root, ".gitlab-ci.yml")); err == nil {
			fix += ", or on GitLab costroid lint-recipes .gitlab/costroid.yml"
		}
		return []finding{warn("no workflow in "+defaultWorkflows+" calls Costroid's action", fix)}
	}
	var lines []finding
	for _, flow := range flows {
		lines = append(lines, lintLines(flow)...)
		lines = append(lines, pinLines(flow)...)
	}
	return append(lines, twoAgentLine(flows)...)
}

func lintLines(flow workflow) []finding {
	report := recipelint.LintCustomer(flow.path, flow.source)
	var lines []finding
	for _, problem := range report.Findings {
		line := warn(problem.String(), "docs/lint.md says what the rule guards")
		if problem.Severity == recipelint.High {
			line.verdict = verdictFail
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 && flow.agent {
		lines = append(lines, ok("%s: costroid lint finds nothing high or medium", flow.path))
	}
	return lines
}

// pinLines holds each call of Costroid's action to the commit of a release:
// the action installs only the release its own commit belongs to.
func pinLines(flow workflow) []finding {
	const fix = "pin the full commit SHA of a release tag with its version in a comment, as costroid init writes it"
	var lines []finding
	for _, pin := range flow.costroid {
		where := fmt.Sprintf("%s:%d: %s@%s", flow.path, pin.line, pin.action, pin.ref)
		switch {
		case !fullSHA.MatchString(pin.ref):
			lines = append(lines, fail(where+" is not a full commit SHA", fix))
		case strings.Trim(pin.ref, "0") == "":
			lines = append(lines, fail(where+" is the zero SHA, which names no release", fix))
		case pin.release == "":
			lines = append(lines, warn(where+" records no release in its comment", fix))
		default:
			lines = append(lines, ok("%s:%d: %s pinned to the commit of %s", flow.path, pin.line, pin.action, pin.release))
		}
	}
	return lines
}

// twoAgentLine warns when more than one workflow starts an agent on the same
// pull request event, so each pull request pays for every one of them.
func twoAgentLine(flows []workflow) []finding {
	var paying []string
	for _, flow := range flows {
		if flow.agent && onPullRequest(flow.root) {
			paying = append(paying, flow.path)
		}
	}
	if len(paying) < 2 {
		return nil
	}
	fix := "keep one of them"
	for _, flow := range flows {
		if flow.agent && len(flow.costroid) == 0 && bytes.Contains(flow.source, []byte(claudeCodeAction+"@")) {
			fix = "keep one of them. To keep " + flow.path + ", delete the others and run costroid init github --agent claude-code-action --existing " + flow.path + ", which prints the patch that reports from it"
			break
		}
	}
	return []finding{warn(joinNames(paying)+" each run an agent on pull_request, so each pull request pays for "+fmt.Sprint(len(paying))+" runs", fix)}
}

func onPullRequest(root *yaml.Node) bool {
	_, on := mappingPair(root, "on")
	if on == nil {
		return false
	}
	events := []*yaml.Node{on}
	switch on.Kind {
	case yaml.SequenceNode:
		events = on.Content
	case yaml.MappingNode:
		events = nil
		for i := 0; i < len(on.Content); i += 2 {
			events = append(events, on.Content[i])
		}
	}
	return slices.ContainsFunc(events, func(event *yaml.Node) bool {
		return event.Value == "pull_request" || event.Value == "pull_request_target"
	})
}

func joinNames(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// agentsRun names the agents the workflows start, or claude when none does.
func agentsRun(flows []workflow) []string {
	var agents []string
	add := func(agent string) {
		if !slices.Contains(agents, agent) {
			agents = append(agents, agent)
		}
	}
	for _, flow := range flows {
		text := string(flow.source)
		if strings.Contains(text, "anthropics/claude-code-action@") || strings.Contains(text, "@anthropic-ai/claude-code@") {
			add("claude")
		}
		if strings.Contains(text, "openai/codex-action@") || strings.Contains(text, "@openai/codex@") {
			add("codex")
		}
	}
	if len(agents) == 0 {
		add("claude")
	}
	slices.Sort(agents)
	return agents
}

// agentLine checks that an agent is on PATH, for costroid run on this
// machine. CI installs its own.
func agentLine(ctx context.Context, root string, host doctorHost, agent string) finding {
	path, err := host.lookPath(agent)
	if err != nil {
		missing := (&run.MissingAgentError{Name: agent}).Error()
		fact, fix, _ := strings.Cut(missing, ": ")
		return warn(fact+", so costroid run cannot start it on this machine, though CI installs its own", fix)
	}
	out, err := host.output(ctx, root, path, "--version")
	found := agentVersion.FindString(string(out))
	if err != nil || found == "" {
		return warn(agent+" --version printed no version", "reinstall "+agent)
	}
	return ok("%s %s is on PATH", agent, found)
}

// keyVariables are the variables each agent reads its provider key from.
var keyVariables = map[string][]string{
	"claude": {"ANTHROPIC_API_KEY"},
	"codex":  {"CODEX_API_KEY", "OPENAI_API_KEY"},
}

// keyLines say whether each provider key is set in this shell, and never
// what it holds.
func keyLines(host doctorHost, agents []string) []finding {
	var lines []finding
	for _, agent := range agents {
		var set []string
		for _, name := range keyVariables[agent] {
			if host.getenv(name) != "" {
				set = append(set, name)
			}
		}
		if len(set) > 0 {
			verb := "is"
			if len(set) > 1 {
				verb = "are"
			}
			lines = append(lines, ok("%s %s set in this shell", joinNames(set), verb))
		} else {
			lines = append(lines, ok("%s is not set in this shell, so a local run of %s uses its own sign-in, priced as docs/bases.md says", strings.Join(keyVariables[agent], " or "), agent))
		}
	}
	if strings.HasPrefix(host.getenv(credentialVariable), secretKeyPrefix) {
		lines = append(lines, fail(credentialVariable+" reads like a key, and every row would carry it", "set it to the key's id from the provider's console, never the key"))
	}
	return lines
}

// repositoryLines read, through gh, what the repository holds: its secrets'
// and variables' names and its visibility.
func repositoryLines(ctx context.Context, root string, host doctorHost, flows []workflow) []finding {
	needed := secretsNeeded(flows)
	if _, err := host.lookPath("gh"); err != nil {
		if len(needed) == 0 {
			return nil
		}
		return []finding{warn("gh is not on PATH, so doctor did not list the repository's secrets",
			"install gh, or check that the repository's settings hold "+joinNames(needed))}
	}
	lines := secretLines(ctx, root, host, needed)
	lines = append(lines, variableLines(ctx, root, host)...)
	return append(lines, visibilityLines(ctx, root, host, flows)...)
}

// secretsNeeded lists the secrets the workflows that run Costroid or an agent
// read, other than the token GitHub gives every job.
func secretsNeeded(flows []workflow) []string {
	var names []string
	for _, flow := range flows {
		if len(flow.costroid) == 0 && !flow.agent {
			continue
		}
		for _, name := range secretNames(flow.source) {
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	slices.Sort(names)
	return names
}

func secretLines(ctx context.Context, root string, host doctorHost, needed []string) []finding {
	if len(needed) == 0 {
		return nil
	}
	have, err := ghNames(ctx, root, host, "secret", "list")
	if err != nil {
		return []finding{warn("gh secret list failed, so doctor did not check the repository's secrets: "+err.Error(), "run gh auth login, then doctor again")}
	}
	var lines []finding
	for _, name := range needed {
		if slices.Contains(have, name) {
			lines = append(lines, ok("the repository has the secret %s", name))
			continue
		}
		lines = append(lines, fail("gh secret list names no repository secret "+name, "gh secret set "+name))
	}
	return lines
}

func variableLines(ctx context.Context, root string, host doctorHost) []finding {
	have, err := ghNames(ctx, root, host, "variable", "list")
	if err != nil {
		return []finding{warn("gh variable list failed, so doctor did not check the repository's variables: "+err.Error(), "run gh auth login, then doctor again")}
	}
	url, audience := slices.Contains(have, "COSTROID_INGEST_URL"), slices.Contains(have, "COSTROID_INGEST_AUDIENCE")
	switch {
	case url && !audience:
		return []finding{warn("the repository sets COSTROID_INGEST_URL and not COSTROID_INGEST_AUDIENCE", "set COSTROID_INGEST_AUDIENCE to the audience your ledger requires, as docs/recipes.md#send-rows-to-a-ledger says")}
	case url:
		return []finding{ok("the repository sets COSTROID_INGEST_URL and COSTROID_INGEST_AUDIENCE, so the ingest job sends the rows")}
	}
	return nil
}

// ghNames runs a gh list command and keeps the first column, the names. A
// variable's value is in the second column, and it is never read.
func ghNames(ctx context.Context, root string, host doctorHost, args ...string) ([]string, error) {
	out, err := host.output(ctx, root, "gh", args...)
	if err != nil {
		return nil, err
	}
	var names []string
	for line := range strings.SplitSeq(string(out), "\n") {
		if fields := strings.Fields(line); len(fields) > 0 {
			names = append(names, fields[0])
		}
	}
	return names, nil
}

func visibilityLines(ctx context.Context, root string, host doctorHost, flows []workflow) []finding {
	out, err := host.output(ctx, root, "gh", "repo", "view", "--json", "isPrivate", "--jq", ".isPrivate")
	if err != nil {
		return []finding{warn("gh repo view failed, so doctor did not read whether the repository is private: "+err.Error(), "run gh auth login, then doctor again")}
	}
	if strings.TrimSpace(string(out)) != "true" {
		return []finding{ok("the repository is public, so the comment shows amounts unless %s sets comment.show_cost: false", configFile)}
	}
	lines := []finding{ok("the repository is private, so the comment always shows amounts")}
	for _, flow := range flows {
		if bytes.Contains(flow.source, []byte("step-security/harden-runner@")) {
			lines = append(lines, warn(flow.path+" runs step-security/harden-runner, which needs a paid plan on a private repository",
				"read docs/recipes.md#egress-in-the-agents-job, and keep the step or delete it"))
		}
	}
	return lines
}

// jobLines are what only a GitHub Actions job sees.
func jobLines(root string, host doctorHost) []finding {
	var lines []finding
	if event := host.getenv("GITHUB_EVENT_NAME"); event == "pull_request" {
		lines = append(lines, ok("the event is pull_request"))
	} else {
		lines = append(lines, warn("the event is "+event+", and costroid report posts only on pull_request", "run the report job on pull_request"))
	}
	lines = append(lines, forkLine(host))
	if host.getenv("ACTIONS_ID_TOKEN_REQUEST_URL") != "" {
		lines = append(lines, ok("this job holds id-token: write, which the ingest job needs and an agent's job must not hold"))
	} else {
		lines = append(lines, ok("this job does not hold id-token: write, so the ingest stage cannot send rows from it"))
	}
	return append(lines, permissionLine(root, host))
}

// forkLine reads the event file for the pull request's head repository.
func forkLine(host doctorHost) finding {
	data, err := os.ReadFile(host.getenv("GITHUB_EVENT_PATH"))
	if err != nil {
		return warn("the event file cannot be read", "run doctor --ci as a step of a job, where GITHUB_EVENT_PATH names it")
	}
	var event struct {
		PullRequest *struct {
			Head struct {
				Repo struct {
					FullName string `json:"full_name"`
				} `json:"repo"`
			} `json:"head"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(data, &event); err != nil || event.PullRequest == nil {
		return warn("the event names no pull request", "run the job on pull_request")
	}
	head, repository := event.PullRequest.Head.Repo.FullName, host.getenv("GITHUB_REPOSITORY")
	if head != repository {
		return warn("the pull request's head is the fork "+head+", which receives no secrets and cannot post the check",
			"add recipes/github-follow-up.yml, which posts it from "+repository)
	}
	return ok("the pull request's head is a branch of %s", repository)
}

// permissionLine reads the job's declared permissions from the workflow file
// the job runs.
func permissionLine(root string, host doctorHost) finding {
	ref, job := host.getenv("GITHUB_WORKFLOW_REF"), host.getenv("GITHUB_JOB")
	repository := host.getenv("GITHUB_REPOSITORY")
	name, _, _ := strings.Cut(strings.TrimPrefix(ref, repository+"/"), "@")
	source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if name == "" || job == "" || err != nil {
		return warn("the workflow file of job "+job+" cannot be read from this checkout", "check the repository out before this step")
	}
	var document yaml.Node
	if yaml.Unmarshal(source, &document) != nil || len(document.Content) == 0 {
		return warn(name+" is not valid YAML", "costroid lint "+name+" says where")
	}
	_, jobs := mappingPair(document.Content[0], "jobs")
	_, declared := mappingPair(jobs, job)
	_, permissions := mappingPair(declared, "permissions")
	if permissions == nil {
		_, permissions = mappingPair(document.Content[0], "permissions")
	}
	if permissions == nil {
		return warn("job "+job+" of "+name+" declares no permissions, so it holds the repository's default token", "declare the permissions the job needs, as the recipes do")
	}
	return ok("job %s of %s declares permissions %s", job, name, flowPermissions(permissions))
}

func flowPermissions(node *yaml.Node) string {
	if node.Kind == yaml.ScalarNode {
		return node.Value
	}
	if len(node.Content) == 0 {
		return "{}"
	}
	var pairs []string
	for i := 0; i+1 < len(node.Content); i += 2 {
		pairs = append(pairs, node.Content[i].Value+": "+node.Content[i+1].Value)
	}
	return strings.Join(pairs, ", ")
}
