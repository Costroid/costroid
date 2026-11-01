package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/scm/scmtest"
)

const showCostOff = "comment:\n  show_cost: false\n"

// gitLabEnvironment states a merge request pipeline against a stub. The commit
// is headCommit, as on an ordinary merge request pipeline.
func gitLabEnvironment(t *testing.T) *scmtest.GitLab {
	t.Helper()
	stub := scmtest.NewGitLab(t)
	for name, value := range map[string]string{
		"GITHUB_ACTIONS":                     "",
		"GITLAB_CI":                          "true",
		gitLabTokenVariable:                  stub.Token,
		"CI_API_V4_URL":                      stub.API(),
		"CI_SERVER_HOST":                     "gitlab.corp.example",
		"CI_PROJECT_PATH":                    "platform/widgets",
		"CI_PROJECT_URL":                     "https://gitlab.corp.example/platform/widgets",
		"CI_PROJECT_ID":                      stub.Project,
		"CI_MERGE_REQUEST_PROJECT_ID":        stub.Project,
		"CI_MERGE_REQUEST_SOURCE_PROJECT_ID": stub.Project,
		"CI_MERGE_REQUEST_IID":               strconv.Itoa(scmtest.GitLabRequest),
		"CI_MERGE_REQUEST_DIFF_BASE_SHA":     baseCommit,
		"CI_MERGE_REQUEST_SOURCE_BRANCH_SHA": "",
		"CI_COMMIT_SHA":                      headCommit,
		"CI_PIPELINE_ID":                     "901",
		"CI_JOB_ID":                          "902",
		installationVariable:                 "",
	} {
		t.Setenv(name, value)
	}
	return stub
}

func TestGitLabReportPostsOneNoteWithTheSameComment(t *testing.T) {
	stub := gitLabEnvironment(t)
	code, _, stderr := execute(t, "report", "--rows", realRows, "--budget-usd", "5")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if len(stub.Notes) != 1 {
		t.Fatalf("%d notes: %s", len(stub.Notes), stderr)
	}
	body := stub.Notes[0].Body
	for _, want := range []string{
		"<!-- costroid:comment -->", "USD " + realTotal, "List-equivalent, not billed", "Budget: USD 5.",
		"agent-flag", "unattributed", `"head_sha": "` + headCommit + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("no %q in\n%s", want, body)
		}
	}
}

func TestASecondGitLabReportUpdatesTheOneNote(t *testing.T) {
	stub := gitLabEnvironment(t)
	execute(t, "report", "--rows", realRows, "--budget-usd", "5")
	if len(stub.Notes) != 1 {
		t.Fatalf("%d notes after the first report", len(stub.Notes))
	}
	first, id := stub.Notes[0].Body, stub.Notes[0].ID
	execute(t, "report", "--rows", realRows, "--budget-usd", "7")
	if len(stub.Notes) != 1 || stub.Notes[0].ID != id {
		t.Fatalf("notes = %+v", stub.Notes)
	}
	if stub.Notes[0].Body == first || !strings.Contains(stub.Notes[0].Body, "Budget: USD 7.") {
		t.Errorf("the note was not updated:\n%s", stub.Notes[0].Body)
	}
}

func TestGitLabShowCostFalseOnAPublicProjectPrintsNoAmount(t *testing.T) {
	stub := gitLabEnvironment(t)
	stub.Files[baseCommit] = map[string]string{configFile: showCostOff}
	stub.Visibility = "public"
	execute(t, "report", "--rows", realRows, "--budget-usd", "5")
	if len(stub.Notes) != 1 {
		t.Fatalf("%d notes", len(stub.Notes))
	}
	body := stub.Notes[0].Body
	for _, amount := range []string{realTotal, "USD 5", `"list_cost"`, `"amount":`} {
		if strings.Contains(body, amount) {
			t.Errorf("%q is out in the open:\n%s", amount, body)
		}
	}
	if !strings.Contains(body, "unattributed") || !strings.Contains(body, "agent-flag") {
		t.Errorf("the grades are gone:\n%s", body)
	}
}

func TestGitLabShowCostFalseOnAPrivateProjectPrintsTheAmount(t *testing.T) {
	for _, visibility := range []string{"private", "internal"} {
		t.Run(visibility, func(t *testing.T) {
			stub := gitLabEnvironment(t)
			stub.Files[baseCommit] = map[string]string{configFile: showCostOff}
			stub.Visibility = visibility
			execute(t, "report", "--rows", realRows, "--budget-usd", "5")
			if len(stub.Notes) != 1 || !strings.Contains(stub.Notes[0].Body, "USD "+realTotal) {
				t.Errorf("no amount in %+v", stub.Notes)
			}
		})
	}
}

