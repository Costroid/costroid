package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/scm/scmtest"
)

const (
	realRows   = "../../internal/comment/testdata/rows.jsonl"
	realTotal  = "0.001619725"
	headCommit = "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d"
	baseCommit = "1111111111111111111111111111111111111111"
	mergeSHA   = "2222222222222222222222222222222222222222"
)

// reportEnvironment points the job's variables at a stub. The rows path is
// absolute because some tests move the working directory.
func reportEnvironment(t *testing.T, event string) *scmtest.Stub {
	t.Helper()
	stub := scmtest.New(t)
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, []byte(event), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITLAB_CI", "")
	t.Setenv("GITHUB_TOKEN", stub.Token)
	t.Setenv("GITHUB_API_URL", stub.Server.URL)
	t.Setenv("GITHUB_REPOSITORY", "example/widgets")
	t.Setenv("GITHUB_SHA", mergeSHA)
	t.Setenv("GITHUB_EVENT_NAME", "pull_request")
	t.Setenv("GITHUB_EVENT_PATH", path)
	t.Setenv(installationVariable, "")
	t.Setenv("GITHUB_OUTPUT", "")
	t.Setenv("GITHUB_STEP_SUMMARY", "")
	return stub
}

func pullRequestEvent() string {
	event, err := json.Marshal(map[string]any{"pull_request": map[string]any{
		"number": 7,
		"head":   map[string]string{"sha": headCommit},
		"base":   map[string]string{"sha": baseCommit},
	}})
	if err != nil {
		panic(err)
	}
	return string(event)
}

func TestReportPostsOneNeutralCheckAndOneComment(t *testing.T) {
	stub := reportEnvironment(t, pullRequestEvent())
	code, _, stderr := execute(t, "report", "--rows", realRows, "--budget-usd", "5", "--installation-id", "58120394")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if len(stub.Checks) != 1 || len(stub.Comments) != 1 {
		t.Fatalf("%d checks, %d comments: %s", len(stub.Checks), len(stub.Comments), stderr)
	}
	check := stub.Checks[0]
	if check.Name != "costroid" || check.Conclusion != "neutral" {
		t.Errorf("check = %+v", check)
	}
	if check.HeadSHA != headCommit {
		t.Errorf("the check sits on %s, not the pull request's head", check.HeadSHA)
	}
	if !strings.Contains(check.Text, `"list_cost": "`+realTotal+`"`) {
		t.Errorf("no block in the check:\n%s", check.Text)
	}
	body := stub.Comments[0].Body
	for _, want := range []string{"USD " + realTotal, "List-equivalent, not billed", "Cap: USD 5 (", "agent-flag", "installation=github%3A58120394"} {
		if !strings.Contains(body, want) {
			t.Errorf("no %q in\n%s", want, body)
		}
	}
	if id := `"check_run_id": ` + idOf(check); !strings.Contains(body, id) {
		t.Errorf("no %q in\n%s", id, body)
	}
}

