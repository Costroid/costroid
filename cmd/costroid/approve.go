package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/cicontext"
	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/history"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm"
)

const (
	// approveEvent is the one event approve posts on. GitHub authenticates
	// the reviewer, and a review is never a GET.
	approveEvent = "pull_request_review"
	// approvalTimeout bounds the reads a report makes for an approval, so the
	// check and the comment keep time to post.
	approvalTimeout = 6 * time.Second
	day             = 24 * time.Hour
)

var (
	errApproveNotGitHubActions = errors.New("approve refused: GitHub Actions was not detected")
	errApproveArguments        = errors.New("approve takes no arguments")
	errNoReviews               = errors.New("this host lists no reviews")
	errHeadMoved               = errors.New("the pull request's head has moved since this run")
)

const approveHelp = `Usage:
  costroid approve

approve runs in a workflow that a pull request review starts. It reads the
pull request's reviews, and when one approves the head commit it posts the
costroid check again: an approved failure concludes neutral and names the
approver. When no approval counts any more, as after a dismissal, it posts
the policy's verdict again. It posts nothing when the newest check already
concludes so.

A review counts only when it approves, names the head commit the API answers
now, comes from an account that can write to the repository and is not an
App, is not the pull request's author's unless approval.allow_author is true,
and is younger than approval.expires_days, 7 by default. Each reviewer's
latest approval, request for changes or dismissal is their standing.

The settings are read from costroid.yml at the head of the base branch. A
review on a fork's pull request runs with a read-only token, so approve posts
nothing there. Like report, it exits 0 whatever the host answers, and outside
GitHub Actions it refuses and exits 125. docs/approval.md has the workflow.
`

