package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/cli"
	"github.com/Costroid/costroid/internal/run"
)

// The kinds of wrong command line every command is walked with.
const (
	unknownFlag     = "a flag the command does not have"
	flagAlone       = "a flag without its value"
	wrongForm       = "a value of the wrong form"
	leftOut         = "a required flag or argument left out"
	oneTooMany      = "an argument too many"
	noSubcommand    = "no subcommand"
	wrongSubcommand = "a subcommand the command does not have"
)

var wrongLineKinds = []string{unknownFlag, flagAlone, wrongForm, leftOut, oneTooMany}

// wrongLines gives each command a command line of each kind, or the reason
// the kind does not exist for it.
var wrongLines = map[string]map[string]any{
	"demo": {
		unknownFlag: []string{"demo", "--frob"},
		flagAlone:   []string{"demo", "--state"},
		wrongForm:   []string{"demo", "--state", "passed"},
		leftOut:     "demo needs no flag",
		oneTooMany:  []string{"demo", "rows.jsonl"},
	},
	"lint": {
		unknownFlag: []string{"lint", "--loose"},
		flagAlone:   "its one flag takes no value",
		wrongForm:   []string{"lint", "--strict=maybe"},
		leftOut:     "lint needs no flag",
		oneTooMany:  "lint takes any number of paths",
	},
	"init": {
		unknownFlag: []string{"init", "github", "--agnet", "claude"},
		flagAlone:   []string{"init", "github", "--agent"},
		wrongForm:   []string{"init", "bitbucket", "--agent", "claude"},
		leftOut:     []string{"init", "github"},
		oneTooMany:  []string{"init", "github", "gitlab", "--agent", "claude"},
	},
	"doctor": {
		unknownFlag: []string{"doctor", "--cli"},
		flagAlone:   "its one flag takes no value",
		wrongForm:   []string{"doctor", "--ci=maybe"},
		leftOut:     "doctor needs no flag",
		oneTooMany:  []string{"doctor", "path"},
	},
	"run": {
		unknownFlag: []string{"run", "--frob"},
		flagAlone:   []string{"run", "--budget-usd"},
		wrongForm:   []string{"run", "--budget-usd", "five", "--", "claude", "-p", "work"},
		leftOut:     []string{"run", "--", "claude", "-p", "work"},
		oneTooMany:  "what follows the flags is the agent's command",
	},
	"policy": {
		unknownFlag: []string{"policy", "--frob"},
		flagAlone:   []string{"policy", "--rows"},
		wrongForm:   []string{"policy", "--rows", "rows.jsonl", "--at", "yesterday"},
		leftOut:     []string{"policy"},
		oneTooMany:  []string{"policy", "--rows", "rows.jsonl", "extra"},
	},
	"receipt": {
		unknownFlag: []string{"receipt", "--frob"},
		flagAlone:   "receipt has no flag",
		wrongForm:   "receipt has no flag",
		leftOut:     "receipt needs no argument",
		oneTooMany:  []string{"receipt", "extra"},
	},
	"validate": {
		unknownFlag: []string{"validate", "--frob"},
		flagAlone:   "its one flag takes no value",
		wrongForm:   []string{"validate", "--json=maybe"},
		leftOut:     "validate needs no argument",
		oneTooMany:  []string{"validate", "a.yml", "b.yml"},
	},
	"version": {
		unknownFlag: []string{"version", "--frob"},
		flagAlone:   "its flags take no value",
		wrongForm:   []string{"version", "--verbose=maybe"},
		leftOut:     "version needs no argument",
		oneTooMany:  []string{"version", "extra"},
	},
	"report": {
		unknownFlag: []string{"report", "--frob"},
		flagAlone:   []string{"report", "--rows"},
		wrongForm:   []string{"report", "--rows", "rows.jsonl", "--recorded", "yesterday"},
		leftOut:     []string{"report"},
		oneTooMany:  "report reads no argument and ignores one",
	},
	"ingest-run": {
		unknownFlag: []string{"ingest-run", "--frob"},
		flagAlone:   []string{"ingest-run", "--from"},
		wrongForm:   []string{"ingest-run", "--from", "nowhere", "--out", "rows.jsonl"},
		leftOut:     []string{"ingest-run", "--out", "rows.jsonl"},
		oneTooMany:  []string{"ingest-run", "--from", "execution-file=execution.json", "--out", "rows.jsonl", "extra"},
	},
	"follow-up": {
		unknownFlag: []string{"follow-up", "--frob"},
		flagAlone:   []string{"follow-up", "--rows"},
		wrongForm:   []string{"follow-up", "--rows", "rows.jsonl", "--ingested=maybe"},
		leftOut:     []string{"follow-up"},
		oneTooMany:  "follow-up reads no argument and ignores one",
	},
	"approve": {
		unknownFlag: []string{"approve", "--frob"},
		flagAlone:   "approve has no flag",
		wrongForm:   "approve has no flag",
		leftOut:     "approve needs no argument",
		oneTooMany:  []string{"approve", "extra"},
	},
	"review": {
		unknownFlag: []string{"review", "--frob"},
		flagAlone:   []string{"review", "--post", "--file"},
		wrongForm:   []string{"review", "--keep", "--from", "codex-home=home", "--out", "answer.md", "--since", "yesterday"},
		leftOut:     []string{"review"},
		oneTooMany:  []string{"review", "--post", "--file", "answer.md", "extra"},
	},
	"result": {
		unknownFlag: []string{"result", "--frob"},
		flagAlone:   []string{"result", "--pr"},
		wrongForm:   []string{"result", "--pr", "seven"},
		leftOut:     []string{"result"},
		oneTooMany:  []string{"result", "--pr", "7", "extra"},
	},
	"mcp": {
		unknownFlag: []string{"mcp", "--frob"},
		flagAlone:   []string{"mcp", "--repo"},
		wrongForm:   "each flag takes any text",
		leftOut:     "mcp needs no flag",
		oneTooMany:  []string{"mcp", "extra"},
	},
	"lint-recipes": {
		unknownFlag: []string{"lint-recipes", "--frob"},
		flagAlone:   "lint-recipes has no flag",
		wrongForm:   "lint-recipes has no flag",
		leftOut:     []string{"lint-recipes"},
		oneTooMany:  "lint-recipes takes any number of paths",
	},
	"export": {
		unknownFlag: []string{"export", "--frob"},
		flagAlone:   []string{"export", "--rows"},
		wrongForm:   []string{"export", "--rows", "rows.jsonl", "--format", "parquet"},
		leftOut:     []string{"export", "--rows", "rows.jsonl"},
		oneTooMany:  []string{"export", "--rows", "rows.jsonl", "--format", "focus-csv", "extra"},
	},
	"merged": {
		unknownFlag: []string{"merged", "--frob"},
		flagAlone:   []string{"merged", "--platform-url"},
		wrongForm:   []string{"merged", "--platform-url", "http://app.costroid.com", "--installation-id", "github:1"},
		leftOut:     []string{"merged"},
		oneTooMany:  []string{"merged", "--dry-run", "extra"},
	},
	"connect": {
		unknownFlag: []string{"connect", "--frob"},
		flagAlone:   []string{"connect", "--platform-url"},
		wrongForm:   []string{"connect", "--platform-url", "https://app.costroid.com", "--installation-id", "github:1"},
		leftOut:     []string{"connect"},
		oneTooMany:  []string{"connect", "--platform-url", "https://app.costroid.com", "extra"},
	},
	"emit-otlp": {
		unknownFlag: []string{"emit-otlp", "--frob"},
		flagAlone:   []string{"emit-otlp", "--rows"},
		wrongForm:   "its one flag takes any path",
		leftOut:     []string{"emit-otlp"},
		oneTooMany:  []string{"emit-otlp", "--rows", "rows.jsonl", "extra"},
	},
	"reconcile": {
		unknownFlag: []string{"reconcile", "--frob"},
		flagAlone:   []string{"reconcile", "--rows"},
		wrongForm:   []string{"reconcile", "--rows", "rows.jsonl", "--from", "2026-11-01", "--to", "2026-11-01", "--upstream-credential", "anthropic"},
		leftOut:     []string{"reconcile", "--rows", "rows.jsonl"},
		oneTooMany:  []string{"reconcile", "--rows", "rows.jsonl", "--from", "2026-11-01", "--to", "2026-11-01", "extra"},
	},
	"serve": {
		unknownFlag: []string{"serve", "--adr", ":1"},
		flagAlone:   []string{"serve", "--addr"},
		wrongForm:   []string{"serve", "--bind", "acme"},
		leftOut:     "serve needs no flag: what it needs is in the environment",
		oneTooMany:  []string{"serve", "extra"},
	},
	"keys": {
		unknownFlag: []string{"keys", "create", "--ownr", "github.com:example"},
		flagAlone:   []string{"keys", "create", "--owner"},
		wrongForm:   []string{"keys", "create", "--owner", "example"},
		leftOut:     []string{"keys", "revoke"},
		oneTooMany:  []string{"keys", "revoke", "a", "b"},
	},
	"egress": {
		unknownFlag: []string{"egress", "report", "--jsn"},
		flagAlone:   "its one flag takes no value",
		wrongForm:   []string{"egress", "report", "--json=maybe"},
		leftOut:     "egress report needs no flag",
		oneTooMany:  []string{"egress", "report", "extra"},
	},
	"gateway": {
		unknownFlag: []string{"gateway", "mint", "--budget", "5"},
		flagAlone:   []string{"gateway", "keygen", "--out"},
		wrongForm:   []string{"gateway", "close", "--url", "https://gateway.example", "--retry-after", "soon"},
		leftOut:     []string{"gateway", "keygen"},
		oneTooMany:  []string{"gateway", "public-key", "extra"},
	},
	"app": {
		unknownFlag: []string{"app", "manifest", "--org", "example"},
		flagAlone:   []string{"app", "manifest", "--name"},
		wrongForm:   []string{"app", "manifest", "--name", "Costroid", "--webhook-url", "http://costroid.example/webhooks/github"},
		leftOut:     []string{"app", "manifest"},
		oneTooMany:  []string{"app", "manifest", "--name", "Costroid", "--webhook-url", "https://costroid.example/webhooks/github", "extra"},
	},
}

