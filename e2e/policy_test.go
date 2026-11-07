//go:build e2e

package e2e

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
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

// TestServeHoldsAPullRequestToItsPolicyFromItsOwnRows asks the service in its
// container for the verdict on the rows it stored under an ingest key. Those
// rows are unverified, so no budget passes on them however far under it they
// are, and a run rule cannot be read from a pull request's total. A spend
// attestation needs a CI token from the gateway's environment.
func TestServeHoldsAPullRequestToItsPolicyFromItsOwnRows(t *testing.T) {
	services := compose(t)
	key := services.createKey()
	job := newWorkspace(t)
	runAgent(t, job)
	sendRows(t, services, key, job)

	config := "policy:\n  pr_budget_usd: \"5\"\n  run_budget_usd: \"5\"\n"
	body, err := json.Marshal(map[string]any{
		"config":         base64.StdEncoding.EncodeToString([]byte(config)),
		"config_ref":     base,
		"workflow_added": time.Now().Add(-8 * 24 * time.Hour).UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	status, data := services.request(http.MethodPost, "/v1/pulls/"+pull+"/verdict?repo="+repository, key, body)
	if status != http.StatusOK {
		t.Fatalf("POST the verdict: %d %s", status, data)
	}
	type result struct {
		Rule, Outcome, Reason string
		CostUSD               string `json:"cost_usd"`
	}
	var answer struct {
		Budget struct {
			Evidence bool
			Basis    string
		}
		Policy struct {
			Conclusion, Trust, Evidence string
			ConfigRef                   string `json:"config_ref"`
			Results                     []result
		}
	}
	if err := json.Unmarshal(data, &answer); err != nil {
		t.Fatal(err)
	}
	if answer.Policy.Conclusion != "neutral" || answer.Policy.Trust != "unverified" || answer.Policy.Evidence != "" || answer.Policy.ConfigRef != base {
		t.Errorf("the verdict on an ingest key's rows: %s", data)
	}
	if answer.Budget.Evidence || answer.Budget.Basis != "none" {
		t.Errorf("an ingest key's rows reached a budget: %s", data)
	}
	want := map[string]result{
		"pr_budget":  {"pr_budget", "unproven", "unverified_rows", recordedTotal},
		"run_budget": {"run_budget", "unproven", "no_run_rows", ""},
	}
	for _, got := range answer.Policy.Results {
		if got != want[got.Rule] {
			t.Errorf("%s: got %+v, want %+v", got.Rule, got, want[got.Rule])
		}
		delete(want, got.Rule)
	}
	if len(want) != 0 {
		t.Errorf("no result for %v: %s", want, data)
	}

	spend := []byte(`{"repository":"` + repository + `","run_id":"` + runID + `","run_attempt":"1","currency":"USD","spend":"0.5"}`)
	status, data = services.request(http.MethodPost, "/v1/credentials/"+strings.Repeat("a", 64)+"/spend", key, spend)
	if status != http.StatusForbidden || !strings.Contains(string(data), "ATTESTATION_FORBIDDEN") {
		t.Errorf("a spend attestation under an ingest key: %d %s", status, data)
	}
}
