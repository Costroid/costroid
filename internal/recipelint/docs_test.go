package recipelint

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
)

const (
	readmePath          = "../../README.md"
	installPage         = "../../docs/install.md"
	listingTemplate     = "../../marketplace/README.md"
	changelogPath       = "../../CHANGELOG.md"
	docsDir             = "../../docs"
	releaseWorkflowFile = "../../.github/workflows/release.yml"
	// fakeBinary stands for the released binary when the install block runs.
	// It prints installedMark.
	installedMark = "the installed costroid ran"
	fakeBinary    = "#!/bin/sh\necho '" + installedMark + "'\n"
	// minuteOfReading is STYLE.md's bar for a README, under a minute of
	// reading, at 220 words a minute.
	minuteOfReading = 220
)

var (
	actionPin     = regexp.MustCompile(regexp.QuoteMeta(costroidAction) + `@[0-9a-f]{40} # v(\S+)`)
	actionCommit  = regexp.MustCompile(regexp.QuoteMeta(costroidAction) + `@([0-9a-f]{40}) # v`)
	actionRef     = regexp.MustCompile("`--action-ref ([0-9a-f]{40})`")
	gitlabVersion = regexp.MustCompile(`(?m)^\s+COSTROID_VERSION: (\S+)$`)
	// releaseNumber is the form every release of Costroid has taken so far.
	// The pin is held to it, so a release of another form fails here first.
	releaseNumber = regexp.MustCompile(`1\.0\.0-alpha\.([0-9]+)`)
	// pastOrExample is the text before a release named as the end of a range
	// that is over, or as an example of the form, never as the one to use.
	pastOrExample    = regexp.MustCompile("(up to|such as) `?v?$")
	changelogHeading = regexp.MustCompile(`^## (\S+) \(([0-9]{4}-[0-9]{2}-[0-9]{2})\)$`)
	shellBlock       = regexp.MustCompile("(?s)```sh\n(.*?)```")
	fencedBlock      = regexp.MustCompile("(?s)```.*?```")
	linkTarget       = regexp.MustCompile(`\]\([^)]*\)`)
	recipeLink       = regexp.MustCompile(`\]\(\.\./recipes/([^)]+)\)`)
	releaseTargets   = regexp.MustCompile(`for target in ([a-z0-9/ ]+); do`)
)

// pendingPages are pages whose release lines other changes own. Each must
// still name another release, so the list only shrinks.
var pendingPages = map[string]string{}

// pinnedRelease is the one release every recipe pins.
func pinnedRelease(t *testing.T) string {
	t.Helper()
	pins := map[string]bool{}
	for _, file := range recipeFiles(t) {
		source := read(t, file)
		for _, match := range actionPin.FindAllStringSubmatch(source, -1) {
			pins[match[1]] = true
		}
		for _, match := range gitlabVersion.FindAllStringSubmatch(source, -1) {
			pins[match[1]] = true
		}
	}
	if len(pins) != 1 {
		t.Fatalf("the recipes pin %d releases, want 1: %v", len(pins), pins)
	}
	var pin string
	for value := range pins {
		pin = value
	}
	if releaseNumber.FindString(pin) != pin {
		t.Fatalf("the recipes pin %s, a form this test does not read: teach releaseNumber the new form", pin)
	}
	return pin
}

// namedRelease is a release a page names as the one to use, and its line.
type namedRelease struct {
	line    int
	release string
}

func releasesNamed(text string) []namedRelease {
	var named []namedRelease
	for index, line := range strings.Split(text, "\n") {
		for _, span := range releaseNumber.FindAllStringIndex(line, -1) {
			start, end := span[0], span[1]
			if start > 0 && strings.ContainsAny(line[start-1:start], "0123456789.") {
				continue
			}
			if end+1 < len(line) && line[end] == '.' && strings.ContainsAny(line[end+1:end+2], "0123456789") {
				continue
			}
			if pastOrExample.MatchString(line[:start]) {
				continue
			}
			named = append(named, namedRelease{index + 1, line[start:end]})
		}
	}
	return named
}

