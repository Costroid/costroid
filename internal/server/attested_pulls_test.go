package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/gateway"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/store"
)

const (
	// secondRun is a second run of the fixtures' pull request, and
	// secondClose the run that closed its key.
	secondRun, secondClose = "16873409930", "16873410556"
	// otherPull is another pull request of the fixtures' repository, whose
	// own run is otherRun, closed by otherClose.
	otherPull, otherRun, otherClose = "1424", "16873409940", "16873410557"
)

// spendWorlds are the service keeping the gateway's spend on the memory
// ledger and on Postgres.
var spendWorlds = map[string]func(*testing.T) *world{
	"in memory":   func(t *testing.T) *world { return spendWorld(t, true) },
	"on Postgres": postgresSpendWorld,
}

// onBothSpendWorlds runs a test on each world.
func onBothSpendWorlds(t *testing.T, test func(*testing.T, func(*testing.T) *world)) {
	for name, open := range spendWorlds {
		t.Run(name, func(t *testing.T) { test(t, open) })
	}
}

// runSpend is the body close sends for the fixtures' repository, run and
// first attempt.
func runSpend(run, usd string) map[string]string {
	return map[string]string{"repository": acmeRepo, "run_id": run, "run_attempt": "1", "currency": "USD", "spend": usd}
}

// firstRunCounted sends the fixtures' run's one row on its key, which the
// gateway counted at USD 0.5.
func firstRunCounted(t *testing.T, w *world) {
	t.Helper()
	w.rows(w.token(), lines(t, credentialed(row("req-1", focus.Output, 1000)))).want(t, http.StatusOK)
	w.attestSpend(w.closeToken(), gateway.KeyID(fixturesKey), fixturesSpend("0.5")).want(t, http.StatusNoContent)
}

// statingOf is a row of request that states credential and claims exact on
// its own.
func statingOf(credential, request string) map[string]any {
	r := stating(credential)
	r["x_CostroidRowId"] = focus.RowIdentity(request, focus.Output)
	return r
}

// verdictOf asks for the verdict on pull request pr of the fixtures'
// repository under the costroid.yml given.
func (w *world) verdictOf(t *testing.T, pr, bearer, config string) verdictAnswer {
	t.Helper()
	got := w.send(http.MethodPost, "/v1/pulls/"+pr+"/verdict?repo="+acmeRepo, bearer, jsonType, verdictBody(t, config, nil)).want(t, http.StatusOK)
	var answer verdictAnswer
	if err := json.Unmarshal(got.body, &answer); err != nil {
		t.Fatal(err)
	}
	return answer
}

// requireOverOnTheGatewaysCount holds a verdict to a failing pr_budget on the
// gateway's count of usd.
func requireOverOnTheGatewaysCount(t *testing.T, answer verdictAnswer, usd string) {
	t.Helper()
	got := ruleOf(t, answer.Policy, policy.RulePullRequestBudget)
	if answer.Policy.Conclusion != policy.Failure || got.Outcome != policy.Fail || got.Reason != policy.ReasonOverLimit || got.CostUSD != usd ||
		answer.Budget.Basis != budgetBasisGateway || answer.Budget.GatewayUSD != usd {
		t.Fatalf("the gateway counted USD %s on the pull request, over a budget of USD 5: %+v %+v", usd, answer.Budget, answer.Policy)
	}
}

// TestARunsSpendStaysInItsPullRequestsBudget holds pr_budget to the gateway's
// count on every run of a pull request whose key a protected run attested for
// it. The first run sends one row on its key, which the gateway counted at
// USD 0.5. The second run's key, attested for the pull request, holds USD 10.
// Whatever becomes of the second run's row, a budget of USD 5 never passes.
//
// The cure reaches only a key whose attestation names the pull request. The
// last case attests the second key naming none, as an earlier release did,
// and holds what the ledger reads then: the second run's count reaches the
// pull request only through its row, so each sender that keeps the row off
// the pull request leaves the budget passing on the first run's USD 0.5.
func TestARunsSpendStaysInItsPullRequestsBudget(t *testing.T) {
	onBothSpendWorlds(t, aRunsSpendStaysInItsPullRequestsBudget)
}

