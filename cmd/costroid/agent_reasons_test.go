package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// failedClaude is a Claude Code whose key was refused: its assistant message
// states authentication_failed, and its result says it ended in an error and
// reports no usage. It writes no session file.
func failedClaude(t *testing.T) string {
	t.Helper()
	claude := replayClaude(t)
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"` + sessionID + `","apiKeySource":"ANTHROPIC_API_KEY","claude_code_version":"2.1.316"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Invalid API key"}]},"error":"authentication_failed","session_id":"` + sessionID + `"}`,
		`{"type":"result","subtype":"success","is_error":true,"num_turns":1,"session_id":"` + sessionID + `","usage":{"input_tokens":0,"output_tokens":0},"modelUsage":{}}`,
	}, "\n") + "\n"
	path := filepath.Join(t.TempDir(), "stream.jsonl")
	if err := os.WriteFile(path, []byte(stream), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(replayStream, path)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	return claude
}

// TestAuthFailureNamesItsReasonAndPrintsNoCost runs an agent whose key was
// refused, as the action does, then reports on its rows. The run names the
// class and its fix, prints no cost for the nothing it measured, and leaves
// an object with the reason and the class. The comment says the same.
func TestAuthFailureNamesItsReasonAndPrintsNoCost(t *testing.T) {
	claude := failedClaude(t)
	rows := filepath.Join(t.TempDir(), "costroid-rows.jsonl")
	code, _, stderr := execute(t, "run", "--budget-usd", "5", "--out", rows, "--", claude, "-p", "work")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	said := "costroid: warning: reason agent_error: the agent ended with authentication_failed: check the API key the agent runs with, the api-key input in the action\n"
	if !strings.Contains(stderr, said) || !strings.Contains(stderr, "costroid: 0 rows in ") {
		t.Errorf("the run does not name its reason:\n%s", stderr)
	}
	if strings.Contains(stderr, "at list") || strings.Contains(stderr, "no_usage") {
		t.Errorf("a run that measured nothing states a cost or another reason:\n%s", stderr)
	}
	object := readFile(t, runResultPath(rows))
	validateResult(t, object)
	var stated struct {
		RunReason  string `json:"run_reason"`
		AgentError *struct {
			Class string `json:"class"`
		} `json:"agent_error"`
	}
	if err := json.Unmarshal([]byte(object), &stated); err != nil {
		t.Fatal(err)
	}
	if stated.RunReason != "agent_error" || stated.AgentError == nil || stated.AgentError.Class != "authentication_failed" {
		t.Errorf("the run's object:\n%s", object)
	}

	stub := reportEnvironment(t, pullRequestEvent())
	t.Setenv(agentResultVariable, "success")
	code, reported, stderr := execute(t, "report", "--rows", rows, "--json")
	if code != 0 || len(stub.Comments) != 1 {
		t.Fatalf("report exit %d: %s", code, stderr)
	}
	validateResult(t, reported)
	body := stub.Comments[0].Body
	sentence := "This report shows no figures. Reason `agent_error`: the agent ended in an error before it reported usage, so the run wrote no rows. " +
		"It ended with `authentication_failed`: check the API key the agent runs with, the api-key input in the action."
	if !strings.Contains(body, sentence) || !strings.Contains(stub.Checks[0].Summary, sentence) {
		t.Errorf("no %q in\n%s", sentence, body)
	}
	for where, text := range map[string]string{"report --json": reported, "the comment": commentBlock(t, body)} {
		if !strings.Contains(text, `"run_reason": "agent_error"`) || !strings.Contains(text, `"class": "authentication_failed"`) || !strings.Contains(text, `"agent_skipped": false`) {
			t.Errorf("%s lacks the reason or the class:\n%s", where, text)
		}
	}
	if strings.Contains(body, "USD 0") {
		t.Errorf("the comment states a cost:\n%s", body)
	}
}

