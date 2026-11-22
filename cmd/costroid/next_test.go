package main

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	costroid "github.com/Costroid/costroid"
	"github.com/Costroid/costroid/internal/cli"
)

// nextSteps lists every line a command ends on that names what to do next,
// and the command that prints it. names is the command line of costroid's it
// names, left the one of another program that init leaves to the reader, and
// page the page of the documentation it names where no command does the act:
// no command opens a merge request.
var nextSteps = []struct {
	line, after string
	names, left []string
	page        string
}{
	{line: nextAfterDemo, after: "demo", names: []string{"init", "github", "--agent", "claude"}},
	{line: nextAfterInit, after: "init", names: []string{"doctor"}},
	{line: nextAfterDoctor, after: "doctor", left: []string{"gh", "pr", "create", "--fill"}},
	{line: nextAfterDoctorOnGitLab, after: "doctor", page: "recipes#gitlab-ci"},
	{line: nextAfterValidate, after: "validate", names: []string{"policy", "--rows", "FILE"}},
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

// leftByInit are the commands init leaves to the reader on GitHub, as its
// closing screen lists them.
func leftByInit(t *testing.T) []string {
	t.Helper()
	code, _, said := runInit(t, t.TempDir(), releaseBuild, costroid.Recipes, "github", "--agent", "claude")
	_, listed, found := strings.Cut(said, "These commands are left, and init ran none of them:\n")
	if code != 0 || !found {
		t.Fatalf("init exited %d and listed no commands:\n%s", code, said)
	}
	listed, _, _ = strings.Cut(listed, "\n\n")
	var commands []string
	for line := range strings.SplitSeq(strings.TrimSuffix(listed, "\n"), "\n") {
		commands = append(commands, strings.TrimSpace(line))
	}
	return commands
}

// TestEveryNextStepIsOneLineAndNamesACommandThatExists walks every next step
// the source holds. Each is one line of at most 80 columns. A command line of
// costroid's that one names is run, on a machine with no program on PATH and
// in a directory with nothing in it, far enough to know that the command
// exists and reads every word of it: it does not end as a wrong command line
// does. Where the command's help calls a flag's value FILE, the line writes
// FILE, never a name: the reader may hold no such file yet. A command of
// another program is one init leaves to the reader, word for word. Only where
// no command does the act does a line name a page, and the documentation
// holds that page and its heading.
func TestEveryNextStepIsOneLineAndNamesACommandThatExists(t *testing.T) {
	clearCI(t)
	left := leftByInit(t)
	printed := nextLiterals(t)
	if len(printed) != len(nextSteps) {
		t.Fatalf("the source holds the next steps %q, and the walk holds %d", printed, len(nextSteps))
	}
	t.Setenv("PATH", t.TempDir())
	t.Chdir(t.TempDir())
	for _, next := range nextSteps {
		if !slices.ContainsFunc(printed, func(literal string) bool { return strings.HasPrefix(next.line, literal) }) {
			t.Errorf("the source does not hold %q", next.line)
		}
		if utf8.RuneCountInString(next.line) > usageWidth || strings.Contains(next.line, "\n") || !strings.HasPrefix(next.line, "Next: ") {
			t.Errorf("%q is not one line of at most %d columns that opens with Next", next.line, usageWidth)
		}
		switch {
		case next.names != nil:
			if !strings.HasPrefix(next.line, "Next: costroid "+strings.Join(next.names, " ")+" ") {
				t.Errorf("%q does not name costroid %s", next.line, strings.Join(next.names, " "))
			}
			entry, ok := lookup(next.names[0])
			if !ok {
				t.Errorf("%q names %s, which is no command", next.line, next.names[0])
			}
			for i, word := range next.names[:len(next.names)-1] {
				takesAFile := regexp.MustCompile(`(?m)^ +` + regexp.QuoteMeta(word) + ` FILE  `).MatchString(entry.help)
				if strings.HasPrefix(word, "-") && takesAFile && next.names[i+1] != "FILE" {
					t.Errorf("%q names the file %s, which the reader may not have: costroid help %s calls it FILE", next.line, next.names[i+1], next.names[0])
				}
			}
			if code, _, stderr := execute(t, next.names...); code == exitUsage || strings.Contains(stderr, "Run costroid help") {
				t.Errorf("costroid %s, which %q names, is a wrong command line: exit %d, stderr %q", strings.Join(next.names, " "), next.line, code, stderr)
			}
		case next.left != nil:
			command := strings.Join(next.left, " ")
			if !strings.HasPrefix(next.line, "Next: "+command+" ") || !slices.Contains(left, command) {
				t.Errorf("%q does not name %s, or init leaves no such command: %q", next.line, command, left)
			}
		default:
			name, anchor, _ := strings.Cut(next.page, "#")
			page := readFile(t, filepath.Join(filepath.Dir(troubleshootingPage), name+".md"))
			heading := regexp.MustCompile(`(?mi)^#+ ` + strings.ReplaceAll(regexp.QuoteMeta(anchor), "-", "[ -]") + `$`)
			if !strings.Contains(next.line, cli.Documentation+"/"+next.page+" ") || !heading.MatchString(page) {
				t.Errorf("%q does not name %s/%s, or docs/%s.md has no such heading", next.line, cli.Documentation, next.page, name)
			}
			if strings.Contains(next.line, "costroid ") {
				t.Errorf("%q names a page and a command", next.line)
			}
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
	named, _, found := strings.Cut(strings.TrimPrefix(lineWith(stderr, "Next: "), "Next: costroid "), " writes ")
	if !found || strings.Contains(stdout, "Next:") {
		t.Fatalf("demo names no command that writes on stderr alone:\n%s", stderr)
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

// executeOnOneStream runs a command line with stdout and stderr as one
// stream, as a terminal shows them: each write in the order it was made.
func executeOnOneStream(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var shown bytes.Buffer
	code := dispatch(context.Background(), args, strings.NewReader(""), &shown, &shown)
	return code, shown.String()
}

// TestDemoNamesInitAboveTheVerdictSentence holds what a terminal shows of
// demo to the verdict sentence as its last paragraph, with the next command
// one blank line above it. The next command is on stderr, so stdout holds the
// comment and the sentence, and under --markdown the comment alone.
func TestDemoNamesInitAboveTheVerdictSentence(t *testing.T) {
	for _, state := range demoStates {
		code, shown := executeOnOneStream(t, "demo", "--state", state)
		if code != 0 {
			t.Fatalf("%s: exit %d: %s", state, code, shown)
		}
		paragraphs := strings.Split(strings.TrimSuffix(shown, "\n"), "\n\n")
		if len(paragraphs) < 3 || paragraphs[len(paragraphs)-2] != nextAfterDemo {
			t.Errorf("%s: the next command does not stand above the last paragraph:\n%s", state, shown)
		}
		if last := paragraphs[len(paragraphs)-1]; strings.Contains(last, "Next:") || strings.Count(shown, "Next:") != 1 {
			t.Errorf("%s: demo does not end on the verdict sentence alone:\n%s", state, last)
		}
		_, stdout, stderr := execute(t, "demo", "--state", state)
		if stderr != "\n"+nextAfterDemo+"\n" || stdout != strings.Replace(shown, "\n"+nextAfterDemo+"\n", "", 1) {
			t.Errorf("%s: stderr does not hold the next command alone, and stdout the rest: %q", state, stderr)
		}
		if _, markdown, said := execute(t, "demo", "--state", state, "--markdown"); strings.Contains(markdown+said, "Next:") {
			t.Errorf("%s: the Markdown names a next command", state)
		}
	}
}

func TestValidateNamesPolicyAfterAValidFileAlone(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if code, stdout, stderr := execute(t, "validate"); code != 0 || strings.Contains(stdout+stderr, "Next:") {
		t.Errorf("no file: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	writeFile(t, filepath.Join(dir, "costroid.yml"), "policy:\n  run_budget_usd: \"5\"\n")
	if code, stdout, stderr := execute(t, "validate"); code != 0 || stdout != "costroid.yml: valid\n" || stderr != nextAfterValidate+"\n" {
		t.Errorf("a valid file: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	writeFile(t, filepath.Join(dir, "costroid.yml"), "policy:\n  no_such_key: 1\n")
	if code, stdout, stderr := execute(t, "validate"); code != exitRejected || strings.Contains(stdout+stderr, "Next:") {
		t.Errorf("a file that breaks the schema: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestInitNamesDoctorLastUnlessItPrintsAPatchOrOpensThePullRequest(t *testing.T) {
	root := t.TempDir()
	code, stdout, stderr := runInit(t, root, releaseBuild, costroid.Recipes, "github", "--agent", "claude")
	if code != 0 || stdout != "" || lastLine(stderr) != nextAfterInit || !strings.HasSuffix(stderr, "\n\n"+nextAfterInit+"\n") {
		t.Errorf("exit %d, stdout %q, stderr:\n%s", code, stdout, stderr)
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
// installed, once gh lists the secret. The line is on stderr, and names the
// command that opens the pull request, or on GitLab the page, since no
// command opens a merge request.
func TestDoctorNamesThePullRequestWhenNothingIsLeftToFix(t *testing.T) {
	root := t.TempDir()
	if code, _, stderr := runInit(t, root, releaseBuild, costroid.Recipes, "github", "--agent", "claude"); code != 0 {
		t.Fatalf("init exited %d: %s", code, stderr)
	}
	code, stdout, stderr := runDoctor(t, root, ghHost("ANTHROPIC_API_KEY\n", ""))
	if code != 0 || stderr != nextAfterDoctor+"\n" || strings.Contains(stdout, "Next:") {
		t.Errorf("no fail: exit %d, stderr %q, stdout:\n%s", code, stderr, stdout)
	}
	noAgent := ghHost("ANTHROPIC_API_KEY\n", "")
	noAgent.programs = []string{"gh"}
	code, stdout, stderr = runDoctor(t, root, noAgent)
	if code != 0 || stderr != nextAfterDoctor+"\n" || !strings.HasPrefix(lastLine(stdout), "costroid: doctor: 0 fail, 0 warn, ") {
		t.Errorf("no agent on this machine: exit %d, stderr %q, stdout:\n%s", code, stderr, stdout)
	}
	code, stdout, stderr = runDoctor(t, t.TempDir(), ghHost("", ""))
	if code != 0 || strings.Contains(stdout+stderr, "Next:") || !strings.HasPrefix(lastLine(stdout), "costroid: doctor: 0 fail, 1 warn, ") {
		t.Errorf("a warn: exit %d, stderr %q, stdout:\n%s", code, stderr, stdout)
	}
	project := t.TempDir()
	if code, _, stderr := runInit(t, project, releaseBuild, costroid.Recipes, "gitlab", "--agent", "claude"); code != 0 {
		t.Fatalf("init gitlab exited %d: %s", code, stderr)
	}
	code, stdout, stderr = runDoctor(t, project, ghHost("", ""))
	if code != 0 || stderr != nextAfterDoctorOnGitLab+"\n" || strings.Contains(stdout, "Next:") {
		t.Errorf("on GitLab: exit %d, stderr %q, stdout:\n%s", code, stderr, stdout)
	}
	troubleshootingLists(t, nextAfterDoctor, nextAfterDoctorOnGitLab)
}
