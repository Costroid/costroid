package main

import (
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/decimal"
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
