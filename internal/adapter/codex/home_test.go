package codex

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/recipelint"
)

const (
	actionFixture     = "testdata/0.164.0-alpha.11.1/action/rollout.jsonl"
	actionThread      = "00000000-0000-4000-8000-000000000001"
	codexActionRecipe = "../../../recipes/github-codex-action.yml"
	runAgentScript    = "../../../action/run-agent.sh"
	readRunScript     = "../../../action/read-run.sh"
)

// actionJobStart is a minute before the recorded run's first line, as a job
// that set itself up first would start, and actionEnd its last line's second.
var (
	actionJobStart = time.Date(2026, 11, 7, 13, 27, 51, 0, time.UTC)
	actionEnd      = time.Date(2026, 11, 7, 13, 28, 57, 0, time.UTC)
)

// rollout is one rollout file for homeWith to write.
type rollout struct {
	thread  string
	content string
	written time.Time
}

// homeWith writes each rollout file under a new Codex home, as Codex lays it
// out, last written at its time.
func homeWith(t *testing.T, files ...rollout) string {
	t.Helper()
	home := t.TempDir()
	day := filepath.Join(home, "sessions", "2026", "11", "07")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		path := filepath.Join(day, rolloutName(file.thread))
		if err := os.WriteFile(path, []byte(file.content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, file.written, file.written); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func rolloutName(thread string) string {
	return "rollout-2026-11-07T13-28-51-" + thread + ".jsonl"
}

func fixture(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// recordedDeltas reads the recorded file's token_count lines on their own, as
// a person would: each request is how far the running total moved.
func recordedDeltas(t *testing.T) []usage {
	t.Helper()
	var deltas []usage
	var previous usage
	for _, line := range readLines(t, actionFixture) {
		var entry struct {
			Type    string `json:"type"`
			Payload struct {
				Type string `json:"type"`
				Info struct {
					Total usage `json:"total_token_usage"`
				} `json:"info"`
			} `json:"payload"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		if entry.Type != "event_msg" || entry.Payload.Type != "token_count" || entry.Payload.Info.Total == previous {
			continue
		}
		deltas = append(deltas, entry.Payload.Info.Total.minus(previous))
		previous = entry.Payload.Info.Total
	}
	return deltas
}

// The recorded openai/codex-action run, read from the Codex home it left: two
// requests, the recorded token_count deltas, and their total priced by hand.
// response-1: 3 x 2.00 + 10770 x 2.50 + 51 x 10.00 = 27441 USD per million
// tokens. response-2: 3 x 2.00 + 10770 x 0.10 + 188 x 2.50 + 33 x 10.00 = 1883.
// Together 29324 per million, USD 0.029324.
func TestCodexActionHome(t *testing.T) {
	home := homeWith(t, rollout{actionThread, fixture(t, actionFixture), actionEnd})
	report, err := Agent{}.ReadHome(home, actionJobStart)
	if err != nil {
		t.Fatal(err)
	}
	if report.Enforcement.Grade != adapter.GradeNone || !strings.Contains(report.Enforcement.Note, "Costroid did not start this run") {
		t.Errorf("enforcement %+v", report.Enforcement)
	}
	if report.CostBasis != focus.ListEquivalent || report.CostSource != costSourceRollout {
		t.Errorf("basis %s, source %s", report.CostBasis, report.CostSource)
	}
	if report.SessionID != actionThread || report.AgentVersion != recordedVersion {
		t.Errorf("session %q, version %q", report.SessionID, report.AgentVersion)
	}
	if len(report.Warnings) != 0 || report.Unrecorded != 0 {
		t.Errorf("warnings %q, unrecorded %d", report.Warnings, report.Unrecorded)
	}
	byHand := []adapter.Tokens{
		{InputUncached: 3, CacheWrite30m: 10770, Output: 51},
		{InputUncached: 3, CacheRead: 10770, CacheWrite30m: 188, Output: 33, CachedInputReported: 10770},
	}
	deltas := recordedDeltas(t)
	if len(report.Requests) != len(byHand) || len(deltas) != len(byHand) {
		t.Fatalf("%d requests, %d recorded deltas, want %d", len(report.Requests), len(deltas), len(byHand))
	}
	for i, request := range report.Requests {
		recorded, err := deltas[i].tokens()
		if err != nil {
			t.Fatal(err)
		}
		if request.Tokens != byHand[i] || recorded != byHand[i] {
			t.Errorf("request %d: read %+v, recorded %+v, by hand %+v", i+1, request.Tokens, recorded, byHand[i])
		}
		if request.Model != "gpt-6.1-sol" || request.Grain != adapter.PerRequest {
			t.Errorf("request %d: %+v", i+1, request)
		}
	}
	if got := listCost(t, report.Requests).String(); got != "0.029324" {
		t.Errorf("list cost %s USD, the recorded deltas priced by hand give 0.029324 USD", got)
	}
}

func TestReadHomeSkipsAFileLastWrittenBeforeTheJob(t *testing.T) {
	const earlier = "00000000-0000-4000-8000-0000000000e1"
	home := homeWith(t,
		rollout{actionThread, fixture(t, actionFixture), actionEnd},
		rollout{earlier, strings.ReplaceAll(fixture(t, actionFixture), actionThread, earlier), actionJobStart.Add(-2 * time.Hour)},
	)
	report, err := Agent{}.ReadHome(home, actionJobStart)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Requests) != 2 || report.SessionID != actionThread {
		t.Fatalf("%d requests in session %s", len(report.Requests), report.SessionID)
	}
	want := "1 rollout file under the Codex home was last written before the job started, so it was not read"
	if len(report.Warnings) != 1 || report.Warnings[0] != want {
		t.Errorf("warnings %q", report.Warnings)
	}
}

func TestReadHomeRefusesAFileStartedBeforeTheJob(t *testing.T) {
	later := actionJobStart.Add(time.Hour)
	home := homeWith(t, rollout{actionThread, fixture(t, actionFixture), later.Add(time.Second)})
	_, err := Agent{}.ReadHome(home, later)
	if err == nil || !strings.Contains(err.Error(), "before the job started, so it holds an earlier run's spend") {
		t.Fatalf("err %v", err)
	}
}

func TestReadHomeWithNoRolloutFileOfTheJobIsAnError(t *testing.T) {
	_, err := Agent{}.ReadHome(homeWith(t), actionJobStart)
	if err == nil || !strings.Contains(err.Error(), "did not start Codex or gave it another codex-home") {
		t.Fatalf("err %v", err)
	}
	_, err = Agent{}.ReadHome(homeWith(t, rollout{actionThread, fixture(t, actionFixture), actionJobStart.Add(-time.Hour)}), actionJobStart)
	if err == nil || !strings.HasSuffix(err.Error(), "; 1 rollout file there was last written before the job started") {
		t.Fatalf("err %v", err)
	}
	if _, err := (Agent{}).ReadHome(filepath.Join(t.TempDir(), "gone"), actionJobStart); err == nil {
		t.Fatal("a missing home read")
	}
}

func TestReadHomeRefusesAFileThatFailsACheck(t *testing.T) {
	skipped := strings.Replace(fixture(t, actionFixture), `"total_token_usage":{"input_tokens":21734`, `"total_token_usage":{"input_tokens":21735`, 1)
	if skipped == fixture(t, actionFixture) {
		t.Fatal("the fixture's last running total was not found")
	}
	_, err := Agent{}.ReadHome(homeWith(t, rollout{actionThread, skipped, actionEnd}), actionJobStart)
	if err == nil || !strings.Contains(err.Error(), "did not move by its last request") {
		t.Fatalf("err %v", err)
	}
}

func TestReadHomeCountsEachSubagentsFile(t *testing.T) {
	const subagent = "00000000-0000-4000-8000-0000000000c1"
	report, err := Agent{}.ReadHome(homeWith(t,
		rollout{actionThread, fixture(t, actionFixture), actionEnd},
		rollout{subagent, strings.ReplaceAll(fixture(t, "testdata/not-recorded/subagent/rollout.jsonl"), "@parent", actionThread), actionEnd},
	), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if report.SessionID != actionThread || len(report.Requests) != 3 {
		t.Fatalf("session %s, %d requests", report.SessionID, len(report.Requests))
	}
	want := adapter.Tokens{InputUncached: 3808, CacheRead: 8192, Output: 300, ReasoningOutput: 64, CachedInputReported: 8192}
	if sub := report.Requests[2]; sub.ID != "response-sub-1" || sub.Model != "gpt-5.6-luna" || sub.Tokens != want {
		t.Errorf("the subagent's request: %+v", sub)
	}
}

func TestReadHomeNamesARequestSentAndNeverSettled(t *testing.T) {
	lines := readLines(t, actionFixture)
	cut := 0
	for i, line := range lines {
		if strings.Contains(line, "custom_tool_call_output") {
			cut = i + 1
		}
	}
	report, err := Agent{}.ReadHome(homeWith(t, rollout{actionThread, strings.Join(lines[:cut], "\n") + "\n", actionEnd}), actionJobStart)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Requests) != 1 || report.Unrecorded != 1 {
		t.Fatalf("%d requests, %d unrecorded", len(report.Requests), report.Unrecorded)
	}
	if len(report.Warnings) != 1 || !strings.HasPrefix(report.Warnings[0], "the rollout files show 1 model request sent with no usage settled, the last at 2026-11-07T13:28:54.813Z") {
		t.Errorf("warnings %q", report.Warnings)
	}
}

// step is what the recipe test reads of one step.
type step struct {
	ID   string            `yaml:"id"`
	If   string            `yaml:"if"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	With map[string]string `yaml:"with"`
}

func agentSteps(t *testing.T) []step {
	t.Helper()
	var workflow struct {
		Jobs map[string]struct {
			Steps []step `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(fixture(t, codexActionRecipe)), &workflow); err != nil {
		t.Fatal(err)
	}
	return workflow.Jobs["agent"].Steps
}

// The codex-action recipe passes the linter under both profiles, and states
// what the reader needs of the action: a Codex home made for the job and read
// after the action at the same path, the recorded Codex version throughout,
// drop-sudo, and no allow-users that admits anyone.
func TestCodexActionRecipeLints(t *testing.T) {
	report, err := recipelint.Lint([]string{codexActionRecipe})
	if err != nil {
		t.Fatal(err)
	}
	if report.Files != 1 || len(report.Findings) != 0 {
		t.Fatalf("%d files, findings %v", report.Files, report.Findings)
	}
	customer, err := recipelint.LintWith([]string{codexActionRecipe}, recipelint.Options{Profile: recipelint.Customer})
	if err != nil {
		t.Fatal(err)
	}
	if customer.Agents != 1 || len(customer.Findings) != 0 {
		t.Fatalf("customer profile: %d agents, findings %v", customer.Agents, customer.Findings)
	}
	var codex, read *step
	var install string
	steps := agentSteps(t)
	for i := range steps {
		switch {
		case strings.HasPrefix(steps[i].Uses, "openai/codex-action@"):
			codex = &steps[i]
		case steps[i].With["stage"] == "read":
			read = &steps[i]
		case strings.Contains(steps[i].Run, "@openai/codex@"):
			install = steps[i].Run
		}
	}
	if codex == nil || read == nil {
		t.Fatal("the agent's job has no codex-action step or no read stage")
	}
	if !regexp.MustCompile(`^openai/codex-action@[0-9a-f]{40}$`).MatchString(codex.Uses) {
		t.Errorf("codex-action is pinned as %s", codex.Uses)
	}
	if home := codex.With["codex-home"]; !strings.HasPrefix(home, "${{ runner.temp }}/") || read.With["codex-home"] != home {
		t.Errorf("codex-home %q on the action, %q on the read stage", home, read.With["codex-home"])
	}
	for what, got := range map[string]string{
		"the install":           install,
		"codex-version":         codex.With["codex-version"],
		"minimum-agent-version": read.With["minimum-agent-version"],
	} {
		if !strings.Contains(got, recordedVersion) {
			t.Errorf("%s names %q, not the recorded %s", what, got, recordedVersion)
		}
	}
	if codex.With["safety-strategy"] != "drop-sudo" {
		t.Errorf("safety-strategy %q", codex.With["safety-strategy"])
	}
	if users, set := codex.With["allow-users"]; !set || strings.Contains(users, "*") {
		t.Errorf("allow-users %q, set %t", users, set)
	}
	if _, set := codex.With["allow-bots"]; set {
		t.Error("allow-bots is set")
	}
	if !strings.HasPrefix(read.If, "always()") || !strings.Contains(read.If, "steps."+codex.ID+".outcome") {
		t.Errorf("the read stage runs if %q", read.If)
	}
}

// readRun runs the action's read stage with stand-ins for codex and costroid,
// and returns the exit code, the output and the arguments costroid got.
func readRun(t *testing.T, env ...string) (int, string, []string) {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	args := filepath.Join(root, "costroid-arguments")
	for name, body := range map[string]string{
		"codex":    `echo "codex-cli ` + recordedVersion + `"`,
		"costroid": `printf '%s\n' "$@" >"` + args + `"`,
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/usr/bin/env bash\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command("bash", readRunScript)
	command.Env = append([]string{"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + root, "COSTROID_ROWS=rows.jsonl"}, env...)
	output, err := command.CombinedOutput()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(args)
	return code, string(output), strings.Fields(string(data))
}

// The read stage hands ingest-run the Codex home, and the job's start as the
// runner's event file dates it, and holds codex to the floor.
func TestReadRunGivesIngestRunTheCodexHomeAndTheJobsStart(t *testing.T) {
	home := t.TempDir()
	event := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(event, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(event, actionJobStart, actionJobStart); err != nil {
		t.Fatal(err)
	}
	code, output, args := readRun(t, "COSTROID_CODEX_HOME="+home, "GITHUB_EVENT_PATH="+event, "COSTROID_MINIMUM_AGENT_VERSION="+recordedVersion)
	want := []string{"ingest-run", "--codex-home", home, "--since", "2026-11-07T13:27:51Z", "--out", "rows.jsonl"}
	if code != 0 || strings.Join(args, " ") != strings.Join(want, " ") {
		t.Fatalf("exit %d, costroid %q: %s", code, args, output)
	}
	for _, tc := range []struct {
		name string
		env  []string
		line string
	}{
		{"both inputs", []string{"COSTROID_CODEX_HOME=" + home, "COSTROID_EXECUTION_FILE=" + event}, "execution-file and codex-home are both set"},
		{"no directory", []string{"COSTROID_CODEX_HOME=" + event}, "codex-home " + event + " is not a directory"},
		{"an older codex", []string{"COSTROID_CODEX_HOME=" + home, "COSTROID_MINIMUM_AGENT_VERSION=0.165.0"}, "codex " + recordedVersion + " is older than the minimum 0.165.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, output, args := readRun(t, tc.env...)
			if code != 125 || !strings.Contains(output, "costroid: "+tc.line) || len(args) != 0 {
				t.Fatalf("exit %d, costroid %q: %s", code, args, output)
			}
		})
	}
}

// runAgent runs the action's run stage for Codex with stand-ins for codex and
// costroid. It returns the exit code, the output and the environment the
// stand-in costroid, and so the agent, was started in.
func runAgent(t *testing.T, env ...string) (int, string, map[string]string) {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	dump := filepath.Join(root, "agent-environment")
	for name, body := range map[string]string{
		"codex":    `echo "codex-cli ` + recordedVersion + `"`,
		"costroid": `env >"` + dump + `"`,
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/usr/bin/env bash\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command("bash", runAgentScript)
	command.Env = append([]string{
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"HOME=" + root,
		"RUNNER_TEMP=" + root,
		"COSTROID_AGENT=codex",
		"COSTROID_PROMPT=Review the diff.",
		"COSTROID_BUDGET_USD=5",
		"COSTROID_MINIMUM_AGENT_VERSION=" + recordedVersion,
		"COSTROID_ROWS=" + filepath.Join(root, "rows.jsonl"),
	}, env...)
	output, err := command.CombinedOutput()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	if data, err := os.ReadFile(dump); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if name, value, ok := strings.Cut(line, "="); ok {
				seen[name] = value
			}
		}
	}
	return code, string(output), seen
}

// The run stage takes Codex's key from api-key or, with api-key empty, from
// CODEX_API_KEY or OPENAI_API_KEY in the step's environment. Codex then holds
// it as CODEX_API_KEY alone.
func TestEitherOpenAISecretName(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []string
		key  string
	}{
		{"api-key", []string{"COSTROID_AGENT_KEY=sk-input"}, "sk-input"},
		{"CODEX_API_KEY", []string{"CODEX_API_KEY=sk-codex"}, "sk-codex"},
		{"OPENAI_API_KEY", []string{"OPENAI_API_KEY=sk-openai"}, "sk-openai"},
		{"api-key before either name", []string{"COSTROID_AGENT_KEY=sk-input", "CODEX_API_KEY=sk-codex", "OPENAI_API_KEY=sk-openai"}, "sk-input"},
		{"CODEX_API_KEY before OPENAI_API_KEY", []string{"CODEX_API_KEY=sk-codex", "OPENAI_API_KEY=sk-openai"}, "sk-codex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, output, seen := runAgent(t, tc.env...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, output)
			}
			if seen["CODEX_API_KEY"] != tc.key {
				t.Errorf("Codex holds CODEX_API_KEY %q, want %q", seen["CODEX_API_KEY"], tc.key)
			}
			if _, held := seen["OPENAI_API_KEY"]; held {
				t.Error("Codex also holds OPENAI_API_KEY")
			}
			if _, held := seen["COSTROID_AGENT_KEY"]; held {
				t.Error("the agent holds the input's variable")
			}
		})
	}
	t.Run("neither name", func(t *testing.T) {
		code, output, _ := runAgent(t)
		if code != 125 || !strings.Contains(output, "which are empty: api-key") {
			t.Fatalf("exit %d: %s", code, output)
		}
	})
}

// Whichever name the key came under, Codex runs with CODEX_API_KEY set, so the
// basis rule reads the run as billed to the key.
func TestCodexAcceptsEitherSecretName(t *testing.T) {
	for _, name := range []string{"CODEX_API_KEY", "OPENAI_API_KEY"} {
		t.Run(name, func(t *testing.T) {
			code, output, seen := runAgent(t, name+"=sk-test")
			if code != 0 {
				t.Fatalf("exit %d: %s", code, output)
			}
			t.Setenv("CODEX_API_KEY", seen["CODEX_API_KEY"])
			session := Agent{}.NewSession(adapter.Plan{}).(*Session)
			if session.costBasis != focus.Measured {
				t.Errorf("a key given as %s reads %s", name, session.costBasis)
			}
		})
	}
}
