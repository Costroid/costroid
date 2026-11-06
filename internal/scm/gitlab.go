package scm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var gitLabProjectPattern = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

// GitLabOptions configures a GitLab adapter.
type GitLabOptions struct {
	// API is the instance's REST root, CI_API_V4_URL in a job. It has no
	// default, so a self-managed instance's token never reaches gitlab.com.
	API string
	// Project is the project's numeric id.
	Project string
	Tokens  TokenSource
	// Client is as in GitHubOptions.
	Client *http.Client
}

// GitLab is the Reporter for one project. It reaches GitLab's REST API v4, and
// it has run against a stub built from GitLab's documentation, never a real
// instance.
type GitLab struct {
	api     string
	project string
	tokens  TokenSource
	client  *http.Client
}

// NewGitLab returns the adapter. The token never travels over plain HTTP,
// except to a loopback address.
func NewGitLab(options GitLabOptions) (*GitLab, error) {
	if !gitLabProjectPattern.MatchString(options.Project) {
		return nil, errors.New("scm: project is not a numeric id")
	}
	if err := checkRoot(options.API); err != nil {
		return nil, err
	}
	if options.Client == nil {
		return nil, errNoClient
	}
	return &GitLab{
		api:     strings.TrimRight(options.API, "/"),
		project: options.Project,
		tokens:  options.Tokens,
		client:  bounded(options.Client),
	}, nil
}

func (g *GitLab) projectPath(suffix string) string {
	return "/projects/" + g.project + suffix
}

func (g *GitLab) do(ctx context.Context, method, path string, in, out any) error {
	token, err := g.tokens.Token(ctx)
	if err != nil {
		return err
	}
	headers := map[string]string{"PRIVATE-TOKEN": token, "Accept": "application/json"}
	return exchange(ctx, g.client, method, g.api, path, headers, in, out)
}

// PostCheck posts nothing. A commit status needs the Developer role and a note
// needs less, so the note's token cannot set one, and a status has no neutral
// state to state.
func (g *GitLab) PostCheck(context.Context, CheckRun) (int64, error) {
	return 0, ErrNoCheck
}

// UpdateCheck sends nothing, because PostCheck creates no check.
func (g *GitLab) UpdateCheck(context.Context, int64, CheckRun) error {
	return ErrNoCheck
}

type gitLabNote struct {
	ID     int64  `json:"id"`
	Body   string `json:"body"`
	System bool   `json:"system"`
	Author struct {
		ID int64 `json:"id"`
	} `json:"author"`
}

// UpsertComment edits the sticky note in place. Only a note this token's own
// user wrote qualifies, because a note's author carries no bot field. A failed
// search posts nothing, so a merge request never holds a second note.
func (g *GitLab) UpsertComment(ctx context.Context, mergeRequest int, marker, body string) error {
	author, err := g.currentUser(ctx)
	if err != nil {
		return err
	}
	id, err := g.findNote(ctx, mergeRequest, author, marker)
	if err != nil {
		return err
	}
	content := struct {
		Body string `json:"body"`
	}{Body: body}
	notes := g.projectPath(fmt.Sprintf("/merge_requests/%d/notes", mergeRequest))
	if id == 0 {
		return g.do(ctx, http.MethodPost, notes, content, nil)
	}
	return g.do(ctx, http.MethodPut, fmt.Sprintf("%s/%d", notes, id), content, nil)
}

func (g *GitLab) currentUser(ctx context.Context) (int64, error) {
	var user struct {
		ID int64 `json:"id"`
	}
	if err := g.do(ctx, http.MethodGet, "/user", nil, &user); err != nil {
		return 0, err
	}
	if user.ID <= 0 {
		return 0, errors.New("scm: the user answer states no id")
	}
	return user.ID, nil
}

// findNote reads the oldest notes first, because the sticky note is early and
// system notes make a merge request's list long.
func (g *GitLab) findNote(ctx context.Context, mergeRequest int, author int64, marker string) (int64, error) {
	for page := 1; page <= maxCommentPages; page++ {
		var notes []gitLabNote
		path := g.projectPath(fmt.Sprintf("/merge_requests/%d/notes?sort=asc&order_by=created_at&per_page=%d&page=%d", mergeRequest, commentsPerPage, page))
		if err := g.do(ctx, http.MethodGet, path, nil, &notes); err != nil {
			return 0, err
		}
		for _, note := range notes {
			if !note.System && note.Author.ID == author && strings.HasPrefix(note.Body, marker) {
				return note.ID, nil
			}
		}
		if len(notes) < commentsPerPage {
			return 0, nil
		}
	}
	return 0, fmt.Errorf("scm: merge request %d holds more than %d notes", mergeRequest, maxCommentPages*commentsPerPage)
}

// ReadFile reads one file through the repository files API.
func (g *GitLab) ReadFile(ctx context.Context, path, ref string) ([]byte, error) {
	var file struct {
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	endpoint := g.projectPath("/repository/files/" + url.PathEscape(path) + "?ref=" + url.QueryEscape(ref))
	if err := g.do(ctx, http.MethodGet, endpoint, nil, &file); err != nil {
		return nil, err
	}
	if file.Encoding != "base64" {
		return nil, fmt.Errorf("scm: %s at %s is not a readable file", path, ref)
	}
	return decodeFile(path, file.Content)
}

// Visibility asks the host. An answer outside GitLab's three values is an
// error, and internal counts as private.
func (g *GitLab) Visibility(ctx context.Context) (Visibility, error) {
	var project struct {
		Visibility string `json:"visibility"`
	}
	if err := g.do(ctx, http.MethodGet, g.projectPath(""), nil, &project); err != nil {
		return Public, err
	}
	switch project.Visibility {
	case "public":
		return Public, nil
	case "private", "internal":
		return Private, nil
	}
	return Public, errors.New("scm: the project answer states no visibility")
}
