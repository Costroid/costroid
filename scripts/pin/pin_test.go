package main

import (
	"bytes"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	oldVersion = "1.0.0-alpha.22"
	oldCommit  = "47ee64dcf7d74266b494f05e706f0942090d61c0"
	newVersion = "1.0.0-alpha.23"
	newCommit  = "61a5b2e3836a940838e127311ed15f3087f3d6d5"
	// firstRelease is what the chart and Compose named before any pin moved
	// them.
	firstRelease = "1.0.0-alpha.1"
)

// tree is a repository in small: each file a pin reads, and each file it
// must leave alone, keyed by its slash path.
type tree map[string]string

func pinnedTree() tree {
	return tree{
		"README.md": "The recipes pin `" + oldVersion + "`: [install it](docs/install.md).\n",
		"recipes/github-claude-code.yml": "# Runs Claude Code under a budget.\nsteps:\n" +
			"  - uses: Costroid/costroid/action@" + oldCommit + " # v" + oldVersion + "\n",
		"recipes/gitlab-ci.yml": "variables:\n  COSTROID_VERSION: " + oldVersion + "\n  COSTROID_COMMIT: " + oldCommit + "\n",
		"docs/install.md": "# Install\n\nThe commands below install `" + oldVersion + "`, the release the recipes pin.\n\n" +
			"```sh\ncurl -fsSLO https://github.com/Costroid/costroid/releases/download/v" + oldVersion + "/SHA256SUMS\n" +
			"go install github.com/Costroid/costroid/cmd/costroid@v" + oldVersion + "\n```\n\n" +
			"Where github.com is unreachable, pass the commit of `v" + oldVersion + "` with `--action-ref " + oldCommit + "`.\n",
		"docs/recipes.md": "| `Costroid/costroid/action` | v" + oldVersion + " | `" + oldCommit + "` |\n",
		// Neither is the pinned release: one is a prefix of it, and one has
		// it as a prefix.
		"docs/troubleshooting.md":                             "A version such as `1.0.0-alpha.2` or `" + oldVersion + "1` is refused.\n",
		"docs/ledger.md":                                      "Export `" + oldVersion + "` first.\n\n```sh\nexport COSTROID_IMAGE=ghcr.io/costroid/costroid:" + oldVersion + "\n```\n",
		"marketplace/README.md":                               "This action is `Costroid/costroid/action` at the commit of `__RELEASE_TAG__`.\n",
		"internal/recipelint/testdata/customer/two-block.yml": "      - uses: Costroid/costroid/action@" + oldCommit + " # v" + oldVersion + "\n",
		"costroid.schema.json": "{\n  \"$id\": \"https://raw.githubusercontent.com/Costroid/costroid/v" + oldVersion + "/costroid.schema.json\",\n" +
			"  \"description\": \"A version such as " + oldVersion + ".\"\n}\n",
		chartFile:      "apiVersion: v2\nname: costroid\nversion: " + firstRelease + "\nappVersion: " + firstRelease + "\n",
		composeFile:    "services:\n  costroid:\n    image: ${COSTROID_IMAGE:-ghcr.io/costroid/costroid:" + firstRelease + "}\n",
		"CHANGELOG.md": "# Changelog\n\n## " + oldVersion + " (2026-11-11)\n\n- Pinned at " + oldCommit + ".\n",
		// A recorded fixture under a folder the pin reads.
		"docs/testdata/recorded/manifest.json": "{\"recorded\": true, \"costroid\": \"" + oldVersion + "\"}\n",
		"docs/testdata/recorded/README.md":     "The job ran Costroid " + oldVersion + " at " + oldCommit + ".\n",
		"internal/recipelint/docs_test.go":     "const pinned = \"" + oldVersion + "\"\n",
		"cmd/costroid/version_test.go":         "const goInstallRelease = \"" + oldVersion + "\"\n",
		"docs/docs_test.go":                    "const pinned = \"" + oldVersion + "\"\n",
	}
}

