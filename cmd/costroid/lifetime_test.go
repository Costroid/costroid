package main

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm/scmtest"
)

const (
	earlierCommit = "4d9f1e2a63b57c80d2e4f6a8102c3d4e5f6a7b8c"
	// lifetimeLine is the rows' own USD 0.001619725 and the earlier check
	// run's synthetic USD 0.002, added up.
	lifetimeLine = "Across this pull request: USD 0.003619725 at list (List-equivalent, not billed), from 2 reports, which may include a repeated report."
)

// pullWithAnEarlierRun gives pull request 7 an earlier commit whose check run
// reports a synthetic USD 0.002 at the rows' basis.
func pullWithAnEarlierRun(t *testing.T, stub *scmtest.Stub) {
	t.Helper()
	earlier := comment.Figure{Currency: "USD", Basis: "list_equivalent", Attribution: "unattributed", Enforcement: "agent-flag", Cost: decimal.MustParse("0.002"), Rows: 1}
	output, err := comment.Check(comment.Input{State: comment.Reported, Ledger: comment.Ledger{Rows: 1, Figures: []comment.Figure{earlier}}})
	if err != nil {
		t.Fatal(err)
	}
	stub.Pulls = []scmtest.Pull{{Number: 7, State: "open", HeadSHA: headCommit, Commits: []string{earlierCommit, headCommit}}}
	stub.Checks = append(stub.Checks, scmtest.Check{ID: 77, Name: comment.CheckName, HeadSHA: earlierCommit, Conclusion: "neutral", Text: output.Text, App: "github-actions"})
}

func TestLifetimeWithheld(t *testing.T) {
	t.Run("shown on a private repository", func(t *testing.T) {
		stub := reportEnvironment(t, pullRequestEvent())
		pullWithAnEarlierRun(t, stub)
		stub.Files[configFile] = "comment:\n  show_cost: false\n"
		stub.SetPrivate(true)
		code, _, stderr := execute(t, "report", "--rows", realRows)
		if code != 0 || len(stub.Comments) != 1 {
			t.Fatalf("exit %d, %d comments: %s", code, len(stub.Comments), stderr)
		}
		if body := stub.Comments[0].Body; !strings.Contains(body, "\n"+lifetimeLine+"\n") {
			t.Errorf("no lifetime line in\n%s", body)
		}
	})
	for name, setUp := range map[string]func(*scmtest.Stub){
		"show_cost false on a public repository": func(stub *scmtest.Stub) {
			stub.Files[configFile] = "comment:\n  show_cost: false\n"
			stub.SetPrivate(false)
		},
		"an unknown visibility": func(stub *scmtest.Stub) {
			stub.Files[configFile] = "comment:\n  show_cost: false\n"
		},
	} {
		t.Run("withheld with "+name, func(t *testing.T) {
			stub := reportEnvironment(t, pullRequestEvent())
			pullWithAnEarlierRun(t, stub)
			setUp(stub)
			code, _, stderr := execute(t, "report", "--rows", realRows)
			if code != 0 || len(stub.Comments) != 1 {
				t.Fatalf("exit %d, %d comments: %s", code, len(stub.Comments), stderr)
			}
			body := stub.Comments[0].Body
			for _, held := range []string{"Across this pull request", "0.003619725", "USD 0.002"} {
				if strings.Contains(body, held) {
					t.Errorf("%q is out in the open:\n%s", held, body)
				}
			}
		})
	}
}

func TestTheLifetimeIsLeftOutWhenTheHistoryCannotBeRead(t *testing.T) {
	stub := reportEnvironment(t, pullRequestEvent())
	pullWithAnEarlierRun(t, stub)
	stub.SetPrivate(true)
	stub.Fail["GET /commits/"+earlierCommit+"/check-runs"] = 502
	code, _, stderr := execute(t, "report", "--rows", realRows)
	if code != 0 || len(stub.Comments) != 1 || len(stub.Checks) != 2 {
		t.Fatalf("exit %d, %d comments, %d checks: %s", code, len(stub.Comments), len(stub.Checks), stderr)
	}
	if strings.Contains(stub.Comments[0].Body, "Across this pull request") {
		t.Errorf("a partial total was posted:\n%s", stub.Comments[0].Body)
	}
	if !strings.Contains(stderr, "the pull request's total is left out") {
		t.Errorf("stderr does not say why: %s", stderr)
	}
}

// baselineConfig sets a rule that reads a baseline, and fails a run of the
// recorded rows over any baseline of ten runs at earlierRunCost.
const baselineConfig = "policy:\n  max_increase_usd: 0\n  enforce: true\n"

// ranMerged adds merged pull request number into main with head, and on the
// head the check runs of n runs of the recorded agent, numbered from first.
func ranMerged(t *testing.T, stub *scmtest.Stub, number int, head string, first int64, n int) {
	t.Helper()
	merged := time.Date(2026, 11, 2, 9, number, 0, 0, time.UTC)
	stub.Pulls = append(stub.Pulls, scmtest.Pull{Number: number, State: "closed", BaseBranch: "main", HeadSHA: head, UpdatedAt: merged, MergedAt: merged})
	for index, object := range earlierReports(t, first, n) {
		stub.Checks = append(stub.Checks, scmtest.Check{
			ID: first + int64(index), Name: comment.CheckName, HeadSHA: head, Conclusion: policy.Neutral,
			Text: "```json\n" + object + "\n```", App: "github-actions",
		})
	}
}

