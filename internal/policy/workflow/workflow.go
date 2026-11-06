// Package workflow finds when the workflow a job runs from reached the base
// branch, which is where the warn-only period starts.
package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/policy"
)

const (
	// DefaultAPI is github.com's API root.
	DefaultAPI = "https://api.github.com"

	apiVersion     = "2022-11-28"
	requestTimeout = 10 * time.Second
	maxResponse    = 2 << 20
	commitsPerPage = 100
	// maxPages bounds the walk at 1,000 commits to one workflow file.
	maxPages = 10
)

var (
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)
	pathPattern       = regexp.MustCompile(`^\.github/workflows/[A-Za-z0-9_.-]{1,200}\.ya?ml$`)
	commitPattern     = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
)

// Path reads the workflow file from GITHUB_WORKFLOW_REF, which names
// owner/name/path@ref.
func Path(workflowRef, repository string) (string, error) {
	trimmed, _, found := strings.Cut(workflowRef, "@")
	path, inRepository := strings.CutPrefix(trimmed, repository+"/")
	if !found || !inRepository || !pathPattern.MatchString(path) {
		return "", errors.New("workflow: GITHUB_WORKFLOW_REF names no workflow file of this repository")
	}
	return path, nil
}

// GitHub reads one repository's commits with the job's token.
type GitHub struct {
	// API is the API root, DefaultAPI when empty.
	API        string
	Repository string
	Token      string
	Client     *http.Client
}

// Start returns the time of the oldest commit on base that touched path. A
// base without one is NotOnBase. A start it cannot read comes back zero, with
// the reason as the error.
func (g GitHub) Start(ctx context.Context, path, base string) (policy.Start, error) {
	added, err := g.oldest(ctx, path, base)
	if errors.Is(err, errNoCommit) {
		return policy.Start{NotOnBase: true}, nil
	}
	if err != nil {
		return policy.Start{}, err
	}
	return policy.Start{At: added}, nil
}

var errNoCommit = errors.New("workflow: no commit on the base touched the workflow")

type commit struct {
	Commit struct {
		Committer struct {
			Date time.Time `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
}

// oldest pages through the commits API, newest first, and keeps the last one.
func (g GitHub) oldest(ctx context.Context, path, base string) (time.Time, error) {
	if !repositoryPattern.MatchString(g.Repository) || !pathPattern.MatchString(path) || !commitPattern.MatchString(base) {
		return time.Time{}, errors.New("workflow: no repository, workflow file or base commit to read")
	}
	root, err := apiRoot(g.API)
	if err != nil {
		return time.Time{}, err
	}
	var last *commit
	for page := 1; page <= maxPages; page++ {
		query := url.Values{"sha": {base}, "path": {path}, "per_page": {strconv.Itoa(commitsPerPage)}, "page": {strconv.Itoa(page)}}
		var commits []commit
		if err := g.get(ctx, root+"/repos/"+g.Repository+"/commits?"+query.Encode(), &commits); err != nil {
			return time.Time{}, err
		}
		if len(commits) > 0 {
			last = &commits[len(commits)-1]
		}
		if len(commits) < commitsPerPage {
			if last == nil {
				return time.Time{}, errNoCommit
			}
			if last.Commit.Committer.Date.IsZero() {
				return time.Time{}, errors.New("workflow: the oldest commit states no date")
			}
			return last.Commit.Committer.Date, nil
		}
	}
	return time.Time{}, fmt.Errorf("workflow: more than %d commits touched %s", maxPages*commitsPerPage, path)
}

// get makes one request. A redirect comes back as an answer, so the token
// never follows one, and an error names the status, never the body.
func (g GitHub) get(ctx context.Context, endpoint string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("workflow: build the request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+g.Token)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", apiVersion)
	request.Header.Set("User-Agent", "costroid")
	if g.Client == nil {
		return errors.New("workflow: no HTTP client")
	}
	client := *g.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		var wrapped *url.Error
		if errors.As(err, &wrapped) {
			err = wrapped.Err
		}
		return fmt.Errorf("workflow: list commits: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("workflow: list commits answered %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponse)).Decode(out); err != nil {
		return fmt.Errorf("workflow: read the commits: %w", err)
	}
	return nil
}

// apiRoot keeps the token off plain HTTP, except to a loopback address.
func apiRoot(root string) (string, error) {
	if root == "" {
		root = DefaultAPI
	}
	parsed, err := url.Parse(root)
	if err != nil {
		return "", fmt.Errorf("workflow: API root: %w", err)
	}
	if parsed.Scheme == "http" {
		if ip := net.ParseIP(parsed.Hostname()); ip == nil || !ip.IsLoopback() {
			return "", errors.New("workflow: API root must be https")
		}
	} else if parsed.Scheme != "https" {
		return "", errors.New("workflow: API root must be https")
	}
	return strings.TrimRight(root, "/"), nil
}
