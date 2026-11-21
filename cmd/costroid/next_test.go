package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	costroid "github.com/Costroid/costroid"
)

// nextSteps lists every line a command ends on that names what to do next:
// the command that prints it, and the command line of costroid's it names.
// doctor's names the pull request, which no command of costroid's opens.
var nextSteps = []struct {
	line, after string
	names       []string
}{
	{nextAfterDemo, "demo", []string{"init", "github", "--agent", "claude"}},
	{nextAfterInit, "init", []string{"doctor"}},
	{nextAfterDoctor, "doctor", nil},
	{nextAfterValidate, "validate", []string{"policy", "--rows", "rows.jsonl"}},
}

// nextLiterals reads every string the commands' source holds that opens a
// next step.
func nextLiterals(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if literal, ok := node.(*ast.BasicLit); ok && literal.Kind == token.STRING {
				if text, err := strconv.Unquote(literal.Value); err == nil && strings.Contains(text, "Next:") {
					lines = append(lines, text)
				}
			}
			return true
		})
	}
	return lines
}

// TestEveryNextStepIsOneLineAndNamesACommandThatExists walks every next step
// the source holds. Each is one line of at most 80 columns. The command line
// one names is run, on a machine with no program on PATH and in a directory
// with nothing in it, far enough to know that the command exists and reads
// every word of it: it does not end as a wrong command line does. No command
// that prints a next step takes --json, so none prints one into JSON.
func TestEveryNextStepIsOneLineAndNamesACommandThatExists(t *testing.T) {
	clearCI(t)
	var walked []string
	for _, next := range nextSteps {
		walked = append(walked, next.line)
	}
	if printed := nextLiterals(t); !slices.Equal(slices.Sorted(slices.Values(printed)), slices.Sorted(slices.Values(walked))) {
		t.Fatalf("the source holds the next steps %q, and the walk holds %q", printed, walked)
	}
	t.Setenv("PATH", t.TempDir())
	t.Chdir(t.TempDir())
	for _, next := range nextSteps {
		if utf8.RuneCountInString(next.line) > usageWidth || strings.Contains(next.line, "\n") || !strings.HasPrefix(next.line, "Next: ") {
			t.Errorf("%q is not one line of at most %d columns that opens with Next", next.line, usageWidth)
		}
		if code, _, stderr := execute(t, next.after, "--json"); code != exitUsage || !strings.Contains(stderr, "--json is not a flag of costroid "+next.after) {
			t.Errorf("%s takes --json, and nothing holds its next step out of the JSON: exit %d, stderr %q", next.after, code, stderr)
		}
		if next.names == nil {
			continue
		}
		if !strings.HasPrefix(next.line, "Next: costroid "+strings.Join(next.names, " ")+" ") {
			t.Errorf("%q does not name costroid %s", next.line, strings.Join(next.names, " "))
		}
		if _, ok := lookup(next.names[0]); !ok {
			t.Errorf("%q names %s, which is no command", next.line, next.names[0])
		}
		if code, _, stderr := execute(t, next.names...); code == exitUsage || strings.Contains(stderr, "Run costroid help") {
			t.Errorf("costroid %s, which %q names, is a wrong command line: exit %d, stderr %q", strings.Join(next.names, " "), next.line, code, stderr)
		}
	}
}

// TestDemoNamesACommandThatWritesTheWorkflow types the command demo's next
// step names, as it is printed, in a repository git has just made, with no
// remote. It exits 0 and writes the workflow and costroid.yml.
func TestDemoNamesACommandThatWritesTheWorkflow(t *testing.T) {
	code, stdout, stderr := execute(t, "demo")
	if code != 0 {
		t.Fatalf("demo exited %d: %s", code, stderr)
	}
	named, _, found := strings.Cut(strings.TrimPrefix(lineWith(stdout, "Next: "), "Next: costroid "), " writes ")
	if !found {
		t.Fatalf("demo names no command that writes:\n%s", stdout)
	}
	typed := strings.Fields(named)
	if typed[0] != "init" {
		t.Fatalf("demo names costroid %s", named)
	}
	root := gitRepository(t)
	t.Chdir(root)
	var out, refused bytes.Buffer
	if code := initRepository(locateRepository, releaseBuild, costroid.Recipes, testPR, typed[1:], &out, &refused); code != 0 {
		t.Fatalf("costroid %s exited %d in a repository with no remote:\n%s", named, code, refused.String())
	}
	for _, name := range []string{initOut[githubHost], configFile} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Errorf("costroid %s did not write %s: %v", named, name, err)
		}
	}
	troubleshootingLists(t, nextAfterDemo)
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
// step to a run with no fail and no warn: a warn's own fix comes first. The
// repository init has just written reaches it on a machine with no agent
// installed, once gh lists the secret.
func TestDoctorNamesThePullRequestWhenNothingIsLeftToFix(t *testing.T) {
	root := t.TempDir()
	if code, _, stderr := runInit(t, root, releaseBuild, costroid.Recipes, "github", "--agent", "claude"); code != 0 {
		t.Fatalf("init exited %d: %s", code, stderr)
	}
	code, stdout, stderr := runDoctor(t, root, ghHost("ANTHROPIC_API_KEY\n", ""))
	if code != 0 || lastLine(stdout) != nextAfterDoctor {
		t.Errorf("no fail: exit %d, stderr %q, stdout:\n%s", code, stderr, stdout)
	}
	noAgent := ghHost("ANTHROPIC_API_KEY\n", "")
	noAgent.programs = []string{"gh"}
	code, stdout, stderr = runDoctor(t, root, noAgent)
	if code != 0 || lastLine(stdout) != nextAfterDoctor || !strings.Contains(stdout, "costroid: doctor: 0 fail, 0 warn, ") {
		t.Errorf("no agent on this machine: exit %d, stderr %q, stdout:\n%s", code, stderr, stdout)
	}
	code, stdout, _ = runDoctor(t, t.TempDir(), ghHost("", ""))
	if code != 0 || strings.Contains(stdout, "Next:") || !strings.HasPrefix(lastLine(stdout), "costroid: doctor: 0 fail, 1 warn, ") {
		t.Errorf("a warn: exit %d, stdout:\n%s", code, stdout)
	}
}
