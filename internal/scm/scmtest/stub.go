// Package scmtest holds stub GitHub and GitLab APIs for tests. They never reach
// the network.
package scmtest

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Comment is a stored issue comment. Login is its author's account, and an
// App's account ends in [bot].
type Comment struct {
	ID    int64
	Body  string
	Login string
}

// Pull is one pull request of the repository. HeadRepository is owner/name,
// and left empty it answers as a fork GitHub no longer has.
type Pull struct {
	Number         int
	State          string
	HeadSHA        string
	HeadRepository string
	HeadBranch     string
	// Commits are the pull request's commits, oldest first, as GitHub lists
	// them.
	Commits []string
}

// Check is a stored check run. App is the slug of the App that posted it, the
// stub's own App when it was posted through the stub.
type Check struct {
	ID         int64
	Name       string
	HeadSHA    string
	Conclusion string
	Summary    string
	Text       string
	App        string
}

// Stub is one repository on a fake GitHub.
type Stub struct {
	Server *httptest.Server
	// Fail maps "METHOD path-prefix" to a status the stub answers instead.
	Fail map[string]int

	mu       sync.Mutex
	Token    string
	Comments []Comment
	Checks   []Check
	// CheckLists maps a commit to a recorded answer of GitHub's list of its
	// check runs, which the stub serves as it is in place of Checks.
	CheckLists map[string][]byte
	// Files maps a path to a file at every ref, and FilesAt maps a ref, then
	// a path, to the file at that ref alone. A ref FilesAt names reads only
	// FilesAt.
	Files   map[string]string
	FilesAt map[string]map[string]string
	// Branches maps a branch to the commit at its head.
	Branches map[string]string
	// Pulls are the repository's pull requests. The list of a commit's pull
	// requests answers empty, as GitHub's does for a fork's.
	Pulls []Pull
	// App is the slug of the App the token belongs to. A check run's answer
	// names it, and the stub posts comments as its account.
	App string
	// Commits maps a path to the dates of the commits that touched it,
	// newest first, as the commits API lists them.
	Commits    map[string][]time.Time
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
	stub := &Stub{
		Token: "stub-token", Fail: map[string]int{}, Files: map[string]string{}, FilesAt: map[string]map[string]string{},
		Branches: map[string]string{}, Commits: map[string][]time.Time{}, CheckLists: map[string][]byte{},
		App: "github-actions", nextID: 1000,
	}
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
	case r.Method == http.MethodGet && strings.Contains(path, "/commits/") && strings.HasSuffix(path, "/pulls"):
		s.answer(w, []any{})
	case r.Method == http.MethodGet && strings.Contains(path, "/commits/") && strings.HasSuffix(path, "/check-runs"):
		s.listChecks(w, r)
	case r.Method == http.MethodGet && strings.Contains(path, "/pulls/") && strings.HasSuffix(path, "/commits"):
		s.listPullCommits(w, r)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/pulls"):
		s.listPulls(w, r)
	case r.Method == http.MethodGet && strings.Contains(path, "/commits/") && strings.HasSuffix(path, "/branches-where-head"):
		s.listBranchesAt(w, path)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/check-runs"):
		s.postCheck(w, body)
	case r.Method == http.MethodPatch && strings.Contains(path, "/check-runs/"):
		s.patchCheck(w, path, body)
	case strings.Contains(path, "/issues/comments/") && r.Method == http.MethodPatch:
		s.patchComment(w, path, body)
	case strings.HasSuffix(path, "/comments") && r.Method == http.MethodGet:
		s.listComments(w, r)
	case strings.HasSuffix(path, "/comments") && r.Method == http.MethodPost:
		s.postComment(w, body)
	case strings.HasSuffix(path, "/commits") && r.Method == http.MethodGet:
		s.listCommits(w, r)
	case strings.Contains(path, "/contents/"):
		s.readFile(w, path, r.URL.Query().Get("ref"))
	case strings.Contains(path, "/git/ref/heads/"):
		s.readBranch(w, path[strings.Index(path, "/git/ref/heads/")+len("/git/ref/heads/"):])
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
	check := Check{s.next(), request.Name, request.HeadSHA, request.Conclusion, request.Output.Summary, request.Output.Text, s.App}
	s.Checks = append(s.Checks, check)
	w.WriteHeader(http.StatusCreated)
	s.answer(w, map[string]any{"id": check.ID, "app": map[string]string{"slug": s.App}})
}

// commitOf reads the commit out of /repos/o/r/commits/{sha}/....
func commitOf(path string) string {
	rest := path[strings.Index(path, "/commits/")+len("/commits/"):]
	sha, _, _ := strings.Cut(rest, "/")
	return sha
}

