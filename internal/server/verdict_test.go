package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Costroid/costroid/internal/cli"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/gateway"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm/scmtest"
	"github.com/Costroid/costroid/internal/store"
	"github.com/Costroid/costroid/internal/store/storetest"
	"github.com/Costroid/costroid/migrations"
)

// spendLedger is a memory ledger that keeps the gateway's attested spend and
// answers the per-PR lines from its rows, one line per row.
type spendLedger struct {
	*memoryLedger
	spend map[store.TenantID]map[string]store.AttestedSpend
}

// AttestSpend holds the store's rules: a key attested for the run named, one
// figure per attesting run and attempt, checked first, and one per key.
func (m *spendLedger) AttestSpend(_ context.Context, tenant store.TenantID, attested store.AttestedSpend) error {
	key := attested.Key.KeySHA256
	if minted, found := m.minted[tenant][key]; !found || !minted.SameRun(attested.Key) {
		return store.ErrNotMinted
	}
	if m.spend[tenant] == nil {
		m.spend[tenant] = map[string]store.AttestedSpend{}
	}
	for _, stored := range m.spend[tenant] {
		spoke := stored.Key.Repository == attested.Key.Repository && stored.AttestingRunID == attested.AttestingRunID &&
			stored.AttestingRunAttempt == attested.AttestingRunAttempt
		if spoke && (stored.Key.KeySHA256 != key || stored.Spend != attested.Spend) {
			return store.ErrAttestingRunUsed
		}
		if spoke {
			return nil
		}
	}
	if _, held := m.spend[tenant][key]; held {
		return store.ErrSpendHeld
	}
	m.spend[tenant][key] = attested
	return nil
}

// standing is a row id's statements a reader reads, as ledger_rows holds
// them: every verified statement, or the unverified one when no statement was
// verified. A verified statement is contested when another run stated the id,
// and outranked when an unverified statement of the id gave way to it.
func standing(statements []store.Record) (stand []store.Record, contested, outranked bool) {
	for _, statement := range statements {
		if statement.Row.Tags[focus.TagRunIdentity] == identityToken {
			stand = append(stand, statement)
		}
	}
	if len(stand) == 0 {
		return statements, false, false
	}
	return stand, len(stand) > 1, len(statements) > len(stand)
}

// statementOf is one statement that stands, and how it met another.
type statementOf struct {
	record               store.Record
	contested, outranked bool
}

func (m *spendLedger) records(tenant store.TenantID, repo, pr string) []statementOf {
	var found []statementOf
	for _, statements := range m.rows[tenant] {
		stand, contested, outranked := standing(statements)
		for _, record := range stand {
			if record.Row.Tags[store.TagRepo] == repo && record.Row.Tags[store.TagPR] == pr {
				found = append(found, statementOf{record, contested, outranked})
			}
		}
	}
	return found
}

func eligible(row focus.Row) bool {
	return row.Tags[focus.TagRunIdentity] == identityToken && row.Tags[focus.TagAttributionGrade] != gradeNone
}

func (m *spendLedger) PullRequest(_ context.Context, tenant store.TenantID, repo, pr string) ([]store.Line, error) {
	var lines []store.Line
	for _, statement := range m.records(tenant, repo, pr) {
		row := statement.record.Row
		line := store.Line{
			Currency: row.BillingCurrency, Model: row.Tags[focus.TagModel], CostSource: row.Tags[focus.TagCostSource],
			AttributionGrade: row.Tags[focus.TagAttributionGrade], RunIdentity: row.Tags[focus.TagRunIdentity],
			BudgetEligible: eligible(row), Rows: 1, ListCost: row.ListCost,
		}
		if statement.contested {
			line.Contested = 1
		}
		if statement.outranked {
			line.Outranked = 1
		}
		lines = append(lines, line)
	}
	return lines, nil
}

// GatewaySpend holds the store's rule: the figure adds up, each key once, the
// spend of the keys the pull request's eligible rows name and of every key an
// attesting run named the pull request for, and stands behind no row while a
// key so named holds no spend.
func (m *spendLedger) GatewaySpend(_ context.Context, tenant store.TenantID, repo, pr string) (int64, decimal.Decimal, error) {
	var rows int64
	keys := map[string]bool{}
	for _, statement := range m.records(tenant, repo, pr) {
		row := statement.record.Row
		if !eligible(row) || statement.record.CredentialBasis != store.CredentialMinted || row.SubAccountID == nil {
			continue
		}
		if _, found := m.spend[tenant][*row.SubAccountID]; found {
			rows++
			keys[*row.SubAccountID] = true
		}
	}
	for key, minted := range m.minted[tenant] {
		if minted.Repository != repo || !slices.Contains(minted.PullRequests.Numbers(), pr) {
			continue
		}
		if _, found := m.spend[tenant][key]; !found {
			rows = 0
		}
		keys[key] = true
	}
	var usd decimal.Decimal
	for key := range keys {
		if attested, found := m.spend[tenant][key]; found {
			usd = usd.Add(decimal.MustParse(attested.Spend))
		}
	}
	return rows, usd, nil
}

