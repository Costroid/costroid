package recipelint

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// RulePluginUnpinned names a plugin part that a later push could change
// unseen: a marketplace entry from outside the repository, a version that is
// not exact, or an MCP server fetched from a package registry at no exact
// version. RulePluginWrites names a part that can do more than read: a hook,
// or a command, skill or agent that allows a tool not known to only read.
const (
	RulePluginUnpinned = "plugin-unpinned"
	RulePluginWrites   = "plugin-writes"
)

// MarketplaceFile is where Claude Code reads a repository's plugin
// marketplace, by path from the repository's root.
const MarketplaceFile = ".claude-plugin/marketplace.json"

const (
	pluginManifest = ".claude-plugin/plugin.json"
	pluginServers  = ".mcp.json"
	pluginHooks    = "hooks/hooks.json"
)

// ReadTools names, for each MCP server a plugin starts, the tools that server
// lists as only reading. A tool or a server it does not name is taken to
// write.
type ReadTools map[string][]string

var (
	exactVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)
	// pinnedPackage is a package argument at an exact version: name@1.2.3,
	// @scope/name@1.2.3 or name==1.2.3.
	pinnedPackage = regexp.MustCompile(`^(@[^@/\s]+/)?[^@=\s]+(@|==)[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)
	// packageRunners fetch what they run from a package registry.
	packageRunners = map[string]bool{"npx": true, "bunx": true, "pnpx": true, "uvx": true, "pipx": true, "pnpm": true, "yarn": true, "npm": true}
	// builtinReadTools are Claude Code's tools that only read files.
	builtinReadTools = map[string]bool{"Read": true, "Grep": true, "Glob": true}
)

type marketplace struct {
	Plugins []struct {
		Name    string          `json:"name"`
		Source  json.RawMessage `json:"source"`
		Version string          `json:"version"`
	} `json:"plugins"`
}

type manifest struct {
	Name       string          `json:"name"`
	Version    string          `json:"version"`
	Hooks      json.RawMessage `json:"hooks"`
	MCPServers json.RawMessage `json:"mcpServers"`
}

type mcpServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	URL     string   `json:"url"`
}

// LintMarketplace holds the marketplace at root, and every plugin it lists, to
// two rules. Each plugin is a directory of the repository, so the commit that
// holds the marketplace pins it, and the marketplace and the plugin state the
// same exact version. Each MCP server runs a command on PATH, or a package at
// an exact version, and every tool a command, skill or agent allows is one
// that only reads.
func LintMarketplace(root string, readTools ReadTools) (Report, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(MarketplaceFile)))
	if err != nil {
		return Report{}, fmt.Errorf("read the marketplace: %w", err)
	}
	var listing marketplace
	if err := json.Unmarshal(data, &listing); err != nil {
		return Report{}, fmt.Errorf("read the marketplace %s: %w", MarketplaceFile, err)
	}
	l := &pluginLinter{root: root, readTools: readTools}
	l.report.Files = 1
	if len(listing.Plugins) == 0 {
		l.flag(MarketplaceFile, data, `"plugins"`, RulePluginUnpinned, "the marketplace lists no plugin")
	}
	for _, entry := range listing.Plugins {
		dir, ok := l.source(data, entry.Name, entry.Source)
		if !exactVersion.MatchString(entry.Version) {
			l.flag(MarketplaceFile, data, entry.Name, RulePluginUnpinned, "plugin %s states version %q, not an exact release such as 1.2.3: state the version its manifest states", entry.Name, entry.Version)
		}
		if ok {
			if err := l.plugin(dir, entry.Name, entry.Version); err != nil {
				return l.report, err
			}
		}
	}
	return l.report, nil
}

type pluginLinter struct {
	root      string
	readTools ReadTools
	report    Report
}

// flag records a finding on the first line of the file that holds near, or
// on its first line when no line does.
func (l *pluginLinter) flag(file string, data []byte, near, rule, format string, args ...any) {
	line := 1
	if at := bytes.Index(data, []byte(near)); near != "" && at >= 0 {
		line += bytes.Count(data[:at], []byte("\n"))
	}
	l.report.Findings = append(l.report.Findings, Finding{Path: file, Line: line, Rule: rule, Message: fmt.Sprintf(format, args...)})
}

// source returns the directory a marketplace entry names, by path from the
// repository's root, when it is one of the repository's own directories.
func (l *pluginLinter) source(data []byte, name string, raw json.RawMessage) (string, bool) {
	var relative string
	if err := json.Unmarshal(raw, &relative); err != nil || !strings.HasPrefix(relative, "./") {
		l.flag(MarketplaceFile, data, name, RulePluginUnpinned, "plugin %s comes from %s, which no commit of this repository pins: keep it in a directory of the repository and name it as ./path", name, raw)
		return "", false
	}
	dir := path.Clean(relative)
	if dir == "." || dir == ".." || strings.HasPrefix(dir, "../") {
		l.flag(MarketplaceFile, data, relative, RulePluginUnpinned, "plugin %s's source %s is outside the repository, which no commit of it pins", name, relative)
		return "", false
	}
	if info, err := os.Stat(filepath.Join(l.root, filepath.FromSlash(dir), filepath.FromSlash(pluginManifest))); err != nil || info.IsDir() {
		l.flag(MarketplaceFile, data, relative, RulePluginUnpinned, "plugin %s's source %s holds no %s", name, relative, pluginManifest)
		return "", false
	}
	return dir, true
}

// plugin checks one plugin's manifest, servers, hooks and allowed tools.
func (l *pluginLinter) plugin(dir, name, listed string) error {
	file := path.Join(dir, pluginManifest)
	data, err := l.read(file)
	if err != nil {
		return err
	}
	var plugin manifest
	if err := json.Unmarshal(data, &plugin); err != nil {
		return fmt.Errorf("read %s: %w", file, err)
	}
	if plugin.Name != name {
		l.flag(file, data, `"name"`, RulePluginUnpinned, "the manifest names the plugin %q and the marketplace lists %q, so the marketplace's entry is not this plugin", plugin.Name, name)
	}
	if !exactVersion.MatchString(plugin.Version) || plugin.Version != listed {
		l.flag(file, data, `"version"`, RulePluginUnpinned, "the manifest states version %q and the marketplace %q: state one exact release, such as 1.2.3, in both", plugin.Version, listed)
	}
	if len(plugin.Hooks) > 0 {
		l.flag(file, data, `"hooks"`, RulePluginWrites, "the manifest declares hooks, and a hook runs a command on every event it names: drop them")
	}
	if _, err := os.Stat(filepath.Join(l.root, filepath.FromSlash(path.Join(dir, pluginHooks)))); err == nil {
		l.flag(path.Join(dir, pluginHooks), nil, "", RulePluginWrites, "the plugin holds hooks, and a hook runs a command on every event it names: drop them")
	}
	if len(plugin.MCPServers) > 0 {
		l.flag(file, data, `"mcpServers"`, RulePluginUnpinned, "the manifest declares its MCP servers, which this lint does not read: keep them in %s", pluginServers)
	}
	if err := l.servers(dir); err != nil {
		return err
	}
	return l.allowedTools(dir, name)
}

func (l *pluginLinter) read(file string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(l.root, filepath.FromSlash(file)))
	if err != nil {
		return nil, fmt.Errorf("read a plugin's file: %w", err)
	}
	return data, nil
}

// servers checks each MCP server the plugin starts: a remote server or a
// package fetched at no exact version is unpinned, and a server whose tools
// are not known to only read writes.
func (l *pluginLinter) servers(dir string) error {
	file := path.Join(dir, pluginServers)
	data, err := l.read(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var config struct {
		MCPServers map[string]mcpServer `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("read %s: %w", file, err)
	}
	for _, name := range slices.Sorted(maps.Keys(config.MCPServers)) {
		server := config.MCPServers[name]
		if server.URL != "" {
			l.flag(file, data, server.URL, RulePluginUnpinned, "server %s is the remote %s, which no version of the plugin pins: run a command at a release instead", name, server.URL)
			continue
		}
		if problem := unpinnedCommand(server); problem != "" {
			l.flag(file, data, server.Command, RulePluginUnpinned, "server %s %s: run a command on PATH, or name the package at an exact release, such as name@1.2.3", name, problem)
		}
		if _, known := l.readTools[name]; !known {
			l.flag(file, data, `"`+name+`"`, RulePluginWrites, "server %s is not one whose tools are known to only read", name)
		}
	}
	return nil
}