// secondRowSenders are what can become of the second run's row: each sends
// it, under own, the token of its own run, or keeps it off the pull request.
var secondRowSenders = map[string]func(t *testing.T, w *world, own string, r map[string]any){
	"an ingest key stated its row first": func(t *testing.T, w *world, own string, r map[string]any) {
		w.rows(acmeKey, lines(t, r)).want(t, http.StatusOK)
		w.rows(own, lines(t, r)).want(t, http.StatusOK)
	},
	"a run of another repository of the owner stated its row first": func(t *testing.T, w *world, own string, r map[string]any) {
		w.rows(w.tokenFor("acme", "gadgets"), lines(t, r)).want(t, http.StatusOK)
		w.rows(own, lines(t, r)).want(t, http.StatusOK)
	},
	"its row never arrived": func(*testing.T, *world, string, map[string]any) {},
}

// secondRunSent counts the first run at USD 0.5, attests the second run's
// key naming pulls and counts USD 10 on it, lets send do what it does with
// the second run's row, and answers the verdict under a budget of USD 5.
func secondRunSent(t *testing.T, w *world, send func(*testing.T, *world, string, map[string]any), pulls ...string) verdictAnswer {
	t.Helper()
	firstRunCounted(t, w)
	second := w.mintFor(t, secondRun, "1", pulls...)
	w.attestSpend(w.protectedToken(secondClose, nil), second, runSpend(secondRun, "10")).want(t, http.StatusNoContent)
	claims := w.claims()
	claims["run_id"] = secondRun
	r := row("req-2", focus.Output, 1000)
	r["SubAccountId"] = second
	send(t, w, w.issuer.Token(claims), r)
	return w.verdict(t, w.token(), verdictBody(t, prBudget, nil))
}

func aRunsSpendStaysInItsPullRequestsBudget(t *testing.T, open func(*testing.T) *world) {
	for name, send := range secondRowSenders {
		t.Run(name, func(t *testing.T) {
			answer := secondRunSent(t, open(t), send, acmePull)
			got := ruleOf(t, answer.Policy, policy.RulePullRequestBudget)
			if answer.Policy.Conclusion == policy.Success || got.Outcome == policy.Pass {
				t.Fatalf("the gateway counted USD 10.5 on the pull request's two runs, and a budget of USD 5 reads %s, %s at USD %s",
					answer.Policy.Conclusion, got.Outcome, got.CostUSD)
			}
		})
	}
	t.Run("an attestation naming no pull request", func(t *testing.T) {
		for name, send := range secondRowSenders {
			t.Run(name, func(t *testing.T) {
				answer := secondRunSent(t, open(t), send)
				got := ruleOf(t, answer.Policy, policy.RulePullRequestBudget)
				if answer.Policy.Conclusion != policy.Success || got.Outcome != policy.Pass || got.CostUSD != "0.5" {
					t.Fatalf("the ledger no longer reads the first run's count alone when the second key names no pull request: %s, %s at USD %s",
						answer.Policy.Conclusion, got.Outcome, got.CostUSD)
				}
			})
		}
	})
}

