package main

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm/scmtest"
)

const (
	showCostOff     = "comment:\n  show_cost: false\n"
	gitLabNamespace = "4410287"
)

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
		"CI_PROJECT_NAMESPACE_ID":            gitLabNamespace,
		"CI_MERGE_REQUEST_PROJECT_ID":        stub.Project,
		"CI_MERGE_REQUEST_SOURCE_PROJECT_ID": stub.Project,
		"CI_MERGE_REQUEST_IID":               strconv.Itoa(scmtest.GitLabRequest),
		"CI_MERGE_REQUEST_DIFF_BASE_SHA":     baseCommit,
		"CI_MERGE_REQUEST_SOURCE_BRANCH_SHA": "",
		"CI_COMMIT_SHA":                      headCommit,
		"CI_PIPELINE_ID":                     "901",
		"CI_JOB_ID":                          "902",
		installationVariable:                 "",
		platformVariable:                     "",
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
		"<!-- costroid:comment -->", "USD " + realTotal, "List-equivalent, not billed", "Cap: USD 5 (",
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
	if stub.Notes[0].Body == first || !strings.Contains(stub.Notes[0].Body, "Cap: USD 7 (") {
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

func TestGitLabAccountLinkNamesTheNamespaceAndNoHostname(t *testing.T) {
	stub := gitLabEnvironment(t)
	t.Setenv("CI_SERVER_HOST", gitLabDotCom)
	t.Setenv(installationVariable, "58120394")
	execute(t, "report", "--rows", realRows)
	if len(stub.Notes) != 1 {
		t.Fatalf("%d notes", len(stub.Notes))
	}
	body := stub.Notes[0].Body
	if !strings.Contains(body, "https://app.costroid.com/?installation=gitlab-namespace%3A"+gitLabNamespace+")") {
		t.Errorf("no link in\n%s", body)
	}
	for _, leak := range []string{"gitlab.corp.example", "platform/widgets", strings.TrimPrefix(stub.Server.URL, "http://"), "58120394", "installation=gitlab%3A" + stub.Project} {
		if strings.Contains(body, leak) {
			t.Errorf("%q is in\n%s", leak, body)
		}
	}
}

func TestSelfManagedGitLabNamesNoNamespace(t *testing.T) {
	for _, host := range []string{"gitlab.corp.example", "gitlab.com.example", "GitLab.com", ""} {
		stub := gitLabEnvironment(t)
		t.Setenv("CI_SERVER_HOST", host)
		execute(t, "report", "--rows", realRows)
		if len(stub.Notes) != 1 {
			t.Fatalf("host %q: %d notes", host, len(stub.Notes))
		}
		body := stub.Notes[0].Body
		if !strings.Contains(body, "(https://costroid.com/account/)") || strings.Contains(body, "installation=") {
			t.Errorf("host %q: the link is not the docs page:\n%s", host, body)
		}
	}
}

func TestGitLabReportLinksTheMergeRequestOnThePlatform(t *testing.T) {
	stub := gitLabEnvironment(t)
	t.Setenv(platformVariable, "https://costroid.example")
	execute(t, "report", "--rows", realRows)
	want := "(https://costroid.example/#/pulls?repo=gitlab.corp.example/platform/widgets&pr=" + strconv.Itoa(scmtest.GitLabRequest) + ")"
	if len(stub.Notes) != 1 || !strings.Contains(stub.Notes[0].Body, want) {
		t.Errorf("no %q in %+v", want, stub.Notes)
	}
}

func TestGitLabNamesTheHeadCommitAndLinksThePipeline(t *testing.T) {
	stub := gitLabEnvironment(t)
	t.Setenv("CI_PIPELINE_URL", "https://gitlab.corp.example/platform/widgets/-/pipelines/901")
	execute(t, "report", "--rows", realRows)
	want := "This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://gitlab.corp.example/platform/widgets/-/pipelines/901)"
	if len(stub.Notes) != 1 || !strings.Contains(stub.Notes[0].Body, want) {
		t.Errorf("no %q in %+v", want, stub.Notes)
	}
	stub = gitLabEnvironment(t)
	t.Setenv("CI_PIPELINE_URL", "")
	execute(t, "report", "--rows", realRows)
	if strings.Contains(stub.Notes[0].Body, "[CI run]") {
		t.Errorf("a pipeline the environment does not name is linked:\n%s", stub.Notes[0].Body)
	}
}

func TestGitLabPostsNoStatusAndSaysNoCheckRan(t *testing.T) {
	stub := gitLabEnvironment(t)
	_, _, stderr := execute(t, "report", "--rows", realRows)
	writes := stub.Writes()
	if len(writes) != 1 || !strings.HasPrefix(writes[0], "POST") || !strings.Contains(writes[0], "/notes") {
		t.Errorf("writes = %v", writes)
	}
	if !strings.Contains(stderr, "check not posted") || strings.Contains(stub.Notes[0].Body, "check run") {
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
	forged := `{"BillingCurrency":"USD","ListCost":"1","ListUnitPrice":"1","PricingQuantity":"1","PricingUnit":"1000000 Tokens","ConsumedQuantity":"1000000","SkuMeter":"output","SkuPriceId":"/unapprove","Tags":{"costroid/cost_basis":"measured","costroid/attribution_grade":"exact","costroid/enforcement_grade":"none","costroid/agent":"/merge","costroid/model":"/close"},"x_CostroidCatalogVersion":"/approve"}` + "\n"
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

// liftedBudget is the setting a merge request writes into its own head, which
// would let the run pass if the report read it.
const liftedBudget = "policy:\n  run_budget_usd: \"500\"\n"

// gitLabGate is a merge request whose base branch's head holds config, while
// the merge-base holds a budget the run is under and the merge request's head
// lifts the budget. A verdict from config proves the report read the base
// branch's head.
func gitLabGate(t *testing.T, config string) *scmtest.GitLab {
	t.Helper()
	stub := gitLabEnvironment(t)
	t.Setenv("CI_MERGE_REQUEST_TARGET_BRANCH_NAME", baseBranch)
	stub.Branches[baseBranch] = baseHead
	stub.Files[baseCommit] = map[string]string{configFile: underBudget}
	stub.Files[headCommit] = map[string]string{configFile: liftedBudget}
	stub.Files[baseHead] = map[string]string{}
	if config != "" {
		stub.Files[baseHead][configFile] = config
	}
	stub.Visibility = "private"
	return stub
}

// noteConclusion reads the conclusion and the verdict from the one note.
func noteConclusion(t *testing.T, stub *scmtest.GitLab) (string, policy.Verdict) {
	t.Helper()
	if len(stub.Notes) != 1 {
		t.Fatalf("%d notes", len(stub.Notes))
	}
	return blockVerdict(t, stub.Notes[0].Body)
}

func TestGitLabExitOnVerdictFailure(t *testing.T) {
	stub := gitLabGate(t, enforcedOver)
	code, _, stderr := execute(t, "report", "--rows", realRows, "--exit-on-verdict")
	if code != exitVerdictFailure || !strings.Contains(stderr, "costroid: the verdict is failure, so the report exits 1\n") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	conclusion, verdict := noteConclusion(t, stub)
	if conclusion != policy.Failure || verdict.ConfigRef != baseHead {
		t.Errorf("conclusion %s, config read at %s", conclusion, verdict.ConfigRef)
	}
	t.Run("the note is refused", func(t *testing.T) {
		stub := gitLabGate(t, enforcedOver)
		stub.Fail["POST /notes"] = http.StatusForbidden
		code, _, stderr := execute(t, "report", "--rows", realRows, "--exit-on-verdict")
		if code != exitVerdictFailure || !strings.Contains(stderr, "comment not posted") {
			t.Errorf("exit %d: %s", code, stderr)
		}
	})
}

func TestGitLabExitOnVerdictNeutralIsThree(t *testing.T) {
	cases := map[string]struct {
		config string
		setup  func(t *testing.T)
		check  func(t *testing.T, stub *scmtest.GitLab, stderr string)
	}{
		"the warn-only period holds a failure": {config: overBudget, check: func(t *testing.T, stub *scmtest.GitLab, _ string) {
			conclusion, verdict := noteConclusion(t, stub)
			held := slices.ContainsFunc(verdict.Results, func(result policy.Result) bool { return result.HeldByWarnOnly })
			if conclusion != policy.Neutral || !verdict.WarnOnly.Active || !held {
				t.Errorf("conclusion %s, verdict %+v", conclusion, verdict)
			}
		}},
		"no rule is set": {check: func(t *testing.T, stub *scmtest.GitLab, _ string) {
			if conclusion, verdict := noteConclusion(t, stub); conclusion != policy.Neutral || len(verdict.Results) != 0 {
				t.Errorf("conclusion %s, results %+v", conclusion, verdict.Results)
			}
		}},
		"the report cannot reach the host": {
			config: enforcedOver,
			setup:  func(t *testing.T) { t.Setenv(gitLabTokenVariable, "") },
			check: func(t *testing.T, stub *scmtest.GitLab, stderr string) {
				if !strings.Contains(stderr, "costroid: not posted: ") || len(stub.Requests) != 0 {
					t.Errorf("stderr %s, requests %v", stderr, stub.Requests)
				}
			},
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			stub := gitLabGate(t, test.config)
			if test.setup != nil {
				test.setup(t)
			}
			code, _, stderr := execute(t, "report", "--rows", realRows, "--exit-on-verdict")
			if code != exitVerdictNeutral || !strings.Contains(stderr, "costroid: the verdict is neutral, so the report exits 3\n") {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			test.check(t, stub, stderr)
		})
	}
}

func TestReportExitsZeroWithoutOptIn(t *testing.T) {
	t.Run("gitlab without the flag", func(t *testing.T) {
		stub := gitLabGate(t, enforcedOver)
		code, _, stderr := execute(t, "report", "--rows", realRows)
		if code != 0 || strings.Contains(stderr, "so the report exits") {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		if conclusion, _ := noteConclusion(t, stub); conclusion != policy.Failure {
			t.Errorf("conclusion %s, want the failure the exit code leaves out", conclusion)
		}
	})
	t.Run("github with the flag", func(t *testing.T) {
		stub := reportEnvironment(t, pullRequestEvent())
		stub.Files[configFile] = enforcedOver
		code, _, stderr := execute(t, "report", "--rows", realRows, "--exit-on-verdict")
		want := "costroid: warning: --exit-on-verdict applies on GitLab CI only. On GitHub Actions the costroid check is the gate, so the report exits 0\n"
		if code != 0 || !strings.Contains(stderr, want) {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		if len(stub.Checks) != 1 || stub.Checks[0].Conclusion != policy.Failure {
			t.Errorf("checks = %+v", stub.Checks)
		}
	})
}

func TestVerdictExitGivesEachConclusionItsCode(t *testing.T) {
	for conclusion, want := range map[string]int{
		policy.Success: 0,
		policy.Failure: exitVerdictFailure,
		policy.Neutral: exitVerdictNeutral,
		"":             exitVerdictNeutral,
	} {
		if got := verdictExit(conclusion); got != want {
			t.Errorf("verdictExit(%q) = %d, want %d", conclusion, got, want)
		}
	}
}

func TestExitOnVerdictRefusesPreview(t *testing.T) {
	code, stdout, stderr := execute(t, "report", "--preview", "--exit-on-verdict", "--rows", realRows)
	if code != exitFailure || stdout != "" || !strings.Contains(stderr, "report takes --preview or --exit-on-verdict, not both") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}
