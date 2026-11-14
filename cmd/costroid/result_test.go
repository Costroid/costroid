package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"

	"github.com/Costroid/costroid/internal/adapter/claudecode"
	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/reason"
)

const (
	// resultSchema is result/v0.9 of Costroid/costroid-schemas, copied. A
	// published version never changes in place, so the copy cannot go stale.
	resultSchema = "../../internal/comment/testdata/result.schema.json"
	agentsDoc    = "../../docs/agents.md"
	actionFile   = "../../action/action.yml"
)

// stepFiles points the job's output and summary files at empty files.
func stepFiles(t *testing.T) (outputs, summary string) {
	t.Helper()
	directory := t.TempDir()
	outputs, summary = filepath.Join(directory, "output"), filepath.Join(directory, "summary")
	for _, path := range []string{outputs, summary} {
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GITHUB_OUTPUT", outputs)
	t.Setenv("GITHUB_STEP_SUMMARY", summary)
	return outputs, summary
}

func readOutputs(t *testing.T, path string) map[string]string {
	t.Helper()
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(readFile(t, path), "\n"), "\n") {
		name, value, found := strings.Cut(line, "=")
		if !found {
			t.Fatalf("output line %q has no =", line)
		}
		if _, repeated := values[name]; repeated {
			t.Fatalf("output %s is written twice", name)
		}
		values[name] = value
	}
	return values
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// checkBlock is the result object inside a posted check run's text.
func checkBlock(t *testing.T, text string) string {
	t.Helper()
	block, found := strings.CutPrefix(text, "```json\n")
	block, closed := strings.CutSuffix(block, "\n```")
	if !found || !closed {
		t.Fatalf("the check's text is not one fenced block:\n%s", text)
	}
	return block
}

func compactJSON(t *testing.T, text string) string {
	t.Helper()
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(text)); err != nil {
		t.Fatal(err)
	}
	return compact.String()
}

