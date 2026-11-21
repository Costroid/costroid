package comment

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/policy"
)

// The repository's own pack the golden states name, and its source, which
// the digest is of.
const (
	goldenPackPath   = ".costroid/policy.rego"
	goldenPackSource = "package costroid.pack\n\nresults contains {\"rule\": \"no_opus_on_docs\", \"outcome\": \"fail\", \"reason\": \"model_on_docs_change\"} if {\n\tsome usage in input.usage\n\tsome model, _ in usage.models\n\tstartswith(model, \"claude-opus\")\n}\n"
	goldenPackConfig = "policy_pack: " + goldenPackPath + "\n"
)

// The pack's results the golden states hold: a rule that fails, and one that
// warns, each with the pack's own message.
var (
	packFailure = policy.PackResult{Rule: "no_opus_on_docs", Outcome: policy.PackFail, Reason: "model_on_docs_change", Message: "Opus models are not used on a change to docs/ alone."}
	packWarning = policy.PackResult{Rule: "large_run", Outcome: policy.PackWarn, Reason: "over_team_estimate", Message: "This run cost more than the team estimate in .costroid/estimates.md."}
)

func sha256Hex(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

// joinPack joins a pack's results, or Costroid's customer_pack result for a
// fault, to a verdict evaluated under body as the engine does: a failure the
// warn-only period holds warns, and the conclusion only rises, to failure on
// a failure and to neutral on anything else. The verdict names the
// costroid.yml that adds policy_pack to body. The pack's digest is of its
// source, or of zero bytes when it was not read.
func joinPack(verdict *policy.Verdict, body string, results []policy.PackResult, fault *policy.Result) *policy.Verdict {
	joined := *verdict
	joined.Results = append([]policy.Result{}, verdict.Results...)
	joined.ConfigSHA256 = sha256Hex(goldenPackConfig + body)
	held := joined.WarnOnly.Active
	raise := func(fails bool) {
		switch {
		case fails:
			joined.Conclusion = policy.Failure
		case joined.Conclusion == policy.Success:
			joined.Conclusion = policy.Neutral
		}
	}
	run := &policy.PackRun{Path: goldenPackPath, SHA256: sha256Hex(goldenPackSource), Results: []policy.PackResult{}}
	if fault != nil {
		result := *fault
		if result.Outcome == policy.Fail && held {
			result.Outcome, result.HeldByWarnOnly = policy.Warn, true
		}
		if result.Reason == policy.ReasonPackUnread {
			run.SHA256 = sha256Hex("")
		}
		joined.Results = append(joined.Results, result)
		raise(result.Outcome == policy.Fail)
	}
	for _, result := range results {
		if result.Outcome == policy.PackFail && held {
			result.Outcome, result.HeldByWarnOnly = policy.PackWarn, true
		}
		run.Results = append(run.Results, result)
		raise(result.Outcome == policy.PackFail)
	}
	joined.Pack = run
	return &joined
}

// packBase is Costroid's verdict on the golden rows under body at the time
// given, before the pack is joined.
func packBase(t *testing.T, body string, at time.Time) *policy.Verdict {
	t.Helper()
	return rulesVerdict(t, settings(body), at, nil, nil)
}

// packStates are the states of a repository's own pack, each shown and
// withheld: a pack rule that fails, after the warn-only period and inside it;
// one that warns; a pack that is not valid; one that could not be read; one
// past its bound, after the period and inside it; and one whose answer was not
// in its words. Beside them: a pack that returned nothing, a failing budget
// that decides before a failing pack rule, a pull request that changes the
// pack, and a pack beside the P95 limit.
func packStates(t *testing.T) map[string]Input {
	t.Helper()
	after, inside := packBase(t, "", afterPeriod), packBase(t, "", insidePeriod)
	fault := func(reason string, outcome string, line int) *policy.Result {
		return &policy.Result{Rule: policy.RuleCustomerPack, Outcome: outcome, Reason: reason, Line: line}
	}
	shown := map[string]Input{
		"pack-rule-fails":               withVerdict(t, joinPack(after, "", []policy.PackResult{packFailure}, nil)),
		"pack-rule-fails-warn-only":     withVerdict(t, joinPack(inside, "", []policy.PackResult{packFailure}, nil)),
		"pack-rule-warns":               withVerdict(t, joinPack(after, "", []policy.PackResult{packWarning}, nil)),
		"pack-not-valid":                withVerdict(t, joinPack(after, "", nil, fault(policy.ReasonPackInvalid, policy.Unproven, 4))),
		"pack-not-read":                 withVerdict(t, rulesVerdict(t, settings(goldenPackConfig), insidePeriod, nil, nil)),
		"pack-past-its-bound":           withVerdict(t, joinPack(after, "", nil, fault(policy.ReasonPackBound, policy.Fail, 0))),
		"pack-past-its-bound-warn-only": withVerdict(t, joinPack(inside, "", nil, fault(policy.ReasonPackBound, policy.Fail, 0))),
		"pack-answer-refused":           withVerdict(t, joinPack(after, "", nil, fault(policy.ReasonPackResult, policy.Fail, 0))),
	}
	states := map[string]Input{}
	for name, input := range shown {
		states[name], states[name+"-withheld"] = input, withheld(input)
	}
	states["pack-returned-nothing"] = withVerdict(t, joinPack(after, "", nil, nil))
	budget := packBase(t, overBudget, afterPeriod)
	states["pack-beside-a-failing-budget"] = withVerdict(t, joinPack(budget, overBudget, []policy.PackResult{packFailure, packWarning}, nil))
	changed := joinPack(inside, "", []policy.PackResult{packWarning}, nil)
	changed.PackChange = policy.PackFileChanged
	states["pack-changed-at-head"] = withVerdict(t, changed)
	p95 := p95States(t)["p95-over"]
	p95.Verdict = joinPack(p95.Verdict, p95Config, []policy.PackResult{packWarning}, nil)
	states["pack-beside-the-p95-limit"] = p95
	return states
}

// TestPackStatesHoldWhatTheEngineWrites holds the golden pack states to the
// engine where no child is needed: a pack costroid.yml names and nothing read
// is Costroid's customer_pack pack_unread, with the digest of zero bytes, and
// joinPack writes the same verdict for it. Each state's conclusion is the one
// its outcomes give.
func TestPackStatesHoldWhatTheEngineWrites(t *testing.T) {
	states := packStates(t)
	engine := states["pack-not-read"].Verdict
	built := joinPack(packBase(t, "", insidePeriod), "", nil, &policy.Result{Rule: policy.RuleCustomerPack, Outcome: policy.Unproven, Reason: policy.ReasonPackUnread})
	if engine.Pack == nil || engine.Pack.Results == nil || len(engine.Pack.Results) != 0 {
		t.Fatalf("the engine wrote %+v", engine.Pack)
	}
	if engine.ConfigSHA256 != built.ConfigSHA256 || engine.Pack.SHA256 != built.Pack.SHA256 || engine.Conclusion != built.Conclusion ||
		!reflect.DeepEqual(engine.Results, built.Results) {
		t.Errorf("the engine wrote %+v beside %+v, joinPack %+v beside %+v", engine.Results, engine.Pack, built.Results, built.Pack)
	}
	for name, want := range map[string]string{
		"pack-rule-fails": policy.Failure, "pack-rule-fails-warn-only": policy.Neutral, "pack-rule-warns": policy.Neutral,
		"pack-not-valid": policy.Neutral, "pack-not-read": policy.Neutral, "pack-past-its-bound": policy.Failure,
		"pack-past-its-bound-warn-only": policy.Neutral, "pack-answer-refused": policy.Failure, "pack-returned-nothing": policy.Neutral,
		"pack-beside-a-failing-budget": policy.Failure, "pack-beside-the-p95-limit": policy.Failure,
	} {
		if got := states[name].Conclusion(); got != want {
			t.Errorf("%s concludes %s, want %s", name, got, want)
		}
	}
}

// TestAPackRuleDecidesAfterEveryFailingRuleOfCostroids names the result the
// verdict sentence states when a pack's result stands beside Costroid's: a
// failing rule of Costroid's comes before a failing pack rule, which comes
// before a rule of Costroid's the warn-only period holds, which comes before
// a pack's failure the period holds, then a pack's warning, then a rule that
// could not pass. The pack's message never reaches the sentence, and its
// line follows in the body as a code span.
func TestAPackRuleDecidesAfterEveryFailingRuleOfCostroids(t *testing.T) {
	over := policy.Result{Rule: policy.RuleRunBudget, Outcome: policy.Fail, Reason: policy.ReasonOverLimit, LimitUSD: "0.001", CostUSD: realTotal}
	held := policy.Result{Rule: policy.RuleRunBudget, Outcome: policy.Warn, Reason: policy.ReasonOverLimit, HeldByWarnOnly: true, LimitUSD: "0.001", CostUSD: realTotal}
	unproven := policy.Result{Rule: policy.RuleRunBudget, Outcome: policy.Unproven, Reason: policy.ReasonUnverifiedRows, LimitUSD: "5", CostUSD: realTotal}
	heldFailure := packFailure
	heldFailure.Outcome, heldFailure.HeldByWarnOnly = policy.PackWarn, true
	for _, tc := range []struct {
		name    string
		ours    policy.Result
		pack    policy.PackResult
		opening string
	}{
		{"a failing budget before a failing pack rule", over, packFailure, "Over budget: "},
		{"a failing pack rule before a held budget", held, packFailure, "Pack rule failed (no_opus_on_docs): "},
		{"a held budget before a held pack rule", held, heldFailure, "Over budget, warn-only: "},
		{"a held pack rule before a pack's warning", unproven, heldFailure, "Pack rule failed, warn-only (no_opus_on_docs): "},
		{"a pack's warning before a budget that could not pass", unproven, packWarning, "Pack rule warned (large_run): "},
	} {
		input := verdictOf(t, policy.Neutral, tc.ours)
		input.Verdict.Pack = &policy.PackRun{Path: goldenPackPath, Results: []policy.PackResult{tc.pack}}
		sentence := VerdictSentence(input)
		if !strings.HasPrefix(sentence, tc.opening) || strings.Contains(sentence, tc.pack.Message) {
			t.Errorf("%s: %s", tc.name, sentence)
		}
		if body := commentOf(t, input); !strings.Contains(body, ": `"+tc.pack.Message+"`") {
			t.Errorf("%s: the body does not hold the pack's message as a code span:\n%s", tc.name, body)
		}
	}
}
