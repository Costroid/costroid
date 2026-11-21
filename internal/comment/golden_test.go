package comment

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focusfile"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/reason"
	"github.com/Costroid/costroid/internal/reconcile"
)

// goldenDir holds one directory per comment state, each with the four things
// a report writes for it.
var goldenDir = filepath.Join("testdata", "golden")

// The files of one state's golden.
const (
	goldenComment = "comment.md"
	goldenSummary = "summary.md"
	goldenCheck   = "check.md"
	goldenOutputs = "outputs.txt"
)

// The times the golden verdicts are read at: inside the seven-day warn-only
// period that began at goldenStart, and after it ended.
var (
	goldenStart     = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	insidePeriod    = time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	afterPeriod     = time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	gitlabNamespace = "4815162342"
	underBudget     = "policy:\n  run_budget_usd: \"5\"\n"
	overBudget      = "policy:\n  run_budget_usd: \"0.001\"\n"
	amountInWords   = regexp.MustCompile(`\b[A-Z]{3} [0-9]`)
	goldenRowsFile  = filepath.Join("testdata", "rows.jsonl")
)

// goldenVerdict evaluates the default pack over the golden rows under a
// costroid.yml read at the base commit.
func goldenVerdict(t *testing.T, config string, trust policy.Trust, at time.Time) *policy.Verdict {
	t.Helper()
	data, err := os.ReadFile(goldenRowsFile)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := policy.ReadUsage(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	return verdictOver(t, usage, config, trust, at)
}

// verdictOver evaluates the default pack over a run's usage under a
// costroid.yml read at the base commit.
func verdictOver(t *testing.T, usage policy.Usage, config string, trust policy.Trust, at time.Time) *policy.Verdict {
	t.Helper()
	verdict, err := policy.Evaluate(context.Background(), policy.Request{
		Usage:    usage,
		Trust:    trust,
		Evidence: policy.GatewayMeasured,
		Config:   policy.ConfigFile{Data: []byte(config), Found: true, Ref: exampleBase, FromBase: true},
		Start:    policy.Start{At: goldenStart},
		At:       at,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &verdict
}

// rulesVerdict evaluates the default pack over the golden rows on the Action
// path, under a costroid.yml read at the base commit, with what the report
// read of earlier runs.
func rulesVerdict(t *testing.T, config policy.ConfigFile, at time.Time, baseline *policy.Baseline, earlier *policy.Lifetime) *policy.Verdict {
	t.Helper()
	data, err := os.ReadFile(goldenRowsFile)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := policy.ReadUsage(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	config.Ref, config.FromBase = exampleBase, true
	verdict, err := policy.Evaluate(context.Background(), policy.Request{
		Usage:    usage,
		Trust:    policy.Unverified,
		Config:   config,
		Start:    policy.Start{At: goldenStart},
		At:       at,
		Baseline: baseline,
		Lifetime: earlier,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &verdict
}

// p95Config sets the P95 limit at 1.5 times the P95 of the agent's latest
// runs.
const p95Config = "policy:\n  max_p95_factor: \"1.5\"\n"

// p95Verdict evaluates the default pack under p95Config over a run's usage,
// with a baseline of runs earlier runs whose P95 is p95, in USD, vouched for
// as the rows are.
func p95Verdict(t *testing.T, usage policy.Usage, trust policy.Trust, at time.Time, runs int, p95 string) *policy.Verdict {
	t.Helper()
	verdict, err := policy.Evaluate(context.Background(), policy.Request{
		Usage:    usage,
		Trust:    trust,
		Evidence: policy.GatewayMeasured,
		Config:   policy.ConfigFile{Data: []byte(p95Config), Found: true, Ref: exampleBase, FromBase: true},
		Start:    policy.Start{At: goldenStart},
		At:       at,
		Baseline: &policy.Baseline{Runs: runs, MedianUSD: decimal.MustParse(p95), P95USD: decimal.MustParse(p95), Trust: trust},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &verdict
}

// p95States are the P95 limit's states, each shown and withheld: a run over
// the limit once the warn-only period ended and inside it, within it on rows
// the agent's job wrote and on rows a ledger checked, and a history of 7
// runs, which is too short for the rule to run.
func p95States(t *testing.T) map[string]Input {
	t.Helper()
	data, err := os.ReadFile(goldenRowsFile)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := policy.ReadUsage(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	checked := realInput(t)
	checked.Ledger = ledgerOf(t, sampleFields())
	attributed := policy.Usage{Rows: 1, Currencies: []policy.CurrencyUsage{{Currency: "USD", ListCost: decimal.MustParse("0.1"), Rows: 1, Models: map[string]int{"gpt-5.5": 1}}}}
	checked.Verdict = p95Verdict(t, attributed, policy.TokenVerified, insidePeriod, policy.MinP95Runs, "0.1")
	shown := map[string]Input{
		"p95-over":           withVerdict(t, p95Verdict(t, usage, policy.Unverified, afterPeriod, policy.MinP95Runs, "0.0006")),
		"p95-warn-only":      withVerdict(t, p95Verdict(t, usage, policy.Unverified, insidePeriod, policy.MinP95Runs, "0.0006")),
		"p95-within":         withVerdict(t, p95Verdict(t, usage, policy.Unverified, insidePeriod, policy.MinP95Runs, "0.0012")),
		"p95-within-checked": checked,
		"p95-short-history":  withVerdict(t, p95Verdict(t, usage, policy.Unverified, insidePeriod, 7, "0.0012")),
	}
	states := map[string]Input{}
	for name, input := range shown {
		states[name], states[name+"-withheld"] = input, withheld(input)
	}
	return states
}

// settings is a costroid.yml found at the base commit.
func settings(document string) policy.ConfigFile {
	return policy.ConfigFile{Data: []byte(document), Found: true}
}

// withVerdict is the recorded subagent run's report under a verdict.
func withVerdict(t *testing.T, verdict *policy.Verdict) Input {
	t.Helper()
	input := realInput(t)
	input.Verdict = verdict
	return input
}

// withheld is the input with its amounts off, as on a public repository whose
// costroid.yml sets comment.show_cost to false.
func withheld(input Input) Input {
	input.Amounts = AmountsOff
	return input
}

// noFigures is a report with no figures for a reason, on a pull request.
func noFigures(state State, reason Reason) Input {
	id := exampleCheckRun
	return Input{State: state, Reason: reason, HeadSHA: sampleHead, Installation: exampleInstallation, CheckRunID: &id, Amounts: AmountsShown, RunURL: exampleRun}
}

// emptyRun is a run that wrote an empty rows file and stated why.
func emptyRun(why reason.Reason) Input {
	input := noFigures(NoRows, EmptyRowsFile)
	input.RunReason = why
	switch why {
	case reason.AgentError:
		input.AgentError = adapter.AgentErrorAuthentication
	case reason.ReaderRefused:
		input.ReaderLine = "claude-code: the execution file ends inside its array"
	}
	return input
}

// manyFigures is a rows file of more lines than the table holds: one figure
// per currency, each a row of one million output tokens.
func manyFigures(t *testing.T) Input {
	t.Helper()
	var rows []rowFields
	for index := range maxFigures + 3 {
		fields := sampleFields()
		fields.currency = fmt.Sprintf("X%c%c", 'A'+index/26, 'A'+index%26)
		rows = append(rows, fields)
	}
	input := realInput(t)
	input.Ledger = ledgerOf(t, rows...)
	return input
}

// ghAwInput is the rows ingest-run wrote for the gh-aw usage artifact in
// internal/adapter/ghaw/testdata/not-recorded/claude, with the AI credits its
// run object states. gh-aw's workflow sets no budget for the report.
func ghAwInput(t *testing.T) Input {
	t.Helper()
	return statedRunInput(t, "gh-aw-rows.jsonl", adapter.Stated{Unit: adapter.UnitAICredits, Quantity: "7.47", Source: "gh-aw"})
}

// The rows costroid run wrote for the recordings of OpenCode 1.19.4 and
// Copilot CLI 1.0.103 replayed, with the figure each agent stated in its run
// object. The run's budget is not the report's.
func openCodeInput(t *testing.T) Input {
	t.Helper()
	return statedRunInput(t, "opencode-rows.jsonl", adapter.Stated{Unit: "USD", Quantity: "0.00153317", Source: "opencode"})
}

func copilotInput(t *testing.T) Input {
	t.Helper()
	return statedRunInput(t, "copilot-rows.jsonl", adapter.Stated{Unit: "nano-AIU", Quantity: "0", Source: "copilot"})
}

// statedRunInput is the report on a rows file under testdata, beside the
// figure its agent stated, with no budget.
func statedRunInput(t *testing.T, rows string, stated adapter.Stated) Input {
	t.Helper()
	input := rowsFileInput(t, rows)
	input.AgentStated = &stated
	return input
}

// rowsFileInput is the report on a rows file under testdata, with no budget.
func rowsFileInput(t *testing.T, rows string) Input {
	t.Helper()
	file, err := os.Open(filepath.Join("testdata", rows))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	ledger, err := ReadRows(file)
	if err != nil {
		t.Fatal(err)
	}
	input := realInput(t)
	input.Ledger, input.Budget = ledger, nil
	return input
}

// subscriptionInput is the rows ingest-run wrote for the recorded
// claude-code-action run on claude_code_oauth_token,
// internal/adapter/claudecode/testdata/2.1.316/action-subscription.
func subscriptionInput(t *testing.T) Input {
	t.Helper()
	return rowsFileInput(t, "subscription-rows.jsonl")
}

// unheldInput is one row nothing held, given a cap of USD 0.05 it went over,
// under a verdict that read config offline, as report --preview reads
// costroid.yml in the working directory.
func unheldInput(t *testing.T, config policy.ConfigFile) Input {
	t.Helper()
	unheld := sampleFields()
	unheld.enforcement = "none"
	input := capInput(ledgerOf(t, unheld), "0.05")
	input.HeadSHA, input.RunURL = sampleHead, exampleRun
	usage := policy.Usage{Rows: 1, Currencies: []policy.CurrencyUsage{{Currency: "USD", ListCost: decimal.MustParse("0.1"), Rows: 1, Models: map[string]int{"gpt-5.5": 1}}}}
	verdict, err := policy.Evaluate(context.Background(), policy.Request{Usage: usage, Trust: policy.Unverified, Config: config, At: insidePeriod})
	if err != nil {
		t.Fatal(err)
	}
	input.Verdict = &verdict
	return input
}

// goldenStates names every state the goldens hold. A state's name is its
// directory under testdata/golden.
func goldenStates(t *testing.T) map[string]Input {
	t.Helper()
	neutral := withVerdict(t, goldenVerdict(t, underBudget, policy.Unverified, insidePeriod))
	// A pass needs rows a ledger checked and attributed, and spend the gateway
	// measured, as serve reads them.
	// Rows the agent's job wrote never are, so the usage is the ledger's: the
	// one row of sampleFields, attributed.
	pass := realInput(t)
	pass.Ledger = ledgerOf(t, sampleFields())
	attributed := policy.Usage{Rows: 1, Currencies: []policy.CurrencyUsage{{Currency: "USD", ListCost: decimal.MustParse("0.1"), Rows: 1, Models: map[string]int{"gpt-5.5": 1}}}}
	pass.Verdict = verdictOver(t, attributed, underBudget, policy.TokenVerified, insidePeriod)
	warn := withVerdict(t, goldenVerdict(t, overBudget, policy.Unverified, insidePeriod))
	fail := withVerdict(t, goldenVerdict(t, overBudget, policy.Unverified, afterPeriod))
	for name, input := range map[string]Input{"neutral": neutral, "pass": pass, "warn": warn, "fail": fail} {
		if input.Conclusion() != map[string]string{"neutral": policy.Neutral, "pass": policy.Success, "warn": policy.Neutral, "fail": policy.Failure}[name] {
			t.Fatalf("the %s verdict concludes %s", name, input.Conclusion())
		}
	}
	if !slices.ContainsFunc(warn.Verdict.Results, func(r policy.Result) bool { return r.Outcome == policy.Warn }) {
		t.Fatalf("the warn verdict holds no warning: %+v", warn.Verdict.Results)
	}

	states := map[string]Input{
		"neutral-shown": neutral, "neutral-withheld": withheld(neutral),
		"pass-shown": pass, "pass-withheld": withheld(pass),
		"warn-shown": warn, "warn-withheld": withheld(warn),
		"fail-shown": fail, "fail-withheld": withheld(fail),
	}

	unsettled := neutral
	unsettled.Amounts = AmountsUnsettled
	states["withheld-unsettled"] = unsettled

	gitlab := neutral
	gitlab.AccountHost, gitlab.Installation, gitlab.CheckRunID = AccountGitLab, gitlabNamespace, nil
	states["gitlab-note"] = gitlab
	states["gitlab-note-withheld"] = withheld(gitlab)

	onPlatform := neutral
	onPlatform.Platform, onPlatform.Repository, onPlatform.PullRequest = "https://app.costroid.com", "github.com/example/widgets", 7
	states["platform-pull"] = onPlatform

	for _, reason := range []Reason{NoRowsFile, NotAFile, UnreadRowsFile, ForkNoRows} {
		states["no-figures-"+string(reason)] = noFigures(Unreadable, reason)
	}
	invalid := noFigures(Unreadable, InvalidRow)
	invalid.ReasonLine = 3
	states["no-figures-"+string(InvalidRow)] = invalid
	states["no-figures-"+string(EmptyRowsFile)] = noFigures(NoRows, EmptyRowsFile)
	skipped := noFigures(NoRows, AgentSkipped)
	yes := true
	skipped.AgentSkipped = &yes
	states["no-figures-"+string(AgentSkipped)] = skipped
	for _, why := range []reason.Reason{reason.NoUsage, reason.ConcurrentRun, reason.ReaderRefused, reason.AgentError} {
		states["empty-run-"+string(why)] = emptyRun(why)
	}

	stopped := capInput(codexStoppedLedger(t), "0.001")
	stopped.HeadSHA, stopped.RunURL = sampleHead, exampleRun
	states["codex-stopped-overshoot"] = stopped
	states["codex-stopped-overshoot-withheld"] = withheld(stopped)

	euro := sampleFields()
	euro.currency = "EUR"
	two := realInput(t)
	two.Ledger = ledgerOf(t, sampleFields(), euro)
	states["two-currencies"] = two
	states["two-currencies-withheld"] = withheld(two)

	fallback := realInput(t)
	fallback.Ledger = ledgerOf(t, sampleFields(), fallbackFields())
	states["fallback-price"] = fallback
	states["fallback-price-withheld"] = withheld(fallback)

	states["figures-omitted"] = manyFigures(t)
	states["figures-omitted-withheld"] = withheld(manyFigures(t))

	reconciled := neutral
	reconciled.Reconciliations = []Reconciliation{
		{Credential: Clean("apikey_01Example"), Day: "2026-11-05", State: reconcile.Matched},
		{Credential: Clean("apikey_02Example"), Day: "2026-11-05", State: reconcile.TokensMiss},
		{Credential: Clean("apikey_03Example"), Day: "2026-11-05", State: reconcile.Pending},
	}
	states["reconciliation"] = reconciled
	states["reconciliation-withheld"] = withheld(reconciled)

	lifetime := neutral
	lifetime.Lifetime = &Lifetime{
		Figures: []LifetimeFigure{
			{Currency: "EUR", Basis: "measured", Cost: decimal.MustParse("0.25"), Reports: 1},
			{Currency: "USD", Basis: "list_equivalent", Cost: decimal.MustParse("0.00323945"), Reports: 2, Undigested: true},
		},
		Capped: true,
		Unread: 1,
	}
	states["lifetime"] = lifetime
	states["lifetime-withheld"] = withheld(lifetime)

	failed := neutral
	failed.AgentError = adapter.AgentErrorRateLimit
	states["agent-error-beside-figures"] = failed

	credits := ghAwInput(t)
	states["gh-aw-stated-credits"] = credits
	states["gh-aw-stated-credits-withheld"] = withheld(credits)
	openCode := openCodeInput(t)
	states["opencode-stated-usd"] = openCode
	states["opencode-stated-usd-withheld"] = withheld(openCode)
	copilot := copilotInput(t)
	states["copilot-stated-nano-aiu"] = copilot
	states["copilot-stated-nano-aiu-withheld"] = withheld(copilot)

	states[policyBudgetNoCap] = policyBudgetWithoutCap(t)

	subscription := subscriptionInput(t)
	states["subscription"] = subscription
	states["subscription-withheld"] = withheld(subscription)

	// A recorded run's figures, shown on a pull request that spent nothing.
	recorded := neutral
	recorded.Recorded = &Recorded{RunID: 42368537950, Date: "2026-11-07"}
	states["recorded-run"] = recorded
	states["recorded-run-withheld"] = withheld(recorded)

	// A FOCUS cost file's rows beside the agent's: one line per currency and
	// period, never added to the agent figure, and the same rows alone.
	estimated := withEstimate(t, neutral, twoCurrencies)
	states["infrastructure"] = estimated
	states["infrastructure-withheld"] = withheld(estimated)
	estimatesAlone, err := ReadRows(strings.NewReader(estimateLines(t, twoSubAccounts)))
	if err != nil {
		t.Fatal(err)
	}
	withoutAgentRows := noFigures(NoRows, EmptyRowsFile)
	withoutAgentRows.Ledger = estimatesAlone
	states["infrastructure-without-agent-rows"] = withoutAgentRows
	// The estimate the workflow named was not there, as on a fork's pull
	// request: one line says so, beside the agent's figures.
	unavailable := neutral
	note := focusfile.NotAvailable(focusfile.DefaultSource)
	unavailable.EstimateUnavailable = &note
	states["infrastructure-not-available"] = unavailable

	// A reviewer approved the failure: the check concludes neutral on the
	// approved commit, and the sentence names the reviewer.
	approved := fail
	approved.Approval = &Approval{Approver: "octocat", HeadSHA: sampleHead, At: time.Date(2026, 11, 12, 9, 30, 0, 0, time.UTC)}
	states["approved-failure"] = approved
	states["approved-failure-withheld"] = withheld(approved)

	// The action refused the artifact that carried the rows: no rule runs and
	// the check fails.
	refused := noFigures(Unreadable, NoRowsFile)
	refused.Refused = &RefusedArtifact{Count: 2, Entries: []string{"notes.txt", "extra/rows.jsonl"}}
	states["artifact-refused"] = refused

	// costroid.yml could not be read at the base commit, so no rule ran.
	states["config-unread"] = withVerdict(t, rulesVerdict(t, policy.ConfigFile{Unread: errors.New("the host did not answer")}, insidePeriod, nil, nil))

	// A rule other than the run budget decides, each with its own noun: the
	// run cost limit, an increase over the baseline, the pull request budget,
	// the monthly budget and the model list.
	states["limit-over"] = withVerdict(t, rulesVerdict(t, settings("policy:\n  max_run_cost_usd: \"0.001\"\n"), afterPeriod, nil, nil))
	baseline := &policy.Baseline{Runs: 12, MedianUSD: decimal.MustParse("0.0012"), P95USD: decimal.MustParse("0.002"), Trust: policy.Unverified}
	states["increase-within"] = withVerdict(t, rulesVerdict(t, settings("policy:\n  max_increase_usd: \"0.01\"\n"), insidePeriod, baseline, nil))
	overPullRequest := withVerdict(t, rulesVerdict(t, settings("policy:\n  pr_budget_usd: \"0.003\"\n"), afterPeriod, nil, &policy.Lifetime{USD: decimal.MustParse("0.001619725"), Trust: policy.Unverified}))
	overPullRequest.Lifetime = &Lifetime{Figures: []LifetimeFigure{{Currency: "USD", Basis: "list_equivalent", Cost: decimal.MustParse("0.00323945"), Reports: 2}}}
	states["pull-request-budget-over"] = overPullRequest
	states["monthly-budget-not-decided"] = withVerdict(t, rulesVerdict(t, settings("policy:\n  monthly_budget_usd: \"100\"\n"), insidePeriod, nil, nil))
	states["model-off-list"] = withVerdict(t, rulesVerdict(t, settings("policy:\n  models:\n    - gpt-*\n"), insidePeriod, nil, nil))
	// Two rules fail: the sentence names the first the pack lists, and the
	// other follows in the body.
	states["two-rules-fail"] = withVerdict(t, rulesVerdict(t, settings("policy:\n  max_run_cost_usd: \"0.001\"\n  models:\n    - gpt-*\n"), afterPeriod, nil, nil))

	maps.Copy(states, p95States(t))
	maps.Copy(states, packStates(t))

	// A fork's own rows fail a rule. The follow-up holds the check at neutral,
	// and the sentence says so with the rule's own noun, shown and withheld.
	// Inside the warn-only period it says the same, and never that the check
	// will fail: a fork's check is neutral after the period too.
	states["fork-failure-held"] = fail.OnForkRows()
	states["fork-failure-held-withheld"] = withheld(fail.OnForkRows())
	states["fork-limit-held"] = states["limit-over"].OnForkRows()
	states["fork-failure-held-warn-only"] = warn.OnForkRows()

	// On GitLab a change is a merge request, in the sentence and in each line
	// under it. GitLab keeps no check runs to add a change's total from, so a
	// budget on that total is not decided there.
	onGitLab := withVerdict(t, rulesVerdict(t, settings("policy:\n  pr_budget_usd: \"0.003\"\n"), insidePeriod, nil, nil))
	onGitLab.AccountHost, onGitLab.Installation, onGitLab.CheckRunID = AccountGitLab, gitlabNamespace, nil
	states["gitlab-note-request-budget"] = onGitLab

	// The rows name a credential, a pull request and a head commit, as a run
	// in CI does, so the attribution rule warns: its line is folded, under a
	// budget and with no rule set.
	joinable := func(verdict *policy.Verdict) *policy.Verdict {
		warned := *verdict
		warned.Results = append(slices.Clone(verdict.Results), policy.Result{Rule: policy.RuleAttribution, Outcome: policy.Warn, Reason: policy.ReasonJoinableRows})
		warned.JoinableRows = 19
		return &warned
	}
	states["joinable-rows"] = withVerdict(t, joinable(neutral.Verdict))
	states["joinable-rows-no-budget"] = withVerdict(t, joinable(goldenVerdict(t, "", policy.Unverified, insidePeriod)))

	// A report states no cap for a run nothing held. A preview compares the
	// cost with the cap it was given, and names the costroid.yml it read.
	states["no-cap-held"] = unheldInput(t, policy.ConfigFile{})
	for name, config := range map[string]policy.ConfigFile{
		"preview-no-cap-held":    {},
		"preview-config-invalid": {Data: []byte("policy: [\n"), Found: true},
	} {
		preview := unheldInput(t, config)
		preview.Preview = true
		states[name] = preview
	}
	return states
}

// rendered is what a report writes for one state: the sticky comment, the job
// summary, the check run's title, summary and text, and the step's outputs.
type rendered struct {
	comment, summary, check string
	outputs                 []Output
}

func renderState(t *testing.T, input Input) rendered {
	t.Helper()
	body, err := CommentWithLifetime(input, input.Lifetime)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := Summary(input)
	if err != nil {
		t.Fatal(err)
	}
	check, err := Check(input)
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := Outputs(input)
	if err != nil {
		t.Fatal(err)
	}
	return rendered{
		comment: body,
		summary: summary,
		check:   "# Title\n\n" + check.Title + "\n\n# Summary\n\n" + check.Summary + "\n\n# Text\n\n" + check.Text + "\n",
		outputs: outputs,
	}
}

func (r rendered) files() map[string]string {
	var outputs strings.Builder
	for _, output := range r.outputs {
		outputs.WriteString(output.Name + "=" + output.Value + "\n")
	}
	return map[string]string{goldenComment: r.comment, goldenSummary: r.summary, goldenCheck: r.check, goldenOutputs: outputs.String()}
}

// goldenProblems lists every invariant a state's rendering breaks: the comment
// opens with the one marker and no other text holds it, the check's title is
// plain text inside the status limit and opens the comment and the job
// summary, each text GitHub takes stays inside its limit, no output holds a
// line break, and a withheld comment or summary holds no amount after a
// currency code.
func goldenProblems(input Input, r rendered) []string {
	var problems []string
	if strings.Count(r.comment, Marker) != 1 || !strings.HasPrefix(r.comment, Marker+"\n") {
		problems = append(problems, fmt.Sprintf("the comment holds the marker %d times, or does not open with it", strings.Count(r.comment, Marker)))
	}
	for name, text := range map[string]string{goldenSummary: r.summary, goldenCheck: r.check} {
		if strings.Contains(text, Marker) {
			problems = append(problems, name+" holds the comment's marker")
		}
	}
	title, _, _ := strings.Cut(strings.TrimPrefix(r.check, "# Title\n\n"), "\n")
	if length := utf8.RuneCountInString(title); length == 0 || length > StatusLimit || strings.ContainsAny(title, markup) {
		problems = append(problems, fmt.Sprintf("the check's title is %d characters, or holds markup: %q", length, title))
	}
	if opening := "**" + title + "**\n"; !strings.HasPrefix(r.comment, Marker+"\n"+opening) || !strings.HasPrefix(r.summary, opening) {
		problems = append(problems, "the comment or the job summary does not open on the check's title")
	}
	for name, text := range map[string]string{goldenComment: r.comment, goldenSummary: r.summary, goldenCheck: r.check} {
		if len(text) > githubLimit {
			problems = append(problems, fmt.Sprintf("%s is %d bytes, over %d", name, len(text), githubLimit))
		}
	}
	for _, output := range r.outputs {
		if strings.ContainsAny(output.Value, "\r\n") {
			problems = append(problems, "output "+output.Name+" holds a line break")
		}
	}
	if input.Amounts != AmountsShown {
		for name, text := range map[string]string{goldenComment: r.comment, goldenSummary: r.summary} {
			if found := amountInWords.FindString(text); found != "" {
				problems = append(problems, fmt.Sprintf("withheld, %s holds %q", name, found))
			}
		}
	}
	slices.Sort(problems)
	return problems
}

// TestGolden renders every comment state and holds each of its four files to
// the golden under testdata/golden/<state>. A change to what a reader sees
// fails it until it is rerun with -update, so the change shows in the diff of
// the pull request that makes it. Every rendering also keeps the invariants
// goldenProblems states, whatever the golden says.
func TestGolden(t *testing.T) {
	states := goldenStates(t)
	for name, input := range states {
		t.Run(name, func(t *testing.T) {
			r := renderState(t, input)
			for _, problem := range goldenProblems(input, r) {
				t.Error(problem)
			}
			dir := filepath.Join(goldenDir, name)
			for file, text := range r.files() {
				path := filepath.Join(dir, file)
				if *update {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				saved, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("%v, rerun with -update", err)
				}
				if string(saved) != text {
					t.Errorf("%s is stale, rerun with -update", path)
				}
			}
		})
	}
	entries, err := os.ReadDir(goldenDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if _, known := states[entry.Name()]; known {
			continue
		}
		if *update {
			if err := os.RemoveAll(filepath.Join(goldenDir, entry.Name())); err != nil {
				t.Fatal(err)
			}
		} else {
			t.Errorf("%s holds no state's golden, rerun with -update", filepath.Join(goldenDir, entry.Name()))
		}
	}
}

// TestGoldenInvariantsBite feeds goldenProblems a rendering that breaks each
// invariant once, so a rule that stopped checking would show here.
func TestGoldenInvariantsBite(t *testing.T) {
	input := realInput(t)
	good := renderState(t, input)
	if problems := goldenProblems(input, good); len(problems) != 0 {
		t.Fatalf("a good rendering breaks %q", problems)
	}
	broken := []struct {
		name  string
		input Input
		edit  func(*rendered)
	}{
		{"a second marker", input, func(r *rendered) { r.comment += Marker }},
		{"no marker", input, func(r *rendered) { r.comment = strings.TrimPrefix(r.comment, Marker) }},
		{"a marker in the summary", input, func(r *rendered) { r.summary = Marker + r.summary }},
		{"a title past the status limit", input, func(r *rendered) {
			long := strings.Repeat("x", StatusLimit+1)
			r.check, r.comment, r.summary = "# Title\n\n"+long+"\n", Marker+"\n**"+long+"**\n", "**"+long+"**\n"
		}},
		{"a title that holds markup", input, func(r *rendered) {
			r.check, r.comment, r.summary = "# Title\n\n`x`\n", Marker+"\n**`x`**\n", "**`x`**\n"
		}},
		{"a comment that opens on another line", input, func(r *rendered) {
			r.comment = Marker + "\n### Costroid\n" + strings.TrimPrefix(r.comment, Marker+"\n")
		}},
		{"a job summary that opens on another line", input, func(r *rendered) { r.summary = "### Costroid\n\n" + r.summary }},
		{"a comment over the limit", input, func(r *rendered) { r.comment += strings.Repeat("x", githubLimit) }},
		{"a check over the limit", input, func(r *rendered) { r.check += strings.Repeat("x", githubLimit) }},
		{"a line break in an output", input, func(r *rendered) { r.outputs[0].Value += "\nx" }},
		{"an amount in a withheld comment", withheld(input), func(r *rendered) { r.comment += "USD 1" }},
		{"an amount in a withheld summary", withheld(input), func(r *rendered) { r.summary += "EUR 0.5" }},
	}
	for _, tc := range broken {
		r := renderState(t, tc.input)
		r.outputs = slices.Clone(r.outputs)
		if tc.input.Amounts != AmountsShown {
			if problems := goldenProblems(tc.input, r); len(problems) != 0 {
				t.Fatalf("%s: the withheld rendering breaks %q before the edit", tc.name, problems)
			}
		}
		tc.edit(&r)
		if problems := goldenProblems(tc.input, r); len(problems) == 0 {
			t.Errorf("%s passed every invariant", tc.name)
		}
	}
}
