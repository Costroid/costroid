package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// secretPattern finds what verify refuses as a credential.
var secretPattern = regexp.MustCompile(`(^|[^A-Za-z0-9])(sk-ant-|sk-|ghp_|ghs_|github_pat_)[A-Za-z0-9_-]*`)

// pathPattern finds an absolute path on a runner or a machine.
var pathPattern = regexp.MustCompile(`(/home/|/Users/|/runner/work/|/tmp/)[^\s"']*`)

// minimumPromptLine is the shortest line of a prompt that is held out on its own.
const minimumPromptLine = 20

// recordingFile is one file of a run, parsed.
type recordingFile struct {
	name   string // relative to the run's directory, with forward slashes
	pretty bool   // one indented JSON document rather than JSON Lines
	array  bool   // that document is an array of lines
	lines  []*value
	// unread counts lines that were not JSON.
	unread int
}

// scrubReport says what the scrub removed, by key path and count, never by value.
type scrubReport struct {
	dropped  map[string]int
	unread   map[string]int
	replaced int
}

func (r scrubReport) print(w func(string)) {
	for _, name := range sortedKeys(r.unread) {
		w(fmt.Sprintf("record: %s: dropped %d lines that are not JSON objects", name, r.unread[name]))
	}
	for _, path := range sortedKeys(r.dropped) {
		w(fmt.Sprintf("record: dropped %s outside the allowlist (%d)", printable(path), r.dropped[path]))
	}
	if r.replaced > 0 {
		w(fmt.Sprintf("record: replaced %d values with placeholders", r.replaced))
	}
}

var printablePath = regexp.MustCompile(`^[A-Za-z0-9_.\[\]*-]{1,120}$`)

// printable keeps a key path out of a log when it could hold more than a name.
func printable(path string) string {
	if printablePath.MatchString(path) {
		return path
	}
	return "(a key that is not a plain name)"
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// readRecording parses every data file under dir. It skips manifest.json and
// README.md, and refuses any other kind of file.
func readRecording(dir string) ([]*recordingFile, error) {
	var files []*recordingFile
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		name, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		name = filepath.ToSlash(name)
		if name == manifestName || name == readmeName {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", name)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file, err := parseFile(name, data)
		if err != nil {
			return err
		}
		files = append(files, file)
		return nil
	})
	return files, err
}

func parseFile(name string, data []byte) (*recordingFile, error) {
	switch filepath.Ext(name) {
	case ".jsonl":
		file := &recordingFile{name: name}
		for _, line := range bytes.Split(data, []byte("\n")) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			parsed, err := parseValue(line)
			if err != nil {
				file.unread++
				continue
			}
			file.lines = append(file.lines, parsed)
		}
		return file, nil
	case ".json":
		parsed, err := parseValue(data)
		if err != nil {
			return nil, fmt.Errorf("%s is not JSON: %w", name, err)
		}
		if parsed.kind == kindArray {
			return &recordingFile{name: name, pretty: true, array: true, lines: parsed.items}, nil
		}
		return &recordingFile{name: name, pretty: true, lines: []*value{parsed}}, nil
	}
	return nil, fmt.Errorf("%s is not a .jsonl or .json file", name)
}

func (f *recordingFile) encode() []byte {
	var out bytes.Buffer
	if !f.pretty {
		for _, line := range f.lines {
			line.compact(&out)
			out.WriteByte('\n')
		}
		return out.Bytes()
	}
	document := &value{kind: kindArray, items: f.lines}
	if !f.array && len(f.lines) == 1 {
		document = f.lines[0]
	}
	document.indent(&out, 0)
	out.WriteByte('\n')
	return out.Bytes()
}

// scrubber cuts one run's files to the allowlist. One scrubber serves every
// file of a run, so an id maps to the same synthetic id in each.
type scrubber struct {
	prompts []string
	ids     map[*idKind]*idMap
	report  scrubReport
}

type idMap struct {
	raw  map[string]string
	next int
}

func newScrubber(prompts []string) *scrubber {
	return &scrubber{
		prompts: promptNeedles(prompts),
		ids:     map[*idKind]*idMap{},
		report:  scrubReport{dropped: map[string]int{}, unread: map[string]int{}},
	}
}

// promptNeedles is each prompt, trimmed, and each of its lines long enough to
// stand on its own.
func promptNeedles(prompts []string) []string {
	var needles []string
	for _, prompt := range prompts {
		prompt = strings.TrimSpace(prompt)
		if prompt == "" {
			continue
		}
		needles = append(needles, prompt)
		for _, line := range strings.Split(prompt, "\n") {
			line = strings.TrimSpace(line)
			if len(line) >= minimumPromptLine && line != prompt {
				needles = append(needles, line)
			}
		}
	}
	slices.SortFunc(needles, func(a, b string) int { return len(b) - len(a) })
	return slices.Compact(needles)
}

// reserve notes every id already in its synthetic form, so a new id never
// takes a number one of them holds.
func (s *scrubber) reserve(v *value, path string) {
	switch v.kind {
	case kindObject:
		for i, key := range v.keys {
			p := child(path, key)
			if kind, ok := idPaths[p]; ok && v.items[i].kind == kindString {
				if match := kind.fixture.FindStringSubmatch(v.items[i].text); match != nil {
					n, _ := strconv.Atoi(match[1])
					ids := s.idMap(kind)
					ids.raw[v.items[i].text] = v.items[i].text
					ids.next = max(ids.next, n+1)
				}
			}
			s.reserve(v.items[i], p)
		}
	case kindArray:
		for _, item := range v.items {
			s.reserve(item, path+"[]")
		}
	}
}

