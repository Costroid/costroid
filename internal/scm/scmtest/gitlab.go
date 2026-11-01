package scmtest

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const (
	gitLabRoot    = "/api/v4"
	gitLabProject = "4242"
	gitLabMaxPage = 100
	// GitLabRequest is the merge request's IID on the stub.
	GitLabRequest = 7
)

// Note is a stored merge request note.
type Note struct {
	ID       int64
	Body     string
	AuthorID int64
	System   bool
}

// GitLab is one project on a fake GitLab REST API v4. Its routes and answers
// follow docs.gitlab.com as read on November 1, 2026, and no real instance
// checked them.
type GitLab struct {
	Server *httptest.Server
	// Fail maps "METHOD path-part" to a status the stub answers instead.
	Fail map[string]int

	mu       sync.Mutex
	Token    string
	Project  string
	UserID   int64
	Notes    []Note
	Requests []string
	// Files maps a ref, then a path, to the file at that commit.
	Files map[string]map[string]string
	// Visibility is the project's field. Empty leaves it out of the answer.
	Visibility string
	nextID     int64
}

// NewGitLab starts a stub and stops it with the test.
func NewGitLab(t *testing.T) *GitLab {
	t.Helper()
	stub := &GitLab{
		Token:   "stub-gitlab-token",
		Project: gitLabProject,
		UserID:  900,
		Fail:    map[string]int{},
		Files:   map[string]map[string]string{},
		nextID:  3000,
	}
	stub.Server = httptest.NewServer(stub)
	t.Cleanup(stub.Server.Close)
	return stub
}

// API is the value CI_API_V4_URL holds.
func (s *GitLab) API() string {
	return s.Server.URL + gitLabRoot
}

// Writes returns the requests that were not GETs.
func (s *GitLab) Writes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.DeleteFunc(slices.Clone(s.Requests), func(line string) bool {
		return strings.HasPrefix(line, http.MethodGet)
	})
}

func (s *GitLab) failure(r *http.Request) int {
	for rule, status := range s.Fail {
		method, part, _ := strings.Cut(rule, " ")
		if r.Method == method && strings.Contains(r.URL.Path, part) {
			return status
		}
	}
	return 0
}

// ServeHTTP answers the routes the adapter calls.
func (s *GitLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Requests = append(s.Requests, r.Method+" "+r.URL.RequestURI())
	if r.Header.Get("PRIVATE-TOKEN") != s.Token {
		s.message(w, http.StatusUnauthorized, "401 Unauthorized")
		return
	}
	if status := s.failure(r); status != 0 {
		w.WriteHeader(status)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	path, routed := strings.CutPrefix(r.URL.Path, gitLabRoot)
	if !routed {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if path == "/user" && r.Method == http.MethodGet {
		s.answer(w, http.StatusOK, map[string]any{"id": s.UserID, "username": "project_4242_bot_a1b2c3", "bot": true})
		return
	}
	project, routed := strings.CutPrefix(path, "/projects/"+s.Project)
	if !routed {
		s.message(w, http.StatusNotFound, "404 Project Not Found")
		return
	}
	s.route(w, r, project, body)
}

func (s *GitLab) route(w http.ResponseWriter, r *http.Request, path string, body []byte) {
	notes := fmt.Sprintf("/merge_requests/%d/notes", GitLabRequest)
	switch {
	case path == "" && r.Method == http.MethodGet:
		s.readProject(w)
	case path == notes && r.Method == http.MethodGet:
		s.listNotes(w, r)
	case path == notes && r.Method == http.MethodPost:
		s.createNote(w, body)
	case strings.HasPrefix(path, notes+"/") && r.Method == http.MethodPut:
		s.updateNote(w, path, body)
	case strings.HasPrefix(path, "/repository/files/") && r.Method == http.MethodGet:
		s.readFile(w, r, strings.TrimPrefix(path, "/repository/files/"))
	default:
		s.message(w, http.StatusNotFound, "404 Not Found")
	}
}

func (s *GitLab) answer(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		panic(fmt.Sprintf("scmtest: %v", err))
	}
}

func (s *GitLab) message(w http.ResponseWriter, status int, text string) {
	s.answer(w, status, map[string]string{"message": text})
}

func (s *GitLab) noteAnswer(note Note) map[string]any {
	return map[string]any{
		"id":   note.ID,
		"body": note.Body,
		"author": map[string]any{
			"id":       note.AuthorID,
			"username": fmt.Sprintf("user_%d", note.AuthorID),
			"state":    "active",
		},
		"system":        note.System,
		"noteable_type": "MergeRequest",
		"noteable_iid":  GitLabRequest,
		"internal":      false,
	}
}

// listNotes pages as GitLab does: at most 100 at a time, newest first unless
// the request asks for sort=asc.
func (s *GitLab) listNotes(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	size, err := strconv.Atoi(query.Get("per_page"))
	if err != nil {
		size = 20
	}
	page, err := strconv.Atoi(query.Get("page"))
	if err != nil {
		page = 1
	}
	size = min(size, gitLabMaxPage)
	ordered := slices.Clone(s.Notes)
	if query.Get("sort") != "asc" {
		slices.Reverse(ordered)
	}
	listed := []map[string]any{}
	start := (page - 1) * size
	for index := start; index >= 0 && index < len(ordered) && index < start+size; index++ {
		listed = append(listed, s.noteAnswer(ordered[index]))
	}
	s.answer(w, http.StatusOK, listed)
}

func noteBody(body []byte) string {
	var request struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return ""
	}
	return request.Body
}

func (s *GitLab) createNote(w http.ResponseWriter, body []byte) {
	s.nextID++
	note := Note{ID: s.nextID, Body: noteBody(body), AuthorID: s.UserID}
	s.Notes = append(s.Notes, note)
	s.answer(w, http.StatusCreated, s.noteAnswer(note))
}

// updateNote refuses another user's note, as it does for a token below the
// Maintainer role.
func (s *GitLab) updateNote(w http.ResponseWriter, path string, body []byte) {
	id, err := strconv.ParseInt(path[strings.LastIndex(path, "/")+1:], 10, 64)
	if err != nil {
		s.message(w, http.StatusNotFound, "404 Not found")
		return
	}
	for index := range s.Notes {
		if s.Notes[index].ID != id {
			continue
		}
		if s.Notes[index].AuthorID != s.UserID {
			s.message(w, http.StatusForbidden, "403 Forbidden")
			return
		}
		s.Notes[index].Body = noteBody(body)
		s.answer(w, http.StatusOK, s.noteAnswer(s.Notes[index]))
		return
	}
	s.message(w, http.StatusNotFound, "404 Not found")
}

func (s *GitLab) readFile(w http.ResponseWriter, r *http.Request, path string) {
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		s.message(w, http.StatusBadRequest, "ref is missing")
		return
	}
	content, found := s.Files[ref][path]
	if !found {
		s.message(w, http.StatusNotFound, "404 File Not Found")
		return
	}
	s.answer(w, http.StatusOK, map[string]any{
		"file_name": path,
		"file_path": path,
		"size":      len(content),
		"encoding":  "base64",
		"content":   base64.StdEncoding.EncodeToString([]byte(content)),
		"ref":       ref,
	})
}

func (s *GitLab) readProject(w http.ResponseWriter) {
	project := map[string]any{"id": s.Project, "default_branch": "main"}
	if s.Visibility != "" {
		project["visibility"] = s.Visibility
	}
	s.answer(w, http.StatusOK, project)
}
