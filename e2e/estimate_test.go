//go:build e2e

package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/scm/scmtest"
)

const (
	estimateRows = 2
	// notAvailable is the comment's line for an estimate that was not there.
	notAvailable = "Infrastructure estimate: not available (the step that writes it did not run or wrote nothing, as on a fork's pull request that gets no secret)."
)

// month is the period an estimator states: the month after the one the test
// runs in, as its first instant, the next month's first instant, and the
// name the comment gives it.
type month struct {
	start, end, name string
}

func nextMonth() month {
	now := time.Now().UTC()
	first := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	const instant = "2006-01-02T15:04:05Z"
	return month{first.Format(instant), first.AddDate(0, 1, 0).Format(instant), first.Format("2006-01")}
}

// file is a FOCUS cost file for the month, as a workflow's own estimator
// step writes it. Every name and figure in it is synthetic.
func (m month) file() string {
	return "BillingCurrency,ChargePeriodStart,ChargePeriodEnd,ListCost,ResourceName,ServiceName,SubAccountId\n" +
		"EUR," + m.start + "," + m.end + ",25.488,aws_instance.web,Amazon EC2,example-account-a\n" +
		"USD," + m.start + "," + m.end + ",1000,google_compute_instance.batch,Compute Engine,example-project-b\n"
}

// TestAnInfrastructureEstimateTravelsApartFromTheAgentsCost carries a FOCUS
// cost file through the action's own step, add-infrastructure.sh, into the
// agent's rows. The report states the estimate in its own section and in the
// machine block's estimates, apart from the agent's figure. A second run,
// whose file is not there, states that the estimate is not available. The
// ingest stage sends the rows to the service in its container, which stores
// the estimate as its own total and keeps it out of the pull request's
// budget.
func TestAnInfrastructureEstimateTravelsApartFromTheAgentsCost(t *testing.T) {
	services := compose(t)
	key := services.createKey()
	job := newWorkspace(t)
	runAgent(t, job)

	ahead := nextMonth()
	estimated := job.estimateStep(t, "estimated", ahead, job.write)
	report := job.report(t, estimated)
	for _, want := range []string{
		"USD " + recordedTotal,
		"Infrastructure, estimated: EUR 25.488 for " + ahead.name + ", from `infrastructure`.",
		"Infrastructure, estimated: USD 1000 for " + ahead.name + ", from `infrastructure`.",
		"The agent figure and these infrastructure figures are not added together, and no currency is added to another.",
	} {
		if !strings.Contains(report.comment, want) {
			t.Errorf("comment: want %q in:\n%s", want, report.comment)
		}
	}
	for name, object := range map[string]string{"check run": report.check, "comment": report.copied} {
		want := []estimateEntry{
			{Source: "infrastructure", Currency: "EUR", Start: ahead.start, End: ahead.end, Basis: "estimate", ListCost: "25.488", Rows: 1},
			{Source: "infrastructure", Currency: "USD", Start: ahead.start, End: ahead.end, Basis: "estimate", ListCost: "1000", Rows: 1},
		}
		holdsTheAgentsFigure(t, name, object, want)
	}

	unavailable := job.estimateStep(t, "unavailable", ahead, func(string, string, os.FileMode) {})
	report = job.report(t, unavailable)
	if !strings.Contains(report.comment, notAvailable) || strings.Contains(report.comment, "Infrastructure, estimated") {
		t.Errorf("comment: want %q alone in:\n%s", notAvailable, report.comment)
	}
	holdsTheAgentsFigure(t, "check run", report.check, []estimateEntry{{Source: "infrastructure", Reason: "no_file"}})

	sendEstimate(t, services, key, estimated)
	readEstimateApart(t, services, key)
}

