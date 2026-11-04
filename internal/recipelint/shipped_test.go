package recipelint

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const (
	reportEnvironment = "  environment:\n    name: costroid-report\n    action: access\n"
	reportForkGuard   = "    - if: $CI_MERGE_REQUEST_SOURCE_PROJECT_ID != $CI_MERGE_REQUEST_PROJECT_ID\n      when: never\n"

	recipesDir   = "../../recipes"
	actionDir    = "../../action"
	recipesDoc   = "../../docs/recipes.md"
	shippedFiles = 5
	testVersion  = "1.2.3"
)

// TestShippedRecipesPass is what puts every recipe under make check.
func TestShippedRecipesPass(t *testing.T) {
	report, err := Lint([]string{recipesDir, filepath.Join(actionDir, "action.yml")})
	if err != nil {
		t.Fatal(err)
	}
	if report.Files != shippedFiles {
		t.Fatalf("linted %d files, want %d", report.Files, shippedFiles)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("findings: %v", report.Findings)
	}
}

func recipeFiles(t *testing.T) []string {
	t.Helper()
	files, err := yamlFiles(recipesDir)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestEveryRuleBitesAShippedRecipe breaks the real files, so a rule that stopped
// reading their shape cannot pass on the fixtures alone.
func TestEveryRuleBitesAShippedRecipe(t *testing.T) {
	const pin = "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1"
	const lockdown = "    permissions:\n      contents: read\n"
	for _, name := range []string{"github-claude-code.yml", "github-claude-code-action.yml", "github-codex.yml"} {
		source := read(t, filepath.Join(recipesDir, name))
		for _, change := range []edit{
			{"target", "  pull_request:\n", "  pull_request_target:\n", RulePullRequestTarget},
			{"unpinned", pin, "actions/checkout@v7", RuleUnpinned},
			{"id-token", lockdown, lockdown + "      id-token: write\n", RuleAgentIDToken},
			{"more access", lockdown, lockdown + "      pull-requests: write\n", RuleAgentPermissions},
			{"forks", "github.event.pull_request.head.repo.full_name == github.repository\n      && ", "", RuleAgentGuard},
			{"version", "minimum-agent-version:", "floor:", RuleAgentVersion},
		} {
			t.Run(name+"/"+change.name, func(t *testing.T) {
				expectRule(t, source, change)
			})
		}
	}
	source := read(t, filepath.Join(recipesDir, "gitlab-ci.yml"))
	for _, change := range []edit{
		{"image", "node:24-slim@sha256:0e0ff40c39bc087845bfb27465a0df4ea419520094bc35842ff83dd8cbe6f9b6", "node:24-slim", RuleUnpinned},
		{"id_tokens", "  environment:\n", "  id_tokens:\n    TOKEN:\n      aud: x\n  environment:\n", RuleAgentIDToken},
		{"forks", "    - if: $CI_MERGE_REQUEST_SOURCE_PROJECT_ID != $CI_MERGE_REQUEST_PROJECT_ID\n      when: never\n", "", RuleAgentGuard},
		{"flags", `OTEL_LOG_USER_PROMPTS: "0"`, `OTEL_LOG_USER_PROMPTS: "1"`, RuleAgentContentFlags},
		{"codex flag", "otel.log_user_prompt=false", "otel.log_user_prompt=true", RuleAgentContentFlags},
		{"note token for every job", "  COSTROID_AGENT: claude\n", "  COSTROID_AGENT: claude\n  COSTROID_GITLAB_TOKEN: $STORED\n", RuleAgentNoteToken},
		{"note token on the agent", "    GIT_DEPTH: \"0\"\n", "    GIT_DEPTH: \"0\"\n    NOTE: $COSTROID_GITLAB_TOKEN\n", RuleAgentNoteToken},
		{"id_tokens on the report", reportEnvironment, reportEnvironment + "  id_tokens:\n    TOKEN:\n      aud: x\n", RuleReportJob},
		{"a checkout in the report", "    GIT_STRATEGY: empty\n", "    GIT_STRATEGY: fetch\n", RuleReportJob},
		{"no strategy in the report", "  variables:\n    GIT_STRATEGY: empty\n", "", RuleReportJob},
		{"forks reach the report", reportForkGuard + "    - if: $COSTROID_GITLAB_TOKEN\n", "    - if: $COSTROID_GITLAB_TOKEN\n", RuleReportJob},
		{"the agent's environment", "    name: costroid-report\n", "    name: costroid-agent\n", RuleReportJob},
		{"no environment", reportEnvironment, "", RuleReportJob},
		{"a provider key in the report", "    GIT_STRATEGY: empty\n", "    GIT_STRATEGY: empty\n    ANTHROPIC_API_KEY: $KEY\n", RuleReportJob},
		{"a cache in the report", reportEnvironment, reportEnvironment + "  cache:\n    paths:\n      - .cache\n", RuleReportJob},
		{"the rows without needs", "    GIT_STRATEGY: empty\n  needs:\n    - job: costroid:claude\n      optional: true\n    - job: costroid:codex\n      optional: true\n", "    GIT_STRATEGY: empty\n", RuleReportJob},
		{"the agent in the report", "    - touch costroid-rows.jsonl\n", "    - touch costroid-rows.jsonl\n    - claude -p hello\n", RuleReportJob},
	} {
		t.Run("gitlab/"+change.name, func(t *testing.T) {
			expectRule(t, source, change)
		})
	}
}

func TestDocsRecordEveryPinnedSHAWithItsRelease(t *testing.T) {
	doc := regexp.MustCompile(` +`).ReplaceAllString(read(t, recipesDoc), " ")
	pins := regexp.MustCompile(`([A-Za-z0-9._-]+/[A-Za-z0-9._/-]+)@([0-9a-f]{40}) # (\S+)`)
	sources := []string{read(t, filepath.Join(actionDir, "action.yml"))}
	for _, file := range recipeFiles(t) {
		sources = append(sources, read(t, file))
	}
	found := 0
	for _, source := range sources {
		for _, match := range pins.FindAllStringSubmatch(source, -1) {
			if match[1] == costroidAction {
				continue
			}
			found++
			row := "| `" + match[1] + "` | " + match[3] + " | `" + match[2] + "` |"
			if !strings.Contains(doc, row) {
				t.Errorf("docs/recipes.md has no row %s", row)
			}
		}
	}
	if found == 0 {
		t.Fatal("found no pins")
	}
}

func TestDocsLeadWithClaudeCode(t *testing.T) {
	doc := read(t, recipesDoc)
	claude, codex := strings.Index(doc, "## Claude Code"), strings.Index(doc, "## Codex")
	if claude < 0 || codex < claude {
		t.Fatalf("Claude Code at %d, Codex at %d", claude, codex)
	}
	if !strings.Contains(doc, "An injected agent can still spend its budget and edit the PR.") {
		t.Fatal("the residual risk is not stated")
	}
}

func TestGitLabInstallsFromTheSameReleasePathAsTheAction(t *testing.T) {
	path := regexp.MustCompile(`https://github\.com/Costroid/costroid/releases/download/v\$[{]?[A-Za-z_]+`)
	asset := "costroid-linux-"
	for _, file := range []string{filepath.Join(actionDir, "install.sh"), filepath.Join(recipesDir, "gitlab-ci.yml")} {
		source := read(t, file)
		if !path.MatchString(source) || !strings.Contains(source, asset) {
			t.Fatalf("%s does not name the release path", file)
		}
	}
}

func TestActionScriptsTakeInputsThroughTheEnvironment(t *testing.T) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(read(t, filepath.Join(actionDir, "action.yml"))), &root); err != nil {
		t.Fatal(err)
	}
	steps := sequence(field(field(root.Content[0], "runs"), "steps"))
	if len(steps) == 0 {
		t.Fatal("no steps")
	}
	for _, step := range steps {
		if strings.Contains(scalarField(step, "run"), "${{") {
			t.Errorf("step %q reads an expression in its script", scalarField(step, "name"))
		}
		if name := scalarField(step, "name"); strings.Contains(text(field(step, "env")), "api-key") && name != "Run the agent" {
			t.Errorf("step %q holds the provider key", name)
		}
	}
}

