package main

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// recipePin is the release a recipe pins Costroid's action to, from the
// comment beside the commit.
var recipePin = regexp.MustCompile(regexp.QuoteMeta(costroidActionPath) + `@[0-9a-f]{40} # v(\S+)`)

// pinnedRelease is the one release every GitHub recipe pins.
func pinnedRelease(t *testing.T) string {
	t.Helper()
	pins := map[string]bool{}
	for _, file := range readerFiles(t, "recipes") {
		for _, match := range recipePin.FindAllStringSubmatch(readFile(t, file), -1) {
			pins[match[1]] = true
		}
	}
	if len(pins) != 1 {
		t.Fatalf("the recipes pin %d releases, want 1: %v", len(pins), pins)
	}
	var pin string
	for release := range pins {
		pin = release
	}
	return pin
}

// readerFiles lists every file a reader opens under dir, a directory of the
// repository, with its path from cmd/costroid.
func readerFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(filepath.Join(repositoryRoot, dir), func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			files = append(files, path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("%s holds no file", dir)
	}
	return files
}

// readerPages are the README, every page of docs/ and every recipe: what a
// reader is told.
func readerPages(t *testing.T) []string {
	t.Helper()
	return slices.Concat([]string{filepath.Join(repositoryRoot, "README.md")}, readerFiles(t, "docs"), readerFiles(t, "recipes"))
}

// commentBreak is a line break inside a YAML comment, or any run of white
// space, so a sentence reads the same however it wraps.
var commentBreak = regexp.MustCompile(`\s*\n\s*(?:#\s*)?|\s+`)

// featureLacks are the sentences that say the release a reader runs lacks a
// feature: a release after the pinned one, or none yet.
func featureLacks(pin string) *regexp.Regexp {
	return regexp.MustCompile("(?i)release after `?v?" + regexp.QuoteMeta(pin) + "|in no release yet")
}

// TestNoPageSaysTheCurrentReleaseLacksAFeature fails on a sentence that sends
// a reader to a release after the one the recipes pin, or says a feature is in
// no release yet. A release pin rewrites the number inside such a sentence, so
// it would stay false across releases.
func TestNoPageSaysTheCurrentReleaseLacksAFeature(t *testing.T) {
	lacks := featureLacks(pinnedRelease(t))
	for _, page := range readerPages(t) {
		text := commentBreak.ReplaceAllString(readFile(t, page), " ")
		for _, found := range lacks.FindAllString(text, -1) {
			t.Errorf("%s says %q, and the recipes pin that release", strings.TrimPrefix(page, repositoryRoot+"/"), found)
		}
	}
}

func TestFeatureLacksReadsAWrappedComment(t *testing.T) {
	text := commentBreak.ReplaceAllString("# It needs a Costroid release\n# after 1.0.0-alpha.9, which has none.\n`--x` is in no release yet.\nA release after 1.0.0-alpha.90.\n", " ")
	got := featureLacks("1.0.0-alpha.9").FindAllString(text, -1)
	if want := []string{"release after 1.0.0-alpha.9", "in no release yet", "release after 1.0.0-alpha.9"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// contractCitation is a version of the row or the tag set a page names, as
// focus-row/v0.5 or tags/v0.4. A git ref such as refs/tags/v1.0.0 is not one.
var contractCitation = regexp.MustCompile(`(refs/)?((?:focus-row|tags)/v[0-9]+\.[0-9]+)\b`)

// historyWords open or mark a sentence that names a past version as history.
var historyWords = regexp.MustCompile(`\b(?:[Uu]p to|[Bb]efore|[Ee]arlier|[Uu]ntil)\b`)

// sentenceEnd ends a sentence of prose, or a table cell.
var sentenceEnd = regexp.MustCompile(`\.\s|\n\n|\|`)

// staleCitations lists each contract version text names that export sends by
// no integration contract, outside a sentence that names it as history.
func staleCitations(text string, sent []string) []string {
	var stale []string
	for _, match := range contractCitation.FindAllStringSubmatchIndex(text, -1) {
		if match[2] >= 0 {
			continue
		}
		version := text[match[4]:match[5]]
		if slices.Contains(sent, version) {
			continue
		}
		start := 0
		if ends := sentenceEnd.FindAllStringIndex(text[:match[0]], -1); len(ends) > 0 {
			start = ends[len(ends)-1][1]
		}
		end := len(text)
		if next := sentenceEnd.FindStringIndex(text[match[1]:]); next != nil {
			end = match[1] + next[0]
		}
		if !historyWords.MatchString(text[start:end]) {
			stale = append(stale, version)
		}
	}
	return stale
}

// TestDocsCiteTheRowContractTheCodeSends holds every focus-row and tags
// version the README and the docs name to one export sends by, read from the
// code's own contracts. A sentence may name another as history.
func TestDocsCiteTheRowContractTheCodeSends(t *testing.T) {
	var sent []string
	for _, version := range exportContracts {
		sent = append(sent, version.FocusRow, version.TagSet)
	}
	cited := 0
	for _, page := range slices.Concat([]string{filepath.Join(repositoryRoot, "README.md")}, readerFiles(t, "docs")) {
		text := readFile(t, page)
		cited += len(contractCitation.FindAllString(text, -1))
		for _, version := range staleCitations(text, sent) {
			t.Errorf("%s names %s, and export sends %v", strings.TrimPrefix(page, repositoryRoot+"/"), version, sent)
		}
	}
	if cited == 0 {
		t.Fatal("no page names a row contract")
	}
	page := readFile(t, filepath.Join(repositoryRoot, "docs", "export.md"))
	_, contract, found := strings.Cut(page, "\n### The row contract\n")
	if !found {
		t.Fatal("docs/export.md has no section The row contract")
	}
	contract, _, _ = strings.Cut(contract, "\n#")
	for _, version := range exportContracts {
		row := "| `integration/v" + version.Version + "` | `" + version.FocusRow + "` | `" + version.TagSet + "` |"
		if !strings.Contains(contract, row) {
			t.Errorf("docs/export.md's row contract lacks the line %q", row)
		}
	}
}

func TestStaleCitationsSkipsHistoryAndGitRefs(t *testing.T) {
	text := "Rows are `focus-row/v0.5`. Up to 1.0.0-alpha.20 they were `focus-row/v0.4`.\n" +
		"| `tags/v0.3` | old |\n" +
		"See refs/tags/v1.0 and `tags/v0.4` and `tags/v0.3`.\n"
	got := staleCitations(text, []string{"focus-row/v0.5", "tags/v0.4"})
	if want := []string{"tags/v0.3", "tags/v0.3"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestReaderPagesIncludeTheRecipes(t *testing.T) {
	pages := readerPages(t)
	for _, want := range []string{"README.md", "docs/install.md", "recipes/github-opencode.yml"} {
		if !slices.Contains(pages, filepath.Join(repositoryRoot, filepath.FromSlash(want))) {
			t.Errorf("the pages read lack %s", want)
		}
	}
}
