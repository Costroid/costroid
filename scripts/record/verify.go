package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const (
	manifestName = "manifest.json"
	readmeName   = "README.md"

	agentClaudeCode = "claude-code"
	agentCodex      = "codex"
	agentGhAw       = "gh-aw"
)

// manifest marks a directory as a recording and says what it records.
type manifest struct {
	Recorded bool   `json:"recorded"`
	Agent    string `json:"agent"`
	// Host names the API whose answers a recording holds, in place of an
	// agent.
	Host     string `json:"host,omitempty"`
	Version  string `json:"version"`
	Scenario string `json:"scenario,omitempty"`
	Run      string `json:"run,omitempty"`
	Date     string `json:"date,omitempty"`
	Costroid string `json:"costroid,omitempty"`
}

var (
	safeWord = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,79}$`)
	safeDate = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
)

func readManifest(dir string) (manifest, error) {
	var m manifest
	data, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return m, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return m, fmt.Errorf("%s: %w", manifestName, err)
	}
	return m, nil
}

func writeManifest(dir string, m manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, manifestName), append(data, '\n'), 0o644)
}

// problems lists every value of the manifest that is not a plain word.
func (m manifest) problems() []string {
	var found []string
	if !m.Recorded {
		found = append(found, "recorded is not true")
	}
	switch {
	case m.Host == "":
		if _, err := agentFor(m.Agent); err != nil {
			found = append(found, "agent is not one of "+agentList(agents, "or"))
		}
	case m.Host != hostGitHub || m.Agent != "":
		found = append(found, "host is not github, or names an agent beside it")
	}
	for name, word := range map[string]string{"version": m.Version, "scenario": m.Scenario, "run": m.Run, "costroid": m.Costroid} {
		if (word != "" || name == "version") && !safeWord.MatchString(word) {
			found = append(found, name+" is not a plain word")
		}
	}
	if m.Date != "" && !safeDate.MatchString(m.Date) {
		found = append(found, "date is not YYYY-MM-DD")
	}
	return found
}

// forbidden names each pattern verify refuses anywhere in a recording.
var forbidden = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"an Anthropic key", regexp.MustCompile(`sk-ant-`)},
	{"an API key", regexp.MustCompile(`(^|[^A-Za-z0-9])sk-`)},
	{"a GitHub token", regexp.MustCompile(`ghp_|ghs_|github_pat_`)},
	{"a home directory", regexp.MustCompile(`/home/|/Users/`)},
	{"a runner's workspace", regexp.MustCompile(`/runner/work/`)},
	{"a temporary directory", regexp.MustCompile(`/tmp/`)},
}

// verifyDirectory lists every reason the recording in dir is not fit to
// commit. It reads every file under dir; an empty list means it is.
func verifyDirectory(dir string, prompts []string) ([]string, error) {
	needles := promptNeedles(prompts)
	// A recording no readable manifest marks is held to Claude Code's and
	// Codex's list; the manifest's own problems are noted when it is reached.
	recorded, _ := readManifest(dir)
	list := allowlistOf(recorded.Agent)
	var found []string
	note := func(name, problem string) { found = append(found, name+": "+problem) }
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		name, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		name = filepath.ToSlash(name)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, problem := range scanText(string(data), needles) {
			note(name, problem)
		}
		switch {
		case name == readmeName:
		case name == manifestName:
			m, err := readManifest(dir)
			if err != nil {
				note(name, err.Error())
			}
			for _, problem := range m.problems() {
				note(name, problem)
			}
		default:
			file, err := parseFile(name, data)
			if err != nil {
				note(name, err.Error())
				return nil
			}
			if file.unread > 0 {
				note(name, fmt.Sprintf("%d lines are not JSON", file.unread))
			}
			for i, line := range file.lines {
				for _, problem := range verifyLine(line, list, needles) {
					note(fmt.Sprintf("%s:%d", name, i+1), problem)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(dir, manifestName)); errors.Is(err, fs.ErrNotExist) && !legacyRecording(dir) {
		note(filepath.Base(dir), "holds no "+manifestName+" that marks it a recording")
	}
	return found, nil
}

// scanText names each forbidden pattern and each prompt the text holds,
// never the match itself.
func scanText(text string, needles []string) []string {
	var found []string
	for _, rule := range forbidden {
		if rule.pattern.MatchString(text) {
			found = append(found, "holds "+rule.name)
		}
	}
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			found = append(found, "holds the run's prompt")
			break
		}
	}
	return found
}

// verifyLine checks one line's keys against the allowlist, its ids against
// their synthetic forms and its decoded strings against the prompt.
func verifyLine(line *value, list *allowlist, needles []string) []string {
	if line.kind != kindObject {
		return []string{"is not a JSON object"}
	}
	var found []string
	var walk func(v *value, path string)
	check := func(v *value, path string) {
		if v.kind == kindString {
			for _, problem := range scanText(v.text, needles) {
				found = append(found, printable(path)+" "+problem)
			}
		}
		if _, ok := list.placeholders[path]; ok {
			if v.kind != kindString && v.raw != "null" {
				found = append(found, path+" is neither a string nor null")
			}
			return
		}
		if kind, ok := list.ids[path]; ok {
			text, isID := kind.text(v)
			if isID && !kind.fixture.MatchString(text) || kind.numeric && v.kind == kindString {
				found = append(found, path+" is not a synthetic "+kind.name+" id")
			}
			return
		}
		if root, ok := list.embedded[path]; ok {
			object, isObject := fencedObject(v.text)
			switch {
			case isObject:
				walk(object, root)
			case v.kind != kindString || v.text != placeholderContent:
				found = append(found, path+" is not a fenced result object")
			}
			return
		}
		if list.kept[path] {
			walk(v, path)
			return
		}
		if v.isNumber() && countUnderUsage(path) {
			return
		}
		found = append(found, "key outside the allowlist: "+printable(path))
	}
	walk = func(v *value, path string) {
		switch v.kind {
		case kindObject:
			for i, key := range v.keys {
				p := child(path, key)
				if strings.HasSuffix(p, "*") && !modelName.MatchString(key) {
					found = append(found, "a model name that is not a plain name under "+printable(path))
					continue
				}
				check(v.items[i], p)
			}
		case kindArray:
			for _, item := range v.items {
				check(item, path+"[]")
			}
		case kindString:
			if list.containers[path] && v.text != placeholderContent {
				found = append(found, "content at "+printable(path))
			}
		}
	}
	walk(line, "")
	return slices.Compact(found)
}

// legacyRecording says whether dir is one of the recordings committed before
// manifests: testdata/<version>/<scenario> beside an adapter.
func legacyRecording(dir string) bool {
	scenario := filepath.Base(dir)
	version := filepath.Base(filepath.Dir(dir))
	return filepath.Base(filepath.Dir(filepath.Dir(dir))) == "testdata" &&
		versionDirectory.MatchString(version) && safeWord.MatchString(scenario)
}

var versionDirectory = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([.+-][A-Za-z0-9.+-]*)?$`)
