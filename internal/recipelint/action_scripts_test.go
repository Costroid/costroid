package recipelint

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// stageCondition finds a stage in a step's if, and stageLine a stage in the
// list the install prints for an empty stage.
var (
	stageCondition = regexp.MustCompile(`inputs\.stage == '([a-z]+)'`)
	stageLine      = regexp.MustCompile(`(?m)^  ([a-z]+) +\S.*$`)
)

// actionStages lists the action's stages: each one a step's if names, and
// install, which only installs.
func actionStages(t *testing.T) []string {
	t.Helper()
	stages := []string{"install"}
	for _, match := range stageCondition.FindAllStringSubmatch(read(t, filepath.Join(actionDir, "action.yml")), -1) {
		if !slices.Contains(stages, match[1]) {
			stages = append(stages, match[1])
		}
	}
	slices.Sort(stages)
	return stages
}

func TestStageHasNoDefault(t *testing.T) {
	for _, file := range []string{filepath.Join(actionDir, "action.yml"), filepath.Join(marketplaceDir, "action.yml")} {
		stage := field(field(document(t, read(t, file)), "inputs"), "stage")
		if stage == nil {
			t.Fatalf("%s has no stage input", file)
		}
		if field(stage, "default") != nil {
			t.Errorf("%s gives stage a default", file)
		}
		if scalarField(stage, "required") != "true" {
			t.Errorf("%s does not mark stage required", file)
		}
	}
	stages := actionStages(t)
	if len(stages) != 6 {
		t.Fatalf("the action names the stages %v", stages)
	}
	for name, value := range map[string]string{"empty": "", "unknown": "Report"} {
		t.Run(name, func(t *testing.T) {
			r := newRelease(t)
			code, output := r.install("COSTROID_STAGE="+value, "COSTROID_VERSION="+testVersion, "COSTROID_SHA256="+sumOf(releaseBinary))
			if code != 1 || r.installed() || r.fetched() != "" {
				t.Fatalf("exit %d, installed %v, fetched %q: %s", code, r.installed(), r.fetched(), output)
			}
			lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
			if len(lines) != 1+len(stages) || !strings.HasPrefix(lines[0], "costroid: stage ") {
				t.Fatalf("want the refusal and one line per stage:\n%s", output)
			}
			var listed []string
			for _, line := range lines[1:] {
				match := stageLine.FindStringSubmatch(line)
				if match == nil {
					t.Fatalf("the line %q names no stage", line)
				}
				listed = append(listed, match[1])
			}
			slices.Sort(listed)
			if !slices.Equal(listed, stages) {
				t.Fatalf("listed %v, the action has %v", listed, stages)
			}
		})
	}
}

func TestEveryStageInstalls(t *testing.T) {
	for _, stage := range actionStages(t) {
		t.Run(stage, func(t *testing.T) {
			r := newRelease(t)
			code, output := r.install("COSTROID_STAGE="+stage, "COSTROID_VERSION="+testVersion, "COSTROID_SHA256="+sumOf(releaseBinary))
			if code != 0 || !r.installed() {
				t.Fatalf("exit %d: %s", code, output)
			}
		})
	}
}

// rowsIn fills the directory the report and ingest stages fetch the artifact
// into, as download-artifact would unpack it.
func rowsIn(t *testing.T, root string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(root, "costroid-rows-in")
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, path, body)
	}
	return dir
}

func pinnedInstall(r *release, stage string) (int, string) {
	r.t.Helper()
	return r.install("COSTROID_STAGE="+stage, "COSTROID_VERSION="+testVersion, "COSTROID_SHA256="+sumOf(releaseBinary))
}