// wrongLine runs a command line that is wrong and holds what it printed to
// the form of a mistake: nothing on stdout, what is wrong first under
// costroid's name, and the command's help named last.
func wrongLine(t *testing.T, args []string) int {
	t.Helper()
	code, stdout, stderr := execute(t, args...)
	lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
	if stdout != "" || !strings.HasPrefix(lines[0], "costroid: ") || !strings.HasPrefix(lines[len(lines)-1], "Run costroid help") {
		t.Errorf("%v: stdout %q, stderr:\n%s", args, stdout, stderr)
	}
	return code
}

// TestAWrongCommandLineExitsTwoOnEveryCommandButRun walks every command with
// each kind of wrong command line: none at all, a command or a subcommand
// that does not exist, a flag the command does not have, a flag without its
// value, a value of the wrong form, a required flag or argument left out and
// an argument too many. Each exits 2, in a repository, where init reads its
// flags.
func TestAWrongCommandLineExitsTwoOnEveryCommandButRun(t *testing.T) {
	clearCI(t)
	t.Chdir(gitRepository(t))
	if code, stdout, stderr := execute(t); code != cli.ExitUsage || stdout != usage.String() || stderr != "" {
		t.Errorf("no command: exit %d, stderr %q", code, stderr)
	}
	for _, args := range [][]string{{"frob"}, {"help", "frob"}, {"help", "demo", "extra"}, {"--version", "extra"}} {
		if code := wrongLine(t, args); code != cli.ExitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, cli.ExitUsage)
		}
	}
	for _, entry := range commands {
		lines, listed := wrongLines[entry.name]
		if !listed {
			t.Errorf("%s has no wrong command line to be walked with", entry.name)
			continue
		}
		if entry.name == "run" {
			continue
		}
		for _, kind := range wrongLineKinds {
			switch line := lines[kind].(type) {
			case []string:
				if code := wrongLine(t, line); code != cli.ExitUsage {
					t.Errorf("%s, %s: %v exits %d, want %d", entry.name, kind, line, code, cli.ExitUsage)
				}
			case string:
			default:
				t.Errorf("%s has neither a command line nor a reason for %s", entry.name, kind)
			}
		}
	}
	for _, name := range subcommandHelp {
		entry, _ := lookup(name)
		if code, stdout, stderr := execute(t, name); code != cli.ExitUsage || stdout == "" || stdout == entry.help || stderr != "" {
			t.Errorf("%s, %s: exit %d, stderr %q, stdout:\n%s", name, noSubcommand, code, stderr, stdout)
		}
		if code := wrongLine(t, []string{name, "frob"}); code != cli.ExitUsage {
			t.Errorf("%s, %s: exit %d, want %d", name, wrongSubcommand, code, cli.ExitUsage)
		}
	}
}

