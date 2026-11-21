package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm/scmtest"
)

const overBudget = "policy:\n  run_budget_usd: \"0.001\"\n"

func writeBaseConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "base.yml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// checkVerdict reads the verdict from the check run's machine block.
func checkVerdict(t *testing.T, text string) policy.Verdict {
	t.Helper()
	var block struct {
		Conclusion string         `json:"conclusion"`
		Policy     policy.Verdict `json:"policy"`
	}
	body := strings.TrimSuffix(strings.TrimPrefix(text, "```json\n"), "\n```")
	if err := json.Unmarshal([]byte(body), &block); err != nil {
		t.Fatalf("%v: %s", err, text)
	}
	if block.Conclusion != block.Policy.Conclusion {
		t.Fatalf("the block concludes %s and its verdict %s", block.Conclusion, block.Policy.Conclusion)
	}
	return block.Policy
}

func TestPolicyGivesTheVerdictTheCheckGives(t *testing.T) {
	for name, body := range map[string]string{
		"over the budget":  overBudget,
		"under the budget": "policy:\n  run_budget_usd: \"5\"\n  max_run_cost_usd: \"1\"\n",
		"no policy":        "comment:\n  show_cost: true\n",
	} {
		t.Run(name, func(t *testing.T) {
			stub := reportEnvironment(t, pullRequestEvent())
			stub.Files[configFile] = body
			if code, _, stderr := execute(t, "report", "--rows", realRows); code != 0 || len(stub.Checks) != 1 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			check := checkVerdict(t, stub.Checks[0].Text)
			if stub.Checks[0].Conclusion != check.Conclusion || check.ConfigRef != baseCommit || !check.ConfigFromBase {
				t.Fatalf("posted %s, verdict %+v", stub.Checks[0].Conclusion, check)
			}
			code, stdout, stderr := execute(t, "policy", "--rows", realRows, "--config", writeBaseConfig(t, body), "--at", check.EvaluatedAt, "--json")
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			var offline policy.Verdict
			if err := json.Unmarshal([]byte(stdout), &offline); err != nil {
				t.Fatal(err)
			}
			check.ConfigRef, check.ConfigFromBase = "", false
			want, _ := json.Marshal(check)
			got, _ := json.Marshal(offline)
			if string(got) != string(want) {
				t.Errorf("offline:\n%s\ncheck:\n%s", got, want)
			}
		})
	}
}

// lastLine is the last line a command printed.
func lastLine(printed string) string {
	lines := strings.Split(strings.TrimSuffix(printed, "\n"), "\n")
	return lines[len(lines)-1]
}

