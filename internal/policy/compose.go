package policy

import "slices"

// severity orders the conclusions. A customer pack moves a conclusion up it,
// never down.
var severity = map[string]int{Success: 0, Neutral: 1, Failure: 2}

// Composed is Costroid's verdict with a customer pack's results joined to it.
type Composed struct {
	// Verdict is Costroid's verdict. Its results are Costroid's, unchanged,
	// and on a fault one more: its own customer_pack result.
	Verdict Verdict
	// Pack is the pack's results, apart from Costroid's.
	Pack []PackResult

	// problem is why the pack gave no results, nil when it gave its answer.
	problem *PackFault
}

// compose joins a pack's results to Costroid's verdict. It reads
// base.Results and never writes them, and the conclusion it returns is never
// below base's: raise only assigns a conclusion of greater severity, and no
// outcome maps to success. A fault becomes Costroid's own customer_pack
// result, its outcome taken from packFaultOutcomes, with the line the
// compiler named when the pack is not valid. compose runs after every
// adjustment of Costroid's own, so nothing lowers its answer.
func compose(base Verdict, pack []PackResult, problem *PackFault) Composed {
	out := Composed{Verdict: base, problem: problem}
	out.Verdict.Results = slices.Clone(base.Results)
	conclusion := base.Conclusion
	raise := func(outcome string) {
		to := Neutral
		if outcome == Fail {
			to = Failure
		}
		if severity[to] > severity[conclusion] {
			conclusion = to
		}
	}
	if problem != nil {
		result := heldByWarnOnly(problem.result(), base.WarnOnly.Active)
		out.Verdict.Results = append(out.Verdict.Results, result)
		raise(result.Outcome)
	}
	for _, result := range pack {
		if result.Outcome == PackFail && base.WarnOnly.Active {
			result.Outcome, result.HeldByWarnOnly = PackWarn, true
		}
		out.Pack = append(out.Pack, result)
		raise(string(result.Outcome))
	}
	out.Verdict.Conclusion = conclusion
	return out
}

// heldByWarnOnly turns a failure into a warning while the warn-only period
// runs, as the default pack does.
func heldByWarnOnly(result Result, active bool) Result {
	if result.Outcome == Fail && active {
		result.Outcome, result.HeldByWarnOnly = Warn, true
	}
	return result
}
