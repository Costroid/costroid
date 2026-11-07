package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm/scmtest"
)

// The digests of realRows, computed outside Costroid:
//
//	jq -r .x_CostroidRowId rows.jsonl | LC_ALL=C sort | sha256sum
//	sha256sum rows.jsonl
const (
	realRowsSHA256     = "ffbdfb76589dfdfd44187050fde97f7a44684d0cb9bb588394dc005ff65bc8f4"
	realRowsFileSHA256 = "74a8dbed24e7286ecc81ca4095a40a499f2ba2cfcde88e9a47522b2123888036"
	// emptySHA256 is the SHA-256 of zero bytes.
	emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	// earlierDigest stands for the digest of an earlier run's rows. The
	// figures beside it are synthetic.
	earlierDigest = "5f1d3c0b8a9e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c5b4a392817060504"
)

// digests reads the two digests of a result object, empty for null.
func digests(t *testing.T, object string) (rows, file string) {
	t.Helper()
	var block comment.Block
	if err := json.Unmarshal([]byte(object), &block); err != nil {
		t.Fatalf("%v: %s", err, object)
	}
	if block.RowsSHA256 != nil {
		rows = *block.RowsSHA256
	}
	if block.RowsFileSHA256 != nil {
		file = *block.RowsFileSHA256
	}
	return rows, file
}

// reordered writes the rows of realRows backwards, with CRLF line endings and
// a blank line: the same rows in another file.
func reordered(t *testing.T) string {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(readFile(t, realRows), "\n"), "\n")
	slices.Reverse(lines)
	path := filepath.Join(t.TempDir(), "rows.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\r\n")+"\r\n\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReportFillsRowsDigests(t *testing.T) {
	unnamed := filepath.Join(t.TempDir(), "rows.jsonl")
	if err := os.WriteFile(unnamed, []byte(strings.Replace(readFile(t, realRows), `"x_CostroidRowId":"1064b631`, `"x_CostroidRowId":"1064B631`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(t.TempDir(), "rows.jsonl")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, rows, wantRows string
	}{
		{"the recorded rows", realRows, realRowsSHA256},
		{"the same rows in another order and layout", reordered(t), realRowsSHA256},
		{"a row id in upper case", unnamed, ""},
		{"an empty rows file", empty, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := reportEnvironment(t, pullRequestEvent())
			code, stdout, stderr := execute(t, "report", "--rows", tc.rows, "--json")
			if code != 0 || len(stub.Checks) != 1 || len(stub.Comments) != 1 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			wantFile := sha256.Sum256([]byte(readFile(t, tc.rows)))
			check := checkBlock(t, stub.Checks[0].Text)
			copied := commentBlock(t, stub.Comments[0].Body)
			for name, object := range map[string]string{"check run": check, "comment copy": copied, "--json": strings.TrimSuffix(stdout, "\n")} {
				validateResult(t, object)
				rows, file := digests(t, object)
				if rows != tc.wantRows {
					t.Errorf("%s: rows_sha256 %q, want %q", name, rows, tc.wantRows)
				}
				if file != hex.EncodeToString(wantFile[:]) {
					t.Errorf("%s: rows_file_sha256 %q does not name this file", name, file)
				}
			}
		})
	}
	if got := sha256.Sum256([]byte(readFile(t, realRows))); hex.EncodeToString(got[:]) != realRowsFileSHA256 {
		t.Errorf("the recorded rows file changed: %x", got)
	}
	if got := sha256.Sum256(nil); hex.EncodeToString(got[:]) != emptySHA256 {
		t.Errorf("the empty digest is %x", got)
	}
}

// pullWithReport gives pull request 7 an earlier commit whose check run
// reports a synthetic USD 0.002 at the rows' basis, over rows of the digest
// given, or of none.
func pullWithReport(t *testing.T, stub *scmtest.Stub, digest string) {
	t.Helper()
	earlier := comment.Figure{Currency: "USD", Basis: "list_equivalent", Attribution: "unattributed", Enforcement: "agent-flag", Cost: decimal.MustParse("0.002"), Rows: 1}
	output, err := comment.Check(comment.Input{State: comment.Reported, Ledger: comment.Ledger{Rows: 1, Figures: []comment.Figure{earlier}}, RowsSHA256: digest})
	if err != nil {
		t.Fatal(err)
	}
	stub.Pulls = []scmtest.Pull{{Number: 7, State: "open", HeadSHA: headCommit, Commits: []string{earlierCommit, headCommit}}}
	stub.Checks = append(stub.Checks, scmtest.Check{ID: 77, Name: comment.CheckName, HeadSHA: earlierCommit, Conclusion: "neutral", Text: output.Text, App: "github-actions"})
}

func pullRequestResult(t *testing.T, verdict policy.Verdict) policy.Result {
	t.Helper()
	for _, result := range verdict.Results {
		if result.Rule == policy.RulePullRequestBudget {
			return result
		}
	}
	t.Fatalf("no pr_budget result in %+v", verdict.Results)
	return policy.Result{}
}

// TestPRBudgetReadsLifetime runs the pull request budget on the total the
// report reads from the pull request's check runs: the earlier runs' USD
// 0.002 and this run's USD 0.001619725.
func TestPRBudgetReadsLifetime(t *testing.T) {
	for _, tc := range []struct {
		name, budget, digest string
		unreadable           bool
		conclusion, outcome  string
		reason, cost         string
	}{
		{"over the budget", "0.003", earlierDigest, false, policy.Failure, policy.Fail, policy.ReasonOverLimit, "0.003619725"},
		{"within the budget", "1", earlierDigest, false, policy.Neutral, policy.Unproven, policy.ReasonUnverifiedRows, "0.003619725"},
		{"an earlier report of these rows", "0.003", realRowsSHA256, false, policy.Neutral, policy.Unproven, policy.ReasonUnverifiedRows, realTotal},
		{"an earlier report without a digest", "0.003", "", false, policy.Neutral, policy.Unproven, policy.ReasonRepeatPossible, "0.003619725"},
		{"a history that cannot be read", "0.003", earlierDigest, true, policy.Neutral, policy.Unproven, policy.ReasonNoLifetime, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := reportEnvironment(t, pullRequestEvent())
			pullWithReport(t, stub, tc.digest)
			if tc.unreadable {
				stub.Fail["GET /commits/"+earlierCommit+"/check-runs"] = 502
			}
			stub.Files[configFile] = "policy:\n  pr_budget_usd: \"" + tc.budget + "\"\n  enforce: true\n"
			code, _, stderr := execute(t, "report", "--rows", realRows)
			if code != 0 || len(stub.Checks) != 2 {
				t.Fatalf("exit %d, %d checks: %s", code, len(stub.Checks), stderr)
			}
			posted := stub.Checks[1]
			verdict := checkVerdict(t, posted.Text)
			result := pullRequestResult(t, verdict)
			if posted.Conclusion != tc.conclusion || result.Outcome != tc.outcome || result.Reason != tc.reason || result.CostUSD != tc.cost {
				t.Errorf("concluded %s with %+v, want %s %s %s at %q", posted.Conclusion, result, tc.conclusion, tc.outcome, tc.reason, tc.cost)
			}
			validateResult(t, checkBlock(t, posted.Text))
		})
	}
}
