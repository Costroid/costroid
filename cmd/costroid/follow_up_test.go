package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm/scmtest"
)

const (
	defaultBranchCommit = "3333333333333333333333333333333333333333"
	finishedRunID       = 19203384756
	agentWorkflow       = ".github/workflows/costroid.yml"
	// forkBranch is the fork's branch, named as a fork's branch often is: the
	// same as the base repository's default branch.
	forkBranch = "main"
)

// The reason that stands under the title of a fork's check: for rows the
// follow-up held at neutral, and for a run that left none.
const (
	forkHeldReason   = "This pull request comes from a fork, and the fork's own run wrote these rows. Its check concludes neutral at most, so a rule that fails here warns and does not block a merge."
	forkNoRowsReason = "The recipes run no agent on a fork's pull request. A neutral check does not block a merge."
)

// underTitle reports a check whose summary opens on its title and states
// reason as the paragraph under it.
func underTitle(check scmtest.Check, reason string) bool {
	return strings.HasPrefix(check.Summary, "**"+check.Title+"**\n\n"+reason+"\n\n")
}

// followUpEnvironment is a workflow_run job in example/widgets, after the
// agent's workflow finished on pull request 7 from headRepository, which GitHub
// lists as the head commit's one pull request.
func followUpEnvironment(t *testing.T, headRepository string) *scmtest.Stub {
	t.Helper()
	owner, _, _ := strings.Cut(headRepository, "/")
	event, err := json.Marshal(map[string]any{"workflow_run": map[string]any{
		"id":              finishedRunID,
		"event":           "pull_request",
		"head_sha":        headCommit,
		"head_branch":     forkBranch,
		"path":            agentWorkflow,
		"pull_requests":   []any{},
		"head_repository": map[string]any{"full_name": headRepository, "owner": map[string]string{"login": owner}},
		"repository":      map[string]string{"full_name": "example/widgets"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	stub := reportEnvironment(t, string(event))
	stub.Pulls = []scmtest.Pull{{Number: 7, State: "open", HeadSHA: headCommit, HeadRepository: headRepository, HeadBranch: forkBranch}}
	t.Setenv("GITHUB_EVENT_NAME", "workflow_run")
	t.Setenv("GITHUB_SHA", defaultBranchCommit)
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GITHUB_RUN_ID", "1")
	return stub
}

func absentRows(t *testing.T) string {
	return filepath.Join(t.TempDir(), "costroid-follow-up", "costroid-rows.jsonl")
}

func TestForkFollowUpPostsOneNeutralCheckWithTheReason(t *testing.T) {
	stub := followUpEnvironment(t, "stranger/widgets")
	stub.Files[configFile] = overBudget + "  enforce: true\n"
	code, _, stderr := execute(t, "follow-up", "--rows", absentRows(t))
	if code != 0 || len(stub.Checks) != 1 {
		t.Fatalf("exit %d, %d checks: %s", code, len(stub.Checks), stderr)
	}
	check := stub.Checks[0]
	if check.Name != "costroid" || check.Conclusion != "neutral" || check.HeadSHA != headCommit {
		t.Errorf("check = %+v", check)
	}
	if !strings.HasPrefix(check.Title, "No figure (fork_no_rows): ") || !underTitle(check, forkNoRowsReason) || !strings.Contains(check.Text, `"reason": "fork_no_rows"`) {
		t.Errorf("no reason:\n%s\n%s", check.Summary, check.Text)
	}
	if !strings.Contains(check.Summary, "[CI run](https://github.com/example/widgets/actions/runs/19203384756)") {
		t.Errorf("the summary does not link the finished run:\n%s", check.Summary)
	}
	if len(stub.Comments) != 0 {
		t.Errorf("a follow-up posted %d comments", len(stub.Comments))
	}
	if verdict := checkVerdict(t, check.Text); verdict.ConfigRef != defaultBranchCommit || !verdict.ConfigFromBase {
		t.Errorf("the settings came from %s, not the default branch's commit", verdict.ConfigRef)
	}
	for _, request := range stub.Requests {
		if strings.Contains(request, headCommit) && !bindsTheHead(request) {
			t.Errorf("the follow-up read the head: %s", request)
		}
	}
}

// bindsTheHead reports the read that checks no branch here has the head
// commit as its head. It names the commit and reads nothing at it.
func bindsTheHead(request string) bool {
	return request == "GET /repos/example/widgets/commits/"+headCommit+"/branches-where-head"
}

// TestForkFollowUpRefusesASharedCommit gives a fork's run a head commit that
// is not bound to exactly one open pull request from the fork's branch, or
// that heads a branch here, with rows over an enforced budget. A check on that
// commit could fail someone else's change, so nothing is posted.
func TestForkFollowUpRefusesASharedCommit(t *testing.T) {
	fork := scmtest.Pull{Number: 7, State: "open", HeadSHA: headCommit, HeadRepository: "stranger/widgets", HeadBranch: forkBranch}
	moved := fork
	moved.HeadSHA = defaultBranchCommit
	closed := fork
	closed.State = "closed"
	twin := fork
	twin.Number = 8
	elsewhere := fork
	elsewhere.HeadBranch = "other"
	cases := map[string]struct {
		pulls    []scmtest.Pull
		branches map[string]string
		fail     string
		says     string
	}{
		"the head of the default branch": {
			pulls:    []scmtest.Pull{fork},
			branches: map[string]string{"main": headCommit},
			says:     "the head of a branch of this repository",
		},
		"the head of a branch with no pull request": {
			pulls:    []scmtest.Pull{fork},
			branches: map[string]string{"release": headCommit, "main": defaultBranchCommit},
			says:     "the head of a branch of this repository",
		},
		"in no pull request": {
			says: "0 open pull requests from stranger/widgets have the head commit as their head",
		},
		"in a closed pull request alone": {
			pulls: []scmtest.Pull{closed},
			says:  "0 open pull requests from stranger/widgets",
		},
		"behind the pull request's head": {
			pulls: []scmtest.Pull{moved},
			says:  "0 open pull requests from stranger/widgets",
		},
		"in a pull request from another branch of the fork": {
			pulls: []scmtest.Pull{elsewhere},
			says:  "0 open pull requests from stranger/widgets",
		},
		"in a pull request from another fork": {
			pulls: []scmtest.Pull{{Number: 9, State: "open", HeadSHA: headCommit, HeadRepository: "someone/widgets", HeadBranch: forkBranch}},
			says:  "0 open pull requests from stranger/widgets",
		},
		"in two open pull requests": {
			pulls: []scmtest.Pull{fork, twin},
			says:  "2 open pull requests from stranger/widgets have the head commit as their head, and the check needs exactly 1",
		},
		"a list GitHub refuses": {
			pulls: []scmtest.Pull{fork},
			fail:  "GET /repos/example/widgets/pulls",
			says:  "the fork's open pull requests could not be read",
		},
		"branches GitHub refuses": {
			pulls: []scmtest.Pull{fork},
			fail:  "GET /branches-where-head",
			says:  "the branches at the head commit could not be read",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			stub := followUpEnvironment(t, "stranger/widgets")
			stub.Commits[agentWorkflow] = []time.Time{time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
			stub.Files[configFile] = overBudget + "  enforce: true\n"
			stub.Pulls = c.pulls
			for branch, head := range c.branches {
				stub.Branches[branch] = head
			}
			if c.fail != "" {
				stub.Fail[c.fail] = http.StatusForbidden
			}
			outputs, _ := stepFiles(t)
			code, _, stderr := execute(t, "follow-up", "--rows", realRows)
			if code != 0 || len(stub.Writes()) != 0 || len(stub.Checks) != 0 {
				t.Fatalf("exit %d, writes %v: %s", code, stub.Writes(), stderr)
			}
			if !strings.Contains(stderr, "costroid: not posted: ") || !strings.Contains(stderr, c.says) {
				t.Errorf("stderr: %s", stderr)
			}
			if data, err := os.ReadFile(outputs); err != nil || len(data) != 0 {
				t.Errorf("outputs %q, %v", data, err)
			}
		})
	}
}

// TestForkFollowUpFindsItsPullRequestByBranch holds the lookup to the fork's
// head branch. The stub answers the list of a commit's pull requests empty, as
// GitHub does for a fork's, so a lookup by commit posts nothing and fails here.
func TestForkFollowUpFindsItsPullRequestByBranch(t *testing.T) {
	stub := followUpEnvironment(t, "stranger/widgets")
	code, _, stderr := execute(t, "follow-up", "--rows", absentRows(t))
	if code != 0 || len(stub.Checks) != 1 || stub.Checks[0].HeadSHA != headCommit {
		t.Fatalf("exit %d, checks %+v: %s", code, stub.Checks, stderr)
	}
	lookup := "GET /repos/example/widgets/pulls?head=stranger%3A" + forkBranch + "&page=1&per_page=100&state=open"
	if found := requestsWith(stub.Requests, "/pulls"); len(found) != 1 || found[0] != lookup {
		t.Errorf("the pull request was looked up as %v", found)
	}
}

// TestForkFollowUpCapsAtNeutral gives a fork's pull request rows over an
// enforced budget after the warn-only period. The rules fail, and the check
// concludes neutral and says why.
func TestForkFollowUpCapsAtNeutral(t *testing.T) {
	stub := followUpEnvironment(t, "stranger/widgets")
	stub.Commits[agentWorkflow] = []time.Time{time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	stub.Files[configFile] = overBudget + "  enforce: true\n"
	outputs, _ := stepFiles(t)
	code, _, stderr := execute(t, "follow-up", "--rows", realRows)
	if code != 0 || len(stub.Checks) != 1 {
		t.Fatalf("exit %d, %d checks: %s", code, len(stub.Checks), stderr)
	}
	check := stub.Checks[0]
	if check.Conclusion != "neutral" || check.HeadSHA != headCommit {
		t.Errorf("check = %+v", check)
	}
	if !underTitle(check, forkHeldReason) {
		t.Errorf("the summary does not say why under its title:\n%s", check.Summary)
	}
	block := checkBlock(t, check.Text)
	validateResult(t, block)
	verdict := checkVerdict(t, check.Text)
	if verdict.Conclusion != "neutral" || len(verdict.Results) == 0 || verdict.Results[0].Outcome != "fail" || verdict.Results[0].Reason != "over_limit" {
		t.Errorf("verdict = %+v", verdict)
	}
	if !strings.Contains(stderr, "costroid: check run 1001, conclusion neutral\n") {
		t.Errorf("stderr: %s", stderr)
	}
	if got := readOutputs(t, outputs)["conclusion"]; got != "neutral" {
		t.Errorf("the conclusion output reads %q", got)
	}
}

// TestForkFollowUpLeavesANeutralVerdictAsItIs holds the cap's sentence to the
// checks the cap moved.
func TestForkFollowUpLeavesANeutralVerdictAsItIs(t *testing.T) {
	stub := followUpEnvironment(t, "stranger/widgets")
	stub.Files[configFile] = underBudget
	code, _, stderr := execute(t, "follow-up", "--rows", realRows)
	if code != 0 || len(stub.Checks) != 1 || stub.Checks[0].Conclusion != "neutral" {
		t.Fatalf("exit %d, %+v: %s", code, stub.Checks, stderr)
	}
	if check := stub.Checks[0]; strings.Contains(check.Summary, forkHeldReason) || strings.Contains(check.Title, "fork") {
		t.Errorf("an uncapped check names the cap:\n%s", check.Summary)
	}
}

// TestAForksRowsOverABudgetNeverSayWarnOnly gives a fork's pull request rows
// over a budget while the warn-only period runs, and after it has ended. A
// fork's check is neutral in both, so both read the same held sentence: the
// title never says "warn-only", and nothing a person reads says the check
// will fail after the period or how to enforce it now. The machine block
// keeps what the policy found: the period, and a rule that warns.
func TestAForksRowsOverABudgetNeverSayWarnOnly(t *testing.T) {
	const closing = " A fork's rows are the fork's own claim, so the check is neutral."
	shown := "Over budget, on a fork's own rows: this run cost USD " + realTotal + " at list (List-equivalent, not billed), over the USD 0.001 run budget." + closing
	withheld := "Over budget, on a fork's own rows: this run is over its run budget, priced at list (List-equivalent, not billed)." + closing + " Amounts are withheld here."
	for name, tc := range map[string]struct {
		config  string
		added   []time.Time
		public  bool
		title   string
		outcome string
	}{
		"the workflow is not on the default branch yet": {overBudget, nil, false, shown, "warn"},
		"inside the seven days":                         {overBudget, []time.Time{time.Now().Add(-day)}, false, shown, "warn"},
		"inside the seven days, amounts withheld":       {overBudget + showCostOff, []time.Time{time.Now().Add(-day)}, true, withheld, "warn"},
		"after the period":                              {overBudget, []time.Time{time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}, false, shown, "fail"},
	} {
		t.Run(name, func(t *testing.T) {
			stub := followUpEnvironment(t, "stranger/widgets")
			if tc.added != nil {
				stub.Commits[agentWorkflow] = tc.added
			}
			stub.Files[configFile] = tc.config
			stub.SetPrivate(!tc.public)
			code, _, stderr := execute(t, "follow-up", "--rows", realRows)
			if code != 0 || len(stub.Checks) != 1 {
				t.Fatalf("exit %d, %d checks: %s", code, len(stub.Checks), stderr)
			}
			check := stub.Checks[0]
			if check.Conclusion != "neutral" || check.Title != tc.title || !underTitle(check, forkHeldReason) {
				t.Errorf("the check concludes %s under the title\n%s\nwant neutral under\n%s\nwith the fork's sentence under it:\n%s", check.Conclusion, check.Title, tc.title, check.Summary)
			}
			for _, untrue := range []string{"warn-only", "Warn-only", "this fails the check", "enforce: true"} {
				if strings.Contains(check.Title+check.Summary, untrue) {
					t.Errorf("a fork's check says %q:\n%s", untrue, check.Summary)
				}
			}
			verdict := checkVerdict(t, check.Text)
			if verdict.Conclusion != "neutral" || len(verdict.Results) == 0 || verdict.Results[0].Outcome != tc.outcome || verdict.WarnOnly.Active != (tc.outcome == "warn") {
				t.Errorf("the machine block's verdict = %+v", verdict)
			}
		})
	}
}

// TestAForksFollowUpNeverHoldsASuccess runs the follow-up on a fork's rows
// under each rule the rows are within, enforced. The verdict sentence has no
// words for a verdict that passed every rule and was held at neutral, and no
// follow-up reaches one: rows a fork's own job wrote pass no rule. This fails
// the day one does, so the state cannot appear without a sentence.
func TestAForksFollowUpNeverHoldsASuccess(t *testing.T) {
	checkedOpening := regexp.MustCompile(`^(Within budget|Within limit|Models on the list): `)
	for name, config := range map[string]string{
		"a run budget the rows are within":     underBudget + "  enforce: true\n",
		"a run cost limit the rows are within": "policy:\n  max_run_cost_usd: \"5\"\n  enforce: true\n",
		"a model list the rows are on":         "policy:\n  models:\n    - claude-*\n  enforce: true\n",
		"every one of them":                    underBudget + "  max_run_cost_usd: \"5\"\n  models:\n    - claude-*\n  enforce: true\n",
		"no rule":                              "",
	} {
		t.Run(name, func(t *testing.T) {
			stub := followUpEnvironment(t, "stranger/widgets")
			stub.Commits[agentWorkflow] = []time.Time{time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
			stub.Files[configFile] = config
			code, _, stderr := execute(t, "follow-up", "--rows", realRows)
			if code != 0 || len(stub.Checks) != 1 {
				t.Fatalf("exit %d, %d checks: %s", code, len(stub.Checks), stderr)
			}
			check := stub.Checks[0]
			ruled, passed := 0, 0
			for _, result := range checkVerdict(t, check.Text).Results {
				if result.Rule == policy.RuleAttribution {
					continue
				}
				ruled++
				if result.Outcome == policy.Pass {
					passed++
				}
			}
			if check.Conclusion != "neutral" || (ruled > 0 && passed == ruled) || checkedOpening.MatchString(check.Title) {
				t.Errorf("the follow-up concluded %s over %d of %d rules passed, a held success the sentence has no words for:\n%s", check.Conclusion, passed, ruled, check.Title)
			}
		})
	}
}

// TestAForksFailureHeldAtNeutralSaysSo gives a fork's pull request rows that
// fail an enforced rule. The check concludes neutral, and its title says so
// before anyone opens it: it opens on the rule's own noun and "on a fork's own
// rows", names the amount only where amounts are shown, and ends on why the
// check is neutral. The fork's sentence is the reason under the title, the
// policy's own conclusion in the machine block is held as before, and no
// sentence in the form "Reason: `code`." is left.
func TestAForksFailureHeldAtNeutralSaysSo(t *testing.T) {
	const closing = " A fork's rows are the fork's own claim, so the check is neutral."
	figure := "this run cost USD " + realTotal + " at list (List-equivalent, not billed)"
	for name, tc := range map[string]struct {
		config string
		public bool
		title  string
	}{
		"over a budget, amounts shown": {enforcedOver, false,
			"Over budget, on a fork's own rows: " + figure + ", over the USD 0.001 run budget." + closing},
		"over a budget, amounts withheld": {enforcedOver + showCostOff, true,
			"Over budget, on a fork's own rows: this run is over its run budget, priced at list (List-equivalent, not billed)." + closing + " Amounts are withheld here."},
		"over a limit": {"policy:\n  max_run_cost_usd: \"0.001\"\n  enforce: true\n", false,
			"Over limit, on a fork's own rows: " + figure + ", over the USD 0.001 run cost limit." + closing},
	} {
		t.Run(name, func(t *testing.T) {
			stub := followUpEnvironment(t, "stranger/widgets")
			stub.Commits[agentWorkflow] = []time.Time{time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
			stub.Files[configFile] = tc.config
			stub.SetPrivate(!tc.public)
			code, _, stderr := execute(t, "follow-up", "--rows", realRows)
			if code != 0 || len(stub.Checks) != 1 {
				t.Fatalf("exit %d, %d checks: %s", code, len(stub.Checks), stderr)
			}
			check := stub.Checks[0]
			if check.Conclusion != "neutral" || check.Title != tc.title {
				t.Errorf("the check concludes %s under the title\n%s\nwant neutral under\n%s", check.Conclusion, check.Title, tc.title)
			}
			if !underTitle(check, forkHeldReason) || strings.Contains(check.Summary, "Reason: `") {
				t.Errorf("the fork's sentence is not the reason under the title, or a sentence in the old form is left:\n%s", check.Summary)
			}
			if verdict := checkVerdict(t, check.Text); verdict.Conclusion != "neutral" || len(verdict.Results) == 0 || verdict.Results[0].Outcome != "fail" {
				t.Errorf("the machine block's verdict = %+v", verdict)
			}
		})
	}
}

func TestForkFollowUpReadsAForksRowsAsData(t *testing.T) {
	stub := followUpEnvironment(t, "stranger/widgets")
	stub.Commits[agentWorkflow] = []time.Time{time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	stub.Files[configFile] = "policy:\n  run_budget_usd: \"5\"\n"
	data, err := os.ReadFile(realRows)
	if err != nil {
		t.Fatal(err)
	}
	hostile := strings.ReplaceAll(string(data), `"costroid/model":"claude-haiku-5-5"`, `"costroid/model":"[x](https://example.com) @admin"`)
	if hostile == string(data) {
		t.Fatal("the fixture names no model to replace")
	}
	rows := filepath.Join(t.TempDir(), "costroid-rows.jsonl")
	if err := os.WriteFile(rows, []byte(hostile), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := execute(t, "follow-up", "--rows", rows)
	if code != 0 || len(stub.Checks) != 1 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	check := stub.Checks[0]
	if check.Conclusion != "neutral" || strings.Contains(check.Summary, "fork_no_rows") {
		t.Errorf("rows from a fork concluded %s:\n%s", check.Conclusion, check.Summary)
	}
	if strings.Contains(check.Summary+check.Text, "example.com") || !strings.Contains(check.Text, `"not-shown"`) {
		t.Errorf("a string from the fork's rows passed the allowlist:\n%s\n%s", check.Summary, check.Text)
	}
	if !strings.Contains(check.Text, `"rows": 19`) {
		t.Errorf("the rows were not read:\n%s", check.Text)
	}
}

func TestForkFollowUpRefusesRowsPastTheCap(t *testing.T) {
	stub := followUpEnvironment(t, "stranger/widgets")
	rows := filepath.Join(t.TempDir(), "costroid-rows.jsonl")
	if err := os.WriteFile(rows, make([]byte, maxFollowUpRows+1), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := execute(t, "follow-up", "--rows", rows)
	if code != 0 || len(stub.Checks) != 1 || stub.Checks[0].Conclusion != "neutral" {
		t.Fatalf("exit %d, %+v: %s", code, stub.Checks, stderr)
	}
	if !strings.Contains(stub.Checks[0].Text, `"reason": "rows_file_unread"`) || !strings.Contains(stderr, "over the follow-up's cap") {
		t.Errorf("stderr: %s\n%s", stderr, stub.Checks[0].Text)
	}
}

func TestForkFollowUpPostsNothingOutsideAForksPullRequest(t *testing.T) {
	cases := map[string]struct {
		headRepository, eventName string
	}{
		"a branch of the repository":     {"example/widgets", "workflow_run"},
		"a branch named in another case": {"Example/Widgets", "workflow_run"},
		"another event":                  {"stranger/widgets", "pull_request"},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			stub := followUpEnvironment(t, run.headRepository)
			t.Setenv("GITHUB_EVENT_NAME", run.eventName)
			code, _, stderr := execute(t, "follow-up", "--rows", absentRows(t))
			if code != 0 || len(stub.Requests) != 0 || !strings.Contains(stderr, "costroid: not posted: ") {
				t.Errorf("exit %d, requests %v: %s", code, stub.Requests, stderr)
			}
		})
	}
}

func TestForkFollowUpRefusesOutsideGitHubActions(t *testing.T) {
	followUpEnvironment(t, "stranger/widgets")
	t.Setenv("GITHUB_ACTIONS", "")
	if code, _, stderr := execute(t, "follow-up", "--rows", absentRows(t)); code != exitFailure || !strings.Contains(stderr, "GitHub Actions was not detected") {
		t.Errorf("exit %d: %s", code, stderr)
	}
	if code, _, _ := execute(t, "follow-up"); code != exitFailure {
		t.Errorf("no --rows: exit %d", code)
	}
}

// TestForkWithNoRowsYieldsForkNoRowsInTheMachineBlock reads the reason from the
// check run's machine block, where an agent reads it, and holds the block to
// result/v0.9.
func TestForkWithNoRowsYieldsForkNoRowsInTheMachineBlock(t *testing.T) {
	stub := followUpEnvironment(t, "stranger/widgets")
	outputs, _ := stepFiles(t)
	if code, _, stderr := execute(t, "follow-up", "--rows", absentRows(t)); code != 0 || len(stub.Checks) != 1 {
		t.Fatalf("exit %d, %d checks: %s", code, len(stub.Checks), stderr)
	}
	block := checkBlock(t, stub.Checks[0].Text)
	validateResult(t, block)
	var object struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(block), &object); err != nil {
		t.Fatal(err)
	}
	if object.State != "rows_unreadable" || object.Reason != "fork_no_rows" {
		t.Errorf("state %q, reason %q:\n%s", object.State, object.Reason, block)
	}
	if result := readOutputs(t, outputs)["result"]; result != compactJSON(t, block) {
		t.Errorf("the result output differs from the check's block:\n%s", result)
	}
}
