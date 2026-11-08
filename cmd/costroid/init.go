package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
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
  costroid init [github|gitlab] --agent AGENT [--out PATH] [--force]
                [--action-ref SHA] [--skills]
  costroid init github --agent AGENT [--post-review] [--platform-url URL]
                [--pr [--key-env NAME]]
  costroid init github --agent claude-code-action --auth subscription
  costroid init github --existing PATH [--action-ref SHA] [--post-review]
                [--platform-url URL] [--budget-usd AMOUNT]

init writes the recipe for a host and an agent as this release ships it, with
Costroid pinned to the commit and version of the release this binary belongs
to. It runs anywhere inside a git repository and writes at its root:
.github/workflows/costroid.yml on GitHub and .gitlab/costroid.yml on GitLab,
or --out, a path inside the repository. It refuses to replace a file without
--force. It writes costroid.yml, with every key it reads commented out, only
when there is none. On GitLab it adds the template to the include of
.gitlab-ci.yml, and writes that file when there is none.

The host is the one the repository's remote names. github, gitlab or --host
names it instead. --agent is claude, codex, claude-code-action or codex-action
on GitHub, and claude or codex on GitLab.

With no --agent, a workflow that already runs anthropics/claude-code-action or
openai/codex-action switches init to --existing, and init says so. --existing
reads such a workflow and prints a patch that adds a read step after the
agent's step and a report job that runs on pull requests. It writes nothing.
On a claude-code-action step, the patch adds --max-budget-usd 5 to claude_args
when they hold no cap, or the amount in USD --budget-usd names, and gives the
read step and the report job the cap they hold. docs/existing-workflow.md
shows the result.

--auth subscription writes the claude-code-action recipe with a Claude
subscription's token, CLAUDE_CODE_OAUTH_TOKEN, in place of an API key.

--post-review adds post-review: "true" to the agent's step and the report's
step, so the agent's own answer is posted as a second comment. docs/review.md
says what that comment shows.

--platform-url names Costroid Platform's address. init prints the command that
sets it as the repository variable COSTROID_PLATFORM_URL, which the recipe's
ingest job reads, and with --existing the patch adds that ingest job.
docs/export.md says what else the platform needs.

--skills writes .agents/skills/costroid/SKILL.md for Codex and
.claude/skills/costroid/SKILL.md for Claude Code: a skill that reads a run's
status and the costroid check's result. A skill file that exists is left as
it is.

It holds the recipe to the rules of lint-recipes before it writes it. Then it
prints what each push can spend and the commands that are left. It runs none
of them, unless --pr is given.

--pr runs the commands that are left on GitHub, and prints each before it
runs it. It sets the agent's key as a repository secret unless gh secret list
names that secret already, then switches to the branch try-costroid, commits,
pushes, opens the pull request, watches its run and prints the URL of
Costroid's comment. The key goes to gh secret set on standard input, read
from the environment variable --key-env names, or else from a prompt on the
terminal that does not echo it. init never prints it, logs it or writes it to
a file. It stops at the first command that fails and lists the ones left.

A development build belongs to no release, so it pins nothing and refuses.
--action-ref names the commit of Costroid's action to pin on GitHub instead.

Exit codes of init:
  0                 written, or the patch printed
  1                 the recipe breaks a rule, which is a defect in costroid
  2                 the directory is in no git repository
  125               costroid could not read or write a file, refused to, or
                    a command --pr runs failed
`

const (
	githubHost         = "github"
	gitlabHost         = "gitlab"
	costroidActionPath = "Costroid/costroid/action"
	claudeCodeAction   = "anthropics/claude-code-action"
	codexAction        = "openai/codex-action"
	agentLabel         = "costroid-agent"
	addedStepID        = "claude"
	addedCodexStepID   = "codex"
	gitlabCI           = ".gitlab-ci.yml"
	subscriptionAuth   = "subscription"
	apiKeyAuth         = "api-key"
	exitNoRepository   = 2

	// pullRequestOnly is the report job's condition in a patch. The report
	// posts on pull_request alone, so no other event starts a runner for it.
	pullRequestOnly = "always() && github.event_name == 'pull_request'"

	// subscriptionBasis is what a run on a Claude subscription's token reads.
	subscriptionBasis = "Figures will read List-equivalent, not billed. No subscription run has been recorded yet, so that reading is unverified."
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

// initOut is where each host reads the file init writes, from the root.
var initOut = map[string]string{
	githubHost: ".github/workflows/costroid.yml",
	gitlabHost: ".gitlab/costroid.yml",
}

// gitlabKeys names the provider key each agent reads on GitLab, where the
// template takes it from the job's environment.
var gitlabKeys = map[string]string{"claude": "ANTHROPIC_API_KEY", "codex": "CODEX_API_KEY"}

// agentActions are the agents' own actions that --existing reads a run of,
// each with the agent name init knows it by and the id it gives a step that
// has none.
var agentActions = []agentAction{
	{uses: claudeCodeAction, agent: "claude-code-action", stepID: addedStepID},
	{uses: codexAction, agent: "codex-action", stepID: addedCodexStepID},
}

type agentAction struct {
	uses, agent, stepID string
}

// reservedKeys are the schema's top-level keys that this release reads
// nothing from, so the costroid.yml init writes leaves them out.
var reservedKeys = []string{"policy_pack", "budget_owner", "assumptions", "catalog", "agents"}

var (
	releaseVersion = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$`)
	fullSHA        = regexp.MustCompile(`^[0-9a-f]{40}$`)
	actionPin      = regexp.MustCompile(regexp.QuoteMeta(costroidActionPath) + `@[0-9a-f]{40}(?: #[^\n]*)?`)
	gitlabVersion  = regexp.MustCompile(`(?m)^( +COSTROID_VERSION: ).*$`)
	gitlabCommit   = regexp.MustCompile(`(?m)^( +COSTROID_COMMIT: ).*$`)
	gitlabAgent    = regexp.MustCompile(`(?m)^( +COSTROID_AGENT: ).*$`)
	gitlabBudget   = regexp.MustCompile(`(?m)^ +COSTROID_BUDGET_USD: "?([0-9]+(?:\.[0-9]+)?)"?$`)
	agentStage     = regexp.MustCompile(`(?m)^( +)stage: (?:run|read)\n`)
	reportStage    = regexp.MustCompile(`(?m)^( +)stage: report\n`)
	variableName   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	plainAmount    = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?$`)
	secretName     = regexp.MustCompile(`\$\{\{\s*secrets\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)
	apiKeyInput    = regexp.MustCompile(`(?m)^( +)anthropic_api_key: \$\{\{ secrets\.ANTHROPIC_API_KEY \}\}$`)
	// copyInstruction is a recipe header's sentence that tells a reader where
	// to copy the file, which init has done.
	copyInstruction = regexp.MustCompile(`Copy (?:to|into) .*?(?:\.(?: |$)|, then )`)
)

var errNoRepository = errors.New("this directory is in no git repository, and init writes at a repository's root: run it inside your repository")

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

// repository is where init runs: the root of the working tree, and the
// directory the command was started in, inside it.
type repository struct {
	root, dir string
}

// place reads a path as the shell that started init would, and returns it
// relative to the root. A path outside the root is refused.
func (r repository) place(name string) (string, error) {
	full := name
	if !filepath.IsAbs(full) {
		full = filepath.Join(r.dir, name)
	}
	relative, err := filepath.Rel(r.root, full)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the repository: name a file under its root", name)
	}
	return relative, nil
}

func (r repository) file(relative string) string {
	return filepath.Join(r.root, relative)
}

