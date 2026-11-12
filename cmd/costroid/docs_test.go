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

func TestReaderPagesIncludeTheRecipes(t *testing.T) {
	pages := readerPages(t)
	for _, want := range []string{"README.md", "docs/install.md", "recipes/github-opencode.yml"} {
		if !slices.Contains(pages, filepath.Join(repositoryRoot, filepath.FromSlash(want))) {
			t.Errorf("the pages read lack %s", want)
		}
	}
}
