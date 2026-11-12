package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	catalogdata "github.com/Costroid/costroid/catalog"
	"go.yaml.in/yaml/v3"
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

// flagName is a long flag as help text and the docs write it.
var flagName = regexp.MustCompile(`--[a-z][a-z0-9-]*[a-z0-9]`)

// TestDocsNameEveryFlagTheHelpNames holds the docs to each command's help:
// every flag the help names, the README or a page of docs/ names as well.
func TestDocsNameEveryFlagTheHelpNames(t *testing.T) {
	named := map[string]bool{}
	for _, page := range slices.Concat([]string{filepath.Join(repositoryRoot, "README.md")}, readerFiles(t, "docs")) {
		for _, flag := range flagName.FindAllString(readFile(t, page), -1) {
			named[flag] = true
		}
	}
	checked := 0
	for _, c := range commands {
		flags := flagName.FindAllString(c.help, -1)
		slices.Sort(flags)
		for _, flag := range slices.Compact(flags) {
			checked++
			if !named[flag] {
				t.Errorf("costroid %s --help names %s, and no page does", c.name, flag)
			}
		}
	}
	if checked < 50 {
		t.Fatalf("read %d flags from the help, too few to be every command's", checked)
	}
}

// readmeSection is the text under a level-two heading of the README, up to
// the next one.
func readmeSection(t *testing.T, heading string) string {
	t.Helper()
	_, section, found := strings.Cut(readFile(t, filepath.Join(repositoryRoot, "README.md")), "\n## "+heading+"\n")
	if !found {
		t.Fatalf("README.md has no section %q", heading)
	}
	section, _, _ = strings.Cut(section, "\n## ")
	return section
}

// TestReadmeFirstCommandsRunWithNoKey runs each command of the README's first
// block, as a reader would, in a repository that holds one recipe and no key.
func TestReadmeFirstCommandsRunWithNoKey(t *testing.T) {
	block := markdownShell.FindStringSubmatch(readmeSection(t, "First commands"))
	if block == nil {
		t.Fatal("the README's first commands have no shell block")
	}
	dir := t.TempDir()
	copyInto(t, filepath.Join(repositoryRoot, "recipes", "github-claude-code-action.yml"), filepath.Join(dir, ".github", "workflows", "costroid.yml"))
	t.Chdir(dir)
	for _, key := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GITHUB_TOKEN"} {
		t.Setenv(key, "")
	}
	ran := 0
	for line := range strings.SplitSeq(strings.TrimSuffix(block[1], "\n"), "\n") {
		command, _, _ := strings.Cut(line, "#")
		args := strings.Fields(command)
		if len(args) == 0 || args[0] != "costroid" {
			t.Fatalf("the block holds %q, which is not a costroid command", line)
		}
		if code, _, stderr := execute(t, args[1:]...); code != 0 {
			t.Errorf("%s exited %d: %s", strings.Join(args, " "), code, stderr)
		}
		ran++
	}
	if ran < 2 {
		t.Errorf("the block runs %d commands, want demo and lint", ran)
	}
}

