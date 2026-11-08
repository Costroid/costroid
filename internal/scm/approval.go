package scm

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// The refusals an approval can answer.
var (
	ErrAuthorApproval = errors.New("scm: the pull request's author cannot approve it")
	ErrNoWriteRole    = errors.New("scm: the approver holds no write role")
	ErrStaleHead      = errors.New("scm: the pull request's head has moved")
	ErrNotAnApproval  = errors.New("scm: the event is not an approval")
	ErrNotApproved    = errors.New("scm: the review does not approve the change")
	ErrNoApprover     = errors.New("scm: the review names no signed-in approver")
	ErrBotApproval    = errors.New("scm: an App's account cannot approve")
	ErrOffHead        = errors.New("scm: the review is not on the pull request's head")
	ErrExpiredReview  = errors.New("scm: the review is older than an approval lasts")
	ErrNoApproval     = errors.New("scm: no review approves the pull request's head")
)

// The review states a host lists. A webhook writes them in lower case.
const (
	ReviewApproved         = "APPROVED"
	ReviewChangesRequested = "CHANGES_REQUESTED"
	ReviewCommented        = "COMMENTED"
	ReviewDismissed        = "DISMISSED"
	ReviewPending          = "PENDING"
)

// Review is one pull request review as the host lists it.
type Review struct {
	ID       int64
	Reviewer string
	// State is one of the Review states, in upper case.
	State       string
	CommitID    string
	SubmittedAt time.Time
}

// ReviewLister lists a pull request's reviews.
type ReviewLister interface {
	Reviews(ctx context.Context, number int) ([]Review, error)
}

// Roles answers whether an account can write to the repository.
type Roles interface {
	CanWrite(ctx context.Context, login string) (bool, error)
}

// ApprovalRules are what a repository asks of an approval: whether the
// author may approve, and how long an approval lasts.
type ApprovalRules struct {
	AllowAuthor bool
	Lasts       time.Duration
}

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

// Approval records who approved which commit, and when. A failed check that
// an approval binds concludes neutral and names the approver.
type Approval struct {
	PullRequest int
	Approver    string
	HeadSHA     string
	At          time.Time
	// ReviewID names the review that approved, 0 for the App's button.
	ReviewID int64
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

// CountReview holds one review to every rule. pull is the pull request as the
// host answers now, so a review of a commit the head has since left never
// counts. The write role is asked last, because it costs a request.
func CountReview(ctx context.Context, roles Roles, rules ApprovalRules, pull PullRequest, review Review, now time.Time) (Approval, error) {
	switch {
	case !strings.EqualFold(review.State, ReviewApproved):
		return Approval{}, ErrNotApproved
	case !loginPattern.MatchString(review.Reviewer):
		return Approval{}, ErrNoApprover
	case strings.HasSuffix(strings.ToLower(review.Reviewer), botSuffix):
		return Approval{}, ErrBotApproval
	case !rules.AllowAuthor && strings.EqualFold(pull.Author, review.Reviewer):
		return Approval{}, ErrAuthorApproval
	case !shaPattern.MatchString(pull.HeadSHA) || review.CommitID != pull.HeadSHA:
		return Approval{}, ErrOffHead
	case review.SubmittedAt.IsZero() || now.Sub(review.SubmittedAt) >= rules.Lasts:
		return Approval{}, ErrExpiredReview
	}
	allowed, err := roles.CanWrite(ctx, review.Reviewer)
	if err != nil {
		return Approval{}, fmt.Errorf("scm: read the approver's role: %w", err)
	}
	if !allowed {
		return Approval{}, ErrNoWriteRole
	}
	return Approval{PullRequest: pull.Number, Approver: review.Reviewer, HeadSHA: pull.HeadSHA, At: review.SubmittedAt, ReviewID: review.ID}, nil
}

// ApprovalOf finds the newest review that approves the pull request's head
// and passes every rule. Each reviewer's own latest approval, request for
// changes or dismissal is their standing: a later comment leaves it as it
// was. With no approval, the error says why the newest approval was refused.
func ApprovalOf(ctx context.Context, roles Roles, rules ApprovalRules, pull PullRequest, reviews []Review, now time.Time) (Approval, error) {
	standing := map[string]Review{}
	for _, review := range reviews {
		state := strings.ToUpper(review.State)
		if state == ReviewCommented || state == ReviewPending {
			continue
		}
		key := strings.ToLower(review.Reviewer)
		if held, found := standing[key]; !found || newer(review, held) {
			standing[key] = review
		}
	}
	approvals := slices.DeleteFunc(slices.Collect(maps.Values(standing)), func(review Review) bool {
		return !strings.EqualFold(review.State, ReviewApproved)
	})
	slices.SortFunc(approvals, func(a, b Review) int {
		if newer(a, b) {
			return -1
		}
		if newer(b, a) {
			return 1
		}
		return 0
	})
	refusal := ErrNoApproval
	for index, review := range approvals {
		approval, err := CountReview(ctx, roles, rules, pull, review, now)
		if err == nil {
			return approval, nil
		}
		if index == 0 {
			refusal = fmt.Errorf("%w: the newest approval, review %d: %w", ErrNoApproval, review.ID, err)
		}
	}
	return Approval{}, refusal
}

// newer orders reviews by when they were submitted, then by id.
func newer(a, b Review) bool {
	return cmp.Or(a.SubmittedAt.Compare(b.SubmittedAt), cmp.Compare(a.ID, b.ID)) > 0
}