// estimateStep runs the action's step on a copy of the agent's rows and run
// object in a directory of its own. place writes the estimator's file for the
// month, or writes nothing, as an estimator step that did not run. The step
// is optional, as the recipe sets it.
func (w *workspace) estimateStep(t *testing.T, name string, ahead month, place func(path, content string, mode os.FileMode)) string {
	t.Helper()
	directory := filepath.Join(w.directory, name)
	rows := filepath.Join(directory, filepath.Base(w.rows))
	w.copy(w.rows, rows)
	w.copy(strings.TrimSuffix(w.rows, ".jsonl")+".run.json", strings.TrimSuffix(rows, ".jsonl")+".run.json")
	file := filepath.Join(directory, "infrastructure.csv")
	place(file, ahead.file(), 0o644)
	script, err := filepath.Abs(filepath.Join("..", "action", "add-infrastructure.sh"))
	if err != nil {
		t.Fatal(err)
	}
	step := exec.Command("bash", script)
	step.Env = append(slices.Clone(w.env),
		"PATH="+filepath.Dir(w.binary)+string(os.PathListSeparator)+os.Getenv("PATH"),
		"COSTROID_INFRASTRUCTURE_FILE="+file, "COSTROID_INFRASTRUCTURE_SOURCE=infrastructure",
		"COSTROID_INFRASTRUCTURE_OPTIONAL=true", "COSTROID_ROWS="+rows,
	)
	output, err := step.CombinedOutput()
	if err != nil {
		t.Fatalf("add-infrastructure.sh: %v\n%s", err, output)
	}
	_, placed := os.Stat(file)
	lines := bytes.Count(w.read(rows), []byte("\n"))
	_, noted := os.Stat(strings.TrimSuffix(rows, ".jsonl") + ".infrastructure.json")
	switch {
	case placed == nil && (lines != recordedRows+estimateRows || noted == nil):
		t.Fatalf("the step left %d rows and a note (%t):\n%s", lines, noted == nil, output)
	case placed != nil && (lines != recordedRows || noted != nil):
		t.Fatalf("with no file, the step left %d rows and no note:\n%s", lines, output)
	}
	return rows
}

func (w *workspace) read(path string) []byte {
	w.t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		w.t.Fatal(err)
	}
	return data
}

// posted is what one report posted: the comment, the result object the
// comment carries, and the check run's.
type posted struct {
	comment, copied, check string
}

var fencedObject = regexp.MustCompile("(?s)```json\n(.*?)\n```")

// report runs the report job on the rows and returns what it posted.
func (w *workspace) report(t *testing.T, rows string) posted {
	t.Helper()
	github := scmtest.New(t)
	github.Branches["main"] = base
	env := append(slices.Clone(w.env), "GITHUB_TOKEN="+github.Token, "GITHUB_API_URL="+github.Server.URL)
	code, _, stderr := w.costroid(env, "report", "--rows", rows, "--budget-usd", "5")
	if code != 0 || len(github.Checks) != 1 || len(github.Comments) != 1 {
		t.Fatalf("costroid report exit %d, %d checks, %d comments: %s", code, len(github.Checks), len(github.Comments), stderr)
	}
	body := github.Comments[0].Body
	copied := fencedObject.FindStringSubmatch(body)
	checked := fencedObject.FindStringSubmatch(github.Checks[0].Text)
	if copied == nil || checked == nil {
		t.Fatalf("no result object in the comment or the check run:\n%s\n%s", body, github.Checks[0].Text)
	}
	return posted{comment: body, copied: copied[1], check: checked[1]}
}

type estimateEntry struct {
	Source   string `json:"source"`
	Currency string `json:"currency"`
	Start    string `json:"charge_period_start"`
	End      string `json:"charge_period_end"`
	Basis    string `json:"cost_basis"`
	ListCost string `json:"list_cost"`
	Rows     int    `json:"rows"`
	Reason   string `json:"reason"`
}

// holdsTheAgentsFigure reads a result object: result/v0.10, the estimates
// wanted, and the agent's figure, cap and rows as the run alone gives them.
func holdsTheAgentsFigure(t *testing.T, name, object string, estimates []estimateEntry) {
	t.Helper()
	var block struct {
		Schema  string `json:"schema"`
		Rows    int    `json:"rows"`
		Figures []struct {
			Currency string `json:"currency"`
			ListCost string `json:"list_cost"`
		} `json:"figures"`
		Cap struct {
			Cost string `json:"cost"`
		} `json:"cap"`
		Estimates        []estimateEntry `json:"estimates"`
		EstimatesOmitted *int            `json:"estimates_omitted"`
	}
	if err := json.Unmarshal([]byte(object), &block); err != nil {
		t.Fatalf("%s: %v\n%s", name, err, object)
	}
	if block.Schema != "costroid.result/0.10" || block.EstimatesOmitted == nil || !slices.Equal(block.Estimates, estimates) {
		t.Errorf("%s: schema %s, estimates %+v, want %+v", name, block.Schema, block.Estimates, estimates)
	}
	if block.Rows != recordedRows || len(block.Figures) != 1 || block.Figures[0].Currency != "USD" || block.Figures[0].ListCost != recordedTotal || block.Cap.Cost != recordedTotal {
		t.Errorf("%s: the agent's figure moved: %s", name, object)
	}
}

