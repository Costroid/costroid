package policy

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"testing"
)

// packOutcomes are the outcomes a pack's result can carry when compose is
// handed one: the pack's two words, and two the decoder never lets through.
var packOutcomes = []PackOutcome{PackWarn, PackFail, "pass", ""}

// multisets returns every multiset of up to size elements of values.
func multisets[T any](values []T, size int) [][]T {
	sets := [][]T{{}}
	var grow func(prefix []T, from int)
	grow = func(prefix []T, from int) {
		if len(prefix) == size {
			return
		}
		for index := from; index < len(values); index++ {
			next := append(slices.Clone(prefix), values[index])
			sets = append(sets, next)
			grow(next, index)
		}
	}
	grow(nil, 0)
	return sets
}

// composeFaults are no fault, a fault of each reason, and one of a reason no
// table holds.
func composeFaults() []*PackFault {
	faults := []*PackFault{nil}
	for _, reason := range slices.Sorted(maps.Keys(packFaultOutcomes)) {
		faults = append(faults, fault(reason, 3, errors.New("a fault")))
	}
	return append(faults, fault("pack_unknown", 0, errors.New("a fault")))
}

func TestComposeNeverLowersAConclusion(t *testing.T) {
	baseResults := []Result{
		{Rule: RuleRunBudget, Outcome: Pass, Reason: ReasonWithinLimit, LimitUSD: "5", CostUSD: "1"},
		{Rule: RuleAttribution, Outcome: Warn, Reason: ReasonJoinableRows},
	}
	packs := multisets(packOutcomes, 3)
	cases := 0
	for _, conclusion := range []string{Success, Neutral, Failure} {
		for _, active := range []bool{false, true} {
			for _, problem := range composeFaults() {
				for _, outcomes := range packs {
					base := Verdict{Conclusion: conclusion, WarnOnly: WarnOnly{Active: active}, Results: slices.Clone(baseResults)}
					var pack []PackResult
					for index, outcome := range outcomes {
						pack = append(pack, PackResult{Rule: fmt.Sprintf("team_%d", index), Outcome: outcome, Reason: "reason"})
					}
					name := fmt.Sprintf("%s, warn-only %t, fault %v, pack %q", conclusion, active, problem, outcomes)
					assertCompose(t, name, base, pack, problem, compose(base, slices.Clone(pack), problem))
					if !reflect.DeepEqual(base.Results, baseResults) {
						t.Fatalf("%s: compose wrote Costroid's results: %+v", name, base.Results)
					}
					cases++
				}
			}
		}
	}
	if want := 3 * 2 * len(composeFaults()) * len(packs); cases != want || len(packs) != 35 {
		t.Fatalf("enumerated %d cases of %d, %d packs", cases, want, len(packs))
	}
}

// assertCompose holds one composition to the order and to what it may
// write: the conclusion is the highest of the base's and of each thing the
// pack added, Costroid's results stay as they were, a fault is Costroid's own
// customer_pack result with the outcome the table gives, and a pack's failure
// warns while the warn-only period runs.
func assertCompose(t *testing.T, name string, base Verdict, pack []PackResult, problem *PackFault, out Composed) {
	t.Helper()
	active := base.WarnOnly.Active
	want := severity[base.Conclusion]
	extra := 0
	if problem != nil {
		extra = 1
		outcome, known := packFaultOutcomes[problem.reason]
		if !known {
			outcome = Fail
		}
		added := out.Verdict.Results[len(out.Verdict.Results)-1]
		wantOutcome := outcome
		if outcome == Fail && active {
			wantOutcome = Warn
		}
		if added.Rule != ruleCustomerPack || added.Reason != problem.reason || added.Outcome != wantOutcome || added.HeldByWarnOnly != (outcome == Fail && active) {
			t.Fatalf("%s: the fault became %+v", name, added)
		}
		if out.Line != problem.Line {
			t.Fatalf("%s: line %d of a fault at %d", name, out.Line, problem.Line)
		}
		want = max(want, level(outcome == Fail && !active))
	}
	if len(out.Verdict.Results) != len(base.Results)+extra || !reflect.DeepEqual(out.Verdict.Results[:len(base.Results)], base.Results) {
		t.Fatalf("%s: Costroid's results became %+v", name, out.Verdict.Results)
	}
	if len(out.Pack) != len(pack) {
		t.Fatalf("%s: %d pack results of %d", name, len(out.Pack), len(pack))
	}
	for index, result := range out.Pack {
		failed := pack[index].Outcome == PackFail
		if failed && active && (result.Outcome != PackWarn || !result.HeldByWarnOnly) {
			t.Fatalf("%s: a pack's failure in the warn-only period became %+v", name, result)
		}
		want = max(want, level(failed && !active))
	}
	if got := severity[out.Verdict.Conclusion]; got < severity[base.Conclusion] || got != want {
		t.Fatalf("%s: concluded %s", name, out.Verdict.Conclusion)
	}
}

// level is the severity a result adds: failure for one that fails, and
// neutral for any other.
func level(fails bool) int {
	if fails {
		return severity[Failure]
	}
	return severity[Neutral]
}
