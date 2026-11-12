package policy

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/Costroid/costroid/internal/decimal"
)

// runs gives one cost per amount in the currency, the first amount on the
// oldest check run.
func runs(currency string, firstCheckRun int64, amounts ...string) []RunCost {
	costs := make([]RunCost, len(amounts))
	for index, amount := range amounts {
		costs[index] = RunCost{CheckRun: firstCheckRun + int64(index), Currency: currency, Cost: decimal.MustParse(amount)}
	}
	return costs
}

// counting gives the amounts from 1 to n, in that order.
func counting(n int) []string {
	amounts := make([]string, n)
	for index := range amounts {
		amounts[index] = fmt.Sprint(index + 1)
	}
	return amounts
}

func TestRollingPercentiles(t *testing.T) {
	cases := []struct {
		name     string
		costs    []RunCost
		runs     int
		p50, p95 string
	}{
		{"one run is every percentile", runs("USD", 1, "0.7"), 1, "0.7", "0.7"},
		{"two runs take the lower for P50, with no midpoint", runs("USD", 1, "1", "2"), 2, "1", "2"},
		{"ten runs: rank 5 and rank 10", runs("USD", 1, counting(10)...), 10, "5", "10"},
		{"eleven runs: rank 6 and rank 11, each rounded up", runs("USD", 1, counting(11)...), 11, "6", "11"},
		{"twenty runs: rank 10 and rank 19", runs("USD", 1, counting(20)...), 20, "10", "19"},
		{"the order of the costs is not the order of the runs", runs("USD", 1, "9", "1", "5", "3", "7", "2", "8", "4", "10", "6"), 10, "5", "10"},
		{
			"the window keeps the latest 20 runs by check run",
			runs("USD", 100, counting(25)...),
			BaselineWindow, "15", "24",
		},
		{
			"amounts past a float's precision stay exact",
			runs("USD", 1, "9007199254740993.000000000001", "9007199254740993.000000000003", "9007199254740993.000000000002"),
			3, "9007199254740993.000000000002", "9007199254740993.000000000003",
		},
		{
			"twelve fractional digits",
			runs("USD", 1, "0.000000000003", "0.000000000001", "0.000000000002", "0.000000000004"),
			4, "0.000000000002", "0.000000000004",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rolling(tc.costs)
			want := []percentiles{{Currency: "USD", Runs: tc.runs, P50: decimal.MustParse(tc.p50), P95: decimal.MustParse(tc.p95)}}
			if len(got) != 1 || got[0].Runs != tc.runs || got[0].P50.Cmp(want[0].P50) != 0 || got[0].P95.Cmp(want[0].P95) != 0 {
				t.Errorf("%+v, want %+v", got, want)
			}
		})
	}
	t.Run("the pack reads the P50, the P95 and the runs", func(t *testing.T) {
		baseline := BaselineOf(runs("USD", 1, counting(20)...), TokenVerified)
		settings := Settings{Amounts: map[string]decimal.Decimal{keyMaxIncrease: decimal.MustParse("0.5"), keyMaxIncreasePercent: decimal.MustParse("10")}}
		want := map[string]any{
			"trust": "token_verified", "runs": 20, "p50_usd": "10", "p95_usd": "19",
			keyMaxIncrease: "10.5", keyMaxIncreasePercent: "11",
		}
		if got := baselineInput(baseline, settings); !reflect.DeepEqual(got, want) {
			t.Errorf("%v, want %v", got, want)
		}
	})
	t.Run("the increase rules hold a run to the P50", func(t *testing.T) {
		baseline := BaselineOf(runs("USD", 1, counting(20)...), TokenVerified)
		limits := config("policy:\n  max_increase_usd: \"0.5\"\n  enforce: true\n")
		for cost, outcome := range map[string]string{"10.5": Pass, "10.500000000001": Fail} {
			verdict := evaluate(t, Request{Usage: verifiedUsage(t, "USD", cost, "m"), Trust: TokenVerified, Evidence: GatewayMeasured, Config: limits, Baseline: baseline})
			if got := result(t, verdict, RuleRunIncrease); got.Outcome != outcome || got.BaselineUSD != "10" {
				t.Errorf("USD %s against a P50 of USD 10 plus USD 0.5: %+v", cost, got)
			}
		}
	})
}