// spendWorld is a memory world that answers the per-PR lines from its rows.
// Its store keeps the gateway's spend when keeps is true.
func spendWorld(t *testing.T, keeps bool) *world {
	t.Helper()
	w, memory := memoryWorld(t)
	ledger := &spendLedger{memoryLedger: memory, spend: map[store.TenantID]map[string]store.AttestedSpend{}}
	config := w.server.Config
	config.Ledger = ledger
	if keeps {
		config.Spend = ledger
	}
	w.server = New(config)
	w.http = httptest.NewServer(w.server.Handler())
	t.Cleanup(w.http.Close)
	return w
}

// readsTheBase gives w's service the GitHub App's installation 58, which
// reaches acme/widgets. Pull request 1423's base branch main is at the commit
// verdictBody names, and holds config there, so a verdict asked through the
// installation is on settings the service read itself at the base.
func readsTheBase(t *testing.T, w *world, config string) {
	t.Helper()
	stub := scmtest.New(t)
	stub.Head, stub.BaseBranch = acmeSHA, "main"
	stub.Branches["main"] = approvalBase
	stub.Files["costroid.yml"] = config
	installations := newMemoryInstallations()
	if err := installations.AddRepositories(context.Background(), installationID, "acme", []store.Repository{{ID: 102, FullName: "acme/widgets"}}); err != nil {
		t.Fatal(err)
	}
	served := w.server.Config
	served.App = &GitHubApp{Secret: []byte(webhookSecretText), Installations: installations, Hosts: newAppFront(t, stub).hosts(t)}
	served.Logger = slog.New(slog.DiscardHandler)
	w.server = New(served)
	w.http = httptest.NewServer(w.server.Handler())
	t.Cleanup(w.http.Close)
}

// closeToken is the token of the run that closed the fixtures' key once the
// fixtures' run had completed: a run that workflow_run started on the
// protected default branch, in the gateway's environment.
func (w *world) closeToken() string {
	return w.protectedToken(closeRun, nil)
}

func (w *world) attestSpend(bearer, keyID string, body map[string]string) answer {
	w.t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		w.t.Fatal(err)
	}
	return w.send(http.MethodPost, "/v1/credentials/"+keyID+"/spend", bearer, jsonType, data)
}

func fixturesSpend(usd string) map[string]string {
	return map[string]string{"repository": acmeRepo, "run_id": acmeRun, "run_attempt": "1", "currency": "USD", "spend": usd}
}

// ended is a workflow that reached the base branch 8 days before the check,
// so the warn-only period is over.
var ended = testNow.Add(-8 * 24 * time.Hour).Format(time.RFC3339)