// The artifact the agent's job uploads could hold a binary or an event file
// of its own. In the ingest stage it is refused before costroid is installed,
// so no step after it runs. In the report stage it is deleted unread, the
// release's binary is installed, and the step's outputs tell the report.
func TestInstallRefusesAnArtifactThatHoldsMoreThanTheRows(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"a binary and an event file": {
			"costroid-rows.jsonl":         "{}\n",
			"costroid-bin/costroid":       "#!/bin/sh\necho pwned\n",
			"_github_workflow/event.json": "{}\n",
		},
		"a tool":                 {"costroid-bin/curl": "#!/bin/sh\necho pwned\n"},
		"a third file":           {"costroid-rows.jsonl": "{}\n", "notes.txt": "x"},
		"a hidden file":          {".costroid-rows.jsonl": "{}\n"},
		"the rows as a folder":   {"costroid-rows.jsonl/x": "{}\n"},
		"a name with a new line": {"x\n::error::y": ""},
	} {
		t.Run("ingest/"+name, func(t *testing.T) {
			r := newRelease(t)
			rowsIn(t, r.root, files)
			code, output := pinnedInstall(r, "ingest")
			if code != 1 || !strings.Contains(output, "costroid: the costroid-rows artifact holds ") {
				t.Fatalf("exit %d: %s", code, output)
			}
			if r.installed() || r.fetched() != "" {
				t.Fatal("it installed after refusing the artifact")
			}
			if strings.Contains(output, "\n::") || strings.Count(output, "\n") != 1 {
				t.Fatalf("an entry's name broke the line:\n%s", output)
			}
		})
		t.Run("report/"+name, func(t *testing.T) {
			r := newRelease(t)
			dir := rowsIn(t, r.root, files)
			code, output, outputs := reportInstall(r)
			if code != 0 || !strings.Contains(output, "costroid: the costroid-rows artifact holds ") {
				t.Fatalf("exit %d: %s", code, output)
			}
			if got := read(t, filepath.Join(r.root, "costroid-bin", "costroid")); got != releaseBinary {
				t.Fatalf("installed %q, not the release's binary", got)
			}
			if left, err := os.ReadDir(dir); err != nil || len(left) != 0 {
				t.Fatalf("the refused artifact is still there: %v, %v", left, err)
			}
			if strings.Contains(output, "\n::") || strings.Count(output, "\n") != 1 {
				t.Fatalf("an entry's name broke the line:\n%s", output)
			}
			lines := strings.Split(strings.TrimSuffix(outputs, "\n"), "\n")
			if len(lines) != 2 || !strings.HasPrefix(lines[0], "refused=") || !regexp.MustCompile(`^refused_count=[1-9][0-9]*$`).MatchString(lines[1]) {
				t.Fatalf("the step outputs read:\n%s", outputs)
			}
			if names := strings.TrimPrefix(lines[0], "refused="); names == "" || strings.ContainsAny(names, "\r:`") {
				t.Fatalf("the names output reads %q", names)
			}
		})
	}
}

// reportInstall runs the install in stage report with a step output file, and
// returns that file's content.
func reportInstall(r *release) (int, string, string) {
	r.t.Helper()
	path := filepath.Join(r.root, "github-output")
	code, output := r.install("COSTROID_STAGE=report", "COSTROID_VERSION="+testVersion, "COSTROID_SHA256="+sumOf(releaseBinary), "GITHUB_OUTPUT="+path)
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		r.t.Fatal(err)
	}
	return code, output, string(data)
}

func TestInstallRefusesARowsFileThatIsALink(t *testing.T) {
	target := filepath.Join(t.TempDir(), "secret")
	write(t, target, "kept")
	for _, stage := range []string{"report", "ingest"} {
		t.Run(stage, func(t *testing.T) {
			r := newRelease(t)
			dir := rowsIn(t, r.root, map[string]string{"costroid-rows.run.json": "{}"})
			if err := os.Symlink(target, filepath.Join(dir, "costroid-rows.jsonl")); err != nil {
				t.Fatal(err)
			}
			if stage == "ingest" {
				if code, output := pinnedInstall(r, stage); code != 1 || r.installed() {
					t.Fatalf("exit %d: %s", code, output)
				}
				return
			}
			code, output, outputs := reportInstall(r)
			if code != 0 || !strings.Contains(outputs, "refused=costroid-rows.jsonl\nrefused_count=1\n") {
				t.Fatalf("exit %d, outputs %q: %s", code, outputs, output)
			}
			if read(t, target) != "kept" {
				t.Fatal("the link's target changed")
			}
		})
	}
}

func TestAReportInstallWithNoOutputFileFails(t *testing.T) {
	r := newRelease(t)
	rowsIn(t, r.root, map[string]string{"notes.txt": "x"})
	if code, output := pinnedInstall(r, "report"); code != 1 || r.installed() || !strings.Contains(output, "GITHUB_OUTPUT is not set") {
		t.Fatalf("exit %d: %s", code, output)
	}
}

