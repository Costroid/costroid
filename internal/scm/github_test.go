package scm_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/scm"
	"github.com/Costroid/costroid/internal/scm/scmtest"
)

const (
	marker    = "<!-- costroid:comment -->"
	sampleSHA = "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d"
	movedSHA  = "1111111111111111111111111111111111111111"
)

func adapter(t *testing.T, stub *scmtest.Stub) *scm.GitHub {
	t.Helper()
	host, err := scm.NewGitHub(scm.GitHubOptions{API: stub.Server.URL, Repository: "example/widgets", Tokens: scm.StaticToken(stub.Token)})
	if err != nil {
		t.Fatal(err)
	}
	return host
}

func TestPostCheckCreatesACompletedCheckAndReturnsItsID(t *testing.T) {
	stub := scmtest.New(t)
	id, err := adapter(t, stub).PostCheck(context.Background(), scm.CheckRun{Name: "costroid", HeadSHA: sampleSHA, Conclusion: "neutral", Summary: "s", Text: "t", Completed: time.Unix(0, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if len(stub.Checks) != 1 || stub.Checks[0].ID != id || stub.Checks[0].Name != "costroid" || stub.Checks[0].HeadSHA != sampleSHA || stub.Checks[0].Conclusion != "neutral" {
		t.Errorf("checks = %+v, id %d", stub.Checks, id)
	}
}

func TestStickyCommentIsCreatedOnceAndEditedInPlace(t *testing.T) {
	stub := scmtest.New(t)
	host := adapter(t, stub)
	for _, body := range []string{marker + " first", marker + " second", marker + " third"} {
		if err := host.UpsertComment(context.Background(), 7, marker, body); err != nil {
			t.Fatal(err)
		}
	}
	if len(stub.Comments) != 1 || stub.Comments[0].Body != marker+" third" {
		t.Errorf("comments = %+v", stub.Comments)
	}
	if posts := len(stub.Writes()); posts != 3 {
		t.Errorf("%d writes", posts)
	}
}

func TestStickyCommentIsFoundPastTheFirstPage(t *testing.T) {
	stub := scmtest.New(t)
	for range 250 {
		stub.Comments = append(stub.Comments, scmtest.Comment{ID: int64(len(stub.Comments) + 1), Body: "chatter"})
	}
	stub.Comments = append(stub.Comments, scmtest.Comment{ID: 900, Body: marker + " old", Bot: true})
	if err := adapter(t, stub).UpsertComment(context.Background(), 7, marker, marker+" new"); err != nil {
		t.Fatal(err)
	}
	if len(stub.Comments) != 251 || stub.Comments[250].Body != marker+" new" {
		t.Errorf("%d comments, last %q", len(stub.Comments), stub.Comments[len(stub.Comments)-1].Body)
	}
}

func TestAPersonsCommentHoldingTheMarkerIsLeftAlone(t *testing.T) {
	stub := scmtest.New(t)
	stub.Comments = []scmtest.Comment{{ID: 1, Body: marker + " planted by a person"}}
	if err := adapter(t, stub).UpsertComment(context.Background(), 7, marker, marker+" new"); err != nil {
		t.Fatal(err)
	}
	if stub.Comments[0].Body != marker+" planted by a person" || len(stub.Comments) != 2 {
		t.Errorf("comments = %+v", stub.Comments)
	}
}

func TestReadFileDecodesTheFileAndNamesAMissingOne(t *testing.T) {
	stub := scmtest.New(t)
	stub.Files["costroid.yml"] = "comment:\n  show_cost: false\n"
	host := adapter(t, stub)
	data, err := host.ReadFile(context.Background(), "costroid.yml", sampleSHA)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != stub.Files["costroid.yml"] {
		t.Errorf("data = %q", data)
	}
	if !strings.Contains(stub.Requests[0], "ref="+sampleSHA) {
		t.Errorf("request = %s", stub.Requests[0])
	}
	if _, err := host.ReadFile(context.Background(), "absent.yml", sampleSHA); !errors.Is(err, scm.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestVisibilityReadsTheAnswerAndReadsPublicWhenTheFieldIsAbsent(t *testing.T) {
	stub := scmtest.New(t)
	host := adapter(t, stub)
	if visibility, err := host.Visibility(context.Background()); err == nil || visibility != scm.Public {
		t.Errorf("no field: %v, %v", visibility, err)
	}
	stub.SetPrivate(true)
	if visibility, err := host.Visibility(context.Background()); err != nil || visibility != scm.Private {
		t.Errorf("private: %v, %v", visibility, err)
	}
	stub.SetPrivate(false)
	if visibility, err := host.Visibility(context.Background()); err != nil || visibility != scm.Public {
		t.Errorf("public: %v, %v", visibility, err)
	}
}

func TestErrorsNameTheStatusAndNeverTheToken(t *testing.T) {
	stub := scmtest.New(t)
	stub.Fail["POST /check-runs"] = http.StatusForbidden
	_, err := adapter(t, stub).PostCheck(context.Background(), scm.CheckRun{Completed: time.Now()})
	var refused *scm.StatusError
	if !errors.As(err, &refused) || refused.Status != http.StatusForbidden {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), stub.Token) {
		t.Errorf("the token is in %q", err)
	}
}

func TestRetryAfterIsCarried(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	host, err := scm.NewGitHub(scm.GitHubOptions{API: server.URL, Repository: "example/widgets", Tokens: scm.StaticToken("t")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = host.PostCheck(context.Background(), scm.CheckRun{Completed: time.Now()})
	var refused *scm.StatusError
	if !errors.As(err, &refused) || refused.RetryAfter != 42*time.Second {
		t.Errorf("err = %#v", err)
	}
}

func TestARedirectIsNotFollowed(t *testing.T) {
	followed := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	host, err := scm.NewGitHub(scm.GitHubOptions{API: server.URL, Repository: "example/widgets", Tokens: scm.StaticToken("t")})
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

func TestTheTokenNeverTravelsOverPlainHTTPToAnotherHost(t *testing.T) {
	for api, allowed := range map[string]bool{
		"http://api.example.com": false,
		"ftp://api.example.com":  false,
		"https://ghe.example":    true,
		"http://127.0.0.1:9":     true,
		"":                       true,
	} {
		_, err := scm.NewGitHub(scm.GitHubOptions{API: api, Repository: "example/widgets", Tokens: scm.StaticToken("t")})
		if (err == nil) != allowed {
			t.Errorf("%q: err = %v", api, err)
		}
	}
}

func TestAMalformedRepositoryIsRefused(t *testing.T) {
	for _, repository := range []string{"", "one", "a/b/c", "a/../../b c", "a/b?x=1"} {
		if _, err := scm.NewGitHub(scm.GitHubOptions{Repository: repository, Tokens: scm.StaticToken("t")}); err == nil {
			t.Errorf("%q was accepted", repository)
		}
	}
}
