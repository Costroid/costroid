package recipelint

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/mcpserver"
)

const pluginDir = "plugin/costroid"

// costroidReadTools are the tools costroid mcp lists. cmd/costroid's
// TestPluginToolsReadOnly proves from the binary that each only reads.
var costroidReadTools = ReadTools{mcpserver.Name: {mcpserver.RunStatus, mcpserver.PullResult, mcpserver.PolicyCheck, mcpserver.LedgerPull}}

// pluginTree copies the marketplace and the plugin into a directory of their
// own, where a case can change them.
func pluginTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	copyFile := func(name string) {
		data, err := os.ReadFile(filepath.Join(repositoryRoot, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		writeTo(t, root, name, string(data))
	}
	copyFile(MarketplaceFile)
	err := filepath.WalkDir(filepath.Join(repositoryRoot, pluginDir), func(file string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relative, err := filepath.Rel(repositoryRoot, file)
		if err == nil {
			copyFile(filepath.ToSlash(relative))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func writeTo(t *testing.T, root, name, content string) {
	t.Helper()
	file := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// replaceIn changes one string in one file of a tree, and fails when the
// string is not there, so a case cannot pass on a file that moved.
func replaceIn(t *testing.T, root, name, old, replacement string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), old) {
		t.Fatalf("%s does not hold %q", name, old)
	}
	writeTo(t, root, name, strings.Replace(string(data), old, replacement, 1))
}

func lintTree(t *testing.T, root string) Report {
	t.Helper()
	report, err := LintMarketplace(root, costroidReadTools)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

// expectPluginRule fails unless the report holds a finding of rule in file.
func expectPluginRule(t *testing.T, report Report, rule, file string) {
	t.Helper()
	for _, finding := range report.Findings {
		if finding.Rule == rule && finding.Path == file {
			return
		}
	}
	t.Errorf("no %s finding in %s: %v", rule, file, report.Findings)
}

func TestPluginPinsExactVersions(t *testing.T) {
	if report := lintTree(t, repositoryRoot); len(report.Findings) != 0 {
		t.Fatalf("the shipped plugin: %v", report.Findings)
	}
	manifestFile := pluginDir + "/" + pluginManifest
	servers := pluginDir + "/" + pluginServers
	for _, change := range []struct {
		name, file, old, replacement, finding string
	}{
		{"latest in the manifest", manifestFile, `"version": "0.1.0"`, `"version": "latest"`, manifestFile},
		{"a range in the manifest", manifestFile, `"version": "0.1.0"`, `"version": "^0.1.0"`, manifestFile},
		{"a range in the marketplace", MarketplaceFile, `"version": "0.1.0"`, `"version": "~0.1.0"`, MarketplaceFile},
		{"two versions", MarketplaceFile, `"version": "0.1.0"`, `"version": "0.2.0"`, manifestFile},
		{"npx at latest", servers, `"command": "costroid",
      "args": ["mcp"]`, `"command": "npx",
      "args": ["-y", "@costroid/mcp@latest"]`, servers},
		{"npx at no version", servers, `"command": "costroid",
      "args": ["mcp"]`, `"command": "npx",
      "args": ["-y", "@costroid/mcp"]`, servers},
		{"uvx at no version", servers, `"command": "costroid",
      "args": ["mcp"]`, `"command": "uvx",
      "args": ["costroid-mcp"]`, servers},
		{"a remote server", servers, `"command": "costroid",
      "args": ["mcp"]`, `"type": "http",
      "url": "https://mcp.example.com/costroid"`, servers},
	} {
		t.Run(change.name, func(t *testing.T) {
			root := pluginTree(t)
			replaceIn(t, root, change.file, change.old, change.replacement)
			expectPluginRule(t, lintTree(t, root), RulePluginUnpinned, change.finding)
		})
	}
	t.Run("npx at an exact release passes", func(t *testing.T) {
		root := pluginTree(t)
		replaceIn(t, root, servers, `"command": "costroid",
      "args": ["mcp"]`, `"command": "npx",
      "args": ["-y", "@costroid/mcp@1.2.3"]`)
		if report := lintTree(t, root); len(report.Findings) != 0 {
			t.Errorf("findings: %v", report.Findings)
		}
	})
}

func TestPluginMarketplaceIsPinned(t *testing.T) {
	if report := lintTree(t, repositoryRoot); len(report.Findings) != 0 {
		t.Fatalf("the shipped marketplace: %v", report.Findings)
	}
	for _, change := range []struct{ name, replacement string }{
		{"a GitHub repository", `{"source": "github", "repo": "Costroid/costroid"}`},
		{"a Git URL", `"https://github.com/Costroid/costroid.git"`},
		{"a path outside", `"./../elsewhere/costroid"`},
		{"a directory with no plugin", `"./plugin/elsewhere"`},
	} {
		t.Run(change.name, func(t *testing.T) {
			root := pluginTree(t)
			replaceIn(t, root, MarketplaceFile, `"./plugin/costroid"`, change.replacement)
			expectPluginRule(t, lintTree(t, root), RulePluginUnpinned, MarketplaceFile)
		})
	}

	// claude-code-action at v1.0.248 takes a marketplace as a Git URL that
	// ends in .git, which names no commit, or as a local path. A recipe that
	// installs a plugin checks its marketplace out at a commit first.
	for _, name := range recipeFiles(t) {
		documents, err := parse([]byte(read(t, name)))
		if err != nil {
			t.Fatal(err)
		}
		for _, root := range documents {
			for _, job := range entries(field(root, "jobs")) {
				for _, step := range sequence(field(job.value, "steps")) {
					for _, entry := range strings.Split(scalarField(field(step, "with"), "plugin_marketplaces"), "\n") {
						if entry = strings.TrimSpace(entry); entry != "" && !strings.HasPrefix(entry, "./") && !strings.HasPrefix(entry, "/") {
							t.Errorf("%s installs plugins from %s, which names no commit", name, entry)
						}
					}
				}
			}
		}
	}
}

func TestPluginAllowsOnlyReadTools(t *testing.T) {
	status := pluginDir + "/commands/status.md"
	for _, change := range []struct{ name, tool string }{
		{"Bash", "Bash"},
		{"Write", "Write"},
		{"a tool the server does not list", "mcp__plugin_costroid_costroid__delete_rows"},
		{"another plugin's tool", "mcp__plugin_other_costroid__run_status"},
	} {
		t.Run(change.name, func(t *testing.T) {
			root := pluginTree(t)
			replaceIn(t, root, status, "allowed-tools: mcp__plugin_costroid_costroid__run_status", "allowed-tools: mcp__plugin_costroid_costroid__run_status, "+change.tool)
			expectPluginRule(t, lintTree(t, root), RulePluginWrites, status)
		})
	}
	t.Run("a hook", func(t *testing.T) {
		root := pluginTree(t)
		writeTo(t, root, pluginDir+"/"+pluginHooks, `{"hooks": {}}`)
		expectPluginRule(t, lintTree(t, root), RulePluginWrites, pluginDir+"/"+pluginHooks)
	})
	t.Run("a server not known to read", func(t *testing.T) {
		root := pluginTree(t)
		report, err := LintMarketplace(root, ReadTools{})
		if err != nil {
			t.Fatal(err)
		}
		expectPluginRule(t, report, RulePluginWrites, pluginDir+"/"+pluginServers)
	})
}
