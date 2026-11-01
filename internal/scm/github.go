package scm

import (
	"bytes"
	"context"
	"encoding/base64"
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
)

const (
	// DefaultAPI is github.com's API root.
	DefaultAPI = "https://api.github.com"

	apiVersion      = "2022-11-28"
	requestTimeout  = 10 * time.Second
	maxResponse     = 2 << 20
	commentsPerPage = 100
	// maxCommentPages bounds the search for the sticky comment at 3,000.
	maxCommentPages = 30
	botAccount      = "Bot"
)

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)

// TokenSource supplies the bearer token of each request.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// StaticToken is a token CI hands the process.
type StaticToken string

// Token returns the token.
func (t StaticToken) Token(context.Context) (string, error) {
	return string(t), nil
}

// GitHubOptions configures a GitHub adapter.
type GitHubOptions struct {
	// API is the API root, DefaultAPI when empty.
	API string
	// Repository is owner/name.
	Repository string
	Tokens     TokenSource
	// Limits and Installation apply the App's write limits. A token from CI
	// has neither.
	Limits       *Limiter
	Installation int64
}

// GitHub is the Host for one repository.
type GitHub struct {
	api          string
	repository   string
	tokens       TokenSource
	limits       *Limiter
	installation int64
	client       *http.Client
}

// NewGitHub returns the adapter. The token never travels over plain HTTP,
// except to a loopback address.
func NewGitHub(options GitHubOptions) (*GitHub, error) {
	if !repositoryPattern.MatchString(options.Repository) {
		return nil, errors.New("scm: repository is not owner/name")
	}
	root := options.API
	if root == "" {
		root = DefaultAPI
	}
	if err := checkRoot(root); err != nil {
		return nil, err
	}
	return &GitHub{
		api:          strings.TrimRight(root, "/"),
		repository:   options.Repository,
		tokens:       options.Tokens,
		limits:       options.Limits,
		installation: options.Installation,
		client:       newClient(),
	}, nil
}