func TestInstallTakesTheRowsAndTheRunObjectOrNoArtifact(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"both files":  {"costroid-rows.jsonl": "{}\n", "costroid-rows.run.json": "{}"},
		"rows alone":  {"costroid-rows.jsonl": "{}\n"},
		"no artifact": nil,
	} {
		t.Run(name, func(t *testing.T) {
			r := newRelease(t)
			if files != nil {
				rowsIn(t, r.root, files)
			}
			if code, output := pinnedInstall(r, "report"); code != 0 || !r.installed() {
				t.Fatalf("exit %d: %s", code, output)
			}
			if info, err := os.Stat(filepath.Join(r.root, "costroid-rows-in")); err != nil || !info.IsDir() {
				t.Fatalf("the report has no rows directory to touch its file in: %v", err)
			}
		})
	}
}

// The stages that install only refuse a stage, never an artifact: their rows
// are their own.
func TestOtherStagesNeverReadTheArtifactDirectory(t *testing.T) {
	r := newRelease(t)
	rowsIn(t, r.root, map[string]string{"costroid-bin/costroid": "x"})
	if code, output := pinnedInstall(r, "run"); code != 0 || !r.installed() {
		t.Fatalf("exit %d: %s", code, output)
	}
}

type actionStep struct {
	index int
	node  *yaml.Node
}

func actionSteps(t *testing.T) map[string]actionStep {
	t.Helper()
	steps := map[string]actionStep{}
	for index, step := range sequence(field(field(document(t, read(t, filepath.Join(actionDir, "action.yml"))), "runs"), "steps")) {
		steps[scalarField(step, "name")] = actionStep{index, step}
	}
	return steps
}

// The rows are fetched before costroid is installed, into a directory of their
// own, and the report and ingest read them there.
func TestReportAndIngestReadTheRowsFromTheirOwnDirectory(t *testing.T) {
	steps := actionSteps(t)
	fetch, install, report, ingest := steps["Fetch the rows"], steps["Install costroid"], steps["Report on the pull request"], steps["Send the rows to the ledger"]
	for name, step := range map[string]actionStep{"fetch": fetch, "install": install, "report": report, "ingest": ingest} {
		if step.node == nil {
			t.Fatalf("action.yml has no %s step", name)
		}
	}
	const dir = "${{ runner.temp }}/costroid-rows-in"
	if got := scalarField(field(fetch.node, "with"), "path"); got != dir {
		t.Errorf("the fetch unpacks into %q", got)
	}
	if fetch.index > install.index || steps["Install cosign"].index < fetch.index {
		t.Error("costroid or cosign is installed before the rows are fetched")
	}
	if got := scalarField(field(ingest.node, "env"), "COSTROID_ROWS"); got != dir+"/costroid-rows.jsonl" {
		t.Errorf("ingest reads %q", got)
	}
	if got := scalarField(field(report.node, "env"), "COSTROID_ROWS"); !strings.Contains(got, "format('{0}/costroid-rows-in/costroid-rows.jsonl', runner.temp)") {
		t.Errorf("report reads %q", got)
	}
	if got := scalarField(report.node, "if"); !strings.Contains(got, "steps.install.outcome == 'success'") || scalarField(install.node, "id") != "install" {
		t.Errorf("the report runs after a refused install: %q", got)
	}
	if got := scalarField(field(install.node, "env"), "COSTROID_STAGE"); got != "${{ inputs.stage }}" {
		t.Errorf("install reads the stage from %q", got)
	}
	for variable, output := range map[string]string{"COSTROID_ARTIFACT_REFUSED": "refused", "COSTROID_ARTIFACT_REFUSED_COUNT": "refused_count"} {
		if got := scalarField(field(report.node, "env"), variable); got != "${{ steps.install.outputs."+output+" }}" {
			t.Errorf("the report reads %s from %q", variable, got)
		}
	}
}

