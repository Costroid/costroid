package policy

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/focusfile"
)

// estimateRows are the rows costroid ingest-run writes for a FOCUS cost file
// of two resources, one in USD and one in EUR.
func estimateRows(t *testing.T) string {
	t.Helper()
	file, err := focusfile.Read(strings.NewReader(
		"BillingCurrency,ChargePeriodStart,ChargePeriodEnd,ListCost,ResourceName\n"+
			"USD,2026-12-01T00:00:00Z,2027-01-01T00:00:00Z,1000,aws_instance.web\n"+
			"EUR,2026-12-01T00:00:00Z,2027-01-01T00:00:00Z,25.488,azurerm_linux_virtual_machine.web\n"),
		focusfile.Options{Tags: map[string]string{focus.TagPR: "7", focus.TagCommitSHA: head}})
	if err != nil {
		t.Fatal(err)
	}
	var rows bytes.Buffer
	writer := focus.NewWriter(&rows)
	for _, row := range file.Rows {
		if err := writer.Write(row); err != nil {
			t.Fatal(err)
		}
	}
	return rows.String()
}

func TestInfrastructureRowsNeverReachAnAgentBudget(t *testing.T) {
	agent := map[string]string{"costroid/agent": "codex", "costroid/model": "gpt-5.5", "costroid/cost_source": "agent_stream"}
	both := rowsOf(t, line{"USD", "0.4", "key-1", agent}) + estimateRows(t)
	usage, err := ReadUsage(strings.NewReader(both))
	if err != nil {
		t.Fatal(err)
	}
	if usage.Rows != 1 || len(usage.Currencies) != 1 || usage.Currencies[0].ListCost.String() != "0.4" {
		t.Fatalf("the usage holds an estimate: %+v", usage)
	}

	// Rows the agent's job wrote fail a limit they pass and never satisfy one,
	// so a USD 1000 estimate read as agent spend would fail both rules here.
	limits := config("policy:\n  run_budget_usd: \"0.5\"\n  max_run_cost_usd: \"0.5\"\n  models: [\"gpt-*\"]\n  enforce: true\n")
	raised := evaluate(t, Request{Usage: usage, Trust: Unverified, Config: limits})
	if raised.Conclusion == Failure {
		t.Errorf("an agent row of USD 0.4 beside a USD 1000 estimate concluded failure: %+v", raised)
	}
	for _, rule := range []string{RuleRunBudget, RuleRunCostThreshold} {
		if got := result(t, raised, rule); got.Outcome == Fail || got.CostUSD != "0.4" {
			t.Errorf("an agent row of USD 0.4 beside a USD 1000 estimate: %s reads %+v", rule, got)
		}
	}
	if got := result(t, raised, RuleModelAllowlist); got.Outcome == Fail || len(got.Models) != 0 {
		t.Errorf("an estimate, which names no model, reached the model list: %+v", got)
	}

	alone, err := ReadUsage(strings.NewReader(estimateRows(t)))
	if err != nil {
		t.Fatal(err)
	}
	satisfied := evaluate(t, Request{Usage: alone, Trust: TokenVerified, Evidence: GatewayMeasured, Config: limits})
	if got := result(t, satisfied, RuleRunBudget); alone.Rows != 0 || got.Outcome != Unproven || got.Reason != ReasonNoRows {
		t.Errorf("estimates alone: %d rows, the budget reads %+v", alone.Rows, got)
	}
}

func TestARowIsAnAgentsWhenItNamesOneOrStatesNoSource(t *testing.T) {
	cases := []struct {
		tags string
		want bool
	}{
		{`{"costroid/agent":"codex","costroid/cost_source":"agent_stream"}`, true},
		{`{"costroid/cost_basis":"measured"}`, true},
		{`{}`, true},
		{`{"costroid/cost_source":"infrastructure"}`, false},
		{`{"costroid/cost_source":"infrastructure","costroid/agent":""}`, false},
		{`{"costroid/cost_source":"infrastructure","costroid/agent":7}`, false},
	}
	for _, tc := range cases {
		var tags focus.Tags
		if err := json.Unmarshal([]byte(tc.tags), &tags); err != nil {
			t.Fatal(err)
		}
		if got := FromAgent(tags); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.tags, got, tc.want)
		}
	}
}