// shell is a scratch runner: a bin directory that shadows real commands.
type shell struct {
	t    *testing.T
	root string
	bin  string
}

func newShell(t *testing.T) *shell {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	return &shell{t: t, root: root, bin: bin}
}

func (s *shell) stub(name, body string) {
	s.t.Helper()
	if err := os.WriteFile(filepath.Join(s.bin, name), []byte("#!/usr/bin/env bash\n"+body+"\n"), 0o755); err != nil {
		s.t.Fatal(err)
	}
}

func (s *shell) run(script string, env ...string) (int, string) {
	s.t.Helper()
	command := exec.Command("bash", filepath.Join(actionDir, script))
	command.Env = append([]string{"PATH=" + s.bin + ":" + os.Getenv("PATH"), "HOME=" + s.root}, env...)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	if err != nil && command.ProcessState == nil {
		s.t.Fatal(err)
	}
	return command.ProcessState.ExitCode(), output.String()
}

func (s *shell) install(sum string) (int, string, string) {
	s.t.Helper()
	path := filepath.Join(s.root, "github-path")
	code, output := s.run("install.sh",
		"COSTROID_VERSION="+testVersion, "COSTROID_SHA256="+sum,
		"RUNNER_TEMP="+s.root, "GITHUB_PATH="+path)
	return code, output, path
}

