package policy

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	schema "github.com/Costroid/costroid/internal/config"
)

func modelLine(currency, cost, model string) line {
	return line{currency, cost, "", map[string]string{"costroid/model": model}}
}

// verifiedUsage is rows a ledger checked and attributed, one per model.
func verifiedUsage(t *testing.T, currency, cost string, models ...string) Usage {
	t.Helper()
	counts := map[string]int{}
	for _, model := range models {
		counts[model]++
	}
	return Usage{Rows: len(models), Currencies: []CurrencyUsage{{Currency: currency, ListCost: mustAmount(t, cost), Rows: len(models), Models: counts}}}
}

func TestModelAllowlist(t *testing.T) {
	const list = "policy:\n  models: [\"claude-*\", codex-mini]\n"
	enforced := config(list + "  enforce: true\n")
	offList := usageOf(t, modelLine("USD", "1", "claude-sonnet-5-5"), modelLine("USD", "1", "gpt-5.5"), modelLine("USD", "1", "codex-mini"))
	for _, trust := range []Trust{Unverified, TokenVerified} {
		t.Run("a model off the list fails on "+string(trust)+" rows", func(t *testing.T) {
			verdict := evaluate(t, Request{Usage: offList, Trust: trust, Config: enforced})
			got := result(t, verdict, RuleModelAllowlist)
			if verdict.Conclusion != Failure || got.Outcome != Fail || got.Reason != ReasonOutsideAllowlist || !reflect.DeepEqual(got.Models, []string{"gpt-5.5"}) {
				t.Errorf("%+v", verdict)
			}
		})
	}
	t.Run("it warns in the warn-only period", func(t *testing.T) {
		verdict := evaluate(t, Request{Usage: offList, Config: config(list)})
		if got := result(t, verdict, RuleModelAllowlist); verdict.Conclusion != Neutral || got.Outcome != Warn || !got.HeldByWarnOnly {
			t.Errorf("%+v", verdict)
		}
	})
	t.Run("every model on the list cannot pass on rows the job wrote", func(t *testing.T) {
		onList := usageOf(t, modelLine("USD", "1", "claude-sonnet-5-5"), modelLine("USD", "1", "codex-mini"))
		verdict := evaluate(t, Request{Usage: onList, Config: enforced})
		if got := result(t, verdict, RuleModelAllowlist); verdict.Conclusion != Neutral || got.Outcome != Unproven || got.Reason != ReasonUnverifiedRows {
			t.Errorf("%+v", verdict)
		}
	})
	t.Run("every model on the list passes on rows a ledger checked and the gateway measured", func(t *testing.T) {
		verdict := evaluate(t, Request{Usage: verifiedUsage(t, "EUR", "1", "claude-opus-5-5", "codex-mini"), Trust: TokenVerified, Evidence: GatewayMeasured, Config: enforced})
		if got := result(t, verdict, RuleModelAllowlist); verdict.Conclusion != Success || got.Outcome != Pass || got.Reason != ReasonOnAllowlist {
			t.Errorf("%+v", verdict)
		}
	})
	cases := []struct {
		name    string
		list    string
		lines   []line
		outcome string
		reason  string
		models  []string
	}{
		{"an exact entry is not a prefix", "[claude]", []line{modelLine("USD", "1", "claude-sonnet-5-5")}, Fail, ReasonOutsideAllowlist, []string{"claude-sonnet-5-5"}},
		{"a prefix entry needs the prefix", "[\"claude-*\"]", []line{modelLine("USD", "1", "claud")}, Fail, ReasonOutsideAllowlist, []string{"claud"}},
		{"a row in another currency is read", "[\"claude-*\"]", []line{modelLine("EUR", "1", "gpt-5.5")}, Fail, ReasonOutsideAllowlist, []string{"gpt-5.5"}},
		{"a row with no model", "[\"claude-*\"]", []line{{"USD", "1", "", nil}}, Unproven, ReasonUnreadableModel, nil},
		{"a model that is no model id", "[\"claude-*\"]", []line{modelLine("USD", "1", "claude-`x`")}, Unproven, ReasonUnreadableModel, nil},
		{"a model off the list beside one that cannot be read", "[\"claude-*\"]", []line{modelLine("USD", "1", "claude `x`"), modelLine("USD", "1", "gpt-5.5")}, Fail, ReasonOutsideAllowlist, []string{"gpt-5.5"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verdict := evaluate(t, Request{Usage: usageOf(t, tc.lines...), Trust: TokenVerified, Config: config("policy:\n  models: " + tc.list + "\n  enforce: true\n")})
			got := result(t, verdict, RuleModelAllowlist)
			if got.Outcome != tc.outcome || got.Reason != tc.reason || !reflect.DeepEqual(got.Models, tc.models) {
				t.Errorf("%+v", got)
			}
		})
	}
	t.Run("the verdict names at most ten models, sorted", func(t *testing.T) {
		var lines []line
		for index := 12; index > 0; index-- {
			lines = append(lines, modelLine("USD", "1", fmt.Sprintf("model-%02d", index)))
		}
		got := result(t, evaluate(t, Request{Usage: usageOf(t, lines...), Config: enforced}), RuleModelAllowlist)
		if len(got.Models) != maxNamedModels || !slices.IsSorted(got.Models) || got.Models[0] != "model-01" {
			t.Errorf("%v", got.Models)
		}
	})
	t.Run("no list, no rule", func(t *testing.T) {
		for _, result := range evaluate(t, Request{Usage: offList, Config: config("policy:\n  enforce: true\n")}).Results {
			if result.Rule == RuleModelAllowlist {
				t.Errorf("%+v", result)
			}
		}
	})
}

