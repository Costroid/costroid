package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/recipelint"
)

const rejectedWorkflow = `on: pull_request_target
permissions: {}
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - run: make
`

func TestLintRecipesPassesTheShippedRecipes(t *testing.T) {
	code, stdout, stderr := execute(t, "lint-recipes", "../../recipes", "../../action/action.yml")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "costroid: 17 files, 0 rejected lines") {
		t.Fatalf("stdout: %s", stdout)
	}
}

func TestLintRecipesExitsOneAndNamesTheRule(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yml")
	if err := os.WriteFile(path, []byte(rejectedWorkflow), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := execute(t, "lint-recipes", path)
	if code != exitRejected {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stderr, "bad.yml:1: pull-request-target:") {
		t.Fatalf("stderr: %s", stderr)
	}
}

func TestLintRecipesRefusesAPathWithNoRecipes(t *testing.T) {
	for name, args := range map[string][]string{
		"no path":         {"lint-recipes"},
		"empty directory": {"lint-recipes", t.TempDir()},
		"missing file":    {"lint-recipes", filepath.Join(t.TempDir(), "absent.yml")},
	} {
		t.Run(name, func(t *testing.T) {
			if code, _, _ := execute(t, args...); code != exitFailure {
				t.Fatalf("exit %d", code)
			}
		})
	}
}

func TestLintRecipesRefusesTheAdminKeyOnTheAgentsJob(t *testing.T) {
	recipe, err := os.ReadFile("../../recipes/github-claude-code-litellm.yml")
	if err != nil {
		t.Fatal(err)
	}
	step := "          COSTROID_GATEWAY_KEY_FILE:"
	seeded := strings.Replace(string(recipe), step, "          GATEWAY: ${{ secrets.LITELLM_MASTER_KEY }}\n"+step, 1)
	if seeded == string(recipe) {
		t.Fatal("the seed found no agent step")
	}
	path := filepath.Join(t.TempDir(), "seeded.yml")
	if err := os.WriteFile(path, []byte(seeded), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := execute(t, "lint-recipes", path)
	if code != exitRejected {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stderr, "agent-admin-key: the agent's job can read secrets.LITELLM_MASTER_KEY") {
		t.Fatalf("stderr: %s", stderr)
	}
}

// marketplaceCopy copies the repository's marketplace, the plugins under
// plugin/ and one recipe to a new directory, and returns it.
func marketplaceCopy(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	copyInto(t, filepath.Join(repositoryRoot, "recipes", "github-connect.yml"), filepath.Join(root, "recipes", "github-connect.yml"))
	copyInto(t, filepath.Join(repositoryRoot, filepath.FromSlash(recipelint.MarketplaceFile)), filepath.Join(root, filepath.FromSlash(recipelint.MarketplaceFile)))
	plugins := filepath.Join(repositoryRoot, "plugin")
	err := filepath.WalkDir(plugins, func(file string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relative, err := filepath.Rel(repositoryRoot, file)
		if err != nil {
			return err
		}
		copyInto(t, file, filepath.Join(root, relative))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// TestLintRecipesChecksTheMarketplace holds a directory's plugin marketplace,
// and the plugins it lists, to the plugin rules whenever lint-recipes runs
// there, with the read tools costroid mcp itself lists. A directory without
// a marketplace is linted as before.
func TestLintRecipesChecksTheMarketplace(t *testing.T) {
	t.Run("this repository, as CI runs it", func(t *testing.T) {
		t.Chdir(repositoryRoot)
		code, stdout, stderr := execute(t, "lint-recipes", "recipes", "action/action.yml")
		if code != 0 || !strings.Contains(stdout, "costroid: 18 files, 0 rejected lines") {
			t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
	})
	for _, test := range []struct {
		label    string
		seed     func(t *testing.T, root string)
		want     string
		rejected int
	}{
		{"a command that allows Bash", func(t *testing.T, root string) {
			seedFile(t, filepath.Join(root, "plugin", "costroid", "commands", "status.md"), "allowed-tools: mcp__plugin_costroid_costroid__run_status", "allowed-tools: mcp__plugin_costroid_costroid__run_status, Bash")
		}, "plugin/costroid/commands/status.md:3: plugin-writes: it allows Bash", 1},
		{"a tool costroid mcp does not list", func(t *testing.T, root string) {
			seedFile(t, filepath.Join(root, "plugin", "costroid", "commands", "status.md"), "__run_status", "__run_shell")
		}, "plugin/costroid/commands/status.md:3: plugin-writes: it allows mcp__plugin_costroid_costroid__run_shell", 1},
		{"a hook", func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "plugin", "costroid", "hooks", "hooks.json"), "{}\n")
		}, "plugin/costroid/hooks/hooks.json:1: plugin-writes:", 1},
		{"a version range", func(t *testing.T, root string) {
			seedFile(t, filepath.Join(root, filepath.FromSlash(recipelint.MarketplaceFile)), `"version": "0.1.0"`, `"version": "^0.1.0"`)
		}, `plugin-unpinned: plugin costroid states version "^0.1.0", not an exact release`, 2},
	} {
		t.Run(test.label, func(t *testing.T) {
			root := marketplaceCopy(t)
			test.seed(t, root)
			t.Chdir(root)
			code, stdout, stderr := execute(t, "lint-recipes", "recipes")
			if code != exitRejected || !strings.Contains(stderr, test.want) || !strings.Contains(stdout, fmt.Sprintf("costroid: 2 files, %d rejected lines", test.rejected)) {
				t.Errorf("exit %d, stdout %q, stderr %q, want %q", code, stdout, stderr, test.want)
			}
		})
	}
	t.Run("the shipped marketplace", func(t *testing.T) {
		t.Chdir(marketplaceCopy(t))
		code, stdout, stderr := execute(t, "lint-recipes", "recipes")
		if code != 0 || !strings.Contains(stdout, "costroid: 2 files, 0 rejected lines") {
			t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
	})
	t.Run("no marketplace", func(t *testing.T) {
		root := marketplaceCopy(t)
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(recipelint.MarketplaceFile))); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(root, "plugin", "costroid", "hooks", "hooks.json"), "{}\n")
		t.Chdir(root)
		code, stdout, stderr := execute(t, "lint-recipes", "recipes")
		if code != 0 || !strings.Contains(stdout, "costroid: 1 files, 0 rejected lines") {
			t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
	})
}

// seedFile replaces the one occurrence of old in a file with seeded.
func seedFile(t *testing.T, file, old, seeded string) {
	t.Helper()
	data := readFile(t, file)
	if strings.Count(data, old) != 1 {
		t.Fatalf("%s holds %q %d times, want once", file, old, strings.Count(data, old))
	}
	writeFile(t, file, strings.Replace(data, old, seeded, 1))
}