// TestPolicyEndsOnTheVerdictSentence holds the last line costroid policy
// prints to the title of the check the report posts for the same rows and the
// same costroid.yml: over the budget, within it by the job's own figures with
// a second rule that could not run, and with no rule set. The lines above it
// are the ones the sentence leaves unsaid.
func TestPolicyEndsOnTheVerdictSentence(t *testing.T) {
	figure := "this run cost USD " + realTotal + " at list (List-equivalent, not billed)"
	for name, tc := range map[string]struct{ config, sentence, above string }{
		"over the budget": {overBudget, "Over budget, warn-only: " + figure + ", over the USD 0.001 run budget. After the warn-only period this fails the check.",
			"Warn-only: the commit that added this workflow to the base branch could not be read, so a rule that fails warns. Add `enforce: true` under `policy` in `costroid.yml` to enforce now.\n"},
		"under the budget": {"policy:\n  run_budget_usd: \"5\"\n  monthly_budget_usd: \"100\"\n",
			"Within budget, by the job's own figures: " + figure + " of the USD 5.00 run budget. Nothing outside the job checked the figures, so the check is neutral.",
			"Monthly budget: the month to date comes only from a ledger, and this check has none, so the rule did not run.\n"},
		"no policy": {"comment:\n  show_cost: true\n", "No budget set: " + figure + ". No rule decides this check.", ""},
	} {
		t.Run(name, func(t *testing.T) {
			stub := reportEnvironment(t, pullRequestEvent())
			stub.Files[configFile] = tc.config
			if code, _, stderr := execute(t, "report", "--rows", realRows); code != 0 || len(stub.Checks) != 1 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			if stub.Checks[0].Title != tc.sentence {
				t.Fatalf("the check's title:\n got %s\nwant %s", stub.Checks[0].Title, tc.sentence)
			}
			code, stdout, stderr := execute(t, "policy", "--rows", realRows, "--config", writeBaseConfig(t, tc.config), "--at", checkVerdict(t, stub.Checks[0].Text).EvaluatedAt)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			if got := lastLine(stdout); got != tc.sentence || !strings.HasSuffix(stdout, tc.sentence+"\n") {
				t.Errorf("policy ends on\n%s\nand the check's title is\n%s", got, tc.sentence)
			}
			if above := strings.TrimSuffix(stdout, tc.sentence+"\n"); tc.above != "" && above != tc.above || strings.Contains(above, "Run budget: ") {
				t.Errorf("above the sentence policy prints\n%swant\n%s", above, tc.above)
			}
		})
	}
	code, stdout, _ := execute(t, "policy", "--rows", realRows, "--config", writeBaseConfig(t, overBudget), "--json")
	if code != 0 || strings.Contains(stdout, "Over budget") || !strings.HasSuffix(stdout, "}\n") {
		t.Errorf("--json prints more than the verdict: exit %d:\n%s", code, stdout)
	}
}