// reviewEvent is the part of a pull_request_review event approve reads.
type reviewEvent struct {
	Review *struct {
		User *struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"review"`
	PullRequest *struct {
		Number int `json:"number"`
		Head   struct {
			SHA  string `json:"sha"`
			Repo *struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
	} `json:"pull_request"`
}

// approvalHost is what approve needs from the host: the pull request, its
// reviews, the reviewer's role and the commit's check runs.
type approvalHost interface {
	scm.Host
	scm.History
	scm.ReviewLister
}

func approveCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("approve", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprint(stderr, approveHelp) }
	if err := set.Parse(args); errors.Is(err, flag.ErrHelp) {
		return 0
	} else if err != nil {
		return exitFailure
	}
	if set.NArg() > 0 {
		fmt.Fprintf(stderr, "costroid: %v\n", errApproveArguments)
		return exitFailure
	}
	if cicontext.Detect(os.Getenv) != cicontext.GitHubActions {
		fmt.Fprintf(stderr, "costroid: %v\n", errApproveNotGitHubActions)
		return exitFailure
	}
	number, err := readReviewEvent(os.Getenv("GITHUB_EVENT_NAME"), os.Getenv("GITHUB_EVENT_PATH"), os.Getenv("GITHUB_REPOSITORY"))
	if err != nil {
		fmt.Fprintf(stderr, "costroid: not posted: %v\n", err)
		return 0
	}
	pull, err := readPullContext()
	if err != nil {
		fmt.Fprintf(stderr, "costroid: not posted: %v\n", err)
		return 0
	}
	reporter, err := hostFromEnvironment(pull)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: not posted: %v\n", err)
		return 0
	}
	host, ok := reporter.(approvalHost)
	if !ok {
		fmt.Fprintf(stderr, "costroid: not posted: %v\n", errNoReviews)
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, reportTimeout)
	defer cancel()
	if err := reconclude(ctx, host, resolveBaseHead(ctx, host, pull, stderr), number, time.Now(), stderr); err != nil {
		fmt.Fprintf(stderr, "costroid: not posted: %v\n", err)
	}
	return 0
}

// readReviewEvent reads the pull request's number from a review event on a
// branch of this repository. A fork's review runs with a read-only token, so
// its post would be refused.
func readReviewEvent(eventName, path, repository string) (int, error) {
	if eventName != approveEvent {
		return 0, fmt.Errorf("approve runs on the %s event, and this run's event is %q", approveEvent, eventName)
	}
	if path == "" {
		return 0, cicontext.ErrNoEventFile
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read the event file: %w", err)
	}
	var event reviewEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return 0, fmt.Errorf("parse the event file: %w", err)
	}
	switch {
	case event.Review == nil || event.Review.User == nil || event.Review.User.Login == "":
		return 0, errors.New("the event names no review by a signed-in account")
	case event.PullRequest == nil || event.PullRequest.Number <= 0 || !headPattern.MatchString(event.PullRequest.Head.SHA):
		return 0, errors.New("the event names no pull request and head commit")
	case event.PullRequest.Head.Repo == nil || !strings.EqualFold(event.PullRequest.Head.Repo.FullName, repository):
		return 0, errors.New("a review on a fork's pull request runs with a read-only token, so it cannot post the check. docs/approval.md says what a fork's pull request gets")
	}
	return event.PullRequest.Number, nil
}

// reconclude posts the newest costroid check on the head again when an
// approval changes its conclusion, or when one no longer does. An error says
// why nothing was posted.
func reconclude(ctx context.Context, host approvalHost, pull pullContext, number int, now time.Time, stderr io.Writer) error {
	current, err := host.PullRequest(ctx, number)
	if err != nil {
		return fmt.Errorf("read the pull request: %w", err)
	}
	approval := findApproval(ctx, host, pull.settingsRef(), current, now, stderr)
	newest, err := history.Newest(ctx, host, current.HeadSHA)
	if errors.Is(err, history.ErrNoCheck) {
		return fmt.Errorf("no costroid check is on commit %s yet, and the report applies an approval when it posts", current.HeadSHA)
	}
	if err != nil {
		return err
	}
	previous, block := newest.CheckRun, newest.Block
	conclusion := block.ApprovedConclusion(approval)
	if block.Conclusion != nil && *block.Conclusion == conclusion {
		return fmt.Errorf("check run %d already concludes %s", previous, conclusion)
	}
	render := func(id *int64) (comment.CheckOutput, error) {
		named := block
		if id != nil {
			named.CheckRunID = id
		}
		return comment.Reconclude(named, approval, previous)
	}
	id, err := postCheck(ctx, host, current.HeadSHA, conclusion, render, stderr)
	if err != nil {
		return err
	}
	nameCheck(ctx, host, *id, render, stderr)
	return nil
}

// findApproval returns the approval of the head that passes every rule, or
// why none counts. Settings that cannot be read give no rules, so no approval
// counts.
func findApproval(ctx context.Context, host approvalHost, settingsRef string, pull scm.PullRequest, now time.Time, stderr io.Writer) *comment.Approval {
	approval, err := approvalOf(ctx, host, settingsRef, pull, now)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", fmt.Errorf("no approval counts: %w", err))
		return nil
	}
	return &comment.Approval{Approver: approval.Approver, HeadSHA: approval.HeadSHA, At: approval.At}
}

func approvalOf(ctx context.Context, host approvalHost, settingsRef string, pull scm.PullRequest, now time.Time) (scm.Approval, error) {
	rules, err := approvalRules(ctx, host, settingsRef)
	if err != nil {
		return scm.Approval{}, err
	}
	reviews, err := host.Reviews(ctx, pull.Number)
	if err != nil {
		return scm.Approval{}, fmt.Errorf("read the reviews: %w", err)
	}
	return scm.ApprovalOf(ctx, host, rules, pull, reviews, now)
}

// approvalRules reads the approval keys of costroid.yml at the settings
// commit. A file that cannot be read or holds an invalid key gives no rules.
func approvalRules(ctx context.Context, files policy.FileReader, ref string) (scm.ApprovalRules, error) {
	config := policy.ReadConfig(ctx, files, policy.Commits{Base: ref, PullRequest: true})
	if config.Unread != nil {
		return scm.ApprovalRules{}, config.Unread
	}
	settings, err := policy.ParseApproval(config.Data)
	if err != nil {
		return scm.ApprovalRules{}, err
	}
	return scm.ApprovalRules{AllowAuthor: settings.AllowAuthor, Lasts: time.Duration(settings.ExpiresDays) * day}, nil
}

// reportApproval is the approval a report applies to a failed verdict: a
// review of this head that passes every rule. A push moves the head, so an
// approval of an earlier commit never reaches it.
func reportApproval(ctx context.Context, host scm.Reporter, pull pullContext, verdict *policy.Verdict, stderr io.Writer) *comment.Approval {
	if verdict == nil || verdict.Conclusion != policy.Failure || pull.pullRequest == 0 {
		return nil
	}
	reviewed, ok := host.(approvalHost)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, approvalTimeout)
	defer cancel()
	current, err := reviewed.PullRequest(ctx, pull.pullRequest)
	if err == nil && current.HeadSHA != pull.head {
		err = errHeadMoved
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", fmt.Errorf("no approval counts: %w", err))
		return nil
	}
	return findApproval(ctx, reviewed, pull.settingsRef(), current, time.Now(), stderr)
}
