package scm_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/scm"
	"github.com/Costroid/costroid/internal/scm/scmtest"
)

const mergeRequest = scmtest.GitLabRequest

func gitLabAdapter(t *testing.T, stub *scmtest.GitLab) *scm.GitLab {
	t.Helper()
	host, err := scm.NewGitLab(scm.GitLabOptions{API: stub.API(), Project: stub.Project, Client: &http.Client{}, Tokens: scm.StaticToken(stub.Token)})
	if err != nil {
		t.Fatal(err)
	}
	return host
}

func TestStickyNoteIsCreatedOnceAndEditedInPlace(t *testing.T) {
	stub := scmtest.NewGitLab(t)
	host := gitLabAdapter(t, stub)
	for _, body := range []string{marker + " first", marker + " second", marker + " third"} {
		if err := host.UpsertComment(context.Background(), mergeRequest, marker, body); err != nil {
			t.Fatal(err)
		}
	}
	if len(stub.Notes) != 1 || stub.Notes[0].Body != marker+" third" {
		t.Errorf("notes = %+v", stub.Notes)
	}
	writes := stub.Writes()
	if len(writes) != 3 || !strings.HasPrefix(writes[0], "POST") || !strings.HasPrefix(writes[2], "PUT") {
		t.Errorf("writes = %v", writes)
	}
}

func TestStickyNoteIsFoundPastTheFirstPage(t *testing.T) {
	stub := scmtest.NewGitLab(t)
	for index := range 250 {
		stub.Notes = append(stub.Notes, scmtest.Note{ID: int64(index + 1), Body: "chatter", AuthorID: 5})
	}
	stub.Notes = append(stub.Notes, scmtest.Note{ID: 900, Body: marker + " old", AuthorID: stub.UserID})
	if err := gitLabAdapter(t, stub).UpsertComment(context.Background(), mergeRequest, marker, marker+" new"); err != nil {
		t.Fatal(err)
	}
	if len(stub.Notes) != 251 || stub.Notes[250].Body != marker+" new" {
		t.Errorf("%d notes, last %q", len(stub.Notes), stub.Notes[len(stub.Notes)-1].Body)
	}
}

func TestStickyNoteSearchReadsTheOldestNotesFirst(t *testing.T) {
	stub := scmtest.NewGitLab(t)
	if err := gitLabAdapter(t, stub).UpsertComment(context.Background(), mergeRequest, marker, marker); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stub.Requests[1], "sort=asc") {
		t.Errorf("request = %s", stub.Requests[1])
	}
}

func TestANoteAnotherUserWroteIsLeftAlone(t *testing.T) {
	stub := scmtest.NewGitLab(t)
	stub.Notes = []scmtest.Note{{ID: 1, Body: marker + " planted by a person", AuthorID: 5}}
	if err := gitLabAdapter(t, stub).UpsertComment(context.Background(), mergeRequest, marker, marker+" new"); err != nil {
		t.Fatal(err)
	}
	if len(stub.Notes) != 2 || stub.Notes[0].Body != marker+" planted by a person" {
		t.Errorf("notes = %+v", stub.Notes)
	}
}

func TestASystemNoteIsNeverTheStickyNote(t *testing.T) {
	stub := scmtest.NewGitLab(t)
	stub.Notes = []scmtest.Note{{ID: 1, Body: marker + " system", AuthorID: stub.UserID, System: true}}
	if err := gitLabAdapter(t, stub).UpsertComment(context.Background(), mergeRequest, marker, marker+" new"); err != nil {
		t.Fatal(err)
	}
	if len(stub.Notes) != 2 || stub.Notes[0].Body != marker+" system" {
		t.Errorf("notes = %+v", stub.Notes)
	}
}

func TestASearchThatCannotFinishPostsNothing(t *testing.T) {
	stub := scmtest.NewGitLab(t)
	for index := range 3000 {
		stub.Notes = append(stub.Notes, scmtest.Note{ID: int64(index + 1), Body: "chatter", AuthorID: 5})
	}
	err := gitLabAdapter(t, stub).UpsertComment(context.Background(), mergeRequest, marker, marker+" new")
	if err == nil || len(stub.Writes()) != 0 {
		t.Errorf("err = %v, writes = %v", err, stub.Writes())
	}
}

func TestAnUnknownUserOrAFailedSearchPostsNothing(t *testing.T) {
	for name, failure := range map[string]string{"user": "GET /user", "search": "GET /notes"} {
		t.Run(name, func(t *testing.T) {
			stub := scmtest.NewGitLab(t)
			stub.Fail[failure] = http.StatusBadGateway
			err := gitLabAdapter(t, stub).UpsertComment(context.Background(), mergeRequest, marker, marker+" new")
			if err == nil || len(stub.Writes()) != 0 {
				t.Errorf("err = %v, writes = %v", err, stub.Writes())
			}
		})
	}
}

