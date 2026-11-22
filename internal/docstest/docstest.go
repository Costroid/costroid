// Package docstest reads the fenced blocks of the README and of docs/, for
// the tests that hold a block to what a command prints.
package docstest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Place names the blocks of one language under one heading of a page. Page is
// the file from the repository's root, Heading the nearest heading above the
// block without its marks, and Language the word after the fence, empty when
// the fence names none.
type Place struct{ Page, Heading, Language string }

// Block is one fenced block.
type Block struct {
	Place
	// Line is the line the fence opens on.
	Line int
	Body string
}

// Blocks reads every fenced block of README.md and of each page of docs/ in
// the repository at root, in the order the pages hold them.
func Blocks(t testing.TB, root string) []Block {
	t.Helper()
	pages, err := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	var blocks []Block
	for _, path := range append([]string{filepath.Join(root, "README.md")}, pages...) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		page, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatal(err)
		}
		heading, open := "", -1
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			fenced := strings.HasPrefix(strings.TrimSpace(line), "```")
			switch {
			case fenced && open < 0:
				open = i
			case fenced:
				language := strings.TrimPrefix(strings.TrimSpace(lines[open]), "```")
				blocks = append(blocks, Block{Place{filepath.ToSlash(page), heading, language}, open + 1, strings.Join(lines[open+1:i], "\n")})
				open = -1
			case open < 0 && strings.HasPrefix(line, "#"):
				heading = strings.TrimSpace(strings.TrimLeft(line, "#"))
			}
		}
		if open >= 0 {
			t.Fatalf("%s: the fence on line %d never closes", page, open+1)
		}
	}
	return blocks
}

// At answers the body of each block at place, and fails the test when the
// page holds none there.
func At(t testing.TB, root string, place Place) []string {
	t.Helper()
	var bodies []string
	for _, block := range Blocks(t, root) {
		if block.Place == place {
			bodies = append(bodies, block.Body)
		}
	}
	if len(bodies) == 0 {
		t.Fatalf("%s holds no %q block under %q", place.Page, place.Language, place.Heading)
	}
	return bodies
}

// stoodFor is a value a page writes in place of one that differs from run to
// run, such as <flag> or <rows' total>.
var stoodFor = regexp.MustCompile(`<[^<>]+>`)

// Fills reports whether printed is the line a page shows, with some text in
// place of each <placeholder> of the page's line and every other character
// the same.
func Fills(shown, printed string) bool {
	parts := stoodFor.Split(shown, -1)
	for i, part := range parts {
		parts[i] = regexp.QuoteMeta(part)
	}
	return regexp.MustCompile("^" + strings.Join(parts, "(.+)") + "$").MatchString(printed)
}