// TestRunKeepsItsOwnMistakesAboveTheAgentsCodes holds run's one exception.
// run passes the agent's code through, so an agent that exits 2 makes run
// exit 2, and each wrong command line of run itself exits 125 with a
// mistake's lines: a caller can tell the agent's 2 from costroid's.
func TestRunKeepsItsOwnMistakesAboveTheAgentsCodes(t *testing.T) {
	for _, kind := range wrongLineKinds {
		line, isLine := wrongLines["run"][kind].([]string)
		if !isLine {
			continue
		}
		if code := wrongLine(t, line); code != cli.ExitCouldNot {
			t.Errorf("%s: %v exits %d, want %d", kind, line, code, cli.ExitCouldNot)
		}
	}
	claude := replayClaude(t)
	agent := filepath.Join(t.TempDir(), "claude")
	writeFile(t, agent, "#!/bin/sh\n\""+claude+"\" \"$@\"\nexit "+strconv.Itoa(cli.ExitUsage)+"\n")
	if err := os.Chmod(agent, 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "rows.jsonl")
	code, _, stderr := execute(t, "run", "--budget-usd", "5", "--out", out, "--", agent, "-p", "work")
	if code != cli.ExitUsage || !strings.Contains(stderr, "rows in "+out) || strings.Contains(stderr, "Run costroid help") {
		t.Errorf("an agent that exits %d: run exits %d:\n%s", cli.ExitUsage, code, stderr)
	}
}

// TestACommandThatCouldNotDoItsWorkExits125 gives each command of the first
// run a command line that is right and work it cannot do: a file that is not
// there, a directory that is no repository, a job that is none, a host that
// refuses.
func TestACommandThatCouldNotDoItsWorkExits125(t *testing.T) {
	clearCI(t)
	directory := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(directory))
	t.Chdir(directory)
	writeFile(t, filepath.Join(directory, "rows.jsonl"), "")
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Server Error"}`, http.StatusInternalServerError)
	}))
	defer refusing.Close()
	t.Setenv("GITHUB_TOKEN", "ghs_example")
	t.Setenv("GITHUB_API_URL", refusing.URL)
	for _, variable := range []string{"COSTROID_DATABASE_URL", "COSTROID_MIGRATION_DATABASE_URL", sealingKeyVariable, platformVariable} {
		t.Setenv(variable, "")
	}
	for name, args := range map[string][]string{
		"lint, no workflows":               {"lint"},
		"lint, a path that is not there":   {"lint", "absent.yml"},
		"init, no repository":              {"init", "github", "--agent", "claude"},
		"validate, a file not there":       {"validate", "absent.yml"},
		"validate, a directory":            {"validate", directory},
		"run, no directory for the rows":   {"run", "--budget-usd", "5", "--out", filepath.Join("absent", "rows.jsonl"), "--", "claude", "-p", "work"},
		"policy, a rows file not there":    {"policy", "--rows", "absent.jsonl"},
		"policy, a directory for the file": {"policy", "--rows", "rows.jsonl", "--config", directory},
		"report, no CI system":             {"report", "--rows", "rows.jsonl"},
		"report, a preview with no rows":   {"report", "--preview", "--rows", "absent.jsonl"},
		"ingest-run, a source not there":   {"ingest-run", "--from", "execution-file=absent.json", "--out", "out.jsonl"},
		"ingest-run, no directory":         {"ingest-run", "--from", "codex-home=absent", "--out", "out.jsonl"},
		"follow-up, no GitHub Actions":     {"follow-up", "--rows", "rows.jsonl"},
		"approve, no GitHub Actions":       {"approve"},
		"doctor, --ci outside a job":       {"doctor", "--ci"},
		"result, a host that refuses":      {"result", "--pr", "7", "--repo", "example/widgets"},
		"lint-recipes, a path not there":   {"lint-recipes", "absent.yml"},
		"emit-otlp, a rows file not there": {"emit-otlp", "--rows", "absent.jsonl"},
		"review, no CI system":             {"review", "--post", "--file", "answer.md"},
		"merged, no GitHub Actions":        {"merged", "--dry-run"},
		"gateway, a variable not set":      {"gateway", "public-key"},
		"reconcile, a rows file not there": {"reconcile", "--rows", "absent.jsonl", "--from", "2026-11-01", "--to", "2026-11-01"},
		"export, a rows file not there":    {"export", "--rows", "absent.jsonl", "--format", "focus-csv"},
		"connect, no job and no id":        {"connect", "--platform-url", "https://app.costroid.com"},
		"serve, a variable not set":        {"serve"},
		"keys, a variable not set":         {"keys", "create", "--owner", "github.com:example"},
		"egress, a variable not set":       {"egress", "report"},
	} {
		code, stdout, stderr := execute(t, args...)
		if code != cli.ExitCouldNot || stdout != "" || !strings.HasPrefix(stderr, "costroid: ") || strings.Contains(stderr, "Run costroid help") {
			t.Errorf("%s: %v exits %d, stdout %q, stderr:\n%s", name, args, code, stdout, stderr)
		}
	}
}

