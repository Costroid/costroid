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

const (
	marker    = "<!-- costroid:comment -->"
	sampleSHA = "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d"
	movedSHA  = "1111111111111111111111111111111111111111"
)

func adapter(t *testing.T, stub *scmtest.Stub) *scm.GitHub {
	t.Helper()
	host, err := scm.NewGitHub(scm.GitHubOptions{API: stub.Server.URL, Repository: "example/widgets", Tokens: scm.StaticToken(stub.Token), Client: http.DefaultClient})
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

func TestUpdateCheckRewritesTheOutputAndKeepsTheConclusion(t *testing.T) {
	stub := scmtest.New(t)
	host := adapter(t, stub)
	id, err := host.PostCheck(context.Background(), scm.CheckRun{Name: "costroid", HeadSHA: sampleSHA, Conclusion: "neutral", Summary: "s", Text: "first", Completed: time.Unix(0, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if err := host.UpdateCheck(context.Background(), id, scm.CheckRun{Title: "Verdict: neutral", Summary: "s", Text: "second"}); err != nil {
		t.Fatal(err)
	}
	if len(stub.Checks) != 1 || stub.Checks[0].Text != "second" || stub.Checks[0].Conclusion != "neutral" {
		t.Errorf("checks = %+v", stub.Checks)
	}
	if writes := stub.Writes(); len(writes) != 2 || writes[1] != fmt.Sprintf("PATCH /repos/example/widgets/check-runs/%d", id) {
		t.Errorf("writes = %v", writes)
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
	stub.Comments = append(stub.Comments, scmtest.Comment{ID: 900, Body: marker + " old", Login: "github-actions[bot]"})
	if err := adapter(t, stub).UpsertComment(context.Background(), 7, marker, marker+" new"); err != nil {
		t.Fatal(err)
	}
	if len(stub.Comments) != 251 || stub.Comments[250].Body != marker+" new" {
		t.Errorf("%d comments, last %q", len(stub.Comments), stub.Comments[len(stub.Comments)-1].Body)
	}
}

func TestAPersonsCommentHoldingTheMarkerIsLeftAlone(t *testing.T) {
	stub := scmtest.New(t)
	stub.Comments = []scmtest.Comment{{ID: 1, Body: marker + " planted by a person", Login: "octocat"}}
	if err := adapter(t, stub).UpsertComment(context.Background(), 7, marker, marker+" new"); err != nil {
		t.Fatal(err)
	}
	if stub.Comments[0].Body != marker+" planted by a person" || len(stub.Comments) != 2 {
		t.Errorf("comments = %+v", stub.Comments)
	}
}

// TestStickyCommentMatchesOwnLogin plants the marker in comments by a person
// and by another App. Only the comment the token's own account wrote is
// edited: GitHub Actions' account by default, and the App's account once a
// check run's answer names that App.
func TestStickyCommentMatchesOwnLogin(t *testing.T) {
	planted := []scmtest.Comment{
		{ID: 1, Body: marker + " planted by a person", Login: "octocat"},
		{ID: 2, Body: marker + " planted by another App", Login: "other-app[bot]"},
		{ID: 3, Body: marker + " by the Actions token", Login: "github-actions[bot]"},
		{ID: 4, Body: marker + " by the App's token", Login: "costroid-app[bot]"},
	}
	cases := []struct {
		name, app string
		postCheck bool
		edited    int64
	}{
		{"the Actions token", "github-actions", false, 3},
		{"the Actions token after a check", "github-actions", true, 3},
		{"an App's token after a check", "costroid-app", true, 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := scmtest.New(t)
			stub.App = c.app
			stub.Comments = append([]scmtest.Comment(nil), planted...)
			host := adapter(t, stub)
			if c.postCheck {
				if _, err := host.PostCheck(context.Background(), scm.CheckRun{Name: "costroid", HeadSHA: sampleSHA, Conclusion: "neutral", Completed: time.Unix(0, 0)}); err != nil {
					t.Fatal(err)
				}
			}
			if err := host.UpsertComment(context.Background(), 7, marker, marker+" new"); err != nil {
				t.Fatal(err)
			}
			if len(stub.Comments) != len(planted) {
				t.Fatalf("a comment was added: %+v", stub.Comments)
			}
			for index, comment := range stub.Comments {
				want := planted[index].Body
				if comment.ID == c.edited {
					want = marker + " new"
				}
				if comment.Body != want {
					t.Errorf("comment %d by %s reads %q", comment.ID, comment.Login, comment.Body)
				}
			}
		})
	}
}

func TestStickyCommentIgnoresAnAppSlugThatIsNotOne(t *testing.T) {
	stub := scmtest.New(t)
	stub.App = "Bad Slug"
	stub.Comments = []scmtest.Comment{{ID: 1, Body: marker + " old", Login: "github-actions[bot]"}}
	host := adapter(t, stub)
	if _, err := host.PostCheck(context.Background(), scm.CheckRun{Name: "costroid", HeadSHA: sampleSHA, Conclusion: "neutral", Completed: time.Unix(0, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := host.UpsertComment(context.Background(), 7, marker, marker+" new"); err != nil {
		t.Fatal(err)
	}
	if len(stub.Comments) != 1 || stub.Comments[0].Body != marker+" new" {
		t.Errorf("comments = %+v", stub.Comments)
	}
}

// TestPullsForCommit finds a fork's open pull request by its head branch. The
// stub answers the list of a commit's pull requests empty, as GitHub does for
// a fork's, so a lookup by commit finds nothing.
func TestPullsForCommit(t *testing.T) {
	stub := scmtest.New(t)
	stub.Pulls = []scmtest.Pull{
		{Number: 7, State: "open", HeadSHA: sampleSHA, HeadRepository: "stranger/widgets", HeadBranch: "fix/a b#c"},
		{Number: 6, State: "closed", HeadSHA: sampleSHA, HeadRepository: "stranger/widgets", HeadBranch: "fix/a b#c"},
		{Number: 5, State: "open", HeadSHA: movedSHA, HeadRepository: "stranger/widgets", HeadBranch: "main"},
		{Number: 4, State: "open", HeadSHA: sampleSHA, HeadRepository: "someone/widgets", HeadBranch: "fix/a b#c"},
	}
	host := adapter(t, stub)
	pulls, err := host.OpenPullsFrom(context.Background(), "stranger", "fix/a b#c")
	if err != nil {
		t.Fatal(err)
	}
	if want := []scm.OpenPull{{Number: 7, HeadSHA: sampleSHA, HeadRepository: "stranger/widgets"}}; fmt.Sprint(pulls) != fmt.Sprint(want) {
		t.Errorf("pulls = %+v", pulls)
	}
	if want := "GET /repos/example/widgets/pulls?head=stranger%3Afix%2Fa+b%23c&page=1&per_page=100&state=open"; stub.Requests[0] != want {
		t.Errorf("request = %s", stub.Requests[0])
	}
	if pulls, err := host.OpenPullsFrom(context.Background(), "nobody", "main"); err != nil || len(pulls) != 0 {
		t.Errorf("a head with no pull request: %v, %v", pulls, err)
	}
	for _, head := range [][2]string{{"", "main"}, {"stranger", ""}} {
		if _, err := host.OpenPullsFrom(context.Background(), head[0], head[1]); err == nil {
			t.Errorf("%q was asked about", head)
		}
	}
	stub.Fail["GET /pulls"] = http.StatusForbidden
	if _, err := host.OpenPullsFrom(context.Background(), "stranger", "main"); !errors.As(err, new(*scm.StatusError)) {
		t.Errorf("a refused list: %v", err)
	}
}

func TestPullsForCommitPagesAndStopsAtAThousand(t *testing.T) {
	for count, fails := range map[int]bool{100: false, 250: false, 999: false, 1000: true} {
		stub := scmtest.New(t)
		for number := range count {
			stub.Pulls = append(stub.Pulls, scmtest.Pull{Number: number + 1, State: "open", HeadSHA: sampleSHA, HeadRepository: "stranger/widgets", HeadBranch: "main"})
		}
		pulls, err := adapter(t, stub).OpenPullsFrom(context.Background(), "stranger", "main")
		if fails {
			if err == nil || !strings.Contains(err.Error(), "more than 1000") {
				t.Errorf("%d pull requests: err = %v", count, err)
			}
			continue
		}
		if err != nil || len(pulls) != count || pulls[count-1].Number != count {
			t.Errorf("%d pull requests: %d read, %v", count, len(pulls), err)
		}
	}
}

func TestBranchesAtNamesEveryBranchWhoseHeadIsTheCommit(t *testing.T) {
	stub := scmtest.New(t)
	stub.Branches["main"] = sampleSHA
	stub.Branches["release"] = sampleSHA
	stub.Branches["other"] = movedSHA
	host := adapter(t, stub)
	branches, err := host.BranchesAt(context.Background(), sampleSHA)
	if err != nil || strings.Join(branches, ",") != "main,release" {
		t.Errorf("branches %v, %v", branches, err)
	}
	if !strings.HasSuffix(stub.Requests[0], "/commits/"+sampleSHA+"/branches-where-head") {
		t.Errorf("request = %s", stub.Requests[0])
	}
	if _, err := host.BranchesAt(context.Background(), "main"); err == nil {
		t.Error("a ref that is not a SHA was asked about")
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
	host, err := scm.NewGitHub(scm.GitHubOptions{API: server.URL, Repository: "example/widgets", Tokens: scm.StaticToken("t"), Client: http.DefaultClient})
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
	host, err := scm.NewGitHub(scm.GitHubOptions{API: server.URL, Repository: "example/widgets", Tokens: scm.StaticToken("t"), Client: http.DefaultClient})
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
		_, err := scm.NewGitHub(scm.GitHubOptions{API: api, Repository: "example/widgets", Tokens: scm.StaticToken("t"), Client: http.DefaultClient})
		if (err == nil) != allowed {
			t.Errorf("%q: err = %v", api, err)
		}
	}
}

func TestTheAdapterNeedsAClient(t *testing.T) {
	_, err := scm.NewGitHub(scm.GitHubOptions{Repository: "example/widgets", Tokens: scm.StaticToken("t")})
	if err == nil {
		t.Fatal("an adapter with no client was built")
	}
}

func TestAMalformedRepositoryIsRefused(t *testing.T) {
	for _, repository := range []string{"", "one", "a/b/c", "a/../../b c", "a/b?x=1"} {
		if _, err := scm.NewGitHub(scm.GitHubOptions{Repository: repository, Tokens: scm.StaticToken("t"), Client: http.DefaultClient}); err == nil {
			t.Errorf("%q was accepted", repository)
		}
	}
}

func TestFirstCommitIsTheOldestOnTheRef(t *testing.T) {
	oldest := time.Date(2026, 11, 1, 9, 30, 0, 0, time.UTC)
	for _, count := range []int{1, 99, 100, 101, 250} {
		stub := scmtest.New(t)
		dates := make([]time.Time, count)
		for index := range dates {
			dates[index] = oldest.Add(time.Duration(count-1-index) * time.Hour)
		}
		stub.Commits[".github/workflows/costroid.yml"] = dates
		added, err := adapter(t, stub).FirstCommit(context.Background(), ".github/workflows/costroid.yml", movedSHA)
		if err != nil || !added.Equal(oldest) {
			t.Errorf("%d commits: %v, %v", count, added, err)
		}
		if !strings.Contains(stub.Requests[0], "sha="+movedSHA) {
			t.Errorf("request = %s", stub.Requests[0])
		}
	}
}

func TestFirstCommitNamesAPathNoCommitTouched(t *testing.T) {
	stub := scmtest.New(t)
	if _, err := adapter(t, stub).FirstCommit(context.Background(), ".github/workflows/costroid.yml", movedSHA); !errors.Is(err, scm.ErrNoCommit) {
		t.Errorf("err = %v", err)
	}
	stub.Fail["GET /commits"] = http.StatusNotFound
	if _, err := adapter(t, stub).FirstCommit(context.Background(), ".github/workflows/costroid.yml", movedSHA); err == nil || errors.Is(err, scm.ErrNoCommit) {
		t.Errorf("a refused list read as no commit: %v", err)
	}
}

func TestFirstCommitStopsAtAThousandCommits(t *testing.T) {
	stub := scmtest.New(t)
	stub.Commits[".github/workflows/costroid.yml"] = make([]time.Time, 1000)
	if _, err := adapter(t, stub).FirstCommit(context.Background(), ".github/workflows/costroid.yml", movedSHA); err == nil || !strings.Contains(err.Error(), "more than 1000") {
		t.Errorf("err = %v", err)
	}
}

func TestBranchHeadReadsTheBranchsOwnRef(t *testing.T) {
	stub := scmtest.New(t)
	stub.Branches["release/2026-11"] = movedSHA
	host := adapter(t, stub)
	head, err := host.BranchHead(context.Background(), "release/2026-11")
	if err != nil || head != movedSHA {
		t.Fatalf("head %q, err %v", head, err)
	}
	if !strings.HasSuffix(stub.Requests[0], "/git/ref/heads/release/2026-11") {
		t.Errorf("request = %s", stub.Requests[0])
	}
	if _, err := host.BranchHead(context.Background(), "main"); !errors.Is(err, scm.ErrNotFound) {
		t.Errorf("a missing branch: %v", err)
	}
}

// TestBranchHeadRefusesAnotherRef answers for a branch with a ref the request
// did not name, as a prefix match would.
func TestBranchHeadRefusesAnotherRef(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"ref":"refs/heads/release/2026-11-hotfix","object":{"sha":%q,"type":"commit"}}`, movedSHA)
	}))
	defer server.Close()
	host, err := scm.NewGitHub(scm.GitHubOptions{API: server.URL, Repository: "example/widgets", Tokens: scm.StaticToken("t"), Client: http.DefaultClient})
	if err != nil {
		t.Fatal(err)
	}
	if head, err := host.BranchHead(context.Background(), "release/2026-11"); err == nil {
		t.Errorf("another ref's head %s was taken", head)
	}
}
