package comment

import (
	"fmt"
	"regexp"
	"time"

	"github.com/Costroid/costroid/internal/policy"
)

// Approval is a review that approved one commit, as the host stated it.
type Approval struct {
	// Approver is the reviewer's login, which no App's account can be.
	Approver string
	HeadSHA  string
	At       time.Time
}

// loginPattern is a person's login on GitHub. A bracket, as an App's account
// ends in, never matches.
var loginPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)

// Conclusion is the check's conclusion: failure when the action refused the
// artifact, the verdict's, neutral when no policy ran, and neutral when an
// approval binds a failed verdict's head.
func (i Input) Conclusion() string {
	if i.refused() {
		return policy.Failure
	}
	if i.Verdict == nil {
		return policy.Neutral
	}
	if i.approved() {
		return policy.Neutral
	}
	return i.Verdict.Conclusion
}

// approved reports an approval that turns this check's failure neutral.
func (i Input) approved() bool {
	return i.Verdict != nil && i.Verdict.Conclusion == policy.Failure && i.Approval.binds(i.HeadSHA)
}

// binds reports an approval of the commit head by a person.
func (a *Approval) binds(head string) bool {
	return a != nil && commitPattern.MatchString(head) && a.HeadSHA == head && loginPattern.MatchString(a.Approver) && !a.At.IsZero()
}

// ApprovedConclusion is the conclusion a check gets from the policy's
// verdict and an approval of its head: neutral for an approved failure, and
// the verdict otherwise. On rows the agent's job wrote, an approval never
// concludes success.
func ApprovedConclusion(verdict string, approval *Approval, head string) string {
	if verdict == policy.Failure && approval.binds(head) {
		return policy.Neutral
	}
	return verdict
}

// approvalLine says who approved which commit and when, and what that does to
// the check. The login passed loginPattern, so it renders as a code span.
func approvalLine(approval Approval) string {
	return fmt.Sprintf("`%s` approved commit `%s` at %s, so the check concludes neutral. The policy's verdict stays failure, and a new push ends the approval.",
		approval.Approver, approval.HeadSHA[:shortCommitLength], approval.At.UTC().Format("2006-01-02 15:04 MST"))
}
