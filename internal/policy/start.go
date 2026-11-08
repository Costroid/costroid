package policy

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/scm"
	"github.com/Costroid/costroid/internal/vcs"
)

// workflowPattern is a workflow file of the repository the job runs in.
var workflowPattern = regexp.MustCompile(`^\.github/workflows/[A-Za-z0-9_.-]{1,200}\.ya?ml$`)

// WorkflowPath reads the workflow file from GITHUB_WORKFLOW_REF, which names
// owner/name/path@ref.
func WorkflowPath(workflowRef, repository string) (string, error) {
	trimmed, _, found := strings.Cut(workflowRef, "@")
	path, inRepository := strings.CutPrefix(trimmed, repository+"/")
	if !found || !inRepository || !workflowPattern.MatchString(path) {
		return "", errors.New("policy: GITHUB_WORKFLOW_REF names no workflow file of this repository")
	}
	return path, nil
}

// History says when a file first reached a branch. scm.GitHub is one.
type History interface {
	FirstCommit(ctx context.Context, path, ref string) (time.Time, error)
}

// ReadStart reads when the workflow file reached the base. A base it has not
// reached is NotOnBase. A start it cannot read comes back zero, with the
// reason as the error, and the period keeps running.
func ReadStart(ctx context.Context, history History, path, base string) (Start, error) {
	if !vcs.IsCommit(base) {
		return Start{}, errors.New("policy: no base commit to read the workflow's history at")
	}
	added, err := history.FirstCommit(ctx, path, base)
	if errors.Is(err, scm.ErrNoCommit) {
		return Start{NotOnBase: true}, nil
	}
	if err != nil {
		return Start{}, err
	}
	return Start{At: added}, nil
}