// locate finds the repository a command was started in.
type locate func() (repository, error)

func initCommand(args []string, stdout, stderr io.Writer) int {
	return initRepository(locateRepository, buildInfo{version: version, commit: commit}, costroid.Recipes, prDefaults, args, stdout, stderr)
}

// locateRepository asks git for the root of the working tree the current
// directory is in.
func locateRepository() (repository, error) {
	dir, err := os.Getwd()
	if err != nil {
		return repository{}, fmt.Errorf("read the current directory: %w", err)
	}
	command := exec.Command("git", "rev-parse", "--show-toplevel")
	command.Dir = dir
	output, err := command.Output()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		return repository{}, errNoRepository
	case err != nil:
		return repository{}, fmt.Errorf("find the repository's root with git: %w", err)
	}
	root, err := filepath.EvalSymlinks(strings.TrimSpace(string(output)))
	if err != nil {
		return repository{}, fmt.Errorf("read the repository's root: %w", err)
	}
	if dir, err = filepath.EvalSymlinks(dir); err != nil {
		return repository{}, fmt.Errorf("read the current directory: %w", err)
	}
	return repository{root: root, dir: dir}, nil
}

// initRepository runs init in the repository find returns, with the recipes
// recipes holds under recipes/, and with pr for what --pr reads beyond the
// arguments.
func initRepository(find locate, build buildInfo, recipes fs.FS, pr prSettings, args []string, stdout, stderr io.Writer) int {
	flags, err := parseInit(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return initFailed(stderr, err)
	}
	repo, err := find()
	if errors.Is(err, errNoRepository) {
		fmt.Fprintf(stderr, "costroid: init: %v\n", err)
		return exitNoRepository
	}
	if err != nil {
		return initFailed(stderr, err)
	}
	flags, switched, err := resolveTarget(repo, flags)
	if err != nil {
		return initFailed(stderr, err)
	}
	release, err := resolvePin(build, flags.actionRef, flags.host)
	if err != nil {
		return initFailed(stderr, err)
	}
	if switched {
		fmt.Fprintf(stderr, "costroid: %s already runs %s, so init prints the patch that reports from it, as --existing %s does\n", filepath.ToSlash(flags.existing), actionFor(flags.agent).uses, filepath.ToSlash(flags.existing))
	}
	if flags.existing != "" {
		return printExistingPatch(repo, flags, release, stdout, stderr)
	}
	return writeRecipe(repo, flags, release, recipes, pr, stdout, stderr)
}

func initFailed(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "costroid: init: %v\n", err)
	return exitFailure
}

type initFlags struct {
	host, agent, auth, out, existing, actionRef string
	keyEnv, platformURL, budgetUSD              string
	force, postReview, skills, pr               bool
}