func verdictBody(t testing.TB, config string, fields map[string]any) []byte {
	t.Helper()
	body := map[string]any{"config_ref": strings.Repeat("1", 40), "workflow_added": ended}
	if config != "" {
		body["config"] = base64.StdEncoding.EncodeToString([]byte(config))
	}
	for key, value := range fields {
		body[key] = value
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (w *world) verdict(t *testing.T, bearer string, body []byte) verdictAnswer {
	t.Helper()
	got := w.send(http.MethodPost, "/v1/pulls/"+acmePull+"/verdict?repo="+acmeRepo, bearer, jsonType, body).want(t, http.StatusOK)
	var answer verdictAnswer
	if err := json.Unmarshal(got.body, &answer); err != nil {
		t.Fatal(err)
	}
	return answer
}

func ruleOf(t *testing.T, verdict policy.Verdict, rule string) policy.Result {
	t.Helper()
	for _, result := range verdict.Results {
		if result.Rule == rule {
			return result
		}
	}
	t.Fatalf("no %s in %+v", rule, verdict.Results)
	return policy.Result{}
}

const prBudget = "policy:\n  pr_budget_usd: \"5\"\n"

// postgresSpendWorld is the service on Postgres, keeping the gateway's spend,
// with fixturesKey attested for acmeRun's first attempt as the memory worlds
// hold it.
func postgresSpendWorld(t *testing.T) *world {
	t.Helper()
	w := postgresWorld(t).world
	config := w.server.Config
	config.Spend = config.Ledger.(*store.Store)
	w.server = New(config)
	w.http = httptest.NewServer(w.server.Handler())
	t.Cleanup(w.http.Close)
	if err := w.attest(w.protectedToken(fixturesMintRun, nil), fixturesRun(gateway.KeyID(fixturesKey))); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestServeSucceedsOnlyOnGatewayMeasuredSpend(t *testing.T) {
	t.Run("in memory", func(t *testing.T) {
		serveSucceedsOnlyOnGatewayMeasuredSpend(t, func(t *testing.T) *world { return spendWorld(t, true) })
	})
	t.Run("on Postgres", func(t *testing.T) {
		serveSucceedsOnlyOnGatewayMeasuredSpend(t, postgresSpendWorld)
	})
}

func serveSucceedsOnlyOnGatewayMeasuredSpend(t *testing.T, open func(*testing.T) *world) {
	open = atTheBaseOf(open)
	w := open(t)
	keyID := gateway.KeyID(fixturesKey)
	w.rows(w.token(), lines(t, credentialed(row("req-1", focus.Output, 1000)), credentialed(row("req-1", focus.InputUncached, 4000)))).
		want(t, http.StatusOK)
	body := verdictBody(t, prBudget, map[string]any{"installation": installationID})

	before := w.verdict(t, w.token(), body)
	if before.Policy.Conclusion != policy.Neutral || before.Policy.Trust != policy.TokenVerified || before.Policy.Evidence != policy.AgentCounts {
		t.Fatalf("agent counts on minted-key rows: %+v", before.Policy)
	}
	if got := ruleOf(t, before.Policy, policy.RulePullRequestBudget); got.Outcome != policy.Unproven || got.Reason != policy.ReasonUnmeasuredSpend {
		t.Fatalf("pr_budget before the gateway's count: %+v", got)
	}
	if before.Budget.Evidence || before.Budget.Basis != budgetBasisAgent {
		t.Fatalf("budget before the gateway's count: %+v", before.Budget)
	}
	rowsUSD := before.Budget.Spend[0].ListCost

	w.attestSpend(w.closeToken(), keyID, fixturesSpend("0.5")).want(t, http.StatusNoContent)
	measured := w.verdict(t, w.token(), body)
	if measured.Policy.Conclusion != policy.Success || measured.Policy.Evidence != policy.GatewayMeasured {
		t.Fatalf("gateway-measured spend under the budget: %+v", measured.Policy)
	}
	got := ruleOf(t, measured.Policy, policy.RulePullRequestBudget)
	if got.Outcome != policy.Pass || got.CostUSD != "0.5" || got.LimitUSD != "5" {
		t.Fatalf("pr_budget is held to the larger of the rows' %s and the gateway's 0.5: %+v", rowsUSD, got)
	}
	if !measured.Budget.Evidence || measured.Budget.Basis != budgetBasisGateway || measured.Budget.GatewayUSD != "0.5" {
		t.Fatalf("budget on the gateway's count: %+v", measured.Budget)
	}

	// The gateway's count binds where the rows understate it.
	over := open(t)
	over.rows(over.token(), lines(t, credentialed(row("req-1", focus.Output, 1000)))).want(t, http.StatusOK)
	over.attestSpend(over.closeToken(), keyID, fixturesSpend("6")).want(t, http.StatusNoContent)
	failed := over.verdict(t, over.token(), body)
	if got := ruleOf(t, failed.Policy, policy.RulePullRequestBudget); failed.Policy.Conclusion != policy.Failure ||
		got.Outcome != policy.Fail || got.Reason != policy.ReasonOverLimit || got.CostUSD != "6" {
		t.Fatalf("rows under the budget, gateway over it: %+v", failed.Policy)
	}

	// One row the gateway did not count leaves the rest agent counts.
	other := open(t)
	other.rows(other.token(), lines(t, credentialed(row("req-1", focus.Output, 1000)), row("req-2", focus.Output, 10))).want(t, http.StatusOK)
	other.attestSpend(other.closeToken(), keyID, fixturesSpend("0.5")).want(t, http.StatusNoContent)
	partly := other.verdict(t, other.token(), body)
	if partly.Policy.Conclusion != policy.Neutral || partly.Policy.Evidence != policy.AgentCounts || partly.Budget.Basis != budgetBasisAgent {
		t.Fatalf("a row outside the gateway's count: %+v %+v", partly.Policy, partly.Budget)
	}
}

// atTheBaseOf opens a world whose service reads prBudget at the base itself.
func atTheBaseOf(open func(*testing.T) *world) func(*testing.T) *world {
	return func(t *testing.T) *world {
		w := open(t)
		readsTheBase(t, w, prBudget)
		return w
	}
}

func TestTokenVerifiedAgentCountsNeverPassABudget(t *testing.T) {
	config := "policy:\n  pr_budget_usd: \"5\"\n  monthly_budget_usd: \"50\"\n  run_budget_usd: \"5\"\n  models: [\"claude-*\"]\n"
	for _, keeps := range []bool{true, false} {
		t.Run(fmt.Sprintf("the store keeps spend: %v", keeps), func(t *testing.T) {
			w := spendWorld(t, keeps)
			w.rows(w.token(), lines(t, row("req-1", focus.Output, 1000), credentialed(row("req-2", focus.Output, 1000)))).want(t, http.StatusOK)
			got := w.attestSpend(w.closeToken(), gateway.KeyID(fixturesKey), fixturesSpend("0.01"))
			if !keeps {
				if got.status != http.StatusNotImplemented || got.code(t) != codeNoSpend {
					t.Fatalf("a store that keeps no spend took an attestation: %d %s", got.status, got.body)
				}
			}
			answer := w.verdict(t, w.token(), verdictBody(t, config, nil))
			verdict := answer.Policy
			if verdict.Conclusion != policy.Neutral || verdict.Trust != policy.TokenVerified || verdict.Evidence != policy.AgentCounts {
				t.Fatalf("token-verified agent counts under every limit: %+v", verdict)
			}
			if got := ruleOf(t, verdict, policy.RulePullRequestBudget); got.Outcome != policy.Unproven || got.Reason != policy.ReasonUnmeasuredSpend {
				t.Fatalf("pr_budget: %+v", got)
			}
			if got := ruleOf(t, verdict, policy.RuleRunBudget); got.Outcome != policy.Unproven || got.Reason != policy.ReasonNoRunRows || got.LimitUSD != "5" {
				t.Fatalf("run_budget on a pull request's rows: %+v", got)
			}
			if got := ruleOf(t, verdict, policy.RuleModelAllowlist); got.Outcome != policy.Unproven || got.Reason != policy.ReasonUnmeasuredSpend {
				t.Fatalf("model_allowlist: %+v", got)
			}
			if answer.Budget.Evidence || answer.Budget.Basis != budgetBasisAgent {
				t.Fatalf("budget: %+v", answer.Budget)
			}
		})
	}

	// Over a limit, the agent's own counts are enough to show it.
	w := spendWorld(t, false)
	w.rows(w.token(), lines(t, row("req-1", focus.Output, 1000))).want(t, http.StatusOK)
	over := w.verdict(t, w.token(), verdictBody(t, "policy:\n  pr_budget_usd: \"0.000001\"\n", nil))
	if got := ruleOf(t, over.Policy, policy.RulePullRequestBudget); over.Policy.Conclusion != policy.Failure || got.Reason != policy.ReasonOverLimit {
		t.Fatalf("agent counts over the budget: %+v", over.Policy)
	}

	// The entry point holds the rule whatever the caller leaves unsaid.
	lines := []policy.LedgerLine{{Currency: "USD", ListCost: decimal.MustParse("1"), Rows: 1, Model: "claude-opus-5-5", Verified: true, Attributed: true}}
	settings := policy.ConfigFile{Data: []byte(prBudget), Found: true}
	for _, evidence := range []policy.Evidence{"", policy.AgentCounts, "gateway"} {
		verdict, err := policy.EvaluateLedger(context.Background(), policy.LedgerRequest{Lines: lines, Evidence: evidence, Config: settings, At: testNow})
		if err != nil {
			t.Fatal(err)
		}
		if verdict.Conclusion != policy.Neutral || verdict.Evidence != policy.AgentCounts {
			t.Fatalf("evidence %q: %+v", evidence, verdict)
		}
	}
}

func TestWarnPeriodEndsAtTwentiethPR(t *testing.T) {
	w := spendWorld(t, false)
	w.rows(w.token(), lines(t, row("req-1", focus.Output, 1000))).want(t, http.StatusOK)
	over := "policy:\n  pr_budget_usd: \"0.000001\"\n"
	inPeriod := testNow.Add(-24 * time.Hour).Format(time.RFC3339)
	cases := []struct {
		name       string
		fields     map[string]any
		conclusion string
		active     bool
		reason     string
	}{
		{"nineteen", map[string]any{"workflow_added": inPeriod, "measured_pull_requests": 19}, policy.Neutral, true, policy.PeriodRunning},
		{"twenty", map[string]any{"workflow_added": inPeriod, "measured_pull_requests": 20}, policy.Failure, false, policy.PeriodEnded},
		{"twenty with no start", map[string]any{"workflow_added": "", "measured_pull_requests": 20}, policy.Failure, false, policy.PeriodEnded},
		{"nineteen with no start", map[string]any{"workflow_added": "", "measured_pull_requests": 19}, policy.Neutral, true, policy.PeriodStartUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verdict := w.verdict(t, w.token(), verdictBody(t, over, tc.fields)).Policy
			period := verdict.WarnOnly
			if verdict.Conclusion != tc.conclusion || period.Active != tc.active || period.Reason != tc.reason {
				t.Fatalf("%+v", verdict)
			}
			if !period.Active && period.Ends != "" {
				t.Fatalf("a period the count ended states the date %s", period.Ends)
			}
		})
	}

	// The report states the count the same way.
	usage, err := policy.ReadUsage(strings.NewReader(`{"BillingCurrency":"USD","ListCost":"1","Tags":{}}` + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	for count, want := range map[int]string{19: policy.Neutral, 20: policy.Failure} {
		verdict, err := policy.Evaluate(context.Background(), policy.Request{
			Usage: usage, Trust: policy.Unverified, At: testNow, MeasuredPullRequests: count,
			Config: policy.ConfigFile{Data: []byte("policy:\n  run_budget_usd: \"0.5\"\n"), Found: true},
			Start:  policy.Start{At: testNow.Add(-24 * time.Hour)},
		})
		if err != nil {
			t.Fatal(err)
		}
		if verdict.Conclusion != want {
			t.Fatalf("%d measured pull requests: %+v", count, verdict)
		}
	}
}

func TestDefaultAudienceRefused(t *testing.T) {
	long := base64.StdEncoding.EncodeToString(make([]byte, minPseudonymKeyBytes))
	for _, value := range []string{
		"https://github.com/acme", "https://GitHub.com/Acme/", "github.com/acme", "https://github.com", "https://www.github.com/acme",
		"http://github.com/acme", " https://github.com/acme ",
	} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(envDatabase, "postgres://x")
			t.Setenv(envAudience, value)
			t.Setenv(envPseudonym, long)
			var stderr bytes.Buffer
			if code := Main(context.Background(), nil, new(bytes.Buffer), &stderr); code != cli.ExitCouldNot {
				t.Fatalf("exit %d", code)
			}
			if !strings.Contains(stderr.String(), "GitHub's default audience") {
				t.Fatalf("stderr %q", stderr.String())
			}
		})
	}
	for _, value := range []string{audience, "https://costroid.example", "https://github.company.example/acme", "costroid"} {
		if err := ownAudience(value); err != nil {
			t.Errorf("%s: %v", value, err)
		}
	}
}

func TestASpendAttestationIsTheClosingJobsOwn(t *testing.T) {
	w := spendWorld(t, true)
	keyID := gateway.KeyID(fixturesKey)
	other := gateway.KeyID("sk-another-run-key")
	spend := func(change func(map[string]string)) map[string]string {
		body := fixturesSpend("1")
		change(body)
		return body
	}
	cases := []struct {
		name   string
		bearer string
		key    string
		body   map[string]string
		status int
		code   string
	}{
		{"an ingest key", acmeKey, keyID, fixturesSpend("1"), http.StatusForbidden, codeAttestation},
		{"the pull request's own close job", w.pullRequestToken(nil), keyID, fixturesSpend("1"), http.StatusForbidden, codeRunUnprotected},
		{"a protected run outside the environment", w.protectedToken(closeRun, func(claims map[string]any) { delete(claims, "environment") }), keyID, fixturesSpend("1"), http.StatusForbidden, codeAttestation},
		{"another repository", w.closeToken(), keyID, spend(func(body map[string]string) { body["repository"] = "github.com/acme/gadgets" }), http.StatusForbidden, codeJoinKey},
		{"the attesting run itself", w.closeToken(), keyID, spend(func(body map[string]string) { body["run_id"] = closeRun }), http.StatusForbidden, codeJoinKey},
		{"a key never minted", w.closeToken(), other, fixturesSpend("1"), http.StatusConflict, codeNotAttested},
		{"a key minted for another run", w.closeToken(), keyID, spend(func(body map[string]string) { body["run_id"] = "16873409999" }), http.StatusConflict, codeNotAttested},
		{"a key minted for another attempt", w.closeToken(), keyID, spend(func(body map[string]string) { body["run_attempt"] = "2" }), http.StatusConflict, codeNotAttested},
		{"a key that is not an id", w.closeToken(), "sk-raw", fixturesSpend("1"), http.StatusBadRequest, codeBadQuery},
		{"euros", w.closeToken(), keyID, spend(func(body map[string]string) { body["currency"] = "EUR" }), http.StatusBadRequest, codeMalformed},
		{"no attempt", w.closeToken(), keyID, spend(func(body map[string]string) { body["run_attempt"] = "" }), http.StatusBadRequest, codeMalformed},
		{"a negative spend", w.closeToken(), keyID, fixturesSpend("-1"), http.StatusBadRequest, codeMalformed},
		{"thirteen digits", w.closeToken(), keyID, fixturesSpend("0.0000000000001"), http.StatusBadRequest, codeMalformed},
		{"thirteen digits, the last a zero", w.closeToken(), keyID, fixturesSpend("0.1000000000000"), http.StatusBadRequest, codeMalformed},
		{"an exponent", w.closeToken(), keyID, fixturesSpend("1e-05"), http.StatusBadRequest, codeMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := w.attestSpend(tc.bearer, tc.key, tc.body)
			if got.status != tc.status || got.code(t) != tc.code {
				t.Fatalf("%d %s", got.status, got.body)
			}
		})
	}
	ledger, _ := w.server.Spend.(*spendLedger)
	if len(ledger.spend[tenantID(t, tenantA)]) != 0 {
		t.Fatalf("a refused attestation was kept: %v", ledger.spend)
	}
}

