package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

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
