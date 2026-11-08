package policy

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/Costroid/costroid/internal/vcs"
)

// branchPattern is a branch name this package asks a host about. A name
// outside it is not resolved, and the report falls back to the event's base.
var branchPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/+-]{0,254}$`)

// Branches resolves a branch to the commit at its head. scm.GitHub and
// scm.GitLab are ones.
type Branches interface {
	BranchHead(ctx context.Context, branch string) (string, error)
}

// BaseHead reads the commit at the head of a pull request's base branch, where
// costroid.yml and the workflow's history are read. GitHub's event names the
// merge-base as the base commit, and so does GitLab's diff base. It moves only
// when the pull request updates, so a setting changed on the branch since then
// would not apply.
func BaseHead(ctx context.Context, branches Branches, branch string) (string, error) {
	if !branchPattern.MatchString(branch) || strings.Contains(branch, "..") {
		return "", errors.New("policy: the event names no base branch")
	}
	head, err := branches.BranchHead(ctx, branch)
	if err != nil {
		return "", fmt.Errorf("policy: read the head of %s: %w", branch, err)
	}
	if !vcs.IsCommit(head) {
		return "", fmt.Errorf("policy: the head of %s is not a commit", branch)
	}
	return head, nil
}