// TestAKeyNamingTwoPullRequestsCountsOnEach attests a run's key for the two
// pull requests GitHub's event lists for its head, and the gateway counts
// USD 10 on it. Each pull request has a run of its own, counted at USD 0.5,
// and no row of the shared key reaches either. Each reads USD 10.5 and fails
// a budget of USD 5. A key attested for a pull request of the same number in
// another repository counts on neither.
func TestAKeyNamingTwoPullRequestsCountsOnEach(t *testing.T) {
	onBothSpendWorlds(t, func(t *testing.T, open func(*testing.T) *world) {
		w := open(t)
		firstRunCounted(t, w)
		other := w.mintFor(t, otherRun, "1", otherPull)
		otherToken := w.issuer.Token(func() map[string]any {
			claims := w.claims()
			claims["run_id"], claims["ref"] = otherRun, "refs/pull/"+otherPull+"/merge"
			return claims
		}())
		w.rows(otherToken, lines(t, statingOf(other, "req-3"))).want(t, http.StatusOK)
		w.attestSpend(w.protectedToken(otherClose, nil), other, runSpend(otherRun, "0.5")).want(t, http.StatusNoContent)

		shared := w.mintFor(t, secondRun, "1", acmePull, otherPull)
		w.attestSpend(w.protectedToken(secondClose, nil), shared, runSpend(secondRun, "10")).want(t, http.StatusNoContent)

		gadgets := gateway.KeyID(gateway.NewKey())
		inGadgets := func(claims map[string]any) {
			claims["repository"] = "acme/gadgets"
		}
		attestation := gateway.AttestationFor(gadgets, gateway.Binding{Repository: "github.com/acme/gadgets", Run: "16873409960", Attempt: "1"}, acmeHead)
		attestation.PullRequests = []string{acmePull, otherPull}
		if err := w.attest(w.protectedToken("16873410600", inGadgets), attestation); err != nil {
			t.Fatal(err)
		}
		w.attestSpend(w.protectedToken("16873410601", inGadgets), gadgets, map[string]string{
			"repository": "github.com/acme/gadgets", "run_id": "16873409960", "run_attempt": "1", "currency": "USD", "spend": "100",
		}).want(t, http.StatusNoContent)

		requireOverOnTheGatewaysCount(t, w.verdictOf(t, acmePull, w.token(), prBudget), "10.5")
		requireOverOnTheGatewaysCount(t, w.verdictOf(t, otherPull, otherToken, prBudget), "10.5")
	})
}

// TestAnAttestationNamingNoPullRequestReadsAsBefore attests the second run's
// key naming no pull request, as an earlier release did and as mint does for a
// run GitHub's event lists none for. Its count reaches the pull request only
// through its rows: while they are missing, the budget reads the first run's
// count, as it did before, and once they arrive it reads both.
func TestAnAttestationNamingNoPullRequestReadsAsBefore(t *testing.T) {
	onBothSpendWorlds(t, func(t *testing.T, open func(*testing.T) *world) {
		w := open(t)
		firstRunCounted(t, w)
		second := w.mintFor(t, secondRun, "1")
		w.attestSpend(w.protectedToken(secondClose, nil), second, runSpend(secondRun, "10")).want(t, http.StatusNoContent)

		answer := w.verdict(t, w.token(), verdictBody(t, prBudget, nil))
		if got := ruleOf(t, answer.Policy, policy.RulePullRequestBudget); answer.Policy.Conclusion != policy.Success || got.Outcome != policy.Pass ||
			got.CostUSD != "0.5" || answer.Budget.GatewayUSD != "0.5" {
			t.Fatalf("a key attested for no pull request, with no row on it: %+v %+v", answer.Budget, answer.Policy)
		}

		claims := w.claims()
		claims["run_id"] = secondRun
		w.rows(w.issuer.Token(claims), lines(t, statingOf(second, "req-2"))).want(t, http.StatusOK)
		requireOverOnTheGatewaysCount(t, w.verdict(t, w.token(), verdictBody(t, prBudget, nil)), "10.5")
	})
}

// TestAnAttestedKeyWithNoSpendKeepsABudgetFromPassing attests the second
// run's key for the pull request, and no count arrives for it: its run has
// not closed, or its close failed. The first run's count is under the budget,
// and the budget does not pass on it: it reads agent_counts, and pr_budget
// unproven with reason unmeasured_spend, as it does when the gateway's count
// does not stand behind every row. Once the count arrives, the budget reads
// both runs. Rows over the budget fail it on the agent's own counts, as they
// do on any pull request.
func TestAnAttestedKeyWithNoSpendKeepsABudgetFromPassing(t *testing.T) {
	onBothSpendWorlds(t, func(t *testing.T, open func(*testing.T) *world) {
		w := open(t)
		firstRunCounted(t, w)
		second := w.mintFor(t, secondRun, "1", acmePull)

		answer := w.verdict(t, w.token(), verdictBody(t, prBudget, nil))
		got := ruleOf(t, answer.Policy, policy.RulePullRequestBudget)
		if answer.Policy.Conclusion != policy.Neutral || answer.Policy.Evidence != policy.AgentCounts || got.Outcome != policy.Unproven ||
			got.Reason != policy.ReasonUnmeasuredSpend || answer.Budget.Basis != budgetBasisAgent || answer.Budget.Evidence || answer.Budget.GatewayUSD != "0.5" {
			t.Fatalf("a key attested for the pull request with no count: %+v %+v", answer.Budget, answer.Policy)
		}

		failed := w.verdictOf(t, acmePull, w.token(), "policy:\n  pr_budget_usd: \"0.000001\"\n")
		if got := ruleOf(t, failed.Policy, policy.RulePullRequestBudget); failed.Policy.Conclusion != policy.Failure || got.Reason != policy.ReasonOverLimit {
			t.Fatalf("rows over the budget beside a key with no count: %+v", failed.Policy)
		}

		w.attestSpend(w.protectedToken(secondClose, nil), second, runSpend(secondRun, "1")).want(t, http.StatusNoContent)
		counted := w.verdict(t, w.token(), verdictBody(t, prBudget, nil))
		if got := ruleOf(t, counted.Policy, policy.RulePullRequestBudget); counted.Policy.Conclusion != policy.Success || got.Outcome != policy.Pass ||
			got.CostUSD != "1.5" || counted.Budget.GatewayUSD != "1.5" {
			t.Fatalf("once the key's count arrived: %+v %+v", counted.Budget, counted.Policy)
		}
	})
}

