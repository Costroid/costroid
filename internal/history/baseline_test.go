package history_test

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/history"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm/scmtest"
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

// mergedDay is the synthetic day the merged pull requests of these tests
// merged on.
var mergedDay = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

// headOf is the synthetic head commit of pull request number.
func headOf(number int) string {
	return fmt.Sprintf("%040x", 0xabc000+number)
}

// pullInto adds pull request number into base in state, merged at minute
// past mergedDay when merged is set, with its head commit and an earlier one.
func pullInto(stub *scmtest.Stub, number int, base, state string, merged bool, minute int) {
	pull := scmtest.Pull{
		Number: number, State: state, BaseBranch: base, HeadSHA: headOf(number), HeadRepository: "example/widgets",
		Commits: []string{fmt.Sprintf("%040x", 0xdef000+number), headOf(number)}, UpdatedAt: mergedDay.Add(time.Duration(minute) * time.Minute),
	}
	if merged {
		pull.MergedAt = pull.UpdatedAt
	}
	stub.Pulls = append(stub.Pulls, pull)
}

// ranOn posts a check run of id on the commit: a run of claude-code on its
// own rows, numbered id, at a synthetic USD cost.
func ranOn(t *testing.T, stub *scmtest.Stub, commit string, id int64, cost string) {
	t.Helper()
	result, err := comment.Result(comment.Input{
		State:      comment.Reported,
		Ledger:     comment.Ledger{Rows: 1, Agents: []comment.Data{"claude-code"}, Figures: []comment.Figure{figure("USD", "measured", cost)}},
		RowsSHA256: fmt.Sprintf("%064x", id),
	})
	if err != nil {
		t.Fatal(err)
	}
	stub.Checks = append(stub.Checks, scmtest.Check{ID: id, Name: comment.CheckName, HeadSHA: commit, Conclusion: "neutral", Text: "```json\n" + result + "\n```", App: "github-actions"})
}

// checkRunsOf are the check runs the reports came from, in order.
func checkRunsOf(reports []history.Report) []int64 {
	ids := make([]int64, len(reports))
	for index, report := range reports {
		ids[index] = report.CheckRun
	}
	slices.Sort(ids)
	return ids
}

// TestBaselineReadsMergedPullRequestsOnly holds the baseline to the check
// runs on the heads of the pull requests merged into the base branch: not a
// pull request closed unmerged, still open or merged into another branch, and
// not a merged pull request's earlier commit. Every run on a merged head
// counts, so a pull request whose agent ran twice adds two runs.
func TestBaselineReadsMergedPullRequestsOnly(t *testing.T) {
	stub := scmtest.New(t)
	pullInto(stub, 1, "main", "closed", true, 1)
	pullInto(stub, 2, "main", "closed", true, 2)
	pullInto(stub, 3, "main", "closed", false, 3)
	pullInto(stub, 4, "main", "open", false, 4)
	pullInto(stub, 5, "release", "closed", true, 5)
	ranOn(t, stub, headOf(1), 11, "1")
	ranOn(t, stub, headOf(2), 21, "2")
	ranOn(t, stub, headOf(2), 22, "3")
	ranOn(t, stub, stub.Pulls[1].Commits[0], 23, "4")
	ranOn(t, stub, headOf(3), 31, "5")
	ranOn(t, stub, headOf(4), 41, "6")
	ranOn(t, stub, headOf(5), 51, "7")
	reports, err := history.Merged(context.Background(), adapter(t, stub), "main", pullNumber, thirdCommit)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := checkRunsOf(reports), []int64{11, 21, 22}; !slices.Equal(got, want) {
		t.Errorf("the baseline read check runs %v, want %v", got, want)
	}
	if lists, heads := requestsWith(stub, "/pulls?"), requestsWith(stub, "/check-runs?"); len(lists) != 1 || len(heads) != 2 {
		t.Errorf("%d lists and %d heads read: %v", len(lists), len(heads), stub.Requests)
	}
	own := runReport(t, 0, 99, []comment.Data{"claude-code"}, figure("USD", "measured", "9"))
	costs, err := history.RunCosts(reports, own)
	if err != nil || len(costs) != 3 {
		t.Errorf("%d runs count, want each of the 3: %+v, %v", len(costs), costs, err)
	}
}