func parseInit(args []string, stderr io.Writer) (initFlags, error) {
	set := flag.NewFlagSet("init", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	set.Usage = func() { fmt.Fprint(stderr, initHelp) }
	var flags initFlags
	var host string
	set.StringVar(&host, "host", "", "the host the recipe runs on")
	set.StringVar(&flags.agent, "agent", "", "the agent the recipe runs or reads")
	set.StringVar(&flags.auth, "auth", "", "how the agent signs in: api-key or subscription")
	set.StringVar(&flags.out, "out", "", "the file to write the recipe to")
	set.StringVar(&flags.existing, "existing", "", "a workflow to print a patch for")
	set.StringVar(&flags.actionRef, "action-ref", "", "the commit of Costroid's action to pin")
	set.BoolVar(&flags.force, "force", false, "replace a file that exists")
	set.BoolVar(&flags.postReview, "post-review", false, "post the agent's answer as a second comment")
	set.StringVar(&flags.platformURL, "platform-url", "", "Costroid Platform's address, for the ingest job")
	set.BoolVar(&flags.skills, "skills", false, "write the skill for Codex and Claude Code")
	set.BoolVar(&flags.pr, "pr", false, "run the commands that are left and open the pull request")
	set.StringVar(&flags.keyEnv, "key-env", "", "the environment variable that holds the agent's key, for --pr")
	set.StringVar(&flags.budgetUSD, "budget-usd", "", "the cap in USD --existing adds to claude_args that hold none")
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
	switch {
	case host == "":
	case flags.host != "" && flags.host != host:
		return flags, fmt.Errorf("the host is %s or --host %s, not both", flags.host, host)
	default:
		flags.host = host
	}
	if flags.platformURL != "" {
		origin, err := platformOrigin(flags.platformURL)
		if err != nil {
			return flags, err
		}
		flags.platformURL = origin
	}
	return flags, flags.check()
}

// check refuses flags that cannot go together, before init reads the
// repository.
func (f initFlags) check() error {
	_, known := initRecipes[f.host]
	switch {
	case f.host != "" && !known:
		return fmt.Errorf("the host is github or gitlab, not %q", f.host)
	case f.auth != "" && f.auth != apiKeyAuth && f.auth != subscriptionAuth:
		return fmt.Errorf("--auth is %s or %s, not %q", apiKeyAuth, subscriptionAuth, f.auth)
	case f.existing != "" && (f.out != "" || f.force):
		return errors.New("--existing writes nothing, so --out and --force do not apply")
	case f.existing != "" && f.auth != "":
		return errors.New("--existing keeps the sign-in the workflow has, so --auth does not apply")
	case f.existing != "" && (f.pr || f.skills):
		return errors.New("--existing writes nothing, so --pr and --skills do not apply")
	case f.keyEnv != "" && !f.pr:
		return errors.New("--key-env names the key --pr sets as a secret, so it needs --pr")
	case f.keyEnv != "" && !variableName.MatchString(f.keyEnv):
		return fmt.Errorf("--key-env names an environment variable, such as ANTHROPIC_API_KEY, not %q", f.keyEnv)
	case f.budgetUSD != "" && (!plainAmount.MatchString(f.budgetUSD) || strings.Trim(f.budgetUSD, "0.") == ""):
		return fmt.Errorf("--budget-usd is an amount in USD above 0, such as 5 or 2.50, not %q", f.budgetUSD)
	}
	return nil
}

// checkHost refuses what only GitHub takes, once the host is known.
func (f initFlags) checkHost() error {
	if f.host == githubHost {
		return nil
	}
	switch {
	case f.pr:
		return errors.New("--pr opens a pull request with gh, which works on GitHub alone")
	case f.postReview:
		return errors.New("--post-review posts the agent's answer on GitHub alone")
	case f.platformURL != "":
		return errors.New("--platform-url sets a GitHub repository variable: on GitLab, set the CI/CD variables docs/export.md lists")
	}
	return nil
}

// resolveTarget settles the host, the agent and the workflow --existing
// reads, from the flags and the repository. It reports whether a workflow
// the repository already has switched init to --existing.
func resolveTarget(repo repository, flags initFlags) (initFlags, bool, error) {
	if flags.host == "" && flags.existing != "" {
		flags.host = githubHost
	}
	if flags.host == "" {
		host, err := remoteHost(repo.root)
		if err != nil {
			return flags, false, err
		}
		flags.host = host
	}
	agents := initRecipes[flags.host]
	switched := false
	if flags.existing != "" {
		placed, err := repo.place(flags.existing)
		if err != nil {
			return flags, false, err
		}
		flags.existing = placed
	} else if flags.host == githubHost && flags.agent == "" {
		found, err := existingWorkflows(repo.root)
		if err != nil {
			return flags, false, err
		}
		switch len(found) {
		case 0:
		case 1:
			flags.existing, flags.agent, switched = found[0].path, found[0].action.agent, true
		default:
			names := make([]string, len(found))
			for i, workflow := range found {
				names[i] = filepath.ToSlash(workflow.path)
			}
			return flags, false, fmt.Errorf("%s each run an agent's action: name the one to report from with --existing", joinNames(names))
		}
	}
	switch {
	case flags.existing != "" && flags.host != githubHost:
		return flags, false, errors.New("--existing reads a GitHub workflow that runs claude-code-action or codex-action, and the host is gitlab")
	case flags.existing != "" && flags.agent != "" && actionFor(flags.agent).uses == "":
		return flags, false, errors.New("--existing reads a GitHub workflow that runs claude-code-action or codex-action: pass one of those to --agent, or no --agent")
	case flags.existing == "" && flags.budgetUSD != "":
		return flags, false, errors.New("--budget-usd sets the cap the --existing patch adds to claude_args: the recipes init writes hold their own")
	case switched && (flags.pr || flags.skills):
		return flags, false, fmt.Errorf("%s already runs %s, and init reads it as --existing does, which writes nothing: pass --agent to write a new workflow with --pr or --skills", filepath.ToSlash(flags.existing), actionFor(flags.agent).uses)
	case flags.existing != "":
		return flags, switched, errors.Join(flags.checkAuth(), flags.checkHost())
	case flags.agent == "":
		return flags, false, fmt.Errorf("--agent is one of %s", agentNames(agents))
	case agents[flags.agent] == "":
		return flags, false, fmt.Errorf("--agent on %s is one of %s, not %q", flags.host, agentNames(agents), flags.agent)
	}
	return flags, false, errors.Join(flags.checkAuth(), flags.checkHost())
}

func (f initFlags) checkAuth() error {
	if f.auth == subscriptionAuth && (f.host != githubHost || f.agent != "claude-code-action") {
		return errors.New("--auth subscription writes the claude-code-action recipe: pass github --agent claude-code-action, because the other recipes run the agent on an API key")
	}
	return nil
}

func actionFor(agent string) agentAction {
	for _, action := range agentActions {
		if action.agent == agent {
			return action
		}
	}
	return agentAction{}
}

func agentNames(agents map[string]string) string {
	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

// remoteHost is the host the repository's remote names: origin's, or the
// only remote's.
func remoteHost(root string) (string, error) {
	const fix = "name the host, github or gitlab"
	name, err := remoteName(root)
	if err != nil {
		return "", fmt.Errorf("%s: %w", fix, err)
	}
	address, err := gitOutput(root, "remote", "get-url", name)
	if err != nil {
		return "", fmt.Errorf("%s: git could not read the remote %s: %w", fix, name, err)
	}
	server := remoteServer(strings.TrimSpace(address))
	switch {
	case strings.Contains(server, githubHost):
		return githubHost, nil
	case strings.Contains(server, gitlabHost):
		return gitlabHost, nil
	}
	return "", fmt.Errorf("%s: the remote %s names neither", fix, name)
}

// remoteName is the remote init reads the host from and --pr pushes to:
// origin, or the only remote.
func remoteName(root string) (string, error) {
	listed, err := gitOutput(root, "remote")
	if err != nil {
		return "", fmt.Errorf("git could not list the remotes: %w", err)
	}
	names := strings.Fields(listed)
	switch {
	case slices.Contains(names, "origin"):
		return "origin", nil
	case len(names) == 1:
		return names[0], nil
	case len(names) == 0:
		return "", errors.New("the repository has no remote")
	}
	return "", fmt.Errorf("the repository has %d remotes and none is origin", len(names))
}

// remoteServer is the lower-case host name of a remote's address, as a URL or
// in the user@host:path form.
func remoteServer(address string) string {
	if parsed, err := url.Parse(address); err == nil && parsed.Host != "" {
		return strings.ToLower(parsed.Hostname())
	}
	if _, after, found := strings.Cut(address, "@"); found {
		address = after
	}
	server, _, _ := strings.Cut(address, ":")
	return strings.ToLower(server)
}

func gitOutput(root string, args ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.Output()
	return string(output), err
}

// existingWorkflow is a workflow that runs an agent's own action.
type existingWorkflow struct {
	path   string
	action agentAction
}

// existingWorkflows lists the workflows under .github/workflows that run one
// agent's action and do not call Costroid's yet, by path from the root.
func existingWorkflows(root string) ([]existingWorkflow, error) {
	directory := filepath.Join(".github", "workflows")
	entries, err := os.ReadDir(filepath.Join(root, directory))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the workflows: %w", err)
	}
	var found []existingWorkflow
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || (!strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml")) {
			continue
		}
		source, err := os.ReadFile(filepath.Join(root, directory, name))
		if err != nil {
			return nil, fmt.Errorf("read the workflows: %w", err)
		}
		if bytes.Contains(source, []byte(costroidActionPath+"@")) {
			continue
		}
		var document yaml.Node
		if yaml.Unmarshal(source, &document) != nil || len(document.Content) != 1 {
			continue
		}
		_, jobs := mappingPair(document.Content[0], "jobs")
		if steps := agentSteps(jobs, ""); len(steps) > 0 {
			found = append(found, existingWorkflow{path: filepath.Join(directory, name), action: steps[0].action})
		}
	}
	return found, nil
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

// written is a file init wrote or changed, by path from the root.
type written struct {
	path, line string
}

func writeRecipe(repo repository, flags initFlags, release pin, recipes fs.FS, pr prSettings, stdout, stderr io.Writer) int {
	name := initRecipes[flags.host][flags.agent]
	source, err := fs.ReadFile(recipes, path.Join("recipes", name))
	if err != nil {
		return initFailed(stderr, fmt.Errorf("read the shipped recipe: %w", err))
	}
	recipe, err := buildRecipe(name, source, release, flags)
	if err != nil {
		return initFailed(stderr, err)
	}
	out := filepath.FromSlash(initOut[flags.host])
	if flags.out != "" {
		if out, err = repo.place(flags.out); err != nil {
			return initFailed(stderr, fmt.Errorf("--out %w", err))
		}
	}
	shown := filepath.ToSlash(out)
	if flags.host == gitlabHost && shown == gitlabCI {
		return initFailed(stderr, fmt.Errorf("--out %s would replace the pipeline that includes the template: name another file", gitlabCI))
	}
	report := recipelint.LintBytes(shown, recipe)
	if len(report.Findings) > 0 {
		for _, finding := range report.Findings {
			fmt.Fprintln(stderr, finding)
		}
		fmt.Fprintf(stderr, "costroid: init wrote nothing: recipes/%s breaks a rule of lint-recipes, which is a defect in costroid\n", name)
		return exitRejected
	}
	if flags.platformURL != "" && !bytes.Contains(recipe, []byte("platform-url: ${{ vars."+platformVariable+" }}")) {
		return initFailed(stderr, fmt.Errorf("recipes/%s has no ingest step that reads %s, which is a defect in costroid", name, platformVariable))
	}
	var flow *prFlow
	if flags.pr {
		if flow, err = newPRFlow(repo, flags, pr, shown, recipe, stdout, stderr); err != nil {
			return initFailed(stderr, err)
		}
	}
	var include *written
	var pipeline []byte
	if flags.host == gitlabHost {
		if pipeline, include, err = planInclude(repo, shown); err != nil {
			return initFailed(stderr, err)
		}
	}
	switch err := createFile(repo.file(out), recipe, flags.force); {
	case errors.Is(err, fs.ErrExist):
		return initFailed(stderr, fmt.Errorf("%s exists: pass --force to replace it", shown))
	case err != nil:
		return initFailed(stderr, err)
	}
	fmt.Fprintf(stdout, "costroid: wrote %s from recipes/%s, %s\n", shown, name, release)
	changed := []string{shown}
	settings, err := commentedConfig()
	if err != nil {
		return initFailed(stderr, err)
	}
	switch err := createFile(repo.file(configFile), settings, false); {
	case err == nil:
		changed = append(changed, configFile)
		fmt.Fprintf(stdout, "costroid: wrote %s, with every key commented out\n", configFile)
	case errors.Is(err, fs.ErrExist):
		fmt.Fprintf(stdout, "costroid: %s exists, and init left it as it is\n", configFile)
	default:
		return initFailed(stderr, err)
	}
	if flags.skills {
		wrote, err := writeSkills(repo, stdout)
		changed = append(changed, wrote...)
		if err != nil {
			return initFailed(stderr, err)
		}
	}
	if include != nil {
		if pipeline != nil {
			if err := createFile(repo.file(gitlabCI), pipeline, true); err != nil {
				return initFailed(stderr, err)
			}
			changed = append(changed, gitlabCI)
		}
		fmt.Fprintln(stdout, include.line)
	}
	if flow == nil {
		printNextSteps(stdout, repo, flags, changed, recipe)
	} else {
		printSpend(stdout, flags, recipe)
		flow.files = changed
		if err := flow.run(); err != nil {
			return initFailed(stderr, err)
		}
		printAfterSteps(stdout, flags, recipe)
	}
	for _, note := range report.Notes {
		fmt.Fprintf(stdout, "\nnote: %s\n", note)
	}
	return 0
}

// buildRecipe is a shipped recipe as init writes it: pinned to release,
// without the header's sentence on where to copy it, and on a subscription's
// token when --auth asks for one.
func buildRecipe(name string, source []byte, release pin, flags initFlags) ([]byte, error) {
	recipe, err := pinRecipe(name, source, release, flags.agent)
	if err != nil {
		return nil, err
	}
	recipe = dropCopyInstruction(recipe)
	if flags.postReview {
		if recipe, err = addPostReview(name, recipe); err != nil {
			return nil, err
		}
	}
	if flags.auth != subscriptionAuth {
		return recipe, nil
	}
	if count := len(apiKeyInput.FindAllIndex(recipe, -1)); count != 1 {
		return nil, fmt.Errorf("recipes/%s passes anthropic_api_key on %d lines, want 1", name, count)
	}
	return apiKeyInput.ReplaceAll(recipe, []byte("${1}claude_code_oauth_token: ${{ secrets.CLAUDE_CODE_OAUTH_TOKEN }}")), nil
}

// addPostReview turns on the agent's answer in a GitHub recipe: post-review
// on the agent's step and on the report's step, after each one's stage.
func addPostReview(name string, recipe []byte) ([]byte, error) {
	for _, stage := range []*regexp.Regexp{agentStage, reportStage} {
		if count := len(stage.FindAllIndex(recipe, -1)); count != 1 {
			return nil, fmt.Errorf("recipes/%s has %d steps whose stage matches %s, want 1", name, count, stage)
		}
		recipe = stage.ReplaceAll(recipe, []byte("${0}${1}post-review: \"true\"\n"))
	}
	return recipe, nil
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

// recipeHeader splits a recipe into the comment lines it opens with, as one
// text, and the rest.
func recipeHeader(recipe []byte) (string, []byte) {
	var header []string
	rest := recipe
	for len(rest) > 0 && rest[0] == '#' {
		line, after, _ := bytes.Cut(rest, []byte("\n"))
		header = append(header, strings.TrimSpace(strings.TrimPrefix(string(line), "#")))
		rest = after
	}
	return strings.Join(header, " "), rest
}

// dropCopyInstruction takes the sentence on where to copy the recipe out of
// its header, and wraps the header again.
func dropCopyInstruction(recipe []byte) []byte {
	header, body := recipeHeader(recipe)
	at := copyInstruction.FindStringIndex(header)
	if at == nil {
		return recipe
	}
	after := header[at[1]:]
	if strings.HasSuffix(header[at[0]:at[1]], " then ") && after != "" {
		after = strings.ToUpper(after[:1]) + after[1:]
	}
	var text strings.Builder
	writeComment(&text, "# ", strings.TrimSpace(header[:at[0]]+after))
	return append([]byte(text.String()), body...)
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

// planInclude reads .gitlab-ci.yml and returns it as it reads with template
// included, nil when it includes the template already, and the line init
// prints for it. A file init cannot read, or whose include it does not write
// into, is refused before anything is written.
func planInclude(repo repository, template string) ([]byte, *written, error) {
	entry := "- local: " + template
	source, err := os.ReadFile(repo.file(gitlabCI))
	if errors.Is(err, fs.ErrNotExist) {
		return []byte("include:\n  " + entry + "\n"), &written{gitlabCI, fmt.Sprintf("costroid: wrote %s, which includes %s", gitlabCI, template)}, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", gitlabCI, err)
	}
	byHand := fmt.Sprintf("add %q to its include by hand", entry)
	top, err := pipelineRoot(source)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w, so init wrote nothing: %s", gitlabCI, err, byHand)
	}
	_, include := mappingPair(top, "include")
	if includes(include, template) {
		return nil, &written{gitlabCI, fmt.Sprintf("costroid: %s already includes %s", gitlabCI, template)}, nil
	}
	pipeline, err := addInclude(source, include, entry)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w, so init wrote nothing: %s", gitlabCI, err, byHand)
	}
	if top, err := pipelineRoot(pipeline); err != nil || !includes(field(top, "include"), template) {
		return nil, nil, fmt.Errorf("%s: adding the include would not read back, so init wrote nothing: %s", gitlabCI, byHand)
	}
	return pipeline, &written{gitlabCI, fmt.Sprintf("costroid: added %s to the include of %s", template, gitlabCI)}, nil
}

// pipelineRoot is the top-level mapping of a pipeline file of one YAML
// document. A file of comments alone has an empty one.
func pipelineRoot(source []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(source))
	var document yaml.Node
	switch err := decoder.Decode(&document); {
	case errors.Is(err, io.EOF):
		return &yaml.Node{Kind: yaml.MappingNode}, nil
	case err != nil:
		return nil, fmt.Errorf("init cannot read it as YAML: %w", err)
	}
	var next yaml.Node
	if err := decoder.Decode(&next); !errors.Is(err, io.EOF) {
		return nil, errors.New("it holds more than one YAML document")
	}
	if len(document.Content) == 0 {
		return &yaml.Node{Kind: yaml.MappingNode}, nil
	}
	top := document.Content[0]
	if top.Kind != yaml.MappingNode || top.Style&yaml.FlowStyle != 0 {
		return nil, errors.New("it is not a block of keys")
	}
	return top, nil
}

