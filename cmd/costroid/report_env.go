package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/Costroid/costroid/internal/cicontext"
	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm"
)

// pullContext is what the CI environment says about the commit.
type pullContext struct {
	head string
	// base is the event's base commit, the merge-base on both hosts.
	base string
	// baseBranch names the base branch, and baseHead is the commit at its
	// head when the report resolved it.
	baseBranch  string
	baseHead    string
	pullRequest int
}

// readPullContext prefers the event file. On a pull request GITHUB_SHA names
// the test merge commit, and the check belongs on the head.
func readPullContext() (pullContext, error) {
	pull := pullContext{head: os.Getenv("GITHUB_SHA")}
	event, err := cicontext.ReadEvent(os.Getenv("GITHUB_EVENT_PATH"))
	if errors.Is(err, cicontext.ErrNoEventFile) {
		return pull, nil
	}
	if err != nil {
		return pull, err
	}
	if event.PullRequest != nil {
		pull.pullRequest = event.PullRequest.Number
		pull.head = cmp.Or(event.PullRequest.Head.SHA, pull.head)
		pull.base = event.PullRequest.Base.SHA
		pull.baseBranch = readBaseBranch(os.Getenv("GITHUB_EVENT_PATH"))
	}
	return pull, nil
}

// readBaseBranch reads pull_request.base.ref, which cicontext.Event does not
// carry. A file that cannot be read names no branch.
func readBaseBranch(path string) string {
	var event struct {
		PullRequest *struct {
			Base struct {
				Ref string `json:"ref"`
			} `json:"base"`
		} `json:"pull_request"`
	}
	if readJSON(path, &event) != nil || event.PullRequest == nil {
		return ""
	}
	return event.PullRequest.Base.Ref
}

// settingsRef is the commit costroid.yml is read from. On a pull request that
// is the base branch's head, or the event's base when the head was not
// resolved, so a pull request cannot change its own setting.
func (p pullContext) settingsRef() string {
	if p.pullRequest > 0 {
		return cmp.Or(p.baseHead, p.base)
	}
	return p.head
}

// resolveBaseHead reads the commit at the head of the base branch. A head that
// cannot be read leaves the event's base, and a warning says so.
func resolveBaseHead(ctx context.Context, host scm.Reporter, pull pullContext, stderr io.Writer) pullContext {
	if pull.pullRequest == 0 {
		return pull
	}
	head, err := resolveBranchHead(ctx, host, pull.baseBranch)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %s is read at the event's base commit %s, not the base branch's head: %v\n", configFile, pull.base, err)
		return pull
	}
	pull.baseHead = head
	return pull
}

func resolveBranchHead(ctx context.Context, host scm.Reporter, branch string) (string, error) {
	branches, ok := host.(policy.Branches)
	if !ok {
		return "", errors.New("this host resolves no branch")
	}
	ctx, cancel := context.WithTimeout(ctx, baseHeadTimeout)
	defer cancel()
	return policy.BaseHead(ctx, branches, branch)
}

// runURL is the page of the CI run this report belongs to, empty when the
// environment does not name one. The comment checks its form again.
func runURL(system cicontext.System) string {
	if system == cicontext.GitLabCI {
		return os.Getenv("CI_PIPELINE_URL")
	}
	server, repository, run := os.Getenv("GITHUB_SERVER_URL"), os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_RUN_ID")
	if server == "" || repository == "" || run == "" {
		return ""
	}
	return strings.TrimRight(server, "/") + "/" + repository + "/actions/runs/" + run
}

// reportTarget is where a report goes and what it says about itself there.
type reportTarget struct {
	host         scm.Reporter
	pull         pullContext
	installation string
	accountHost  string
}

// targetFromEnvironment selects the adapter. A job on a known CI system gets an
// error when it lacks what a post needs, and that error ends as a line on
// stderr.
func targetFromEnvironment(system cicontext.System, flags reportFlags, stderr io.Writer) (reportTarget, error) {
	if system == cicontext.GitLabCI {
		return gitLabTarget()
	}
	pull, err := readPullContext()
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
	}
	host, err := hostFromEnvironment(pull)
	return reportTarget{host: host, pull: pull, installation: flags.installation}, err
}

// ciRunFromEnvironment is GitHub Actions' run id and attempt, nil outside it
// or when either is not a whole number from 1.
func ciRunFromEnvironment(getenv func(string) string) *comment.BlockCIRun {
	if cicontext.Detect(getenv) != cicontext.GitHubActions {
		return nil
	}
	id, idErr := strconv.ParseInt(getenv("GITHUB_RUN_ID"), 10, 64)
	attempt, attemptErr := strconv.Atoi(getenv("GITHUB_RUN_ATTEMPT"))
	if idErr != nil || attemptErr != nil || id < 1 || attempt < 1 {
		return nil
	}
	return &comment.BlockCIRun{ID: id, Attempt: attempt}
}

// hostFromEnvironment runs in the customer's CI, outside the service's egress
// allowlist, so its client is a plain one.
func hostFromEnvironment(pull pullContext) (scm.Reporter, error) {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return nil, errors.New("GITHUB_TOKEN is not set")
	}
	if !headPattern.MatchString(pull.head) {
		return nil, errors.New("no head commit: GITHUB_SHA and the event file name none")
	}
	return scm.NewGitHub(scm.GitHubOptions{
		API:        os.Getenv("GITHUB_API_URL"),
		Repository: os.Getenv("GITHUB_REPOSITORY"),
		Tokens:     scm.StaticToken(token),
		Client:     &http.Client{},
	})
}

// gitLabTarget reads a merge request pipeline in the customer's CI, outside the
// service's egress allowlist, so its client is a plain one. The project is the merge
// request's, and a fork's pipeline is refused here as well as in the recipe's
// rules, so a fork's merge request never gets a post.
func gitLabTarget() (reportTarget, error) {
	run, err := cicontext.Read(os.Getenv)
	if err != nil {
		return reportTarget{}, err
	}
	token := os.Getenv(gitLabTokenVariable)
	project := os.Getenv("CI_MERGE_REQUEST_PROJECT_ID")
	switch {
	case token == "":
		return reportTarget{}, errors.New(gitLabTokenVariable + " is not set")
	case run.PullRequest == "":
		return reportTarget{}, errors.New("no merge request: CI_MERGE_REQUEST_IID names none")
	case run.Head == "":
		return reportTarget{}, errors.New("no head commit: CI_COMMIT_SHA names none")
	case os.Getenv("CI_MERGE_REQUEST_SOURCE_PROJECT_ID") != project || os.Getenv("CI_PROJECT_ID") != project:
		return reportTarget{}, errors.New("this pipeline is for a fork's merge request")
	}
	number, err := strconv.Atoi(run.PullRequest)
	if err != nil {
		return reportTarget{}, fmt.Errorf("merge request: %w", err)
	}
	host, err := scm.NewGitLab(scm.GitLabOptions{
		API:     os.Getenv("CI_API_V4_URL"),
		Project: project,
		Tokens:  scm.StaticToken(token),
		Client:  &http.Client{},
	})
	if err != nil {
		return reportTarget{}, err
	}
	return reportTarget{
		host:         host,
		pull:         pullContext{head: run.Head, base: run.Base, baseBranch: os.Getenv("CI_MERGE_REQUEST_TARGET_BRANCH_NAME"), pullRequest: number},
		installation: project,
		accountHost:  comment.AccountGitLab,
	}, nil
}
