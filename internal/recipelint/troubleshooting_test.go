package recipelint

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	repositoryRoot     = "../.."
	troubleshootingDoc = "../../docs/troubleshooting.md"
	// hole stands for any text in a line's shape: a format verb, a shell
	// expansion, or a placeholder such as <error> on the page.
	hole = "\x00"
)

var (
	goLine      = regexp.MustCompile(`"costroid: ((?:[^"\\\n]|\\.)*)"`)
	shellLine   = regexp.MustCompile(`(?m)(?:echo "(?:::warning::)?costroid: |\b(?:fail|warn) ")((?:[^"\\\n]|\\.)*)"`)
	goVerb      = regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z]`)
	shellValue  = regexp.MustCompile(`\$(?:\{[^}]*\}|\([^)]*\)|[A-Za-z_][A-Za-z0-9_]*|[0-9])`)
	pageLine    = regexp.MustCompile("`costroid: ([^`]+)`")
	placeholder = regexp.MustCompile(`<[a-z][a-z -]*>`)
)

// printedShapes reads every line the binary and the action print that starts
// with costroid:, as a shape with a hole wherever a value goes in.
func printedShapes(t *testing.T) (map[string]string, string) {
	t.Helper()
	shapes := map[string]string{}
	var sources strings.Builder
	walk := func(dir string, keep func(string) bool) {
		err := filepath.WalkDir(filepath.Join(repositoryRoot, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !keep(path) {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sources.Write(data)
			pattern, verb := goLine, goVerb
			if strings.HasSuffix(path, ".sh") {
				pattern, verb = shellLine, shellValue
			}
			for _, match := range pattern.FindAllStringSubmatch(string(data), -1) {
				line, _, _ := strings.Cut(match[1], `\n`)
				shapes[verb.ReplaceAllString(line, hole)] = path
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	goSource := func(path string) bool { return strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") }
	walk("cmd", goSource)
	walk("internal", goSource)
	walk("action", func(path string) bool { return strings.HasSuffix(path, ".sh") })
	return shapes, sources.String()
}

func pageLines(t *testing.T) []string {
	t.Helper()
	var lines []string
	for _, match := range pageLine.FindAllStringSubmatch(read(t, troubleshootingDoc), -1) {
		lines = append(lines, match[1])
	}
	if len(lines) == 0 {
		t.Fatal("the page quotes no line")
	}
	return lines
}

func shapePattern(shape string) *regexp.Regexp {
	parts := strings.Split(shape, hole)
	for i, part := range parts {
		parts[i] = regexp.QuoteMeta(part)
	}
	return regexp.MustCompile("^" + strings.Join(parts, "(.+)") + "$")
}

// TestTroubleshootingQuotesOnlyLinesTheCodePrints holds each quoted line to a
// shape the code prints, and what fills each of that shape's holes to text in
// the source, so the page cannot quote a line that was reworded or never existed.
func TestTroubleshootingQuotesOnlyLinesTheCodePrints(t *testing.T) {
	shapes, sources := printedShapes(t)
	held := func(line string) bool {
		for shape := range shapes {
			match := shapePattern(shape).FindStringSubmatch(line)
			if match == nil {
				continue
			}
			found := true
			for _, fill := range match[1:] {
				for _, piece := range placeholder.Split(fill, -1) {
					found = found && (strings.TrimSpace(piece) == "" || strings.Contains(sources, piece))
				}
			}
			if found {
				return true
			}
		}
		return false
	}
	for _, line := range pageLines(t) {
		if !held(line) {
			t.Errorf("the page quotes %q, which no line in the source prints", "costroid: "+line)
		}
	}
}

// TestTroubleshootingListsEveryLineTheCodePrints holds the page to the source
// the other way: each shape of line must have an entry the shape can print.
func TestTroubleshootingListsEveryLineTheCodePrints(t *testing.T) {
	shapes, _ := printedShapes(t)
	if len(shapes) < 20 {
		t.Fatalf("found %d shapes, which is too few to be every line", len(shapes))
	}
	lines := pageLines(t)
	for shape, path := range shapes {
		pattern, covered := shapePattern(shape), false
		for _, line := range lines {
			covered = covered || pattern.MatchString(line)
		}
		if !covered {
			t.Errorf("docs/troubleshooting.md has no entry for %q from %s", "costroid: "+strings.ReplaceAll(shape, hole, "<...>"), path)
		}
	}
}