// unpinnedCommand says how a server's command fetches what it runs at no
// exact version, or returns "".
func unpinnedCommand(server mcpServer) string {
	for _, arg := range server.Args {
		if strings.HasSuffix(arg, "@latest") {
			return "names " + arg
		}
	}
	if !packageRunners[path.Base(server.Command)] {
		return ""
	}
	for _, arg := range server.Args {
		if pinnedPackage.MatchString(arg) {
			return ""
		}
	}
	return "runs " + path.Base(server.Command) + " with no package at an exact release"
}

// allowedTools checks the tools each command, skill and agent allows.
func (l *pluginLinter) allowedTools(dir, plugin string) error {
	var files []string
	for _, pattern := range []string{"commands/*.md", "skills/*/SKILL.md", "agents/*.md"} {
		found, err := filepath.Glob(filepath.Join(l.root, filepath.FromSlash(dir), filepath.FromSlash(pattern)))
		if err != nil {
			return fmt.Errorf("list %s in %s: %w", pattern, dir, err)
		}
		files = append(files, found...)
	}
	for _, found := range files {
		relative, err := filepath.Rel(l.root, found)
		if err != nil {
			return fmt.Errorf("name %s: %w", found, err)
		}
		file := filepath.ToSlash(relative)
		data, err := l.read(file)
		if err != nil {
			return err
		}
		matter, err := frontMatter(data)
		if err != nil {
			return fmt.Errorf("read %s's front matter: %w", file, err)
		}
		for _, key := range []string{"allowed-tools", "tools"} {
			for _, tool := range toolList(matter[key]) {
				if !l.reads(plugin, tool) {
					l.flag(file, data, tool, RulePluginWrites, "it allows %s, which is not a tool known to only read: allow only Read, Grep, Glob and the read tools of the plugin's own servers", tool)
				}
			}
		}
	}
	return nil
}

