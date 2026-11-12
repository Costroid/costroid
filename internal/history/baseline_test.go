package history_test

import (
	"cmp"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/history"
	"github.com/Costroid/costroid/internal/policy"
)

// runReport is a report on check run id of rows with the digest numbered
// digest, 0 for none, by the agents, at the figures.
func runReport(t *testing.T, id int64, digest int, agents []comment.Data, figures ...comment.Figure) history.Report {
	t.Helper()
	input := comment.Input{State: comment.Reported, Ledger: comment.Ledger{Rows: len(figures), Figures: figures, Agents: agents}}
	if digest > 0 {
		input.RowsSHA256 = fmt.Sprintf("%064x", digest)
	}
	result, err := comment.Result(input)
	if err != nil {
		t.Fatal(err)
	}
	report, err := history.Parse([]byte(result))
	if err != nil {
		t.Fatal(err)
	}
	report.CheckRun = id
	return report
}

func TestRunCostsCountEachRunOfTheAgentOnce(t *testing.T) {
	claude := []comment.Data{"claude-code"}
	own := runReport(t, 0, 1, claude, figure("USD", "measured", "9"))
	reports := []history.Report{
		runReport(t, 20, 2, claude, figure("USD", "measured", "1"), figure("USD", "list_equivalent", "0.5")),
		runReport(t, 10, 2, claude, figure("USD", "measured", "1"), figure("USD", "list_equivalent", "0.5")),
		runReport(t, 5, 1, claude, figure("USD", "measured", "9")),
		runReport(t, 30, 0, claude, figure("USD", "measured", "7")),
		runReport(t, 40, 3, []comment.Data{"codex"}, figure("USD", "measured", "7")),
		runReport(t, 50, 4, []comment.Data{"claude-code", "codex"}, figure("USD", "measured", "7")),
		runReport(t, 60, 5, claude, figure("USD", "measured", "7"), figure("EUR", "measured", "7")),
		runReport(t, 70, 6, claude, figure("EUR", "measured", "3")),
		runReport(t, 80, 7, claude),
	}
	costs, err := history.RunCosts(reports, own)
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(costs, func(a, b policy.RunCost) int { return cmp.Compare(a.CheckRun, b.CheckRun) })
	want := []policy.RunCost{
		{CheckRun: 10, Currency: "USD", Cost: decimal.MustParse("1.5")},
		{CheckRun: 70, Currency: "EUR", Cost: decimal.MustParse("3")},
	}
	if len(costs) != len(want) {
		t.Fatalf("%+v, want %+v", costs, want)
	}
	for index := range want {
		if costs[index].CheckRun != want[index].CheckRun || costs[index].Currency != want[index].Currency || costs[index].Cost.Cmp(want[index].Cost) != 0 {
			t.Errorf("%+v, want %+v", costs[index], want[index])
		}
	}
	for name, agents := range map[string][]comment.Data{"no agent": nil, "two agents": {"claude-code", "codex"}} {
		costs, err := history.RunCosts(reports, runReport(t, 0, 1, agents, figure("USD", "measured", "9")))
		if err != nil || !reflect.DeepEqual(costs, []policy.RunCost(nil)) {
			t.Errorf("this run with %s read %+v, %v", name, costs, err)
		}
	}
}