// pageRow is a line the troubleshooting page quotes.
var pageRow = regexp.MustCompile("`costroid: ([^`]+)`")

// sectionLists reports whether the section of page under the heading that
// makes anchor quotes a line of the shape of line, where a word in angle
// brackets on the page stands for any text. A section runs to the next
// heading of its own level.
func sectionLists(page, anchor, line string) bool {
	inside := false
	for _, text := range strings.SplitAfter(page, "\n## ") {
		heading, _, _ := strings.Cut(text, "\n")
		if inside = anchors("## " + heading + "\n")[anchor]; !inside {
			continue
		}
		for _, row := range pageRow.FindAllStringSubmatch(text, -1) {
			parts := regexp.MustCompile(`<[a-z][a-z -]*>`).Split(row[1], -1)
			for i, part := range parts {
				parts[i] = regexp.QuoteMeta(part)
			}
			if row[1] != "<error>" && regexp.MustCompile("^"+strings.Join(parts, ".+")+"$").MatchString(line) {
				return true
			}
		}
	}
	return false
}

// TestAnErrorsAddressNamesTheSectionThatListsItsLine holds the address an
// error with no next step of its own ends on. Every section a command can
// name is a heading of the troubleshooting page. Then it walks errors of
// that kind: each ends on a section's address, never on the page alone, and
// the section quotes a line of the error's shape. The one row that stands
// for any error does not count as quoting it.
func TestAnErrorsAddressNamesTheSectionThatListsItsLine(t *testing.T) {
	page := readFile(t, filepath.Join(repositoryRoot, "docs", "troubleshooting.md"))
	headings := anchors(page)
	named := []string{cli.AnyCommand, otherCommands, "writing-rows-for-another-tool", "reading-a-gh-aw-run"}
	for _, entry := range commands {
		named = append(named, section(entry.name))
	}
	for name := range sections {
		if _, ok := lookup(name); !ok {
			t.Errorf("a section is named for %s, which is no command", name)
		}
	}
	for _, anchor := range named {
		if !headings[anchor] {
			t.Errorf("docs/troubleshooting.md has no heading that makes #%s", anchor)
		}
	}

	clearCI(t)
	directory := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(directory))
	t.Chdir(directory)
	writeFile(t, filepath.Join(directory, "rows.jsonl"), "")
	writeFile(t, filepath.Join(directory, "no-currency.jsonl"), `{"BillingCurrency":"dollars","ListCost":"1"}`+"\n")
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Server Error"}`, http.StatusInternalServerError)
	}))
	defer refusing.Close()
	t.Setenv("GITHUB_TOKEN", "ghs_example")
	t.Setenv("GITHUB_API_URL", refusing.URL)
	for _, variable := range []string{"COSTROID_DATABASE_URL", "COSTROID_MIGRATION_DATABASE_URL", sealingKeyVariable, platformVariable} {
		t.Setenv(variable, "")
	}
	for _, c := range []struct {
		args   []string
		anchor string
	}{
		{[]string{"lint", "absent.yml"}, cli.AnyCommand},
		{[]string{"lint-recipes", "absent.yml"}, cli.AnyCommand},
		{[]string{"validate", "absent.yml"}, cli.AnyCommand},
		{[]string{"validate", directory}, cli.AnyCommand},
		{[]string{"policy", "--rows", "absent.jsonl"}, cli.AnyCommand},
		{[]string{"policy", "--rows", "rows.jsonl", "--config", directory}, cli.AnyCommand},
		{[]string{"run", "--budget-usd", "5", "--out", filepath.Join("absent", "rows.jsonl"), "--", "claude", "-p", "work"}, cli.AnyCommand},
		{[]string{"ingest-run", "--from", "execution-file=absent.json", "--out", "out.jsonl"}, cli.AnyCommand},
		{[]string{"emit-otlp", "--rows", "absent.jsonl"}, cli.AnyCommand},
		{[]string{"reconcile", "--rows", "absent.jsonl", "--from", "2026-11-01", "--to", "2026-11-01"}, cli.AnyCommand},
		{[]string{"export", "--rows", "absent.jsonl", "--format", "focus-csv"}, cli.AnyCommand},
		{[]string{"ingest-run", "--from", "gh-aw=absent", "--out", "out.jsonl"}, "reading-a-gh-aw-run"},
		{[]string{"policy", "--rows", "no-currency.jsonl"}, otherCommands},
		{[]string{"run", "--budget-usd", "5", "--agent", "claude-code", "--out", "out.jsonl", "--", "npx"}, "running-the-agent"},
		{[]string{"result", "--quiet", "--pr", "7", "--repo", "example/widgets"}, otherCommands},
		{[]string{"gateway", "public-key"}, otherCommands},
		{[]string{"serve"}, otherCommands},
		{[]string{"keys", "create", "--owner", "github.com:example"}, otherCommands},
		{[]string{"egress", "report"}, otherCommands},
	} {
		code, _, stderr := execute(t, c.args...)
		lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
		first, found := strings.CutPrefix(lines[0], "costroid: ")
		want := "See " + cli.Troubleshooting + "#" + c.anchor + "."
		if code != cli.ExitCouldNot || !found || len(lines) != 2 || lines[1] != want {
			t.Errorf("%v: exit %d, and it ends on %q, want %q:\n%s", c.args, code, lines[len(lines)-1], want, stderr)
			continue
		}
		if !sectionLists(page, c.anchor, first) {
			t.Errorf("%v ends on #%s, and that section quotes no line of the shape of %q", c.args, c.anchor, lines[0])
		}
	}
}

