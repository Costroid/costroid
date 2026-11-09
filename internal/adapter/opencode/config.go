package opencode

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

// environment is where a run reads OpenCode's config: its variables, its
// working directory and the user's home.
type environment struct {
	getenv func(string) string
	dir    string
	home   string
}

// model is the provider and model a run's steps are priced at.
type model struct {
	provider string
	id       string
	// fromConfig says the model came from OpenCode's config, not --model.
	fromConfig bool
}

// resolveModel picks the model as OpenCode does for a new session with no
// agent model: --model, then the model key of its config. The provider must
// be one the catalog prices.
func resolveModel(flag string, env environment) (model, error) {
	chosen, fromConfig := flag, false
	if chosen == "" {
		configured, err := configModel(env)
		if err != nil {
			return model{}, err
		}
		chosen, fromConfig = configured, true
	}
	if chosen == "" {
		return model{}, errors.New("opencode: neither --model nor OpenCode's config names a model, and the stream names none; pass --model provider/model")
	}
	name, id, _ := strings.Cut(chosen, "/")
	if _, priced := providers[name]; !priced || id == "" {
		return model{}, fmt.Errorf("opencode: the model %q is not provider/model on a provider the catalog prices: costroid prices %s", chosen, providerNames())
	}
	return model{provider: name, id: id, fromConfig: fromConfig}, nil
}

// configSource is one place OpenCode reads config from: a file, or the text
// of a variable.
type configSource struct {
	path string
	text string
	// inline marks a source whose text is given, not read from path.
	inline bool
}

// configModel is the model key the last of OpenCode's config sources sets,
// or "" when none does. OpenCode 1.19.4 merges its sources in this order,
// each over the ones before: its global directory's config.json,
// opencode.json and opencode.jsonc; OPENCODE_CONFIG; each opencode.json and
// opencode.jsonc from the repository's root down to the working directory;
// opencode.json and opencode.jsonc in each .opencode directory and in
// OPENCODE_CONFIG_DIR; then OPENCODE_CONFIG_CONTENT. An organization's
// remote config and a managed config directory are not read.
func configModel(env environment) (string, error) {
	chosen := ""
	for _, source := range configSources(env) {
		text := source.text
		if !source.inline {
			data, err := os.ReadFile(source.path)
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
				continue
			}
			if err != nil {
				return "", fmt.Errorf("opencode: read %s: %w", source.path, err)
			}
			text = string(data)
		}
		value, err := modelKey(substitute(text, env.getenv))
		if err != nil {
			return "", fmt.Errorf("opencode: %s: %w", source.name(), err)
		}
		if value != "" {
			chosen = value
		}
	}
	return chosen, nil
}

func (s configSource) name() string {
	if s.inline {
		return s.path
	}
	return "the config " + s.path
}

func configSources(env environment) []configSource {
	files := func(dir string, names ...string) []configSource {
		var found []configSource
		for _, name := range names {
			found = append(found, configSource{path: filepath.Join(dir, name)})
		}
		return found
	}
	global := filepath.Join(env.home, ".config", "opencode")
	if base := env.getenv("XDG_CONFIG_HOME"); base != "" {
		global = filepath.Join(base, "opencode")
	}
	sources := files(global, "config.json", "opencode.json", "opencode.jsonc")
	if path := env.getenv("OPENCODE_CONFIG"); path != "" {
		sources = append(sources, configSource{path: path})
	}
	project := !truthy(env.getenv("OPENCODE_DISABLE_PROJECT_CONFIG"))
	ancestors := upTo(env.dir, worktree(env.dir))
	if project {
		for _, dir := range slices.Backward(ancestors) {
			sources = append(sources, files(dir, "opencode.json", "opencode.jsonc")...)
		}
	}
	directories := []string{global}
	if project {
		for _, dir := range ancestors {
			directories = append(directories, filepath.Join(dir, ".opencode"))
		}
	}
	directories = append(directories, filepath.Join(env.home, ".opencode"))
	configDir := env.getenv("OPENCODE_CONFIG_DIR")
	if configDir != "" {
		directories = append(directories, configDir)
	}
	var seen []string
	for _, dir := range directories {
		if slices.Contains(seen, dir) || !isDir(dir) {
			continue
		}
		seen = append(seen, dir)
		if strings.HasSuffix(dir, ".opencode") || dir == configDir {
			sources = append(sources, files(dir, "opencode.json", "opencode.jsonc")...)
		}
	}
	if content := env.getenv("OPENCODE_CONFIG_CONTENT"); content != "" {
		sources = append(sources, configSource{path: "OPENCODE_CONFIG_CONTENT", text: content, inline: true})
	}
	return sources
}