func TestActionOutputsCarryTheRecordedRunsFigures(t *testing.T) {
	stub := reportEnvironment(t, pullRequestEvent())
	outputs, _ := stepFiles(t)
	if code, _, stderr := execute(t, "report", "--rows", realRows, "--budget-usd", "5"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	// The recorded run's trace in docs/comment-example.md: cache writes are its
	// 3495 five-minute and 4450 one-hour tokens.
	want := map[string]string{
		"cost": realTotal, "currency": "USD", "cost_basis": "list_equivalent",
		"tokens_input_uncached": "8", "tokens_cache_read": "15605", "tokens_cache_write": "7945", "tokens_output": "272",
		"enforcement_grade": "agent-flag", "catalog_version": "0.4.2",
		"check_run_id": strconv.FormatInt(stub.Checks[0].ID, 10),
		"conclusion":   "neutral",
		"result":       compactJSON(t, checkBlock(t, stub.Checks[0].Text)),
	}
	got := readOutputs(t, outputs)
	for name, value := range want {
		if got[name] != value {
			t.Errorf("output %s = %q, want %q", name, got[name], value)
		}
	}
	if len(got) != len(comment.OutputNames) {
		t.Errorf("%d outputs, want %d: %v", len(got), len(comment.OutputNames), got)
	}
}

func TestActionOutputsSayMultipleAndSumNothingAcrossCurrencies(t *testing.T) {
	rows := filepath.Join(t.TempDir(), "rows.jsonl")
	usd := readFile(t, realRows)
	eur := strings.ReplaceAll(strings.SplitN(usd, "\n", 2)[0], `"BillingCurrency":"USD"`, `"BillingCurrency":"EUR"`)
	if err := os.WriteFile(rows, []byte(usd+eur+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reportEnvironment(t, pullRequestEvent())
	outputs, _ := stepFiles(t)
	execute(t, "report", "--rows", rows)
	got := readOutputs(t, outputs)
	want := map[string]string{
		"cost": "", "currency": comment.Multiple, "cost_basis": "list_equivalent",
		"tokens_input_uncached": "", "tokens_cache_read": "", "tokens_cache_write": "", "tokens_output": "",
		"enforcement_grade": "agent-flag",
	}
	for name, value := range want {
		if got[name] != value {
			t.Errorf("output %s = %q, want %q", name, got[name], value)
		}
	}
	if !strings.Contains(got["result"], `"currency":"EUR"`) || !strings.Contains(got["result"], `"list_cost":"`+realTotal+`"`) {
		t.Errorf("result does not hold each figure: %s", got["result"])
	}
}

func TestActionOutputsAreWrittenWhenNothingIsPosted(t *testing.T) {
	reportEnvironment(t, pullRequestEvent())
	t.Setenv("GITHUB_TOKEN", "")
	outputs, summary := stepFiles(t)
	if code, _, stderr := execute(t, "report", "--rows", realRows); code != 0 || !strings.Contains(stderr, "not posted: GITHUB_TOKEN is not set") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	got := readOutputs(t, outputs)
	if got["cost"] != realTotal || got["check_run_id"] != "" || !strings.Contains(got["result"], `"head_sha":"`+headCommit+`"`) {
		t.Errorf("outputs %v", got)
	}
	if text := readFile(t, summary); strings.Contains(text, realTotal) || !strings.Contains(text, "| Basis | Catalog | Attribution | Enforcement |") {
		t.Errorf("a summary whose setting was never read shows amounts:\n%s", text)
	}
}

func TestActionOutputsAreTheOnesTheActionDeclares(t *testing.T) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(readFile(t, actionFile)), &root); err != nil {
		t.Fatal(err)
	}
	var action struct {
		Outputs yaml.Node `yaml:"outputs"`
		Runs    struct {
			Steps []struct {
				ID   string `yaml:"id"`
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := root.Decode(&action); err != nil {
		t.Fatal(err)
	}
	var names []string
	for index := 0; index+1 < len(action.Outputs.Content); index += 2 {
		name := action.Outputs.Content[index].Value
		names = append(names, name)
		var output struct{ Value string }
		if err := action.Outputs.Content[index+1].Decode(&output); err != nil {
			t.Fatal(err)
		}
		if want := "${{ steps.report.outputs." + name + " }}"; output.Value != want {
			t.Errorf("output %s reads %q, want %q", name, output.Value, want)
		}
	}
	if !slices.Equal(names, comment.OutputNames) {
		t.Errorf("the action declares %v, the report writes %v", names, comment.OutputNames)
	}
	reporting := slices.IndexFunc(action.Runs.Steps, func(step struct {
		ID   string `yaml:"id"`
		Name string `yaml:"name"`
		Run  string `yaml:"run"`
	}) bool {
		return step.ID == "report"
	})
	if reporting < 0 || !strings.Contains(action.Runs.Steps[reporting].Run, "costroid report") {
		t.Error("no step with the id report runs costroid report")
	}
}

// codeSpan is where the summary puts a catalog version or a commit, the only
// digits a withheld summary may hold.
var codeSpan = regexp.MustCompile("`[^`]*`")

func TestStepSummaryWithholdsAmountsAndTokenCountsOnAPublicRepository(t *testing.T) {
	stub := reportEnvironment(t, pullRequestEvent())
	stub.Files[configFile] = showCostOff
	stub.SetPrivate(false)
	_, summaryPath := stepFiles(t)
	execute(t, "report", "--rows", realRows, "--budget-usd", "5")
	summary := readFile(t, summaryPath)
	for _, want := range []string{"| Basis | Catalog | Attribution | Enforcement |", "| List-equivalent, not billed | `0.4.2` | unattributed | agent-flag:", "Amounts and token counts are off on this public repository"} {
		if !strings.Contains(summary, want) {
			t.Errorf("no %q in\n%s", want, summary)
		}
	}
	if digits := regexp.MustCompile(`[0-9]+`).FindAllString(codeSpan.ReplaceAllString(summary, ""), -1); len(digits) > 0 {
		t.Errorf("the withheld summary holds %v:\n%s", digits, summary)
	}
}

func TestStepSummaryWithholdsNothingWhereAmountsAreShown(t *testing.T) {
	stub := reportEnvironment(t, pullRequestEvent())
	stub.SetPrivate(true)
	_, summaryPath := stepFiles(t)
	execute(t, "report", "--rows", realRows, "--budget-usd", "5")
	summary := readFile(t, summaryPath)
	table := "| Basis | Catalog | Cost | Enforcement | Input, uncached | Cache read | Cache write | Output |\n| --- | --- | --- | --- | --- | --- | --- | --- |\n" +
		"| List-equivalent, not billed | `0.4.2` | USD " + realTotal + " | agent-flag: client-side estimate, can pass the cap | 8 | 15605 | 7945 | 272 |\n"
	if !strings.HasPrefix(summary, "### Costroid\n\n"+table) {
		t.Errorf("the summary does not lead with the table:\n%s", summary)
	}
	if !strings.Contains(summary, "Cap: USD 5 (agent-flag: client-side estimate, can pass the cap).") {
		t.Errorf("no cap line in\n%s", summary)
	}
}

func validateResult(t *testing.T, object string) {
	t.Helper()
	file, err := os.Open(resultSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	document, err := jsonschema.UnmarshalJSON(file)
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("result.schema.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("result.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	instance, err := jsonschema.UnmarshalJSON(strings.NewReader(object))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Errorf("the object breaks result/v0.9: %#v\n%s", err, object)
	}
}

func TestRunJSONMatchesSchema(t *testing.T) {
	claude := replayClaude(t)
	out := filepath.Join(t.TempDir(), "rows.jsonl")
	// The agent's stream and its own stderr share this writer, as they share
	// a job log. A file takes both writers at once, and a buffer does not.
	stderrFile, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer stderrFile.Close()
	var printed bytes.Buffer
	code := dispatch(context.Background(), []string{"run", "--budget-usd", "5", "--out", out, "--json", "--", claude, "-p", "work"}, strings.NewReader(""), &printed, stderrFile)
	stdout, stderr := printed.String(), readFile(t, stderrFile.Name())
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stderr, `"type":"result"`) {
		t.Errorf("the agent's stream did not reach stderr:\n%s", stderr)
	}
	validateResult(t, stdout)
	var object struct {
		Conclusion *string `json:"conclusion"`
		Figures    []struct {
			ListCost string            `json:"list_cost"`
			Tokens   map[string]string `json:"tokens"`
		} `json:"figures"`
		Cap struct {
			Remainder string `json:"remainder"`
		} `json:"cap"`
	}
	if err := json.Unmarshal([]byte(stdout), &object); err != nil {
		t.Fatalf("stdout is not one object: %v\n%s", err, stdout)
	}
	if object.Conclusion != nil || len(object.Figures) != 1 || object.Figures[0].ListCost != realTotal || object.Figures[0].Tokens["output"] != "272" || object.Cap.Remainder != "4.998380275" {
		t.Errorf("object:\n%s", stdout)
	}
}

func TestRunJSONMatchesSchemaWhenTheRunWroteNoRows(t *testing.T) {
	rows := filepath.Join(t.TempDir(), "rows.jsonl")
	if err := os.WriteFile(rows, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	budget := decimal.MustParse("5")
	object, err := runResult(&stderr, runRecord{out: rows, budget: &budget})
	if err != nil {
		t.Fatal(err)
	}
	validateResult(t, object)
	if !strings.Contains(object, `"reason": "empty_rows_file"`) {
		t.Errorf("no reason in\n%s", object)
	}
}

func TestReportJSONMatchesBlock(t *testing.T) {
	for name, amounts := range map[string]string{"shown": "", "off": showCostOff} {
		t.Run(name, func(t *testing.T) {
			stub := reportEnvironment(t, pullRequestEvent())
			stub.SetPrivate(false)
			if amounts != "" {
				stub.Files[configFile] = amounts
			}
			code, stdout, stderr := execute(t, "report", "--rows", realRows, "--budget-usd", "5", "--json")
			if code != 0 || len(stub.Checks) != 1 {
				t.Fatalf("exit %d, %d checks: %s", code, len(stub.Checks), stderr)
			}
			if block := checkBlock(t, stub.Checks[0].Text); stdout != block+"\n" {
				t.Errorf("stdout differs from the check run's block:\n%s\n%s", stdout, block)
			}
			validateResult(t, stdout)
			validateResult(t, commentBlock(t, stub.Comments[0].Body))
		})
	}
}

// commentBlock is the copy of the result object in the comment.
func commentBlock(t *testing.T, body string) string {
	t.Helper()
	match := regexp.MustCompile("(?s)```json\n(.*?)\n```").FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("no block in\n%s", body)
	}
	return match[1]
}

// TestAgentsDocReadsTheResultFromTheCheckRun runs the page's jq program on the
// check run as the GitHub API lists it, and gets the object report printed.
func TestAgentsDocReadsTheResultFromTheCheckRun(t *testing.T) {
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq is not installed")
	}
	program := regexp.MustCompile(`--jq '([^']+)'`).FindStringSubmatch(readFile(t, agentsDoc))
	if program == nil {
		t.Fatal("docs/agents.md shows no --jq program")
	}
	stub := reportEnvironment(t, pullRequestEvent())
	_, printed, _ := execute(t, "report", "--rows", realRows, "--json")
	older := map[string]any{"id": 1, "completed_at": "2026-11-01T00:00:00Z", "output": map[string]string{"text": "```json\n{}\n```"}}
	newer := map[string]any{"id": stub.Checks[0].ID, "completed_at": "2026-11-02T00:00:00Z", "output": map[string]string{"text": stub.Checks[0].Text}}
	listing, err := json.Marshal(map[string]any{"total_count": 2, "check_runs": []any{older, newer}})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(jq, program[1])
	command.Stdin = bytes.NewReader(listing)
	read, err := command.Output()
	if err != nil {
		t.Fatalf("jq: %v", err)
	}
	if compactJSON(t, string(read)) != compactJSON(t, printed) {
		t.Errorf("the page's program reads\n%s\nand report printed\n%s", read, printed)
	}
}

// silentClaude is a Claude Code that ends with a result object and reports no
// usage, in a config directory of its own.
func silentClaude(t *testing.T) string {
	t.Helper()
	claude := replayClaude(t)
	stream := filepath.Join(t.TempDir(), "stream.jsonl")
	if err := os.WriteFile(stream, []byte(`{"type":"result","subtype":"success","is_error":false,"num_turns":0,"session_id":"`+sessionID+`","usage":{},"modelUsage":{}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(replayStream, stream)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	return claude
}

// runJSON runs costroid run --json and returns its exit code, the object it
// printed and the rows file.
func runJSON(t *testing.T, claude string) (int, string, string) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "rows.jsonl")
	stderrFile, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer stderrFile.Close()
	var printed bytes.Buffer
	code := dispatch(context.Background(), []string{"run", "--budget-usd", "5", "--out", out, "--json", "--", claude, "-p", "work"}, strings.NewReader(""), &printed, stderrFile)
	return code, printed.String(), out
}

// TestEmptyRunReasonInJSONAndComment follows each reason a run states from
// run --json to the report: the object names it, and the comment, the check
// run and report --json say it in words and as its code.
func TestEmptyRunReasonInJSONAndComment(t *testing.T) {
	cases := []struct {
		reason   reason.Reason
		code     int
		start    func(t *testing.T) string
		sentence string
	}{
		{reason.NoUsage, 0, silentClaude, "This report shows no figures. Reason `no_usage`: the agent reported no usage, so the run wrote no rows."},
		{reason.ConcurrentRun, exitFailure, func(t *testing.T) string {
			claude := silentClaude(t)
			release, err := claudecode.Agent{}.Claim()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(release)
			return claude
		}, "This report shows no figures. Reason `concurrent_run`: another costroid run holds the agent's state directory, so this run did not start and wrote no rows."},
	}
	for _, test := range cases {
		t.Run(string(test.reason), func(t *testing.T) {
			code, printed, rows := runJSON(t, test.start(t))
			if code != test.code {
				t.Fatalf("run exit %d, want %d:\n%s", code, test.code, printed)
			}
			validateResult(t, printed)
			field := `"run_reason": "` + string(test.reason) + `"`
			if !strings.Contains(printed, field) || !strings.Contains(printed, `"reason": "empty_rows_file"`) {
				t.Errorf("run --json lacks %s:\n%s", field, printed)
			}
			if written := readFile(t, runResultPath(rows)); written != printed {
				t.Errorf("the object beside the rows differs from run --json:\n%s\n%s", written, printed)
			}
			stub := reportEnvironment(t, pullRequestEvent())
			code, reported, stderr := execute(t, "report", "--rows", rows, "--json")
			if code != 0 || len(stub.Checks) != 1 || len(stub.Comments) != 1 {
				t.Fatalf("report exit %d, %d checks, %d comments: %s", code, len(stub.Checks), len(stub.Comments), stderr)
			}
			validateResult(t, reported)
			body := stub.Comments[0].Body
			for where, text := range map[string]string{"report --json": reported, "the check run": stub.Checks[0].Text, "the comment": body} {
				if !strings.Contains(text, field) {
					t.Errorf("%s lacks %s:\n%s", where, field, text)
				}
			}
			if !strings.Contains(body, test.sentence) || !strings.Contains(stub.Checks[0].Summary, test.sentence) {
				t.Errorf("no %q in\n%s", test.sentence, body)
			}
		})
	}
}

// TestReportFindsTheRunsReasonWithNoFlag runs the agent as the action does,
// without --json, into the action's rows path. The report is given only that
// path, and finds the reason in the object the run left beside it.
func TestReportFindsTheRunsReasonWithNoFlag(t *testing.T) {
	claude := silentClaude(t)
	rows := filepath.Join(t.TempDir(), "costroid-rows.jsonl")
	code, printed, stderr := execute(t, "run", "--budget-usd", "5", "--out", rows, "--", claude, "-p", "work")
	if code != 0 || strings.Contains(printed, "run_reason") {
		t.Fatalf("exit %d, stdout %q: %s", code, printed, stderr)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(rows), "costroid-rows.run.json")); err != nil {
		t.Fatalf("no object beside the rows: %v", err)
	}
	stub := reportEnvironment(t, pullRequestEvent())
	if code, _, stderr := execute(t, "report", "--rows", rows); code != 0 || len(stub.Comments) != 1 {
		t.Fatalf("report exit %d: %s", code, stderr)
	}
	body := stub.Comments[0].Body
	if !strings.Contains(body, "Reason `no_usage`: the agent reported no usage") || !strings.Contains(commentBlock(t, body), `"run_reason": "no_usage"`) {
		t.Errorf("the comment does not carry the run's reason:\n%s", body)
	}
}

// TestReportIgnoresARunReasonItCannotUse gives the report objects it must not
// take a reason from. Each warns, the report exits 0, and nothing it shows
// names a reason.
func TestReportIgnoresARunReasonItCannotUse(t *testing.T) {
	directory := t.TempDir()
	withRows := filepath.Join(directory, "with-rows.jsonl")
	if err := os.WriteFile(withRows, []byte(readFile(t, realRows)), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		rows, object string
		link         bool
	}{
		"a newer reason": {rows: "empty.jsonl", object: `{"run_reason":"agent_crashed"}`},
		"beside rows":    {rows: "with-rows.jsonl", object: `{"run_reason":"no_usage"}`},
		"not JSON":       {rows: "broken.jsonl", object: `{"run_reason":`},
		"a link":         {rows: "linked.jsonl", link: true},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			rows := filepath.Join(directory, test.rows)
			if test.rows != "with-rows.jsonl" {
				if err := os.WriteFile(rows, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			object := runResultPath(rows)
			if test.link {
				target := filepath.Join(directory, "elsewhere.json")
				if err := os.WriteFile(target, []byte(`{"run_reason":"no_usage"}`), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, object); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(object, []byte(test.object), 0o644); err != nil {
				t.Fatal(err)
			}
			stub := reportEnvironment(t, pullRequestEvent())
			code, printed, stderr := execute(t, "report", "--rows", rows, "--json")
			if code != 0 || !strings.Contains(stderr, "ignored") {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			validateResult(t, printed)
			if strings.Contains(printed, "run_reason") || strings.Contains(stub.Comments[0].Body, "Reason `") {
				t.Errorf("an ignored reason is shown:\n%s", stub.Comments[0].Body)
			}
		})
	}
}

// TestRunRemovesAnEarlierRunsObject leaves a refused run's object beside the
// rows path. A run that writes rows replaces it, and a run that cannot start
// its agent removes it, so neither inherits its reason.
func TestRunRemovesAnEarlierRunsObject(t *testing.T) {
	claude := replayClaude(t)
	rows := filepath.Join(t.TempDir(), "rows.jsonl")
	stale := `{"run_reason":"concurrent_run"}`
	for name, argv := range map[string][]string{
		"rows written":        {claude, "-p", "work"},
		"the agent not found": {filepath.Join(t.TempDir(), "claude"), "-p", "work"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(runResultPath(rows), []byte(stale), 0o644); err != nil {
				t.Fatal(err)
			}
			execute(t, append([]string{"run", "--budget-usd", "5", "--out", rows, "--"}, argv...)...)
			written, err := os.ReadFile(runResultPath(rows))
			if err == nil && strings.Contains(string(written), "run_reason") {
				t.Errorf("the earlier run's object is still there:\n%s", written)
			}
			if name == "rows written" && err != nil {
				t.Errorf("the run wrote no object: %v", err)
			}
		})
	}
}

// TestCheckRunCarriesItsID reads the check run's id from the check run's own
// object, from report --json and from the step's outputs. When the update that
// writes it fails, the check keeps the object it was created with, report
// --json still matches it, and the comment still names the check.
func TestCheckRunCarriesItsID(t *testing.T) {
	for name, fail := range map[string]bool{"updated": false, "update refused": true} {
		t.Run(name, func(t *testing.T) {
			stub := reportEnvironment(t, pullRequestEvent())
			if fail {
				stub.Fail["PATCH /check-runs/"] = 500
			}
			outputs, _ := stepFiles(t)
			code, printed, stderr := execute(t, "report", "--rows", realRows, "--budget-usd", "5", "--json")
			if code != 0 || len(stub.Checks) != 1 {
				t.Fatalf("exit %d, %d checks: %s", code, len(stub.Checks), stderr)
			}
			field := fmt.Sprintf(`"check_run_id": %d,`, stub.Checks[0].ID)
			block := checkBlock(t, stub.Checks[0].Text)
			if printed != block+"\n" {
				t.Errorf("report --json differs from the check run's object:\n%s\n%s", printed, block)
			}
			validateResult(t, printed)
			if strings.Contains(block, field) == fail {
				t.Errorf("the check run's object, update refused %v:\n%s", fail, block)
			}
			if fail != strings.Contains(stderr, "does not name itself") {
				t.Errorf("stderr: %s", stderr)
			}
			if !strings.Contains(commentBlock(t, stub.Comments[0].Body), field) {
				t.Errorf("the comment does not name the check run:\n%s", stub.Comments[0].Body)
			}
			got := readOutputs(t, outputs)
			if got["check_run_id"] != strconv.FormatInt(stub.Checks[0].ID, 10) || got["result"] != compactJSON(t, block) {
				t.Errorf("outputs %v", got)
			}
			if conclusion := stub.Checks[0].Conclusion; conclusion != "neutral" {
				t.Errorf("the update moved the conclusion to %q", conclusion)
			}
		})
	}
}
