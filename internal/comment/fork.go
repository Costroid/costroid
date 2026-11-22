package comment

import "github.com/Costroid/costroid/internal/policy"

// OnForkRows is the input of a report on rows a fork's own workflow run
// wrote. The recipes run no agent on a fork's pull request, so those rows are
// the fork's own claim: they neither block a merge nor pass a budget. A
// verdict that concludes otherwise is held at neutral. A rule the rows break
// reads as held whether it fails or the warn-only period holds it, because a
// fork's check stays neutral after the period too.
func (i Input) OnForkRows() Input {
	i.forkRows = true
	if i.Verdict == nil || i.Verdict.Conclusion == policy.Neutral {
		return i
	}
	held := *i.Verdict
	held.Conclusion = policy.Neutral
	i.Verdict = &held
	return i
}

// forkHeld reports a fork's own rows that break a rule, which the verdict
// sentence and the reason under it say is held at neutral.
func (i Input) forkHeld() bool {
	decided, ruled := deciding(i.Verdict)
	return i.forkRows && ruled && broken(decided)
}

// forkHeldLine is the reason under the sentence of a rule a fork's rows
// break. request is the host's word for a change.
func forkHeldLine(request string) string {
	return "This " + request + " comes from a fork, and the fork's own run wrote these rows. " +
		"Its check concludes neutral at most, so a rule that fails here warns and does not block a merge."
}

// forkOverBudget is the fold's line on a fork's own rows within a budget: what
// a run over it would conclude. The follow-up holds such a check at neutral,
// in the warn-only period and after it, so the line is one sentence in both
// and never says a rule would fail.
const forkOverBudget = "Over budget, the check would stay neutral, because a fork's rows are the fork's own claim."
