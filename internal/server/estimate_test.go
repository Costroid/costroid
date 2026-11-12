package server

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/focusfile"
	"github.com/Costroid/costroid/internal/store"
)

// estimateBody is the rows costroid ingest-run --from focus writes in the
// fixtures' pull request for a FOCUS cost file of one resource in USD.
func estimateBody(t testing.TB, figure string) []byte {
	t.Helper()
	read, err := focusfile.Read(strings.NewReader(
		"BillingCurrency,ChargePeriodStart,ChargePeriodEnd,ListCost,ResourceName,ServiceName,SubAccountId\n"+
			"USD,2026-11-01T00:00:00Z,2026-12-01T00:00:00Z,"+figure+",aws_instance.web,Amazon EC2,example-account-a\n"),
		focusfile.Options{Tags: map[string]string{store.TagRepo: acmeRepo, store.TagPR: acmePull, store.TagCommitSHA: acmeHead, store.TagWorkflowRunID: acmeRun}})
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := focus.NewWriter(&body)
	for _, row := range read.Rows {
		if err := writer.Write(row); err != nil {
			t.Fatal(err)
		}
	}
	return body.Bytes()
}

func TestServeStoresAnEstimateRowAndKeepsItOutOfAgentBudgets(t *testing.T) {
	w, ledger := memoryWorld(t)
	agent := row("req-estimate-beside", focus.Output, 1000000)
	w.rows(w.token(), append(lines(t, agent), estimateBody(t, "1000")...)).want(t, http.StatusOK)

	var estimate *store.Record
	for _, record := range ledger.stored(t, tenantA) {
		if record.Row.Tags[focus.TagCostBasis] == focusfile.Basis {
			estimate = &record
		}
	}
	if estimate == nil {
		t.Fatal("the ledger stored no estimate row")
	}
	row := estimate.Row
	for name, got := range map[string]string{
		"ListCost": row.ListCost.String(), "EffectiveCost": row.EffectiveCost.String(), "BilledCost": row.BilledCost.String(),
		"cost_source": row.Tags[focus.TagCostSource], "run_identity": row.Tags[focus.TagRunIdentity],
		"enforcement_grade": row.Tags[focus.TagEnforcementGrade], "repo": row.Tags[store.TagRepo], "pr": row.Tags[store.TagPR],
	} {
		want := map[string]string{
			"ListCost": "1000", "EffectiveCost": "0", "BilledCost": "0", "cost_source": "infrastructure",
			"run_identity": identityToken, "enforcement_grade": enforcementNone, "repo": acmeRepo, "pr": acmePull,
		}[name]
		if got != want {
			t.Errorf("%s reads %q, want %q", name, got, want)
		}
	}
	if _, named := row.Tags[focus.TagAgent]; named || row.Tags[focus.TagModel] != "" {
		t.Errorf("the estimate names an agent or a model: %v", row.Tags)
	}
	if err := row.Check(); err != nil {
		t.Error(err)
	}

	lines := []store.Line{
		{Currency: "USD", Agent: "claude-code", CostBasis: "measured", CostSource: "agent_stream", AttributionGrade: gradeExact,
			RunIdentity: identityToken, BudgetEligible: true, Rows: 1, ListCost: decimal.MustParse("0.4")},
		{Currency: "USD", CostBasis: focusfile.Basis, CostSource: "infrastructure", AttributionGrade: gradeWindowed,
			RunIdentity: identityToken, BudgetEligible: true, Rows: 1, ListCost: decimal.MustParse("1000")},
	}
	budget := budgetOf(lines, measuredSpend{})
	if len(budget.Spend) != 1 || budget.Spend[0].ListCost != "0.4" {
		t.Errorf("the budget reads %+v", budget)
	}
	if pack := ledgerLines(lines); len(pack) != 1 || pack[0].ListCost.String() != "0.4" {
		t.Errorf("the verdict reads %+v", pack)
	}
	totals := totalsOf(lines)
	if len(totals) != 2 || totals[1].CostBasis != focusfile.Basis || totals[1].Label != "Estimate" || totals[1].ListCost != "1000" {
		t.Errorf("the view's totals read %+v", totals)
	}
}

func TestServeRefusesAnEstimateThatNamesAnAgentOrANegativeFigure(t *testing.T) {
	w, _ := memoryWorld(t)
	named := bytes.Replace(estimateBody(t, "1"), []byte(`"Tags":{`), []byte(`"Tags":{"costroid/agent":"claude-code",`), 1)
	if code := w.rows(w.token(), named).want(t, http.StatusUnprocessableEntity).code(t); code != codeInvalidRow {
		t.Errorf("an estimate that names an agent: %s", code)
	}
	negative := bytes.Replace(estimateBody(t, "1"), []byte(`"ListCost":"1"`), []byte(`"ListCost":"-1"`), 1)
	if code := w.rows(w.token(), negative).want(t, http.StatusUnprocessableEntity).code(t); code != codeInvalidRow {
		t.Errorf("a negative estimate: %s", code)
	}
}

func TestPostgresStoresAnEstimateApartFromTheAgentsSpend(t *testing.T) {
	w := postgresWorld(t)
	w.rows(w.token(), append(readFile(t, goldenRows), estimateBody(t, "1000")...)).want(t, http.StatusOK)
	view := w.view(t, acmeKey, acmeRepo, acmePull)
	if view.Budget.Basis != budgetBasisAgent || view.Budget.Spend[0].ListCost != goldenTotal {
		t.Fatalf("the estimate moved the budget: %+v", view.Budget)
	}
	found := false
	for _, total := range view.Totals {
		found = found || (total.CostBasis == focusfile.Basis && total.ListCost == "1000" && total.Label == "Estimate")
	}
	if !found {
		t.Errorf("the view states no estimate apart: %+v", view.Totals)
	}
}