// On a runner, ingest's curl and jq are the system's, whatever a step put on
// PATH. The token request reads a local file and the ledger's address is
// malformed, so nothing leaves this machine.
func TestIngestOnARunnerCallsTheSystemsCurlAndJq(t *testing.T) {
	for _, tool := range []string{"/usr/bin/curl", "/usr/bin/jq"} {
		if _, err := os.Stat(tool); err != nil {
			t.Skipf("this machine has no %s", tool)
		}
	}
	s := newShell(t)
	marker := filepath.Join(s.root, "pwned")
	s.stub("curl", "touch "+marker+"\necho '{\"value\":\"a-token\"}'")
	s.stub("jq", "touch "+marker+"\necho a-token")
	answer := filepath.Join(s.root, "answer.json")
	write(t, answer, `{"value":"a-token"}`)
	rows := filepath.Join(s.root, "rows.jsonl")
	write(t, rows, "{}\n")
	env := []string{
		"COSTROID_ROWS=" + rows,
		"COSTROID_INGEST_URL=https://ledger example/v1/rows",
		"ACTIONS_ID_TOKEN_REQUEST_URL=file://" + answer,
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN=request-token",
	}
	code, output := s.run("ingest.sh", append(env, "GITHUB_ACTIONS=true")...)
	if code != 0 || !strings.Contains(output, "::add-mask::a-token") || !strings.Contains(output, "the ledger refused the rows") {
		t.Fatalf("exit %d: %s", code, output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("a curl or jq on PATH answered on a runner")
	}
	if code, output := s.run("ingest.sh", env...); code != 0 {
		t.Fatalf("exit %d: %s", code, output)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("off a runner, the stubs on PATH were not used, so the check above proves nothing")
	}
}

func TestRunAgentNamesEveryMissingInput(t *testing.T) {
	for name, check := range map[string]struct {
		env  []string
		want string
	}{
		"every input": {
			[]string{"COSTROID_AGENT=", "COSTROID_PROMPT=", "COSTROID_BUDGET_USD=", "COSTROID_AGENT_KEY=", "COSTROID_MINIMUM_AGENT_VERSION="},
			"costroid: the run stage needs these inputs, which are empty: agent, prompt, budget-usd, api-key, minimum-agent-version",
		},
		"all but the agent": {
			[]string{"COSTROID_PROMPT=", "COSTROID_BUDGET_USD=", "COSTROID_AGENT_KEY=", "COSTROID_MINIMUM_AGENT_VERSION="},
			"costroid: the run stage needs these inputs, which are empty: prompt, budget-usd, api-key, minimum-agent-version",
		},
		"two": {
			[]string{"COSTROID_BUDGET_USD=", "COSTROID_AGENT_KEY="},
			"costroid: the run stage needs these inputs, which are empty: budget-usd, api-key",
		},
		"an unknown agent beside them": {
			[]string{"COSTROID_AGENT=opencode", "COSTROID_PROMPT=", "COSTROID_AGENT_VERSION=latest"},
			"costroid: agent must be claude or codex, got 'opencode'\n" +
				"costroid: the run stage needs these inputs, which are empty: prompt\n" +
				"costroid: agent-version must be a release number, got 'latest'",
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := newShell(t)
			s.stubAgents()
			s.stub("npm", "touch "+filepath.Join(s.root, "npm"))
			code, output := s.run("run-agent.sh", agentEnv(s.root, check.env...)...)
			if code != 125 || strings.TrimSuffix(output, "\n") != check.want {
				t.Fatalf("exit %d:\n%s\nwant:\n%s", code, output, check.want)
			}
			for _, trace := range []string{"argv", "npm"} {
				if _, err := os.Stat(filepath.Join(s.root, trace)); !os.IsNotExist(err) {
					t.Fatalf("%s ran", trace)
				}
			}
		})
	}
}

// runBare runs an action script on a PATH of the stubs and the system's own
// directories alone, so an agent installed on this machine stays out of it.
func (s *shell) runBare(script string, env ...string) (int, string) {
	s.t.Helper()
	command := exec.Command("/bin/bash", filepath.Join(actionDir, script))
	command.Env = append([]string{"PATH=" + s.bin + ":/usr/bin:/bin", "HOME=" + s.root}, env...)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	if err != nil && command.ProcessState == nil {
		s.t.Fatal(err)
	}
	return command.ProcessState.ExitCode(), output.String()
}

// stubNpm installs an agent stub that reports the version npm was asked for.
func (s *shell) stubNpm() {
	s.t.Helper()
	s.stub("npm", `printf '%s\n' "$@" > "`+s.root+`/npm"
version=${3##*@}
name=claude
case $3 in @openai/codex@*) name=codex ;; esac
printf '#!/usr/bin/env bash\necho "%s"\n' "$version" > "`+s.bin+`/$name"
chmod +x "`+s.bin+`/$name"`)
	s.stub("costroid", `printf '%s\n' "$@" > "`+s.root+`/argv"`)
}

