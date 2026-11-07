//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/scm/scmtest"
)

const workflowPath = ".github/workflows/costroid.yml"

// TestAPolicyHoldsTheRecordedRunToItsBudget reports the recorded run under a
// budget that costroid.yml sets at the base commit. Over the budget it warns
// during the warn-only period and fails after it. Under the budget the rows the
// agent's job wrote cannot pass it.
func TestAPolicyHoldsTheRecordedRunToItsBudget(t *testing.T) {
	job := newWorkspace(t)
	runAgent(t, job)
	cases := []struct {
		name, budget string
		added        time.Duration
		conclusion   string
		says, omits  []string
	}{
		{
			name: "over the budget inside the period", budget: "0.001", added: 24 * time.Hour, conclusion: "neutral",
			says: []string{
				"Run budget: USD " + recordedTotal + " is over the USD 0.001 budget.",
				"Warn-only until ",
				"Add `enforce: true` under `policy` in `costroid.yml` to enforce now.",
			},
		},
		{
			name: "over the budget after the period", budget: "0.001", added: 8 * 24 * time.Hour, conclusion: "failure",
			says:  []string{"Run budget: USD " + recordedTotal + " is over the USD 0.001 budget."},
			omits: []string{"Warn-only"},
		},
		{
			name: "under the budget", budget: "5", added: 8 * 24 * time.Hour, conclusion: "neutral",
			says:  []string{"Run budget: unverified rows cannot satisfy a budget."},
			omits: []string{"Warn-only"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			github := scmtest.New(t)
			github.Branches["main"] = base
			github.Files["costroid.yml"] = "policy:\n  run_budget_usd: \"" + tc.budget + "\"\n"
			github.Commits[workflowPath] = []time.Time{time.Now().Add(-tc.added)}
			env := append(job.env, "GITHUB_TOKEN="+github.Token, "GITHUB_API_URL="+github.Server.URL,
				"GITHUB_WORKFLOW_REF=acme/widgets/"+workflowPath+"@refs/pull/"+pull+"/merge")
			code, _, stderr := job.costroid(env, "report", "--rows", job.rows, "--budget-usd", "5")
			if code != 0 || len(github.Checks) != 1 || len(github.Comments) != 1 {
				t.Fatalf("exit %d, %d checks, %d comments: %s", code, len(github.Checks), len(github.Comments), stderr)
			}
			if strings.Contains(stderr, "is not a key Costroid reads") || strings.Contains(stderr, "policy:") {
				t.Errorf("the report warned:\n%s", stderr)
			}
			check := github.Checks[0]
			if check.Conclusion != tc.conclusion || check.HeadSHA != head {
				t.Errorf("the check concluded %s on %s, want %s", check.Conclusion, check.HeadSHA, tc.conclusion)
			}
			body := github.Comments[0].Body
			for _, want := range append(tc.says, "**Verdict: "+tc.conclusion+".**", "USD "+recordedTotal, `"config_ref": "`+base+`"`) {
				if !strings.Contains(body, want) {
					t.Errorf("comment: want %q in:\n%s", want, body)
				}
			}
			for _, unwanted := range tc.omits {
				if strings.Contains(body, unwanted) {
					t.Errorf("comment: %q in:\n%s", unwanted, body)
				}
			}
		})
	}
}