// TestBaselineReadsTheLatestMergesInOneRequestEach reads the latest 30 merges
// of 35 in 31 requests: one list, and one for each head.
func TestBaselineReadsTheLatestMergesInOneRequestEach(t *testing.T) {
	stub := scmtest.New(t)
	for number := 1; number <= 35; number++ {
		pullInto(stub, number, "main", "closed", true, number)
		ranOn(t, stub, headOf(number), int64(number), "1")
	}
	reports, err := history.Merged(context.Background(), adapter(t, stub), "main", 0, thirdCommit)
	if err != nil {
		t.Fatal(err)
	}
	if got := checkRunsOf(reports); len(got) != history.MergedWindow || got[0] != 6 || got[len(got)-1] != 35 {
		t.Errorf("the baseline read check runs %v, want 6 to 35", got)
	}
	if len(stub.Requests) != 1+history.MergedWindow {
		t.Errorf("%d requests, want %d: %v", len(stub.Requests), 1+history.MergedWindow, stub.Requests)
	}
}

// TestBaselineNeverReadsThePullRequestsOwnRuns leaves out the pull request
// the verdict is about when the list names it merged, and any merged pull
// request whose head is its head, whose check runs are its own.
func TestBaselineNeverReadsThePullRequestsOwnRuns(t *testing.T) {
	stub := scmtest.New(t)
	pullInto(stub, pullNumber, "main", "closed", true, 1)
	pullInto(stub, 8, "main", "closed", true, 2)
	stub.Pulls[1].HeadSHA = thirdCommit
	pullInto(stub, 9, "main", "closed", true, 3)
	ranOn(t, stub, headOf(pullNumber), 71, "1")
	ranOn(t, stub, thirdCommit, 81, "1")
	ranOn(t, stub, headOf(9), 91, "1")
	reports, err := history.Merged(context.Background(), adapter(t, stub), "main", pullNumber, thirdCommit)
	if err != nil {
		t.Fatal(err)
	}
	if got := checkRunsOf(reports); !slices.Equal(got, []int64{91}) {
		t.Errorf("the baseline read check runs %v, want only 91", got)
	}
}

// TestAPartialHistoryIsNoBaseline fails the whole read when any part of it
// cannot be read: the list, one head's check runs, a costroid check run whose
// object this release does not read, or a head whose check runs one answer
// does not hold. A baseline of the rest would hold other runs than the
// latest.
func TestAPartialHistoryIsNoBaseline(t *testing.T) {
	cases := map[string]func(*testing.T, *scmtest.Stub){
		"the list is refused": func(_ *testing.T, stub *scmtest.Stub) {
			stub.Fail["GET /repos/example/widgets/pulls"] = http.StatusBadGateway
		},
		"a head's check runs are refused": func(_ *testing.T, stub *scmtest.Stub) {
			stub.Fail["GET /repos/example/widgets/commits/"+headOf(2)+"/check-runs"] = http.StatusBadGateway
		},
		"a check run holds an object this release does not read": func(t *testing.T, stub *scmtest.Stub) {
			posted(stub, headOf(2), withSchema(checkText(t, figure("USD", "measured", "1")), "costroid.result/0.2"))
		},
		"a check run holds no object": func(_ *testing.T, stub *scmtest.Stub) {
			posted(stub, headOf(2), "the check run's text")
		},
		"a head holds more check runs than one answer": func(t *testing.T, stub *scmtest.Stub) {
			for id := range int64(100) {
				ranOn(t, stub, headOf(2), 1000+id, "1")
			}
		},
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			stub := scmtest.New(t)
			for number := 1; number <= 3; number++ {
				pullInto(stub, number, "main", "closed", true, number)
				ranOn(t, stub, headOf(number), int64(number), "1")
			}
			breakIt(t, stub)
			if reports, err := history.Merged(context.Background(), adapter(t, stub), "main", pullNumber, thirdCommit); err == nil {
				t.Errorf("a partial history read %d reports", len(reports))
			}
		})
	}
}