// TestASecondSpendForAKeyIsRefusedAndNeverTheLarger holds a key to one
// figure, written once by one attesting run and attempt. The same string
// again from that run is answered as the first was; another string from it,
// even the same number written otherwise, and any figure from another run,
// are refused, and the verdict reads the first figure. It runs on the memory
// ledger and on Postgres, whose unique indexes hold the rule.
func TestASecondSpendForAKeyIsRefusedAndNeverTheLarger(t *testing.T) {
	t.Run("in memory", func(t *testing.T) {
		secondSpendForAKeyIsRefusedAndNeverTheLarger(t, spendWorld(t, true))
	})
	t.Run("on Postgres", func(t *testing.T) {
		secondSpendForAKeyIsRefusedAndNeverTheLarger(t, postgresSpendWorld(t))
	})
}

func secondSpendForAKeyIsRefusedAndNeverTheLarger(t *testing.T, w *world) {
	keyID := gateway.KeyID(fixturesKey)
	w.rows(w.token(), lines(t, credentialed(row("req-1", focus.Output, 1000)))).want(t, http.StatusOK)
	w.attestSpend(w.closeToken(), keyID, fixturesSpend("0.5")).want(t, http.StatusNoContent)
	w.attestSpend(w.closeToken(), keyID, fixturesSpend("0.5")).want(t, http.StatusNoContent)

	another := w.mintFor(t, acmeRun, "1")
	laterAttempt := w.protectedToken(closeRun, func(claims map[string]any) { claims["run_attempt"] = "2" })
	for _, tc := range []struct {
		name, bearer, key, spend, message string
	}{
		{"the same number written otherwise", w.closeToken(), keyID, "0.50", "Run " + closeRun + ", attempt 1, has written a spend already."},
		{"a larger figure from the same run", w.closeToken(), keyID, "6", "Run " + closeRun + ", attempt 1, has written a spend already."},
		{"another key's figure from the same run", w.closeToken(), another, "0.1", "Run " + closeRun + ", attempt 1, has written a spend already."},
		{"a larger figure from a later attempt", laterAttempt, keyID, "6", "The key holds a spend already."},
		{"the same figure from another run", w.protectedToken(otherPullRequestRun, nil), keyID, "0.5", "The key holds a spend already."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := w.attestSpend(tc.bearer, tc.key, fixturesSpend(tc.spend))
			if got.status != http.StatusConflict || got.code(t) != codeSpendHeld || !strings.Contains(string(got.body), tc.message) {
				t.Fatalf("%d %s", got.status, got.body)
			}
		})
	}

	verdict := w.verdict(t, w.token(), verdictBody(t, prBudget, nil))
	if got := ruleOf(t, verdict.Policy, policy.RulePullRequestBudget); verdict.Budget.GatewayUSD != "0.5" || got.CostUSD != "0.5" || got.Outcome != policy.Pass {
		t.Fatalf("the verdict reads the gateway's count as USD %s, and the first figure was USD 0.5: %+v", verdict.Budget.GatewayUSD, got)
	}
}

