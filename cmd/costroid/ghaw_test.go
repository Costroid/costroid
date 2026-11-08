package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
)

// ghawArtifact is a gh-aw usage artifact written by hand, as
// internal/adapter/ghaw/testdata/not-recorded/README.md says.
const ghawArtifact = "../../internal/adapter/ghaw/testdata/not-recorded/claude"

// creditsField matches both AI-credit figures of a token-usage record.
var creditsField = regexp.MustCompile(`"ai_credits_(this_response|total)":[0-9.]+`)

// copyArtifact copies the artifact to a new directory, with every AI-credit
// figure in its records replaced by credits, or as it is when credits is
// empty.
func copyArtifact(t *testing.T, credits string) string {
	t.Helper()
	dir := t.TempDir()
	err := filepath.WalkDir(ghawArtifact, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		name, err := filepath.Rel(ghawArtifact, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if credits != "" {
			data = creditsField.ReplaceAll(data, []byte(`"ai_credits_$1":`+credits))
		}
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, name), data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// ingestGhAw runs ingest-run on an artifact and returns the rows file, the
// run's object and what it printed.
func ingestGhAw(t *testing.T, artifact string) (rows, object []byte, stderr string) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "rows.jsonl")
	code, _, stderr := execute(t, "ingest-run", "--from", "gh-aw="+artifact, "--out", out)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	rows, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	object, err = os.ReadFile(runResultPath(out))
	if err != nil {
		t.Fatal(err)
	}
	return rows, object, stderr
}