// stubCurl writes the release's bytes wherever curl was told to, and records the
// address it was given.
func (s *shell) stubCurl(body string) {
	s.t.Helper()
	s.stub("curl", `while [ $# -gt 0 ]; do
  case $1 in --output) out=$2; shift ;; https://*) echo "$1" > "`+s.root+`/url" ;; esac
  shift
done
printf '%s' '`+body+`' > "$out"`)
}

func TestInstallAcceptsTheBinaryItPinned(t *testing.T) {
	const binary = "the released bytes"
	s := newShell(t)
	s.stubCurl(binary)
	sum := sha256.Sum256([]byte(binary))
	code, output, githubPath := s.install(hex.EncodeToString(sum[:]))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, output)
	}
	installed := filepath.Join(s.root, "costroid-bin", "costroid")
	if got := read(t, installed); got != binary {
		t.Fatalf("installed %q", got)
	}
	if got := strings.TrimSpace(read(t, githubPath)); got != filepath.Dir(installed) {
		t.Fatalf("GITHUB_PATH holds %q", got)
	}
	if got := read(t, filepath.Join(s.root, "url")); !strings.Contains(got, "/releases/download/v"+testVersion+"/costroid-linux-") {
		t.Fatalf("fetched %q", got)
	}
}

func TestInstallRefusesABinaryThatDoesNotMatch(t *testing.T) {
	s := newShell(t)
	s.stubCurl("a swapped binary")
	sum := sha256.Sum256([]byte("the released bytes"))
	code, output, githubPath := s.install(hex.EncodeToString(sum[:]))
	if code == 0 {
		t.Fatalf("a mismatch passed: %s", output)
	}
	if !strings.Contains(output, "does not match the pinned SHA-256") {
		t.Fatalf("output: %s", output)
	}
	if _, err := os.Stat(filepath.Join(s.root, "costroid-bin", "costroid")); !os.IsNotExist(err) {
		t.Fatalf("the binary was installed: %v", err)
	}
	if _, err := os.Stat(githubPath); !os.IsNotExist(err) {
		t.Fatalf("the path was set: %v", err)
	}
}

func TestInstallRefusesAnEmptyPin(t *testing.T) {
	s := newShell(t)
	s.stubCurl("anything")
	for name, sum := range map[string]string{
		"empty":  "",
		"short":  "abc123",
		"upper":  strings.Repeat("A", 64),
		"option": "--help" + strings.Repeat("0", 58),
	} {
		t.Run(name, func(t *testing.T) {
			if code, output, _ := s.install(sum); code == 0 {
				t.Fatalf("passed: %s", output)
			}
			if _, err := os.Stat(filepath.Join(s.root, "url")); !os.IsNotExist(err) {
				t.Fatal("it fetched before checking the pin")
			}
		})
	}
}

// agentEnv is the environment of a passing run-agent.sh.
func agentEnv(root string, overrides ...string) []string {
	return append([]string{
		"COSTROID_AGENT=claude",
		"COSTROID_PROMPT=Read the diff.",
		"COSTROID_AGENT_ARGS=--bare\n--restricted\n",
		"COSTROID_MINIMUM_AGENT_VERSION=2.1.316",
		"COSTROID_BUDGET_USD=5",
		"COSTROID_AGENT_KEY=test-key",
		"COSTROID_ROWS=" + filepath.Join(root, "rows.jsonl"),
	}, overrides...)
}