// sendEstimate sends the rows with the estimate, the agent's and the
// estimate's, as the ingest stage sends them, and reads the estimate's back
// from the database.
func sendEstimate(t *testing.T, services *stack, key, rows string) {
	t.Helper()
	data, err := os.ReadFile(rows)
	if err != nil {
		t.Fatal(err)
	}
	status, body := services.request(http.MethodPost, "/v1/rows", key, data)
	if status != http.StatusOK {
		t.Fatalf("POST /v1/rows: %d %s", status, body)
	}
	var answer struct {
		Accepted int `json:"accepted"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		t.Fatal(err)
	}
	if answer.Accepted != recordedRows+estimateRows {
		t.Errorf("accepted %d rows, want %d: %s", answer.Accepted, recordedRows+estimateRows, body)
	}
	var stored bytes.Buffer
	query := `SELECT document->>'BillingCurrency', document->>'ListCost', document->>'EffectiveCost' FROM focus_rows WHERE document->'Tags'->>'costroid/cost_basis' = 'estimate' ORDER BY 1`
	if out, err := services.docker(&stored, "exec", "--no-TTY", "postgres", "psql", "--username=postgres", "--dbname=costroid",
		"--no-align", "--tuples-only", "--field-separator=,", "--command", query); err != nil {
		t.Fatalf("psql: %v\n%s", err, out)
	}
	if got, want := strings.TrimSpace(stored.String()), "EUR,25.488,0\nUSD,1000,0"; got != want {
		t.Errorf("stored estimate rows read %q, want %q", got, want)
	}
}

// readEstimateApart reads the pull request back: the view states the
// estimate as its own total beside the agent's, and the verdict holds the
// pull request's budget to the agent's USD alone, not to the USD 1000
// estimate.
func readEstimateApart(t *testing.T, services *stack, key string) {
	t.Helper()
	status, body := services.request(http.MethodGet, "/v1/pulls/"+pull+"?repo="+repository, key, nil)
	if status != http.StatusOK {
		t.Fatalf("GET the per-PR view: %d %s", status, body)
	}
	var answer struct {
		Totals []struct {
			Currency  string `json:"currency"`
			CostBasis string `json:"cost_basis"`
			Label     string `json:"label"`
			ListCost  string `json:"list_cost"`
		} `json:"totals"`
		Budget struct {
			Spend []json.RawMessage `json:"spend"`
		} `json:"budget"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		t.Fatal(err)
	}
	var totals []string
	for _, total := range answer.Totals {
		totals = append(totals, strings.Join([]string{total.Currency, total.CostBasis, total.Label, total.ListCost}, " "))
	}
	slices.Sort(totals)
	want := []string{"EUR estimate Estimate 25.488", "USD estimate Estimate 1000", "USD list_equivalent List-equivalent " + recordedTotal}
	if !slices.Equal(totals, want) || len(answer.Budget.Spend) != 0 {
		t.Errorf("the view's totals %q and budget spend %d, want %q and none: %s", totals, len(answer.Budget.Spend), want, body)
	}

	config := "policy:\n  pr_budget_usd: \"5\"\n"
	request, err := json.Marshal(map[string]any{
		"config":         base64.StdEncoding.EncodeToString([]byte(config)),
		"config_ref":     base,
		"workflow_added": time.Now().Add(-8 * 24 * time.Hour).UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	status, body = services.request(http.MethodPost, "/v1/pulls/"+pull+"/verdict?repo="+repository, key, request)
	if status != http.StatusOK {
		t.Fatalf("POST the verdict: %d %s", status, body)
	}
	var verdict struct {
		Policy struct {
			Results []struct {
				Rule    string `json:"rule"`
				CostUSD string `json:"cost_usd"`
			} `json:"results"`
		} `json:"policy"`
	}
	if err := json.Unmarshal(body, &verdict); err != nil {
		t.Fatal(err)
	}
	if len(verdict.Policy.Results) != 1 || verdict.Policy.Results[0].Rule != "pr_budget" || verdict.Policy.Results[0].CostUSD != recordedTotal {
		t.Errorf("the pull request's budget read %s, want the agent's USD %s alone", body, recordedTotal)
	}
}