// TestASecondStatementOfAnAttestationWithOtherPullRequestsIsRefused attests a
// key for two pull requests, and states the attestation again with other
// lists. The same pull requests in another order are the same attestation.
// Any other list from the same attesting run is refused as a second
// attestation from that run, and from another run as a second attestation of
// the key, as another head is. The first stands.
func TestASecondStatementOfAnAttestationWithOtherPullRequestsIsRefused(t *testing.T) {
	onBothSpendWorlds(t, func(t *testing.T, open func(*testing.T) *world) {
		w := open(t)
		key := gateway.KeyID(gateway.NewKey())
		naming := func(pulls ...string) gateway.Attestation {
			attestation := fixturesRun(key)
			attestation.PullRequests = pulls
			return attestation
		}
		if err := w.attest(w.mintToken(), naming(acmePull, otherPull)); err != nil {
			t.Fatal(err)
		}
		if err := w.attest(w.mintToken(), naming(otherPull, acmePull, otherPull)); err != nil {
			t.Fatalf("the same pull requests in another order: %v", err)
		}
		for name, pulls := range map[string][]string{
			"one of the two":  {acmePull},
			"none":            nil,
			"a third besides": {acmePull, otherPull, "1425"},
			"another":         {"1425"},
		} {
			t.Run(name, func(t *testing.T) {
				refusedWith(t, w.attest(w.mintToken(), naming(pulls...)), "409", codeAttestingRunUsed)
				refusedWith(t, w.attest(w.protectedToken(closeRun, nil), naming(pulls...)), "409", codeAttestedElsewhere)
			})
		}
		if err := w.attest(w.mintToken(), naming(acmePull, otherPull)); err != nil {
			t.Fatalf("the first attestation no longer stands: %v", err)
		}
	})
}

// requireHeldToTheCount holds a verdict on a pull request with no eligible
// row to pr_budget's outcome and reason, at the gateway's count of usd, with
// no evidence behind a budget.
func requireHeldToTheCount(t *testing.T, answer verdictAnswer, conclusion, outcome, reason, usd string) {
	t.Helper()
	got := ruleOf(t, answer.Policy, policy.RulePullRequestBudget)
	if answer.Policy.Conclusion != conclusion || got.Outcome != outcome || got.Reason != reason || got.CostUSD != usd ||
		answer.Budget.Basis != budgetBasisNone || answer.Budget.Evidence || answer.Budget.GatewayUSD != usd {
		t.Fatalf("want %s, pr_budget %s %s at USD %s: %+v %+v", conclusion, outcome, reason, usd, answer.Budget, answer.Policy)
	}
}

