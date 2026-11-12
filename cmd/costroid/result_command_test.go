package main

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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

func TestResultCommandRefusesWithoutAPullRequest(t *testing.T) {
	reportEnvironment(t, pullRequestEvent())
	for name, args := range map[string][]string{
		"no pull request": {"result"},
		"an argument":     {"result", "--pr", "7", "extra"},
	} {
		if code, _, stderr := execute(t, args...); code != exitFailure || !strings.Contains(stderr, "result needs --pr") {
			t.Errorf("%s: exit %d, stderr %q", name, code, stderr)
		}
	}
}

// TestResultNamesTheFixForAMissingToken runs result with no GITHUB_TOKEN and
// a gh on PATH that leaves a mark when it runs. result exits 125 with the
// export line that sets a token, gh auth token as one way to get it, and
// never runs gh.
func TestResultNamesTheFixForAMissingToken(t *testing.T) {
	reportEnvironment(t, pullRequestEvent())
	t.Setenv("GITHUB_TOKEN", "")
	bin, mark := t.TempDir(), filepath.Join(t.TempDir(), "gh-ran")
	writeFile(t, filepath.Join(bin, "gh"), "#!/bin/sh\ntouch "+mark+"\necho token\n")
	if err := os.Chmod(filepath.Join(bin, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	code, stdout, stderr := execute(t, "result", "--pr", "7")
	want := `costroid: GITHUB_TOKEN is not set: set a token that can read the repository, such as with export GITHUB_TOKEN="$(gh auth token)" once gh is signed in` + "\n"
	if code != exitFailure || stdout != "" || stderr != want {
		t.Errorf("exit %d, stdout %q, stderr %q, want %q", code, stdout, stderr, want)
	}
	if _, err := os.Stat(mark); !errors.Is(err, fs.ErrNotExist) {
		t.Error("result ran gh for a token")
	}
}

// repositoryWithRemotes makes a repository with the remotes given, by name
// and address, and makes it the working directory.
func repositoryWithRemotes(t *testing.T, remotes ...[2]string) {
	t.Helper()
	root := gitRepository(t)
	for _, remote := range remotes {
		runGit(t, root, "remote", "add", remote[0], remote[1])
	}
	t.Chdir(root)
}

// TestResultReadsTheRepositoryFromTheRemote runs result with no --repo and
// no GITHUB_REPOSITORY, in a repository whose remote names one on GitHub.
// result asks GitHub for that repository's pull request, prints its newest
// object, and says on stderr which repository it read and from which remote.
// A remote that names no GitHub repository, or none at all, is refused with
// the flag and the variable that name one.
func TestResultReadsTheRepositoryFromTheRemote(t *testing.T) {
	_, newest := threeReports(t)
	t.Setenv("GITHUB_REPOSITORY", "")
	for _, test := range []struct {
		label, remote, address, read string
	}{
		{"ssh", "origin", "git@github.com:acme/gadgets.git", "acme/gadgets"},
		{"https, the only remote", "upstream", "https://github.com/acme/tools", "acme/tools"},
	} {
		t.Run(test.label, func(t *testing.T) {
			stub := reportEnvironment(t, pullRequestEvent())
			stub.Head = headCommit
			stub.Checks = []scmtest.Check{newest}
			t.Setenv("GITHUB_REPOSITORY", "")
			repositoryWithRemotes(t, [2]string{test.remote, test.address})
			code, stdout, stderr := execute(t, "result", "--pr", "7", "--json")
			if code != 0 || stdout != checkBlock(t, newest.Text)+"\n" {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
			}
			if want := "costroid: result reads " + test.read + ", the repository the remote " + test.remote + " names\n"; stderr != want {
				t.Errorf("stderr %q, want %q", stderr, want)
			}
			for _, request := range stub.Requests {
				if !strings.HasPrefix(request, "GET /repos/"+test.read+"/") {
					t.Errorf("result asked %s", request)
				}
			}
		})
	}
	for _, refusal := range []struct {
		label   string
		remotes [][2]string
		want    string
	}{
		{"gitlab", [][2]string{{"origin", "git@gitlab.com:acme/gadgets.git"}}, "the remote origin names gitlab.com, and result reads GitHub alone"},
		{"none", nil, "the repository has no remote"},
	} {
		t.Run(refusal.label, func(t *testing.T) {
			repositoryWithRemotes(t, refusal.remotes...)
			code, stdout, stderr := execute(t, "result", "--pr", "7")
			want := "costroid: name the repository with --repo OWNER/NAME, or set GITHUB_REPOSITORY: " + refusal.want + "\n"
			if code != exitFailure || stdout != "" || stderr != want {
				t.Errorf("exit %d, stdout %q, stderr %q, want %q", code, stdout, stderr, want)
			}
		})
	}
}
