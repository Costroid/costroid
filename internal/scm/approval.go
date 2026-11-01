package scm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The refusals an approval can answer.
var (
	ErrAuthorApproval = errors.New("scm: the pull request's author cannot approve it")
	ErrNoWriteRole    = errors.New("scm: the approver holds no write role")
	ErrStaleHead      = errors.New("scm: the pull request's head has moved")
	ErrNotAnApproval  = errors.New("scm: the event is not an approval")
)

// ApprovalRequest is one press of the approve action.
type ApprovalRequest struct {
	PullRequest int
	Approver    string
	HeadSHA     string
	// AllowAuthor lets a pull request's author approve it. It is off by default.
	AllowAuthor bool
}

// RequestFrom reads an approval out of a queued event.
func RequestFrom(event Event) (ApprovalRequest, error) {
	if event.Name != EventCheckRun || event.Action != requestedAction || event.RequestedAction != ApproveAction || event.PullRequest <= 0 {
		return ApprovalRequest{}, ErrNotAnApproval
	}
	return ApprovalRequest{PullRequest: event.PullRequest, Approver: event.Sender, HeadSHA: event.HeadSHA}, nil
}

// Approval records who approved which commit. It flips nothing yet: the check
// stays neutral until a policy gives it a conclusion to flip.
type Approval struct {
	PullRequest int
	Approver    string
	HeadSHA     string
	At          time.Time
}

// Holds reports whether the approval still binds the pull request's head. A
// new push moves the head, and the approval lapses.
func (a Approval) Holds(head string) bool {
	return a.HeadSHA == head
}

// Approve verifies one approval against the host. Any error refuses it.
func Approve(ctx context.Context, host Host, request ApprovalRequest, now time.Time) (Approval, error) {
	pull, err := host.PullRequest(ctx, request.PullRequest)
	if err != nil {
		return Approval{}, fmt.Errorf("scm: read the pull request: %w", err)
	}
	if pull.HeadSHA != request.HeadSHA {
		return Approval{}, ErrStaleHead
	}
	if !request.AllowAuthor && strings.EqualFold(pull.Author, request.Approver) {
		return Approval{}, ErrAuthorApproval
	}
	allowed, err := host.CanWrite(ctx, request.Approver)
	if err != nil {
		return Approval{}, fmt.Errorf("scm: read the approver's role: %w", err)
	}
	if !allowed {
		return Approval{}, ErrNoWriteRole
	}
	return Approval{PullRequest: pull.Number, Approver: request.Approver, HeadSHA: pull.HeadSHA, At: now}, nil
}
