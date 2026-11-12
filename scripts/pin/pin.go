package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// release is one release of Costroid, as a pin names it.
type release struct {
	version, commit string
}

var (
	versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)
	commitPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	recipePin      = regexp.MustCompile(`Costroid/costroid/action@([0-9a-f]{40}) # v(\S+)`)
	chartVersions  = regexp.MustCompile(`(?m)^(version|appVersion): .*$`)
	composeImage   = regexp.MustCompile(`\$\{COSTROID_IMAGE:-ghcr\.io/costroid/costroid:([^}]*)\}`)
)

func (r release) check() error {
	if !versionPattern.MatchString(r.version) {
		return fmt.Errorf("-version %q is not a release version such as 1.0.0-alpha.24", r.version)
	}
	if !commitPattern.MatchString(r.commit) {
		return fmt.Errorf("-commit %q is not a full 40-hex commit", r.commit)
	}
	return nil
}

const (
	recipesDir  = "recipes"
	chartFile   = "deploy/helm/costroid/Chart.yaml"
	composeFile = "deploy/compose.yaml"
	// schemaFile is read on its $id line alone, which names a tag only when
	// the schema is published at one.
	schemaFile = "costroid.schema.json"
)

// pinnedTrees are read whole, and pinnedFiles alone. Paths are from the
// repository's root.
var (
	pinnedTrees = []string{"docs", "recipes", "marketplace"}
	pinnedFiles = []string{"README.md", "internal/recipelint/testdata/customer/two-block.yml", schemaFile}
	// neverChanged are files a pin leaves alone wherever they stand: the
	// changelog records each release as it was, and the two tests hold
	// versions of their own.
	neverChanged = []string{"CHANGELOG.md", "docs_test.go", "version_test.go"}
)

// packagings are the files that name a release as a default to run, whatever
// release they named before.
var packagings = []struct {
	path string
	set  func(text, version string) (string, int, error)
}{
	{chartFile, setChart},
	{composeFile, setCompose},
}

// summary is what one pin did.
type summary struct {
	from    release
	changed []string
	moved   int
}

// pin moves every reference to the release the recipes pin to target. It
// writes nothing when a sentence refuses the move, and returns refusals.
func pin(root string, target release) (summary, error) {
	from, err := pinnedRelease(root)
	if err != nil {
		return summary{}, err
	}
	result := summary{from: from}
	paths, err := pinnedPaths(root)
	if err != nil {
		return result, err
	}
	var (
		edits   = map[string]string{}
		refused refusals
	)
	for _, name := range paths {
		text, err := readText(root, name)
		if err != nil {
			return result, err
		}
		moved, count, found := move(name, text, from, target)
		refused = append(refused, found...)
		if moved != text {
			edits[name] = moved
			result.moved += count
		}
	}
	for _, packaging := range packagings {
		name := packaging.path
		text, err := readText(root, name)
		if err != nil {
			return result, err
		}
		moved, count, err := packaging.set(text, target.version)
		if err != nil {
			return result, fmt.Errorf("%s: %w", name, err)
		}
		if moved != text {
			edits[name] = moved
			result.moved += count
		}
	}
	if len(refused) > 0 {
		return result, refused
	}
	for name := range edits {
		result.changed = append(result.changed, name)
	}
	sort.Strings(result.changed)
	for _, name := range result.changed {
		if err := writeText(root, name, edits[name]); err != nil {
			return result, err
		}
	}
	return result, stillStanding(root, paths, from, target)
}

// pinnedRelease reads the release every recipe pins, and refuses recipes
// that pin two.
func pinnedRelease(root string) (release, error) {
	names, err := filepath.Glob(filepath.Join(root, recipesDir, "*.yml"))
	if err != nil {
		return release{}, err
	}
	var found []release
	for _, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			return release{}, err
		}
		for _, match := range recipePin.FindAllStringSubmatch(string(data), -1) {
			if r := (release{version: match[2], commit: match[1]}); !slices.Contains(found, r) {
				found = append(found, r)
			}
		}
	}
	switch len(found) {
	case 0:
		return release{}, errors.New("no recipe under recipes/ pins Costroid/costroid/action")
	case 1:
		return found[0], nil
	}
	return release{}, fmt.Errorf("the recipes pin %d releases, %v; they move together", len(found), found)
}

