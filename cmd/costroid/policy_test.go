package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/policy"
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

func TestPolicyExitsOneOnFailure(t *testing.T) {
	config := writeBaseConfig(t, overBudget)
	code, stdout, stderr := execute(t, "policy", "--rows", realRows, "--config", config,
		"--workflow-added", "2026-10-01T00:00:00Z", "--at", "2026-11-06T00:00:00Z")
	if code != exitRejected || !strings.HasPrefix(stdout, "Verdict: failure.\n") || !strings.Contains(stdout, "USD "+realTotal+" is over the USD 0.001 budget.") {
		t.Fatalf("exit %d:\n%s%s", code, stdout, stderr)
	}
	code, stdout, _ = execute(t, "policy", "--rows", realRows, "--config", config,
		"--workflow-added", "2026-11-05T00:00:00Z", "--at", "2026-11-06T00:00:00Z")
	if code != 0 || !strings.HasPrefix(stdout, "Verdict: neutral.\n") || !strings.Contains(stdout, "Warn-only until 2026-11-12 00:00 UTC") {
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
	code, stdout, stderr := execute(t, "policy", "--rows", realRows, "--config", writeBaseConfig(t, "policy:\n  run_budget_usd: 5\n"))
	if code != 0 || !strings.Contains(stdout, "is not valid") || !strings.Contains(stderr, "policy.run_budget_usd must be a quoted decimal") {
		t.Errorf("exit %d:\n%s%s", code, stdout, stderr)
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
	if !strings.Contains(body, "Run budget: the run is over the budget.") {
		t.Errorf("the comment does not state the verdict:\n%s", body)
	}
	check := checkVerdict(t, stub.Checks[0].Text)
	if check.Results[0].LimitUSD != "0.001" || check.Results[0].CostUSD != realTotal {
		t.Errorf("the check's block lost its amounts: %+v", check.Results)
	}
}
