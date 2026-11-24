package scm_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/docstest"
	"github.com/Costroid/costroid/internal/scm"
)

// visibilityWarning is how the report prints an error of Visibility.
const visibilityWarning = "costroid: warning: repository visibility: "

// visibilityRow is a row of docs/troubleshooting.md for that warning: its
// first cell, quoted.
var visibilityRow = regexp.MustCompile("(?m)^\\| `(" + regexp.QuoteMeta(visibilityWarning) + "[^`]*)` \\|")

// answering is a client whose every request gets status and body, or err.
func answering(status int, body string, err error) *http.Client {
	return &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Request: request, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
}

// visibilityErrors are the errors Visibility answers on GitHub and on GitLab
// for each way a host can fail it: each refusal, no answer, an answer that is
// not JSON, and one that states no visibility.
func visibilityErrors(t *testing.T) []error {
	t.Helper()
	type failure struct {
		status int
		body   string
		err    error
	}
	failures := []failure{{status: http.StatusOK, body: "<html>"}, {err: errors.New("dial tcp: i/o timeout")}}
	for _, status := range []int{401, 403, 404, 429, 500, 502} {
		failures = append(failures, failure{status: status})
	}
	var found []error
	ctx := context.Background()
	for _, host := range []string{"github", "gitlab"} {
		for _, f := range append(failures, failure{status: http.StatusOK, body: `{"visibility":"unknown"}`}) {
			client := answering(f.status, f.body, f.err)
			var err error
			if host == "github" {
				github, newErr := scm.NewGitHub(scm.GitHubOptions{API: walkGitHub, Repository: "example/widgets", Tokens: scm.StaticToken("token"), Client: client})
				if newErr != nil {
					t.Fatal(newErr)
				}
				_, err = github.Visibility(ctx)
			} else {
				gitlab, newErr := scm.NewGitLab(scm.GitLabOptions{API: walkGitLab, Project: "42", Tokens: scm.StaticToken("token"), Client: client})
				if newErr != nil {
					t.Fatal(newErr)
				}
				_, err = gitlab.Visibility(ctx)
			}
			if err == nil {
				t.Fatalf("%s answered %d %q and Visibility returned no error", host, f.status, f.body)
			}
			found = append(found, err)
		}
	}
	return found
}

// TestTheVisibilityRowsAreWhatTheAdaptersAnswer holds the rows of
// docs/troubleshooting.md for the report's visibility warning to the errors
// the GitHub and GitLab adapters answer, in both directions: each error the
// warning can print fills a row, and each row is filled by one. The report
// must print the warning as the rows quote it.
func TestTheVisibilityRowsAreWhatTheAdaptersAnswer(t *testing.T) {
	if source := string(read(t, "../../cmd/costroid/report.go")); !strings.Contains(source, strconv.Quote(visibilityWarning+"%v\n")) {
		t.Fatalf("cmd/costroid/report.go prints no %q", visibilityWarning+"%v")
	}
	rows := visibilityRow.FindAllStringSubmatch(docstest.Page(t, "../..", "docs/troubleshooting.md"), -1)
	if len(rows) == 0 {
		t.Fatal("docs/troubleshooting.md has no row for the visibility warning")
	}
	filled := map[string]bool{}
	for _, err := range visibilityErrors(t) {
		line := visibilityWarning + err.Error()
		matched := false
		for _, row := range rows {
			if docstest.Fills(row[1], line) {
				filled[row[1]], matched = true, true
			}
		}
		if !matched {
			t.Errorf("the report can print %q, and docs/troubleshooting.md has no row for it", line)
		}
	}
	for _, row := range rows {
		if !filled[row[1]] {
			t.Errorf("docs/troubleshooting.md has a row for %q, which no error of Visibility prints", row[1])
		}
	}
}