// TestPolicyEndsOnTheCheckTitleUnderEveryRule runs the report and costroid
// policy over the same rows, costroid.yml and earlier runs, for each rule a
// pull request's history lets run, and holds the last line policy prints to
// the title the report's check carries.
func TestPolicyEndsOnTheCheckTitleUnderEveryRule(t *testing.T) {
	text := func(config string) *string { return &config }
	for _, tc := range []pathCase{
		{name: "the pull request over its budget", config: text("policy:\n  pr_budget_usd: \"0.011\"\n  enforce: true\n"), earlier: 10},
		{name: "the pull request within its budget", config: text("policy:\n  pr_budget_usd: \"0.012\"\n  enforce: true\n"), earlier: 10},
		{name: "a run over the P50 of ten earlier runs", config: text("policy:\n  max_increase_usd: \"0.0006\"\n  enforce: true\n"), earlier: 10},
		{name: "a run within the P50 of ten earlier runs", config: text("policy:\n  max_increase_usd: \"0.0007\"\n  enforce: true\n"), earlier: 10},
		{name: "nine earlier runs are no baseline", config: text("policy:\n  max_increase_usd: 0\n  enforce: true\n"), earlier: 9},
		{name: "a model off the list", config: text("policy:\n  models:\n    - gpt-*\n  enforce: true\n")},
		{name: "no costroid.yml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects := earlierReports(t, tc.earlier)
			stub := reportEnvironment(t, pullRequestEvent())
			stub.SetPrivate(true)
			if tc.config != nil {
				stub.Files[configFile] = *tc.config
			}
			stub.Pulls = []scmtest.Pull{{Number: 7, State: "open", HeadSHA: headCommit, Commits: []string{earlierCommit, headCommit}}}
			for index, object := range objects {
				stub.Checks = append(stub.Checks, scmtest.Check{
					ID: int64(100 + index), Name: comment.CheckName, HeadSHA: earlierCommit, Conclusion: policy.Neutral,
					Text: "```json\n" + object + "\n```", App: "github-actions",
				})
			}
			if code, _, stderr := execute(t, "report", "--rows", realRows); code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			posted := stub.Checks[len(stub.Checks)-1]
			if posted.HeadSHA != headCommit || posted.Title == "" {
				t.Fatalf("the report posted no check with a title: %+v", posted)
			}
			directory := t.TempDir()
			configPath := filepath.Join(directory, "base.yml")
			if tc.config != nil {
				if err := os.WriteFile(configPath, []byte(*tc.config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var lines bytes.Buffer
			for _, object := range objects {
				if err := json.Compact(&lines, []byte(object)); err != nil {
					t.Fatal(err)
				}
				lines.WriteByte('\n')
			}
			historyPath := filepath.Join(directory, "earlier.jsonl")
			if err := os.WriteFile(historyPath, lines.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			code, stdout, stderr := execute(t, "policy", "--rows", realRows, "--config", configPath, "--history", historyPath)
			if code != 0 && code != exitRejected {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			if got := lastLine(stdout); got != posted.Title {
				t.Errorf("policy ends on\n%s\nand the check's title is\n%s", got, posted.Title)
			}
		})
	}
}

func TestPolicyExitsOneOnFailure(t *testing.T) {
	config := writeBaseConfig(t, overBudget)
	code, stdout, stderr := execute(t, "policy", "--rows", realRows, "--config", config,
		"--workflow-added", "2026-10-01T00:00:00Z", "--at", "2026-11-06T00:00:00Z")
	if want := "Over budget: this run cost USD " + realTotal + " at list (List-equivalent, not billed), over the USD 0.001 run budget.\n"; code != exitRejected || stdout != want {
		t.Fatalf("exit %d:\n%s%s", code, stdout, stderr)
	}
	code, stdout, _ = execute(t, "policy", "--rows", realRows, "--config", config,
		"--workflow-added", "2026-11-05T00:00:00Z", "--at", "2026-11-06T00:00:00Z")
	if code != 0 || !strings.HasPrefix(stdout, "Warn-only until 2026-11-12 00:00 UTC") || !strings.HasSuffix(stdout, "\nOver budget, warn-only: this run cost USD "+realTotal+" at list (List-equivalent, not billed), over the USD 0.001 run budget. After 2026-11-12 00:00 UTC this fails the check.\n") {
		t.Fatalf("exit %d:\n%s", code, stdout)
	}
}

func TestPolicyRefusesWhatItCannotRead(t *testing.T) {
	directory := t.TempDir()
	for name, args := range map[string][]string{
		"no rows flag":     {"policy"},
		"a missing rows":   {"policy", "--rows", filepath.Join(directory, "none.jsonl")},
		"a directory":      {"policy", "--rows", realRows, "--config", directory},
		"a bad start":      {"policy", "--rows", realRows, "--workflow-added", "yesterday"},
		"a bad check time": {"policy", "--rows", realRows, "--at", "2026-11-06"},
		"an argument":      {"policy", "--rows", realRows, "extra"},
	} {
		if code, _, _ := execute(t, args...); code != exitFailure {
			t.Errorf("%s: exit %d", name, code)
		}
	}
	code, stdout, stderr := execute(t, "policy", "--rows", realRows, "--config", writeBaseConfig(t, "policy:\n  run_budget_usd: 017\n"))
	if code != 0 || !strings.Contains(stdout, "is not valid") || !strings.Contains(stderr, "policy.run_budget_usd must be a plain decimal") {
		t.Errorf("exit %d:\n%s%s", code, stdout, stderr)
	}
}

// TestPolicyNamesTheFileItRead holds costroid policy to the costroid.yml it
// read: the working directory's by default, and a file --config names by its
// path. Only the report reads the base branch's.
func TestPolicyNamesTheFileItRead(t *testing.T) {
	rows, err := filepath.Abs(realRows)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	t.Chdir(directory)
	other := writeBaseConfig(t, "comment:\n  show_cost: true\n")
	for name, check := range map[string]struct {
		config string
		args   []string
		want   string
	}{
		"no file":               {"", nil, "No rule applied: `costroid.yml` in this directory sets no budget and no limit."},
		"a file with no policy": {"comment:\n  show_cost: true\n", nil, "No rule applied: `costroid.yml` in this directory sets no budget and no limit."},
		"an invalid file":       {"policy: [\n", nil, "No rule ran, so the check concludes neutral: `costroid.yml` in this directory is not valid"},
		"another file":          {"", []string{"--config", other}, "No rule applied: `" + other + "` sets no budget and no limit."},
	} {
		os.Remove(configFile)
		if check.config != "" {
			if err := os.WriteFile(configFile, []byte(check.config), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		code, stdout, stderr := execute(t, append([]string{"policy", "--rows", rows}, check.args...)...)
		if code != 0 || !strings.Contains(stdout, check.want) || strings.Contains(stdout, "base branch") {
			t.Errorf("%s: exit %d, want %q and no base branch:\n%s%s", name, code, check.want, stdout, stderr)
		}
	}
}

func TestAWithheldCommentStatesTheVerdictWithoutAmounts(t *testing.T) {
	stub := reportEnvironment(t, pullRequestEvent())
	stub.SetPrivate(false)
	stub.Files[configFile] = "comment:\n  show_cost: false\n" + overBudget
	if code, _, stderr := execute(t, "report", "--rows", realRows); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	body := stub.Comments[0].Body
	for _, amount := range []string{realTotal, "0.001", "limit_usd", "cost_usd"} {
		if strings.Contains(body, amount) {
			t.Errorf("the comment holds %q:\n%s", amount, body)
		}
	}
	if !strings.Contains(body, "**Over budget, warn-only: this run is over its run budget, priced at list (List-equivalent, not billed). After the warn-only period this fails the check. Amounts are withheld here.**\n") {
		t.Errorf("the comment does not state the verdict:\n%s", body)
	}
	check := checkVerdict(t, stub.Checks[0].Text)
	if check.Results[0].LimitUSD != "0.001" || check.Results[0].CostUSD != realTotal {
		t.Errorf("the check's block lost its amounts: %+v", check.Results)
	}
}

// TestAFailedVerdictReachesTheOutputsAndTheResult holds the conclusion to one
// value in the check, the conclusion output, the result output and --json.
func TestAFailedVerdictReachesTheOutputsAndTheResult(t *testing.T) {
	stub := reportEnvironment(t, pullRequestEvent())
	stub.Files[configFile] = overBudget + "  enforce: true\n"
	outputs, _ := stepFiles(t)
	code, stdout, stderr := execute(t, "report", "--rows", realRows, "--json")
	if code != 0 || len(stub.Checks) != 1 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	got := readOutputs(t, outputs)
	block := checkBlock(t, stub.Checks[0].Text)
	if stub.Checks[0].Conclusion != policy.Failure || got["conclusion"] != policy.Failure || got["result"] != compactJSON(t, block) || stdout != block+"\n" {
		t.Errorf("check %s, output %q, result %s", stub.Checks[0].Conclusion, got["conclusion"], got["result"])
	}
	if verdict := checkVerdict(t, stub.Checks[0].Text); verdict.Results[0].Outcome != policy.Fail {
		t.Errorf("%+v", verdict)
	}
	validateResult(t, block)
	validateResult(t, commentBlock(t, stub.Comments[0].Body))
}

func TestAnUnreadBaseConfigConcludesNeutralAndSaysWhy(t *testing.T) {
	stub := reportEnvironment(t, pullRequestEvent())
	stub.SetPrivate(true)
	stub.Files[configFile] = overBudget + "  enforce: true\n"
	stub.Fail["GET /contents/"] = http.StatusInternalServerError
	if code, _, stderr := execute(t, "report", "--rows", realRows); code != 0 || len(stub.Checks) != 1 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	verdict := checkVerdict(t, stub.Checks[0].Text)
	if stub.Checks[0].Conclusion != policy.Neutral || verdict.Config != policy.ConfigUnread || len(verdict.Results) != 0 {
		t.Errorf("posted %s with %+v", stub.Checks[0].Conclusion, verdict)
	}
	if want := "No rule ran, so the check concludes neutral: `costroid.yml` on the base branch could not be read"; !strings.Contains(stub.Comments[0].Body, want) {
		t.Errorf("the comment does not say why:\n%s", stub.Comments[0].Body)
	}
}
