package workflow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

const (
	repository = "acme/widgets"
	path       = ".github/workflows/costroid.yml"
	base       = "1111111111111111111111111111111111111111"
)

func TestPathReadsTheWorkflowRef(t *testing.T) {
	got, err := Path("acme/widgets/.github/workflows/costroid.yml@refs/pull/7/merge", repository)
	if err != nil || got != path {
		t.Fatalf("%q, %v", got, err)
	}
	for _, ref := range []string{
		"",
		".github/workflows/costroid.yml@refs/heads/main",
		"other/repo/.github/workflows/costroid.yml@refs/heads/main",
		"acme/widgets/.github/workflows/costroid.yml",
		"acme/widgets/.github/workflows/../../secrets.yml@refs/heads/main",
		"acme/widgets/README.md@refs/heads/main",
	} {
		if _, err := Path(ref, repository); err == nil {
			t.Errorf("accepted %q", ref)
		}
	}
}

// history serves count commits that touched the workflow, newest first, the
// oldest dated oldest.
func history(t *testing.T, count int, oldest time.Time) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if r.URL.Path != "/repos/"+repository+"/commits" || query.Get("sha") != base || query.Get("path") != path || r.Header.Get("Authorization") != "Bearer token" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		size, _ := strconv.Atoi(query.Get("per_page"))
		page, _ := strconv.Atoi(query.Get("page"))
		commits := []map[string]any{}
		for index := (page - 1) * size; index < count && index < page*size; index++ {
			date := oldest.Add(time.Duration(count-1-index) * time.Hour)
			commits = append(commits, map[string]any{"commit": map[string]any{"committer": map[string]any{"date": date.Format(time.RFC3339)}}})
		}
		if err := json.NewEncoder(w).Encode(commits); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestStartIsTheOldestCommitOnTheBase(t *testing.T) {
	oldest := time.Date(2026, 11, 1, 9, 30, 0, 0, time.UTC)
	for _, count := range []int{1, 99, 100, 101, 250} {
		server := history(t, count, oldest)
		github := GitHub{API: server.URL, Repository: repository, Token: "token", Client: server.Client()}
		start, err := github.Start(context.Background(), path, base)
		if err != nil || !start.At.Equal(oldest) || start.NotOnBase {
			t.Errorf("%d commits: %+v, %v", count, start, err)
		}
	}
}

func TestAWorkflowNotOnTheBase(t *testing.T) {
	server := history(t, 0, time.Time{})
	github := GitHub{API: server.URL, Repository: repository, Token: "token", Client: server.Client()}
	start, err := github.Start(context.Background(), path, base)
	if err != nil || !start.NotOnBase || !start.At.IsZero() {
		t.Errorf("%+v, %v", start, err)
	}
}

func TestAStartNotReadIsZero(t *testing.T) {
	refused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(refused.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com/", http.StatusFound)
	}))
	t.Cleanup(redirect.Close)
	cases := map[string]GitHub{
		"a refusal":          {API: refused.URL, Repository: repository, Token: "token", Client: refused.Client()},
		"a redirect":         {API: redirect.URL, Repository: repository, Token: "token", Client: redirect.Client()},
		"plain HTTP":         {API: "http://api.example.com", Repository: repository, Token: "token", Client: http.DefaultClient},
		"no repository":      {API: refused.URL, Token: "token", Client: refused.Client()},
		"too many to read":   {API: history(t, maxPages*commitsPerPage, time.Now()).URL, Repository: repository, Token: "token", Client: http.DefaultClient},
		"no client to reach": {API: refused.URL, Repository: repository, Token: "token"},
	}
	for name, github := range cases {
		start, err := github.Start(context.Background(), path, base)
		if err == nil || !start.At.IsZero() || start.NotOnBase {
			t.Errorf("%s: %+v, %v", name, start, err)
		}
	}
}