func field(node *yaml.Node, key string) *yaml.Node {
	_, value := mappingPair(node, key)
	return value
}

// includes says whether an include names template as a local file.
func includes(include *yaml.Node, template string) bool {
	if include == nil {
		return false
	}
	switch include.Kind {
	case yaml.ScalarNode:
		return strings.TrimPrefix(include.Value, "/") == template
	case yaml.MappingNode:
		local := field(include, "local")
		return local != nil && strings.TrimPrefix(local.Value, "/") == template
	case yaml.SequenceNode:
		return slices.ContainsFunc(include.Content, func(item *yaml.Node) bool { return includes(item, template) })
	}
	return false
}

// addInclude adds entry to a pipeline file: as a new include key at its end
// when it has none, or as the last item of its include list.
func addInclude(source []byte, include *yaml.Node, entry string) ([]byte, error) {
	if include == nil {
		text := string(source)
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		return []byte(text + "\ninclude:\n  " + entry + "\n"), nil
	}
	if include.Kind != yaml.SequenceNode || include.Style&yaml.FlowStyle != 0 || len(include.Content) == 0 {
		return nil, errors.New("its include is not a list init adds to")
	}
	lines, endsWithNewline := splitLines(source)
	last := include.Content[len(include.Content)-1]
	dash, err := dashIndent(lines, last)
	if err != nil {
		return nil, errors.New("its include is not a list init adds to")
	}
	end := blockEnd(lines, last.Line-1, dash) + 1
	lines = slices.Insert(lines, end, strings.Repeat(" ", dash)+entry)
	text := strings.Join(lines, "\n")
	if endsWithNewline || end == len(lines)-1 {
		text += "\n"
	}
	return []byte(text), nil
}

