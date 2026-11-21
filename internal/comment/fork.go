package comment

import "github.com/Costroid/costroid/internal/policy"

// OnForkRows is the input of a report on rows a fork's own workflow run
// wrote. The recipes run no agent on a fork's pull request, so those rows are
// the fork's own claim: they neither block a merge nor pass a budget. A
// verdict that concludes otherwise is held at neutral, and the verdict
// sentence and the reason under it say so.
func (i Input) OnForkRows() Input {
	if i.Verdict == nil || i.Verdict.Conclusion == policy.Neutral {
		return i
	}
	held := *i.Verdict
	held.Conclusion = policy.Neutral
	i.Verdict, i.forkHeld = &held, true
	return i
}

// forkHeldLine is the reason under the sentence of a check OnForkRows held at
// neutral. request is the host's word for a change.
func forkHeldLine(request string) string {
	return "This " + request + " comes from a fork, and the fork's own run wrote these rows. " +
		"Its check concludes neutral at most, so a rule that fails here warns and does not block a merge."
}