func (s *shell) stubAgents() {
	s.t.Helper()
	s.stub("claude", `echo "2.1.316 (Claude Code)"`)
	s.stub("codex", `echo "codex-cli 0.164.0-alpha.11.1"`)
	s.stub("costroid", `printf '%s\n' "$@" > "`+s.root+`/argv"
env > "`+s.root+`/env"
[ -t 0 ] && echo tty
exit ${STUB_EXIT:-0}`)
}

// The argument list a YAML block scalar yields ends in a newline, and that must
// not reach the agent as an empty argument.
func TestRunAgentPassesTheAgentUnderTheBudget(t *testing.T) {
	s := newShell(t)
	s.stubAgents()
	code, output := s.run("run-agent.sh", agentEnv(s.root)...)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, output)
	}
	want := strings.Join([]string{
		"run", "--budget-usd", "5", "--out", filepath.Join(s.root, "rows.jsonl"), "--",
		"claude", "-p", "--bare", "--restricted", "Read the diff.", "",
	}, "\n")
	if got := read(t, filepath.Join(s.root, "argv")); got != want {
		t.Fatalf("argv:\n%s\nwant:\n%s", got, want)
	}
	env := read(t, filepath.Join(s.root, "env"))
	if !strings.Contains(env, "ANTHROPIC_API_KEY=test-key\n") {
		t.Fatal("the key did not reach the agent")
	}
	if strings.Contains(env, "COSTROID_AGENT_KEY") {
		t.Fatal("the key stayed in its input variable")
	}
}

func TestRunAgentGivesCodexItsOwnKeyAndCommand(t *testing.T) {
	s := newShell(t)
	s.stubAgents()
	code, output := s.run("run-agent.sh", agentEnv(s.root,
		"COSTROID_AGENT=codex", "COSTROID_MINIMUM_AGENT_VERSION=0.164.0-alpha.11.1", "COSTROID_AGENT_ARGS=")...)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, output)
	}
	if got := read(t, filepath.Join(s.root, "argv")); !strings.Contains(got, "--\ncodex\nexec\nRead the diff.\n") {
		t.Fatalf("argv:\n%s", got)
	}
	if env := read(t, filepath.Join(s.root, "env")); !strings.Contains(env, "CODEX_API_KEY=test-key\n") {
		t.Fatal("the key did not reach the agent")
	}
}

func TestRunAgentKeepsTheBudgetStopExitCode(t *testing.T) {
	s := newShell(t)
	s.stubAgents()
	if code, output := s.run("run-agent.sh", agentEnv(s.root, "STUB_EXIT=124")...); code != 124 {
		t.Fatalf("exit %d: %s", code, output)
	}
}

func TestRunAgentRefusesWhatItCannotRunSafely(t *testing.T) {
	for name, override := range map[string]string{
		"an unknown agent":  "COSTROID_AGENT=opencode",
		"no prompt":         "COSTROID_PROMPT=",
		"no budget":         "COSTROID_BUDGET_USD=",
		"no key":            "COSTROID_AGENT_KEY=",
		"no floor":          "COSTROID_MINIMUM_AGENT_VERSION=",
		"an older agent":    "COSTROID_MINIMUM_AGENT_VERSION=2.1.317",
		"a far older agent": "COSTROID_MINIMUM_AGENT_VERSION=10.0.0",
	} {
		t.Run(name, func(t *testing.T) {
			s := newShell(t)
			s.stubAgents()
			code, output := s.run("run-agent.sh", agentEnv(s.root, override)...)
			if code != 125 {
				t.Fatalf("exit %d: %s", code, output)
			}
			if _, err := os.Stat(filepath.Join(s.root, "argv")); !os.IsNotExist(err) {
				t.Fatal("the agent ran")
			}
		})
	}
}

func TestRunAgentAcceptsANewerAgentThanTheFloor(t *testing.T) {
	s := newShell(t)
	s.stubAgents()
	if code, output := s.run("run-agent.sh", agentEnv(s.root, "COSTROID_MINIMUM_AGENT_VERSION=2.1.99")...); code != 0 {
		t.Fatalf("exit %d: %s", code, output)
	}
}