// truthy reads a flag variable as OpenCode does: true or 1, in any case.
func truthy(value string) bool {
	value = strings.ToLower(value)
	return value == "true" || value == "1"
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// worktree is the root of the repository that holds dir: the nearest
// directory with a .git entry, or the file system's root outside one.
func worktree(dir string) string {
	for current := dir; ; current = filepath.Dir(current) {
		if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil {
			return current
		}
		if filepath.Dir(current) == current {
			return current
		}
	}
}

// upTo lists dir and each directory above it up to stop, nearest first.
func upTo(dir, stop string) []string {
	var found []string
	for current := dir; current != ""; current = filepath.Dir(current) {
		found = append(found, current)
		if current == stop || filepath.Dir(current) == current {
			break
		}
	}
	return found
}

var envReference = regexp.MustCompile(`\{env:([^}]+)\}`)

// substitute replaces each {env:NAME} with the variable's value, as OpenCode
// does before it parses a config.
func substitute(text string, getenv func(string) string) string {
	return envReference.ReplaceAllStringFunc(text, func(reference string) string {
		return getenv(envReference.FindStringSubmatch(reference)[1])
	})
}

// modelKey reads the top-level model of a config in JSON with comments and
// trailing commas. A model set through {file:} is refused, since its text is
// another file's.
func modelKey(text string) (string, error) {
	var config struct {
		Model json.RawMessage `json:"model"`
	}
	plain := plainJSON(text)
	if len(bytes.TrimSpace(plain)) == 0 {
		return "", nil
	}
	if err := json.Unmarshal(plain, &config); err != nil {
		return "", fmt.Errorf("not JSON costroid can read: %w", err)
	}
	if len(config.Model) == 0 || string(config.Model) == "null" {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(config.Model, &value); err != nil {
		return "", errors.New("model is not a string")
	}
	if strings.Contains(value, "{file:") {
		return "", errors.New("model is set through {file:}, which costroid does not read: pass --model")
	}
	return value, nil
}

// plainJSON removes the comments and the trailing commas OpenCode's parser
// allows, leaving strings as they are.
func plainJSON(text string) []byte {
	var out bytes.Buffer
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case c == '"':
			end := stringEnd(text, i)
			out.WriteString(text[i:end])
			i = end - 1
		case strings.HasPrefix(text[i:], "//"):
			for i < len(text) && text[i] != '\n' {
				i++
			}
			out.WriteByte('\n')
		case strings.HasPrefix(text[i:], "/*"):
			end := strings.Index(text[i+2:], "*/")
			if end < 0 {
				return out.Bytes()
			}
			i += end + 3
			out.WriteByte(' ')
		case c == ',' && closesNext(text[i+1:]):
		default:
			out.WriteByte(c)
		}
	}
	return out.Bytes()
}

// stringEnd is the index just past the string that opens at start.
func stringEnd(text string, start int) int {
	for i := start + 1; i < len(text); i++ {
		switch text[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return len(text)
}

// closesNext says whether the next token after a comma closes an object or an
// array, past spaces and comments.
func closesNext(rest string) bool {
	for i := 0; i < len(rest); i++ {
		switch {
		case strings.ContainsRune(" \t\r\n", rune(rest[i])):
		case strings.HasPrefix(rest[i:], "//"):
			for i < len(rest) && rest[i] != '\n' {
				i++
			}
		case strings.HasPrefix(rest[i:], "/*"):
			end := strings.Index(rest[i+2:], "*/")
			if end < 0 {
				return false
			}
			i += end + 3
		default:
			return rest[i] == '}' || rest[i] == ']'
		}
	}
	return false
}
