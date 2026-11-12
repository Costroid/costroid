package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/gateway"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/store"
)

// spendLedger is a memory ledger that keeps the gateway's attested spend and
// answers the per-PR lines from its rows, one line per row.
type spendLedger struct {
	*memoryLedger
	spend map[store.TenantID]map[string]decimal.Decimal
}

func (m *spendLedger) AttestSpend(_ context.Context, tenant store.TenantID, key store.MintedCredential, usd decimal.Decimal) error {
	if m.spend[tenant] == nil {
		m.spend[tenant] = map[string]decimal.Decimal{}
	}
	if stored, seen := m.spend[tenant][key.KeySHA256]; !seen || usd.Cmp(stored) > 0 {
		m.spend[tenant][key.KeySHA256] = usd
	}
	return nil
}

func (m *spendLedger) records(tenant store.TenantID, repo, pr string) []store.Record {
	var found []store.Record
	for _, record := range m.rows[tenant] {
		if record.Row.Tags[store.TagRepo] == repo && record.Row.Tags[store.TagPR] == pr {
			found = append(found, record)
		}
	}
	return found
}

func eligible(row focus.Row) bool {
	return row.Tags[focus.TagRunIdentity] == identityToken && row.Tags[focus.TagAttributionGrade] != gradeNone
}

func (m *spendLedger) PullRequest(_ context.Context, tenant store.TenantID, repo, pr string) ([]store.Line, error) {
	var lines []store.Line
	for _, record := range m.records(tenant, repo, pr) {
		row := record.Row
		lines = append(lines, store.Line{
			Currency: row.BillingCurrency, Model: row.Tags[focus.TagModel], CostSource: row.Tags[focus.TagCostSource],
			AttributionGrade: row.Tags[focus.TagAttributionGrade], RunIdentity: row.Tags[focus.TagRunIdentity],
			BudgetEligible: eligible(row), Rows: 1, ListCost: row.ListCost,
		})
	}
	return lines, nil
}

func (m *spendLedger) GatewaySpend(_ context.Context, tenant store.TenantID, repo, pr string) (int64, decimal.Decimal, error) {
	var rows int64
	var usd decimal.Decimal
	counted := map[string]bool{}
	for _, record := range m.records(tenant, repo, pr) {
		row := record.Row
		if !eligible(row) || record.CredentialBasis != store.CredentialMinted || row.SubAccountID == nil {
			continue
		}
		spend, attested := m.spend[tenant][*row.SubAccountID]
		if !attested {
			continue
		}
		rows++
		if !counted[*row.SubAccountID] {
			counted[*row.SubAccountID] = true
			usd = usd.Add(spend)
		}
	}
	return rows, usd, nil
}

// spendWorld is a memory world that answers the per-PR lines from its rows.
// Its store keeps the gateway's spend when keeps is true.
func spendWorld(t *testing.T, keeps bool) *world {
	t.Helper()
	w, memory := memoryWorld(t)
	ledger := &spendLedger{memoryLedger: memory, spend: map[store.TenantID]map[string]decimal.Decimal{}}
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

// closeToken is the closing job's token: the fixtures' run, in the gateway's
// environment.
func (w *world) closeToken() string {
	return w.mintToken(gatewayEnvironment, nil)
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
	binding := gateway.Binding{Repository: acmeRepo, Run: acmeRun, Attempt: "1"}
	if err := w.attest(w.closeToken(), gateway.AttestationFor(gateway.KeyID(fixturesKey), binding)); err != nil {
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
	w := open(t)
	keyID := gateway.KeyID(fixturesKey)
	w.rows(w.token(), lines(t, credentialed(row("req-1", focus.Output, 1000)), credentialed(row("req-1", focus.InputUncached, 4000)))).
		want(t, http.StatusOK)
	body := verdictBody(t, prBudget, nil)

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
	w.attestSpend(w.closeToken(), keyID, fixturesSpend("6")).want(t, http.StatusNoContent)
	over := w.verdict(t, w.token(), body)
	if got := ruleOf(t, over.Policy, policy.RulePullRequestBudget); over.Policy.Conclusion != policy.Failure ||
		got.Outcome != policy.Fail || got.Reason != policy.ReasonOverLimit || got.CostUSD != "6" {
		t.Fatalf("rows under the budget, gateway over it: %+v", over.Policy)
	}
	// A smaller count later does not lower the figure.
	w.attestSpend(w.closeToken(), keyID, fixturesSpend("0.5")).want(t, http.StatusNoContent)
	if again := w.verdict(t, w.token(), body); again.Policy.Conclusion != policy.Failure {
		t.Fatalf("a smaller count lowered the spend: %+v", again.Policy)
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
			if code := Main(context.Background(), nil, &stderr); code != exitFailure {
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
	secondAttempt := w.mintToken(gatewayEnvironment, func(claims map[string]any) { claims["run_attempt"] = "2" })
	cases := []struct {
		name   string
		bearer string
		key    string
		body   map[string]string
		status int
		code   string
	}{
		{"an ingest key", acmeKey, keyID, fixturesSpend("1"), http.StatusForbidden, codeAttestation},
		{"a job outside the environment", w.token(), keyID, fixturesSpend("1"), http.StatusForbidden, codeAttestation},
		{"another run", w.closeToken(), keyID, map[string]string{"repository": acmeRepo, "run_id": "1", "run_attempt": "1", "currency": "USD", "spend": "1"}, http.StatusForbidden, codeJoinKey},
		{"a key never minted", w.closeToken(), other, fixturesSpend("1"), http.StatusConflict, codeJoinKey},
		{"a key minted for another attempt", secondAttempt, keyID, map[string]string{"repository": acmeRepo, "run_id": acmeRun, "run_attempt": "2", "currency": "USD", "spend": "1"}, http.StatusConflict, codeJoinKey},
		{"a key that is not an id", w.closeToken(), "sk-raw", fixturesSpend("1"), http.StatusBadRequest, codeBadQuery},
		{"euros", w.closeToken(), keyID, map[string]string{"repository": acmeRepo, "run_id": acmeRun, "run_attempt": "1", "currency": "EUR", "spend": "1"}, http.StatusBadRequest, codeMalformed},
		{"a negative spend", w.closeToken(), keyID, fixturesSpend("-1"), http.StatusBadRequest, codeMalformed},
		{"thirteen digits", w.closeToken(), keyID, fixturesSpend("0.0000000000001"), http.StatusBadRequest, codeMalformed},
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
	if len(ledger.spend) != 0 {
		t.Fatalf("a refused attestation was kept: %v", ledger.spend)
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
	w.attestSpend(w.closeToken(), keyID, fixturesSpend("2."+strings.Repeat("0", decimal.MaxLength-2))).want(t, http.StatusNoContent)
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
