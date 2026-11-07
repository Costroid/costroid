package comment

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/reconcile"
)

// codexStoppedLedger reads the rows of the recorded Codex run that a 0.001 USD
// cap stopped: one request of 5632 uncached input, 11264 cached input and 64
// output tokens, priced at the catalog's gpt-5.6-sol rows per million as
// 5632 × 4.00 + 11264 × 0.40 + 64 × 20.00, which is 0.0283136 USD.
func codexStoppedLedger(t *testing.T) Ledger {
	t.Helper()
	file, err := os.Open(filepath.Join("testdata", "codex-stopped.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	ledger, err := ReadRows(file)
	if err != nil {
		t.Fatal(err)
	}
	return ledger
}

func capInput(ledger Ledger, budget string) Input {
	amount := decimal.MustParse(budget)
	return Input{Ledger: ledger, State: Reported, Budget: &amount, Amounts: AmountsShown}
}

var capLinePattern = regexp.MustCompile(`(?m)^(Cap: |No cap ).*$`)

func capLineOf(t *testing.T, input Input) string {
	t.Helper()
	body := commentOf(t, input)
	line := capLinePattern.FindString(body)
	if line == "" {
		t.Fatalf("no cap line in\n%s", body)
	}
	return line
}

func TestCapLineStatesTheCodexOvershootInUSDAndTokens(t *testing.T) {
	input := capInput(codexStoppedLedger(t), "0.001")
	want := "Cap: USD 0.001 (wrapper-watchdog: the wrapper stops the process at the cap). Cost: USD 0.0283136, Measured. Over the cap by USD 0.0273136 and 16960 tokens."
	if line := capLineOf(t, input); line != want {
		t.Errorf("cap line\n%s\nwant\n%s", line, want)
	}
	output, err := Check(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"cost": "0.0283136"`, `"remainder": "0"`, `"overshoot": "0.0273136"`, `"overshoot_tokens": "16960"`, `"enforcement_grade": "wrapper-watchdog"`} {
		if !strings.Contains(output.Text, field) {
			t.Errorf("no %s in\n%s", field, output.Text)
		}
	}
}

func TestCapLineSaysAnAgentFlagCapIsAClientSideEstimate(t *testing.T) {
	line := capLineOf(t, realInput(t))
	want := "Cap: USD 5 (agent-flag: client-side estimate, can pass the cap). Cost: USD 0.001619725, List-equivalent, not billed. Under the cap by USD 4.998380275."
	if line != want {
		t.Errorf("cap line\n%s\nwant\n%s", line, want)
	}
}

// TestCapLineCountsTheTokensFromTheRequestThatCrossedTheCap puts the cap
// between the recorded run's requests. The first ends at 11:15:15 and costs
// USD 0.00079059. The second, ending at 11:15:16, takes the total past 0.001
// with 3501 tokens, and two more follow with 6701 and 7255.
func TestCapLineCountsTheTokensFromTheRequestThatCrossedTheCap(t *testing.T) {
	line := capLineOf(t, capInput(realLedger(t), "0.001"))
	if want := "Over the cap by USD 0.000619725 and 17457 tokens."; !strings.HasSuffix(line, want) {
		t.Errorf("cap line\n%s\nwant it to end\n%s", line, want)
	}
}

func TestCapLineShowsNoCapForARunNothingHeld(t *testing.T) {
	none := sampleFields()
	none.enforcement = "none"
	line := capLineOf(t, capInput(ledgerOf(t, none), "5"))
	if line != "No cap held this run: its enforcement grade is none." {
		t.Errorf("cap line %q", line)
	}
}

func TestCapLineNeverShowsAGradeStrongerThanTheWeakest(t *testing.T) {
	watched, flagged := sampleFields(), sampleFields()
	flagged.enforcement = "agent-flag"
	line := capLineOf(t, capInput(ledgerOf(t, watched, flagged), "5"))
	if !strings.HasPrefix(line, "Cap: USD 5 (agent-flag: client-side estimate, can pass the cap). Cost: USD 0.2, Measured.") {
		t.Errorf("cap line %q", line)
	}
}

func TestCapLineNeverStatesACapInTurns(t *testing.T) {
	for _, input := range []Input{capInput(codexStoppedLedger(t), "0.001"), realInput(t), capInput(realLedger(t), "0.001")} {
		if line := capLineOf(t, input); strings.Contains(strings.ToLower(line), "turn") {
			t.Errorf("cap line %q", line)
		}
	}
}

func TestCapLineComparesNothingAcrossTwoBases(t *testing.T) {
	measured, listed := sampleFields(), sampleFields()
	listed.basis = "list_equivalent"
	input := capInput(ledgerOf(t, measured, listed), "5")
	line := capLineOf(t, input)
	if strings.Contains(line, "Cost:") || strings.Contains(line, "0.2") {
		t.Errorf("cap line %q", line)
	}
	output, err := Check(input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.Text, `"cost": null`) {
		t.Errorf("the block compares a cost:\n%s", output.Text)
	}
}

func TestCapLineLeavesTheReconciliationOutUntilThereIsOne(t *testing.T) {
	input := realInput(t)
	if body := commentOf(t, input); strings.Contains(body, "reconciliation") {
		t.Errorf("a reconciliation with no data:\n%s", body)
	}
	input.Reconciliations = []Reconciliation{
		{Credential: Clean("apikey_01Example"), Day: "2026-11-05", State: reconcile.Matched},
		{Credential: Clean("apikey_02Example"), Day: "2026-11-05", State: "settled"},
		{Credential: Clean("apikey_03Example"), Day: "5 November", State: reconcile.Pending},
	}
	body := commentOf(t, input)
	want := "Last reconciliation of `apikey_01Example`: 2026-11-05, tokens matched the provider's report within 1%."
	if !strings.Contains(body, want) {
		t.Errorf("no %q in\n%s", want, body)
	}
	if strings.Contains(body, "apikey_02Example") || strings.Contains(body, "apikey_03Example") {
		t.Errorf("a reconciliation outside the closed form is shown:\n%s", body)
	}
}

func TestCapLineNamesTheGradeOfACapTheReportWasNotGiven(t *testing.T) {
	input := realInput(t)
	input.Budget = nil
	want := "Cap: held by agent-flag (client-side estimate, can pass the cap). The rows do not record its amount, and the report was not given it."
	if line := capLineOf(t, input); line != want {
		t.Errorf("cap line\n%s\nwant\n%s", line, want)
	}
	none := sampleFields()
	none.enforcement = "none"
	if line := capLineOf(t, Input{Ledger: ledgerOf(t, none), State: Reported, Amounts: AmountsShown}); line != "No cap held this run: its enforcement grade is none." {
		t.Errorf("cap line %q", line)
	}
}

// policyBudgetNoCap names the golden state of the recorded run under a USD 5
// policy budget, by a report that was not given the run's cap.
const policyBudgetNoCap = "policy-budget-no-cap"

// policyBudgetWithoutCap is the input of the policyBudgetNoCap state.
func policyBudgetWithoutCap(t *testing.T) Input {
	t.Helper()
	input := exampleInput(t)
	input.Budget = nil
	return input
}

// TestPolicyBudgetBesideNoCap renders the policy's budget beside every kind
// of cap line. TestGolden holds the case of a policy budget and no cap to its
// golden. The cap and the budget never share a name, and no line says a
// budget is missing while the verdict checks one.
func TestPolicyBudgetBesideNoCap(t *testing.T) {
	golden := goldenStates(t)[policyBudgetNoCap]
	body := commentOf(t, golden)
	if !strings.Contains(body, `"cap": null`) || !strings.Contains(body, `"limit_usd": "5"`) {
		t.Errorf("the block does not hold the two apart:\n%s", body)
	}
	five := decimal.MustParse("5")
	for _, grade := range []string{"agent-flag", "none"} {
		for _, budget := range []*decimal.Decimal{nil, &five} {
			for _, verdict := range []*policy.Verdict{nil, exampleInput(t).Verdict} {
				input := golden
				input.Budget, input.Verdict = budget, verdict
				fields := sampleFields()
				fields.enforcement = grade
				input.Ledger = ledgerOf(t, fields)
				name := fmt.Sprintf("grade %s, cap %v, policy %v", grade, budget != nil, verdict != nil)
				assertNoContradiction(t, name, visiblePart(commentOf(t, input)), grade, verdict != nil)
			}
		}
	}
}

func assertNoContradiction(t *testing.T, name, text, grade string, policyBudget bool) {
	t.Helper()
	budgetLine := "Run budget: USD 5, from `policy.run_budget_usd` in `costroid.yml`."
	if strings.Contains(text, budgetLine) != policyBudget {
		t.Errorf("%s: the policy's budget line is %v:\n%s", name, !policyBudget, text)
	}
	if regexp.MustCompile(`(?i)budget: not|no budget|not recorded`).MatchString(text) {
		t.Errorf("%s: a budget reads as missing:\n%s", name, text)
	}
	if strings.Contains(text, "never the cap") && grade == "none" {
		t.Errorf("%s: a run nothing held is told of a cap:\n%s", name, text)
	}
	if regexp.MustCompile(`(?m)^Budget|^Cap: .*budget`).MatchString(text) {
		t.Errorf("%s: the cap line names a budget:\n%s", name, text)
	}
}
