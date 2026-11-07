package policy

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"testing"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

// recordedRows are rows files costroid run wrote from recordings.
var recordedRows = []string{
	filepath.Join("..", "comment", "testdata", "rows.jsonl"),
	filepath.Join("..", "comment", "testdata", "codex-stopped.jsonl"),
}

// seedRows adds every recorded rows file whole, and each of its lines alone.
func seedRows(f *testing.F) {
	f.Helper()
	for _, path := range recordedRows {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
		for _, line := range bytes.Split(data, []byte("\n")) {
			f.Add(line)
		}
	}
	f.Add([]byte(`{"BillingCurrency":"USD","ListCost":"-1"}` + "\n"))
	f.Add([]byte(`{"BillingCurrency":"USD","ListCost":"1","Tags":{"costroid/model":7}}` + "\n\n" + `{"BillingCurrency":"EUR","ListCost":"0.000000000001"}`))
}

// fuzzConfigs are costroid.yml files that set every rule, with the edges the
// schema draws.
var fuzzConfigs = []string{
	"policy:\n  run_budget_usd: \"5\"\n  max_run_cost_usd: 4\n  max_increase_usd: \"5\"\n  max_increase_percent: 500\n  pr_budget_usd: 50\n  models: [\"claude-*\"]\n  enforce: true\n",
	"policy:\n  run_budget_usd: \"0.000000000001\"\n  monthly_budget_usd: 18446744073709551615\n",
	"policy:\n  run_budget_usd: 5.0\n",
	"policy:\n  models: [codex, codex]\n",
	"comment:\n  show_cost: false\npolicy:\n  enforce: false\napproval:\n  allow_author: true\n  expires_days: 90\n",
	"policy: &a [*a]\n",
	"",
}

// budgetRules are the rules a cost is held to. On rows the agent's job wrote,
// none of them passes, whatever the rows claim.
var budgetRules = []string{
	RuleRunBudget, RuleRunCostThreshold, RuleRunIncrease, RuleRunIncreasePercent,
	RulePullRequestBudget, RuleMonthlyBudget,
}

var rowsLinePattern = regexp.MustCompile(`^policy: rows line ([0-9]+)`)

// assertUsage holds what ReadUsage returned to the rows it read: the counts
// add up and none is negative, each currency's sum is the exact sum of its
// rows, the currencies are distinct and sorted, and every row is
// unattributed.
func assertUsage(t *testing.T, data []byte, usage Usage) {
	t.Helper()
	rows := 0
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSuffix(line, []byte("\r"))) > 0 {
			rows++
		}
	}
	if usage.Rows != rows {
		t.Fatalf("%d rows read from %d lines that hold one", usage.Rows, rows)
	}
	counted := 0
	for index, entry := range usage.Currencies {
		if index > 0 && usage.Currencies[index-1].Currency >= entry.Currency {
			t.Fatalf("currencies out of order or repeated: %+v", usage.Currencies)
		}
		if !currencyPattern.MatchString(entry.Currency) || entry.ListCost.Sign() < 0 {
			t.Fatalf("currency %q, sum %s", entry.Currency, entry.ListCost)
		}
		named := 0
		for model, count := range entry.Models {
			if count < 1 || !modelPattern.MatchString(model) {
				t.Fatalf("model %q counted %d times", model, count)
			}
			named += count
		}
		if entry.UnattributedRows != entry.Rows || entry.JoinableRows < 0 || entry.JoinableRows > entry.Rows ||
			entry.UnreadableModels < 0 || named+entry.UnreadableModels != entry.Rows {
			t.Fatalf("%s: %d rows, %d unattributed, %d joinable, %d named, %d unreadable",
				entry.Currency, entry.Rows, entry.UnattributedRows, entry.JoinableRows, named, entry.UnreadableModels)
		}
		counted += entry.Rows
	}
	if counted != usage.Rows {
		t.Fatalf("the currencies hold %d rows of %d", counted, usage.Rows)
	}
}