func TestBaselineNeedsItsMinimumSample(t *testing.T) {
	limits := config("policy:\n  max_increase_usd: \"0.5\"\n  max_increase_percent: 10\n  enforce: true\n")
	for _, tc := range []struct {
		runs    int
		outcome string
		reason  string
	}{
		{0, Unproven, ReasonNoBaseline},
		{1, Unproven, ReasonNoBaseline},
		{MinBaselineRuns - 1, Unproven, ReasonNoBaseline},
		{MinBaselineRuns, Fail, ReasonOverBaseline},
		{BaselineWindow + 5, Fail, ReasonOverBaseline},
	} {
		t.Run(fmt.Sprintf("%d runs", tc.runs), func(t *testing.T) {
			baseline := BaselineOf(runs("USD", 1, counting(tc.runs)...), TokenVerified)
			if tc.runs == 0 && baseline != nil {
				t.Fatalf("no runs read as a baseline: %+v", baseline)
			}
			if tc.runs > 0 && baseline.Runs != min(tc.runs, BaselineWindow) {
				t.Fatalf("%d runs read as %d", tc.runs, baseline.Runs)
			}
			if input := baselineInput(baseline, Settings{}); (input != nil) != (tc.reason == ReasonOverBaseline) {
				t.Errorf("the pack's input: %v", input)
			}
			verdict := evaluate(t, Request{Usage: verifiedUsage(t, "USD", "900", "m"), Trust: TokenVerified, Evidence: GatewayMeasured, Config: limits, Baseline: baseline})
			for _, rule := range []string{RuleRunIncrease, RuleRunIncreasePercent} {
				got := result(t, verdict, rule)
				if got.Outcome != tc.outcome || got.Reason != tc.reason {
					t.Errorf("%s: %+v, want %s for %s", rule, got, tc.outcome, tc.reason)
				}
				if tc.reason == ReasonNoBaseline && (got.BaselineUSD != "" || got.CostUSD != "") {
					t.Errorf("%s compared nothing and names figures: %+v", rule, got)
				}
			}
		})
	}
}

func TestBaselineNeverMixesCurrencies(t *testing.T) {
	inEuros := runs("EUR", 1, "900", "900", "900", "900", "900", "900", "900", "900", "900", "900")
	inDollars := runs("USD", 100, counting(MinBaselineRuns-1)...)
	got := rolling(append(append([]RunCost{}, inEuros...), inDollars...))
	want := []percentiles{
		{Currency: "EUR", Runs: 10, P50: decimal.MustParse("900"), P95: decimal.MustParse("900")},
		{Currency: "USD", Runs: MinBaselineRuns - 1, P50: decimal.MustParse("5"), P95: decimal.MustParse("9")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%+v, want %+v", got, want)
	}
	baseline := BaselineOf(append(append([]RunCost{}, inEuros...), inDollars...), TokenVerified)
	if baseline == nil || baseline.Runs != MinBaselineRuns-1 || baseline.MedianUSD.String() != "5" {
		t.Fatalf("ten runs in EUR filled a USD baseline: %+v", baseline)
	}
	limits := config("policy:\n  max_increase_usd: 0\n  enforce: true\n")
	verdict := evaluate(t, Request{Usage: verifiedUsage(t, "USD", "900", "m"), Trust: TokenVerified, Evidence: GatewayMeasured, Config: limits, Baseline: baseline})
	if got := result(t, verdict, RuleRunIncrease); got.Outcome != Unproven || got.Reason != ReasonNoBaseline {
		t.Errorf("nine runs in USD beside ten in EUR: %+v", got)
	}
	if BaselineOf(inEuros, TokenVerified) != nil {
		t.Error("runs in EUR alone gave a USD baseline")
	}
}
