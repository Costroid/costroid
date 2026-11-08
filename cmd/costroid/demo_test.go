package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/comment"
)

const (
	docsExample  = "../../docs/comment-example.md"
	demoRowsFile = "demo/rows.jsonl"
	actionDir    = "../../internal/adapter/claudecode/testdata/2.1.316/action-session"
	// actionSession names the session file the recording's result points to.
	actionSession = "00000000-0000-4000-8000-000000000001"
	demoTotal     = "0.0391529"
)

// TestDemoRendersForATerminal reads the demo as a terminal shows it: the
// comment's first line, its verdict, the cost table and how the cost was
// computed, in plain text with aligned columns, and no machine block.
func TestDemoRendersForATerminal(t *testing.T) {
	code, stdout, stderr := execute(t, "demo")
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, markup := range []string{"|", "**", "`", "<", "#", "](", comment.Marker, "costroid.result", "{"} {
		if strings.Contains(stdout, markup) {
			t.Errorf("the demo prints %q to a terminal:\n%s", markup, stdout)
		}
	}
	lines := strings.Split(stdout, "\n")
	if len(lines) > 45 {
		t.Errorf("the demo prints %d lines", len(lines))
	}
	if want := "Costroid\n\nThis run cost USD " + demoTotal + " at list (Measured). Nothing held it to a budget.\n\nVerdict: neutral."; !strings.HasPrefix(stdout, want) {
		t.Errorf("the demo does not open on the first line and the verdict:\n%s", stdout)
	}
	header := "Cost           Basis     Attribution   Enforcement"
	row := "USD " + demoTotal + "  Measured  unattributed  none: nothing held the run to a budget"
	if !strings.Contains(stdout, "\n"+header+"\n"+row+"\n") {
		t.Errorf("no aligned cost table in\n%s", stdout)
	}
	computed := strings.Index(stdout, "\nHow USD "+demoTotal+" was computed:\n\nCatalog  Token class  ")
	if computed < 0 {
		t.Fatalf("no computation in\n%s", stdout)
	}
	tokens := strings.Index(lines[slices.IndexFunc(lines, func(line string) bool { return strings.HasPrefix(line, "Catalog  ") })], "Tokens")
	for _, line := range lines {
		if strings.Contains(line, ":cache_write_5m") && strings.Index(line, "5959") != tokens {
			t.Errorf("the Tokens column does not line up:\n%s", stdout)
		}
	}
	for _, line := range lines {
		if len(line) > terminalWidth && !strings.Contains(line, "  ") && !strings.Contains(line, "://") {
			t.Errorf("a line of prose passes %d columns: %q", terminalWidth, line)
		}
	}
}

// TestDemoMarkdownKeepsTheComment holds --markdown to the comment report
// posts, machine block included, for every state.
func TestDemoMarkdownKeepsTheComment(t *testing.T) {
	outsideCI(t)
	code, stdout, stderr := execute(t, "demo", "--markdown")
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if want := readFile(t, docsExample); stdout != want {
		t.Errorf("costroid demo --markdown does not print %s:\n%s", docsExample, stdout)
	}
	if !strings.Contains(stdout, "This run cost USD "+demoTotal+" at list (Measured). Nothing held it to a budget.") {
		t.Errorf("the demo does not open on the recorded run's figure:\n%s", stdout)
	}
	for _, state := range demoStates {
		want, err := demoStateComment(context.Background(), state)
		if err != nil {
			t.Fatal(err)
		}
		code, markdown, _ := execute(t, "demo", "--state", state, "--markdown")
		if code != 0 || markdown != want || !strings.HasPrefix(markdown, comment.Marker+"\n") || !strings.Contains(markdown, "Machine block") {
			t.Errorf("--state %s --markdown: exit %d, not the comment:\n%s", state, code, markdown)
		}
		_, plain, _ := execute(t, "demo", "--state", state)
		if plain != forTerminal(want) {
			t.Errorf("--state %s does not render its comment", state)
		}
	}
}