// reads reports whether a tool only reads: one of Claude Code's file readers,
// or a read tool of one of this plugin's servers, by the name Claude Code
// gives it, mcp__plugin_<plugin>_<server>__<tool>.
func (l *pluginLinter) reads(plugin, tool string) bool {
	if builtinReadTools[tool] {
		return true
	}
	for server, tools := range l.readTools {
		if name, found := strings.CutPrefix(tool, "mcp__plugin_"+plugin+"_"+server+"__"); found && slices.Contains(tools, name) {
			return true
		}
	}
	return false
}

// frontMatter reads the YAML between a Markdown file's opening --- lines. A
// file without it has none.
func frontMatter(data []byte) (map[string]any, error) {
	rest, found := bytes.CutPrefix(data, []byte("---\n"))
	if !found {
		return nil, nil
	}
	matter, _, found := bytes.Cut(rest, []byte("\n---\n"))
	if !found {
		return nil, errors.New("the front matter has no closing ---")
	}
	var fields map[string]any
	if err := yaml.Unmarshal(matter, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

// toolList reads allowed-tools as Claude Code does: a list, or one string of
// names split on commas and spaces.
func toolList(value any) []string {
	var tools []string
	switch typed := value.(type) {
	case string:
		tools = strings.FieldsFunc(typed, func(r rune) bool { return r == ',' || r == ' ' })
	case []any:
		for _, item := range typed {
			tools = append(tools, strings.TrimSpace(fmt.Sprint(item)))
		}
	}
	return tools
}
