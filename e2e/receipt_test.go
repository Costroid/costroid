//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/receipt"
	"github.com/Costroid/costroid/internal/receipt/receipttest"
	"github.com/Costroid/costroid/internal/scm/scmtest"
)

// receiptBranch is the pull request's branch, on the developer's machine and
// on the host.
const receiptBranch = "fix/retry-backoff"

// TestAReceiptCommentIsShownApartFromTheRunsCost walks a receipt from a
// developer's machine to the pull request. On the machine, the binary counts
// a synthetic copy of a recorded Claude Code session on the branch and prints
// the receipt, and refuses to in a CI job. The author posts it beside a
// stranger's, and the report job, asked for receipts as the action asks,
// states the author's on its own line, priced at the catalog, apart from the
// recorded run's cost: the check's title and conclusion, the step's outputs
// but result, and the rows the ledger stores are the bytes the same job
// writes with no receipt.
func TestAReceiptCommentIsShownApartFromTheRunsCost(t *testing.T) {
	services := compose(t)
	key := services.createKey()
	job := newWorkspace(t)
	runAgent(t, job)

	printed := job.receiptOnAMachine(t)
	stated, err := receipt.Parse(printed)
	if err != nil {
		t.Fatalf("the printed receipt: %v\n%s", err, printed)
	}
	prices, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := receipt.Rows(stated, prices, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	figure := receipt.Figures(rows)[0]

	plain := job.reportWith(t, false, "")
	read := job.reportWith(t, true, printed)
	line := "Developer-machine sessions, unchecked: " + comment.Money("USD", figure.Cost.String()) + " at list (List-equivalent, not billed), 1 session of Claude Code on " + stated.Sessions[0].FirstDay +
		" (UTC), as `octocat` stated them in [a comment](https://" + repository + "/pull/" + pull + "#issuecomment-40) on "
	for name, text := range map[string]string{"comment": read.comment, "check summary": read.summary, "job summary": read.jobSummary} {
		if !strings.Contains(text, line) || !strings.Contains(text, "USD "+recordedTotal) {
			t.Errorf("the %s lacks the receipt's line or the run's figure:\n%s", name, text)
		}
	}
	if strings.Contains(read.comment, "900000000") || strings.Contains(read.check, `"id": 41`) {
		t.Error("the stranger's receipt was read")
	}
	var block struct {
		Schema   string                 `json:"schema"`
		Receipts []comment.BlockReceipt `json:"receipts"`
		Figures  []struct {
			ListCost string `json:"list_cost"`
		} `json:"figures"`
	}
	if err := json.Unmarshal([]byte(read.check), &block); err != nil {
		t.Fatal(err)
	}
	if block.Schema != "costroid.result/0.13" || len(block.Receipts) != 1 || block.Receipts[0].Comment.ID != 40 ||
		block.Receipts[0].Figures[0].ListCost != figure.Cost.String() || len(block.Figures) != 1 || block.Figures[0].ListCost != recordedTotal {
		t.Errorf("the check run's object: %s", read.check)
	}
	if read.title != plain.title || read.conclusion != plain.conclusion || withoutResult(read.outputs) != withoutResult(plain.outputs) {
		t.Errorf("the receipt moved the title, the conclusion or an output:\n%q %s\n%q %s\n%s\n%s", read.title, read.conclusion, plain.title, plain.conclusion, read.outputs, plain.outputs)
	}

	sendRows(t, services, key, job)
	readView(t, services, key)
}

// receiptOnAMachine runs costroid receipt as a developer does, in a clone on
// the branch, outside any CI job, over a synthetic copy of the recorded
// one-request session with the branch planted on its lines. In a CI job the
// same command refuses and prints nothing.
func (w *workspace) receiptOnAMachine(t *testing.T) string {
	t.Helper()
	clone := filepath.Join(w.directory, "clone")
	config := filepath.Join(w.directory, "laptop-claude")
	var env []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !slices.Contains([]string{"CI", "GITHUB_ACTIONS", "GITLAB_CI", "CLAUDE_CONFIG_DIR", "GIT_DIR", "GIT_WORK_TREE"}, name) {
			env = append(env, entry)
		}
	}
	env = append(env, "CLAUDE_CONFIG_DIR="+config, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_AUTHOR_NAME=Developer", "GIT_AUTHOR_EMAIL=developer@example.com",
		"GIT_COMMITTER_NAME=Developer", "GIT_COMMITTER_EMAIL=developer@example.com", "GIT_COMMITTER_DATE=2026-11-01T00:00:00Z")
	for _, args := range [][]string{
		{"init", "--quiet", "--initial-branch=main", clone},
		{"-C", clone, "commit", "--quiet", "--allow-empty", "--message", "Start"},
		{"-C", clone, "checkout", "--quiet", "-b", receiptBranch},
	} {
		git := exec.Command("git", args...)
		git.Env = env
		if out, err := git.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	receipttest.Write(t, receipttest.OneRequest, config, clone, "11111111-0000-4000-8000-000000000001",
		func(receipttest.Line) (string, string) { return clone, receiptBranch }, nil)
	command := exec.Command(w.binary, "receipt")
	command.Dir, command.Env = clone, env
	var stdout, stderr strings.Builder
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("costroid receipt: %v\n%s", err, stderr.String())
	}
	refused := exec.Command(w.binary, "receipt")
	refused.Dir, refused.Env = clone, append(slices.Clone(env), "GITHUB_ACTIONS=true")
	if out, err := refused.Output(); err == nil || refused.ProcessState.ExitCode() != 125 || len(out) != 0 {
		t.Errorf("costroid receipt in a CI job: exit %d, stdout %q", refused.ProcessState.ExitCode(), out)
	}
	return stdout.String()
}

