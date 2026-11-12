package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/history"
	"github.com/Costroid/costroid/internal/scm"
)

const resultHelp = `Usage:
  costroid result --pr N [--repo OWNER/NAME] [--json]

result prints the result object of the newest costroid check run on the pull
request's head commit: its conclusion and its figures per currency and cost
basis. It reads the check run, never the comment, whose copy can withhold
amounts. --json prints the object whole, byte for byte as the check run
carries it. docs/agents.md describes the object.

result reads GitHub with GITHUB_TOKEN, and the repository from --repo, else
GITHUB_REPOSITORY, else the GitHub repository the git remote origin names, or
the only remote, which result says on stderr. It never asks gh for a token:
export GITHUB_TOKEN="$(gh auth token)" sets one with gh signed in.
GITHUB_API_URL names a GitHub Enterprise Server's API. GitLab takes no check,
so result reads GitHub alone.

Exit codes of result:
  0                 the object was printed
  1                 no costroid check run is on the head commit yet
  125               costroid could not read the pull request or its check run
`

var errResultArguments = errors.New("result needs --pr with the pull request's number, and takes no argument")

// errNoToken names the fix for a missing GITHUB_TOKEN. result never asks gh
// for a token itself.
var errNoToken = errors.New(`GITHUB_TOKEN is not set: set a token that can read the repository, such as with export GITHUB_TOKEN="$(gh auth token)" once gh is signed in`)

// resultTarget names the repository result reads, as owner/name. When it is
// empty, the repository is the one the git remote names, and said names it
// on that writer. The token is GITHUB_TOKEN's.
type resultTarget struct {
	repository string
	said       io.Writer
}

// githubPath is an owner/name a remote's address ends with.
var githubPath = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// remoteRepository is the GitHub repository the remote of the working
// directory's repository names, origin's or the only one's, with that
// remote's name.
func remoteRepository() (repository, remote string, err error) {
	if remote, err = remoteName("."); err != nil {
		return "", "", err
	}
	address, err := gitOutput(".", "remote", "get-url", remote)
	if err != nil {
		return "", "", fmt.Errorf("git could not read the remote %s: %w", remote, err)
	}
	address = strings.TrimSpace(address)
	server := remoteServer(address)
	if !strings.Contains(server, githubHost) {
		return "", "", fmt.Errorf("the remote %s names %s, and result reads GitHub alone", remote, server)
	}
	path := address
	if parsed, err := url.Parse(address); err == nil && parsed.Host != "" {
		path = parsed.Path
	} else if _, after, found := strings.Cut(address, ":"); found {
		path = after
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if !githubPath.MatchString(path) {
		return "", "", fmt.Errorf("the remote %s names no OWNER/NAME on GitHub", remote)
	}
	return path, remote, nil
}

// pullHost is what result needs from GitHub: the pull request's head and the
// commit's check runs.
type pullHost interface {
	PullRequest(ctx context.Context, number int) (scm.PullRequest, error)
	scm.History
}

// newest reads the newest result object on the pull request's head through
// the check run, in reportTimeout at most.
func (r resultTarget) newest(ctx context.Context, number int) (history.Posted, error) {
	ctx, cancel := context.WithTimeout(ctx, reportTimeout)
	defer cancel()
	host, err := r.host()
	if err != nil {
		return history.Posted{}, err
	}
	pull, err := host.PullRequest(ctx, number)
	if err != nil {
		return history.Posted{}, fmt.Errorf("read pull request %d: %w", number, err)
	}
	return history.Newest(ctx, host, pull.HeadSHA)
}

func (r resultTarget) host() (pullHost, error) {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return nil, errNoToken
	}
	repository := r.repository
	if repository == "" {
		found, remote, err := remoteRepository()
		if err != nil {
			return nil, fmt.Errorf("name the repository with --repo OWNER/NAME, or set GITHUB_REPOSITORY: %w", err)
		}
		repository = found
		if r.said != nil {
			fmt.Fprintf(r.said, "costroid: result reads %s, the repository the remote %s names\n", repository, remote)
		}
	}
	return scm.NewGitHub(scm.GitHubOptions{
		API:        os.Getenv("GITHUB_API_URL"),
		Repository: repository,
		Tokens:     scm.StaticToken(token),
		Client:     &http.Client{},
	})
}

func resultCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("result", flag.ContinueOnError)
	set.SetOutput(stderr)
	number := set.Int("pr", 0, "the pull request's number")
	repository := set.String("repo", os.Getenv("GITHUB_REPOSITORY"), "the repository as owner/name")
	asJSON := set.Bool("json", false, "print the result object whole")
	if err := parseFlags(set, args, resultHelp, stdout, stderr); errors.Is(err, flag.ErrHelp) {
		return 0
	} else if err != nil {
		return exitFailure
	}
	if *number < 1 || set.NArg() > 0 {
		fmt.Fprintf(stderr, "costroid: %v\n", errResultArguments)
		return exitFailure
	}
	posted, err := resultTarget{repository: *repository, said: stderr}.newest(ctx, *number)
	if errors.Is(err, history.ErrNoCheck) {
		fmt.Fprintf(stderr, "costroid: pull request %d: %v\n", *number, err)
		return exitRejected
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	if *asJSON {
		fmt.Fprintln(stdout, posted.Object)
		return 0
	}
	printPosted(stdout, *number, posted)
	return 0
}

// printPosted states the check's conclusion and one line per figure. Every
// string from the object passes comment.Clean, because any job of the
// repository can post a check run named costroid.
func printPosted(stdout io.Writer, number int, posted history.Posted) {
	block := posted.Block
	conclusion := comment.Data("none")
	if block.Conclusion != nil {
		conclusion = comment.Clean(*block.Conclusion)
	}
	fmt.Fprintf(stdout, "Pull request %d, commit %s, check run %d: %s.\n", number, comment.Clean(block.HeadSHA), posted.CheckRun, conclusion)
	if len(block.Figures) == 0 {
		fmt.Fprintln(stdout, "No figures.")
	}
	for _, figure := range block.Figures {
		amount := comment.Data("withheld")
		if figure.ListCost != "" {
			amount = comment.Clean(figure.ListCost)
		}
		fmt.Fprintf(stdout, "%s %s %s, %d rows.\n", comment.Clean(figure.Currency), amount, comment.Clean(figure.CostBasis), figure.Rows)
	}
	if block.FiguresOmitted > 0 {
		fmt.Fprintf(stdout, "%d more figures are left out of the object.\n", block.FiguresOmitted)
	}
}
