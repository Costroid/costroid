package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm/scmtest"
	"github.com/Costroid/costroid/internal/server"
	"github.com/Costroid/costroid/internal/store"
)

// earlierRunCost is what each earlier run of the recorded agent costs in the
// synthetic history, in USD. The recorded rows cost USD 0.001619725, so a run
// is over a baseline of earlier runs at this cost.
const earlierRunCost = "0.001"

// earlierReports are the result objects of n earlier runs of the recorded
// agent, each on its own check run and rows. The figures are synthetic.
func earlierReports(t *testing.T, n int) []string {
	t.Helper()
	objects := make([]string, n)
	for index := range objects {
		id := int64(100 + index)
		result, err := comment.Result(comment.Input{
			State: comment.Reported,
			Ledger: comment.Ledger{Rows: 1, Agents: []comment.Data{"claude-code"}, Figures: []comment.Figure{
				{Currency: "USD", Basis: "list_equivalent", Attribution: "unattributed", Enforcement: "agent-flag", Cost: decimal.MustParse(earlierRunCost), Rows: 1},
			}},
			RowsSHA256: fmt.Sprintf("%064x", id),
			CheckRunID: &id,
			IDInCheck:  true,
		})
		if err != nil {
			t.Fatal(err)
		}
		objects[index] = result
	}
	return objects
}

// pathCase is one pull request as each path sees it: costroid.yml at the
// base, nil for none, and the earlier runs of the same agent.
type pathCase struct {
	name    string
	config  *string
	earlier int
	// want is the conclusion and the outcome and reason of each result, as
	// the report and costroid policy give them.
	want verdictWords
	// serve is what serve answers, when its answer differs by design: it
	// covers the pull request, so a run rule concludes unproven there.
	serve *verdictWords
}

type verdictWords struct {
	conclusion string
	results    []string
}

func wordsOf(verdict policy.Verdict) verdictWords {
	words := verdictWords{conclusion: verdict.Conclusion}
	for _, result := range verdict.Results {
		words.results = append(words.results, result.Rule+" "+result.Outcome+" "+result.Reason)
	}
	return words
}

func text(s string) *string { return &s }

// TestOfflineEqualsOnline holds one pull request to one costroid.yml and one
// history on the three paths: costroid policy on the rows file, the report on
// GitHub, and serve over the same rows in its ledger. The report and costroid
// policy give the same verdict, rule for rule and figure for figure. serve
// gives it too, except on a run rule, which it answers for the pull request.
func TestOfflineEqualsOnline(t *testing.T) {
	cases := []pathCase{
		{name: "no costroid.yml", earlier: 10, want: verdictWords{conclusion: policy.Neutral}},
		{name: "a costroid.yml that is not valid", config: text("policy:\n  run_budget: 5\n"), earlier: 10, want: verdictWords{conclusion: policy.Neutral}},
		{
			name: "a model off the list", config: text("policy:\n  models: [\"gpt-*\"]\n  enforce: true\n"), earlier: 10,
			want: verdictWords{policy.Failure, []string{"model_allowlist fail model_outside_allowlist"}},
		},
		{
			name: "every model on the list", config: text("policy:\n  models: [\"claude-*\"]\n  enforce: true\n"), earlier: 10,
			want: verdictWords{policy.Neutral, []string{"model_allowlist unproven unverified_rows"}},
		},
		{
			name: "the pull request over its budget", config: text("policy:\n  pr_budget_usd: \"0.011\"\n  enforce: true\n"), earlier: 10,
			want: verdictWords{policy.Failure, []string{"pr_budget fail over_limit"}},
		},
		{
			name: "the pull request within its budget", config: text("policy:\n  pr_budget_usd: \"0.012\"\n  enforce: true\n"), earlier: 10,
			want: verdictWords{policy.Neutral, []string{"pr_budget unproven unverified_rows"}},
		},
		{
			name: "a run over the P50 of ten earlier runs", config: text("policy:\n  max_increase_usd: \"0.0006\"\n  enforce: true\n"), earlier: 10,
			want:  verdictWords{policy.Failure, []string{"run_increase fail over_baseline"}},
			serve: &verdictWords{policy.Neutral, []string{"run_increase unproven no_run_rows"}},
		},
		{
			name: "a run within the P50 of ten earlier runs", config: text("policy:\n  max_increase_usd: \"0.0007\"\n  enforce: true\n"), earlier: 10,
			want:  verdictWords{policy.Neutral, []string{"run_increase unproven unverified_rows"}},
			serve: &verdictWords{policy.Neutral, []string{"run_increase unproven no_run_rows"}},
		},
		{
			name: "nine earlier runs are no baseline", config: text("policy:\n  max_increase_usd: 0\n  enforce: true\n"), earlier: 9,
			want:  verdictWords{policy.Neutral, []string{"run_increase unproven no_baseline"}},
			serve: &verdictWords{policy.Neutral, []string{"run_increase unproven no_run_rows"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objects := earlierReports(t, tc.earlier)
			report := reportPath(t, tc, objects)
			offline := offlinePath(t, tc, objects)
			served := servePath(t, tc)
			if got := wordsOf(report); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("the report: %+v, want %+v", got, tc.want)
			}
			if !reflect.DeepEqual(report.Results, offline.Results) || report.Conclusion != offline.Conclusion || report.Config != offline.Config {
				t.Errorf("costroid policy differs from the report:\nreport  %+v\noffline %+v", report, offline)
			}
			want := tc.want
			if tc.serve != nil {
				want = *tc.serve
			}
			if got := wordsOf(served); !reflect.DeepEqual(got, want) || served.Config != report.Config {
				t.Errorf("serve: %+v with %s, want %+v with %s", got, served.Config, want, report.Config)
			}
			if tc.serve == nil && !reflect.DeepEqual(served.Results, report.Results) {
				t.Errorf("serve's figures differ from the report's:\nreport %+v\nserve  %+v", report.Results, served.Results)
			}
		})
	}
}

