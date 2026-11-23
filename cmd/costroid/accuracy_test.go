package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/accuracy"
)

// builtLine is how the accuracy line opens and closes for a run of Claude
// Code on Anthropic, with the counts of the record this build carries.
func builtLine(t *testing.T) (string, string) {
	t.Helper()
	count := accuracy.Built.Count("claude-code", "anthropic")
	if count.Ran == 0 {
		t.Fatal("the record this build carries holds no day of claude-code on Anthropic")
	}
	opening := fmt.Sprintf("Costroid's own test, not this run: `claude-code` %s on Anthropic passed %d of the %d days it ran, %s to %s (UTC)",
		strings.Join(count.Versions, " and "), count.Passed, count.Ran, count.First, count.Last)
	return opening, " Nothing has compared this run with a provider's report.\n\n</details>"
}

// TestTheReportGivesTheCommentTheRecord runs report on a pull request, report
// --preview and demo on the measured rows of a recorded Claude Code run. The comment, the check and the
// job summary each end their folded details on the line the record this
// build carries gives, and a job summary that folds nothing ends on it.
func TestTheReportGivesTheCommentTheRecord(t *testing.T) {
	opening, closing := builtLine(t)
	holds := func(surface, text string) {
		t.Helper()
		at := strings.Index(text, opening)
		if at < 0 || !strings.Contains(text[at:], closing) || strings.Count(text, opening) != 1 {
			t.Errorf("the %s does not end its fold on the record's line:\n%s", surface, text)
		}
	}

	rows := absolute(t, demoRowsFile)
	stub := reportEnvironment(t, pullRequestEvent())
	_, summary := stepFiles(t)
	if code, _, stderr := execute(t, "report", "--rows", rows, "--budget-usd", "5"); code != 0 || len(stub.Comments) != 1 || len(stub.Checks) != 1 {
		t.Fatalf("exit %d, %d comments, %d checks: %s", code, len(stub.Comments), len(stub.Checks), stderr)
	}
	holds("comment", stub.Comments[0].Body)
	holds("check", stub.Checks[0].Summary)
	text := readFile(t, summary)
	ends := strings.HasSuffix(text, closing+"\n") || strings.HasSuffix(text, strings.TrimSuffix(closing, "\n\n</details>")+"\n")
	if strings.Count(text, opening) != 1 || !ends {
		t.Errorf("the job summary does not end on the record's line:\n%s", text)
	}

	outsideCI(t)
	t.Chdir(t.TempDir())
	code, preview, stderr := execute(t, "report", "--preview", "--rows", rows)
	if code != 0 {
		t.Fatalf("report --preview: exit %d: %s", code, stderr)
	}
	holds("preview", preview)
	_, demo, _ := execute(t, "demo", "--markdown")
	holds("demo", demo)
}

// TestAForksFollowUpCarriesTheAccuracyLine runs follow-up for a fork's pull
// request on the measured rows of a recorded Claude Code run. Its check and
// its job summary end their folded details on the record's line, as every
// other report's do.
func TestAForksFollowUpCarriesTheAccuracyLine(t *testing.T) {
	opening, closing := builtLine(t)
	stub := followUpEnvironment(t, "stranger/widgets")
	stub.SetPrivate(true)
	_, summary := stepFiles(t)
	code, _, stderr := execute(t, "follow-up", "--rows", absolute(t, demoRowsFile))
	if code != 0 || len(stub.Checks) != 1 {
		t.Fatalf("exit %d, %d checks: %s", code, len(stub.Checks), stderr)
	}
	for surface, text := range map[string]string{"check": stub.Checks[0].Summary, "job summary": readFile(t, summary)} {
		at := strings.Index(text, opening)
		if at < 0 || !strings.Contains(text[at:], closing) || strings.Count(text, opening) != 1 {
			t.Errorf("the follow-up's %s does not end its fold on the record's line:\n%s", surface, text)
		}
	}
}