// firstRun are the commands a person meets first.
var firstRun = []string{"demo", "lint", "init", "doctor", "validate", "run", "policy", "report", "ingest-run", "follow-up", "approve", "result"}

// nextStep is what the last line of an error names: a command, a page of the
// documentation or a recipe.
var nextStep = regexp.MustCompile(`\bcostroid [a-z]|` + regexp.QuoteMeta(cli.Documentation) + `/[a-z]|\brecipes/[a-z-]+\.yml`)

// TestAnErrorsFirstLineSaysWhatIsWrong holds the form of an error on each
// command of the first run: one line under costroid's name that names the
// thing as it was typed, with no command's own name before it, then at most
// two lines, the last of which names what to do next.
func TestAnErrorsFirstLineSaysWhatIsWrong(t *testing.T) {
	clearCI(t)
	directory := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(directory))
	t.Chdir(directory)
	writeFile(t, filepath.Join(directory, "rows.jsonl"), "")
	writeFile(t, filepath.Join(directory, "no-currency.jsonl"), "{}\n")
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Server Error"}`, http.StatusInternalServerError)
	}))
	defer refusing.Close()
	t.Setenv("GITHUB_API_URL", refusing.URL)
	walked := map[string]bool{}
	for _, check := range []struct {
		args  []string
		names string
		// then holds the lines under the first, where costroid says why
		// and what to do next in words of its own.
		then []string
		// token is the GitHub token the command reads.
		token string
	}{
		{args: []string{"demo", "--state", "passed"}, names: `"passed"`},
		{args: []string{"demo", "rows.jsonl"}, names: "demo takes no argument"},
		{args: []string{"lint"}, names: defaultWorkflows + " does not exist", then: []string{"lint reads the GitHub workflows of the repository it runs in.", "Run costroid lint at a repository's root, or name the files: costroid lint <paths>."}},
		{args: []string{"lint", "absent.yml"}, names: "absent.yml does not exist"},
		{args: []string{"init", "github", "--agent", "claude"}, names: "this directory is in no git repository", then: []string{"init writes at a repository's root.", "Run costroid init inside your repository."}},
		{args: []string{"init", "bitbucket", "--agent", "claude"}, names: `"bitbucket"`},
		{args: []string{"doctor", "path"}, names: `"path"`},
		{args: []string{"doctor", "--ci"}, names: "--ci"},
		{args: []string{"validate", "absent.yml"}, names: "absent.yml does not exist", then: []string{"See " + cli.Troubleshooting + "#" + cli.AnyCommand + "."}},
		{args: []string{"validate", "a.yml", "b.yml"}, names: "validate takes one path"},
		{args: []string{"run", "--budget-usd", "five", "--", "claude", "-p", "work"}, names: `--budget-usd is an amount in USD above 0, such as 5 or 2.50, not "five"`},
		{args: []string{"run", "--budget-usd", "5", "--", "frobnicate"}, names: "frobnicate is not an agent costroid run reads", then: []string{"It reads " + strings.Join(run.Agents, ", ") + ".", "Name the one the command starts with --agent.", "Run costroid help run to see its usage."}},
		{args: []string{"run", "--budget-usd", "5", "--out", filepath.Join("absent", "rows.jsonl"), "--", "claude", "-p", "work"}, names: filepath.Join("absent", "rows.jsonl") + " is in a directory that does not exist"},
		{args: []string{"policy"}, names: "policy needs --rows and takes no argument", then: []string{"Run costroid help policy to see its usage."}},
		{args: []string{"policy", "--rows", "rows.jsonl", "--at", "yesterday"}, names: `--at takes a time in RFC 3339, such as 2026-11-21T09:00:00Z, not "yesterday"`},
		{args: []string{"policy", "--rows", "absent.jsonl"}, names: "absent.jsonl does not exist"},
		{args: []string{"policy", "--rows", "no-currency.jsonl"}, names: "rows line 1: BillingCurrency is not an ISO code", then: []string{"See " + cli.Troubleshooting + "#" + otherCommands + "."}},
		{args: []string{"run", "--budget-usd", "5", "--agent", "claude-code", "--out", "out.jsonl", "--", "npx"}, names: "npx names no package or script", then: []string{"See " + cli.Troubleshooting + "#running-the-agent."}},
		{args: []string{"report"}, names: "report needs --rows"},
		{args: []string{"report", "--rows", "rows.jsonl"}, names: "report refused: neither GitHub Actions nor GitLab CI was detected", then: []string{"report posts from a job of one of them.", "Run costroid report --preview --rows FILE to print the comment here."}},
		{args: []string{"report", "--preview", "--rows", "absent.jsonl"}, names: "no rows file at absent.jsonl", then: []string{"costroid run writes one at --out, and costroid demo shows a comment without one."}},
		{args: []string{"ingest-run", "--out", "out.jsonl"}, names: "ingest-run needs --from KIND=PATH and --out"},
		{args: []string{"ingest-run", "--from", "execution-file=absent.json", "--out", "out.jsonl"}, names: "absent.json does not exist"},
		{args: []string{"follow-up"}, names: "follow-up needs --rows"},
		{args: []string{"follow-up", "--rows", "rows.jsonl"}, names: "follow-up refused: GitHub Actions was not detected", then: []string{"follow-up runs in a GitHub Actions job on the workflow_run event, as recipes/github-follow-up.yml does."}},
		{args: []string{"approve", "extra"}, names: "approve takes no arguments"},
		{args: []string{"approve"}, names: "approve refused: GitHub Actions was not detected", then: []string{"approve runs in a GitHub Actions job on the pull_request_review event, as recipes/github-approval.yml does."}},
		{args: []string{"result"}, names: "result needs --pr"},
		{args: []string{"result", "--pr", "7", "--repo", "example/widgets"}, names: "GITHUB_TOKEN is not set"},
		{args: []string{"result", "--quiet", "--pr", "7", "--repo", "example/widgets"}, token: "ghs_example", names: "read pull request 7: GET /repos/example/widgets/pulls/7 answered 500", then: []string{"See " + cli.Troubleshooting + "#" + otherCommands + "."}},
	} {
		walked[check.args[0]] = true
		t.Setenv("GITHUB_TOKEN", check.token)
		code, stdout, stderr := execute(t, check.args...)
		lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
		first, found := strings.CutPrefix(lines[0], "costroid: ")
		if code == 0 || stdout != "" || !found || !strings.Contains(first, check.names) {
			t.Errorf("%v: exit %d, stdout %q, and the first line does not say %q:\n%s", check.args, code, stdout, check.names, stderr)
			continue
		}
		if strings.HasPrefix(first, check.args[0]+": ") || strings.HasPrefix(first, "preview: ") {
			t.Errorf("%v: a command's name stands before what is wrong:\n%s", check.args, stderr)
		}
		if check.then != nil && (first != check.names || !slices.Equal(lines[1:], check.then)) {
			t.Errorf("%v prints:\n%swant %q, then:\n%s", check.args, stderr, check.names, strings.Join(check.then, "\n"))
		}
		if len(lines) < 2 || len(lines) > 4 || !nextStep.MatchString(lines[len(lines)-1]) {
			t.Errorf("%v: %d lines, and the last names no command, page or recipe:\n%s", check.args, len(lines), stderr)
		}
		for _, line := range lines[1:] {
			if strings.HasPrefix(line, "costroid:") || !strings.HasSuffix(line, ".") {
				t.Errorf("%v: %q is not a sentence under the first line:\n%s", check.args, line, stderr)
			}
		}
	}
	for _, name := range firstRun {
		if !walked[name] {
			t.Errorf("%s is a command of the first run, and no error of it is held", name)
		}
	}
}