// TestModelAllowlistNeverPassesOnAgentCounts holds token-verified rows to the
// allowlist on both entry points. The token proves which run sent the rows,
// and the model names on them are still the agent's job's own, so with no
// measured or reconciled spend behind them the rule cannot pass. It can fail.
func TestModelAllowlistNeverPassesOnAgentCounts(t *testing.T) {
	enforced := config("policy:\n  models: [\"claude-*\"]\n  enforce: true\n")
	onList := verifiedUsage(t, "USD", "1", "claude-opus-5-5")
	onLedger := []LedgerLine{{Currency: "USD", ListCost: mustAmount(t, "1"), Rows: 1, Model: "claude-opus-5-5", Verified: true, Attributed: true}}
	offLedger := []LedgerLine{{Currency: "USD", ListCost: mustAmount(t, "1"), Rows: 1, Model: "gpt-5.5", Verified: true, Attributed: true}}
	ledger := func(lines []LedgerLine, evidence Evidence) Verdict {
		t.Helper()
		verdict, err := EvaluateLedger(context.Background(), LedgerRequest{Lines: lines, Evidence: evidence, Config: enforced, At: checkTime})
		if err != nil {
			t.Fatal(err)
		}
		return verdict
	}
	for _, evidence := range []Evidence{"", AgentCounts, "gateway"} {
		t.Run(fmt.Sprintf("evidence %q", evidence), func(t *testing.T) {
			report := evaluate(t, Request{Usage: onList, Trust: TokenVerified, Evidence: evidence, Config: enforced})
			served := ledger(onLedger, evidence)
			for path, verdict := range map[string]Verdict{"the report": report, "the ledger": served} {
				got := result(t, verdict, RuleModelAllowlist)
				if verdict.Conclusion != Neutral || got.Outcome != Unproven || got.Reason != ReasonUnmeasuredSpend {
					t.Errorf("%s: every model on the list, on agent counts: %+v", path, verdict)
				}
			}
			if report.Evidence != "" || served.Evidence != AgentCounts {
				t.Errorf("the report states %q and the ledger %q", report.Evidence, served.Evidence)
			}
			off := evaluate(t, Request{Usage: verifiedUsage(t, "USD", "1", "gpt-5.5"), Trust: TokenVerified, Evidence: evidence, Config: enforced})
			for path, verdict := range map[string]Verdict{"the report": off, "the ledger": ledger(offLedger, evidence)} {
				if got := result(t, verdict, RuleModelAllowlist); verdict.Conclusion != Failure || got.Outcome != Fail || got.Reason != ReasonOutsideAllowlist {
					t.Errorf("%s: a model off the list, on agent counts: %+v", path, verdict)
				}
			}
		})
	}
	for _, evidence := range []Evidence{GatewayMeasured, Reconciled} {
		t.Run("evidence "+string(evidence), func(t *testing.T) {
			report := evaluate(t, Request{Usage: onList, Trust: TokenVerified, Evidence: evidence, Config: enforced})
			for path, verdict := range map[string]Verdict{"the report": report, "the ledger": ledger(onLedger, evidence)} {
				if got := result(t, verdict, RuleModelAllowlist); verdict.Conclusion != Success || got.Outcome != Pass || got.Reason != ReasonOnAllowlist {
					t.Errorf("%s: %+v", path, verdict)
				}
			}
		})
	}
}