// perPushSentence is the recipe header's statement of what each push runs,
// with the GitLab template's budget variable read as its amount.
func perPushSentence(recipe []byte) string {
	header, _ := recipeHeader(recipe)
	_, sentence, found := strings.Cut(header, "Each push")
	if !found {
		return ""
	}
	sentence = "Each push" + sentence
	if budget := gitlabBudget.FindSubmatch(recipe); budget != nil {
		sentence = strings.ReplaceAll(sentence, "COSTROID_BUDGET_USD", "USD "+string(budget[1]))
	}
	return sentence
}

func printNextSteps(stdout io.Writer, repo repository, flags initFlags, changed []string, recipe []byte) {
	printSpend(stdout, flags, recipe)
	var commands []string
	if repo.dir != repo.root {
		if up, err := filepath.Rel(repo.dir, repo.root); err == nil {
			commands = append(commands, "cd "+filepath.ToSlash(up))
		}
	}
	if flags.host == gitlabHost {
		fmt.Fprint(stdout, "\n"+wrap(fmt.Sprintf("Set %s as a CI/CD variable scoped to the environment costroid-agent, and COSTROID_GITLAB_TOKEN scoped to costroid-report. docs/recipes.md#give-the-report-a-token says which token. Every merge request needs the label %s.", gitlabKeys[flags.agent], agentLabel), usageWidth))
	} else {
		for _, name := range secretNames(recipe) {
			commands = append(commands, "gh secret set "+name)
		}
		if flags.platformURL != "" {
			commands = append(commands, variableCommand(flags.platformURL))
		}
	}
	commands = append(commands, "git switch -c try-costroid", "git add "+strings.Join(changed, " "), `git commit -m "Add the costroid check"`)
	if flags.host == gitlabHost {
		commands = append(commands, "git push --set-upstream origin try-costroid")
	} else {
		commands = append(commands, "gh pr create --fill")
	}
	fmt.Fprint(stdout, "\nThese commands are left, and init ran none of them:\n")
	for _, command := range commands {
		fmt.Fprintf(stdout, "  %s\n", command)
	}
	printAfterSteps(stdout, flags, recipe)
}

// printSpend says what each push can spend, and on a subscription what the
// figures read.
func printSpend(stdout io.Writer, flags initFlags, recipe []byte) {
	if sentence := perPushSentence(recipe); sentence != "" {
		fmt.Fprint(stdout, "\n"+wrap(sentence, usageWidth))
	}
	if flags.auth == subscriptionAuth {
		fmt.Fprint(stdout, "\n"+wrap(subscriptionBasis, usageWidth))
	}
}

// printAfterSteps says what the platform needs beyond its variable, and when
// the agent's label is needed.
func printAfterSteps(stdout io.Writer, flags initFlags, recipe []byte) {
	if flags.platformURL != "" {
		fmt.Fprint(stdout, "\n"+wrap(platformNeeds, usageWidth))
	}
	if flags.host == githubHost && bytes.Contains(recipe, []byte(agentLabel)) {
		fmt.Fprintf(stdout, "\nBefore a first contributor's pull request:\n  gh label create %s\n", agentLabel)
		fmt.Fprint(stdout, wrap(fmt.Sprintf("The label %s lets a maintainer start the agent on a first contributor's pull request.", agentLabel), usageWidth))
	}
}