// listPulls answers as GitHub's list of pull requests does, filtered by state
// and by a head of the form owner:branch.
func (s *Stub) listPulls(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	size, err := strconv.Atoi(query.Get("per_page"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	page, err := strconv.Atoi(query.Get("page"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	state := cmp.Or(query.Get("state"), "open")
	owner, branch, filtered := strings.Cut(query.Get("head"), ":")
	var matched []Pull
	for _, pull := range s.Pulls {
		holder, _, _ := strings.Cut(pull.HeadRepository, "/")
		if pull.State != state || filtered && (holder != owner || pull.HeadBranch != branch) {
			continue
		}
		matched = append(matched, pull)
	}
	listed := []map[string]any{}
	for index := (page - 1) * size; index >= 0 && index < len(matched) && index < page*size; index++ {
		pull := matched[index]
		head := map[string]any{"sha": pull.HeadSHA, "ref": pull.HeadBranch, "repo": nil}
		if pull.HeadRepository != "" {
			head["repo"] = map[string]string{"full_name": pull.HeadRepository}
		}
		listed = append(listed, map[string]any{"number": pull.Number, "state": pull.State, "head": head})
	}
	s.answer(w, listed)
}

// listChecks answers as GitHub's list of a commit's check runs does, filtered
// by name, from a recorded answer when the commit has one.
func (s *Stub) listChecks(w http.ResponseWriter, r *http.Request) {
	sha := commitOf(r.URL.Path)
	if recorded, found := s.CheckLists[sha]; found {
		if _, err := w.Write(recorded); err != nil {
			panic(fmt.Sprintf("scmtest: %v", err))
		}
		return
	}
	size, page, ok := paging(r)
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var matched []Check
	for _, check := range s.Checks {
		if check.HeadSHA == sha && check.Name == r.URL.Query().Get("check_name") {
			matched = append(matched, check)
		}
	}
	listed := []map[string]any{}
	for index := (page - 1) * size; index >= 0 && index < len(matched) && index < page*size; index++ {
		check := matched[index]
		listed = append(listed, map[string]any{
			"id": check.ID, "name": check.Name, "head_sha": check.HeadSHA, "status": "completed", "conclusion": check.Conclusion,
			"output": map[string]string{"summary": check.Summary, "text": check.Text}, "app": map[string]string{"slug": check.App},
		})
	}
	s.answer(w, map[string]any{"total_count": len(matched), "check_runs": listed})
}

// listPullCommits answers as GitHub's list of a pull request's commits does,
// which stops at 250.
func (s *Stub) listPullCommits(w http.ResponseWriter, r *http.Request) {
	size, page, ok := paging(r)
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	segments := strings.Split(r.URL.Path, "/")
	number, err := strconv.Atoi(segments[len(segments)-2])
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	index := slices.IndexFunc(s.Pulls, func(pull Pull) bool { return pull.Number == number })
	if index < 0 {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	commits := s.Pulls[index].Commits[:min(len(s.Pulls[index].Commits), maxPullCommits)]
	listed := []map[string]string{}
	for at := (page - 1) * size; at >= 0 && at < len(commits) && at < page*size; at++ {
		listed = append(listed, map[string]string{"sha": commits[at]})
	}
	s.answer(w, listed)
}

// maxPullCommits is the most commits GitHub lists for a pull request.
const maxPullCommits = 250

// paging reads per_page and page.
func paging(r *http.Request) (int, int, bool) {
	size, err := strconv.Atoi(r.URL.Query().Get("per_page"))
	if err != nil {
		return 0, 0, false
	}
	page, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil {
		return 0, 0, false
	}
	return size, page, true
}

// listBranchesAt answers with every branch whose head is the commit.
func (s *Stub) listBranchesAt(w http.ResponseWriter, path string) {
	sha := commitOf(path)
	listed := []map[string]any{}
	for _, branch := range slices.Sorted(maps.Keys(s.Branches)) {
		if s.Branches[branch] == sha {
			listed = append(listed, map[string]any{"name": branch, "commit": map[string]string{"sha": sha}})
		}
	}
	s.answer(w, listed)
}

// patchCheck replaces a stored check's output, as GitHub does. The posted
// conclusion stays.
func (s *Stub) patchCheck(w http.ResponseWriter, path string, body []byte) {
	id, err := strconv.ParseInt(path[strings.LastIndex(path, "/")+1:], 10, 64)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var request struct {
		Output *struct {
			Summary string `json:"summary"`
			Text    string `json:"text"`
		} `json:"output"`
	}
	if err := json.Unmarshal(body, &request); err != nil || request.Output == nil {
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}
	for index := range s.Checks {
		if s.Checks[index].ID == id {
			s.Checks[index].Summary, s.Checks[index].Text = request.Output.Summary, request.Output.Text
			s.answer(w, map[string]int64{"id": id})
			return
		}
	}
	w.WriteHeader(http.StatusNotFound)
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
	s.Comments = append(s.Comments, Comment{ID: s.next(), Body: commentBody(body), Login: s.App + "[bot]"})
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
		Login string `json:"login"`
		Type  string `json:"type"`
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
		if strings.HasSuffix(stored.Login, "[bot]") {
			kind = "Bot"
		}
		listed = append(listed, comment{stored.ID, stored.Body, account{stored.Login, kind}})
	}
	s.answer(w, listed)
}

func (s *Stub) listCommits(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	size, err := strconv.Atoi(query.Get("per_page"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	page, err := strconv.Atoi(query.Get("page"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	dates := s.Commits[query.Get("path")]
	listed := []map[string]any{}
	for index := (page - 1) * size; index >= 0 && index < len(dates) && index < page*size; index++ {
		listed = append(listed, map[string]any{"commit": map[string]any{"committer": map[string]string{"date": dates[index].Format(time.RFC3339)}}})
	}
	s.answer(w, listed)
}

func (s *Stub) readFile(w http.ResponseWriter, path, ref string) {
	name := path[strings.LastIndex(path, "/")+1:]
	files, pinned := s.FilesAt[ref]
	if !pinned {
		files = s.Files
	}
	content, found := files[name]
	if !found {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	s.answer(w, map[string]string{"type": "file", "encoding": "base64", "content": encoded + "\n"})
}

// readBranch answers as the git refs API does for one branch.
func (s *Stub) readBranch(w http.ResponseWriter, branch string) {
	head, found := s.Branches[branch]
	if !found {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	s.answer(w, map[string]any{"ref": "refs/heads/" + branch, "object": map[string]string{"sha": head, "type": "commit"}})
}

func (s *Stub) readRepository(w http.ResponseWriter) {
	if s.Private == nil {
		s.answer(w, map[string]string{})
		return
	}
	s.answer(w, map[string]bool{"private": *s.Private})
}