// TestAnEnvironmentOnlyCountLetsNoRulePass sends the gateway's count from
// each job a pull request can edit, with the job in the gateway's
// environment, for a key a protected run attested. Each is refused, and the
// budget stays on the agent's own counts, which let no rule pass.
func TestAnEnvironmentOnlyCountLetsNoRulePass(t *testing.T) {
	for name, bearer := range map[string]func(w *world) string{
		"the pull request's own close job": func(w *world) string { return w.pullRequestToken(nil) },
		"another run the pull request started": func(w *world) string {
			return w.pullRequestToken(func(claims map[string]any) { claims["run_id"] = otherPullRequestRun })
		},
		"a workflow_run run of a default branch no rule protects": func(w *world) string {
			return w.protectedToken(closeRun, func(claims map[string]any) { claims["ref_protected"] = "false" })
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := spendWorld(t, true)
			w.rows(w.token(), lines(t, credentialed(row("req-1", focus.Output, 1000)))).want(t, http.StatusOK)
			got := w.attestSpend(bearer(w), gateway.KeyID(fixturesKey), fixturesSpend("0.001"))
			if got.status != http.StatusForbidden || got.code(t) != codeRunUnprotected {
				t.Fatalf("%d %s", got.status, got.body)
			}
			answer := w.verdict(t, w.token(), verdictBody(t, prBudget, nil))
			if answer.Budget.Evidence || answer.Budget.Basis != budgetBasisAgent || answer.Policy.Conclusion != policy.Neutral ||
				ruleOf(t, answer.Policy, policy.RulePullRequestBudget).Outcome != policy.Unproven {
				t.Fatalf("a count from a job a pull request can edit: %+v %+v", answer.Budget, answer.Policy)
			}
		})
	}
}