func TestReadFileReadsTheNamedCommitAndNamesAMissingFile(t *testing.T) {
	stub := scmtest.NewGitLab(t)
	stub.Files[sampleSHA] = map[string]string{"costroid.yml": "comment:\n  show_cost: false\n"}
	stub.Files[movedSHA] = map[string]string{"costroid.yml": "comment:\n  show_cost: true\n"}
	host := gitLabAdapter(t, stub)
	data, err := host.ReadFile(context.Background(), "costroid.yml", sampleSHA)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != stub.Files[sampleSHA]["costroid.yml"] {
		t.Errorf("data = %q", data)
	}
	if _, err := host.ReadFile(context.Background(), "absent.yml", sampleSHA); !errors.Is(err, scm.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestReadFileEncodesASlashInThePath(t *testing.T) {
	stub := scmtest.NewGitLab(t)
	stub.Files[sampleSHA] = map[string]string{"app/models/key.rb": "key"}
	data, err := gitLabAdapter(t, stub).ReadFile(context.Background(), "app/models/key.rb", sampleSHA)
	if err != nil || string(data) != "key" {
		t.Fatalf("data = %q, err = %v", data, err)
	}
	if !strings.Contains(stub.Requests[0], "/repository/files/app%2Fmodels%2Fkey.rb?ref=") {
		t.Errorf("request = %s", stub.Requests[0])
	}
}

func TestGitLabVisibilityReadsThreeValuesAndNothingElse(t *testing.T) {
	cases := map[string]struct {
		want scm.Visibility
		bad  bool
	}{
		"public":   {want: scm.Public},
		"private":  {want: scm.Private},
		"internal": {want: scm.Private},
		"":         {want: scm.Public, bad: true},
		"secret":   {want: scm.Public, bad: true},
	}
	for field, want := range cases {
		t.Run(field, func(t *testing.T) {
			stub := scmtest.NewGitLab(t)
			stub.Visibility = field
			got, err := gitLabAdapter(t, stub).Visibility(context.Background())
			if got != want.want || (err != nil) != want.bad {
				t.Errorf("visibility %v, err %v", got, err)
			}
		})
	}
}

func TestPostCheckSendsNothing(t *testing.T) {
	stub := scmtest.NewGitLab(t)
	host := gitLabAdapter(t, stub)
	_, err := host.PostCheck(context.Background(), scm.CheckRun{HeadSHA: sampleSHA, Completed: time.Now()})
	if !errors.Is(err, scm.ErrNoCheck) || len(stub.Requests) != 0 {
		t.Errorf("err = %v, requests = %v", err, stub.Requests)
	}
	if err := host.UpdateCheck(context.Background(), 1, scm.CheckRun{Text: "t"}); !errors.Is(err, scm.ErrNoCheck) || len(stub.Requests) != 0 {
		t.Errorf("update: err = %v, requests = %v", err, stub.Requests)
	}
}

func TestGitLabRequestsCarryTheTokenInItsOwnHeaderAndNoGitHubOnes(t *testing.T) {
	var seen http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		fmt.Fprint(w, `{"visibility":"private"}`)
	}))
	defer server.Close()
	host, err := scm.NewGitLab(scm.GitLabOptions{API: server.URL + "/api/v4/", Project: "4242", Client: &http.Client{}, Tokens: scm.StaticToken("the-token")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.Visibility(context.Background()); err != nil {
		t.Fatal(err)
	}
	if seen.Get("PRIVATE-TOKEN") != "the-token" || seen.Get("Authorization") != "" || seen.Get("X-GitHub-Api-Version") != "" {
		t.Errorf("headers = %v", seen)
	}
}

func TestGitLabErrorsNameTheStatusAndNeverTheToken(t *testing.T) {
	stub := scmtest.NewGitLab(t)
	stub.Fail["POST /notes"] = http.StatusForbidden
	err := gitLabAdapter(t, stub).UpsertComment(context.Background(), mergeRequest, marker, marker)
	var refused *scm.StatusError
	if !errors.As(err, &refused) || refused.Status != http.StatusForbidden {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), stub.Token) {
		t.Errorf("the token is in %q", err)
	}
}

func TestAGitLabRedirectIsNotFollowed(t *testing.T) {
	followed := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	host, err := scm.NewGitLab(scm.GitLabOptions{API: server.URL, Project: "4242", Client: &http.Client{}, Tokens: scm.StaticToken("t")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.Visibility(context.Background()); err == nil {
		t.Error("a redirect answered")
	}
	if followed {
		t.Error("the token followed a redirect")
	}
}

func TestTheGitLabTokenNeverTravelsOverPlainHTTPToAnotherHost(t *testing.T) {
	for api, allowed := range map[string]bool{
		"http://gitlab.example.com/api/v4":  false,
		"":                                  false,
		"https://gitlab.example.com/api/v4": true,
		"http://127.0.0.1:9/api/v4":         true,
	} {
		_, err := scm.NewGitLab(scm.GitLabOptions{API: api, Project: "4242", Client: &http.Client{}, Tokens: scm.StaticToken("t")})
		if (err == nil) != allowed {
			t.Errorf("%q: err = %v", api, err)
		}
	}
}

func TestAMalformedProjectIsRefused(t *testing.T) {
	for _, project := range []string{"", "0", "group/project", "1/../2", "1?x=1"} {
		if _, err := scm.NewGitLab(scm.GitLabOptions{API: "https://gitlab.example.com/api/v4", Project: project, Client: &http.Client{}, Tokens: scm.StaticToken("t")}); err == nil {
			t.Errorf("%q was accepted", project)
		}
	}
}

func TestGitLabBranchHeadEncodesASlashAndNamesAMissingBranch(t *testing.T) {
	stub := scmtest.NewGitLab(t)
	stub.Branches["release/2026-11"] = sampleSHA
	host := gitLabAdapter(t, stub)
	head, err := host.BranchHead(context.Background(), "release/2026-11")
	if err != nil || head != sampleSHA {
		t.Fatalf("head %q, err %v", head, err)
	}
	if !strings.HasSuffix(stub.Requests[0], "/repository/branches/release%2F2026-11") {
		t.Errorf("request = %s", stub.Requests[0])
	}
	if _, err := host.BranchHead(context.Background(), "main"); !errors.Is(err, scm.ErrNotFound) {
		t.Errorf("a missing branch: %v", err)
	}
}