// pinnedPaths lists the files a pin reads, as slash paths from root, leaving
// out every recorded fixture and every file in neverChanged.
func pinnedPaths(root string) ([]string, error) {
	paths := slices.Clone(pinnedFiles)
	for _, tree := range pinnedTrees {
		err := filepath.WalkDir(filepath.Join(root, tree), func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				recorded, err := isRecorded(name)
				if err != nil {
					return err
				}
				if recorded {
					return filepath.SkipDir
				}
				return nil
			}
			relative, err := filepath.Rel(root, name)
			paths = append(paths, filepath.ToSlash(relative))
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	return slices.DeleteFunc(paths, func(name string) bool {
		return slices.Contains(neverChanged, path.Base(name))
	}), nil
}

// isRecorded reports a folder whose manifest.json says it holds a recording,
// which no pin may change.
func isRecorded(dir string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var manifest struct {
		Recorded bool `json:"recorded"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return false, fmt.Errorf("%s: %w", filepath.Join(dir, "manifest.json"), err)
	}
	return manifest.Recorded, nil
}

// move replaces the version and the commit of from with those of to in one
// file's text, and returns the text, the count of references it moved and
// the sentences that refuse a move.
func move(name, text string, from, to release) (string, int, refusals) {
	spans := references(name, text, from, to)
	var refused refusals
	for _, span := range spans {
		if reason := statesHistory(name, text, span.start, span.end); reason != "" {
			refused = append(refused, refusal{path: name, line: lineOf(text, span.start), reason: reason, sentence: sentenceAround(name, text, span.start, span.end)})
		}
	}
	var moved strings.Builder
	last := 0
	for _, span := range spans {
		moved.WriteString(text[last:span.start])
		moved.WriteString(span.to)
		last = span.end
	}
	moved.WriteString(text[last:])
	return moved.String(), len(spans), refused
}

// span is one reference in a file's text and what it becomes.
type span struct {
	start, end int
	to         string
}

// references finds each place the version or the commit of from stands in a
// file, in order, leaving out a part that does not change.
func references(name, text string, from, to release) []span {
	var spans []span
	if from.version != to.version {
		for _, start := range occurrences(text, from.version, versionEdge) {
			spans = append(spans, span{start: start, end: start + len(from.version), to: to.version})
		}
	}
	if from.commit != to.commit {
		for _, start := range occurrences(text, from.commit, commitEdge) {
			spans = append(spans, span{start: start, end: start + len(from.commit), to: to.commit})
		}
	}
	if name == schemaFile {
		spans = slices.DeleteFunc(spans, func(s span) bool {
			return !strings.Contains(lineAt(text, s.start), `"$id"`)
		})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	return spans
}

// occurrences returns the offsets where needle stands whole: edge rejects a
// neighbour that would make it part of a longer version or commit.
func occurrences(text, needle string, edge func(text string, start, end int) bool) []int {
	var found []int
	for offset := 0; ; {
		index := strings.Index(text[offset:], needle)
		if index < 0 {
			return found
		}
		start := offset + index
		end := start + len(needle)
		if edge(text, start, end) {
			found = append(found, start)
		}
		offset = start + 1
	}
}

func isAlphanumeric(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// versionEdge accepts 1.0.0-alpha.2 alone, as in v1.0.0-alpha.2 or
// `1.0.0-alpha.2`. It refuses 1.0.0-alpha.23 and 11.0.0-alpha.2, and accepts
// a full stop that ends a sentence.
func versionEdge(text string, start, end int) bool {
	if start > 0 {
		before := text[start-1]
		if before == '.' || isAlphanumeric(before) && before != 'v' {
			return false
		}
		if before == 'v' && start > 1 && isAlphanumeric(text[start-2]) {
			return false
		}
	}
	if end < len(text) {
		after := text[end]
		if isAlphanumeric(after) {
			return false
		}
		if strings.IndexByte(".-+", after) >= 0 && end+1 < len(text) && isAlphanumeric(text[end+1]) {
			return false
		}
	}
	return true
}

// commitEdge accepts a commit that no other hex digit or letter touches.
func commitEdge(text string, start, end int) bool {
	return (start == 0 || !isAlphanumeric(text[start-1])) && (end == len(text) || !isAlphanumeric(text[end]))
}

// setChart names version as the chart's version and appVersion.
func setChart(text, version string) (string, int, error) {
	if found := len(chartVersions.FindAllString(text, -1)); found != 2 {
		return text, 0, fmt.Errorf("want one version and one appVersion line, found %d", found)
	}
	count := 0
	moved := chartVersions.ReplaceAllStringFunc(text, func(line string) string {
		key, _, _ := strings.Cut(line, ":")
		next := key + ": " + version
		if next != line {
			count++
		}
		return next
	})
	return moved, count, nil
}

// setCompose names version as the image Compose runs when COSTROID_IMAGE is
// unset.
func setCompose(text, version string) (string, int, error) {
	matches := composeImage.FindAllStringSubmatch(text, -1)
	if len(matches) != 1 {
		return text, 0, fmt.Errorf("want one default for COSTROID_IMAGE, found %d", len(matches))
	}
	if matches[0][1] == version {
		return text, 0, nil
	}
	return composeImage.ReplaceAllLiteralString(text, "${COSTROID_IMAGE:-ghcr.io/costroid/costroid:"+version+"}"), 1, nil
}

// stillStanding reads every pinned file back and fails on a reference to the
// old release left in it, or on a chart or Compose file that does not name
// the new one.
func stillStanding(root string, paths []string, from, to release) error {
	var left []string
	for _, name := range paths {
		text, err := readText(root, name)
		if err != nil {
			return err
		}
		for _, s := range references(name, text, from, to) {
			left = append(left, fmt.Sprintf("%s:%d", name, lineOf(text, s.start)))
		}
	}
	for _, packaging := range packagings {
		text, err := readText(root, packaging.path)
		if err != nil {
			return err
		}
		if _, count, err := packaging.set(text, to.version); err != nil || count != 0 {
			left = append(left, packaging.path)
		}
	}
	if len(left) > 0 {
		sort.Strings(left)
		return fmt.Errorf("the old release still stands at %s", strings.Join(left, ", "))
	}
	return nil
}

func readText(root, name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	return string(data), err
}

func writeText(root, name, text string) error {
	full := filepath.Join(root, filepath.FromSlash(name))
	info, err := os.Stat(full)
	if err != nil {
		return err
	}
	return os.WriteFile(full, []byte(text), info.Mode().Perm())
}

func lineOf(text string, offset int) int {
	return strings.Count(text[:offset], "\n") + 1
}

func lineAt(text string, offset int) string {
	start := strings.LastIndexByte(text[:offset], '\n') + 1
	end := strings.IndexByte(text[offset:], '\n')
	if end < 0 {
		return text[start:]
	}
	return text[start : offset+end]
}
