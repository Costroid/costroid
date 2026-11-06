//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/scm/scmtest"
)

const workflowPath = ".github/workflows/costroid.yml"

// history answers GitHub's commits route for the workflow file, which the stub
// does not serve, and hands every other request to the stub.
type history struct {
	stub  *scmtest.Stub
	added time.Time
}

func (h history) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/repos/acme/widgets/commits" {
		h.stub.ServeHTTP(w, r)
		return
	}
	query := r.URL.Query()
	if r.Header.Get("Authorization") != "Bearer "+h.stub.Token || query.Get("sha") != base || query.Get("path") != workflowPath {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	commits := []map[string]any{}
	if query.Get("page") == "1" {
		commits = append(commits, map[string]any{"commit": map[string]any{"committer": map[string]any{"date": h.added.Format(time.RFC3339)}}})
	}
	if err := json.NewEncoder(w).Encode(commits); err != nil {
		panic(err)
	}
}

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
			github.Files["costroid.yml"] = "policy:\n  run_budget_usd: \"" + tc.budget + "\"\n"
			server := httptest.NewServer(history{stub: github, added: time.Now().Add(-tc.added)})
			t.Cleanup(server.Close)
			env := append(job.env, "GITHUB_TOKEN="+github.Token, "GITHUB_API_URL="+server.URL,
				"GITHUB_WORKFLOW_REF=acme/widgets/"+workflowPath+"@refs/pull/"+pull+"/merge")
			code, _, stderr := job.costroid(env, "report", "--rows", job.rows, "--budget-usd", "5")
			if code != 0 || len(github.Checks) != 1 || len(github.Comments) != 1 {
				t.Fatalf("exit %d, %d checks, %d comments: %s", code, len(github.Checks), len(github.Comments), stderr)
			}
			if strings.Contains(stderr, "is not a key Costroid reads") || strings.Contains(stderr, "workflow:") {
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