// postedVerdict runs the report on pull request 7 into main and returns the
// words of the verdict it posts, with the report's stderr.
func postedVerdict(t *testing.T, stub *scmtest.Stub) (verdictWords, string) {
	t.Helper()
	code, _, stderr := execute(t, "report", "--rows", realRows)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, check := range slices.Backward(stub.Checks) {
		if check.HeadSHA != headCommit {
			continue
		}
		block, err := comment.ParseResult(check.Text)
		if err != nil || block.Policy == nil {
			t.Fatalf("the posted check carries no verdict: %v", err)
		}
		return wordsOf(*block.Policy), stderr
	}
	t.Fatalf("no check posted: %s", stderr)
	return verdictWords{}, ""
}

// TestTheReportNeverReadsThePullRequestsOwnRunsAsItsBaseline holds the
// report's baseline to other pull requests' runs. Pull request 7's own ten
// earlier runs are on its commits, the list names it merged too, and merged
// pull request 8 has pull request 7's head: none of them is a baseline. Ten
// runs on merged pull request 9 are.
func TestTheReportNeverReadsThePullRequestsOwnRunsAsItsBaseline(t *testing.T) {
	setUp := func(t *testing.T) *scmtest.Stub {
		stub := reportEnvironment(t, pullRequestEventOn("main"))
		stub.SetPrivate(true)
		stub.Files[configFile] = baselineConfig
		stub.Pulls = []scmtest.Pull{{Number: 7, State: "open", BaseBranch: "main", HeadSHA: headCommit, Commits: []string{earlierCommit, headCommit}}}
		for index, object := range earlierReports(t, 100, 10) {
			stub.Checks = append(stub.Checks, scmtest.Check{
				ID: int64(100 + index), Name: comment.CheckName, HeadSHA: earlierCommit, Conclusion: policy.Neutral,
				Text: "```json\n" + object + "\n```", App: "github-actions",
			})
		}
		ranMerged(t, stub, 7, earlierCommit, 200, 0)
		ranMerged(t, stub, 8, headCommit, 300, 10)
		return stub
	}
	noBaseline := verdictWords{policy.Neutral, []string{"run_increase unproven no_baseline"}}
	t.Run("its own runs", func(t *testing.T) {
		if got, stderr := postedVerdict(t, setUp(t)); !reflect.DeepEqual(got, noBaseline) {
			t.Errorf("verdict %+v, want %+v: %s", got, noBaseline, stderr)
		}
	})
	t.Run("another pull request's runs", func(t *testing.T) {
		stub := setUp(t)
		ranMerged(t, stub, 9, mergedHead(9), 400, 10)
		want := verdictWords{policy.Failure, []string{"run_increase fail over_baseline"}}
		if got, stderr := postedVerdict(t, stub); !reflect.DeepEqual(got, want) {
			t.Errorf("verdict %+v, want %+v: %s", got, want, stderr)
		}
	})
}

// TestTheReportReadsNoMergedPullRequestWithoutABaselineRule leaves the merged
// pull requests unread when no rule reads a baseline.
func TestTheReportReadsNoMergedPullRequestWithoutABaselineRule(t *testing.T) {
	for name, config := range map[string]string{
		"no rule":              "policy:\n  run_budget_usd: 5\n",
		"a file that is valid": "comment:\n  show_cost: true\n",
		"a file that is not":   "policy:\n  run_budget: 5\n",
	} {
		t.Run(name, func(t *testing.T) {
			stub := reportEnvironment(t, pullRequestEventOn("main"))
			stub.Files[configFile] = config
			ranMerged(t, stub, 9, mergedHead(9), 400, 10)
			postedVerdict(t, stub)
			for _, request := range stub.Requests {
				if strings.Contains(request, "/pulls?") || strings.Contains(request, mergedHead(9)) {
					t.Errorf("read %s", request)
				}
			}
		})
	}
}

// TestTheReportLeavesTheBaselineOutWhenItCannotBeRead posts no baseline
// when one merged head cannot be read, says why, and keeps the pull
// request's total, which reads other check runs.
func TestTheReportLeavesTheBaselineOutWhenItCannotBeRead(t *testing.T) {
	stub := reportEnvironment(t, pullRequestEventOn("main"))
	pullWithAnEarlierRun(t, stub)
	stub.SetPrivate(true)
	stub.Files[configFile] = baselineConfig
	ranMerged(t, stub, 9, mergedHead(9), 400, 10)
	ranMerged(t, stub, 10, mergedHead(10), 500, 10)
	stub.Fail["GET /repos/example/widgets/commits/"+mergedHead(10)+"/check-runs"] = 502
	want := verdictWords{policy.Neutral, []string{"run_increase unproven no_baseline"}}
	got, stderr := postedVerdict(t, stub)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("verdict %+v, want %+v", got, want)
	}
	if !strings.Contains(stderr, "costroid: warning: the baseline is left out: history: read the check runs on "+mergedHead(10)) {
		t.Errorf("stderr does not say why: %s", stderr)
	}
	if !strings.Contains(stub.Comments[0].Body, "Across this pull request") {
		t.Errorf("the total was left out with the baseline:\n%s", stub.Comments[0].Body)
	}
}

// TestBaselineKeysArePolicyKeys holds the keys that make the report read a
// baseline to the keys costroid.yml takes.
func TestBaselineKeysArePolicyKeys(t *testing.T) {
	for _, key := range baselineKeys {
		if !slices.Contains(policy.Keys(), key) {
			t.Errorf("%s is not a policy key", key)
		}
	}
}