// TestSkippedAgentIsNamedAsSkipped reports on the empty rows file the action
// leaves when the agent's job was skipped. The comment says the agent did not
// run and why, and the object names the reason and the skip.
func TestSkippedAgentIsNamedAsSkipped(t *testing.T) {
	rows := filepath.Join(t.TempDir(), "costroid-rows.jsonl")
	if err := os.WriteFile(rows, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	stub := reportEnvironment(t, pullRequestEvent())
	t.Setenv(agentResultVariable, "skipped")
	t.Setenv("GITHUB_RUN_ID", "19203384756")
	t.Setenv("GITHUB_RUN_ATTEMPT", "2")
	code, reported, stderr := execute(t, "report", "--rows", rows, "--json")
	if code != 0 || len(stub.Checks) != 1 || len(stub.Comments) != 1 {
		t.Fatalf("report exit %d, %d checks, %d comments: %s", code, len(stub.Checks), len(stub.Comments), stderr)
	}
	validateResult(t, reported)
	sentence := "The agent did not run on this pull request: the workflow skipped its job, as the recipes do for a first-time contributor without the `costroid-agent` label and for a fork's pull request."
	body := stub.Comments[0].Body
	if !strings.Contains(body, sentence) || !strings.Contains(stub.Checks[0].Summary, sentence) {
		t.Errorf("no %q in\n%s", sentence, body)
	}
	if strings.Contains(body, "job log") || strings.Contains(body, "The rows file holds no rows") {
		t.Errorf("the comment blames a job that never ran:\n%s", body)
	}
	for where, text := range map[string]string{"report --json": reported, "the check run": stub.Checks[0].Text, "the comment": commentBlock(t, body)} {
		for _, field := range []string{`"reason": "agent_skipped"`, `"agent_skipped": true`, `"ci_run": {`, `"id": 19203384756`, `"attempt": 2`} {
			if !strings.Contains(text, field) {
				t.Errorf("%s lacks %s:\n%s", where, field, text)
			}
		}
	}

	stub = reportEnvironment(t, pullRequestEvent())
	t.Setenv(agentResultVariable, "")
	code, flagged, stderr := execute(t, "report", "--rows", rows, "--agent-result", "skipped", "--json")
	if code != 0 || !strings.Contains(flagged, `"reason": "agent_skipped"`) || !strings.Contains(stub.Comments[0].Body, sentence) {
		t.Errorf("--agent-result skipped: exit %d, %s\n%s", code, stderr, flagged)
	}
}

// TestAnAgentResultTheReportCannotUseIsIgnored gives the report a result it
// does not know, and a skipped job beside rows. Each warns and changes nothing
// the report shows.
func TestAnAgentResultTheReportCannotUseIsIgnored(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "costroid-rows.jsonl")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct{ rows, result, warning string }{
		"an unknown result": {empty, "neutral", `the agent's result "neutral" is not success, failure, cancelled or skipped`},
		"skipped with rows": {realRows, "skipped", "the agent's job reads skipped, and the rows file is not empty"},
	} {
		t.Run(name, func(t *testing.T) {
			stub := reportEnvironment(t, pullRequestEvent())
			t.Setenv(agentResultVariable, test.result)
			code, reported, stderr := execute(t, "report", "--rows", test.rows, "--json")
			if code != 0 || !strings.Contains(stderr, test.warning) {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			validateResult(t, reported)
			if strings.Contains(reported, `"reason": "agent_skipped"`) || !strings.Contains(reported, `"agent_skipped": null`) || strings.Contains(stub.Comments[0].Body, "did not run") {
				t.Errorf("an ignored result is shown:\n%s", reported)
			}
		})
	}
}

// TestAnAgentErrorBesideFiguresIsNamed reports on rows whose run says its
// agent ended rate limited. The figures stand, and one line names the class
// and its fix.
func TestAnAgentErrorBesideFiguresIsNamed(t *testing.T) {
	rows := filepath.Join(t.TempDir(), "costroid-rows.jsonl")
	if err := os.WriteFile(rows, []byte(readFile(t, realRows)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runResultPath(rows), []byte(`{"agent_error":{"class":"rate_limit"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	stub := reportEnvironment(t, pullRequestEvent())
	t.Setenv(agentResultVariable, "failure")
	code, reported, stderr := execute(t, "report", "--rows", rows, "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	validateResult(t, reported)
	line := "The agent ended with `rate_limit` after the requests these figures cover: run the job again later, or raise the API key's rate limit with the provider."
	if body := stub.Comments[0].Body; !strings.Contains(body, line) || !strings.Contains(body, realTotal) {
		t.Errorf("no %q beside the figures in\n%s", line, body)
	}
	if !strings.Contains(reported, `"class": "rate_limit"`) || strings.Contains(reported, "run_reason") {
		t.Errorf("object:\n%s", reported)
	}

	if err := os.WriteFile(runResultPath(rows), []byte(`{"agent_error":{"class":"agent_crashed"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	stub = reportEnvironment(t, pullRequestEvent())
	code, reported, stderr = execute(t, "report", "--rows", rows, "--json")
	if code != 0 || !strings.Contains(stderr, `the agent's error class "agent_crashed" is not one this release knows`) {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(reported, `"agent_error": null`) || strings.Contains(stub.Comments[0].Body, "The agent ended with") {
		t.Errorf("an unknown class is shown:\n%s", reported)
	}
}

// TestRunNamesTheArgumentsItLacks leaves out each of run's required
// arguments in turn, and all of them together.
func TestRunNamesTheArgumentsItLacks(t *testing.T) {
	for args, want := range map[string]string{
		"--out r.jsonl -- claude -p work": "costroid: run needs --budget-usd\n",
		"--budget-usd 5 --out r.jsonl":    "costroid: run needs an agent command after --\n",
		"--budget-usd 5":                  "costroid: run needs an agent command after --\n",
		"--json":                          "costroid: run needs --budget-usd and an agent command after --\n",
	} {
		code, _, stderr := execute(t, append([]string{"run"}, strings.Fields(args)...)...)
		if code != exitFailure || stderr != want {
			t.Errorf("run %s: exit %d, %q, want %q", args, code, stderr, want)
		}
	}
}

// TestAnIngestedAgentErrorKeepsItsObjectValid reads the execution file of a
// claude-code-action run whose key was refused. The run names its reason and
// class on stderr, and the object it leaves holds to result/v0.7: a reason
// whose class the object cannot carry is left out, never shown without it.
func TestAnIngestedAgentErrorKeepsItsObjectValid(t *testing.T) {
	clearCI(t)
	messages := `[{"type":"system","subtype":"init","session_id":"` + sessionID + `","apiKeySource":"ANTHROPIC_API_KEY","claude_code_version":"2.1.316"},` +
		`{"type":"assistant","message":{"content":[]},"error":"authentication_failed"},` +
		`{"type":"result","subtype":"success","is_error":true,"session_id":"` + sessionID + `","modelUsage":{}}]`
	execution := filepath.Join(t.TempDir(), "execution.json")
	if err := os.WriteFile(execution, []byte(messages), 0o644); err != nil {
		t.Fatal(err)
	}
	rows := filepath.Join(t.TempDir(), "rows.jsonl")
	code, _, stderr := execute(t, "ingest-run", "--execution-file", execution, "--out", rows)
	if code != 0 || !strings.Contains(stderr, "reason agent_error: the agent ended with authentication_failed") || strings.Contains(stderr, "at list") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	validateResult(t, readFile(t, runResultPath(rows)))
}

// TestAnAgentErrorReasonWithoutItsClassIsDropped gives the report a run
// object that states agent_error and no class. The reason is not shown,
// because the object it prints could not carry it.
func TestAnAgentErrorReasonWithoutItsClassIsDropped(t *testing.T) {
	rows := filepath.Join(t.TempDir(), "costroid-rows.jsonl")
	if err := os.WriteFile(rows, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runResultPath(rows), []byte(`{"run_reason":"agent_error"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	stub := reportEnvironment(t, pullRequestEvent())
	t.Setenv(agentResultVariable, "")
	code, reported, stderr := execute(t, "report", "--rows", rows, "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	validateResult(t, reported)
	if strings.Contains(reported, "run_reason") || strings.Contains(stub.Comments[0].Body, "Reason `") {
		t.Errorf("a reason without its class is shown:\n%s", reported)
	}
}