// assertRefusalNamesALine holds a refused rows file to an error that names a
// line the file has.
func assertRefusalNamesALine(t *testing.T, data []byte, err error) {
	t.Helper()
	match := rowsLinePattern.FindStringSubmatch(err.Error())
	if match == nil {
		t.Fatalf("the refusal names no line: %v", err)
	}
	line, _ := strconv.Atoi(match[1])
	if line < 1 || line > bytes.Count(data, []byte("\n"))+1 {
		t.Fatalf("the refusal names line %d of a file of %d lines: %v", line, bytes.Count(data, []byte("\n"))+1, err)
	}
}

// assertUnverifiedNeverPasses evaluates rows the job wrote and holds the
// verdict to L51's rule: it never concludes success, no budget rule passes,
// and every outcome and reason is one the pack states.
func assertUnverifiedNeverPasses(t *testing.T, usage Usage, config []byte) {
	t.Helper()
	verdict, err := Evaluate(context.Background(), Request{
		Usage: usage, Trust: Unverified, Config: ConfigFile{Data: config, Found: true, FromBase: true}, At: checkTime,
		Baseline: &Baseline{Runs: 12, MedianUSD: decimal.MustParse("1"), Trust: TokenVerified},
		Lifetime: &Lifetime{USD: decimal.MustParse("2"), Trust: TokenVerified},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains([]string{Neutral, Failure}, verdict.Conclusion) {
		t.Fatalf("rows the job wrote concluded %s: %+v", verdict.Conclusion, verdict)
	}
	for _, result := range verdict.Results {
		if slices.Contains(budgetRules, result.Rule) && result.Outcome == Pass {
			t.Fatalf("rows the job wrote passed %s: %+v", result.Rule, result)
		}
		if !slices.Contains([]string{Pass, Warn, Fail, Unproven}, result.Outcome) || result.Reason == "" {
			t.Fatalf("a result outside the pack's words: %+v", result)
		}
	}
	if verdict.ConfigProblem() != nil && (verdict.Conclusion != Neutral || len(verdict.Results) != 0) {
		t.Fatalf("settings that could not be read ran a rule: %+v", verdict)
	}
}

// FuzzReadUsage reads a rows file the agent's job wrote. A refusal names a
// line the file has; a file read is counted exactly, and the pack never
// passes a budget on it.
func FuzzReadUsage(f *testing.F) {
	seedRows(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		usage, err := ReadUsage(bytes.NewReader(data))
		if err != nil {
			assertRefusalNamesALine(t, data, err)
			return
		}
		assertUsage(t, data, usage)
		assertUnverifiedNeverPasses(t, usage, []byte(fuzzConfigs[0]))
	})
}

// FuzzParseSettings reads a costroid.yml. What it accepts is what the schema
// states: amounts plain, non-negative and of at most twelve fractional
// digits, models distinct and printable. An unreadable file runs no rule,
// and no file makes rows the job wrote pass a budget.
func FuzzParseSettings(f *testing.F) {
	for _, config := range fuzzConfigs {
		f.Add([]byte(config))
	}
	rows, err := os.ReadFile(recordedRows[0])
	if err != nil {
		f.Fatal(err)
	}
	usage, err := ReadUsage(bytes.NewReader(rows))
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		settings, err := ParseSettings(data)
		if err == nil {
			for key, amount := range settings.Amounts {
				if !slices.Contains(amountKeys, key) || amount.Sign() < 0 || amount.Scale() > focus.MaxScale {
					t.Fatalf("policy.%s read as %s", key, amount)
				}
			}
			for index, model := range settings.Models {
				if len(model) > maxModelLength || !modelEntryPattern.MatchString(model) || slices.Contains(settings.Models[:index], model) {
					t.Fatalf("policy.models read as %q", settings.Models)
				}
			}
		}
		approval, err := ParseApproval(data)
		if err == nil && (approval.ExpiresDays < 1 || approval.ExpiresDays > maxApprovalDays) {
			t.Fatalf("approval.expires_days read as %d", approval.ExpiresDays)
		}
		assertUnverifiedNeverPasses(t, usage, data)
	})
}