// TestThreeTriggers holds a run to the median of earlier runs, a pull request
// to its total, and a repository to its month, each at, over and under its
// limit on both trust paths.
func TestThreeTriggers(t *testing.T) {
	type shape struct {
		name, settings, rule, over string
		baseline                   *Baseline
		lifetime                   *Lifetime
	}
	baseline := func(median string, trust Trust) *Baseline {
		return &Baseline{Runs: MinBaselineRuns, MedianUSD: mustAmount(t, median), Trust: trust}
	}
	lifetime := func(total string, trust Trust) *Lifetime {
		return &Lifetime{USD: mustAmount(t, total), Trust: trust}
	}
	shapes := func(trust Trust) []shape {
		return []shape{
			{"an increase in USD", "max_increase_usd: \"0.5\"", RuleRunIncrease, ReasonOverBaseline, baseline("2", trust), nil},
			{"an increase in percent", "max_increase_percent: 25", RuleRunIncreasePercent, ReasonOverBaseline, baseline("2", trust), nil},
			{"a pull request budget", "pr_budget_usd: \"10\"", RulePullRequestBudget, ReasonOverLimit, nil, lifetime("7.5", trust)},
		}
	}
	paths := []struct {
		trust    Trust
		evidence Evidence
		usage    func(cost string) Usage
		within   string
	}{
		{Unverified, "", func(cost string) Usage { return usageOf(t, line{"USD", cost, "", nil}) }, Unproven},
		{TokenVerified, GatewayMeasured, func(cost string) Usage { return verifiedUsage(t, "USD", cost, "m") }, Pass},
	}
	for _, path := range paths {
		for _, shape := range shapes(path.trust) {
			for _, tc := range []struct {
				at, cost, outcome, reason string
			}{
				{"under", "2.4", path.within, ReasonWithinLimit},
				{"at", "2.5", path.within, ReasonWithinLimit},
				{"over", "2.500000000001", Fail, shape.over},
			} {
				t.Run(fmt.Sprintf("%s %s on %s rows", shape.name, tc.at, path.trust), func(t *testing.T) {
					request := Request{Usage: path.usage(tc.cost), Trust: path.trust, Evidence: path.evidence, Config: config("policy:\n  " + shape.settings + "\n  enforce: true\n"), Baseline: shape.baseline, Lifetime: shape.lifetime}
					got := result(t, evaluate(t, request), shape.rule)
					reason := tc.reason
					if tc.outcome == Unproven {
						reason = ReasonUnverifiedRows
					}
					if got.Outcome != tc.outcome || got.Reason != reason {
						t.Errorf("%+v, want %s for %s", got, tc.outcome, reason)
					}
				})
			}
		}
	}
	t.Run("the figures each result was held to", func(t *testing.T) {
		usage := verifiedUsage(t, "USD", "3", "m")
		limits := config("policy:\n  max_increase_usd: \"0.5\"\n  max_increase_percent: \"12.5\"\n  pr_budget_usd: 10\n")
		verdict := evaluate(t, Request{Usage: usage, Trust: TokenVerified, Config: limits, Baseline: baseline("2", TokenVerified), Lifetime: lifetime("7.5", TokenVerified)})
		want := []Result{
			{Rule: RuleRunIncrease, Outcome: Warn, Reason: ReasonOverBaseline, HeldByWarnOnly: true, LimitUSD: "0.5", CostUSD: "3", BaselineUSD: "2"},
			{Rule: RuleRunIncreasePercent, Outcome: Warn, Reason: ReasonOverBaseline, HeldByWarnOnly: true, LimitPercent: "12.5", CostUSD: "3", BaselineUSD: "2"},
			{Rule: RulePullRequestBudget, Outcome: Warn, Reason: ReasonOverLimit, HeldByWarnOnly: true, LimitUSD: "10", CostUSD: "10.5"},
		}
		if !reflect.DeepEqual(verdict.Results, want) {
			t.Errorf("%+v", verdict.Results)
		}
	})
	t.Run("a percent of a median past twelve fractional digits compares exactly", func(t *testing.T) {
		limits := config("policy:\n  max_increase_percent: 50\n  enforce: true\n")
		for cost, outcome := range map[string]string{"0.000000000004": Pass, "0.000000000005": Fail} {
			verdict := evaluate(t, Request{Usage: verifiedUsage(t, "USD", cost, "m"), Trust: TokenVerified, Evidence: GatewayMeasured, Config: limits, Baseline: baseline("0.000000000003", TokenVerified)})
			if got := result(t, verdict, RuleRunIncreasePercent); got.Outcome != outcome {
				t.Errorf("USD %s against 150%% of USD 0.000000000003: %+v", cost, got)
			}
		}
	})
	t.Run("the pull request can be over while this run wrote no rows", func(t *testing.T) {
		verdict := evaluate(t, Request{Config: config("policy:\n  pr_budget_usd: 10\n  enforce: true\n"), Lifetime: lifetime("10.01", Unverified)})
		if got := result(t, verdict, RulePullRequestBudget); got.Outcome != Fail || got.CostUSD != "10.01" || verdict.Conclusion != Failure {
			t.Errorf("%+v", verdict)
		}
	})
	t.Run("a total that may count a report twice proves nothing", func(t *testing.T) {
		total := &Lifetime{USD: mustAmount(t, "90"), MayRepeat: true, Trust: TokenVerified}
		verdict := evaluate(t, Request{Usage: verifiedUsage(t, "USD", "1", "m"), Trust: TokenVerified, Config: config("policy:\n  pr_budget_usd: 10\n  enforce: true\n"), Lifetime: total})
		if got := result(t, verdict, RulePullRequestBudget); got.Outcome != Unproven || got.Reason != ReasonRepeatPossible {
			t.Errorf("%+v", got)
		}
	})
	t.Run("without what a rule reads, it does not compare", func(t *testing.T) {
		limits := config("policy:\n  max_increase_usd: 0\n  max_increase_percent: 0\n  pr_budget_usd: 0\n  monthly_budget_usd: 0\n  enforce: true\n")
		short := &Baseline{Runs: MinBaselineRuns - 1, MedianUSD: mustAmount(t, "0"), Trust: TokenVerified}
		for _, request := range []Request{
			{Usage: verifiedUsage(t, "USD", "900", "m"), Trust: TokenVerified, Config: limits},
			{Usage: verifiedUsage(t, "USD", "900", "m"), Trust: TokenVerified, Config: limits, Baseline: short},
			{Usage: usageOf(t, line{"USD", "900", "", nil}), Config: limits, Baseline: short},
		} {
			verdict := evaluate(t, request)
			want := map[string]string{RuleRunIncrease: ReasonNoBaseline, RuleRunIncreasePercent: ReasonNoBaseline, RulePullRequestBudget: ReasonNoLifetime, RuleMonthlyBudget: ReasonNoLedger}
			for rule, reason := range want {
				if got := result(t, verdict, rule); got.Outcome != Unproven || got.Reason != reason || got.CostUSD != "" || got.BaselineUSD != "" {
					t.Errorf("%s: %+v", rule, got)
				}
			}
			if verdict.Conclusion != Neutral {
				t.Errorf("concluded %s", verdict.Conclusion)
			}
		}
	})
	t.Run("a monthly budget needs a ledger on either path", func(t *testing.T) {
		for _, trust := range []Trust{Unverified, TokenVerified} {
			for _, cost := range []string{"99", "100", "101"} {
				verdict := evaluate(t, Request{Usage: verifiedUsage(t, "USD", cost, "m"), Trust: trust, Config: config("policy:\n  monthly_budget_usd: 100\n  enforce: true\n")})
				if got := result(t, verdict, RuleMonthlyBudget); got.Outcome != Unproven || got.Reason != ReasonNoLedger || got.LimitUSD != "100" {
					t.Errorf("%s USD %s: %+v", trust, cost, got)
				}
			}
		}
	})
	t.Run("no amount in another currency meets a USD limit", func(t *testing.T) {
		limits := config("policy:\n  max_increase_usd: \"0.5\"\n  max_increase_percent: 25\n  pr_budget_usd: 10\n  enforce: true\n")
		usage := Usage{Rows: 2, Currencies: []CurrencyUsage{
			{Currency: "EUR", ListCost: mustAmount(t, "900"), Rows: 1},
			{Currency: "USD", ListCost: mustAmount(t, "1"), Rows: 1},
		}}
		verdict := evaluate(t, Request{Usage: usage, Trust: TokenVerified, Config: limits, Baseline: baseline("2", TokenVerified), Lifetime: lifetime("1", TokenVerified)})
		for _, rule := range []string{RuleRunIncrease, RuleRunIncreasePercent, RulePullRequestBudget} {
			if got := result(t, verdict, rule); got.Outcome != Unproven || got.Reason != ReasonOtherCurrency {
				t.Errorf("%s: %+v", rule, got)
			}
		}
		earlierInEuros := &Lifetime{USD: mustAmount(t, "1"), OtherCurrency: true, Trust: TokenVerified}
		verdict = evaluate(t, Request{Usage: verifiedUsage(t, "USD", "1", "m"), Trust: TokenVerified, Config: limits, Lifetime: earlierInEuros})
		if got := result(t, verdict, RulePullRequestBudget); got.Outcome != Unproven || got.Reason != ReasonOtherCurrency || got.CostUSD != "2" {
			t.Errorf("%+v", got)
		}
	})
}

