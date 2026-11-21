package scm_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/scm"
	"github.com/Costroid/costroid/internal/scm/scmtest"
)

// minute is a synthetic instant, minutes past a day's start.
func minute(n int) time.Time {
	return time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC).Add(time.Duration(n) * time.Minute)
}

func numbersOf(pulls []scm.MergedHead) []int {
	numbers := make([]int, len(pulls))
	for index, pull := range pulls {
		numbers[index] = pull.Number
	}
	return numbers
}

func TestMergedIntoListsTheLatestMergesIntoTheBranch(t *testing.T) {
	stub := scmtest.New(t)
	stub.Pulls = []scmtest.Pull{
		{Number: 1, State: "closed", BaseBranch: "main", HeadSHA: sampleSHA, UpdatedAt: minute(90), MergedAt: minute(10)},
		{Number: 2, State: "closed", BaseBranch: "main", HeadSHA: sampleSHA, UpdatedAt: minute(20), MergedAt: minute(20)},
		{Number: 3, State: "closed", BaseBranch: "main", HeadSHA: sampleSHA, UpdatedAt: minute(30)},
		{Number: 4, State: "open", BaseBranch: "main", HeadSHA: sampleSHA, UpdatedAt: minute(40)},
		{Number: 5, State: "closed", BaseBranch: "release", HeadSHA: sampleSHA, UpdatedAt: minute(50), MergedAt: minute(50)},
		{Number: 6, State: "closed", BaseBranch: "main", HeadSHA: movedSHA, UpdatedAt: minute(60), MergedAt: minute(60)},
	}
	host := adapter(t, stub)
	pulls, err := host.MergedInto(context.Background(), "main", 30)
	if err != nil {
		t.Fatal(err)
	}
	if got := numbersOf(pulls); !slices.Equal(got, []int{6, 2, 1}) {
		t.Errorf("merged %v, want 6, 2 and 1, the latest merge first", got)
	}
	if pulls[0].HeadSHA != movedSHA || !pulls[0].MergedAt.Equal(minute(60)) {
		t.Errorf("the latest merge reads %+v", pulls[0])
	}
	if want := "GET /repos/example/widgets/pulls?base=main&direction=desc&page=1&per_page=100&sort=updated&state=closed"; len(stub.Requests) != 1 || stub.Requests[0] != want {
		t.Errorf("requests = %v", stub.Requests)
	}
	if pulls, err := host.MergedInto(context.Background(), "main", 2); err != nil || !slices.Equal(numbersOf(pulls), []int{6, 2}) {
		t.Errorf("a limit of 2 kept %v, %v", numbersOf(pulls), err)
	}
	if _, err := host.MergedInto(context.Background(), "", 30); err == nil {
		t.Error("a list with no branch was asked for")
	}
}

// TestMergedIntoSkipsNoMergeOfAFullAnswer reads a full answer of 100. Pull
// request 101 merged at minute 40, after pull request 100, and was last
// updated before every pull request the answer holds, so the answer leaves
// it out. Only the merge after the answer's oldest update is kept.
func TestMergedIntoSkipsNoMergeOfAFullAnswer(t *testing.T) {
	stub := scmtest.New(t)
	for number := 1; number <= 98; number++ {
		stub.Pulls = append(stub.Pulls, scmtest.Pull{Number: number, State: "closed", BaseBranch: "main", HeadSHA: sampleSHA, UpdatedAt: minute(100 + number)})
	}
	stub.Pulls = append(stub.Pulls,
		scmtest.Pull{Number: 99, State: "closed", BaseBranch: "main", HeadSHA: sampleSHA, UpdatedAt: minute(60), MergedAt: minute(60)},
		scmtest.Pull{Number: 100, State: "closed", BaseBranch: "main", HeadSHA: sampleSHA, UpdatedAt: minute(50), MergedAt: minute(10)},
		scmtest.Pull{Number: 101, State: "closed", BaseBranch: "main", HeadSHA: sampleSHA, UpdatedAt: minute(40), MergedAt: minute(40)},
	)
	pulls, err := adapter(t, stub).MergedInto(context.Background(), "main", 30)
	if err != nil || !slices.Equal(numbersOf(pulls), []int{99}) {
		t.Errorf("merged %v, %v, want 99 alone", numbersOf(pulls), err)
	}
}

func TestMergedIntoRefusesAPullRequestOfAnotherBaseOrWithoutAHead(t *testing.T) {
	for name, pull := range map[string]string{
		"another base": `{"number": 1, "merged_at": "2026-11-02T00:00:00Z", "head": {"sha": "` + sampleSHA + `"}, "base": {"ref": "release"}}`,
		"no head":      `{"number": 1, "merged_at": "2026-11-02T00:00:00Z", "head": {"sha": "main"}, "base": {"ref": "main"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, "["+pull+"]")
			}))
			defer server.Close()
			host, err := scm.NewGitHub(scm.GitHubOptions{API: server.URL, Repository: "example/widgets", Tokens: scm.StaticToken("token"), Client: http.DefaultClient})
			if err != nil {
				t.Fatal(err)
			}
			if pulls, err := host.MergedInto(context.Background(), "main", 30); err == nil {
				t.Errorf("read %+v", pulls)
			}
		})
	}
}

// TestHeadCheckRunsReadsOneAnswer reads a commit's check runs in one request,
// and refuses a commit whose check runs go on past it.
func TestHeadCheckRunsReadsOneAnswer(t *testing.T) {
	for count, fails := range map[int]bool{100: false, 101: true} {
		stub := scmtest.New(t)
		for id := range count {
			stub.Checks = append(stub.Checks, scmtest.Check{ID: int64(id + 1), Name: "costroid", HeadSHA: sampleSHA, App: "github-actions", Text: "text"})
		}
		checks, err := adapter(t, stub).HeadCheckRuns(context.Background(), sampleSHA, "costroid")
		if fails != (err != nil) || !fails && len(checks) != count {
			t.Errorf("%d check runs: %d read, %v", count, len(checks), err)
		}
		if fails && !strings.Contains(fmt.Sprint(err), "more than 100") {
			t.Errorf("%d check runs: %v", count, err)
		}
		if len(stub.Requests) != 1 {
			t.Errorf("%d check runs: %d requests", count, len(stub.Requests))
		}
	}
}