// TestSpendAttestationRefusesALongAmountBeforeParsing sends a megabyte of
// trailing zeros, which the body bound lets through, and the longest spend
// the contract takes.
func TestSpendAttestationRefusesALongAmountBeforeParsing(t *testing.T) {
	w := spendWorld(t, true)
	keyID := gateway.KeyID(fixturesKey)
	got := w.attestSpend(w.closeToken(), keyID, fixturesSpend("1."+strings.Repeat("0", 1<<20)))
	if got.status != http.StatusBadRequest || got.code(t) != codeMalformed || !strings.Contains(string(got.body), "spend is longer than 64 characters.") {
		t.Fatalf("%d %s", got.status, got.body)
	}
	ledger, _ := w.server.Spend.(*spendLedger)
	if len(ledger.spend) != 0 {
		t.Fatalf("a refused attestation was kept: %v", ledger.spend)
	}
	w.attestSpend(w.closeToken(), keyID, fixturesSpend(strings.Repeat("9", 26)+"."+strings.Repeat("0", 12))).want(t, http.StatusNoContent)
}

func TestAVerdictReadsItsOwnRepositoryAndAWellFormedRequest(t *testing.T) {
	w := spendWorld(t, false)
	path := "/v1/pulls/" + acmePull + "/verdict?repo="
	got := w.send(http.MethodPost, path+"github.com/acme/other", w.token(), jsonType, verdictBody(t, "", nil)).want(t, http.StatusForbidden)
	if got.code(t) != codeWrongRepo {
		t.Fatalf("code %s", got.code(t))
	}
	for name, body := range map[string][]byte{
		"not JSON":         []byte("{"),
		"an unknown field": []byte(`{"budget_usd": "5"}`),
		"not base64":       []byte(`{"config": "!!"}`),
		"a short ref":      []byte(`{"config_ref": "main"}`),
		"a bad time":       []byte(`{"workflow_added": "yesterday"}`),
		"a negative count": []byte(`{"measured_pull_requests": -1}`),
	} {
		got := w.send(http.MethodPost, path+acmeRepo, w.token(), jsonType, body)
		if got.status != http.StatusBadRequest || got.code(t) != codeMalformed {
			t.Errorf("%s: %d %s", name, got.status, got.body)
		}
	}
	// An ingest key's rows are unverified, and no rule passes on them.
	stated := row("req-1", focus.Output, 1000)
	tags(stated)[store.TagRepo], tags(stated)[store.TagPR] = acmeRepo, acmePull
	w.rows(acmeKey, lines(t, stated)).want(t, http.StatusOK)
	answer := w.verdict(t, acmeKey, verdictBody(t, prBudget, nil))
	verdict := answer.Policy
	if verdict.Trust != policy.Unverified || verdict.Evidence != "" || verdict.Conclusion != policy.Neutral || answer.Budget.Basis != budgetBasisNone {
		t.Fatalf("%+v %+v", verdict, answer.Budget)
	}
	if got := ruleOf(t, verdict, policy.RulePullRequestBudget); got.Outcome != policy.Unproven || got.Reason != policy.ReasonUnverifiedRows {
		t.Fatalf("pr_budget on an ingest key's rows: %+v", got)
	}
}