// reportRun is what one report job wrote.
type reportRun struct {
	title, conclusion, summary, check string
	comment, jobSummary, outputs      string
}

// reportWith runs the report job on the run's rows, on a host where the
// pull request's author octocat posted the receipt as comment 40 and a
// stranger posted a larger one as comment 41. asked sets the action's
// receipts input.
func (w *workspace) reportWith(t *testing.T, asked bool, posted string) reportRun {
	t.Helper()
	github := scmtest.New(t)
	github.Branches["main"] = base
	github.Author, github.HeadBranch, github.HeadRepository = "octocat", receiptBranch, "acme/widgets"
	at := time.Date(2026, 11, 22, 8, 30, 0, 0, time.UTC)
	if posted != "" {
		stranger := strings.Replace(posted, `"output": "`, `"output": "900000000`, 1)
		github.Comments = []scmtest.Comment{
			{ID: 40, Body: posted, Login: "octocat", CreatedAt: at, UpdatedAt: at},
			{ID: 41, Body: stranger, Login: "mallory", CreatedAt: at.Add(time.Minute), UpdatedAt: at.Add(time.Minute)},
		}
	}
	outputs, summary := filepath.Join(t.TempDir(), "outputs"), filepath.Join(t.TempDir(), "summary")
	env := append(slices.Clone(w.env), "GITHUB_TOKEN="+github.Token, "GITHUB_API_URL="+github.Server.URL,
		"GITHUB_OUTPUT="+outputs, "GITHUB_STEP_SUMMARY="+summary, "COSTROID_RECEIPTS="+map[bool]string{true: "true", false: "false"}[asked])
	code, _, stderr := w.costroid(env, "report", "--rows", w.rows, "--budget-usd", "5")
	if code != 0 || len(github.Checks) != 1 {
		t.Fatalf("costroid report exit %d, %d checks: %s", code, len(github.Checks), stderr)
	}
	run := reportRun{title: github.Checks[0].Title, conclusion: github.Checks[0].Conclusion, summary: github.Checks[0].Summary}
	if checked := fencedObject.FindStringSubmatch(github.Checks[0].Text); checked != nil {
		run.check = checked[1]
	}
	for _, each := range github.Comments {
		if strings.HasPrefix(each.Body, comment.Marker) {
			run.comment = each.Body
		}
	}
	for path, into := range map[string]*string{outputs: &run.outputs, summary: &run.jobSummary} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		*into = string(data)
	}
	return run
}

// withoutResult is the step's outputs but the result object.
func withoutResult(outputs string) string {
	var kept []string
	for _, line := range strings.SplitAfter(outputs, "\n") {
		if !strings.HasPrefix(line, "result=") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "")
}