func (files tree) write(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func readTree(t *testing.T, root string) tree {
	t.Helper()
	files := tree{}
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, name)
		files[filepath.ToSlash(relative)] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func runPin(t *testing.T, root string, version, commit string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run([]string{"-root", root, "-version", version, "-commit", commit}, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func moved(text string) string {
	return strings.NewReplacer(oldVersion+"`", newVersion+"`", oldCommit, newCommit, "v"+oldVersion, "v"+newVersion,
		" "+oldVersion+"\n", " "+newVersion+"\n", ":"+oldVersion+"\n", ":"+newVersion+"\n").Replace(text)
}

func TestPinMovesEveryPinnedReference(t *testing.T) {
	before := pinnedTree()
	root := before.write(t)
	code, stdout, stderr := runPin(t, root, newVersion, newCommit)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	after := readTree(t, root)
	want := maps.Clone(before)
	for _, name := range []string{
		"README.md", "recipes/github-claude-code.yml", "recipes/gitlab-ci.yml", "docs/install.md",
		"docs/recipes.md", "docs/ledger.md", "internal/recipelint/testdata/customer/two-block.yml",
	} {
		want[name] = moved(before[name])
		if want[name] == before[name] {
			t.Fatalf("the fixture's %s names no pinned release", name)
		}
	}
	want["costroid.schema.json"] = strings.Replace(before["costroid.schema.json"], "/v"+oldVersion+"/", "/v"+newVersion+"/", 1)
	want[chartFile] = strings.ReplaceAll(before[chartFile], firstRelease, newVersion)
	want[composeFile] = strings.ReplaceAll(before[composeFile], firstRelease, newVersion)
	for name, body := range want {
		if after[name] != body {
			t.Errorf("%s reads\n%s\nwant\n%s", name, after[name], body)
		}
	}
	if !strings.Contains(after["costroid.schema.json"], "such as "+oldVersion+".") {
		t.Error("the pin moved a version outside the schema's $id")
	}
	changed := strings.Split(strings.TrimSpace(stdout), "\n")
	count := changed[len(changed)-1]
	if got := strings.Join(changed[:len(changed)-1], " "); got != "README.md costroid.schema.json deploy/compose.yaml deploy/helm/costroid/Chart.yaml docs/install.md docs/ledger.md docs/recipes.md internal/recipelint/testdata/customer/two-block.yml recipes/github-claude-code.yml recipes/gitlab-ci.yml" {
		t.Errorf("the pin listed the files %s", got)
	}
	if want := "pin: " + oldVersion + " at " + oldCommit + " to " + newVersion + " at " + newCommit + ": 20 references moved in 10 files"; count != want {
		t.Errorf("the count reads %q, want %q", count, want)
	}

	if err := stillStanding(root, []string{"docs/install.md"}, release{oldVersion, oldCommit}, release{newVersion, newCommit}); err != nil {
		t.Errorf("a moved file still reads as the old release: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "install.md"), []byte(before["docs/install.md"]), 0o644); err != nil {
		t.Fatal(err)
	}
	err := stillStanding(root, []string{"docs/install.md"}, release{oldVersion, oldCommit}, release{newVersion, newCommit})
	if err == nil || !strings.Contains(err.Error(), "docs/install.md:3") {
		t.Errorf("the check after a pin passed a file that still names the old release: %v", err)
	}
}

func TestPinLeavesRecordedFixturesAlone(t *testing.T) {
	before := pinnedTree()
	root := before.write(t)
	if code, _, stderr := runPin(t, root, newVersion, newCommit); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	after := readTree(t, root)
	for _, name := range []string{
		"CHANGELOG.md", "docs/testdata/recorded/manifest.json", "docs/testdata/recorded/README.md",
		"internal/recipelint/docs_test.go", "cmd/costroid/version_test.go", "docs/docs_test.go",
	} {
		if after[name] != before[name] {
			t.Errorf("the pin changed %s:\n%s", name, after[name])
		}
	}
}

func TestPinRefusesAVersionInAHistorySentence(t *testing.T) {
	for name, sentence := range map[string]struct {
		path, text string
		line       int
	}{
		"after":         {"docs/recipes.md", "Intro.\n\nBoth need a Costroid release after `" + oldVersion + "`, which has no `--agent`.\n", 3},
		"before":        {"docs/install.md", "Releases before " + oldVersion + " take no flag.\n", 1},
		"since":         {"docs/install.md", "The flag exists since `v" + oldVersion + "`.\n", 1},
		"until":         {"docs/install.md", "It was missing until " + oldVersion + ".\n", 1},
		"up to":         {"docs/gateway.md", "Versions up to " + oldVersion + " derived the pair.\n", 1},
		"and earlier":   {"docs/verify.md", "Each release carries provenance. " + oldVersion + " and earlier releases carry none.\n", 1},
		"or later":      {"README.md", "Run " + oldVersion + " or later.\n", 1},
		"in no release": {"docs/install.md", "The flag `--pr`, in no release yet, follows " + oldVersion + ".\n", 1},
		"a recorded run": {"docs/adapters.md", "It rests on a recorded stop (GitHub Actions run 42945131550, 2026-11-11):\n" +
			"`costroid run` on Costroid " + oldVersion + ", on an Anthropic key.\n", 2},
		"a comment over two lines": {"recipes/github-opencode.yml", "# Runs OpenCode.\n# It needs a Costroid release after\n# " + oldVersion + ", which has no\n# costroid run --agent opencode.\n" +
			"steps:\n  - uses: Costroid/costroid/action@" + oldCommit + " # v" + oldVersion + "\n", 3},
	} {
		t.Run(name, func(t *testing.T) {
			files := pinnedTree()
			files[sentence.path] += "\n" + sentence.text
			root := files.write(t)
			line := strings.Count(files[sentence.path], "\n") - strings.Count(sentence.text, "\n") + sentence.line
			before := readTree(t, root)
			code, stdout, stderr := runPin(t, root, newVersion, newCommit)
			if code != 1 || stdout != "" {
				t.Fatalf("exit %d, stdout %q", code, stdout)
			}
			if want := sentence.path + ":" + strconv.Itoa(line) + ": "; !strings.Contains(stderr, want) || !strings.Contains(stderr, "Nothing was changed.") {
				t.Errorf("the refusal does not name %s:\n%s", want, stderr)
			}
			if after := readTree(t, root); !maps.Equal(after, before) {
				t.Error("a refused pin changed files")
			}
		})
	}

	t.Run("a pin that moves nothing", func(t *testing.T) {
		files := pinnedTree()
		files["docs/recipes.md"] += "\nBoth need a Costroid release after `" + oldVersion + "`.\n"
		root := files.write(t)
		if code, _, stderr := runPin(t, root, oldVersion, oldCommit); code != 0 {
			t.Errorf("a pin to the release the recipes already pin refused: %s", stderr)
		}
	})
}

func TestPinMovesTheChartAndCompose(t *testing.T) {
	// The recipes already pin the release, as on the first pin of the chart
	// and Compose: only those two move.
	before := pinnedTree()
	root := before.write(t)
	code, stdout, stderr := runPin(t, root, oldVersion, oldCommit)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	want := "deploy/compose.yaml\ndeploy/helm/costroid/Chart.yaml\npin: " + oldVersion + " at " + oldCommit + " to " + oldVersion + " at " + oldCommit + ": 3 references moved in 2 files\n"
	if stdout != want {
		t.Errorf("stdout %q, want %q", stdout, want)
	}
	after := readTree(t, root)
	for _, line := range []string{"\nversion: " + oldVersion + "\n", "\nappVersion: " + oldVersion + "\n"} {
		if !strings.Contains(after[chartFile], line) {
			t.Errorf("the chart has no line %q:\n%s", strings.TrimSpace(line), after[chartFile])
		}
	}
	if !strings.Contains(after[composeFile], "${COSTROID_IMAGE:-ghcr.io/costroid/costroid:"+oldVersion+"}") {
		t.Errorf("Compose does not default to %s:\n%s", oldVersion, after[composeFile])
	}
	for name, body := range before {
		if name != chartFile && name != composeFile && after[name] != body {
			t.Errorf("the pin changed %s", name)
		}
	}

	for name, broken := range map[string]tree{
		"a chart with no appVersion":    {chartFile: "apiVersion: v2\nversion: " + firstRelease + "\n"},
		"Compose with no image default": {composeFile: "services:\n  costroid:\n    image: ghcr.io/costroid/costroid\n"},
	} {
		t.Run(name, func(t *testing.T) {
			files := pinnedTree()
			maps.Copy(files, broken)
			root := files.write(t)
			if code, _, stderr := runPin(t, root, newVersion, newCommit); code != 1 || !strings.Contains(stderr, "want one") {
				t.Errorf("exit %d: %s", code, stderr)
			}
			if after := readTree(t, root); !maps.Equal(after, files) {
				t.Error("a failed pin changed files")
			}
		})
	}
}
