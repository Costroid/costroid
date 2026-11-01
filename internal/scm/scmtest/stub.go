// Package scmtest holds stub GitHub and GitLab APIs for tests. They never reach
// the network.
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

// Comment is a stored issue comment.
type Comment struct {
	ID   int64
	Body string
	Bot  bool
}

// Check is a stored check run.
type Check struct {
	ID         int64
	Name       string
	HeadSHA    string
	Conclusion string
	Summary    string
	Text       string
}

// Stub is one repository on a fake GitHub.
type Stub struct {
	Server *httptest.Server
	// Fail maps "METHOD path-prefix" to a status the stub answers instead.
	Fail map[string]int

	mu         sync.Mutex
	Token      string
	Comments   []Comment
	Checks     []Check
	Files      map[string]string
	Private    *bool
	Requests   []string
	Head       string
	Author     string
	Permission string
	nextID     int64
}

// New starts a stub and stops it with the test.
func New(t *testing.T) *Stub {
	t.Helper()
	stub := &Stub{Token: "stub-token", Fail: map[string]int{}, Files: map[string]string{}, nextID: 1000}
	stub.Server = httptest.NewServer(stub)
	t.Cleanup(stub.Server.Close)
	return stub
}

// SetPrivate states the repository's visibility.
func (s *Stub) SetPrivate(private bool) {
	s.Private = &private
}

// Writes returns the requests that were not GETs.
func (s *Stub) Writes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.DeleteFunc(slices.Clone(s.Requests), func(line string) bool {
		return strings.HasPrefix(line, http.MethodGet)
	})
}

func (s *Stub) failure(r *http.Request) int {
	for rule, status := range s.Fail {
		method, prefix, _ := strings.Cut(rule, " ")
		if r.Method == method && strings.Contains(r.URL.Path, prefix) {
			return status
		}
	}
	return 0
}

func (s *Stub) next() int64 {
	s.nextID++
	return s.nextID
}

// ServeHTTP answers the routes the adapter calls.
func (s *Stub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Requests = append(s.Requests, r.Method+" "+r.URL.RequestURI())
	if r.Header.Get("Authorization") != "Bearer "+s.Token {
		w.WriteHeader(http.StatusUnauthorized)
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
	path := r.URL.Path
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/check-runs"):
		s.postCheck(w, body)
	case strings.Contains(path, "/issues/comments/") && r.Method == http.MethodPatch:
		s.patchComment(w, path, body)
	case strings.HasSuffix(path, "/comments") && r.Method == http.MethodGet:
		s.listComments(w, r)
	case strings.HasSuffix(path, "/comments") && r.Method == http.MethodPost:
		s.postComment(w, body)
	case strings.Contains(path, "/contents/"):
		s.readFile(w, path)
	case strings.Contains(path, "/pulls/"):
		s.answer(w, map[string]any{"head": map[string]string{"sha": s.Head}, "user": map[string]string{"login": s.Author}})
	case strings.Contains(path, "/collaborators/"):
		s.answer(w, map[string]string{"permission": s.Permission})
	case strings.HasPrefix(path, "/repos/"):
		s.readRepository(w)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *Stub) answer(w http.ResponseWriter, value any) {
	if err := json.NewEncoder(w).Encode(value); err != nil {
		panic(fmt.Sprintf("scmtest: %v", err))
	}
}

func (s *Stub) postCheck(w http.ResponseWriter, body []byte) {
	var request struct {
		Name       string `json:"name"`
		HeadSHA    string `json:"head_sha"`
		Conclusion string `json:"conclusion"`
		Output     struct {
			Summary string `json:"summary"`
			Text    string `json:"text"`
		} `json:"output"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}
	check := Check{s.next(), request.Name, request.HeadSHA, request.Conclusion, request.Output.Summary, request.Output.Text}
	s.Checks = append(s.Checks, check)
	w.WriteHeader(http.StatusCreated)
	s.answer(w, map[string]int64{"id": check.ID})
}

func commentBody(body []byte) string {
	var request struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return ""
	}
	return request.Body
}

func (s *Stub) postComment(w http.ResponseWriter, body []byte) {
	s.Comments = append(s.Comments, Comment{ID: s.next(), Body: commentBody(body), Bot: true})
	w.WriteHeader(http.StatusCreated)
	s.answer(w, map[string]string{})
}

func (s *Stub) patchComment(w http.ResponseWriter, path string, body []byte) {
	id, err := strconv.ParseInt(path[strings.LastIndex(path, "/")+1:], 10, 64)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	for index := range s.Comments {
		if s.Comments[index].ID == id {
			s.Comments[index].Body = commentBody(body)
			s.answer(w, map[string]string{})
			return
		}
	}
	w.WriteHeader(http.StatusNotFound)
}

func (s *Stub) listComments(w http.ResponseWriter, r *http.Request) {
	size, err := strconv.Atoi(r.URL.Query().Get("per_page"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	page, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	type account struct {
		Type string `json:"type"`
	}
	type comment struct {
		ID   int64   `json:"id"`
		Body string  `json:"body"`
		User account `json:"user"`
	}
	listed := []comment{}
	start := (page - 1) * size
	for index := start; index >= 0 && index < len(s.Comments) && index < start+size; index++ {
		stored := s.Comments[index]
		kind := "User"
		if stored.Bot {
			kind = "Bot"
		}
		listed = append(listed, comment{stored.ID, stored.Body, account{kind}})
	}
	s.answer(w, listed)
}

func (s *Stub) readFile(w http.ResponseWriter, path string) {
	name := path[strings.LastIndex(path, "/")+1:]
	content, found := s.Files[name]
	if !found {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	s.answer(w, map[string]string{"type": "file", "encoding": "base64", "content": encoded + "\n"})
}

func (s *Stub) readRepository(w http.ResponseWriter) {
	if s.Private == nil {
		s.answer(w, map[string]string{})
		return
	}
	s.answer(w, map[string]bool{"private": *s.Private})
}