// rowlessPulls are pull requests whose run's key is attested for them and
// counted at usd, and none of whose rows the ledger verified: none arrived,
// or an ingest key stated them, unverified.
func rowlessPulls(usd string) map[string]func(t *testing.T, w *world) {
	counted := func(t *testing.T, w *world) {
		key := w.mintFor(t, secondRun, "1", acmePull)
		w.attestSpend(w.protectedToken(secondClose, nil), key, runSpend(secondRun, usd)).want(t, http.StatusNoContent)
	}
	return map[string]func(t *testing.T, w *world){
		"no row arrived": counted,
		"an ingest key stated the rows alone": func(t *testing.T, w *world) {
			counted(t, w)
			stated := row("req-2", focus.Output, 1000)
			tags(stated)[store.TagRepo], tags(stated)[store.TagPR] = acmeRepo, acmePull
			w.rows(acmeKey, lines(t, stated)).want(t, http.StatusOK)
		},
	}
}

// TestACountOverTheBudgetFailsWithNoRows attests a run's key for the pull
// request, and the gateway counts USD 10 on it. No row of the pull request
// is one the ledger verified. Leaving every row out does not turn the
// failure into neutral: pr_budget fails with over_limit at the count, and the
// failure is recorded as any other.
func TestACountOverTheBudgetFailsWithNoRows(t *testing.T) {
	onBothSpendWorlds(t, func(t *testing.T, open func(*testing.T) *world) {
		for name, arrange := range rowlessPulls("10") {
			t.Run(name, func(t *testing.T) {
				w := open(t)
				arrange(t, w)
				requireHeldToTheCount(t, w.verdict(t, w.token(), verdictBody(t, prBudget, nil)), policy.Failure, policy.Fail, policy.ReasonOverLimit, "10")
			})
		}
	})
	t.Run("recorded", func(t *testing.T) {
		recordedOnce(t, func(ledger demotionLedger) Ledger { return rowlessLedger{ledger} }, "10")
	})
}

// recordedOnce asks for pull request 7's verdict, over a budget of USD 5, on
// the rows withRows gives and gateway counts that stand behind none of them
// and add up to usd, and holds the override record to one failure of
// pr_budget: one per verdict, whatever number of keys the counts are on.
func recordedOnce(t *testing.T, withRows func(demotionLedger) Ledger, usd string) {
	t.Helper()
	w := memoryDemotionWorld(t)
	config := w.server.Config
	config.Ledger = withRows(config.Ledger.(demotionLedger))
	config.Spend = standingCounts{usd: usd}
	w.server = New(config)
	w.http = httptest.NewServer(w.server.Handler())
	t.Cleanup(w.http.Close)
	settings := "policy:\n  pr_budget_usd: \"5\"\n  enforce: true\n"
	w.stub.Files["costroid.yml"] = settings
	requireFails(t, w.ask(t, 7, settings, nil), "a count over the budget")
	recorded := w.keeper.(*memoryOverrides).failuresOf(w.tenant, "7")
	if len(recorded) != 1 || recorded[0].Rule != policy.RulePullRequestBudget {
		t.Fatalf("the record holds %+v, want the budget's failure once", recorded)
	}
}

// TestACountWithinTheBudgetAndNoRowsNeverPasses attests a run's key for the
// pull request, and the gateway counts USD 1 on it, within a budget of USD 5.
// No row of the pull request is one the ledger verified, so pr_budget does
// not pass: a pass needs rows the ledger verified.
func TestACountWithinTheBudgetAndNoRowsNeverPasses(t *testing.T) {
	reasons := map[string]string{"no row arrived": policy.ReasonNoRows, "an ingest key stated the rows alone": policy.ReasonUnverifiedRows}
	onBothSpendWorlds(t, func(t *testing.T, open func(*testing.T) *world) {
		for name, arrange := range rowlessPulls("1") {
			t.Run(name, func(t *testing.T) {
				w := open(t)
				arrange(t, w)
				requireHeldToTheCount(t, w.verdict(t, w.token(), verdictBody(t, prBudget, nil)), policy.Neutral, policy.Unproven, reasons[name], "1")
			})
		}
	})
}

// rowlessLedger is a demotion ledger whose pull requests hold no row.
type rowlessLedger struct {
	demotionLedger
}

func (rowlessLedger) PullRequest(context.Context, store.TenantID, string, string) ([]store.Line, error) {
	return nil, nil
}

// standingCounts is a ledger's gateway figures when the counts that stand,
// adding up to usd, stand behind no row: no row arrived, or a key attested
// for the pull request holds no count yet.
type standingCounts struct {
	usd string
}

