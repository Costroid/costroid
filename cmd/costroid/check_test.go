package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/scm/scmtest"
)

// checkWritesGolden holds, for each flow that posts the costroid check, every
// request it writes to the host, what it prints on stderr, and each check run
// it leaves, with every time in them replaced by <time>.
var checkWritesGolden = filepath.Join("testdata", "check-writes.golden")

var (
	goldenTimes = regexp.MustCompile(`[0-9]{4}-[0-9]{2}-[0-9]{2}(T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?Z| [0-9]{2}:[0-9]{2} UTC)`)
	// approvedAt is when the reviewer approved the head in every flow, an hour
	// before the flow runs, so the approval has not expired.
	approvedAt = time.Now().Add(-time.Hour)
)

// checkFlow is one run of report, follow-up or approve against a stub, and
// what it wrote.
type checkFlow struct {
	name string
	run  func(t *testing.T) (*scmtest.Stub, int, string)
}

// recordFlow runs a flow and writes down what it wrote: the requests that
// were not GETs, stderr, and each check run left on the stub.
func recordFlow(t *testing.T, flow checkFlow) string {
	t.Helper()
	stub, writes, stderr := flow.run(t)
	var out strings.Builder
	fmt.Fprintf(&out, "== %s\n-- writes\n", flow.name)
	for _, request := range stub.Writes()[writes:] {
		out.WriteString(request + "\n")
	}
	out.WriteString("-- stderr\n" + stderr)
	for _, check := range stub.Checks {
		fmt.Fprintf(&out, "-- check %d %s %s %s\n%s\n-- text\n%s\n", check.ID, check.Name, check.HeadSHA, check.Conclusion, check.Summary, check.Text)
	}
	return goldenTimes.ReplaceAllString(out.String(), "<time>")
}

