package main

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/scm/scmtest"
)

// threeReports posts three reports on the pull request's head, under three
// caps, so the head carries three different result objects. It returns the
// stub and the newest check run, which it lists in the middle, so that
// neither end of the list is the newest by its place.
func threeReports(t *testing.T) (*scmtest.Stub, scmtest.Check) {
	t.Helper()
	stub := reportEnvironment(t, pullRequestEvent())
	stub.Head = headCommit
	for _, budget := range []string{"5", "0.001", "1"} {
		if code, _, stderr := execute(t, "report", "--rows", realRows, "--budget-usd", budget); code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
	}
	if len(stub.Checks) != 3 || stub.Checks[0].Text == stub.Checks[1].Text || stub.Checks[1].Text == stub.Checks[2].Text {
		t.Fatalf("the reports posted %d checks", len(stub.Checks))
	}
	newest := slices.MaxFunc(stub.Checks, func(a, b scmtest.Check) int { return cmp.Compare(a.ID, b.ID) })
	stub.Checks = []scmtest.Check{stub.Checks[0], newest, stub.Checks[1]}
	stub.Checks = append(stub.Checks, scmtest.Check{ID: newest.ID + 100, Name: "costroid", HeadSHA: baseCommit, Text: newest.Text, App: stub.App})
	return stub, newest
}

func TestResultCommandReadsTheNewestCheck(t *testing.T) {
	_, newest := threeReports(t)
	code, stdout, stderr := execute(t, "result", "--pr", "7", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if want := checkBlock(t, newest.Text) + "\n"; stdout != want {
		t.Errorf("result --json printed\n%s\nthe newest check run carries\n%s", stdout, want)
	}
	code, stdout, stderr = execute(t, "result", "--pr", "7")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	head := fmt.Sprintf("Pull request 7, commit %s, check run %d: %s.\n", headCommit, newest.ID, newest.Conclusion)
	if !strings.HasPrefix(stdout, head) || !strings.Contains(stdout, "USD "+realTotal+" ") {
		t.Errorf("result printed\n%s", stdout)
	}
}

func TestResultCommandReadsTheCheckRunNotTheComment(t *testing.T) {
	stub, _ := threeReports(t)
	before := len(stub.Requests)
	if code, _, stderr := execute(t, "result", "--pr", "7", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, request := range stub.Requests[before:] {
		if !strings.HasPrefix(request, "GET ") || strings.Contains(request, "/comments") {
			t.Errorf("result asked %s", request)
		}
	}
}

func TestResultCommandSaysWhenNoCheckIsOnTheHead(t *testing.T) {
	stub := reportEnvironment(t, pullRequestEvent())
	stub.Head = headCommit
	code, stdout, stderr := execute(t, "result", "--pr", "7")
	if code != exitRejected || stdout != "" || stderr != "costroid: pull request 7: history: no costroid check run is on the commit yet\n" {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestResultCommandRefusesWithoutATokenOrAPullRequest(t *testing.T) {
	reportEnvironment(t, pullRequestEvent())
	for name, args := range map[string][]string{
		"no pull request": {"result"},
		"an argument":     {"result", "--pr", "7", "extra"},
	} {
		if code, _, stderr := execute(t, args...); code != exitFailure || !strings.Contains(stderr, "result needs --pr") {
			t.Errorf("%s: exit %d, stderr %q", name, code, stderr)
		}
	}
	t.Setenv("GITHUB_TOKEN", "")
	if code, _, stderr := execute(t, "result", "--pr", "7"); code != exitFailure || stderr != "costroid: GITHUB_TOKEN is not set\n" {
		t.Errorf("no token: exit %d, stderr %q", code, stderr)
	}
}