func idOf(check scmtest.Check) string {
	encoded, err := json.Marshal(check.ID)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func TestASecondReportEditsTheCommentInPlace(t *testing.T) {
	stub := reportEnvironment(t, pullRequestEvent())
	execute(t, "report", "--rows", realRows)
	first := stub.Comments[0].Body
	execute(t, "report", "--rows", realRows)
	if len(stub.Comments) != 1 {
		t.Fatalf("%d comments", len(stub.Comments))
	}
	if stub.Comments[0].Body == first {
		t.Error("the comment still names the first check run")
	}
}

func TestZeroRowsStillPostTheCheckAndTheComment(t *testing.T) {
	stub := reportEnvironment(t, pullRequestEvent())
	empty := filepath.Join(t.TempDir(), "rows.jsonl")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, _ := execute(t, "report", "--rows", empty)
	if code != 0 || len(stub.Checks) != 1 || stub.Checks[0].Conclusion != "neutral" || len(stub.Comments) != 1 {
		t.Fatalf("exit %d, %+v, %+v", code, stub.Checks, stub.Comments)
	}
	body := stub.Comments[0].Body
	if !strings.Contains(body, "The rows file holds no rows, so this report shows no figures.") || !strings.Contains(stub.Checks[0].Text, `"reason": "empty_rows_file"`) {
		t.Errorf("body:\n%s", body)
	}
}

func TestAnUnreadableRowsFilePostsTheCheckAndSaysWhy(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "rows.jsonl")
	if err := os.WriteFile(bad, []byte("\n{"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct{ reason, sentence string }{
		bad:                                {"invalid_row", "Line 2 of the rows file is not a valid row, so this report shows no figures."},
		filepath.Join(dir, "absent.jsonl"): {"no_rows_file", "No rows file reached this report, so it shows no figures."},
		dir:                                {"rows_path_not_a_file", "The rows path is not a regular file, so this report did not read it and shows no figures."},
	}
	for path, want := range cases {
		stub := reportEnvironment(t, pullRequestEvent())
		code, _, stderr := execute(t, "report", "--rows", path)
		if code != 0 || len(stub.Checks) != 1 || len(stub.Comments) != 1 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		text, body := stub.Checks[0].Text, stub.Comments[0].Body
		if !strings.Contains(text, `"state": "rows_unreadable"`) || !strings.Contains(text, `"reason": "`+want.reason+`"`) || !strings.Contains(body, want.sentence) {
			t.Errorf("%s:\ncheck:\n%s\ncomment:\n%s", want.reason, text, body)
		}
	}
}

func TestTheCommentNamesTheHeadCommitAndLinksTheWorkflowRun(t *testing.T) {
	stub := reportEnvironment(t, pullRequestEvent())
	t.Setenv("GITHUB_SERVER_URL", "https://github.com/")
	t.Setenv("GITHUB_RUN_ID", "19203384756")
	execute(t, "report", "--rows", realRows)
	want := "This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)"
	if len(stub.Comments) != 1 || !strings.Contains(stub.Comments[0].Body, want) || !strings.Contains(stub.Checks[0].Summary, want) {
		t.Errorf("no %q in %+v", want, stub.Comments)
	}
	for _, variable := range []string{"GITHUB_SERVER_URL", "GITHUB_RUN_ID"} {
		stub := reportEnvironment(t, pullRequestEvent())
		t.Setenv("GITHUB_SERVER_URL", "https://github.com")
		t.Setenv("GITHUB_RUN_ID", "19203384756")
		t.Setenv(variable, "")
		execute(t, "report", "--rows", realRows)
		if body := stub.Comments[0].Body; strings.Contains(body, "[CI run]") || !strings.Contains(body, "This is one run at commit `9f2c6e1`") {
			t.Errorf("without %s:\n%s", variable, body)
		}
	}
}

func TestAFailedPostNeverFailsTheJob(t *testing.T) {
	cases := map[string][]string{
		"the check is refused":             {"POST /check-runs"},
		"the comment is refused":           {"POST /comments"},
		"the listing is refused":           {"GET /comments"},
		"everything is refused":            {"POST /check-runs", "POST /comments", "GET /comments", "GET /contents", "GET /repos"},
		"a fork's token reads and refuses": {"POST /"},
	}
	for name, failures := range cases {
		t.Run(name, func(t *testing.T) {
			stub := reportEnvironment(t, pullRequestEvent())
			for _, failure := range failures {
				stub.Fail[failure] = http.StatusForbidden
			}
			code, _, stderr := execute(t, "report", "--rows", realRows)
			if code != 0 {
				t.Errorf("exit %d", code)
			}
			if !strings.Contains(stderr, "not posted") {
				t.Errorf("stderr = %q", stderr)
			}
			if strings.Contains(stderr, stub.Token) {
				t.Errorf("the token is on stderr: %q", stderr)
			}
		})
	}
}

func TestACommentRefusalLeavesTheCheckPosted(t *testing.T) {
	stub := reportEnvironment(t, pullRequestEvent())
	stub.Fail["POST /comments"] = http.StatusForbidden
	execute(t, "report", "--rows", realRows)
	if len(stub.Checks) != 1 {
		t.Errorf("%d checks", len(stub.Checks))
	}
}

func TestACheckRefusalStillPostsTheCommentAndSaysSo(t *testing.T) {
	stub := reportEnvironment(t, pullRequestEvent())
	stub.Fail["POST /check-runs"] = http.StatusForbidden
	execute(t, "report", "--rows", realRows)
	if len(stub.Comments) != 1 || !strings.Contains(stub.Comments[0].Body, "the check run was not posted") {
		t.Errorf("comments = %+v", stub.Comments)
	}
}

func TestAnUnreachableHostStillExitsZero(t *testing.T) {
	stub := reportEnvironment(t, pullRequestEvent())
	stub.Server.Close()
	code, _, stderr := execute(t, "report", "--rows", realRows)
	if code != 0 || !strings.Contains(stderr, "not posted") {
		t.Errorf("exit %d: %s", code, stderr)
	}
}

func TestAJobWithoutATokenOrARepositoryExitsZeroAndSaysSo(t *testing.T) {
	for _, variable := range []string{"GITHUB_TOKEN", "GITHUB_REPOSITORY"} {
		stub := reportEnvironment(t, pullRequestEvent())
		t.Setenv(variable, "")
		code, _, stderr := execute(t, "report", "--rows", realRows)
		if code != 0 || !strings.Contains(stderr, "not posted") || len(stub.Requests) != 0 {
			t.Errorf("%s: exit %d: %s, %v", variable, code, stderr, stub.Requests)
		}
	}
}

// TestReportOnACommentEventPostsNothingAndSaysWhy runs report where an
// @-mention workflow runs it: on a comment, whose GITHUB_SHA is the default
// branch's last commit. It posts nothing, and one line says why and where to
// read on.
func TestReportOnACommentEventPostsNothingAndSaysWhy(t *testing.T) {
	stub := reportEnvironment(t, `{"action":"created","issue":{"number":7,"pull_request":{"url":"https://api.github.com/repos/example/widgets/pulls/7"}},"comment":{"body":"@claude review"}}`)
	t.Setenv("GITHUB_EVENT_NAME", "issue_comment")
	outputs, summary := stepFiles(t)
	code, stdout, stderr := execute(t, "report", "--rows", realRows, "--json")
	if code != 0 || len(stub.Requests) != 0 || stdout != "" {
		t.Fatalf("exit %d, requests %v, stdout %q: %s", code, stub.Requests, stdout, stderr)
	}
	want := "costroid: not posted: report posts on the pull_request event, and this run's event is \"issue_comment\". " +
		"docs/existing-workflow.md says how to report from the workflow you have\n"
	if stderr != want {
		t.Errorf("stderr:\n%s", stderr)
	}
	for _, path := range []string{outputs, summary} {
		if data, err := os.ReadFile(path); err == nil && len(data) != 0 {
			t.Errorf("%s holds %q", path, data)
		}
	}
}

func TestReportPostsOnThePullRequestEventAlone(t *testing.T) {
	for _, event := range []string{"push", "schedule", "workflow_dispatch", "pull_request_target", "workflow_run", ""} {
		t.Run(event, func(t *testing.T) {
			stub := reportEnvironment(t, pullRequestEvent())
			t.Setenv("GITHUB_EVENT_NAME", event)
			code, _, stderr := execute(t, "report", "--rows", realRows)
			if code != 0 || len(stub.Requests) != 0 || strings.Count(stderr, "\n") != 1 || !strings.Contains(stderr, "costroid: not posted: report posts on the pull_request event") {
				t.Errorf("exit %d, requests %v: %s", code, stub.Requests, stderr)
			}
			if event == "workflow_run" && !strings.Contains(stderr, "costroid follow-up") {
				t.Errorf("a workflow_run job is not sent to the follow-up: %s", stderr)
			}
		})
	}
}

func TestReportNeedsItsRowsFlag(t *testing.T) {
	code, _, stderr := execute(t, "report")
	if code != exitFailure || !strings.Contains(stderr, "--rows") {
		t.Errorf("exit %d: %s", code, stderr)
	}
}

func assertWithheld(t *testing.T, stub *scmtest.Stub) {
	t.Helper()
	if len(stub.Comments) != 1 || len(stub.Checks) != 1 {
		t.Fatalf("%d comments, %d checks", len(stub.Comments), len(stub.Checks))
	}
	body := stub.Comments[0].Body
	for _, amount := range []string{realTotal, "USD 5", `"list_cost"`, `"amount":`} {
		if strings.Contains(body, amount) || strings.Contains(stub.Checks[0].Summary, amount) {
			t.Errorf("%q is out in the open:\n%s\n%s", amount, body, stub.Checks[0].Summary)
		}
	}
	for _, grade := range []string{"neutral", "agent-flag", "unattributed"} {
		if !strings.Contains(body, grade) {
			t.Errorf("no %q in\n%s", grade, body)
		}
	}
	if !strings.Contains(stub.Checks[0].Text, `"list_cost": "`+realTotal+`"`) {
		t.Errorf("the check's block lost its amounts:\n%s", stub.Checks[0].Text)
	}
}

func assertShown(t *testing.T, stub *scmtest.Stub) {
	t.Helper()
	if len(stub.Comments) != 1 || !strings.Contains(stub.Comments[0].Body, "USD "+realTotal) {
		t.Errorf("no amount in %+v", stub.Comments)
	}
}

func TestShowCostFalseOnAPublicRepositoryWithholdsAmountsFromTheComment(t *testing.T) {
	stub := reportEnvironment(t, pullRequestEvent())
	stub.Files[configFile] = "comment:\n  show_cost: false\n"
	stub.SetPrivate(false)
	execute(t, "report", "--rows", realRows, "--budget-usd", "5")
	assertWithheld(t, stub)
	if !strings.Contains(stub.Comments[0].Body, "Amounts are off on this public repository") {
		t.Errorf("body:\n%s", stub.Comments[0].Body)
	}
}

func TestShowCostFalseOnAPrivateRepositoryKeepsAmounts(t *testing.T) {
	stub := reportEnvironment(t, pullRequestEvent())
	stub.Files[configFile] = "comment:\n  show_cost: false\n"
	stub.SetPrivate(true)
	execute(t, "report", "--rows", realRows)
	assertShown(t, stub)
}

func TestNoConfigOnAPublicRepositoryShowsAmounts(t *testing.T) {
	stub := reportEnvironment(t, pullRequestEvent())
	stub.SetPrivate(false)
	execute(t, "report", "--rows", realRows)
	assertShown(t, stub)
}

func TestShowCostIsReadFromTheBaseCommitAndNeverTheHead(t *testing.T) {
	stub := reportEnvironment(t, pullRequestEvent())
	stub.SetPrivate(false)
	execute(t, "report", "--rows", realRows)
	read := false
	for _, line := range stub.Requests {
		if strings.Contains(line, "/contents/") {
			read = true
			if !strings.Contains(line, "ref="+baseCommit) {
				t.Errorf("read from %s", line)
			}
		}
	}
	if !read {
		t.Errorf("costroid.yml was never read: %v", stub.Requests)
	}
}

func TestAPublicRepositoryWhoseSettingCannotBeReadWithholdsAmounts(t *testing.T) {
	cases := map[string]func(*scmtest.Stub){
		"the file read fails":        func(stub *scmtest.Stub) { stub.Fail["GET /contents"] = http.StatusBadGateway },
		"the file does not parse":    func(stub *scmtest.Stub) { stub.Files[configFile] = "comment: [" },
		"show_cost is not a boolean": func(stub *scmtest.Stub) { stub.Files[configFile] = "comment:\n  show_cost: maybe\n" },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			stub := reportEnvironment(t, pullRequestEvent())
			stub.SetPrivate(false)
			breakIt(stub)
			execute(t, "report", "--rows", realRows, "--budget-usd", "5")
			assertWithheld(t, stub)
			if !strings.Contains(stub.Comments[0].Body, "could not be read") {
				t.Errorf("body:\n%s", stub.Comments[0].Body)
			}
		})
	}
}

func TestShowCostFalseWithAnUnknownVisibilityWithholdsAmounts(t *testing.T) {
	stub := reportEnvironment(t, pullRequestEvent())
	stub.Files[configFile] = "comment:\n  show_cost: false\n"
	execute(t, "report", "--rows", realRows, "--budget-usd", "5")
	assertWithheld(t, stub)
}

func TestAPullRequestEventWithoutABaseCommitDoesNotReadTheHeadsConfig(t *testing.T) {
	stub := reportEnvironment(t, `{"pull_request":{"number":7,"head":{"sha":"`+headCommit+`"}}}`)
	stub.Files[configFile] = "comment:\n  show_cost: true\n"
	stub.SetPrivate(false)
	execute(t, "report", "--rows", realRows, "--budget-usd", "5")
	assertWithheld(t, stub)
}

func TestReportRowsRefuseALink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.jsonl")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "costroid-rows.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if rows, err := readReportRows(link); err == nil || rows.state != comment.Unreadable || rows.reason != comment.NotAFile {
		t.Fatalf("state %q, reason %q, error %v", rows.state, rows.reason, err)
	}
}
