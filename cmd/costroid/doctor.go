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
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/config"
	"github.com/Costroid/costroid/internal/focus"
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

With COSTROID_PLATFORM_URL set in the shell, doctor asks Costroid Platform for
the scopes of the integration token in COSTROID_PLATFORM_TOKEN and for the
binding of the repository's owner. It names the token by that variable alone.

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
// environment, the programs on PATH and their output, and Costroid Platform.
// Tests replace each.
type doctorHost struct {
	getenv   func(string) string
	lookPath func(string) (string, error)
	// output runs a program in dir and returns its stdout.
	output func(ctx context.Context, dir, name string, args ...string) ([]byte, error)
	// client asks Costroid Platform about the integration token.
	client *http.Client
	now    func() time.Time
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

// doctorCommand asks the platform with a client that follows no redirect, so
// the token reaches only the origin named, as connect's does.
func doctorCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	client := &http.Client{
		Timeout:       doctorTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	host := doctorHost{getenv: os.Getenv, lookPath: exec.LookPath, output: runOutput, client: client, now: time.Now}
	return doctor(ctx, ".", buildInfo{version: version, commit: commit}, host, args, stdout, stderr)
}

// doctor checks the repository at root and prints its table.
func doctor(ctx context.Context, root string, build buildInfo, host doctorHost, args []string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("doctor", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	ci := set.Bool("ci", false, "add the checks only a GitHub Actions job can make")
	err := parseFlags(set, args, doctorHelp, stdout, stderr)
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
	lines = append(lines, platformLines(ctx, root, host)...)
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
	// action is the agent's own action the file calls, by the name init's
	// --agent takes, or empty.
	action string
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
	recipelint.Uses(flow.root, func(key, value *yaml.Node) {
		for _, action := range agentActions {
			if flow.action == "" && strings.HasPrefix(value.Value, action.uses+"@") {
				flow.action = action.agent
			}
		}
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
// A repository whose workflow already runs an agent's action is sent to
// init --existing, which reports from that workflow instead of adding one.
func workflowLines(root string, flows []workflow) []finding {
	if !slices.ContainsFunc(flows, func(flow workflow) bool { return len(flow.costroid) > 0 }) {
		fix := "run costroid init github --agent claude"
		if index := slices.IndexFunc(flows, func(flow workflow) bool { return flow.action != "" }); index >= 0 {
			fix = existingFix(flows[index])
		}
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
		if flow.agent && len(flow.costroid) == 0 && flow.action != "" {
			fix = "keep one of them. To keep " + flow.path + ", delete the others and " + existingFix(flow)
			break
		}
	}
	return []finding{warn(joinNames(paying)+" each run an agent on pull_request, so each pull request pays for "+fmt.Sprint(len(paying))+" runs", fix)}
}

// existingFix sends a workflow that runs an agent's action to init
// --existing.
func existingFix(flow workflow) string {
	return "run costroid init github --agent " + flow.action + " --existing " + flow.path + ", which prints the patch that reports from it"
}

// onPullRequest reads the workflow's events as costroid lint does, so a
// trigger written through an alias counts.
func onPullRequest(root *yaml.Node) bool {
	return slices.ContainsFunc(recipelint.EventNames(root), func(event string) bool {
		return event == "pull_request" || event == "pull_request_target"
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
	if focus.ReadsLikeKey(host.getenv(credentialVariable)) {
		lines = append(lines, fail(credentialVariable+" reads like a key, and every row would carry it", "set it to the key's id from the provider's console, never the key"))
	}
	return lines
}

// ghNoRemote is what gh prints when the repository has no remote it can name
// a GitHub repository from. Every gh read then fails alike.
const ghNoRemote = "no git remotes found"

// ghFix says what to do about a failed gh read, from gh's own error.
func ghFix(err error) string {
	if strings.Contains(err.Error(), ghNoRemote) {
		return "add a GitHub remote, or push the repository first"
	}
	return "run gh auth login, then doctor again"
}

// repositoryLines read, through gh, what the repository holds: its secrets'
// and variables' names and its visibility. With the platform's URL set, in
// this shell or as a repository variable, the integration token's secret is
// needed by name.
func repositoryLines(ctx context.Context, root string, host doctorHost, flows []workflow) []finding {
	needed := secretsNeeded(flows)
	if _, err := host.lookPath("gh"); err != nil {
		if host.getenv(platformVariable) != "" {
			needed = append(needed, platformTokenVariable)
		}
		if len(needed) == 0 {
			return nil
		}
		return []finding{warn("gh is not on PATH, so doctor did not list the repository's secrets",
			"install gh, or check that the repository's settings hold "+joinNames(needed))}
	}
	variables, err := ghNames(ctx, root, host, "variable", "list")
	if err != nil && strings.Contains(err.Error(), ghNoRemote) {
		return []finding{warn("gh variable list failed, so doctor did not read the repository's secrets, variables or visibility: "+err.Error(), ghFix(err))}
	}
	if host.getenv(platformVariable) != "" || slices.Contains(variables, platformVariable) {
		needed = append(needed, platformTokenVariable)
	}
	lines := secretLines(ctx, root, host, needed)
	lines = append(lines, variableLines(variables, err)...)
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
		return []finding{warn("gh secret list failed, so doctor did not check the repository's secrets: "+err.Error(), ghFix(err))}
	}
	var lines []finding
	for _, name := range needed {
		if slices.Contains(have, name) {
			lines = append(lines, ok("the repository has the secret %s", name))
			continue
		}
		lines = append(lines, warn("gh secret list names no repository secret "+name+"; an organization or environment secret of that name is not listed here", "gh secret set "+name+", unless the organization or the environment already holds it"))
	}
	return lines
}

// variableLines judge the variables gh variable list named, or the error it
// failed with.
func variableLines(have []string, err error) []finding {
	if err != nil {
		return []finding{warn("gh variable list failed, so doctor did not check the repository's variables: "+err.Error(), ghFix(err))}
	}
	var lines []finding
	url, audience := slices.Contains(have, "COSTROID_INGEST_URL"), slices.Contains(have, "COSTROID_INGEST_AUDIENCE")
	switch {
	case url && !audience:
		lines = append(lines, warn("the repository sets COSTROID_INGEST_URL and not COSTROID_INGEST_AUDIENCE", "set COSTROID_INGEST_AUDIENCE to the audience your ledger requires, as docs/recipes.md#send-rows-to-a-ledger says"))
	case url:
		lines = append(lines, ok("the repository sets COSTROID_INGEST_URL and COSTROID_INGEST_AUDIENCE, so the ingest job sends the rows"))
	}
	if slices.Contains(have, platformVariable) {
		lines = append(lines, ok("the repository sets %s, so the ingest job sends the rows to Costroid Platform", platformVariable))
	}
	return lines
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
		return []finding{warn("gh repo view failed, so doctor did not read whether the repository is private: "+err.Error(), ghFix(err))}
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

// tokenScopes are the scopes docs/connect.md gives the one token per owner:
// connect uses the first two, and export the first and the third.
var tokenScopes = []string{"identity:read", "installations:write", "rows:write"}

var scopePattern = regexp.MustCompile(`^[a-z]+:[a-z]+$`)

const (
	// bindingCurrentFor is how long a binding stays current after its last
	// proof, under integration/v0.2.
	bindingCurrentFor = 7 * 24 * time.Hour
	connectFix        = "run costroid connect from a scheduled job, as docs/connect.md says"
)

// platformLines ask Costroid Platform, when COSTROID_PLATFORM_URL is set in
// this shell, for the scopes of the integration token in
// COSTROID_PLATFORM_TOKEN and for the binding of the repository's owner. They
// name the token by its variable alone.
func platformLines(ctx context.Context, root string, host doctorHost) []finding {
	address := host.getenv(platformVariable)
	if address == "" {
		return nil
	}
	base, err := integrationBase(address)
	if err != nil {
		return []finding{warn(platformVariable+" is not an https origin, so doctor did not ask Costroid Platform", "set it to https://app.costroid.com, or to http on a loopback address")}
	}
	token := host.getenv(platformTokenVariable)
	if token == "" {
		return []finding{ok("%s is not set in this shell, so doctor did not read its scopes or the binding; set it to the integration token to check them", platformTokenVariable)}
	}
	if !integrationTokenPattern.MatchString(token) {
		return []finding{fail(platformTokenVariable+" is not an integration token, which starts costroid_it_", "create one on Costroid Platform, as docs/connect.md says")}
	}
	target := platform{client: host.client, base: base, token: token}
	scopes, err := target.currentScopes(ctx)
	if err != nil {
		return []finding{platformFailure("the token request", err)}
	}
	lines := []finding{scopeLine(scopes)}
	if !slices.Contains(scopes.Scopes, "identity:read") {
		return lines
	}
	return append(lines, bindingLines(ctx, root, host, target)...)
}

// tokenAnswer is the part of GET /tokens/current that doctor reads. The
// answer never holds the secret.
type tokenAnswer struct {
	Scopes    []string `json:"scopes"`
	ExpiresAt string   `json:"expires_at"`
	expires   time.Time
}

func (p platform) currentScopes(ctx context.Context) (tokenAnswer, error) {
	request, err := p.request(ctx, http.MethodGet, "/tokens/current", nil)
	if err != nil {
		return tokenAnswer{}, err
	}
	_, data, err := p.do(request, "the token request")
	if err != nil {
		return tokenAnswer{}, err
	}
	var answer tokenAnswer
	if err := json.Unmarshal(data, &answer); err != nil || len(answer.Scopes) == 0 ||
		slices.ContainsFunc(answer.Scopes, func(scope string) bool { return !scopePattern.MatchString(scope) }) {
		return tokenAnswer{}, errors.New("the token answer of Costroid Platform names no scope it can print")
	}
	answer.expires, err = time.Parse(time.RFC3339, answer.ExpiresAt)
	if err != nil {
		return tokenAnswer{}, errors.New("the token answer of Costroid Platform names no time it expires")
	}
	return answer, nil
}

// scopeLine states the token's scopes, and the ones docs/connect.md gives it
// that it lacks.
func scopeLine(token tokenAnswer) finding {
	held := fmt.Sprintf("%s in this shell carries %s, and expires at %s", platformTokenVariable, joinNames(token.Scopes), token.expires.UTC().Format(time.RFC3339))
	var lacks []string
	for _, scope := range tokenScopes {
		if !slices.Contains(token.Scopes, scope) {
			lacks = append(lacks, scope)
		}
	}
	if len(lacks) == 0 {
		return ok("%s", held)
	}
	return warn(held+", and lacks "+joinNames(lacks), "create one with "+joinNames(tokenScopes)+", as docs/connect.md says")
}

// bindingLines state the binding of the installation a CI job of this
// repository proves, as GET /identity lists it, and on a line of its own a
// binding of the same owner made through the GitHub App.
func bindingLines(ctx context.Context, root string, host doctorHost, target platform) []finding {
	installation, err := ownerInstallation(ctx, root, host)
	if err != nil {
		fix := "run doctor where gh can read the repository, or as a step of the connect job"
		if strings.Contains(err.Error(), ghNoRemote) {
			fix = ghFix(err)
		}
		return []finding{warn("doctor did not read the binding, because it could not name the repository's owner: "+err.Error(), fix)}
	}
	identity, err := target.identity(ctx)
	if err != nil {
		return []finding{platformFailure("the identity request", err)}
	}
	lines := []finding{bindingLine(installation, identity, host.now())}
	if app, found := identity.app(installation); found {
		lines = append(lines, ok("tenant %s also holds owner %s through the GitHub App installation %s, proven at %s", identity.Tenant.ID, app.owner, app.id, app.provenAt.UTC().Format(time.RFC3339)))
	}
	return lines
}

// bindingLine states the binding of the installation, which only a job's
// proof makes.
func bindingLine(installation string, identity identityAnswer, now time.Time) finding {
	tenant := identity.Tenant.ID
	var ended *installationAnswer
	for index, held := range identity.Installations {
		if held.ID != installation {
			continue
		}
		if held.Status != "active" {
			ended = &identity.Installations[index]
			continue
		}
		proven, err := time.Parse(time.RFC3339, held.ProvenAt)
		if err != nil {
			return warn(fmt.Sprintf("tenant %s lists %s as active, with no time it was proven", tenant, installation), connectFix)
		}
		at := proven.UTC().Format(time.RFC3339)
		if now.Sub(proven) >= bindingCurrentFor {
			return warn(fmt.Sprintf("%s is bound to tenant %s, last proven at %s, more than 7 days ago, so another tenant's proof can displace it", installation, tenant, at), connectFix)
		}
		return ok("%s is bound to tenant %s, proven at %s", installation, tenant, at)
	}
	if ended != nil && installationStatus.MatchString(ended.Status) {
		return warn(fmt.Sprintf("%s was bound to tenant %s and is %s, so Costroid Platform refuses its rows with %s", installation, tenant, ended.Status, installationNotBound), connectFix)
	}
	return warn(fmt.Sprintf("tenant %s holds no binding of %s, so Costroid Platform refuses its rows with %s", tenant, installation, installationNotBound), connectFix)
}

// installationStatus is integration/v0.2's InstallationStatus for an ended
// binding, so nothing else the platform sends is printed.
var installationStatus = regexp.MustCompile(`^(revoked|displaced)$`)

// ownerInstallation names the installation a CI job of this repository
// proves: from the job's own variables in CI, as connect reads them, and
// elsewhere from the owner's account id that gh reads.
func ownerInstallation(ctx context.Context, root string, host doctorHost) (string, error) {
	if host.getenv("GITHUB_ACTIONS") == "true" || host.getenv("GITLAB_CI") == "true" {
		job, err := connectJob("", host.getenv)
		return job.installation, err
	}
	if _, err := host.lookPath("gh"); err != nil {
		return "", errors.New("gh is not on PATH")
	}
	out, err := host.output(ctx, root, "gh", "api", "repos/{owner}/{repo}", "--jq", ".owner.id")
	if err != nil {
		return "", err
	}
	owner := strings.TrimSpace(string(out))
	if !installationNumber.MatchString(owner) {
		return "", errors.New("gh api named no owner's number")
	}
	return githubOwnerKind + owner, nil
}

// platformFailure states a request doctor could not complete: a refusal by
// its status and code, as connect prints it, or the error.
func platformFailure(what string, err error) finding {
	var refused *refusal
	if !errors.As(err, &refused) {
		return warn("doctor could not ask Costroid Platform: "+err.Error(), "check "+platformVariable+" and the network, then doctor again")
	}
	code := refused.code
	if code == "" {
		code = "with no refusal code"
	}
	fact := fmt.Sprintf("Costroid Platform refused %s: %d %s", what, refused.status, code)
	switch refused.code {
	case "AUTH_TOKEN_INVALID":
		return warn(fact, "the token is unknown, expired or revoked, so create one on Costroid Platform")
	case "AUTH_TOKEN_SCOPE":
		return warn(fact, "create a token with "+joinNames(tokenScopes)+", as docs/connect.md says")
	default:
		return warn(fact, "the contract's refusals in docs/connect.md say what each code means")
	}
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
	declared := recipelint.Field(recipelint.Field(document.Content[0], "jobs"), job)
	permissions := recipelint.Field(declared, "permissions")
	if permissions == nil {
		permissions = recipelint.Field(document.Content[0], "permissions")
	}
	if permissions == nil {
		return warn("job "+job+" of "+name+" declares no permissions, so it holds the repository's default token", "declare the permissions the job needs, as the recipes do")
	}
	granted, err := flowPermissions(permissions)
	if err != nil {
		return warn("job "+job+" of "+name+" declares permissions doctor cannot read: "+err.Error(), "costroid lint "+name+" says where")
	}
	return ok("job %s of %s declares permissions %s", job, name, granted)
}

// flowPermissions writes a permissions block on one line: a scalar as it
// is, a mapping as its pairs in name order, read through aliases and merge
// keys.
func flowPermissions(node *yaml.Node) (string, error) {
	if node.Kind == yaml.ScalarNode {
		return node.Value, nil
	}
	var granted map[string]string
	if err := node.Decode(&granted); err != nil {
		return "", err
	}
	if len(granted) == 0 {
		return "{}", nil
	}
	var pairs []string
	for _, name := range slices.Sorted(maps.Keys(granted)) {
		pairs = append(pairs, name+": "+granted[name])
	}
	return strings.Join(pairs, ", "), nil
}