// TestReadmePastedRecipesNeedNoInstall holds each recipe the README offers
// with no install to what it claims: pasted as .github/workflows/costroid.yml
// it passes lint --strict and lint-recipes, calls Costroid's action at the
// release the recipes pin, and runs no costroid binary of its own.
func TestReadmePastedRecipesNeedNoInstall(t *testing.T) {
	linked := regexp.MustCompile(`\]\(recipes/([a-z0-9-]+\.yml)\)`).FindAllStringSubmatch(readmeSection(t, "Without installing anything"), -1)
	var names []string
	for _, link := range linked {
		names = append(names, link[1])
	}
	if want := []string{"github-claude-code-action.yml", "github-codex-action.yml"}; !slices.Equal(names, want) {
		t.Fatalf("the section offers %v, want %v", names, want)
	}
	pin := pinnedRelease(t)
	runsCostroid := regexp.MustCompile(`(?m)(?:^|[\s;&|(])costroid\s`)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			source := readFile(t, filepath.Join(repositoryRoot, "recipes", name))
			dir := t.TempDir()
			path := filepath.Join(".github", "workflows", "costroid.yml")
			copyInto(t, filepath.Join(repositoryRoot, "recipes", name), filepath.Join(dir, path))
			t.Chdir(dir)
			for _, args := range [][]string{{"lint", "--strict"}, {"lint-recipes", path}} {
				if code, stdout, stderr := execute(t, args...); code != 0 {
					t.Errorf("costroid %s exited %d:\n%s%s", strings.Join(args, " "), code, stdout, stderr)
				}
			}
			pins := recipePin.FindAllStringSubmatch(source, -1)
			if len(pins) == 0 {
				t.Error("the recipe does not call Costroid's action")
			}
			for _, match := range pins {
				if match[1] != pin {
					t.Errorf("the recipe calls Costroid's action at v%s, and the recipes pin v%s", match[1], pin)
				}
			}
			var workflow struct {
				Jobs map[string]struct {
					Steps []struct {
						Run string `yaml:"run"`
					} `yaml:"steps"`
				} `yaml:"jobs"`
			}
			if err := yaml.Unmarshal([]byte(source), &workflow); err != nil {
				t.Fatal(err)
			}
			for job, steps := range workflow.Jobs {
				for _, step := range steps.Steps {
					if runsCostroid.MatchString(step.Run) {
						t.Errorf("job %s runs costroid itself: %q", job, step.Run)
					}
				}
			}
		})
	}
}

// TestCatalogPageNamesTheCatalogTheBinaryCarries holds docs/catalog.md to the
// catalog release this binary embeds: its version, each sheet's file, and
// every day a price in it was read.
func TestCatalogPageNamesTheCatalogTheBinaryCarries(t *testing.T) {
	page := readFile(t, filepath.Join(repositoryRoot, "docs", "catalog.md"))
	if !strings.Contains(page, "Catalog "+catalogdata.Version+" holds ") {
		t.Errorf("docs/catalog.md does not name catalog %s, which the binary carries", catalogdata.Version)
	}
	sheets, err := fs.Glob(catalogdata.Sheets, catalogdata.Version+"/*.json")
	if err != nil || len(sheets) == 0 {
		t.Fatalf("catalog %s holds no sheet: %v", catalogdata.Version, err)
	}
	read := map[string]bool{}
	for _, sheet := range sheets {
		if name := filepath.Base(sheet); !strings.Contains(page, "`"+name+"`") {
			t.Errorf("docs/catalog.md does not name the sheet %s", name)
		}
		data, err := fs.ReadFile(catalogdata.Sheets, sheet)
		if err != nil {
			t.Fatal(err)
		}
		var parsed struct {
			Rows []struct {
				RetrievalDate string `json:"RetrievalDate"`
			} `json:"rows"`
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("%s: %v", sheet, err)
		}
		for _, row := range parsed.Rows {
			read[row.RetrievalDate] = true
		}
	}
	for day := range read {
		if !strings.Contains(page, day) {
			t.Errorf("docs/catalog.md does not name %s, a day a price in catalog %s was read", day, catalogdata.Version)
		}
	}
}

var (
	// indexLine is one line of docs/README.md: a link to a page, then one clause.
	indexLine = regexp.MustCompile("^- \\[`([a-z0-9-]+\\.md)`\\]\\(([a-z0-9-]+\\.md)(?:#[a-z0-9-]+)?\\): (.+)\\.$")
	// relativeLink is a Markdown link's target that is not a URL.
	relativeLink = regexp.MustCompile(`\]\(([^)\s:]+)\)`)
	headingLine  = regexp.MustCompile(`(?m)^#{1,6} +(.+)$`)
	fencedBlock  = regexp.MustCompile("(?s)```.*?```")
	// slugDrops are the characters GitHub leaves out of a heading's anchor.
	slugDrops = regexp.MustCompile(`[^\p{L}\p{N}_\- ]`)
)