// reportPath posts the report on pull request 7, whose earlier commit holds
// the earlier runs' check runs, and reads the verdict from the check it posts.
func reportPath(t *testing.T, tc pathCase, objects []string) policy.Verdict {
	t.Helper()
	stub := reportEnvironment(t, pullRequestEvent())
	stub.SetPrivate(true)
	if tc.config != nil {
		stub.Files[configFile] = *tc.config
	}
	stub.Pulls = []scmtest.Pull{{Number: 7, State: "open", HeadSHA: headCommit, Commits: []string{earlierCommit, headCommit}}}
	for index, object := range objects {
		stub.Checks = append(stub.Checks, scmtest.Check{
			ID: int64(100 + index), Name: comment.CheckName, HeadSHA: earlierCommit, Conclusion: policy.Neutral,
			Text: "```json\n" + object + "\n```", App: "github-actions",
		})
	}
	code, _, stderr := execute(t, "report", "--rows", realRows)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, check := range stub.Checks {
		if check.HeadSHA != headCommit {
			continue
		}
		block, err := comment.ParseResult(check.Text)
		if err != nil || block.Policy == nil {
			t.Fatalf("the posted check carries no verdict: %v\n%s", err, check.Text)
		}
		return *block.Policy
	}
	t.Fatalf("no check posted: %s", stderr)
	return policy.Verdict{}
}

