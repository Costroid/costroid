// Package scm reaches a source-code host through one interface. GitHub and
// GitLab are the adapters so far.
package scm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// ErrNotFound reports a file or an object the host does not have.
var ErrNotFound = errors.New("scm: not found")

// ErrNoCommit reports a ref where no commit touched the path asked about.
var ErrNoCommit = errors.New("scm: no commit on the ref touched the path")

// ErrNoCheck reports a host where the adapter posts no check.
var ErrNoCheck = errors.New("scm: this host takes no check from this token")

// Visibility is who can read a repository.
type Visibility int

// A repository is public or it is not. Internal counts as private.
const (
	Private Visibility = iota
	Public
)

// CheckRun is one completed check.
type CheckRun struct {
	Name       string
	HeadSHA    string
	Conclusion string
	Title      string
	Summary    string
	Text       string
	Completed  time.Time
}

// PullRequest is what an approval needs to know about a pull request.
type PullRequest struct {
	Number  int
	HeadSHA string
	Author  string
}

// OpenPull is one open pull request as the host lists it.
type OpenPull struct {
	Number  int
	HeadSHA string
	// HeadRepository is owner/name, empty when the host no longer has it.
	HeadRepository string
}

// ForkHeads is what a fork's follow-up asks before it posts on a commit.
type ForkHeads interface {
	// OpenPullsFrom lists this repository's open pull requests whose head is
	// branch in a repository owner holds.
	OpenPullsFrom(ctx context.Context, owner, branch string) ([]OpenPull, error)
	// BranchesAt lists this repository's branches whose head is the commit.
	BranchesAt(ctx context.Context, sha string) ([]string, error)
}

// ListedCheck is one completed check run as a commit's list gives it.
type ListedCheck struct {
	ID   int64
	Text string
}

// History reads what earlier reports posted on a pull request.
type History interface {
	// PullCommits lists the pull request's commits, oldest first. Capped says
	// the list reached the most the host lists, so later commits can be
	// missing from it.
	PullCommits(ctx context.Context, number int) (commits []string, capped bool, err error)
	// CheckRuns lists the commit's completed check runs named name that
	// GitHub Actions or the App this token belongs to posted. No other App's
	// check run is listed, whatever its name.
	CheckRuns(ctx context.Context, sha, name string) ([]ListedCheck, error)
}

// Reporter is what a report needs from one repository on one source-code host.
type Reporter interface {
	// PostCheck returns the check's id. A host without one answers ErrNoCheck.
	PostCheck(ctx context.Context, check CheckRun) (int64, error)
	// UpdateCheck replaces the output of check run id with check's title,
	// summary and text. A host without checks answers ErrNoCheck.
	UpdateCheck(ctx context.Context, id int64, check CheckRun) error
	// UpsertComment edits the comment that opens with marker and that the
	// token's own account wrote, or creates it when there is none.
	UpsertComment(ctx context.Context, pullRequest int, marker, body string) error
	// ReadFile reads a file at a ref. A missing file is ErrNotFound.
	ReadFile(ctx context.Context, path, ref string) ([]byte, error)
	Visibility(ctx context.Context) (Visibility, error)
}

// CommentFinder finds a sticky comment without writing one.
type CommentFinder interface {
	// HasComment reports whether the token's own account wrote a comment on
	// the pull request that opens with marker.
	HasComment(ctx context.Context, pullRequest int, marker string) (bool, error)
}

// Host is one repository a person can approve a change on.
type Host interface {
	Reporter
	PullRequest(ctx context.Context, number int) (PullRequest, error)
	// CanWrite reports whether login holds a write role on the repository.
	CanWrite(ctx context.Context, login string) (bool, error)
}

// StatusError is an answer the host refused. It never carries the body, which
// is the host's text and can echo the request.
type StatusError struct {
	Method     string
	Path       string
	Status     int
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("scm: %s %s answered %d", e.Method, e.Path, e.Status)
}

// Is makes a 404 match ErrNotFound.
func (e *StatusError) Is(target error) bool {
	return target == ErrNotFound && e.Status == http.StatusNotFound
}