// TestGhAwStatedCreditsNeverAddToUSD prices a gh-aw run from its tokens alone.
// The same records with AI-credit figures a million times larger write the
// same rows, the same USD line, and the same object and comment but for
// gh-aw's own figure, which names its unit.
func TestGhAwStatedCreditsNeverAddToUSD(t *testing.T) {
	clearCI(t)
	rows, object, stderr := ingestGhAw(t, copyArtifact(t, "1.5"))
	larger, largerObject, largerStderr := ingestGhAw(t, copyArtifact(t, "1500000.5"))
	if !bytes.Equal(rows, larger) {
		t.Error("the AI credits changed the rows")
	}
	if want := strings.Replace(string(object), `"quantity": "3"`, `"quantity": "3000001"`, 1); want == string(object) || want != string(largerObject) {
		t.Errorf("the AI credits changed the run's object beyond agent_stated:\n%s\n%s", object, largerObject)
	}
	comments := map[string]string{}
	for credits, rowsFile := range map[string][]byte{"3": rows, "3000001": larger} {
		path := filepath.Join(t.TempDir(), "rows.jsonl")
		objectFile := object
		if credits != "3" {
			objectFile = largerObject
		}
		if err := os.WriteFile(path, rowsFile, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(runResultPath(path), objectFile, 0o644); err != nil {
			t.Fatal(err)
		}
		code, stdout, previewErr := execute(t, "report", "--preview", "--rows", path)
		line := "gh-aw stated " + credits + " AI credits for this run. Costroid does not convert credits."
		if code != 0 || strings.Count(stdout, line) != 1 {
			t.Fatalf("exit %d, no %q once in\n%s%s", code, line, stdout, previewErr)
		}
		comments[credits] = strings.Replace(strings.Replace(stdout, line, "", 1), `"quantity": "`+credits+`"`, "", 1)
	}
	if comments["3"] != comments["3000001"] {
		t.Errorf("the AI credits changed the comment beyond their line:\n%s\n%s", comments["3"], comments["3000001"])
	}
	var total decimal.Decimal
	for _, line := range strings.Split(strings.TrimSpace(string(rows)), "\n") {
		var row struct{ ListCost string }
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		cost, err := decimal.Parse(row.ListCost)
		if err != nil {
			t.Fatal(err)
		}
		total = total.Add(cost)
	}
	usdLine := "costroid: USD " + total.String() + " at list, Measured\n"
	if !strings.Contains(stderr, usdLine) || !strings.Contains(largerStderr, usdLine) {
		t.Errorf("no %q in\n%s\n%s", usdLine, stderr, largerStderr)
	}
	for output, credits := range map[string]string{stderr: "3", largerStderr: "3000001"} {
		want := "costroid: gh-aw stated " + credits + " AI credits for this run. Costroid does not convert credits.\n"
		if !strings.Contains(output, want) {
			t.Errorf("no %q in %s", want, output)
		}
	}
}

// TestIngestRunRefusesAGhAwArtifactWithAnExtraEntry writes no rows from an
// artifact that holds a file gh-aw does not write, and says which.
func TestIngestRunRefusesAGhAwArtifactWithAnExtraEntry(t *testing.T) {
	clearCI(t)
	artifact := copyArtifact(t, "1")
	if err := os.WriteFile(filepath.Join(artifact, "costroid"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "rows.jsonl")
	code, _, stderr := execute(t, "ingest-run", "--from", "gh-aw="+artifact, "--out", out)
	if code != exitFailure || !strings.Contains(stderr, "the usage artifact holds costroid, which gh-aw v1.2.1 does not write there") {
		t.Errorf("exit %d: %s", code, stderr)
	}
	if rows, err := os.ReadFile(out); err != nil || len(rows) != 0 {
		t.Errorf("rows %q, %v", rows, err)
	}
}

// TestGhAwDocShowsWhatIngestRunPrints runs the command docs/gh-aw.md shows on
// the artifact it names, and finds every line the page quotes.
func TestGhAwDocShowsWhatIngestRunPrints(t *testing.T) {
	clearCI(t)
	page, err := os.ReadFile("../../docs/gh-aw.md")
	if err != nil {
		t.Fatal(err)
	}
	command, printed := docBlock(t, string(page), "sh", "costroid ingest-run"), docBlock(t, string(page), "text", "costroid: gh-aw,")
	artifact := copyArtifact(t, "")
	dir := t.TempDir()
	if err := os.Rename(artifact, filepath.Join(dir, "gh-aw-usage")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	code, _, stderr := execute(t, strings.Fields(command)[1:]...)
	if code != 0 {
		t.Fatalf("%s: exit %d: %s", command, code, stderr)
	}
	for _, line := range strings.Split(printed, "\n") {
		if !strings.Contains(stderr, line+"\n") {
			t.Errorf("the page quotes %q, and the command printed:\n%s", line, stderr)
		}
	}
}

// docBlock is the line of a fenced block of the page that starts with
// prefix, or for a text block, the whole block that starts with it.
func docBlock(t *testing.T, page, language, prefix string) string {
	t.Helper()
	for _, block := range strings.Split(page, "```"+language+"\n")[1:] {
		body, _, _ := strings.Cut(block, "```")
		if language == "text" && strings.HasPrefix(body, prefix) {
			return strings.TrimSuffix(body, "\n")
		}
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(line, prefix) {
				return line
			}
		}
	}
	t.Fatalf("docs/gh-aw.md has no %s block with %q", language, prefix)
	return ""
}

// ingestedRows runs ingest-run on the gh-aw artifact, as the gh-aw follow-up
// recipe does, and returns the rows file beside its run object.
func ingestedRows(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "costroid-rows.jsonl")
	if code, _, stderr := execute(t, "ingest-run", "--from", "gh-aw="+copyArtifact(t, ""), "--out", out); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	return out
}

// TestGhAwFollowUpPostsOnABranchWithTheCredits posts the check of a gh-aw run
// on a pull request from a branch of this repository, which has no report job
// of its own. The check states gh-aw's credits on their own line and in the
// object, and its verdict stands as report's would.
func TestGhAwFollowUpPostsOnABranchWithTheCredits(t *testing.T) {
	stub := followUpEnvironment(t, "example/widgets")
	stub.Files[configFile] = overBudget + "  enforce: true\n"
	stub.Commits[agentWorkflow] = []time.Time{time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	rows := ingestedRows(t)
	code, _, stderr := execute(t, "follow-up", "--ingested", "--rows", rows)
	if code != 0 || len(stub.Checks) != 1 {
		t.Fatalf("exit %d, %d checks: %s", code, len(stub.Checks), stderr)
	}
	check := stub.Checks[0]
	line := "gh-aw stated 7.47 AI credits for this run. Costroid does not convert credits."
	if check.HeadSHA != headCommit || !strings.Contains(check.Summary, line) || strings.Contains(check.Summary, forkCapSentence) {
		t.Errorf("check on %s:\n%s", check.HeadSHA, check.Summary)
	}
	block := checkBlock(t, check.Text)
	validateResult(t, block)
	if !strings.Contains(block, `"quantity": "7.47"`) || !strings.Contains(block, `"list_cost": "0.04974"`) {
		t.Errorf("the object does not state the figure and the credits apart:\n%s", block)
	}
	if verdict := checkVerdict(t, check.Text); verdict.Conclusion != "failure" {
		t.Errorf("a branch's verdict was held at %s", verdict.Conclusion)
	}
	if code, _, stderr := execute(t, "follow-up", "--rows", rows); code != 0 || len(stub.Checks) != 1 || !strings.Contains(stderr, "its own report job posts the check") {
		t.Errorf("without --ingested a branch got a check: exit %d, %d checks: %s", code, len(stub.Checks), stderr)
	}
}

// TestGhAwFollowUpStillCapsAFork holds a fork's gh-aw run at neutral, as every
// fork's check is.
func TestGhAwFollowUpStillCapsAFork(t *testing.T) {
	stub := followUpEnvironment(t, "stranger/widgets")
	stub.Files[configFile] = overBudget + "  enforce: true\n"
	stub.Commits[agentWorkflow] = []time.Time{time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	code, _, stderr := execute(t, "follow-up", "--ingested", "--rows", ingestedRows(t))
	if code != 0 || len(stub.Checks) != 1 || stub.Checks[0].Conclusion != "neutral" || !strings.Contains(stub.Checks[0].Summary, forkCapSentence) {
		t.Fatalf("exit %d, %+v: %s", code, stub.Checks, stderr)
	}
}

// TestGhAwFollowUpNamesTheRefusedArtifact posts the reason a refused usage
// artifact gave, on a branch's pull request.
func TestGhAwFollowUpNamesTheRefusedArtifact(t *testing.T) {
	stub := followUpEnvironment(t, "example/widgets")
	artifact := copyArtifact(t, "")
	if err := os.WriteFile(filepath.Join(artifact, "costroid"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rows := filepath.Join(t.TempDir(), "costroid-rows.jsonl")
	if code, _, _ := execute(t, "ingest-run", "--from", "gh-aw="+artifact, "--out", rows); code != exitFailure {
		t.Fatalf("ingest-run read the artifact: exit %d", code)
	}
	code, _, stderr := execute(t, "follow-up", "--ingested", "--rows", rows)
	if code != 0 || len(stub.Checks) != 1 {
		t.Fatalf("exit %d, %d checks: %s", code, len(stub.Checks), stderr)
	}
	if text := stub.Checks[0].Text; !strings.Contains(text, `"run_reason": "reader_refused"`) || !strings.Contains(text, "which gh-aw v1.2.1 does not write there") {
		t.Errorf("the check does not name the refusal:\n%s", text)
	}
}

// TestGhAwCommentNamesTheProxyCap shows, in the comment's Enforcement cell of
// a gh-aw run, the AI-credit cap gh-aw's proxy held the run to, in place of
// the note that nothing held it. Without aw_info.json the artifact states no
// cap, and the cell reads as any run at grade none.
func TestGhAwCommentNamesTheProxyCap(t *testing.T) {
	clearCI(t)
	const capped = "| none: gh-aw's proxy held this run to 10 AI credits, priced by gh-aw, not by Costroid |"
	const plain = "| none: nothing held the run to a budget |"
	artifact := copyArtifact(t, "")
	for _, stated := range []bool{true, false} {
		if !stated {
			if err := os.Remove(filepath.Join(artifact, "aw_info.json")); err != nil {
				t.Fatal(err)
			}
		}
		out := filepath.Join(t.TempDir(), "costroid-rows.jsonl")
		if code, _, stderr := execute(t, "ingest-run", "--from", "gh-aw="+artifact, "--out", out); code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		code, stdout, stderr := execute(t, "report", "--preview", "--rows", out)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		want, other := capped, plain
		if !stated {
			want, other = plain, capped
		}
		if !strings.Contains(stdout, want) || strings.Contains(stdout, other) {
			t.Errorf("aw_info.json stated %t, and the comment's cell is not %q:\n%s", stated, want, stdout)
		}
	}
}
