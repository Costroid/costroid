package main

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	costroid "github.com/Costroid/costroid"
)

// nextCommand is the command a next-step line names.
var nextCommand = regexp.MustCompile(`^Next: costroid (\S+) `)

func TestEveryNextStepIsOneLineAndNamesACommandThatExists(t *testing.T) {
	for _, next := range []string{nextAfterDemo, nextAfterInit, nextAfterValidate} {
		named := nextCommand.FindStringSubmatch(next)
		if named == nil {
			t.Errorf("%q names no command", next)
		} else if _, ok := lookup(named[1]); !ok {
			t.Errorf("%q names %s, which is no command", next, named[1])
		}
	}
	for _, next := range []string{nextAfterDemo, nextAfterInit, nextAfterDoctor, nextAfterValidate} {
		if utf8.RuneCountInString(next) > usageWidth || strings.Contains(next, "\n") || !strings.HasPrefix(next, "Next: ") {
			t.Errorf("%q is not one line of at most %d columns that opens with Next", next, usageWidth)
		}
	}
}

// TestDemoNamesInitAboveTheVerdictSentence holds demo to the verdict sentence
// as its last paragraph, with the next command one blank line above it, and
// to the comment alone under --markdown.
func TestDemoNamesInitAboveTheVerdictSentence(t *testing.T) {
	for _, state := range demoStates {
		code, stdout, stderr := execute(t, "demo", "--state", state)
		if code != 0 {
			t.Fatalf("%s: exit %d: %s", state, code, stderr)
		}
		paragraphs := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n\n")
		if len(paragraphs) < 3 || paragraphs[len(paragraphs)-2] != nextAfterDemo {
			t.Errorf("%s: the next command does not stand above the last paragraph:\n%s", state, stdout)
		}
		if last := paragraphs[len(paragraphs)-1]; strings.Contains(last, "Next:") || strings.Count(stdout, "Next:") != 1 {
			t.Errorf("%s: demo does not end on the verdict sentence alone:\n%s", state, last)
		}
		if _, markdown, _ := execute(t, "demo", "--state", state, "--markdown"); strings.Contains(markdown, "Next:") {
			t.Errorf("%s: the Markdown names a next command", state)
		}
	}
}

func TestValidateNamesPolicyAfterAValidFileAlone(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if code, stdout, _ := execute(t, "validate"); code != 0 || strings.Contains(stdout, "Next:") {
		t.Errorf("no file: exit %d, stdout %q", code, stdout)
	}
	writeFile(t, filepath.Join(dir, "costroid.yml"), "policy:\n  run_budget_usd: \"5\"\n")
	if code, stdout, _ := execute(t, "validate"); code != 0 || stdout != "costroid.yml: valid\n"+nextAfterValidate+"\n" {
		t.Errorf("a valid file: exit %d, stdout %q", code, stdout)
	}
	writeFile(t, filepath.Join(dir, "costroid.yml"), "policy:\n  no_such_key: 1\n")
	if code, stdout, _ := execute(t, "validate"); code != exitRejected || strings.Contains(stdout, "Next:") {
		t.Errorf("a file that breaks the schema: exit %d, stdout %q", code, stdout)
	}
}

func TestInitNamesDoctorLastUnlessItPrintsAPatchOrOpensThePullRequest(t *testing.T) {
	root := t.TempDir()
	code, stdout, stderr := runInit(t, root, releaseBuild, costroid.Recipes, "github", "--agent", "claude")
	if code != 0 || lastLine(stdout) != nextAfterInit || !strings.HasSuffix(stdout, "\n\n"+nextAfterInit+"\n") {
		t.Errorf("exit %d, stderr %q, stdout:\n%s", code, stderr, stdout)
	}
	workflow := workflowDir + "/review.yml"
	writeFile(t, filepath.Join(root, workflow), existingReview)
	code, stdout, stderr = runInit(t, root, releaseBuild, costroid.Recipes, "github", "--existing", workflow)
	if code != 0 || strings.Contains(stdout, "Next:") {
		t.Errorf("--existing: exit %d, stderr %q, and the patch names a next command:\n%s", code, stderr, stdout)
	}
}

// TestDoctorNamesThePullRequestWhenNothingIsLeftToFix holds doctor's next
// step to a run with no fail and no warn: a warn's own fix comes first.
func TestDoctorNamesThePullRequestWhenNothingIsLeftToFix(t *testing.T) {
	root := t.TempDir()
	if code, _, stderr := runInit(t, root, releaseBuild, costroid.Recipes, "github", "--agent", "claude"); code != 0 {
		t.Fatalf("init exited %d: %s", code, stderr)
	}
	code, stdout, stderr := runDoctor(t, root, ghHost("ANTHROPIC_API_KEY\n", ""))
	if code != 0 || lastLine(stdout) != nextAfterDoctor {
		t.Errorf("no fail: exit %d, stderr %q, stdout:\n%s", code, stderr, stdout)
	}
	code, stdout, _ = runDoctor(t, t.TempDir(), ghHost("", ""))
	if code != 0 || strings.Contains(stdout, "Next:") || !strings.HasPrefix(lastLine(stdout), "costroid: doctor: 0 fail, 1 warn, ") {
		t.Errorf("a warn: exit %d, stdout:\n%s", code, stdout)
	}
}