// goErrorChain is how Go says what a system call answered: the call, the
// thing and the error, as in "open rows.jsonl: no such file or directory",
// `exec: "claude": executable file not found in $PATH` and
// `Get "https://host/": dial tcp`.
var goErrorChain = regexp.MustCompile(`\b(open|openat|stat|lstat|read|write|readdir|mkdir|remove|rename|chdir|fork/exec|exec|dial|lookup) [^\s:]+: |no such file or directory|executable file not found|(Get|Post|Put|Patch) "`)

// TestNoErrorPrintsAGoErrorChain takes from each command of the first run a
// file that is not there, a directory where a file goes, a file where a
// directory goes, a directory that is not there and a program that is not
// installed, and holds what the command prints to words: the file or the
// program as it was named, and never the call that failed.
func TestNoErrorPrintsAGoErrorChain(t *testing.T) {
	clearCI(t)
	directory := gitRepository(t)
	t.Chdir(directory)
	writeFile(t, filepath.Join(directory, "rows.jsonl"), "")
	writeFile(t, filepath.Join(directory, "file"), "")
	if err := os.Mkdir(filepath.Join(directory, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	event := filepath.Join(directory, "absent-event.json")
	inAJob := map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_EVENT_PATH": event, "GITHUB_REPOSITORY": "example/widgets", "GITHUB_TOKEN": "ghs_example", "GITHUB_SHA": strings.Repeat("a", 40)}
	walked := map[string]bool{}
	for _, check := range []struct {
		args []string
		env  map[string]string
	}{
		{args: []string{"lint"}},
		{args: []string{"lint", "absent.yml"}},
		{args: []string{"lint", filepath.Join("file", "below.yml")}},
		{args: []string{"validate", "absent.yml"}},
		{args: []string{"validate", "empty"}},
		{args: []string{"policy", "--rows", "absent.jsonl"}},
		{args: []string{"policy", "--rows", "empty"}},
		{args: []string{"policy", "--rows", "rows.jsonl", "--config", "empty"}},
		{args: []string{"policy", "--rows", "rows.jsonl", "--history", "absent.jsonl"}},
		{args: []string{"run", "--budget-usd", "5", "--out", filepath.Join("absent", "rows.jsonl"), "--", "claude", "-p", "work"}},
		{args: []string{"run", "--budget-usd", "5", "--out", "out.jsonl", "--", "claude", "-p", "work"}, env: map[string]string{"PATH": filepath.Join(directory, "empty")}},
		{args: []string{"report", "--preview", "--rows", "absent.jsonl"}},
		{args: []string{"report", "--preview", "--rows", "empty"}},
		{args: []string{"ingest-run", "--from", "execution-file=absent.json", "--out", "out.jsonl"}},
		{args: []string{"ingest-run", "--from", "execution-file=empty", "--out", "out.jsonl"}},
		{args: []string{"ingest-run", "--from", "codex-home=absent", "--out", "out.jsonl"}},
		{args: []string{"ingest-run", "--from", "gh-aw=absent", "--out", "out.jsonl"}},
		{args: []string{"ingest-run", "--from", "focus=absent.csv", "--out", "out.jsonl"}},
		{args: []string{"init", "github", "--agent", "claude", "--action-ref", strings.Repeat("a", 40), "--out", filepath.Join("file", "costroid.yml")}},
		{args: []string{"doctor", "--ci"}},
		{args: []string{"follow-up", "--rows", "rows.jsonl"}, env: inAJob},
		{args: []string{"approve"}, env: inAJob},
		{args: []string{"result", "--pr", "7"}, env: map[string]string{"GITHUB_TOKEN": "ghs_example"}},
		{args: []string{"demo", "--state", "passed"}},
	} {
		walked[check.args[0]] = true
		t.Run(strings.Join(check.args, " "), func(t *testing.T) {
			for name, value := range check.env {
				t.Setenv(name, value)
			}
			if check.env["GITHUB_ACTIONS"] != "" {
				t.Setenv("GITHUB_EVENT_NAME", map[string]string{"follow-up": followUpTrigger, "approve": approveEvent}[check.args[0]])
			}
			_, _, stderr := execute(t, check.args...)
			if stderr == "" {
				t.Fatal("the command printed nothing on stderr")
			}
			if chain := goErrorChain.FindString(stderr); chain != "" {
				t.Errorf("stderr holds %q:\n%s", chain, stderr)
			}
		})
	}
	for _, name := range firstRun {
		if !walked[name] {
			t.Errorf("%s is a command of the first run, and no failure of it is held", name)
		}
	}
}

// The names a command's code returns a code by, in this package and from
// internal/cli, and the row of an exit code table each one stands for.
var (
	exitNames    = map[string][]string{"exitRejected": {"1"}, "exitUsage": {"2"}, "exitVerdictNeutral": {"3"}, "exitBudgetStop": {"124"}, "exitFailure": {"125"}}
	cliExitNames = map[string][]string{"ExitFailed": {"1"}, "ExitUsage": {"2"}, "ExitCapped": {"124"}, "ExitCouldNot": {"125"}, "Fail": {"2", "125"}}
)

// commandSource is the code of the two packages that hold a command: this
// one and internal/server.
type commandSource struct {
	// funcs holds each function by package and name, and each method by
	// package, receiver and name.
	funcs map[string]*ast.FuncDecl
	// methods holds, by package and name, the methods of that name.
	methods map[string][]string
	// fields holds, by package and name, the names a struct gives a field.
	fields map[string]bool
	// roots holds, by command, the functions its line of the table names.
	roots map[string][]string
}

func readCommandSource(t *testing.T) commandSource {
	t.Helper()
	source := commandSource{funcs: map[string]*ast.FuncDecl{}, methods: map[string][]string{}, fields: map[string]bool{}, roots: map[string][]string{}}
	for pkg, dir := range map[string]string{"main": ".", "server": filepath.Join("..", "..", "internal", "server")} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(parsed, func(node ast.Node) bool {
				if fields, isStruct := node.(*ast.StructType); isStruct {
					for _, field := range fields.Fields.List {
						for _, name := range field.Names {
							source.fields[pkg+"."+name.Name] = true
						}
					}
				}
				return true
			})
			for _, decl := range parsed.Decls {
				switch decl := decl.(type) {
				case *ast.FuncDecl:
					source.readFunc(pkg, decl)
				case *ast.GenDecl:
					source.readTable(decl)
				}
			}
		}
	}
	return source
}