func newClient() *http.Client {
	return &http.Client{
		Timeout: requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func checkRoot(root string) error {
	parsed, err := url.Parse(root)
	if err != nil {
		return fmt.Errorf("scm: API root: %w", err)
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme == "http" {
		if ip := net.ParseIP(parsed.Hostname()); ip != nil && ip.IsLoopback() {
			return nil
		}
	}
	return errors.New("scm: API root must be https")
}

func (g *GitHub) repo(suffix string) string {
	return "/repos/" + g.repository + suffix
}

func (g *GitHub) do(ctx context.Context, method, path string, in, out any) error {
	if g.limits != nil {
		release, err := g.limits.Admit(g.installation, method != http.MethodGet)
		if err != nil {
			return err
		}
		defer release()
	}
	token, err := g.tokens.Token(ctx)
	if err != nil {
		return err
	}
	return send(ctx, g.client, method, g.api, path, "Bearer "+token, in, out)
}

// send makes one request. Its errors name the method and the path, never the
// credential.
func send(ctx context.Context, client *http.Client, method, root, path, authorization string, in, out any) error {
	var body io.Reader
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("scm: encode %s %s: %w", method, path, err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, root+path, body)
	if err != nil {
		return fmt.Errorf("scm: build %s %s: %w", method, path, err)
	}
	request.Header.Set("Authorization", authorization)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", apiVersion)
	request.Header.Set("User-Agent", "costroid")
	if in != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("scm: %s %s: %w", method, path, unwrapURL(err))
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return &StatusError{Method: method, Path: path, Status: response.StatusCode, RetryAfter: retryAfter(response)}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponse)).Decode(out); err != nil {
		return fmt.Errorf("scm: read %s %s: %w", method, path, err)
	}
	return nil
}

func unwrapURL(err error) error {
	var wrapped *url.Error
	if errors.As(err, &wrapped) {
		return wrapped.Err
	}
	return err
}

func retryAfter(response *http.Response) time.Duration {
	seconds, err := strconv.Atoi(response.Header.Get("Retry-After"))
	if err != nil || seconds < 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

type checkOutput struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Text    string `json:"text"`
}

// PostCheck creates the check run and returns its id.
func (g *GitHub) PostCheck(ctx context.Context, check CheckRun) (int64, error) {
	request := struct {
		Name        string      `json:"name"`
		HeadSHA     string      `json:"head_sha"`
		Status      string      `json:"status"`
		Conclusion  string      `json:"conclusion"`
		CompletedAt string      `json:"completed_at"`
		Output      checkOutput `json:"output"`
	}{
		Name:        check.Name,
		HeadSHA:     check.HeadSHA,
		Status:      "completed",
		Conclusion:  check.Conclusion,
		CompletedAt: check.Completed.UTC().Format(time.RFC3339),
		Output:      checkOutput{Title: check.Title, Summary: check.Summary, Text: check.Text},
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := g.do(ctx, http.MethodPost, g.repo("/check-runs"), request, &created); err != nil {
		return 0, err
	}
	return created.ID, nil
}

type issueComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
	User struct {
		Type string `json:"type"`
	} `json:"user"`
}

// UpsertComment edits the sticky comment in place. Only a comment a bot
// account wrote qualifies, so a person who pastes the marker into their own
// comment cannot have it rewritten.
func (g *GitHub) UpsertComment(ctx context.Context, pullRequest int, marker, body string) error {
	id, err := g.findComment(ctx, pullRequest, marker)
	if err != nil {
		return err
	}
	content := struct {
		Body string `json:"body"`
	}{Body: body}
	if id == 0 {
		path := g.repo(fmt.Sprintf("/issues/%d/comments", pullRequest))
		return g.do(ctx, http.MethodPost, path, content, nil)
	}
	return g.do(ctx, http.MethodPatch, g.repo(fmt.Sprintf("/issues/comments/%d", id)), content, nil)
}

func (g *GitHub) findComment(ctx context.Context, pullRequest int, marker string) (int64, error) {
	for page := 1; page <= maxCommentPages; page++ {
		var comments []issueComment
		path := g.repo(fmt.Sprintf("/issues/%d/comments?per_page=%d&page=%d", pullRequest, commentsPerPage, page))
		if err := g.do(ctx, http.MethodGet, path, nil, &comments); err != nil {
			return 0, err
		}
		for _, comment := range comments {
			if comment.User.Type == botAccount && strings.HasPrefix(comment.Body, marker) {
				return comment.ID, nil
			}
		}
		if len(comments) < commentsPerPage {
			return 0, nil
		}
	}
	return 0, nil
}

// ReadFile reads one file through the contents API.
func (g *GitHub) ReadFile(ctx context.Context, path, ref string) ([]byte, error) {
	var file struct {
		Type     string `json:"type"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	endpoint := g.repo("/contents/" + url.PathEscape(path) + "?ref=" + url.QueryEscape(ref))
	if err := g.do(ctx, http.MethodGet, endpoint, nil, &file); err != nil {
		return nil, err
	}
	if file.Type != "file" || file.Encoding != "base64" {
		return nil, fmt.Errorf("scm: %s at %s is not a readable file", path, ref)
	}
	content, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("scm: decode %s: %w", path, err)
	}
	return content, nil
}

// Visibility asks the host, and an answer that omits the field is an error.
func (g *GitHub) Visibility(ctx context.Context) (Visibility, error) {
	var repository struct {
		Private *bool `json:"private"`
	}
	if err := g.do(ctx, http.MethodGet, g.repo(""), nil, &repository); err != nil {
		return Public, err
	}
	if repository.Private == nil {
		return Public, errors.New("scm: the repository answer states no visibility")
	}
	if *repository.Private {
		return Private, nil
	}
	return Public, nil
}

// PullRequest reads the head and the author.
func (g *GitHub) PullRequest(ctx context.Context, number int) (PullRequest, error) {
	var answer struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := g.do(ctx, http.MethodGet, g.repo(fmt.Sprintf("/pulls/%d", number)), nil, &answer); err != nil {
		return PullRequest{}, err
	}
	return PullRequest{Number: number, HeadSHA: answer.Head.SHA, Author: answer.User.Login}, nil
}

// CanWrite reads the collaborator's permission field.
func (g *GitHub) CanWrite(ctx context.Context, login string) (bool, error) {
	var answer struct {
		Permission string `json:"permission"`
	}
	path := g.repo("/collaborators/" + url.PathEscape(login) + "/permission")
	if err := g.do(ctx, http.MethodGet, path, nil, &answer); err != nil {
		return false, err
	}
	return answer.Permission == "admin" || answer.Permission == "write", nil
}