func TestIntegerAmountsAccepted(t *testing.T) {
	for _, key := range amountKeys {
		t.Run(key, func(t *testing.T) {
			whole, err := ParseSettings([]byte("policy:\n  " + key + ": 5\n"))
			if err != nil {
				t.Fatal(err)
			}
			quoted, err := ParseSettings([]byte("policy:\n  " + key + ": \"5.00\"\n"))
			if err != nil {
				t.Fatal(err)
			}
			if whole.Amounts[key].String() != "5" || whole.Amounts[key].Cmp(quoted.Amounts[key]) != 0 {
				t.Errorf("5 reads %v, \"5.00\" reads %v", whole.Amounts, quoted.Amounts)
			}
			if problems, err := schema.Validate([]byte("policy:\n  " + key + ": 5\n")); err != nil || len(problems) != 0 {
				t.Errorf("the schema refuses 5: %v, %v", problems, err)
			}
			for _, float := range []string{"5.0", "0.5", "1e3", "-5", ".inf"} {
				if _, err := ParseSettings([]byte("policy:\n  " + key + ": " + float + "\n")); err == nil || !strings.Contains(err.Error(), "a whole number or a quoted decimal") {
					t.Errorf("%s: %v", float, err)
				}
			}
		})
	}
	t.Run("a whole number holds a run to its budget", func(t *testing.T) {
		verdict := evaluate(t, Request{Usage: usageOf(t, line{"USD", "5.000000000001", "", nil}), Config: config("policy:\n  run_budget_usd: 5\n  enforce: true\n")})
		if got := result(t, verdict, RuleRunBudget); verdict.Config != ConfigFound || got.Outcome != Fail || got.LimitUSD != "5" {
			t.Errorf("%+v", verdict)
		}
	})
}