func (standingCounts) AttestSpend(context.Context, store.TenantID, store.AttestedSpend) error {
	return nil
}

func (c standingCounts) GatewaySpend(context.Context, store.TenantID, string, string) (int64, decimal.Decimal, error) {
	return 0, decimal.MustParse(c.usd), nil
}

// verifiedLedger is a demotion ledger whose pull requests' rows a CI token
// proved, graded exact, costing USD 1.
type verifiedLedger struct {
	demotionLedger
}

func (verifiedLedger) PullRequest(context.Context, store.TenantID, string, string) ([]store.Line, error) {
	return []store.Line{{
		Currency: "USD", ListCost: decimal.MustParse("1"), Rows: 1, Model: "claude-opus-5-5", BudgetEligible: true,
		RunIdentity: identityToken, AttributionGrade: gradeExact,
	}}, nil
}

// thirdRun is a third run of the fixtures' pull request, whose key is
// attested for it and holds no count yet.
const thirdRun = "16873409935"

// threeRuns counts the first run's row at USD 0.5, attests the second run's
// key for the pull request with a count of usd and no row, and attests the
// third run's key for it with no count, then answers the verdict under a
// budget of USD 5.
func threeRuns(t *testing.T, w *world, usd string) verdictAnswer {
	t.Helper()
	firstRunCounted(t, w)
	second := w.mintFor(t, secondRun, "1", acmePull)
	w.attestSpend(w.protectedToken(secondClose, nil), second, runSpend(secondRun, usd)).want(t, http.StatusNoContent)
	w.mintFor(t, thirdRun, "1", acmePull)
	return w.verdict(t, w.token(), verdictBody(t, prBudget, nil))
}

// TestACountOverTheBudgetFailsWhileAnotherKeyIsUncounted counts USD 0.5 and
// USD 10 on two keys attested for the pull request, while a third key
// attested for it holds no count. The budget rests on agent counts, and a
// missing count can only add, so pr_budget fails with over_limit at the
// USD 10.5 that stands. The failure is recorded once per verdict.
func TestACountOverTheBudgetFailsWhileAnotherKeyIsUncounted(t *testing.T) {
	onBothSpendWorlds(t, func(t *testing.T, open func(*testing.T) *world) {
		answer := threeRuns(t, open(t), "10")
		got := ruleOf(t, answer.Policy, policy.RulePullRequestBudget)
		if answer.Policy.Conclusion != policy.Failure || got.Outcome != policy.Fail || got.Reason != policy.ReasonOverLimit || got.CostUSD != "10.5" ||
			answer.Budget.Basis != budgetBasisAgent || answer.Budget.Evidence || answer.Budget.GatewayUSD != "10.5" {
			t.Fatalf("USD 10.5 counted, over a budget of USD 5, with a key uncounted: %+v %+v", answer.Budget, answer.Policy)
		}
	})
	t.Run("recorded", func(t *testing.T) {
		recordedOnce(t, func(ledger demotionLedger) Ledger { return verifiedLedger{ledger} }, "12")
	})
}

// TestACountWithinTheBudgetNeverPassesWhileAKeyIsUncounted counts USD 0.5 and
// USD 1 on two keys attested for the pull request, within a budget of USD 5,
// while a third key attested for it holds no count. The missing count could
// take the pull request over, so pr_budget reads unproven with
// unmeasured_spend at the USD 1.5 that stands, never pass.
func TestACountWithinTheBudgetNeverPassesWhileAKeyIsUncounted(t *testing.T) {
	onBothSpendWorlds(t, func(t *testing.T, open func(*testing.T) *world) {
		answer := threeRuns(t, open(t), "1")
		got := ruleOf(t, answer.Policy, policy.RulePullRequestBudget)
		if answer.Policy.Conclusion != policy.Neutral || got.Outcome != policy.Unproven || got.Reason != policy.ReasonUnmeasuredSpend || got.CostUSD != "1.5" ||
			answer.Budget.Basis != budgetBasisAgent || answer.Budget.Evidence || answer.Budget.GatewayUSD != "1.5" {
			t.Fatalf("USD 1.5 counted, within a budget of USD 5, with a key uncounted: %+v %+v", answer.Budget, answer.Policy)
		}
	})
}
