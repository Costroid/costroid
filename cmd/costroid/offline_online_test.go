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
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm"
	"github.com/Costroid/costroid/internal/scm/scmtest"
	"github.com/Costroid/costroid/internal/server"
	"github.com/Costroid/costroid/internal/store"
)

// earlierRunCost is what each earlier run of the recorded agent costs in the
// synthetic history, in USD. The recorded rows cost USD 0.001619725, so a run
// is over a baseline of earlier runs at this cost.
const earlierRunCost = "0.001"

// earlierReports are the result objects of n earlier runs of the recorded
// agent, each on its own check run and rows, numbered from first. The figures
// are synthetic.
func earlierReports(t *testing.T, first int64, n int) []string {
	t.Helper()
	return reportsAt(t, first, n, earlierRunCost)
}

// costlyRunCost is what each run costs in a costly synthetic history, in
// USD: a run of the recorded rows is far below a baseline of such runs.
const costlyRunCost = "5"

// costlyReports are earlierReports at costlyRunCost.
func costlyReports(t *testing.T, first int64, n int) []string {
	t.Helper()
	return reportsAt(t, first, n, costlyRunCost)
}

// reportsAt are the result objects of earlierReports, each run at cost.
func reportsAt(t *testing.T, first int64, n int, cost string) []string {
	t.Helper()
	objects := make([]string, n)
	for index := range objects {
		id := first + int64(index)
		result, err := comment.Result(comment.Input{
			State: comment.Reported,
			Ledger: comment.Ledger{Rows: 1, Agents: []comment.Data{"claude-code"}, Figures: []comment.Figure{
				{Currency: "USD", Basis: "list_equivalent", Attribution: "unattributed", Enforcement: "agent-flag", Cost: decimal.MustParse(cost), Rows: 1},
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
// base, nil for none, the earlier runs of the same agent on the pull
// request's commits, and its runs on the pull requests merged into the base
// branch, perHead on each merged head.
type pathCase struct {
	name   string
	config *string
	// pack is the pack costroid.yml names, as the base holds it, nil for
	// none.
	pack    *string
	earlier int
	merged  int
	perHead int
	// unread puts on the first merged head a check run whose object this
	// release does not read.
	unread bool
	// costly makes the pull request's own earlier runs cost costlyRunCost.
	costly bool
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
	if verdict.Pack != nil {
		for _, result := range verdict.Pack.Results {
			words.results = append(words.results, "pack "+result.Rule+" "+string(result.Outcome)+" "+result.Reason)
		}
	}
	return words
}

// The packs the paths run, beside the bodies check_pack_test.go names: one
// that is not valid at line 4, and one that fails a run whose budget could
// not pass.
const (
	packInvalid   = "package costroid.pack\n\nresults contains x if {\n\tx := http.send({})\n}\n"
	packUnproven  = "package costroid.pack\n\nresults contains {\"rule\": \"proven_budget\", \"outcome\": \"fail\", \"reason\": \"budget_unproven\"} if {\n\tsome result in input.results\n\tresult.rule == \"run_budget\"\n\tresult.outcome == \"unproven\"\n}\n"
	packAndBudget = packConfig + "  run_budget_usd: \"5\"\n"
)

func text(s string) *string { return &s }

// TestOfflineEqualsOnline holds one pull request to one costroid.yml and one
// history on the three paths: costroid policy on the rows file, the report on
// GitHub, and serve over the same rows in its ledger. The history is the
// pull request's own earlier runs and the agent's runs on the pull requests
// merged into the base branch. The report and costroid policy give the same
// verdict, rule for rule and figure for figure. serve gives it too, except on
// a run rule, which it answers for the pull request.
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
			name: "a run over the P50 of ten merged runs", config: text("policy:\n  max_increase_usd: \"0.0006\"\n  enforce: true\n"), merged: 10,
			want:  verdictWords{policy.Failure, []string{"run_increase fail over_baseline"}},
			serve: &verdictWords{policy.Neutral, []string{"run_increase unproven no_run_rows"}},
		},
		{
			name: "a run within the P50 of ten merged runs", config: text("policy:\n  max_increase_usd: \"0.0007\"\n  enforce: true\n"), merged: 10,
			want:  verdictWords{policy.Neutral, []string{"run_increase unproven unverified_rows"}},
			serve: &verdictWords{policy.Neutral, []string{"run_increase unproven no_run_rows"}},
		},
		{
			name: "nine merged runs are no baseline", config: text("policy:\n  max_increase_usd: 0\n  enforce: true\n"), merged: 9,
			want:  verdictWords{policy.Neutral, []string{"run_increase unproven no_baseline"}},
			serve: &verdictWords{policy.Neutral, []string{"run_increase unproven no_run_rows"}},
		},
		{
			name: "ten runs on five merged pull requests each count", config: text("policy:\n  max_increase_percent: 50\n  enforce: true\n"), merged: 10, perHead: 2,
			want:  verdictWords{policy.Failure, []string{"run_increase_percent fail over_baseline"}},
			serve: &verdictWords{policy.Neutral, []string{"run_increase_percent unproven no_run_rows"}},
		},
		{
			name: "the pull request's own ten runs are no baseline", config: text("policy:\n  max_increase_usd: 0\n  enforce: true\n"), earlier: 10,
			want:  verdictWords{policy.Neutral, []string{"run_increase unproven no_baseline"}},
			serve: &verdictWords{policy.Neutral, []string{"run_increase unproven no_run_rows"}},
		},
		{
			name: "a merged run that cannot be read leaves no baseline", config: text("policy:\n  max_increase_usd: 0\n  enforce: true\n"), merged: 20, unread: true,
			want:  verdictWords{policy.Neutral, []string{"run_increase unproven no_baseline"}},
			serve: &verdictWords{policy.Neutral, []string{"run_increase unproven no_run_rows"}},
		},
		{
			name: "a run over 1.5 times the P95 of twenty merged runs", config: text("policy:\n  max_p95_factor: \"1.5\"\n  enforce: true\n"), merged: 20,
			want:  verdictWords{policy.Failure, []string{"run_p95 fail over_baseline"}},
			serve: &verdictWords{policy.Neutral, []string{"run_p95 unproven no_run_rows"}},
		},
		{
			name: "a run within twice the P95 of twenty merged runs", config: text("policy:\n  max_p95_factor: 2\n  enforce: true\n"), merged: 20,
			want:  verdictWords{policy.Neutral, []string{"run_p95 unproven unverified_rows"}},
			serve: &verdictWords{policy.Neutral, []string{"run_p95 unproven no_run_rows"}},
		},
		{
			name: "nineteen merged runs are no P95", config: text("policy:\n  max_p95_factor: 1\n  enforce: true\n"), merged: 19,
			want:  verdictWords{policy.Neutral, []string{"run_p95 unproven no_baseline"}},
			serve: &verdictWords{policy.Neutral, []string{"run_p95 unproven no_run_rows"}},
		},
		{
			name: "the pull request's own twenty runs are no P95", config: text("policy:\n  max_p95_factor: 1\n  enforce: true\n"), earlier: 20,
			want:  verdictWords{policy.Neutral, []string{"run_p95 unproven no_baseline"}},
			serve: &verdictWords{policy.Neutral, []string{"run_p95 unproven no_run_rows"}},
		},
		{
			name: "the P95 and the median rules read one baseline", config: text("policy:\n  max_increase_usd: \"0.0006\"\n  max_p95_factor: 2\n  enforce: true\n"), merged: 20,
			want:  verdictWords{policy.Failure, []string{"run_increase fail over_baseline", "run_p95 unproven unverified_rows"}},
			serve: &verdictWords{policy.Neutral, []string{"run_increase unproven no_run_rows", "run_p95 unproven no_run_rows"}},
		},
		{
			name: "a pack rule that fails", config: text(packConfig), pack: text(packFails), earlier: 10,
			want: verdictWords{policy.Failure, []string{"pack base_rule fail on_every_run"}},
		},
		{
			name: "a pack rule that warns", config: text(packConfig), pack: text(packWarns), earlier: 10,
			want: verdictWords{policy.Neutral, []string{"pack moved_rule warn on_every_run"}},
		},
		{
			name: "a pack that is not valid", config: text(packConfig), pack: text(packInvalid), earlier: 10,
			want: verdictWords{policy.Neutral, []string{"customer_pack unproven pack_invalid"}},
		},
		{
			name: "a pack the base does not hold", config: text(packConfig), earlier: 10,
			want: verdictWords{policy.Neutral, []string{"customer_pack unproven pack_unread"}},
		},
		{
			name: "a pack that fails a run whose budget could not pass", config: text(packAndBudget), pack: text(packUnproven), earlier: 10,
			want:  verdictWords{policy.Failure, []string{"run_budget unproven unverified_rows", "pack proven_budget fail budget_unproven"}},
			serve: &verdictWords{policy.Failure, []string{"run_budget unproven no_run_rows", "pack proven_budget fail budget_unproven"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			history := pathHistory{own: earlierReports(t, 100, tc.earlier), merged: earlierReports(t, 1, tc.merged)}
			if tc.unread {
				history.merged[0] = olderSchema.ReplaceAllString(history.merged[0], `"schema": "costroid.result/0.2"`)
			}
			report := reportPath(t, tc, history)
			offline := offlinePath(t, tc, history)
			served := servePath(t, tc)
			if got := wordsOf(report); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("the report: %+v, want %+v", got, tc.want)
			}
			if !reflect.DeepEqual(report.Results, offline.Results) || report.Conclusion != offline.Conclusion || report.Config != offline.Config || !reflect.DeepEqual(report.Pack, offline.Pack) {
				t.Errorf("costroid policy differs from the report:\nreport  %+v\noffline %+v", report, offline)
			}
			if !reflect.DeepEqual(served.Pack, report.Pack) {
				t.Errorf("serve ran another pack than the report:\nreport %+v\nserve  %+v", report.Pack, served.Pack)
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

// olderSchema finds the schema of a result object, which a test rewrites to
// one this release does not read.
var olderSchema = regexp.MustCompile(`"schema": "costroid\.result/[0-9.]+"`)

// pathHistory is the result objects of the pull request's own earlier runs
// and of the runs on the merged pull requests' heads.
type pathHistory struct {
	own, merged []string
}

// mergedHead is the synthetic head commit of merged pull request number.
func mergedHead(number int) string {
	return fmt.Sprintf("%040x", 0xfeed00+number)
}

// reportPath posts the report on pull request 7 into main, whose earlier
// commit holds the check runs of its own earlier runs, and reads the verdict
// from the check it posts. The merged runs are on the heads of pull requests
// merged into main, perHead on each.
func reportPath(t *testing.T, tc pathCase, history pathHistory) policy.Verdict {
	t.Helper()
	check := postedCheck(t, tc, history)
	block, err := comment.ParseResult(check.Text)
	if err != nil || block.Policy == nil {
		t.Fatalf("the posted check carries no verdict: %v\n%s", err, check.Text)
	}
	return *block.Policy
}

// postedCheck posts the report as reportPath does, and returns the check it
// posts on the head.
func postedCheck(t *testing.T, tc pathCase, history pathHistory) scmtest.Check {
	t.Helper()
	stub := reportEnvironment(t, pullRequestEventOn("main"))
	stub.SetPrivate(true)
	if tc.config != nil {
		stub.Files[configFile] = *tc.config
	}
	if tc.pack != nil {
		stub.Files[packPath] = *tc.pack
	}
	stub.Pulls = []scmtest.Pull{{Number: 7, State: "open", BaseBranch: "main", HeadSHA: headCommit, Commits: []string{earlierCommit, headCommit}}}
	for index, object := range history.own {
		stub.Checks = append(stub.Checks, scmtest.Check{
			ID: int64(100 + index), Name: comment.CheckName, HeadSHA: earlierCommit, Conclusion: policy.Neutral,
			Text: "```json\n" + object + "\n```", App: "github-actions",
		})
	}
	perHead := max(tc.perHead, 1)
	for index, object := range history.merged {
		number := 10 + index/perHead
		if index%perHead == 0 {
			merged := time.Date(2026, 11, 2, 9, number, 0, 0, time.UTC)
			stub.Pulls = append(stub.Pulls, scmtest.Pull{Number: number, State: "closed", BaseBranch: "main", HeadSHA: mergedHead(number), UpdatedAt: merged, MergedAt: merged})
		}
		stub.Checks = append(stub.Checks, scmtest.Check{
			ID: int64(1 + index), Name: comment.CheckName, HeadSHA: mergedHead(number), Conclusion: policy.Neutral,
			Text: "```json\n" + object + "\n```", App: "github-actions",
		})
	}
	code, _, stderr := execute(t, "report", "--rows", realRows)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, check := range stub.Checks {
		if check.HeadSHA == headCommit {
			return check
		}
	}
	t.Fatalf("no check posted: %s", stderr)
	return scmtest.Check{}
}

// offlinePath runs costroid policy on the rows with the same costroid.yml,
// the pull request's own earlier runs' result objects as --history and the
// merged runs' as --baseline, one per line.
func offlinePath(t *testing.T, tc pathCase, history pathHistory) policy.Verdict {
	t.Helper()
	stdout := offlineOutput(t, tc, history, "--json")
	var verdict policy.Verdict
	if err := json.Unmarshal([]byte(stdout), &verdict); err != nil {
		t.Fatalf("%v: %s", err, stdout)
	}
	return verdict
}

// offlineOutput runs costroid policy as offlinePath does, with the flags
// added, and returns what it prints.
func offlineOutput(t *testing.T, tc pathCase, history pathHistory, flags ...string) string {
	t.Helper()
	directory := t.TempDir()
	configPath := filepath.Join(directory, "base.yml")
	if tc.config != nil {
		if err := os.WriteFile(configPath, []byte(*tc.config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if tc.pack != nil {
		pack := filepath.Join(directory, filepath.FromSlash(packPath))
		if err := os.MkdirAll(filepath.Dir(pack), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pack, []byte(*tc.pack), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	historyPath := writeObjects(t, filepath.Join(directory, "earlier.jsonl"), history.own)
	baselinePath := writeObjects(t, filepath.Join(directory, "merged.jsonl"), history.merged)
	args := append([]string{"policy", "--rows", realRows, "--config", configPath, "--history", historyPath, "--baseline", baselinePath}, flags...)
	code, stdout, stderr := execute(t, args...)
	if code != 0 && code != exitRejected {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	return stdout
}

// writeObjects writes the result objects to path, one per line, and returns
// the path.
func writeObjects(t *testing.T, path string, objects []string) string {
	t.Helper()
	var lines bytes.Buffer
	for _, object := range objects {
		if err := json.Compact(&lines, []byte(object)); err != nil {
			t.Fatal(err)
		}
		lines.WriteByte('\n')
	}
	if err := os.WriteFile(path, lines.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// servePath asks serve for the verdict on pull request 7, whose rows in its
// ledger are the recorded run's and the earlier runs', none checked against a
// CI token. serve reaches the repository as the App's installation, and reads
// costroid.yml and the pack at the base commit itself.
func servePath(t *testing.T, tc pathCase) policy.Verdict {
	t.Helper()
	earlier := decimal.MustParse(earlierRunCost).Mul(decimal.FromInt(int64(tc.earlier)))
	ledger := pullLedger{lines: []store.Line{
		ingestedLine(decimal.MustParse(realTotal), 19),
		ingestedLine(earlier, int64(tc.earlier)),
	}}
	repository := scmtest.New(t)
	if tc.config != nil {
		repository.Files[configFile] = *tc.config
	}
	if tc.pack != nil {
		repository.Files[packPath] = *tc.pack
	}
	app := &server.GitHubApp{Installations: oneInstallation{}, Hosts: stubHosts{repository}}
	service := httptest.NewServer(server.New(server.Config{Ledger: ledger, App: app}).Handler())
	defer service.Close()
	body := map[string]any{"config_ref": baseCommit, "installation": servedInstallation}
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

// servedInstallation is the App's installation that reaches
// example/widgets.
const servedInstallation = 42

// oneInstallation is the App's installations table holding one
// installation, which reaches example/widgets, and refusing every write.
type oneInstallation struct{}

func (oneInstallation) AddRepositories(context.Context, int64, string, []store.Repository) error {
	return errReadOnly
}

func (oneInstallation) RemoveRepositories(context.Context, int64, []int64) error {
	return errReadOnly
}

func (oneInstallation) SuspendInstallation(context.Context, int64, string, bool) error {
	return errReadOnly
}

func (oneInstallation) DeleteInstallation(context.Context, int64) error {
	return errReadOnly
}

func (oneInstallation) Installation(_ context.Context, id int64) (store.Installation, error) {
	if id != servedInstallation {
		return store.Installation{}, store.ErrNotFound
	}
	return store.Installation{ID: id, Account: "example", Repositories: []store.Repository{{ID: 1, FullName: "example/widgets"}}}, nil
}

// stubHosts reaches every repository through one stub.
type stubHosts struct {
	stub *scmtest.Stub
}

func (h stubHosts) Repository(installation int64, repository string) (server.ApprovalHost, error) {
	return scm.NewGitHub(scm.GitHubOptions{
		API: h.stub.Server.URL, Repository: repository, Tokens: scm.StaticToken(h.stub.Token), Client: &http.Client{}, Installation: installation,
	})
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
	objects := earlierReports(t, 100, 1)
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

// TestPolicyRefusesHistoryAloneWhereARuleReadsABaseline refuses --history
// without --baseline when a rule reads a baseline: the file once gave the
// baseline, and is never read as giving none. Where no rule reads one, or
// beside --baseline, --history is read as before.
func TestPolicyRefusesHistoryAloneWhereARuleReadsABaseline(t *testing.T) {
	directory := t.TempDir()
	history := writeObjects(t, filepath.Join(directory, "earlier.jsonl"), earlierReports(t, 100, 10))
	baseline := writeObjects(t, filepath.Join(directory, "merged.jsonl"), nil)
	increase := writeBaseConfig(t, "policy:\n  max_increase_percent: 10\n")
	code, stdout, stderr := execute(t, "policy", "--rows", realRows, "--config", increase, "--history", history)
	want := "costroid: --history gives no baseline: run_increase, run_increase_percent and run_p95 read the merged pull requests' check runs, which --baseline names\n"
	if code != exitFailure || stdout != "" || stderr != want {
		t.Errorf("exit %d: %q %q", code, stdout, stderr)
	}
	for name, args := range map[string][]string{
		"with --baseline":         {"--config", increase, "--history", history, "--baseline", baseline},
		"where no rule reads one": {"--config", writeBaseConfig(t, "policy:\n  pr_budget_usd: 5\n"), "--history", history},
	} {
		if code, stdout, stderr := execute(t, append([]string{"policy", "--rows", realRows}, args...)...); code != 0 || stdout == "" || stderr != "" {
			t.Errorf("%s: exit %d: %q %q", name, code, stdout, stderr)
		}
	}
}

// TestPolicyRefusesABaselineLineWithoutItsCheckRun refuses a --baseline file
// as --history is refused: a run without its check run cannot be placed in
// the window.
func TestPolicyRefusesABaselineLineWithoutItsCheckRun(t *testing.T) {
	directory := t.TempDir()
	unnamed := strings.Replace(earlierReports(t, 100, 1)[0], `"check_run_id": 100,`, "", 1)
	path := writeObjects(t, filepath.Join(directory, "merged.jsonl"), []string{unnamed})
	code, stdout, stderr := execute(t, "policy", "--rows", realRows, "--config", filepath.Join(directory, "none.yml"), "--baseline", path)
	if code != exitFailure || stdout != "" || !strings.Contains(stderr, "--baseline line 1: the result object names no check run") {
		t.Errorf("exit %d: %q %q", code, stdout, stderr)
	}
}