// offlinePath runs costroid policy on the rows with the same costroid.yml and
// the earlier runs' result objects as --history, one per line.
func offlinePath(t *testing.T, tc pathCase, objects []string) policy.Verdict {
	t.Helper()
	directory := t.TempDir()
	configPath := filepath.Join(directory, "base.yml")
	if tc.config != nil {
		if err := os.WriteFile(configPath, []byte(*tc.config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var lines bytes.Buffer
	for _, object := range objects {
		if err := json.Compact(&lines, []byte(object)); err != nil {
			t.Fatal(err)
		}
		lines.WriteByte('\n')
	}
	historyPath := filepath.Join(directory, "earlier.jsonl")
	if err := os.WriteFile(historyPath, lines.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := execute(t, "policy", "--json", "--rows", realRows, "--config", configPath, "--history", historyPath)
	if code != 0 && code != exitRejected {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var verdict policy.Verdict
	if err := json.Unmarshal([]byte(stdout), &verdict); err != nil {
		t.Fatalf("%v: %s", err, stdout)
	}
	return verdict
}

// servePath asks serve for the verdict on pull request 7, whose rows in its
// ledger are the recorded run's and the earlier runs', none checked against a
// CI token.
func servePath(t *testing.T, tc pathCase) policy.Verdict {
	t.Helper()
	earlier := decimal.MustParse(earlierRunCost).Mul(decimal.FromInt(int64(tc.earlier)))
	ledger := pullLedger{lines: []store.Line{
		ingestedLine(decimal.MustParse(realTotal), 19),
		ingestedLine(earlier, int64(tc.earlier)),
	}}
	service := httptest.NewServer(server.New(server.Config{Ledger: ledger}).Handler())
	defer service.Close()
	body := map[string]any{"config_ref": baseCommit}
	if tc.config != nil {
		body["config"] = base64.StdEncoding.EncodeToString([]byte(*tc.config))
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, service.URL+"/v1/pulls/7/verdict?repo=github.com/example/widgets", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+store.KeyPrefix+"offline-equals-online")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("answered %d: %s, %v", response.StatusCode, answer, err)
	}
	var verdict struct {
		Policy policy.Verdict `json:"policy"`
	}
	if err := json.Unmarshal(answer, &verdict); err != nil {
		t.Fatal(err)
	}
	return verdict.Policy
}

// ingestedLine is rows of the recorded agent and model as an ingest key sent
// them: graded unattributed and never checked against a token.
func ingestedLine(cost decimal.Decimal, rows int64) store.Line {
	return store.Line{
		Currency: "USD", Agent: "claude-code", Model: "claude-haiku-5-5", CostBasis: "list_equivalent",
		CostSource: "agent_session_file", AttributionGrade: "unattributed", RunIdentity: "unverified",
		Rows: rows, ListCost: cost,
	}
}

// pullLedger holds one pull request's lines for any ingest key, and refuses
// every write.
type pullLedger struct {
	lines []store.Line
}

var errReadOnly = errors.New("this ledger holds one pull request and takes no write")

func (l pullLedger) TenantForKey(context.Context, string) (store.TenantID, error) {
	return store.ParseTenantID("00000000-0000-4000-8000-000000000007")
}

func (l pullLedger) TenantForOwner(context.Context, string, string) (store.TenantID, error) {
	return store.TenantID{}, store.ErrNotFound
}

func (l pullLedger) Ingest(context.Context, store.TenantID, store.Batch) (store.Receipt, error) {
	return store.Receipt{}, errReadOnly
}

func (l pullLedger) PullRequest(_ context.Context, _ store.TenantID, repo, pr string) ([]store.Line, error) {
	if repo != "github.com/example/widgets" || pr != "7" {
		return nil, nil
	}
	return l.lines, nil
}

func (l pullLedger) Attest(context.Context, store.TenantID, store.MintedCredential) error {
	return errReadOnly
}

func (l pullLedger) MintedCredentials(context.Context, store.TenantID, []string) (map[string]store.MintedCredential, error) {
	return nil, errReadOnly
}

// TestPolicyRefusesAHistoryLineWithoutItsCheckRun holds costroid policy to
// the order of the runs: an object that names no check run cannot be placed
// in the window, so the file is refused with the line.
func TestPolicyRefusesAHistoryLineWithoutItsCheckRun(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "earlier.jsonl")
	objects := earlierReports(t, 1)
	unnamed := strings.Replace(objects[0], `"check_run_id": 100,`, "", 1)
	var lines bytes.Buffer
	for _, object := range []string{objects[0], unnamed} {
		if err := json.Compact(&lines, []byte(object)); err != nil {
			t.Fatal(err)
		}
		lines.WriteByte('\n')
	}
	if err := os.WriteFile(path, lines.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := execute(t, "policy", "--rows", realRows, "--config", filepath.Join(directory, "none.yml"), "--history", path)
	if code != exitFailure || stdout != "" || !strings.Contains(stderr, "--history line 2: the result object names no check run") {
		t.Errorf("exit %d: %q %q", code, stdout, stderr)
	}
}