// gitlabScript runs a template job's script the way a runner does, stopping at
// the first failing line.
func (s *shell) gitlabScript(job string, env ...string) (int, string) {
	s.t.Helper()
	documents, err := parse([]byte(read(s.t, filepath.Join(recipesDir, "gitlab-ci.yml"))))
	if err != nil {
		s.t.Fatal(err)
	}
	var lines []string
	for _, line := range sequence(field(field(documents[0], job), "script")) {
		lines = append(lines, line.Value)
	}
	command := exec.Command("bash", "-eo", "pipefail", "-c", strings.Join(lines, "\n"))
	command.Dir = s.root
	command.Env = append([]string{"PATH=" + s.bin + ":" + os.Getenv("PATH"), "HOME=" + s.root}, env...)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	err = command.Run()
	if err != nil && command.ProcessState == nil {
		s.t.Fatal(err)
	}
	return command.ProcessState.ExitCode(), output.String()
}

func TestGitLabJobsRunTheAgentUnderTheBudget(t *testing.T) {
	for job, floor := range map[string]string{"costroid:claude": "2.1.316", "costroid:codex": "0.164.0-alpha.11.1"} {
		t.Run(job, func(t *testing.T) {
			s := newShell(t)
			s.stubAgents()
			s.stub("npm", "exit 0")
			s.stub("git", "exit 0")
			env := []string{"MINIMUM_AGENT_VERSION=" + floor, "COSTROID_BUDGET_USD=5", "COSTROID_PROMPT=Read the diff."}
			if code, output := s.gitlabScript(job, env...); code != 0 {
				t.Fatalf("exit %d: %s", code, output)
			}
			argv := read(t, filepath.Join(s.root, "argv"))
			if !strings.Contains(argv, "--budget-usd\n5\n--out\ncostroid-rows.jsonl\n--\n") || !strings.HasSuffix(argv, "\nRead the diff.\n") {
				t.Fatalf("argv:\n%s", argv)
			}
			if err := os.Remove(filepath.Join(s.root, "argv")); err != nil {
				t.Fatal(err)
			}
			env[0] = "MINIMUM_AGENT_VERSION=99.0.0"
			if code, output := s.gitlabScript(job, env...); code == 0 {
				t.Fatalf("an older agent ran: %s", output)
			}
			if _, err := os.Stat(filepath.Join(s.root, "argv")); !os.IsNotExist(err) {
				t.Fatal("the agent ran below its floor")
			}
		})
	}
}

func TestGitLabReportJobPostsTheRowsItWasHanded(t *testing.T) {
	s := newShell(t)
	s.stubAgents()
	code, output := s.gitlabScript("costroid:report", "COSTROID_BUDGET_USD=5")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, output)
	}
	want := "report\n--rows\ncostroid-rows.jsonl\n--budget-usd\n5\n"
	if got := read(t, filepath.Join(s.root, "argv")); got != want {
		t.Fatalf("argv:\n%s\nwant:\n%s", got, want)
	}
	if _, err := os.Stat(filepath.Join(s.root, "costroid-rows.jsonl")); err != nil {
		t.Fatalf("a run with no rows must still report: %v", err)
	}
}

func gitLabJob(t *testing.T, job string) *yaml.Node {
	t.Helper()
	documents, err := parse([]byte(read(t, filepath.Join(recipesDir, "gitlab-ci.yml"))))
	if err != nil {
		t.Fatal(err)
	}
	node := field(documents[0], job)
	if node == nil {
		t.Fatalf("no job %s", job)
	}
	return node
}

// The report keeps ingest's trigger and order, and adds the fork guard. Its last
// rule names its own variable, because a comment needs no ledger.
func TestGitLabReportRunsWhenIngestRuns(t *testing.T) {
	ingest, report := gitLabJob(t, "costroid:ingest"), gitLabJob(t, "costroid:report")
	ingestRules, reportRules := sequence(field(ingest, "rules")), sequence(field(report, "rules"))
	if len(ingestRules) != 2 || len(reportRules) != 3 {
		t.Fatalf("%d and %d rules", len(ingestRules), len(reportRules))
	}
	if ingestFirst, reportFirst := scalarField(ingestRules[0], "if"), scalarField(reportRules[0], "if"); ingestFirst != reportFirst {
		t.Errorf("the first rule differs: %q, %q", ingestFirst, reportFirst)
	}
	if got := scalarField(reportRules[1], "if"); !strings.Contains(got, gitLabForkGuard) || scalarField(reportRules[1], "when") != "never" {
		t.Errorf("the fork rule reads %q", got)
	}
	if got := scalarField(reportRules[2], "when"); got != scalarField(ingestRules[1], "when") {
		t.Errorf("the last rule's when reads %q", got)
	}
	if text(field(report, "needs")) != text(field(ingest, "needs")) || scalarField(report, "stage") != scalarField(ingest, "stage") {
		t.Error("the report does not follow the agent as ingest does")
	}
}

