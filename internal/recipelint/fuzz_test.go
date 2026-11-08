package recipelint

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode"
)

// seedWorkflows adds every shipped recipe, the action and the customer
// workflows the tests hold, then the YAML shapes that once broke parsers.
func seedWorkflows(f *testing.F) {
	f.Helper()
	paths := []string{filepath.Join("..", "..", "action", "action.yml")}
	for _, pattern := range []string{
		filepath.Join("..", "..", "recipes", "*.yml"),
		filepath.Join("testdata", "customer", "*.yml"),
	} {
		found, err := filepath.Glob(pattern)
		if err != nil {
			f.Fatal(err)
		}
		paths = append(paths, found...)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	for _, document := range []string{
		"", "---\n", "--- \n...\n---\n", "a: &a [*a, *a]\nb: *a\n", "on: push\njobs: [\n", "jobs: {}\non: {}\n", "runs: ~\n", "- 1\n", "on:\njobs:\r 0:",
	} {
		f.Add([]byte(document))
	}
}

// yamlBreaks are the line breaks the YAML parser counts lines by.
var yamlBreaks = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\u0085", "\n", "\u2028", "\n", "\u2029", "\n")

// assertFindings holds a report to what a reader of the lint's output needs:
// every finding names the file, a line in it and a rule, on one line that
// holds no control character.
func assertFindings(t *testing.T, path string, data []byte, report Report) {
	t.Helper()
	lines := strings.Count(yamlBreaks.Replace(string(data)), "\n") + 1
	for _, finding := range append(append([]Finding{}, report.Findings...), report.Notes...) {
		if finding.Path != path || finding.Rule == "" || finding.Line < 1 || finding.Line > lines {
			t.Fatalf("a finding at %s:%d of a file of %d lines, rule %q", finding.Path, finding.Line, lines, finding.Rule)
		}
		if line := finding.String(); strings.ContainsFunc(line, unicode.IsControl) || strings.ContainsAny(line, "\u2028\u2029") {
			t.Fatalf("a finding spans lines or holds a control character: %q", line)
		}
	}
}

// FuzzLintBytes lints a recipe or an action. Every finding names a line of
// the file, and the same bytes always give the same report.
func FuzzLintBytes(f *testing.F) {
	seedWorkflows(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		const path = "recipes/fuzz.yml"
		report := LintBytes(path, data)
		assertFindings(t, path, data, report)
		if again := LintBytes(path, data); !reflect.DeepEqual(again, report) {
			t.Fatalf("two lints of the same bytes differ: %+v and %+v", report, again)
		}
	})
}

// FuzzLintCustomer lints a team's own workflow. Every finding names a line of
// the file, a workflow with no agent step says nothing, and the agents counted
// are at most the documents.
func FuzzLintCustomer(f *testing.F) {
	seedWorkflows(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		const path = ".github/workflows/fuzz.yml"
		report := LintCustomer(path, data)
		assertFindings(t, path, data, report)
		if report.Agents < 0 || report.Agents > strings.Count(string(data), "jobs")+1 {
			t.Fatalf("%d agents counted in a file that names jobs %d times", report.Agents, strings.Count(string(data), "jobs"))
		}
		for _, finding := range report.Findings {
			if finding.Severity == "" {
				t.Fatalf("a customer finding with no severity: %+v", finding)
			}
		}
	})
}