// The head turns amounts on and the base turns them off, so reading the head
// would publish the amount a public project's base withholds.
func TestGitLabConfigIsReadAtTheBaseCommitAndNeverTheHead(t *testing.T) {
	stub := gitLabEnvironment(t)
	stub.Files[baseCommit] = map[string]string{configFile: showCostOff}
	stub.Files[headCommit] = map[string]string{configFile: "comment:\n  show_cost: true\n"}
	stub.Visibility = "public"
	execute(t, "report", "--rows", realRows)
	if len(stub.Notes) != 1 || strings.Contains(stub.Notes[0].Body, realTotal) {
		t.Fatalf("the head's setting won: %+v", stub.Notes)
	}
	read := false
	for _, line := range stub.Requests {
		if strings.Contains(line, "/repository/files/") {
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

func TestGitLabWithoutABaseCommitWithholdsAmounts(t *testing.T) {
	stub := gitLabEnvironment(t)
	t.Setenv("CI_MERGE_REQUEST_DIFF_BASE_SHA", "")
	stub.Files[headCommit] = map[string]string{configFile: "comment:\n  show_cost: true\n"}
	stub.Visibility = "public"
	execute(t, "report", "--rows", realRows, "--budget-usd", "5")
	if len(stub.Notes) != 1 || strings.Contains(stub.Notes[0].Body, realTotal) {
		t.Errorf("notes = %+v", stub.Notes)
	}
}

func TestGitLabUnknownVisibilityWithholdsAmounts(t *testing.T) {
	stub := gitLabEnvironment(t)
	stub.Files[baseCommit] = map[string]string{configFile: showCostOff}
	execute(t, "report", "--rows", realRows, "--budget-usd", "5")
	if len(stub.Notes) != 1 || strings.Contains(stub.Notes[0].Body, realTotal) || !strings.Contains(stub.Notes[0].Body, "could not be read") {
		t.Errorf("notes = %+v", stub.Notes)
	}
}

func TestGitLabMergedResultsNamesTheSourceBranchHead(t *testing.T) {
	stub := gitLabEnvironment(t)
	t.Setenv("CI_COMMIT_SHA", mergeSHA)
	t.Setenv("CI_MERGE_REQUEST_SOURCE_BRANCH_SHA", headCommit)
	execute(t, "report", "--rows", realRows)
	if len(stub.Notes) != 1 || !strings.Contains(stub.Notes[0].Body, `"head_sha": "`+headCommit+`"`) {
		t.Errorf("notes = %+v", stub.Notes)
	}
}

func TestGitLabAccountLinkNamesTheProjectAndNoHostname(t *testing.T) {
	stub := gitLabEnvironment(t)
	t.Setenv(installationVariable, "58120394")
	execute(t, "report", "--rows", realRows)
	if len(stub.Notes) != 1 {
		t.Fatalf("%d notes", len(stub.Notes))
	}
	body := stub.Notes[0].Body
	if !strings.Contains(body, "https://app.costroid.com/?installation=gitlab%3A"+stub.Project+")") {
		t.Errorf("no link in\n%s", body)
	}
	for _, leak := range []string{"gitlab.corp.example", "platform/widgets", strings.TrimPrefix(stub.Server.URL, "http://"), "58120394"} {
		if strings.Contains(body, leak) {
			t.Errorf("%q is in\n%s", leak, body)
		}
	}
}

func TestGitLabPostsNoStatusAndSaysNoCheckRan(t *testing.T) {
	stub := gitLabEnvironment(t)
	_, _, stderr := execute(t, "report", "--rows", realRows)
	writes := stub.Writes()
	if len(writes) != 1 || !strings.HasPrefix(writes[0], "POST") || !strings.Contains(writes[0], "/notes") {
		t.Errorf("writes = %v", writes)
	}
	if !strings.Contains(stderr, "check not posted") || !strings.Contains(stub.Notes[0].Body, "the check run was not posted") {
		t.Errorf("stderr: %s\nbody:\n%s", stderr, stub.Notes[0].Body)
	}
	if strings.Contains(stub.Notes[0].Body, `"check_run_id"`) {
		t.Errorf("the block names a check:\n%s", stub.Notes[0].Body)
	}
}

// A GitLab note runs a line that opens with a slash as a quick action.
func TestNoLineOfTheNoteIsAQuickAction(t *testing.T) {
	stub := gitLabEnvironment(t)
	rows := filepath.Join(t.TempDir(), "rows.jsonl")
	forged := `{"BillingCurrency":"USD","ListCost":"1","Tags":{"costroid/cost_basis":"measured","costroid/attribution_grade":"exact","costroid/enforcement_grade":"none","costroid/agent":"/merge","costroid/model":"/close"},"x_CostroidCatalogVersion":"/approve"}` + "\n"
	if err := os.WriteFile(rows, []byte(forged), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, stderr := execute(t, "report", "--rows", rows)
	if len(stub.Notes) != 1 {
		t.Fatalf("%d notes: %s", len(stub.Notes), stderr)
	}
	body := stub.Notes[0].Body
	if !strings.Contains(body, "USD 1") {
		t.Fatalf("the forged row was not read:\n%s", body)
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "/") {
			t.Errorf("line %q opens with a slash", line)
		}
	}
}

func TestGitLabStatesUnattributedForAForgedExactGrade(t *testing.T) {
	stub := gitLabEnvironment(t)
	data, err := os.ReadFile(realRows)
	if err != nil {
		t.Fatal(err)
	}
	rows := filepath.Join(t.TempDir(), "rows.jsonl")
	forged := strings.ReplaceAll(string(data), `"costroid/attribution_grade":"unattributed"`, `"costroid/attribution_grade":"exact"`)
	if forged == string(data) {
		t.Fatal("the rows carry no grade to forge")
	}
	if err := os.WriteFile(rows, []byte(forged), 0o644); err != nil {
		t.Fatal(err)
	}
	execute(t, "report", "--rows", rows)
	if len(stub.Notes) != 1 {
		t.Fatalf("%d notes", len(stub.Notes))
	}
	body := stub.Notes[0].Body
	if strings.Contains(body, "exact") || !strings.Contains(body, "unattributed") {
		t.Errorf("body:\n%s", body)
	}
}

func TestGitLabJobWithoutWhatItNeedsExitsZeroAndSaysSo(t *testing.T) {
	cases := map[string]struct{ variable, value string }{
		"no token":         {gitLabTokenVariable, ""},
		"no merge request": {"CI_MERGE_REQUEST_IID", ""},
		"no head":          {"CI_COMMIT_SHA", ""},
		"plain http":       {"CI_API_V4_URL", "http://gitlab.corp.example/api/v4"},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			stub := gitLabEnvironment(t)
			t.Setenv(change.variable, change.value)
			code, _, stderr := execute(t, "report", "--rows", realRows)
			if code != 0 || !strings.Contains(stderr, "not posted") || len(stub.Requests) != 0 {
				t.Errorf("exit %d: %s, %v", code, stderr, stub.Requests)
			}
		})
	}
}

func TestAForksPipelinePostsNothing(t *testing.T) {
	cases := map[string]map[string]string{
		"in the fork":   {"CI_PROJECT_ID": "5555", "CI_MERGE_REQUEST_SOURCE_PROJECT_ID": "5555"},
		"in the parent": {"CI_MERGE_REQUEST_SOURCE_PROJECT_ID": "5555"},
	}
	for name, variables := range cases {
		t.Run(name, func(t *testing.T) {
			stub := gitLabEnvironment(t)
			for variable, value := range variables {
				t.Setenv(variable, value)
			}
			code, _, stderr := execute(t, "report", "--rows", realRows)
			if code != 0 || !strings.Contains(stderr, "fork") || len(stub.Requests) != 0 {
				t.Errorf("exit %d: %s, %v", code, stderr, stub.Requests)
			}
		})
	}
}

func TestAFailedGitLabPostNeverFailsTheJob(t *testing.T) {
	for name, failure := range map[string]string{
		"the note is refused":   "POST /notes",
		"the search is refused": "GET /notes",
		"the user is refused":   "GET /user",
	} {
		t.Run(name, func(t *testing.T) {
			stub := gitLabEnvironment(t)
			stub.Fail[failure] = http.StatusForbidden
			code, _, stderr := execute(t, "report", "--rows", realRows)
			if code != 0 || !strings.Contains(stderr, "comment not posted") || strings.Contains(stderr, stub.Token) {
				t.Errorf("exit %d: %s", code, stderr)
			}
		})
	}
}

func TestReportOutsideCIRefusesAndNamesWhy(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("GITLAB_CI", "")
	code, _, stderr := execute(t, "report", "--rows", realRows)
	if code != exitFailure || !strings.Contains(stderr, errNoCISystem.Error()) {
		t.Errorf("exit %d: %s", code, stderr)
	}
}

func TestGitHubStaysSelectedInsideGitHubActions(t *testing.T) {
	github := reportEnvironment(t, pullRequestEvent())
	gitlab := gitLabEnvironment(t)
	t.Setenv("GITHUB_ACTIONS", "true")
	execute(t, "report", "--rows", realRows)
	if len(github.Comments) != 1 || len(gitlab.Requests) != 0 {
		t.Errorf("%d comments, gitlab saw %v", len(github.Comments), gitlab.Requests)
	}
}