func (s commandSource) readFunc(pkg string, decl *ast.FuncDecl) {
	if decl.Body == nil {
		return
	}
	key := pkg + "." + decl.Name.Name
	if decl.Recv != nil {
		key = pkg + "." + typeName(decl.Recv.List[0].Type) + "." + decl.Name.Name
		s.methods[pkg+"."+decl.Name.Name] = append(s.methods[pkg+"."+decl.Name.Name], key)
	}
	s.funcs[key] = decl
}

// readTable reads the dispatch table: each line's name, and every function
// its run names.
func (s commandSource) readTable(decl *ast.GenDecl) {
	for _, spec := range decl.Specs {
		value, isValue := spec.(*ast.ValueSpec)
		if !isValue || len(value.Names) != 1 || value.Names[0].Name != "commands" || len(value.Values) != 1 {
			continue
		}
		table, isTable := value.Values[0].(*ast.CompositeLit)
		if !isTable {
			continue
		}
		for _, line := range table.Elts {
			entry, isEntry := line.(*ast.CompositeLit)
			if !isEntry {
				continue
			}
			name := ""
			var called []string
			for _, field := range entry.Elts {
				pair, isPair := field.(*ast.KeyValueExpr)
				if !isPair {
					continue
				}
				switch key := pair.Key.(*ast.Ident).Name; key {
				case "name":
					name, _ = strconv.Unquote(pair.Value.(*ast.BasicLit).Value)
				case "run":
					called = s.calls("main", pair.Value, map[string]bool{})
				}
			}
			s.roots[name] = called
		}
	}
}

// typeName is the name of a type written as T or *T, empty for any other.
func typeName(expression ast.Expr) string {
	if pointer, isPointer := expression.(*ast.StarExpr); isPointer {
		expression = pointer.X
	}
	if name, isName := expression.(*ast.Ident); isName {
		return name.Name
	}
	return ""
}

// receiverType is the type of a name the file declares with one: a
// parameter, a receiver, or a variable set to a literal of the type. It is
// empty for anything else.
func receiverType(expression ast.Expr) string {
	name, isName := expression.(*ast.Ident)
	if !isName || name.Obj == nil {
		return ""
	}
	switch decl := name.Obj.Decl.(type) {
	case *ast.Field:
		return typeName(decl.Type)
	case *ast.AssignStmt:
		for i, left := range decl.Lhs {
			if assigned, isAssigned := left.(*ast.Ident); !isAssigned || assigned.Name != name.Name || len(decl.Rhs) != len(decl.Lhs) {
				continue
			}
			value := decl.Rhs[i]
			if address, isAddress := value.(*ast.UnaryExpr); isAddress {
				value = address.X
			}
			if literal, isLiteral := value.(*ast.CompositeLit); isLiteral {
				return typeName(literal.Type)
			}
		}
	}
	return ""
}