// TestDocsNameOnlyTheRecipesRelease holds every release the README and the
// docs tell a reader to use to the release the recipes pin.
func TestDocsNameOnlyTheRecipesRelease(t *testing.T) {
	pin := pinnedRelease(t)
	pages, err := filepath.Glob(filepath.Join(docsDir, "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	pages = append(pages, readmePath)
	readmeNamesPin := false
	for _, page := range pages {
		name, err := filepath.Rel(repositoryRoot, page)
		if err != nil {
			t.Fatal(err)
		}
		name = filepath.ToSlash(name)
		var stale []string
		for _, named := range releasesNamed(read(t, page)) {
			if named.release != pin {
				stale = append(stale, name+":"+strconv.Itoa(named.line)+" names "+named.release)
			} else if page == readmePath {
				readmeNamesPin = true
			}
		}
		_, pending := pendingPages[name]
		switch {
		case pending && len(stale) == 0:
			t.Errorf("%s names only %s now: remove it from pendingPages", name, pin)
		case pending:
			t.Logf("pending, %s: %v", pendingPages[name], stale)
		default:
			for _, line := range stale {
				t.Errorf("%s, and the recipes pin %s", line, pin)
			}
		}
	}
	if !readmeNamesPin {
		t.Errorf("README.md does not name %s", pin)
	}
}

func TestReleasesNamedSkipsHistoryExamplesAndOtherVersions(t *testing.T) {
	text := "Versions up to 1.0.0-alpha.8 derived it.\n" +
		"Releases up to `1.0.0-alpha.5` wrote it.\n" +
		"A number such as `1.0.0-alpha.3`.\n" +
		"Codex 0.164.0-alpha.11.1 and 21.0.0-alpha.2.\n" +
		"Install v1.0.0-alpha.7 and `1.0.0-alpha.12`.\n"
	got := releasesNamed(text)
	want := []namedRelease{{5, "1.0.0-alpha.7"}, {5, "1.0.0-alpha.12"}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// section is the text under a level-two heading, up to the next one.
func section(t *testing.T, text, heading string) string {
	t.Helper()
	_, after, found := strings.Cut(text, "\n## "+heading+"\n")
	if !found {
		t.Fatalf("no section %q", heading)
	}
	body, _, _ := strings.Cut(after, "\n## ")
	return body
}

// installRun runs a README block in a scratch directory. curl serves the
// release's files, and sudo writes into a bin directory on PATH in place of
// /usr/local/bin.
func installRun(t *testing.T, block, sums string) (int, string, []string) {
	t.Helper()
	s := newShell(t)
	served := filepath.Join(s.root, "release")
	work := filepath.Join(s.root, "work")
	for _, directory := range []string{served, work} {
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(served, releaseAsset), fakeBinary)
	write(t, filepath.Join(served, "SHA256SUMS"), sums)
	s.stub("curl", `for argument in "$@"; do
  case $argument in https://*) url=$argument ;; esac
done
echo "$url" >> "`+s.root+`/urls"
cp "`+served+`/${url##*/}" . || exit 22`)
	s.stub("sudo", `exec "${@//\/usr\/local\/bin/`+s.bin+`}"`)
	command := exec.Command("bash", "-eo", "pipefail", "-c", block)
	command.Dir = work
	command.Env = []string{"PATH=" + s.bin + ":" + os.Getenv("PATH"), "HOME=" + s.root}
	output, err := command.CombinedOutput()
	if err != nil && command.ProcessState == nil {
		t.Fatal(err)
	}
	urls, _ := os.ReadFile(filepath.Join(s.root, "urls"))
	return command.ProcessState.ExitCode(), string(output), strings.Fields(string(urls))
}

// pinnedCommit is the one commit of Costroid's action every GitHub recipe pins.
func pinnedCommit(t *testing.T) string {
	t.Helper()
	commits := map[string]bool{}
	for _, file := range recipeFiles(t) {
		for _, match := range actionCommit.FindAllStringSubmatch(read(t, file), -1) {
			commits[match[1]] = true
		}
	}
	if len(commits) != 1 {
		t.Fatalf("the recipes pin %d commits of %s, want 1: %v", len(commits), costroidAction, commits)
	}
	var commit string
	for value := range commits {
		commit = value
	}
	return commit
}

// TestInstallPageBlockIsComplete runs the install page's first block, so a
// stranger who pastes it ends with costroid on PATH, from the release the
// recipes pin, checked against SHA256SUMS.
func TestInstallPageBlockIsComplete(t *testing.T) {
	pin := pinnedRelease(t)
	page := read(t, installPage)
	binary := section(t, page, "The binary")
	blocks := shellBlock.FindAllStringSubmatch(binary, -1)
	if len(blocks) != 1 {
		t.Fatalf("the binary section has %d shell blocks, want the install", len(blocks))
	}
	block := blocks[0][1]
	sums := sumOf(fakeBinary) + "  " + releaseAsset + "\n" + sumOf("arm") + "  costroid-linux-arm64\n"

	code, output, urls := installRun(t, block, sums)
	if code != 0 || !strings.Contains(output, installedMark) {
		t.Fatalf("the block exited %d without running the installed costroid:\n%s", code, output)
	}
	if len(urls) == 0 {
		t.Fatal("the block downloaded nothing")
	}
	for _, url := range urls {
		if !strings.HasPrefix(url, "https://github.com/Costroid/costroid/releases/download/v"+pin+"/") {
			t.Errorf("the block downloads %s, not from the release the recipes pin, %s", url, pin)
		}
	}

	tampered := sumOf("other bytes") + "  " + releaseAsset + "\n"
	if code, output, _ := installRun(t, block, tampered); code == 0 || strings.Contains(output, installedMark) {
		t.Errorf("the block installed a binary that does not match SHA256SUMS, exit %d:\n%s", code, output)
	}

	if !strings.Contains(section(t, page, "The signature"), "release.yml@refs/tags/v"+pin+" ") {
		t.Errorf("the signature check does not name the tag v%s", pin)
	}
	if !strings.Contains(binary, "prints `costroid "+pin+", catalog ") {
		t.Errorf("the binary section does not say what costroid version prints for %s", pin)
	}
	if !strings.Contains(binary, "`shasum -a 256 --check`") {
		t.Error("the binary section does not give macOS's checksum command")
	}
	match := releaseTargets.FindStringSubmatch(read(t, releaseWorkflowFile))
	if match == nil {
		t.Fatal("release.yml lists no targets")
	}
	for _, target := range strings.Fields(match[1]) {
		if asset := "costroid-" + strings.ReplaceAll(target, "/", "-"); !strings.Contains(binary, "`"+asset+"`") {
			t.Errorf("the binary section does not name %s, which the release builds", asset)
		}
	}

	goInstall := section(t, page, "With Go")
	if want := "go install github.com/Costroid/costroid/cmd/costroid@v" + pin + "\n"; !strings.Contains(goInstall, want) {
		t.Errorf("the Go section does not install v%s with %q", pin, want)
	}
	refs := actionRef.FindAllStringSubmatch(goInstall, -1)
	if len(refs) != 1 || refs[0][1] != pinnedCommit(t) {
		t.Errorf("the Go section names --action-ref %v, want the commit the recipes pin, %s", refs, pinnedCommit(t))
	}
}

// readingWords counts the words a reader reads: those outside fenced blocks,
// which are copied rather than read, with link targets left out.
func readingWords(text string) int {
	text = linkTarget.ReplaceAllString(fencedBlock.ReplaceAllString(text, " "), "]")
	words := 0
	for _, field := range strings.Fields(text) {
		if strings.ContainsFunc(field, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
			words++
		}
	}
	return words
}

// TestReadmeReadsInAMinute holds the README to STYLE.md's minute. The licence
// is a legal statement, not reading toward the first command, so it is not
// counted. The README leads with the path for a workflow a team already runs,
// then init --pr, then the comment previewed on the reader's own machine.
func TestReadmeReadsInAMinute(t *testing.T) {
	readme := read(t, readmePath)
	body, _, found := strings.Cut(readme, "\n## Licence\n")
	if !found {
		t.Fatal("README.md has no Licence section")
	}
	if words := readingWords(body); words > minuteOfReading {
		t.Errorf("README.md reads %d words outside its code and its licence, past a minute at %d", words, minuteOfReading)
	}
	at := 0
	for _, step := range []string{"costroid init github --existing ", "`--pr`", "costroid report --preview --rows rows.jsonl\n"} {
		next := strings.Index(readme[at:], step)
		if next < 0 {
			t.Fatalf("README.md does not name %q after byte %d: it leads with --existing, then --pr, then report --preview", step, at)
		}
		at += next + len(step)
	}
	if !strings.Contains(section(t, readme, "First command"), "Claude Code installed and signed in") {
		t.Error("the first command does not say it needs Claude Code installed and signed in")
	}
}

func TestReadingWordsSkipsCodeAndLinkTargets(t *testing.T) {
	text := "One [two](https://example.com/three four) `five`.\n\n```sh\nsix seven\n```\n\n- eight, 9 -- ...\n"
	if got := readingWords(text); got != 5 {
		t.Errorf("got %d words, want 5: One, two, five, eight, 9", got)
	}
}

// TestDocsNameEveryShippedRecipe holds docs/recipes.md to recipes/: each file
// a release ships, and init can write, is linked from the page that lists them.
func TestDocsNameEveryShippedRecipe(t *testing.T) {
	linked := map[string]bool{}
	for _, match := range recipeLink.FindAllStringSubmatch(read(t, recipesDoc), -1) {
		linked[match[1]] = true
	}
	files := recipeFiles(t)
	if len(files) == 0 {
		t.Fatal("recipes/ holds no recipe")
	}
	for _, file := range files {
		if name := filepath.Base(file); !linked[name] {
			t.Errorf("docs/recipes.md does not link recipes/%s", name)
		}
	}
	for name := range linked {
		if _, err := os.Stat(filepath.Join(recipesDir, name)); err != nil {
			t.Errorf("docs/recipes.md links recipes/%s, which does not exist", name)
		}
	}
}

// TestListingNamesItsReleaseFirst holds the wrapper's README to name the
// release and its commit before its first section, where a reader of the
// Marketplace page sees them. scripts/marketplace.sh fills both in.
func TestListingNamesItsReleaseFirst(t *testing.T) {
	top, _, found := strings.Cut(read(t, listingTemplate), "\n## ")
	if !found {
		t.Fatal("marketplace/README.md has no section")
	}
	for _, placeholder := range []string{"__RELEASE_TAG__", "__RELEASE_COMMIT__"} {
		if !strings.Contains(top, placeholder) {
			t.Errorf("marketplace/README.md does not name %s before its first section", placeholder)
		}
	}
}

type changelogEntry struct {
	version string
	date    string
	number  int
}

func changelogEntries(t *testing.T) []changelogEntry {
	t.Helper()
	var entries []changelogEntry
	for _, line := range strings.Split(read(t, changelogPath), "\n") {
		if !strings.HasPrefix(line, "## ") {
			continue
		}
		match := changelogHeading.FindStringSubmatch(line)
		if match == nil {
			t.Fatalf("the heading %q is not ## <release> (<date>)", line)
		}
		if _, err := time.Parse(time.DateOnly, match[2]); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		number := releaseNumber.FindStringSubmatch(match[1])
		if number == nil || number[0] != match[1] {
			t.Fatalf("%q names a release of a form this test does not read", line)
		}
		n, _ := strconv.Atoi(number[1])
		entries = append(entries, changelogEntry{version: match[1], date: match[2], number: n})
	}
	return entries
}

// tagDates reads each release tag's date, in UTC. A checkout without tags,
// such as a shallow clone, returns none.
func tagDates(t *testing.T) map[string]string {
	t.Helper()
	command := exec.Command("git", "-C", repositoryRoot, "tag", "--list", "v*", "--format=%(refname:strip=2) %(creatordate:unix)")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git tag: %v", err)
	}
	dates := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		tag, stamp, found := strings.Cut(line, " ")
		if !found || releaseNumber.FindString(tag) != strings.TrimPrefix(tag, "v") {
			continue
		}
		seconds, err := strconv.ParseInt(stamp, 10, 64)
		if err != nil {
			t.Fatalf("tag %s: %v", tag, err)
		}
		dates[strings.TrimPrefix(tag, "v")] = time.Unix(seconds, 0).UTC().Format(time.DateOnly)
	}
	return dates
}

// TestChangelogMatchesTags holds CHANGELOG.md to the release tags: one entry
// per tag, newest first, each dated by its tag in UTC. The newest entry may
// precede its tag, for the release being prepared.
func TestChangelogMatchesTags(t *testing.T) {
	entries := changelogEntries(t)
	if len(entries) == 0 {
		t.Fatal("CHANGELOG.md has no release")
	}
	for i, entry := range entries {
		if want := len(entries) - i; entry.number != want {
			t.Errorf("entry %d is %s, want alpha.%d: newest first, from alpha.1, none missing", i+1, entry.version, want)
		}
		if i > 0 && entry.date > entries[i-1].date {
			t.Errorf("%s is dated %s, after the newer %s", entry.version, entry.date, entries[i-1].version)
		}
	}
	tags := tagDates(t)
	if len(tags) == 0 {
		t.Skip("this checkout holds no release tags")
	}
	newestTag := 0
	for version := range tags {
		n, _ := strconv.Atoi(releaseNumber.FindStringSubmatch(version)[1])
		newestTag = max(newestTag, n)
	}
	listed := map[string]bool{}
	for i, entry := range entries {
		listed[entry.version] = true
		date, tagged := tags[entry.version]
		switch {
		case !tagged && i == 0 && entry.number == newestTag+1:
			t.Logf("%s has no tag yet", entry.version)
		case !tagged:
			t.Errorf("%s has no tag", entry.version)
		case date != entry.date:
			t.Errorf("%s is dated %s, and its tag %s", entry.version, entry.date, date)
		}
	}
	for version := range tags {
		if !listed[version] {
			t.Errorf("the tag v%s has no entry", version)
		}
	}
}