// branchPullRequest is approvalEnvironment with pull request 7 listed with its
// one commit, and the workflow that runs the report added to the base branch
// on 2026-10-01, so the report reads the pull request's total and the
// warn-only period has ended.
func branchPullRequest(t *testing.T) *reviewedRepository {
	t.Helper()
	repository := approvalEnvironment(t)
	repository.Pulls = []scmtest.Pull{{Number: 7, State: "open", HeadSHA: headCommit, HeadRepository: "example/widgets", HeadBranch: "feature", Commits: []string{headCommit}}}
	repository.Commits[workflowFile] = []time.Time{time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	t.Setenv("GITHUB_WORKFLOW_REF", "example/widgets/"+workflowFile+"@refs/pull/7/merge")
	return repository
}

// failedReport is the report job on pull request 7 over an enforced budget its
// rows go over, which concludes failure.
func failedReport(t *testing.T) *reviewedRepository {
	t.Helper()
	repository := branchPullRequest(t)
	reportOn(t, repository, headCommit)
	return repository
}

func checkFlows() []checkFlow {
	return []checkFlow{
		{"report: a failed verdict", func(t *testing.T) (*scmtest.Stub, int, string) {
			repository := branchPullRequest(t)
			setEvent(t, "pull_request", pullEvent(headCommit))
			_, _, stderr := execute(t, "report", "--rows", realRows)
			return repository.Stub, 0, stderr
		}},
		{"report: an approved failure", func(t *testing.T) (*scmtest.Stub, int, string) {
			repository := branchPullRequest(t)
			repository.Reviews = []map[string]any{review(31, approver, "APPROVED", headCommit, approvedAt)}
			setEvent(t, "pull_request", pullEvent(headCommit))
			_, _, stderr := execute(t, "report", "--rows", realRows)
			return repository.Stub, 0, stderr
		}},
		{"report: the host refuses the check", func(t *testing.T) (*scmtest.Stub, int, string) {
			repository := branchPullRequest(t)
			repository.Fail["POST /repos/example/widgets/check-runs"] = 500
			setEvent(t, "pull_request", pullEvent(headCommit))
			_, _, stderr := execute(t, "report", "--rows", realRows)
			return repository.Stub, 0, stderr
		}},
		{"report: the host refuses the check's name", func(t *testing.T) (*scmtest.Stub, int, string) {
			repository := branchPullRequest(t)
			repository.Fail["PATCH /repos/example/widgets/check-runs/"] = 500
			setEvent(t, "pull_request", pullEvent(headCommit))
			_, _, stderr := execute(t, "report", "--rows", realRows)
			return repository.Stub, 0, stderr
		}},
		{"follow-up: a fork's rows over an enforced budget", func(t *testing.T) (*scmtest.Stub, int, string) {
			stub := followUpEnvironment(t, "stranger/widgets")
			stub.Commits[agentWorkflow] = []time.Time{time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
			stub.Files[configFile] = enforcedOver
			_, _, stderr := execute(t, "follow-up", "--rows", realRows)
			return stub, 0, stderr
		}},
		{"follow-up: a fork's run left no rows", func(t *testing.T) (*scmtest.Stub, int, string) {
			stub := followUpEnvironment(t, "stranger/widgets")
			stub.Files[configFile] = enforcedOver
			_, _, stderr := execute(t, "follow-up", "--rows", filepath.Join("testdata", "no-such-run", "costroid-rows.jsonl"))
			return stub, 0, stderr
		}},
		{"follow-up: the host refuses the check", func(t *testing.T) (*scmtest.Stub, int, string) {
			stub := followUpEnvironment(t, "stranger/widgets")
			stub.Fail["POST /repos/example/widgets/check-runs"] = 500
			_, _, stderr := execute(t, "follow-up", "--rows", realRows)
			return stub, 0, stderr
		}},
		{"approve: an approval of the head", func(t *testing.T) (*scmtest.Stub, int, string) {
			repository := failedReport(t)
			writes := len(repository.Writes())
			repository.Reviews = []map[string]any{review(31, approver, "APPROVED", headCommit, approvedAt)}
			_, stderr := approve(t, reviewEventJSON("submitted", approver, headCommit))
			return repository.Stub, writes, stderr
		}},
		{"approve: a dismissal", func(t *testing.T) (*scmtest.Stub, int, string) {
			repository := failedReport(t)
			repository.Reviews = []map[string]any{review(31, approver, "APPROVED", headCommit, approvedAt)}
			approve(t, reviewEventJSON("submitted", approver, headCommit))
			writes := len(repository.Writes())
			repository.Reviews[0]["state"] = "DISMISSED"
			_, stderr := approve(t, reviewEventJSON("dismissed", approver, headCommit))
			return repository.Stub, writes, stderr
		}},
		{"approve: the host refuses the check's name", func(t *testing.T) (*scmtest.Stub, int, string) {
			repository := failedReport(t)
			writes := len(repository.Writes())
			repository.Fail["PATCH /repos/example/widgets/check-runs/"] = 500
			repository.Reviews = []map[string]any{review(31, approver, "APPROVED", headCommit, approvedAt)}
			_, stderr := approve(t, reviewEventJSON("submitted", approver, headCommit))
			return repository.Stub, writes, stderr
		}},
		{"approve: the host refuses the check", func(t *testing.T) (*scmtest.Stub, int, string) {
			repository := failedReport(t)
			writes := len(repository.Writes())
			repository.Fail["POST /repos/example/widgets/check-runs"] = 500
			repository.Reviews = []map[string]any{review(31, approver, "APPROVED", headCommit, approvedAt)}
			_, stderr := approve(t, reviewEventJSON("submitted", approver, headCommit))
			return repository.Stub, writes, stderr
		}},
	}
}

// TestCheckWritesPerFlowAreUnchanged runs report, follow-up and approve, each
// on the path that posts the check and on the paths where the host refuses a
// write, and holds what each writes to testdata/check-writes.golden. A change
// to how any flow posts or names its check fails it until it is rerun with
// -update, so the change shows in the diff of the pull request that makes it.
func TestCheckWritesPerFlowAreUnchanged(t *testing.T) {
	var recorded strings.Builder
	for _, flow := range checkFlows() {
		t.Run(flow.name, func(t *testing.T) {
			recorded.WriteString(recordFlow(t, flow))
		})
	}
	if *update {
		if err := os.WriteFile(checkWritesGolden, []byte(recorded.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := os.ReadFile(checkWritesGolden)
	if err != nil {
		t.Fatalf("%v, rerun with -update", err)
	}
	if string(saved) != recorded.String() {
		t.Errorf("%s is stale, rerun with -update. It records:\n%s", checkWritesGolden, recorded.String())
	}
}

// TestFollowUpNamesItsCheck holds a fork's check to what a branch's check
// carries: the check run's id in its own result object and in the step's
// outputs. The second request keeps the fork's sentences in the summary.
func TestFollowUpNamesItsCheck(t *testing.T) {
	for name, rows := range map[string]string{
		"rows over an enforced budget": realRows,
		"no rows":                      filepath.Join("testdata", "no-such-run", "costroid-rows.jsonl"),
	} {
		t.Run(name, func(t *testing.T) {
			stub := followUpEnvironment(t, "stranger/widgets")
			stub.Commits[agentWorkflow] = []time.Time{time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
			stub.Files[configFile] = enforcedOver
			outputs, _ := stepFiles(t)
			code, _, stderr := execute(t, "follow-up", "--rows", rows)
			if code != 0 || len(stub.Checks) != 1 {
				t.Fatalf("exit %d, %d checks: %s", code, len(stub.Checks), stderr)
			}
			check := stub.Checks[0]
			id := fmt.Sprint(check.ID)
			if writes := stub.Writes(); !slices.Equal(writes, []string{"POST /repos/example/widgets/check-runs", "PATCH /repos/example/widgets/check-runs/" + id}) {
				t.Errorf("the follow-up wrote %q", writes)
			}
			if object := objectOf(t, check); object.CheckRunID == nil || *object.CheckRunID != check.ID {
				t.Errorf("the check's object names check run %v, not its own %d", object.CheckRunID, check.ID)
			}
			if got := readOutputs(t, outputs)["check_run_id"]; got != id {
				t.Errorf("the check_run_id output reads %q, want %s", got, id)
			}
			want := forkCapSentence
			if rows != realRows {
				want = forkNoRowsSentence
			}
			if !strings.HasSuffix(check.Summary, "\n\n"+want) {
				t.Errorf("the named check's summary lost the fork's sentences:\n%s", check.Summary)
			}
		})
	}
}

// recordedResultV05 is the costroid check run a release that wrote
// costroid.result/0.5 posted on a real pull request, as GitHub listed it.
const recordedResultV05 = "../../internal/history/testdata/check-runs/check-runs.json"

// readRecordedV05 returns the recorded check run's text, the object exactly
// as that release wrote it.
func readRecordedV05(t *testing.T) string {
	t.Helper()
	var listed struct {
		CheckRuns []struct {
			Output struct {
				Text string `json:"text"`
			} `json:"output"`
		} `json:"check_runs"`
	}
	data, err := os.ReadFile(recordedResultV05)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &listed); err != nil || len(listed.CheckRuns) != 1 {
		t.Fatalf("%s: %v, %d check runs", recordedResultV05, err, len(listed.CheckRuns))
	}
	return listed.CheckRuns[0].Output.Text
}

// replaceOnce replaces old, which must occur exactly once in text.
func replaceOnce(t *testing.T, text, old, replacement string) string {
	t.Helper()
	if count := strings.Count(text, old); count != 1 {
		t.Fatalf("%q occurs %d times", old, count)
	}
	return strings.Replace(text, old, replacement, 1)
}

// TestApproveReconcludesAnOlderResultVersion approves a pull request whose
// newest check a release that wrote costroid.result/0.5 posted. The recorded
// object is read byte for byte as that release wrote it. Set on the head and
// failed over an enforced budget, it is posted again as a 0.5 object, with
// only its conclusion and its check run's id changed.
func TestApproveReconcludesAnOlderResultVersion(t *testing.T) {
	recorded := readRecordedV05(t)
	block, err := comment.ParseResult(recorded)
	if err != nil || block.Schema != "costroid.result/0.5" {
		t.Fatalf("the recorded object reads %q: %v", block.Schema, err)
	}

	failed := replaceOnce(t, recorded, `"head_sha": "0000000000000000000000000000000000000001"`, `"head_sha": "`+headCommit+`"`)
	// The stub numbers the check runs it posts from 1001, and GitHub numbers
	// them in the order they are created, so the earlier check is 999.
	failed = replaceOnce(t, failed, "\"check_run_id\": 90000001,\n  \"conclusion\": \"neutral\"", "\"check_run_id\": 999,\n  \"conclusion\": \"failure\"")
	failed = replaceOnce(t, failed, "\"policy\": {\n    \"conclusion\": \"neutral\"", "\"policy\": {\n    \"conclusion\": \"failure\"")
	failed = replaceOnce(t, failed, "\"active\": true,\n      \"reason\": \"in_period\",\n      \"started\": \"2026-11-04T14:16:40Z\",\n      \"ends\": \"2026-11-11T14:16:40Z\"", "\"active\": false,\n      \"reason\": \"enforced\"")
	failed = replaceOnce(t, failed, "\"outcome\": \"unproven\",\n        \"reason\": \"unverified_rows\",\n        \"limit_usd\": \"0.5\"", "\"outcome\": \"fail\",\n        \"reason\": \"over_limit\",\n        \"limit_usd\": \"0.1\"")

	repository := approvalEnvironment(t)
	repository.Checks = []scmtest.Check{{ID: 999, Name: comment.CheckName, HeadSHA: headCommit, Conclusion: "failure", Text: failed, App: repository.App}}
	repository.Reviews = []map[string]any{review(31, approver, "APPROVED", headCommit, approvedAt)}
	code, stderr := approve(t, reviewEventJSON("submitted", approver, headCommit))
	if code != 0 || len(repository.Checks) != 2 {
		t.Fatalf("exit %d, %d checks: %s", code, len(repository.Checks), stderr)
	}
	posted := repository.Checks[1]
	want := replaceOnce(t, failed, "\"check_run_id\": 999,\n  \"conclusion\": \"failure\"", fmt.Sprintf("\"check_run_id\": %d,\n  \"conclusion\": \"neutral\"", posted.ID))
	if posted.Conclusion != "neutral" || posted.Text != want {
		t.Errorf("the check concludes %s and carries\n%s\nwant\n%s", posted.Conclusion, posted.Text, want)
	}
	if !strings.Contains(posted.Summary, "Check run 999 carries this report in full.") {
		t.Errorf("the summary does not name the recorded check run:\n%s", posted.Summary)
	}

	repository.Reviews[0]["state"] = "DISMISSED"
	if code, stderr := approve(t, reviewEventJSON("dismissed", approver, headCommit)); code != 0 || len(repository.Checks) != 3 || repository.Checks[2].Conclusion != "failure" {
		t.Fatalf("the dismissal of an approval of a 0.5 object: exit %d, %d checks: %s", code, len(repository.Checks), stderr)
	}
	again, err := comment.ParseResult(repository.Checks[2].Text)
	if err != nil || again.Schema != "costroid.result/0.5" {
		t.Errorf("the dismissal posted %q: %v", again.Schema, err)
	}

	for name, schema := range map[string]string{"an earlier version": "costroid.result/0.4", "a later version": "costroid.result/9.9"} {
		t.Run(name, func(t *testing.T) {
			_, err := comment.ParseResult(replaceOnce(t, recorded, `"costroid.result/0.5"`, `"`+schema+`"`))
			if err == nil || !strings.Contains(err.Error(), schema) {
				t.Errorf("read: %v", err)
			}
		})
	}
}