// calls walks node and answers the functions and methods it names. It gives
// codes each exit code the node names. A method is found by its receiver's
// type where the file declares it; where it does not, every method of the
// name is answered, unless a struct has a field of that name, which the
// selector then reads.
func (s commandSource) calls(pkg string, node ast.Node, codes map[string]bool) []string {
	var called []string
	ast.Inspect(node, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.SelectorExpr:
			if from, isName := node.X.(*ast.Ident); isName && from.Name == "cli" {
				for _, code := range cliExitNames[node.Sel.Name] {
					codes[code] = true
				}
				return false
			} else if isName && from.Name == "server" && pkg == "main" {
				called = append(called, "server."+node.Sel.Name)
				return false
			}
			if receiver := receiverType(node.X); receiver != "" {
				called = append(called, pkg+"."+receiver+"."+node.Sel.Name)
			} else if !s.fields[pkg+"."+node.Sel.Name] {
				called = append(called, s.methods[pkg+"."+node.Sel.Name]...)
			}
			called = append(called, s.calls(pkg, node.X, codes)...)
			return false
		case *ast.Ident:
			if pkg == "main" {
				for _, code := range exitNames[node.Name] {
					codes[code] = true
				}
			}
			// A name the file binds to a variable is no function.
			if node.Obj == nil || node.Obj.Kind == ast.Fun {
				called = append(called, pkg+"."+node.Name)
			}
		case *ast.ReturnStmt:
			// A number a function returns alone is a code, named or not.
			if len(node.Results) == 1 {
				if literal, isLiteral := node.Results[0].(*ast.BasicLit); isLiteral && literal.Kind == token.INT {
					codes[literal.Value] = true
				}
			}
		}
		return true
	})
	return called
}

// codesOf answers every exit code the functions a command reaches name.
func (s commandSource) codesOf(command string) []string {
	codes := map[string]bool{}
	seen := map[string]bool{}
	queue := slices.Clone(s.roots[command])
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		seen[name] = true
		if fn, found := s.funcs[name]; found {
			pkg, _, _ := strings.Cut(name, ".")
			queue = append(queue, s.calls(pkg, fn.Body, codes)...)
		}
	}
	return slices.Sorted(maps.Keys(codes))
}

// pageCodes reads the codes a help page lists, in the page's order.
func pageCodes(page, command string) []string {
	var codes []string
	for line := range strings.SplitSeq(part(page, "Exit codes of "+command), "\n") {
		if row := exitRow.FindStringSubmatch(line); row != nil {
			codes = append(codes, row[1])
		}
	}
	return codes
}

// TestEveryHelpPageListsTheCodesItsCommandReturns reads the code of every
// command, from its line of the table through each function it reaches, for
// the exit codes it names, and holds the command's help page to that list:
// a code the command can exit with and the page leaves out fails, and so
// does a code the page lists and the command never names. run names no 0 and
// no 2 of its own: its page gives the agent's own code in their place.
func TestEveryHelpPageListsTheCodesItsCommandReturns(t *testing.T) {
	source := readCommandSource(t)
	if len(source.roots) != len(commands) {
		t.Fatalf("the table's source holds %d commands, and the table %d", len(source.roots), len(commands))
	}
	for _, entry := range commands {
		named := source.codesOf(entry.name)
		listed := pageCodes(entry.help, entry.name)
		if entry.name == "run" {
			if slices.Contains(named, strconv.Itoa(cli.ExitUsage)) {
				t.Errorf("run names exit %d, which is the agent's to return", cli.ExitUsage)
			}
			named = slices.DeleteFunc(named, func(code string) bool { return code == strconv.Itoa(cli.ExitDone) })
			listed = slices.DeleteFunc(listed, func(code string) bool { return code == "the agent's own" })
			if len(listed) == len(pageCodes(entry.help, entry.name)) {
				t.Error("help run does not say the agent's own code is run's")
			}
		}
		sorted := slices.Clone(listed)
		slices.SortFunc(sorted, func(a, b string) int {
			left, _ := strconv.Atoi(a)
			right, _ := strconv.Atoi(b)
			return left - right
		})
		slices.SortFunc(named, func(a, b string) int {
			left, _ := strconv.Atoi(a)
			right, _ := strconv.Atoi(b)
			return left - right
		})
		if !slices.Equal(sorted, named) {
			t.Errorf("help %s lists the exit codes %v, and its code names %v", entry.name, sorted, named)
		}
		if !slices.Equal(sorted, listed) {
			t.Errorf("help %s lists its exit codes out of order: %v", entry.name, listed)
		}
	}
}

// exitCodePage is the page of the documentation that states the contract.
const exitCodePage = "../../docs/exit-codes.md"

// TestTheExitCodePageMatchesTheCommands holds the documentation's table of
// command by code to the help pages: each command has one row, in the
// table's order, and the row marks a code exactly when the command's page
// lists it. The page also states each code's meaning in the words the help
// uses.
func TestTheExitCodePageMatchesTheCommands(t *testing.T) {
	page := readFile(t, exitCodePage)
	for _, sentence := range []string{cli.UsageExit[1], "the agent's own", "a wrong command line of `run` itself exits 125, where every other command exits 2"} {
		if !strings.Contains(strings.ToLower(page), sentence) {
			t.Errorf("%s does not say %q", exitCodePage, sentence)
		}
	}
	var header []string
	rows := map[string][]string{}
	var order []string
	for line := range strings.SplitSeq(page, "\n") {
		cells := strings.Split(strings.Trim(line, "| "), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		switch {
		case len(cells) < 2 || strings.HasPrefix(cells[0], "---"):
		case cells[0] == "Command":
			header = cells[1:]
		case header != nil && strings.HasPrefix(cells[0], "`costroid "):
			name := strings.TrimSuffix(strings.TrimPrefix(cells[0], "`costroid "), "`")
			order = append(order, name)
			for i, cell := range cells[1:] {
				if cell != "" && i < len(header) {
					rows[name] = append(rows[name], strings.Trim(header[i], "`"))
				}
			}
		}
	}
	if header == nil {
		t.Fatalf("%s has no table of command by code", exitCodePage)
	}
	var names []string
	for _, entry := range commands {
		names = append(names, entry.name)
		if listed := pageCodes(entry.help, entry.name); !slices.Equal(rows[entry.name], listed) {
			t.Errorf("%s gives costroid %s the codes %v, and its help lists %v", exitCodePage, entry.name, rows[entry.name], listed)
		}
	}
	if !slices.Equal(order, names) {
		t.Errorf("%s lists the commands as %v, and the table as %v", exitCodePage, order, names)
	}
}
