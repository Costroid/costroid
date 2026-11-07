package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/scm/scmtest"
)

// pullWithMeasuredRuns gives pull request 7 one earlier commit per report,
// each with a check run that reports a measured USD 0.002.
func pullWithMeasuredRuns(t *testing.T, stub *scmtest.Stub, reports int) {
	t.Helper()
	measured := comment.Figure{Currency: "USD", Basis: "measured", Attribution: "unattributed", Enforcement: "agent-flag", Cost: decimal.MustParse("0.002"), Rows: 1}
	output, err := comment.Check(comment.Input{State: comment.Reported, Ledger: comment.Ledger{Rows: 1, Figures: []comment.Figure{measured}}})
	if err != nil {
		t.Fatal(err)
	}
	var commits []string
	for index := range reports {
		commit := fmt.Sprintf("%040x", index+1)
		commits = append(commits, commit)
		stub.Checks = append(stub.Checks, scmtest.Check{ID: int64(100 + index), Name: comment.CheckName, HeadSHA: commit, Conclusion: "neutral", Text: output.Text, App: "github-actions"})
	}
	stub.Pulls = []scmtest.Pull{{Number: 7, State: "open", HeadSHA: headCommit, Commits: append(commits, headCommit)}}
}

// TestWarnPeriodEndsAtTwentiethPR reports a run over its budget a day after the
// workflow reached the base branch. The report counts the pull request's
// measured reports from its check runs, and the twentieth ends the warn-only
// period before its 7 days do. This run's own rows are at list, so only the
// earlier reports count.
func TestWarnPeriodEndsAtTwentiethPR(t *testing.T) {
	for reports, want := range map[int]string{19: "neutral", 20: "failure"} {
		t.Run(fmt.Sprintf("%d measured reports", reports), func(t *testing.T) {
			stub := reportEnvironment(t, pullRequestEvent())
			t.Setenv("GITHUB_WORKFLOW_REF", "example/widgets/"+workflowFile+"@refs/pull/7/merge")
			stub.Commits[workflowFile] = []time.Time{time.Now().Add(-24 * time.Hour)}
			stub.Files[configFile] = overBudget
			pullWithMeasuredRuns(t, stub, reports)
			execute(t, "report", "--rows", realRows)
			posted := stub.Checks[len(stub.Checks)-1]
			if posted.HeadSHA != headCommit || posted.Conclusion != want {
				t.Fatalf("the check on %s concluded %s, want %s", posted.HeadSHA, posted.Conclusion, want)
			}
			ended := strings.Contains(posted.Text, `"reason": "ended"`)
			if ended != (want == "failure") {
				t.Errorf("the period ended %t:\n%s", ended, posted.Text)
			}
		})
	}
}
