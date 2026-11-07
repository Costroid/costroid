package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm/scmtest"
)

const (
	// baseHead is the head of the base branch when the report runs. baseCommit
	// stands for the merge-base the event names, which the branch has moved past.
	baseHead     = "3333333333333333333333333333333333333333"
	baseBranch   = "release/2026-11"
	workflowFile = ".github/workflows/costroid.yml"
	// underBudget is the setting at the merge-base, enforcedOver the one the
	// branch holds now.
	underBudget  = "policy:\n  run_budget_usd: \"5\"\n"
	enforcedOver = overBudget + "  enforce: true\n"
)

// pullRequestEventOn is pullRequestEvent with the base branch named.
func pullRequestEventOn(branch string) string {
	event, err := json.Marshal(map[string]any{"pull_request": map[string]any{
		"number": 7,
		"head":   map[string]string{"sha": headCommit},
		"base":   map[string]string{"sha": baseCommit, "ref": branch},
	}})
	if err != nil {
		panic(err)
	}
	return string(event)
}

// gitHubBaseMoved is a repository whose base branch moved past the merge-base
// after the pull request last updated: the budget the branch holds now fails
// the run, and the one at the merge-base would not.
func gitHubBaseMoved(t *testing.T, event string) *scmtest.Stub {
	t.Helper()
	stub := reportEnvironment(t, event)
	t.Setenv("GITHUB_WORKFLOW_REF", "example/widgets/"+workflowFile+"@refs/pull/7/merge")
	stub.Commits[workflowFile] = []time.Time{time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	stub.FilesAt[baseCommit] = map[string]string{configFile: underBudget}
	stub.FilesAt[baseHead] = map[string]string{configFile: enforcedOver}
	return stub
}

// requestsWith returns the logged requests whose line contains part.
func requestsWith(requests []string, part string) []string {
	var found []string
	for _, line := range requests {
		if strings.Contains(line, part) {
			found = append(found, line)
		}
	}
	return found
}

// blockVerdict reads the verdict from a comment's copy of the result object.
func blockVerdict(t *testing.T, body string) (string, policy.Verdict) {
	t.Helper()
	var block struct {
		Conclusion string         `json:"conclusion"`
		Policy     policy.Verdict `json:"policy"`
	}
	if err := json.Unmarshal([]byte(commentBlock(t, body)), &block); err != nil {
		t.Fatal(err)
	}
	return block.Conclusion, block.Policy
}

// TestPolicyReadsTheBaseBranchHead stubs a base branch whose head differs from
// the merge-base the event names. The policy reads costroid.yml and the
// workflow's history at the head, and records the head as config_ref.
func TestPolicyReadsTheBaseBranchHead(t *testing.T) {
	t.Run("GitHub", func(t *testing.T) {
		stub := gitHubBaseMoved(t, pullRequestEventOn(baseBranch))
		stub.Branches[baseBranch] = baseHead
		code, _, stderr := execute(t, "report", "--rows", realRows)
		if code != 0 || len(stub.Checks) != 1 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		verdict := checkVerdict(t, stub.Checks[0].Text)
		if stub.Checks[0].Conclusion != policy.Failure || verdict.ConfigRef != baseHead || !verdict.ConfigFromBase {
			t.Errorf("posted %s, verdict %+v", stub.Checks[0].Conclusion, verdict)
		}
		reads := requestsWith(stub.Requests, "/contents/")
		if len(reads) == 0 || len(requestsWith(reads, "ref="+baseHead)) != len(reads) {
			t.Errorf("costroid.yml was read at %v", reads)
		}
		history := requestsWith(stub.Requests, "/commits?")
		if len(history) == 0 || len(requestsWith(history, "sha="+baseHead)) != len(history) {
			t.Errorf("the warn-only period's start was read at %v", history)
		}
		if strings.Contains(stderr, "the event's base commit") {
			t.Errorf("a resolved head still warns: %s", stderr)
		}
	})
	t.Run("GitLab", func(t *testing.T) {
		stub := gitLabEnvironment(t)
		t.Setenv("CI_MERGE_REQUEST_TARGET_BRANCH_NAME", baseBranch)
		stub.Branches[baseBranch] = baseHead
		stub.Files[baseCommit] = map[string]string{configFile: underBudget}
		stub.Files[baseHead] = map[string]string{configFile: enforcedOver}
		code, _, stderr := execute(t, "report", "--rows", realRows)
		if code != 0 || len(stub.Notes) != 1 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		conclusion, verdict := blockVerdict(t, stub.Notes[0].Body)
		if conclusion != policy.Failure || verdict.ConfigRef != baseHead || !verdict.ConfigFromBase {
			t.Errorf("concluded %s, verdict %+v", conclusion, verdict)
		}
	})
}

// TestBaseHeadUnresolvedFallsBack gives the report a base branch it cannot
// resolve. It reads costroid.yml at the event's base commit, records that
// commit as config_ref, and says so in a warning.
func TestBaseHeadUnresolvedFallsBack(t *testing.T) {
	for name, test := range map[string]struct{ event, warning string }{
		"the host has no such branch": {pullRequestEventOn(baseBranch), "not the base branch's head: policy: read the head of " + baseBranch},
		"the event names no branch":   {pullRequestEvent(), "not the base branch's head: policy: the event names no base branch"},
	} {
		t.Run(name, func(t *testing.T) {
			stub := gitHubBaseMoved(t, test.event)
			stub.FilesAt[baseCommit] = map[string]string{configFile: enforcedOver}
			stub.FilesAt[baseHead] = map[string]string{configFile: underBudget}
			code, _, stderr := execute(t, "report", "--rows", realRows)
			if code != 0 || len(stub.Checks) != 1 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			verdict := checkVerdict(t, stub.Checks[0].Text)
			if stub.Checks[0].Conclusion != policy.Failure || verdict.ConfigRef != baseCommit {
				t.Errorf("posted %s, verdict %+v", stub.Checks[0].Conclusion, verdict)
			}
			if want := "costroid: warning: costroid.yml is read at the event's base commit " + baseCommit + ", " + test.warning; !strings.Contains(stderr, want) {
				t.Errorf("no %q in\n%s", want, stderr)
			}
		})
	}
}

// TestRefusedStreamStillReports refuses the agent's output in the read stage
// and in the run stage. Each writes an empty rows file and the result object
// beside it, and the report's check and comment name reader_refused with the
// reader's first error line, cleaned.
func TestRefusedStreamStillReports(t *testing.T) {
	cases := map[string]struct {
		start func(t *testing.T, rows string) (int, string)
		line  string
	}{
		"read": {
			start: func(t *testing.T, rows string) (int, string) {
				clearCI(t)
				execution := filepath.Join(t.TempDir(), "execution.json")
				body := "[{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"s\"},\n `@team <img>]"
				if err := os.WriteFile(execution, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
				code, _, stderr := execute(t, "ingest-run", "--execution-file", execution, "--out", rows)
				return code, stderr
			},
			line: "claude-code: read the execution file: invalid character '?' looking for beginning of value",
		},
		"run": {
			start: func(t *testing.T, rows string) (int, string) {
				claude := replayClaude(t)
				stream := filepath.Join(t.TempDir(), "stream.jsonl")
				if err := os.WriteFile(stream, []byte("not the agent's stream\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				t.Setenv(replayStream, stream)
				t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
				code, _, stderr := execute(t, "run", "--budget-usd", "5", "--out", rows, "--", claude, "-p", "work")
				return code, stderr
			},
			line: "claude-code: the stream held no result object and no session file was found",
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			rows := filepath.Join(t.TempDir(), "costroid-rows.jsonl")
			code, stderr := test.start(t, rows)
			if code != exitFailure || !strings.Contains(stderr, "costroid: reason reader_refused: ") {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			if written := readFile(t, rows); written != "" {
				t.Errorf("rows: %q", written)
			}
			object := readFile(t, runResultPath(rows))
			validateResult(t, object)
			fields := []string{`"run_reason": "reader_refused"`, `"reader_error": "` + test.line + `"`}
			stub := reportEnvironment(t, pullRequestEvent())
			code, reported, stderr := execute(t, "report", "--rows", rows, "--json")
			if code != 0 || len(stub.Checks) != 1 || len(stub.Comments) != 1 {
				t.Fatalf("report exit %d: %s", code, stderr)
			}
			body := stub.Comments[0].Body
			validateResult(t, reported)
			validateResult(t, commentBlock(t, body))
			for where, text := range map[string]string{"the run's object": object, "the check run": stub.Checks[0].Text, "the comment": body} {
				for _, field := range fields {
					if !strings.Contains(text, field) {
						t.Errorf("%s lacks %s:\n%s", where, field, text)
					}
				}
			}
			sentence := "This report shows no figures. Reason `reader_refused`: the reader refused the agent's stream, so the run wrote no rows. The reader's first error: `" + test.line + "`."
			if !strings.Contains(body, sentence) || !strings.Contains(stub.Checks[0].Summary, sentence) {
				t.Errorf("no %q in\n%s", sentence, body)
			}
		})
	}
}