func (s *scrubber) idMap(kind *idKind) *idMap {
	ids, ok := s.ids[kind]
	if !ok {
		ids = &idMap{raw: map[string]string{}, next: 1}
		s.ids[kind] = ids
	}
	return ids
}

func (s *scrubber) mapID(kind *idKind, raw string) string {
	ids := s.idMap(kind)
	if synthetic, ok := ids.raw[raw]; ok {
		return synthetic
	}
	synthetic := fmt.Sprintf(kind.format, ids.next)
	ids.next++
	ids.raw[raw] = synthetic
	return synthetic
}

// scrubFiles cuts every file in place, in name order.
func (s *scrubber) scrubFiles(files []*recordingFile) {
	slices.SortFunc(files, func(a, b *recordingFile) int { return strings.Compare(a.name, b.name) })
	for _, file := range files {
		for _, line := range file.lines {
			s.reserve(line, "")
		}
	}
	for _, file := range files {
		if file.unread > 0 {
			s.report.unread[file.name] = file.unread
		}
		kept := file.lines[:0]
		for _, line := range file.lines {
			if line.kind != kindObject {
				s.report.unread[file.name]++
				continue
			}
			kept = append(kept, s.scrub(line, ""))
		}
		file.lines = kept
	}
}

// scrub returns v cut to the allowlist.
func (s *scrubber) scrub(v *value, path string) *value {
	switch v.kind {
	case kindObject:
		out := &value{kind: kindObject}
		for i, key := range v.keys {
			p := child(path, key)
			if strings.HasSuffix(p, "*") && !modelName.MatchString(key) {
				s.report.dropped[p]++
				continue
			}
			if item := s.member(v.items[i], p); item != nil {
				out.keys = append(out.keys, key)
				out.items = append(out.items, item)
			}
		}
		return out
	case kindArray:
		out := &value{kind: kindArray}
		for _, item := range v.items {
			if kept := s.member(item, path+"[]"); kept != nil {
				out.items = append(out.items, kept)
			}
		}
		return out
	}
	return s.scalar(v, path)
}

// member decides one key's or element's fate by its path.
func (s *scrubber) member(v *value, path string) *value {
	if placeholder, ok := placeholders[path]; ok {
		if v.kind == kindLiteral && v.raw == "null" {
			return v
		}
		s.report.replaced++
		return stringValue(placeholder)
	}
	if kind, ok := idPaths[path]; ok {
		if v.kind != kindString {
			return v
		}
		return stringValue(s.mapID(kind, v.text))
	}
	if allowed.kept[path] {
		return s.scrub(v, path)
	}
	if v.isNumber() && countUnderUsage(path) {
		return v
	}
	s.report.dropped[path]++
	return nil
}

func (s *scrubber) scalar(v *value, path string) *value {
	if v.kind != kindString {
		return v
	}
	if allowed.containers[path] {
		s.report.replaced++
		return stringValue(placeholderContent)
	}
	text := v.text
	for _, needle := range s.prompts {
		text = strings.ReplaceAll(text, needle, placeholderPrompt)
	}
	text = pathPattern.ReplaceAllString(text, placeholderPath)
	text = secretPattern.ReplaceAllString(text, "${1}"+placeholderSecret)
	if text == v.text {
		return v
	}
	s.report.replaced++
	return stringValue(text)
}

// scrubDirectory cuts the run in raw to the allowlist and writes it to out,
// which must not exist yet. Nothing is written when a file cannot be read.
func scrubDirectory(raw, out string, prompts []string) (scrubReport, error) {
	files, err := readRecording(raw)
	if err != nil {
		return scrubReport{}, err
	}
	if len(files) == 0 {
		return scrubReport{}, fmt.Errorf("%s holds no recording", raw)
	}
	manifest, err := readManifest(raw)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return scrubReport{}, err
	}
	s := newScrubber(prompts)
	s.scrubFiles(files)
	if manifest.Version == "" {
		manifest.Version = agentVersion(files)
	}
	if manifest.Agent == "" {
		manifest.Agent = agentOf(files)
	}
	manifest.Recorded = true
	if err := os.Mkdir(out, 0o755); err != nil {
		return scrubReport{}, err
	}
	for _, file := range files {
		target := filepath.Join(out, filepath.FromSlash(file.name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return scrubReport{}, err
		}
		if err := os.WriteFile(target, file.encode(), 0o644); err != nil {
			return scrubReport{}, err
		}
	}
	return s.report, writeManifest(out, manifest)
}

// agentVersion reads the agent's own version from the files: Claude Code's
// claude_code_version or version, or Codex's cli_version.
func agentVersion(files []*recordingFile) string {
	for _, file := range files {
		for _, line := range file.lines {
			for _, version := range []string{
				line.fieldText("claude_code_version"),
				line.field("payload").fieldText("cli_version"),
				line.fieldText("version"),
			} {
				if version != "" {
					return version
				}
			}
		}
	}
	return ""
}

func agentOf(files []*recordingFile) string {
	for _, file := range files {
		for _, line := range file.lines {
			if line.fieldText("claude_code_version") != "" || line.fieldText("sessionId") != "" {
				return agentClaudeCode
			}
			if line.fieldText("type") == "session_meta" || line.fieldText("type") == "thread.started" {
				return agentCodex
			}
		}
	}
	return ""
}