// protectedMigration is the migration from which a ledger takes an
// attestation only from a protected run.
const protectedMigration = "0010_protected_attestation.sql"

// migrationsBefore is the schema as it stood before the migration named.
func migrationsBefore(t *testing.T, first string) fstest.MapFS {
	t.Helper()
	names, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{}
	for _, name := range names {
		if name >= first {
			continue
		}
		data, err := fs.ReadFile(migrations.Files, name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = &fstest.MapFile{Data: data}
	}
	return files
}

// environmentEraRow is a row of the fixtures' run on key as the service
// stored it when a job in the gateway's environment had attested key: exact,
// on a minted credential.
func environmentEraRow(key string) store.Record {
	cost, version := decimal.MustParse("0.01"), "0.1.0"
	return store.Record{
		Row: focus.Row{
			RowID: strings.Repeat("e", 64), BillingCurrency: "USD", ServiceCategory: "AI and Machine Learning",
			SkuMeter: focus.Output, ConsumedUnit: "Tokens", ConsumedQuantity: decimal.FromInt(100),
			ListCost: cost, EffectiveCost: cost, ContractedCost: cost, CatalogVersion: &version, SubAccountID: &key,
			ChargePeriodStart: "2026-11-01T10:00:00Z", ChargePeriodEnd: "2026-11-01T10:00:01Z",
			Tags: map[string]string{
				focus.TagAgent: "claude-code", focus.TagModel: "claude-haiku-5-5", focus.TagCostSource: "agent_stream",
				focus.TagCostBasis: "measured", focus.TagAttributionGrade: gradeExact, focus.TagEnforcementGrade: "none",
				focus.TagRunIdentity: identityToken, store.TagRepo: acmeRepo, store.TagPR: acmePull,
				store.TagWorkflowRunID: acmeRun, store.TagCommitSHA: acmeHead,
			},
		},
		RunAttempt: "1", Actor: "octo-dev", CredentialBasis: store.CredentialMinted, HeadBasis: store.HeadStated,
	}
}

// TestAGradeStoredBeforeTheNewRuleLetsNothingPass stores a row graded exact,
// its key's attestation and the gateway's count of USD 0.01 as the service
// kept them when a job in the gateway's environment attested them, then
// migrates. The row keeps its grade, because a ledger does not rewrite its
// records. Nothing rests on it: the verdict reads the agent's own counts, so
// no rule passes, and once a protected run has attested a key and a count of
// a later run, the earlier row still keeps the budget from passing.
func TestAGradeStoredBeforeTheNewRuleLetsNothingPass(t *testing.T) {
	ctx := context.Background()
	db := storetest.Empty(t)
	if err := store.MigrateFrom(ctx, db.OwnerURL, migrationsBefore(t, protectedMigration)); err != nil {
		t.Fatal(err)
	}
	a := db.Tenant(t, "acme", []store.Binding{{Host: "github.com", Owner: "acme"}}, store.KeyDigest(acmeKey))
	before := gateway.KeyID("sk-a-key-an-environment-attested")
	ledger, err := store.Open(ctx, db.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ledger.Close)
	if _, err := ledger.Ingest(ctx, a, store.Batch{Records: []store.Record{environmentEraRow(before)}, Day: testNow}); err != nil {
		t.Fatal(err)
	}
	owner, err := pgx.Connect(ctx, db.OwnerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close(ctx) }()
	for _, statement := range []struct {
		sql       string
		arguments []any
	}{
		{`SELECT set_config('costroid.tenant_id', $1, false)`, []any{a.String()}},
		{`INSERT INTO minted_credentials (tenant_id, key_sha256, repo, workflow_run_id, run_attempt, environment)
			VALUES (costroid_tenant(), $1, $2, $3, '1', 'costroid-gateway')`, []any{before, acmeRepo, acmeRun}},
		{`INSERT INTO gateway_spend (tenant_id, key_sha256, usd) VALUES (costroid_tenant(), $1, 0.01)`, []any{before}},
	} {
		if _, err := owner.Exec(ctx, statement.sql, statement.arguments...); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Migrate(ctx, db.OwnerURL); err != nil {
		t.Fatal(err)
	}

	w := newWorld(t, ledger)
	config := w.server.Config
	config.Spend = ledger
	w.server = New(config)
	w.http = httptest.NewServer(w.server.Handler())
	t.Cleanup(w.http.Close)
	lw := &ledgerWorld{world: w, db: db, a: a}
	if view := lw.view(t, w.token(), acmeRepo, acmePull); len(view.Lines) != 1 || view.Lines[0].AttributionGrade != gradeExact {
		t.Fatalf("the row stored before the rule: %+v", view.Lines)
	}
	body := verdictBody(t, prBudget, nil)
	held := func(when, counted string) {
		t.Helper()
		answer := w.verdict(t, w.token(), body)
		if answer.Budget.Evidence || answer.Budget.Basis != budgetBasisAgent || answer.Budget.GatewayUSD != counted || answer.Policy.Conclusion != policy.Neutral ||
			ruleOf(t, answer.Policy, policy.RulePullRequestBudget).Outcome != policy.Unproven {
			t.Fatalf("%s: %+v %+v", when, answer.Budget, answer.Policy)
		}
	}
	held("on the count an environment's job attested", "")

	// A later attempt of the run, whose key a protected run attested and
	// counted. The earlier row has no count a protected run attested, so the
	// pull request's budget still rests on the agent's own counts.
	later := w.mintFor(t, acmeRun, "2")
	laterToken := w.issuer.Token(func() map[string]any { claims := w.claims(); claims["run_attempt"] = "2"; return claims }())
	w.rows(laterToken, lines(t, stating(later))).want(t, http.StatusOK)
	w.attestSpend(w.protectedToken(closeRun, nil), later, map[string]string{"repository": acmeRepo, "run_id": acmeRun, "run_attempt": "2", "currency": "USD", "spend": "0.001"}).want(t, http.StatusNoContent)
	held("with a protected count for a later attempt beside it", "0.001")
}