func TestGitLabDocsStateTheAdapterIsUntestedAndCiteTheToken(t *testing.T) {
	doc := read(t, recipesDoc)
	for _, want := range []string{
		"has run against a stub",
		"never a real GitLab",
		"https://docs.gitlab.com/ci/jobs/ci_job_token/",
		"https://docs.gitlab.com/user/permissions/",
		gitLabNoteToken,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/recipes.md does not state %q", want)
		}
	}
}

func (s *shell) stubOIDC() []string {
	s.t.Helper()
	s.stub("curl", `cat > "`+s.root+`/headers-$(ls "`+s.root+`" | wc -l)"
for arg; do [ "$arg" = "--get" ] && { echo '{"value":"a-token"}'; exit 0; }; done
printf '%s\n' "$@" > "`+s.root+`/post"
exit ${STUB_POST_EXIT:-0}`)
	rows := filepath.Join(s.root, "rows.jsonl")
	if err := os.WriteFile(rows, []byte("{}\n"), 0o644); err != nil {
		s.t.Fatal(err)
	}
	return []string{
		"COSTROID_ROWS=" + rows,
		"COSTROID_INGEST_URL=https://ledger.example/v1/rows",
		"COSTROID_INGEST_AUDIENCE=costroid",
		"ACTIONS_ID_TOKEN_REQUEST_URL=https://oidc.example/token?v=1",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN=request-token",
	}
}

func TestIngestPostsTheRowsUnderTheJobsToken(t *testing.T) {
	s := newShell(t)
	env := s.stubOIDC()
	code, output := s.run("ingest.sh", env...)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, output)
	}
	post := read(t, filepath.Join(s.root, "post"))
	if !strings.Contains(post, "https://ledger.example/v1/rows") || strings.Contains(post, "a-token") {
		t.Fatalf("argv:\n%s", post)
	}
	if strings.Contains(output, "request-token") {
		t.Fatalf("output leaks the request token: %s", output)
	}
}

func TestIngestFailureWarnsAndNeverFailsTheJob(t *testing.T) {
	s := newShell(t)
	env := s.stubOIDC()
	code, output := s.run("ingest.sh", append(env, "STUB_POST_EXIT=22")...)
	if code != 0 || !strings.Contains(output, "::warning::costroid: the ledger refused the rows") {
		t.Fatalf("exit %d: %s", code, output)
	}
}

func TestIngestWithNoRowsSendsNothing(t *testing.T) {
	s := newShell(t)
	env := s.stubOIDC()
	if err := os.WriteFile(filepath.Join(s.root, "rows.jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	code, output := s.run("ingest.sh", env...)
	if code != 0 || !strings.Contains(output, "the run left no rows") {
		t.Fatalf("exit %d: %s", code, output)
	}
	if _, err := os.Stat(filepath.Join(s.root, "post")); !os.IsNotExist(err) {
		t.Fatal("it posted")
	}
}

// The report job reads costroid.yml at the base commit. A private repository
// answers 403 without contents: read, and comment.show_cost then goes unread.
func TestTheGitHubReportJobsCanReadTheSettings(t *testing.T) {
	for _, name := range []string{"github-claude-code.yml", "github-codex.yml"} {
		var recipe struct {
			Jobs map[string]struct {
				Permissions map[string]string `yaml:"permissions"`
			} `yaml:"jobs"`
		}
		raw, err := os.ReadFile(filepath.Join("..", "..", "recipes", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := yaml.Unmarshal(raw, &recipe); err != nil {
			t.Fatal(err)
		}
		report, found := recipe.Jobs["report"]
		if !found {
			t.Fatalf("%s has no report job", name)
		}
		if report.Permissions["contents"] != "read" {
			t.Errorf("%s: the report job's contents permission is %q, want read", name, report.Permissions["contents"])
		}
	}
}