// secretNames lists the repository secrets a GitHub recipe needs, in order.
// The integration token is left out: a recipe sends to Costroid Platform only
// once COSTROID_PLATFORM_URL is set, and docs/connect.md keeps the token as an
// organization secret.
func secretNames(recipe []byte) []string {
	var names []string
	for _, match := range secretName.FindAllSubmatch(recipe, -1) {
		if name := string(match[1]); name != "GITHUB_TOKEN" && name != platformTokenVariable && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
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
// of the schema this release reads, each under its description and
// commented out.
func commentedConfig() ([]byte, error) {
	var schema schemaNode
	if err := json.Unmarshal(costroid.ConfigSchema, &schema); err != nil {
		return nil, fmt.Errorf("read the embedded schema of costroid.yml: %w", err)
	}
	var text strings.Builder
	fmt.Fprintf(&text, "# yaml-language-server: $schema=%s\n", schema.ID)
	writeComment(&text, "# ", schema.Description+" To set a key, remove the # before it and before each key it sits under. costroid validate checks the file.")
	for _, property := range schema.Properties {
		if slices.Contains(reservedKeys, property.name) {
			continue
		}
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

// edit replaces remove lines of a file, from the line at, with lines. With a
// remove of 0 it adds lines before the line at, or at the end when at is the
// line count.
type edit struct {
	at, remove int
	lines      []string
}

// agentStep is where a workflow runs an agent's own action.
type agentStep struct {
	jobKey, job, steps, step *yaml.Node
	action                   agentAction
}

// workflowPatch is what --existing adds to a workflow, and a sentence on the
// cap it states, empty when the agent's action holds none Costroid reads.
type workflowPatch struct {
	edits   []edit
	capNote string
}

// addInputs adds post-review after a block's stage line when postReview is
// set, and the cap's lines after the line that opens with capAfter, each in
// that line's indentation.
func addInputs(block []string, postReview bool, capAfter string, cap []string) []string {
	out := make([]string, 0, len(block)+len(cap)+1)
	for _, line := range block {
		out = append(out, line)
		trimmed := strings.TrimLeft(line, " ")
		indent := line[:len(line)-len(trimmed)]
		if postReview && strings.HasPrefix(trimmed, "stage: ") {
			out = append(out, indent+`post-review: "true"`)
		}
		if capAfter != "" && strings.HasPrefix(trimmed, capAfter) {
			out = append(out, indented(indent, cap)...)
		}
	}
	return out
}

// platformNeeds is what the ingest job needs besides the variable.
const platformNeeds = "The ingest job sends the rows once the integration token is set as COSTROID_PLATFORM_TOKEN and the owner is bound to the platform. docs/connect.md says how."

func printExistingPatch(repo repository, flags initFlags, release pin, stdout, stderr io.Writer) int {
	shown := filepath.ToSlash(flags.existing)
	source, err := os.ReadFile(repo.file(flags.existing))
	if err != nil {
		return initFailed(stderr, err)
	}
	if bytes.Contains(source, []byte(costroidActionPath+"@")) {
		fmt.Fprintf(stderr, "costroid: %s already calls %s, so init has nothing to add\n", shown, costroidActionPath)
		return 0
	}
	lines, endsWithNewline := splitLines(source)
	patch, err := existingEdits(source, lines, release, flags)
	if err != nil {
		return initFailed(stderr, fmt.Errorf("%s: %w", shown, err))
	}
	fmt.Fprint(stdout, unifiedDiff(shown, lines, endsWithNewline, patch.edits))
	fmt.Fprintf(stderr, "costroid: printed a patch for %s, and wrote nothing\n", shown)
	text := "Apply it from the repository's root with git apply. Your job keeps the permissions it has, and the read step asks for nothing more. The report job holds checks: write and pull-requests: write, runs on pull requests alone, and runs nothing the pull request wrote. docs/existing-workflow.md says more."
	if patch.capNote != "" {
		text += " " + patch.capNote
	}
	fmt.Fprint(stderr, wrap(text, usageWidth))
	if bytes.Contains(source, []byte("claude_code_oauth_token")) {
		fmt.Fprint(stderr, wrap(subscriptionBasis, usageWidth))
	}
	if flags.platformURL != "" {
		fmt.Fprint(stderr, "\n"+wrap("The ingest job holds id-token: write and reads the platform's address from a repository variable. Set it with:", usageWidth))
		fmt.Fprintf(stderr, "  %s\n", variableCommand(flags.platformURL))
		fmt.Fprint(stderr, wrap(platformNeeds, usageWidth))
	}
	return 0
}

// variableCommand sets the repository variable the ingest job reads.
func variableCommand(platformURL string) string {
	return "gh variable set " + platformVariable + " --body " + platformURL
}

// existingEdits finds the one step that runs an agent's action, of
// flags.agent's when it is set, and the end of the jobs block, and returns
// the read step and the report job to add there, in the indentation the file
// already uses, with the cap, the answer's opt-in and the ingest job the
// flags ask for.
func existingEdits(source []byte, lines []string, release pin, flags initFlags) (workflowPatch, error) {
	jobsKey, jobs, err := workflowJobs(source)
	if err != nil {
		return workflowPatch{}, err
	}
	found := agentSteps(jobs, flags.agent)
	if len(found) != 1 {
		return workflowPatch{}, fmt.Errorf("runs %s in %d steps, and init adds to exactly one: docs/existing-workflow.md shows the two blocks to add by hand", actionNames(flags.agent), len(found))
	}
	step := found[0]
	stepID, addID, err := agentStepID(step)
	if err != nil {
		return workflowPatch{}, err
	}
	dash, err := dashIndent(lines, step.step)
	if err != nil {
		return workflowPatch{}, fmt.Errorf("writes the %s step in a layout init does not write into", step.action.agent)
	}
	jobIndent := step.jobKey.Column - 1
	unit := step.job.Content[0].Column - 1 - jobIndent
	if step.job.Style&yaml.FlowStyle != 0 || unit <= 0 {
		return workflowPatch{}, errors.New("indents the job in a way init does not write into")
	}
	reportName, err := freeJobName(jobs, "report")
	if err != nil {
		return workflowPatch{}, err
	}
	var patch workflowPatch
	var readCap, reportCap []string
	if flags.budgetUSD != "" && step.action.uses != claudeCodeAction {
		return workflowPatch{}, fmt.Errorf("--budget-usd sets the cap init adds to the claude_args of %s, and the step runs %s, which takes none", claudeCodeAction, step.action.uses)
	}
	if step.action.uses == claudeCodeAction {
		held, err := claudeCap(lines, step, dash, unit, cmp.Or(flags.budgetUSD, defaultBudgetUSD))
		if err != nil {
			return workflowPatch{}, err
		}
		patch.edits, patch.capNote = held.edits, held.note
		if flags.budgetUSD != "" && len(held.edits) == 0 {
			patch.capNote += fmt.Sprintf(" --budget-usd %s is not used, because init adds a cap only to claude_args it can read that hold none.", flags.budgetUSD)
		}
		if readCap, reportCap, err = held.inputs(); err != nil {
			return workflowPatch{}, err
		}
	}
	var added []string
	if addID {
		added = append(added, strings.Repeat(" ", step.step.Column-1)+"id: "+stepID)
	}
	if step.action.uses == codexAction {
		read, err := codexReadStep(dash, unit, stepID, step.step, release)
		if err != nil {
			return workflowPatch{}, err
		}
		added = append(added, addInputs(read, flags.postReview, "", nil)...)
	} else {
		added = append(added, addInputs(readStep(dash, unit, stepID, release), flags.postReview, "execution-file:", readCap)...)
	}
	report := addInputs(reportJob(jobIndent, unit, reportName, step.jobKey.Value, release), flags.postReview, "stage: report", reportCap)
	jobsAdded := append([]string{""}, report...)
	if flags.platformURL != "" {
		ingestName, err := freeJobName(jobs, "ingest")
		if err != nil {
			return workflowPatch{}, err
		}
		jobsAdded = append(append(jobsAdded, ""), ingestJob(jobIndent, unit, ingestName, step.jobKey.Value, release)...)
	}
	patch.edits = append(patch.edits,
		edit{at: blockEnd(lines, step.step.Line-1, dash) + 1, lines: added},
		edit{at: blockEnd(lines, jobsKey.Line-1, jobsKey.Column-1) + 1, lines: jobsAdded},
	)
	slices.SortStableFunc(patch.edits, func(a, b edit) int { return a.at - b.at })
	return patch, nil
}

// workflowJobs is a workflow's jobs block, with its key.
func workflowJobs(source []byte) (*yaml.Node, *yaml.Node, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(source, &document); err != nil {
		return nil, nil, fmt.Errorf("not valid YAML: %w", err)
	}
	if len(document.Content) != 1 {
		return nil, nil, errors.New("holds no workflow")
	}
	jobsKey, jobs := mappingPair(document.Content[0], "jobs")
	if jobs == nil || jobs.Kind != yaml.MappingNode || jobs.Style&yaml.FlowStyle != 0 {
		return nil, nil, errors.New("has no jobs block to add to")
	}
	return jobsKey, jobs, nil
}

// defaultBudgetUSD is the cap --existing adds to claude_args that hold none
// when --budget-usd names no other, the recipes' own.
const defaultBudgetUSD = "5"

var (
	// capFlag finds --max-budget-usd in claude_args, and capValue reads its
	// figure: one expression, a quoted word or a bare one.
	capFlag  = regexp.MustCompile(`(?:^|\s)--max-budget-usd(?:[\s=]|$)`)
	capValue = regexp.MustCompile(`(?:^|\s)--max-budget-usd(?:=|\s+)(\$\{\{[^}]*\}\}|"[^"]*"|'[^']*'|\S+)`)
	// budgetAmount is a figure the read stage takes as budget-usd.
	budgetAmount = regexp.MustCompile(`^(?:[0-9]+(?:\.[0-9]+)?|\$\{\{[^}]*\}\})$`)
	// wholeExpression is an input that is one expression and nothing else.
	wholeExpression = regexp.MustCompile(`^\$\{\{[^}]*\}\}$`)
)

// heldCap is the cap a claude-code-action step holds once the patch applies:
// the edits that add --max-budget-usd to its claude_args, the figure, and the
// claude_args the read step is told. budget is empty when no cap is read.
// inputs gives them as YAML lines without their indentation.
type heldCap struct {
	edits        []edit
	budget, args string
	note         string
}

// inputs are the YAML lines of the cap for the read step and the report job.
func (c heldCap) inputs() ([]string, []string, error) {
	if c.budget == "" {
		return nil, nil, nil
	}
	budget, err := yamlEntry("budget-usd", c.budget)
	if err != nil {
		return nil, nil, err
	}
	args, err := yamlEntry("claude-args", c.args)
	if err != nil {
		return nil, nil, err
	}
	return append(slices.Clone(budget), args...), budget, nil
}

// yamlEntry is key: value as YAML lines, the value quoted or folded as YAML
// needs it.
func yamlEntry(key, value string) ([]string, error) {
	var data bytes.Buffer
	encoder := yaml.NewEncoder(&data)
	encoder.SetIndent(2)
	if err := encoder.Encode(map[string]string{key: value}); err != nil {
		return nil, fmt.Errorf("write %s: %w", key, err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("write %s: %w", key, err)
	}
	return strings.Split(strings.TrimSuffix(data.String(), "\n"), "\n"), nil
}

// indented puts indent before each line that is not empty.
func indented(indent string, lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		if line != "" {
			out[i] = indent + line
		}
	}
	return out
}

// claudeCap reads the cap a claude-code-action step's claude_args hold, and
// adds --max-budget-usd budget when they hold none. A layout init does not
// write into leaves the step as it is, with a note that says why.
func claudeCap(lines []string, step agentStep, dash, unit int, budget string) (heldCap, error) {
	withKey, with := mappingPair(step.step, "with")
	keys := strings.Repeat(" ", step.step.Column-1)
	added := "claude_args: --max-budget-usd " + budget
	switch {
	case withKey == nil:
		at := blockEnd(lines, step.step.Line-1, dash) + 1
		return addedCap(lines, step, budget, edit{at: at, lines: []string{keys + "with:", keys + strings.Repeat(" ", unit) + added}})
	case with.Kind == yaml.ScalarNode && with.Tag == "!!null" && with.Value == "":
		child := strings.Repeat(" ", withKey.Column-1+unit)
		return addedCap(lines, step, budget, edit{at: blockEnd(lines, withKey.Line-1, withKey.Column-1) + 1, lines: []string{child + added}})
	case with.Kind != yaml.MappingNode || with.Style&yaml.FlowStyle != 0:
		return unheldCap("its with is written in a layout init does not write into"), nil
	}
	argsKey, args := mappingPair(with, "claude_args")
	switch {
	case args == nil:
		at := blockEnd(lines, withKey.Line-1, withKey.Column-1) + 1
		return addedCap(lines, step, budget, edit{at: at, lines: []string{strings.Repeat(" ", with.Column-1) + added}})
	case args.Kind != yaml.ScalarNode:
		return unheldCap("its claude_args are not text"), nil
	case wholeExpression.MatchString(strings.TrimSpace(args.Value)):
		return unheldCap("its claude_args are one expression, which init cannot read"), nil
	case capFlag.MatchString(withoutComments(args.Value)):
		return statedCap(args.Value), nil
	}
	end := blockEnd(lines, argsKey.Line-1, argsKey.Column-1)
	if args.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		indent := contentIndent(lines[argsKey.Line : end+1])
		if indent == "" {
			return unheldCap("its claude_args are an empty block"), nil
		}
		return addedCap(lines, step, budget, edit{at: end + 1, lines: []string{indent + "--max-budget-usd " + budget}})
	}
	if end != argsKey.Line-1 || args.Line != argsKey.Line {
		return unheldCap("its claude_args run over more than one line"), nil
	}
	value, err := yamlScalar(strings.TrimSpace(args.Value + " --max-budget-usd " + budget))
	if err != nil {
		return heldCap{}, err
	}
	line := lines[args.Line-1][:args.Column-1] + value
	if args.LineComment != "" {
		line += " " + args.LineComment
	}
	return addedCap(lines, step, budget, edit{at: args.Line - 1, remove: 1, lines: []string{line}})
}

// addedCap is the cap once change adds --max-budget-usd budget to the step: it
// reads the patched workflow back and takes the claude_args it holds.
func addedCap(lines []string, step agentStep, budget string, change edit) (heldCap, error) {
	patched := []byte(strings.Join(applyEdits(lines, []edit{change}), "\n") + "\n")
	_, jobs, err := workflowJobs(patched)
	if err != nil {
		return heldCap{}, fmt.Errorf("adding --max-budget-usd would not read back: %w", err)
	}
	found := agentSteps(jobs, step.action.agent)
	if len(found) != 1 {
		return heldCap{}, errors.New("adding --max-budget-usd would not read back")
	}
	args := field(field(found[0].step, "with"), "claude_args")
	if args == nil || !capFlag.MatchString(withoutComments(args.Value)) {
		return heldCap{}, errors.New("adding --max-budget-usd would not read back")
	}
	return heldCap{
		edits:  []edit{change},
		budget: budget,
		args:   args.Value,
		note:   fmt.Sprintf("The patch adds --max-budget-usd %s to the claude_args of the claude-code-action step, so Claude Code stops a run once its own estimate passes USD %s, and gives the read step and the report job that cap. To change the cap, change it in claude_args, in claude-args and in both budget-usd inputs.", budget, budget),
	}, nil
}

// statedCap is the cap claude_args already hold: the last --max-budget-usd
// given, as claude-code-action reads them.
func statedCap(args string) heldCap {
	matches := capValue.FindAllStringSubmatch(withoutComments(args), -1)
	if len(matches) == 0 {
		return unheldCap("its claude_args hold --max-budget-usd with no figure")
	}
	figure := strings.Trim(matches[len(matches)-1][1], `"'`)
	if !budgetAmount.MatchString(figure) {
		return unheldCap(fmt.Sprintf("its claude_args hold --max-budget-usd %s, which init does not read as an amount", figure))
	}
	return heldCap{
		budget: figure,
		args:   args,
		note:   fmt.Sprintf("The read step and the report job are given the cap the step's claude_args hold, --max-budget-usd %s.", figure),
	}
}

func unheldCap(reason string) heldCap {
	return heldCap{note: "The claude-code-action step's claude_args are left as they are, because " + reason + ", so the patch states no cap. docs/existing-workflow.md#hold-each-run-to-a-cap shows the lines to add by hand."}
}

// withoutComments drops the lines of claude_args that start with #, as
// claude-code-action does.
func withoutComments(args string) string {
	var kept []string
	for line := range strings.SplitSeq(args, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// contentIndent is the indentation of the first line of a block scalar's
// content that is not empty.
func contentIndent(content []string) string {
	for _, line := range content {
		if trimmed := strings.TrimLeft(line, " "); trimmed != "" {
			return line[:len(line)-len(trimmed)]
		}
	}
	return ""
}

// yamlScalar is value as one YAML scalar, quoted where YAML needs it.
func yamlScalar(value string) (string, error) {
	data, err := yaml.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("write claude_args: %w", err)
	}
	return strings.TrimSuffix(string(data), "\n"), nil
}

// applyEdits is lines with edits applied. Edits are in the order of their
// lines.
func applyEdits(lines []string, edits []edit) []string {
	var out []string
	next := 0
	for _, change := range edits {
		out = append(out, lines[next:change.at]...)
		out = append(out, change.lines...)
		next = change.at + change.remove
	}
	return append(out, lines[next:]...)
}

// actionNames names the actions --existing looks for.
func actionNames(agent string) string {
	if action := actionFor(agent); action.uses != "" {
		return action.uses
	}
	names := make([]string, len(agentActions))
	for i, action := range agentActions {
		names[i] = action.uses
	}
	return strings.Join(names, " or ")
}

// agentSteps finds every step that runs an agent's action, or only agent's
// when it is set.
func agentSteps(jobs *yaml.Node, agent string) []agentStep {
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		return nil
	}
	var found []agentStep
	for i := 0; i+1 < len(jobs.Content); i += 2 {
		_, steps := mappingPair(jobs.Content[i+1], "steps")
		if steps == nil || steps.Kind != yaml.SequenceNode {
			continue
		}
		for _, step := range steps.Content {
			uses := field(step, "uses")
			if uses == nil {
				continue
			}
			for _, action := range agentActions {
				if (agent == "" || agent == action.agent) && strings.HasPrefix(strings.ToLower(uses.Value), action.uses+"@") {
					found = append(found, agentStep{jobKey: jobs.Content[i], job: jobs.Content[i+1], steps: steps, step: step, action: action})
				}
			}
		}
	}
	return found
}

// agentStepID is the id the read step names: the step's own, or one init
// adds when it has none.
func agentStepID(found agentStep) (string, bool, error) {
	if id := field(found.step, "id"); id != nil {
		return id.Value, false, nil
	}
	for _, other := range found.steps.Content {
		if id := field(other, "id"); id != nil && id.Value == found.action.stepID {
			return "", false, fmt.Errorf("another step of job %s has the id %s: give the %s step an id and run init again", found.jobKey.Value, found.action.stepID, found.action.agent)
		}
	}
	return found.action.stepID, true, nil
}

// dashIndent is the column, from 0, of the dash that opens a list item.
func dashIndent(lines []string, item *yaml.Node) (int, error) {
	layout := errors.New("an item in a layout init does not write into")
	if item.Style&yaml.FlowStyle != 0 || item.Line < 1 || item.Line > len(lines) || item.Column < 3 || len(lines[item.Line-1]) < item.Column-1 {
		return 0, layout
	}
	prefix := lines[item.Line-1][:item.Column-1]
	if strings.TrimSpace(prefix) != "-" {
		return 0, layout
	}
	return strings.Index(prefix, "-"), nil
}

// freeJobName is name, or costroid-name when the workflow has a job of that
// name.
func freeJobName(jobs *yaml.Node, name string) (string, error) {
	for _, candidate := range []string{name, "costroid-" + name} {
		if _, job := mappingPair(jobs, candidate); job == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("already has jobs named %s and costroid-%s", name, name)
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

// codexReadStep reads a codex-action run from the codex-home its step was
// given, as recipes/github-codex-action.yml does.
func codexReadStep(dash, unit int, stepID string, step *yaml.Node, release pin) ([]string, error) {
	home := field(field(step, "with"), "codex-home")
	if home == nil || home.Kind != yaml.ScalarNode || strings.TrimSpace(home.Value) == "" || strings.Contains(home.Value, "\n") {
		return nil, errors.New("its openai/codex-action step sets no codex-home, where Costroid reads the run: give the step a directory made for the job under runner.temp, as recipes/github-codex-action.yml does, and run init again")
	}
	value, err := yaml.Marshal(home.Value)
	if err != nil {
		return nil, fmt.Errorf("write its codex-home: %w", err)
	}
	key := strings.Repeat(" ", dash+2)
	child := key + strings.Repeat(" ", unit)
	return []string{
		strings.Repeat(" ", dash) + "- name: Read what the run cost",
		key + "if: always() && steps." + stepID + ".outcome != 'skipped'",
		key + "uses: " + release.uses(),
		key + "with:",
		child + "stage: read",
		child + "codex-home: " + strings.TrimSuffix(string(value), "\n"),
		child + "billing-account: ${{ github.repository_owner }}",
	}, nil
}

// reportJob is docs/existing-workflow.md's second block. It runs on
// pull_request alone: on any other event the report posts nothing.
func reportJob(indent, unit int, name, agentJob string, release pin) []string {
	at := func(level int) string { return strings.Repeat(" ", indent+level*unit) }
	step := at(2) + "  "
	return []string{
		at(0) + name + ":",
		at(1) + "needs: " + agentJob,
		at(1) + "if: " + pullRequestOnly,
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

// ingestJob sends the rows the read step kept to Costroid Platform, as the
// recipes' ingest job does, once the repository variable names it.
func ingestJob(indent, unit int, name, agentJob string, release pin) []string {
	at := func(level int) string { return strings.Repeat(" ", indent+level*unit) }
	with := at(2) + "  " + strings.Repeat(" ", unit)
	return []string{
		at(0) + name + ":",
		at(1) + "needs: " + agentJob,
		at(1) + "if: always() && needs." + agentJob + ".result != 'skipped' && vars." + platformVariable + " != ''",
		at(1) + "runs-on: ubuntu-24.04",
		at(1) + "timeout-minutes: 5",
		at(1) + "permissions:",
		at(2) + "id-token: write",
		at(1) + "steps:",
		at(2) + "- uses: " + release.uses(),
		at(2) + "  with:",
		with + "stage: ingest",
		with + "platform-url: ${{ vars." + platformVariable + " }}",
		with + "installation-id: github-owner:${{ github.repository_owner_id }}",
		with + "platform-token: ${{ secrets." + platformTokenVariable + " }}",
	}
}

// splitLines splits a file into its lines, and says whether the last one
// ends with a newline.
func splitLines(source []byte) ([]string, bool) {
	text := string(source)
	endsWithNewline := strings.HasSuffix(text, "\n")
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n"), endsWithNewline
}

const diffContext = 3

// unifiedDiff prints the edits as a unified diff with three lines of context.
// Edits are in the order of their lines.
func unifiedDiff(name string, lines []string, endsWithNewline bool, edits []edit) string {
	var out strings.Builder
	fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n", name, name)
	shift := 0
	for len(edits) > 0 {
		count, end := 1, edits[0].at+edits[0].remove
		for count < len(edits) && edits[count].at-diffContext <= end+diffContext {
			end = max(end, edits[count].at+edits[count].remove)
			count++
		}
		hunk := edits[:count]
		edits = edits[count:]
		start, stop := max(0, hunk[0].at-diffContext), min(len(lines), end+diffContext)
		added, removed := 0, 0
		for _, change := range hunk {
			added += len(change.lines)
			removed += change.remove
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", start+1, stop-start, start+1+shift, stop-start-removed+added)
		writeHunk(&out, lines, endsWithNewline, start, stop, hunk)
		shift += added - removed
	}
	return out.String()
}

// writeHunk prints lines start to stop with the hunk's edits in place. The
// last line of a file with no final newline gains one when text follows it,
// so the diff replaces it.
func writeHunk(out *strings.Builder, lines []string, endsWithNewline bool, start, stop int, hunk []edit) {
	last := len(lines) - 1
	noNewline := func(index int) string {
		if index == last && !endsWithNewline {
			return "\n\\ No newline at end of file"
		}
		return ""
	}
	appended := !endsWithNewline && hunk[len(hunk)-1].at == len(lines)
	next := 0
	for index := start; index < stop || next < len(hunk); {
		if next < len(hunk) && hunk[next].at == index {
			change := hunk[next]
			next++
			for removed := index; removed < index+change.remove; removed++ {
				out.WriteString("-" + lines[removed] + noNewline(removed) + "\n")
			}
			for _, line := range change.lines {
				out.WriteString("+" + line + "\n")
			}
			index += change.remove
			continue
		}
		if index == last && appended {
			out.WriteString("-" + lines[index] + noNewline(index) + "\n+" + lines[index] + "\n")
		} else {
			out.WriteString(" " + lines[index] + noNewline(index) + "\n")
		}
		index++
	}
}