// TestDocsIndexLinksEveryPage holds docs/README.md to docs/: each page is on
// one line of its own, a link and one clause, and each line names a page.
func TestDocsIndexLinksEveryPage(t *testing.T) {
	index := readFile(t, filepath.Join(repositoryRoot, "docs", "README.md"))
	linked := map[string]bool{}
	for line := range strings.SplitSeq(index, "\n") {
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		match := indexLine.FindStringSubmatch(line)
		if match == nil || match[1] != match[2] || strings.Contains(match[3], "](") {
			t.Errorf("docs/README.md holds %q, not one link and one clause", line)
			continue
		}
		if linked[match[1]] {
			t.Errorf("docs/README.md links %s twice", match[1])
		}
		linked[match[1]] = true
	}
	pages, err := filepath.Glob(filepath.Join(repositoryRoot, "docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range pages {
		name := filepath.Base(page)
		if name != "README.md" && !linked[name] {
			t.Errorf("docs/README.md does not link %s", name)
		}
		delete(linked, name)
	}
	for name := range linked {
		t.Errorf("docs/README.md links %s, which is not in docs/", name)
	}
	if !strings.Contains(readFile(t, filepath.Join(repositoryRoot, "README.md")), "](docs/README.md)") {
		t.Error("README.md does not link docs/README.md")
	}
}

// anchors are the fragments GitHub gives the headings of a Markdown file.
func anchors(text string) map[string]bool {
	found, seen := map[string]bool{}, map[string]int{}
	for _, heading := range headingLine.FindAllStringSubmatch(fencedBlock.ReplaceAllString(text, ""), -1) {
		slug := strings.ReplaceAll(slugDrops.ReplaceAllString(strings.ToLower(strings.TrimSpace(heading[1])), ""), " ", "-")
		if n := seen[slug]; n > 0 {
			found[slug+"-"+strconv.Itoa(n)] = true
		} else {
			found[slug] = true
		}
		seen[slug]++
	}
	return found
}

// TestDocsRelativeLinksResolve holds every link from the README or a page of
// docs/ to a file of this tree to a file that exists, and a fragment to a
// heading of that file.
func TestDocsRelativeLinksResolve(t *testing.T) {
	checked := 0
	for _, page := range slices.Concat([]string{filepath.Join(repositoryRoot, "README.md")}, readerFiles(t, "docs")) {
		text := readFile(t, page)
		for _, link := range relativeLink.FindAllStringSubmatch(fencedBlock.ReplaceAllString(text, ""), -1) {
			path, fragment, _ := strings.Cut(link[1], "#")
			target, targetText := page, text
			if path != "" {
				target = filepath.Join(filepath.Dir(page), filepath.FromSlash(path))
				data, err := os.ReadFile(target)
				if err != nil {
					if _, statErr := os.Stat(target); statErr == nil {
						continue
					}
					t.Errorf("%s links %s, which does not exist", page, link[1])
					continue
				}
				targetText = string(data)
			}
			checked++
			if fragment != "" && strings.HasSuffix(target, ".md") && !anchors(targetText)[fragment] {
				t.Errorf("%s links %s, and no heading there gives #%s", page, link[1], fragment)
			}
		}
	}
	if checked < 50 {
		t.Fatalf("checked %d links, too few to be the docs'", checked)
	}
}

func TestAnchorsAreGitHubs(t *testing.T) {
	got := anchors("# Read the `costroid/repo_visibility` tag\n## On `openai/codex-action`\n```sh\n# not a heading\n```\n## Two departures from FOCUS 1.4\n## Two departures from FOCUS 1.4\n")
	for _, want := range []string{"read-the-costroidrepo_visibility-tag", "on-openaicodex-action", "two-departures-from-focus-14", "two-departures-from-focus-14-1"} {
		if !got[want] {
			t.Errorf("no anchor %s in %v", want, got)
		}
	}
	if got["not-a-heading"] {
		t.Error("a comment in a code block made an anchor")
	}
}