// skipWhereAnAgentIsInstalled skips a test that needs both agents off runBare's
// PATH.
func skipWhereAnAgentIsInstalled(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"claude", "codex"} {
		for _, dir := range []string{"/usr/bin", "/bin"} {
			if _, err := os.Stat(filepath.Join(dir, tool)); err == nil {
				t.Skipf("%s is in %s, so it cannot be absent here", tool, dir)
			}
		}
	}
}

func TestRunAgentInstallsTheAgentAtItsVersionWhenItIsAbsent(t *testing.T) {
	skipWhereAnAgentIsInstalled(t)
	for name, check := range map[string]struct {
		env  []string
		want string
	}{
		"claude at the recipes' version": {nil, "install\n--global\n@anthropic-ai/claude-code@2.1.316\n"},
		"claude at the version asked":    {[]string{"COSTROID_AGENT_VERSION=2.1.400"}, "install\n--global\n@anthropic-ai/claude-code@2.1.400\n"},
		"codex at the recipes' version": {
			[]string{"COSTROID_AGENT=codex", "COSTROID_MINIMUM_AGENT_VERSION=0.164.0-alpha.11.1"},
			"install\n--global\n@openai/codex@0.164.0-alpha.11.1\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := newShell(t)
			s.stubNpm()
			code, output := s.runBare("run-agent.sh", agentEnv(s.root, check.env...)...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, output)
			}
			if got := read(t, filepath.Join(s.root, "npm")); got != check.want {
				t.Fatalf("npm argv:\n%s\nwant:\n%s", got, check.want)
			}
			if _, err := os.Stat(filepath.Join(s.root, "argv")); err != nil {
				t.Fatal("the agent did not run after its install")
			}
		})
	}
}

func TestRunAgentHoldsAnInstalledAgentToTheFloor(t *testing.T) {
	skipWhereAnAgentIsInstalled(t)
	s := newShell(t)
	s.stubNpm()
	code, output := s.runBare("run-agent.sh", agentEnv(s.root, "COSTROID_AGENT_VERSION=2.1.300")...)
	if code != 125 || !strings.Contains(output, "costroid: claude 2.1.300 is older than the minimum 2.1.316") {
		t.Fatalf("exit %d: %s", code, output)
	}
	if _, err := os.Stat(filepath.Join(s.root, "argv")); !os.IsNotExist(err) {
		t.Fatal("the agent ran below its floor")
	}
}

func TestRunAgentLeavesAnAgentOnTheRunnerAsItIs(t *testing.T) {
	s := newShell(t)
	s.stubAgents()
	s.stub("npm", "touch "+filepath.Join(s.root, "npm"))
	if code, output := s.run("run-agent.sh", agentEnv(s.root, "COSTROID_AGENT_VERSION=2.1.400")...); code != 0 {
		t.Fatalf("exit %d: %s", code, output)
	}
	if _, err := os.Stat(filepath.Join(s.root, "npm")); !os.IsNotExist(err) {
		t.Fatal("npm ran for an agent already on the runner")
	}
}

func TestRunAgentRefusesAnInstallThatFails(t *testing.T) {
	skipWhereAnAgentIsInstalled(t)
	s := newShell(t)
	s.stub("npm", "exit 1")
	code, output := s.runBare("run-agent.sh", agentEnv(s.root)...)
	if code != 125 || !strings.Contains(output, "costroid: could not install @anthropic-ai/claude-code@2.1.316") {
		t.Fatalf("exit %d: %s", code, output)
	}
}

// The release run-agent.sh installs by default is the one each recipe installs.
func TestRunAgentInstallsTheReleaseTheRecipesInstall(t *testing.T) {
	script := read(t, filepath.Join(actionDir, "run-agent.sh"))
	for agent, install := range agentInstall {
		want := install.FindAllString(script, -1)
		if len(want) != 1 {
			t.Fatalf("run-agent.sh records %v for %s", want, agent)
		}
		found := 0
		for _, file := range recipeFiles(t) {
			for _, got := range install.FindAllString(read(t, file), -1) {
				found++
				if got != want[0] {
					t.Errorf("%s installs %s, and run-agent.sh %s", filepath.Base(file), got, want[0])
				}
			}
		}
		if found == 0 {
			t.Errorf("no recipe installs %s", agent)
		}
	}
}