// TestDemoStateShowsEachState reads each state the comment can be in, as a
// terminal shows it and as the check run's object states it.
func TestDemoStateShowsEachState(t *testing.T) {
	for state, want := range map[string][]string{
		demoNeutral: {"Verdict: neutral.", "Nothing held it to a budget.", "No cap held this run", `"conclusion": "neutral"`},
		demoCapped: {
			"This run cost USD 0.0283136 at list (Measured). Held by wrapper-watchdog",
			"Cap: USD 0.001 (wrapper-watchdog: the wrapper stops the process at the cap).\nCost: USD 0.0283136, Measured. Over the cap by USD 0.0273136 and 16960 tokens.",
			`"overshoot": "0.0273136"`,
		},
		demoFailure: {"Verdict: failure. Run budget: USD " + demoTotal + " is over the USD 0.01 budget.", `"conclusion": "failure"`},
		demoSkipped: {"The agent did not run on this pull request", `"agent_skipped": true`},
	} {
		_, plain, _ := execute(t, "demo", "--state", state)
		_, markdown, _ := execute(t, "demo", "--state", state, "--markdown")
		for _, line := range want {
			if !strings.Contains(plain, line) && !strings.Contains(markdown, line) {
				t.Errorf("--state %s shows no %q:\n%s", state, line, plain)
			}
		}
		if cost := strings.Contains(plain, "This run cost"); cost == (state == demoSkipped) {
			t.Errorf("--state %s states a cost: %t", state, cost)
		}
	}
	code, stdout, stderr := execute(t, "demo", "--state", "passed")
	if want := "costroid: --state takes neutral, capped, failure, skipped, not \"passed\"\n"; code != exitFailure || stdout != "" || stderr != want {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

// TestDemoCappedRowsAreTheRecordedCodexStop keeps the capped state's rows
// the ones the comment's goldens read for the recorded Codex stop.
func TestDemoCappedRowsAreTheRecordedCodexStop(t *testing.T) {
	if readFile(t, "../../internal/comment/testdata/codex-stopped.jsonl") != demoCappedRows {
		t.Error("demo/capped.jsonl is not internal/comment/testdata/codex-stopped.jsonl")
	}
}

func TestDemoTakesNoArgument(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := demoCommand(context.Background(), []string{"rows.jsonl"}, &stdout, &stderr); code != exitFailure || stdout.Len() > 0 {
		t.Errorf("exit %d, stdout %q", code, stdout.String())
	}
}

// TestDemoRowsAreWhatIngestRunReadsFromTheRecording writes the demo's rows
// again from the recorded run, so the demo shows no figure the recording does
// not hold.
func TestDemoRowsAreWhatIngestRunReadsFromTheRecording(t *testing.T) {
	outsideCI(t)
	t.Setenv(billingAccountVariable, "")
	t.Setenv(credentialVariable, "")
	config := t.TempDir()
	copyInto(t, filepath.Join(actionDir, "session.jsonl"), filepath.Join(config, "projects", "-work", actionSession+".jsonl"))
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	work := t.TempDir()
	execution := filepath.Join(work, "claude-execution-output.json")
	copyInto(t, filepath.Join(actionDir, "claude-execution-output.json"), execution)
	saved := time.Date(2026, 11, 7, 11, 33, 30, 0, time.UTC)
	if err := os.Chtimes(execution, saved, saved); err != nil {
		t.Fatal(err)
	}
	rows := filepath.Join(work, "rows.jsonl")
	if code, _, stderr := execute(t, "ingest-run", "--execution-file", execution, "--out", rows); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if readFile(t, rows) != demoRows {
		t.Errorf("%s is not what ingest-run reads from %s", demoRowsFile, actionDir)
	}
	ledger, err := comment.ReadRows(strings.NewReader(demoRows))
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Figures) != 1 || ledger.Figures[0].Cost.String() != demoTotal {
		t.Errorf("the demo's rows read %+v, not the run's own total %s", ledger.Figures, demoTotal)
	}
}

func TestPreviewPrintsTheCommentWithoutAHost(t *testing.T) {
	t.Run("outside CI", func(t *testing.T) {
		outsideCI(t)
		t.Setenv(installationVariable, "")
		rows := absolute(t, demoRowsFile)
		t.Chdir(t.TempDir())
		code, stdout, stderr := execute(t, "report", "--preview", "--rows", rows)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		assertPreview(t, stdout)
		for _, want := range []string{
			"No rule applied: `costroid.yml` in this directory sets no budget and no threshold.",
			"This is one run, not a total across runs.",
			"(https://docs.costroid.com/account)",
			"Machine block, the check run was not posted",
		} {
			if !strings.Contains(stdout, want) {
				t.Errorf("no %q in\n%s", want, stdout)
			}
		}
	})
	t.Run("inside GitHub Actions", func(t *testing.T) {
		stub := reportEnvironment(t, pullRequestEvent())
		outputs, summary := stepFiles(t)
		rows := absolute(t, demoRowsFile)
		directory := t.TempDir()
		if err := os.WriteFile(filepath.Join(directory, configFile), []byte(demoConfig), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Chdir(directory)
		code, stdout, stderr := execute(t, "report", "--preview", "--rows", rows, "--installation-id", demoInstallation)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		assertPreview(t, stdout)
		for _, want := range []string{"Run budget: unverified rows cannot satisfy a budget.", "installation=github%3A" + demoInstallation} {
			if !strings.Contains(stdout, want) {
				t.Errorf("no %q in\n%s", want, stdout)
			}
		}
		if len(stub.Requests) > 0 {
			t.Errorf("the preview reached the host: %v", stub.Requests)
		}
		if readFile(t, outputs) != "" || readFile(t, summary) != "" {
			t.Error("the preview wrote the step's outputs or its summary")
		}
	})
}

func assertPreview(t *testing.T, stdout string) {
	t.Helper()
	if !strings.HasPrefix(stdout, comment.Marker+"\n### Costroid\n\nThis run cost USD "+demoTotal+" at list (Measured). Nothing held it to a budget.\n\n**Verdict: neutral.**") {
		t.Errorf("the preview does not open as the comment does:\n%s", stdout)
	}
	if !strings.Contains(stdout, "```json\n{\n  \"schema\": \"costroid.result/0.7\"") {
		t.Errorf("no machine block in\n%s", stdout)
	}
}

func TestPreviewRefusesJSON(t *testing.T) {
	code, stdout, stderr := execute(t, "report", "--preview", "--json", "--rows", demoRowsFile)
	if code != exitFailure || stdout != "" || !strings.Contains(stderr, "--preview or --json") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

// TestPreviewNamesThisDirectory holds the preview to what it read: the
// costroid.yml in the working directory, never the base branch's, and a rows
// file that is there.
func TestPreviewNamesThisDirectory(t *testing.T) {
	outsideCI(t)
	t.Setenv(installationVariable, "")
	rows := absolute(t, demoRowsFile)
	directory := t.TempDir()
	t.Chdir(directory)
	for config, want := range map[string]string{
		"":                "No rule applied: `costroid.yml` in this directory sets no budget and no threshold.",
		"policy: [\n":     "No rule ran, so the check concludes neutral: `costroid.yml` in this directory is not valid",
		demoFailingConfig: "Run budget: USD 0.01, from `policy.run_budget_usd` in `costroid.yml`.",
	} {
		os.Remove(configFile)
		if config != "" {
			if err := os.WriteFile(configFile, []byte(config), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		code, stdout, stderr := execute(t, "report", "--preview", "--rows", rows)
		if code != 0 || !strings.Contains(stdout, want) || strings.Contains(stdout, "base branch's file") || strings.Contains(stdout, "`costroid.yml` on the base branch") {
			t.Errorf("costroid.yml %q: exit %d, no %q, or the base branch's costroid.yml, in\n%s%s", config, code, want, stdout, stderr)
		}
	}

	missing := filepath.Join(directory, "rows.jsonl")
	code, stdout, stderr := execute(t, "report", "--preview", "--rows", missing)
	want := "costroid: preview: no rows file at " + missing + ". costroid run writes one at --out, and costroid demo shows a comment without one\n"
	if code != exitFailure || stdout != "" || stderr != want {
		t.Errorf("a missing rows file: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	code, stdout, stderr = execute(t, "report", "--preview", "--rows", missing, "--agent-result", "skipped")
	if code != 0 || !strings.Contains(stdout, "The agent did not run on this pull request") || strings.Contains(stdout, "job log") {
		t.Errorf("a skipped agent's missing rows file: exit %d:\n%s%s", code, stdout, stderr)
	}
}

// TestPreviewShowsTheOvershoot gives the preview a cap for a run nothing
// held. The comment's text states the cap, the cost and how far over or
// under it the run went, with the figures of its own machine block.
func TestPreviewShowsTheOvershoot(t *testing.T) {
	outsideCI(t)
	rows := absolute(t, demoRowsFile)
	t.Chdir(t.TempDir())
	for budget, want := range map[string]string{
		"0.01": "No cap held this run: its enforcement grade is none. Cap: USD 0.01, which nothing enforced. Cost: USD " + demoTotal + ", Measured. Over the cap by USD 0.0291529 and 36120 tokens.\n",
		"5":    "No cap held this run: its enforcement grade is none. Cap: USD 5, which nothing enforced. Cost: USD " + demoTotal + ", Measured. Under the cap by USD 4.9608471.\n",
	} {
		code, stdout, stderr := execute(t, "report", "--preview", "--rows", rows, "--budget-usd", budget)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		text, block, _ := strings.Cut(stdout, "<summary>Machine block")
		if !strings.Contains(text, "\n"+want) {
			t.Errorf("--budget-usd %s: the comment's text does not state %q:\n%s", budget, want, text)
		}
		if budget == "0.01" && !strings.Contains(block, `"overshoot": "0.0291529"`) {
			t.Errorf("the machine block states another overshoot:\n%s", block)
		}
	}

	capped := filepath.Join(t.TempDir(), "capped.jsonl")
	if err := os.WriteFile(capped, []byte(demoCappedRows), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := execute(t, "report", "--preview", "--rows", capped, "--budget-usd", demoCap)
	if code != 0 || !strings.Contains(stdout, "\nCap: USD 0.001 (wrapper-watchdog: the wrapper stops the process at the cap). Cost: USD 0.0283136, Measured. Over the cap by USD 0.0273136 and 16960 tokens.\n") || strings.Contains(stdout, "nothing enforced") {
		t.Errorf("a held run's cap line changed: exit %d:\n%s%s", code, stdout, stderr)
	}
}

func outsideCI(t *testing.T) {
	t.Helper()
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("GITLAB_CI", "")
}

func absolute(t *testing.T, path string) string {
	t.Helper()
	full, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return full
}
