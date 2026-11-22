package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Costroid/costroid/internal/cli"
	"github.com/Costroid/costroid/internal/gateway"
	"github.com/Costroid/costroid/internal/gateway/gatewaytest"
)

// flagSources are the directories whose flag sets back a command of the
// table.
var flagSources = []string{".", filepath.Join("..", "..", "internal", "server")}

// flagless are the commands that define no flag of their own. Each takes
// --quiet, which internal/cli gives every command.
var flagless = []string{"approve", "lint-recipes", "receipt"}

// computedFlags gives the names of flags a command defines from a table of its
// own, rather than as a literal.
var computedFlags = map[string]func() []string{
	"ingest-run": func() []string {
		var names []string
		for _, kind := range sourceKinds {
			if kind.alias {
				names = append(names, kind.name)
			}
		}
		return names
	},
}

// subcommandHelp are the commands that also answer help as a subcommand.
var subcommandHelp = []string{"gateway", "keys", "egress", "app"}

// helpAfterFlags gives each command whose flag set lives in this package a
// command line that sets one flag before help is asked for.
var helpAfterFlags = map[string][][]string{
	"run":        {{"run", "--out", "rows.jsonl"}},
	"ingest-run": {{"ingest-run", "--out", "rows.jsonl"}},
	"report":     {{"report", "--rows", "rows.jsonl"}},
	"demo":       {{"demo", "--markdown"}},
	"init":       {{"init", "--force"}},
	"doctor":     {{"doctor", "--ci"}},
	"follow-up":  {{"follow-up", "--rows", "rows.jsonl"}},
	"approve":    {{"approve"}},
	"reconcile":  {{"reconcile", "--rows", "rows.jsonl"}},
	"export":     {{"export", "--rows", "rows.jsonl"}},
	"merged":     {{"merged", "--dry-run"}},
	"connect":    {{"connect", "--platform-url", "https://platform.example"}},
	"review":     {{"review", "--keep"}},
	"policy":     {{"policy", "--rows", "rows.jsonl"}},
	"lint":       {{"lint", "--strict"}},
	"gateway": {
		{"gateway", "keygen", "--out", "sealing.key"},
		{"gateway", "request", "--out", "request"},
		{"gateway", "mint", "--url", "https://gateway.example"},
		{"gateway", "close", "--url", "https://gateway.example"},
	},
	"result":    {{"result", "--pr", "1"}},
	"validate":  {{"validate", "--json"}},
	"version":   {{"version", "--verbose"}},
	"mcp":       {{"mcp", "--repo", "owner/name"}},
	"emit-otlp": {{"emit-otlp", "--rows", "rows.jsonl"}},
	"app":       {{"app", "manifest", "--name", "Costroid"}, {"app", "create", "--name", "Costroid"}},
}

// serverFlagSets are the commands whose flag sets live in internal/server.
var serverFlagSets = []string{"serve", "keys", "egress"}

func TestHelpAfterFlagsPrintsOnStdout(t *testing.T) {
	for _, entry := range commands {
		lines, ok := helpAfterFlags[entry.name]
		if !ok {
			if !slices.Contains(flagless, entry.name) && !slices.Contains(serverFlagSets, entry.name) {
				t.Errorf("%s defines flags, and helpAfterFlags gives it no command line", entry.name)
			}
			continue
		}
		for _, line := range lines {
			for _, ask := range []string{"-h", "-help", "--help"} {
				args := append(slices.Clone(line), ask)
				code, stdout, stderr := execute(t, args...)
				if code != 0 || stdout != entry.help || stderr != "" {
					t.Errorf("%v: exit %d, stderr %q, stdout:\n%s", args, code, stderr, stdout)
				}
			}
		}
	}
}

// screenLines is the height of the screen the bare call must fit.
const screenLines = 24

// TestBareCallFitsOneScreenAndListsEveryCommandOnce holds what costroid alone
// prints: a line of purpose, each command once under a group with what it
// does and none in a list, a command that needs nothing set up, how to read
// one command's help and where the documentation is, in one screen.
func TestBareCallFitsOneScreenAndListsEveryCommandOnce(t *testing.T) {
	code, stdout, stderr := execute(t)
	if code != exitUsage || stderr != "" || stdout != usage.String() {
		t.Fatalf("exit %d, stderr %q, stdout:\n%s", code, stderr, stdout)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) > screenLines {
		t.Errorf("the bare call prints %d lines, more than a screen of %d", len(lines), screenLines)
	}
	for number, line := range lines {
		if width := utf8.RuneCountInString(line); width > usageWidth {
			t.Errorf("line %d is %d columns: %s", number+1, width, line)
		}
	}
	if !oneSentence.MatchString(lines[0]) || lines[1] != "" {
		t.Errorf("the bare call does not open with one line of purpose: %q, %q", lines[0], lines[1])
	}
	var names []string
	for _, entry := range commands {
		names = append(names, regexp.QuoteMeta(entry.name))
		cell := regexp.MustCompile(`(?m)(?:^| {2})` + regexp.QuoteMeta(entry.name) + ` {2,}` + regexp.QuoteMeta(entry.does) + `(?: {2}|$)`)
		if found := len(cell.FindAllString(stdout, -1)); found != 1 {
			t.Errorf("%s stands beside what it does %d times, want once:\n%s", entry.name, found, stdout)
		}
		if int(entry.where) >= len(groupNames) {
			t.Errorf("%s is in no group", entry.name)
		}
	}
	name := `(?:` + strings.Join(names, "|") + `)`
	if list := regexp.MustCompile(`\b` + name + `,? (?:and )?` + name + `\b`).FindString(strings.Join(lines[:len(lines)-3], "\n")); list != "" {
		t.Errorf("the bare call lists commands with no word on what each does: %q", list)
	}
	for _, heading := range groupNames {
		if strings.Count(stdout, heading) != 1 {
			t.Errorf("the group %q does not head its commands once:\n%s", heading, stdout)
		}
	}
	for _, want := range []string{"  costroid demo\n", "  costroid help <command>\n", "  " + cli.Documentation + "\n"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the bare call lacks %q:\n%s", want, stdout)
		}
	}
}

// oneSentence is a line that ends its one sentence where the line ends.
var oneSentence = regexp.MustCompile(`^[^.]*(?:\.\S[^.]*)*\.$`)

// pageHeading is a heading of a help page: every other line below the first
// paragraph is indented.
var pageHeading = regexp.MustCompile(`(?m)^(\S.*):$`)

// pageParts cuts a help page into its first paragraph and the body under each
// heading, in order.
func pageParts(page string) (summary string, headings, bodies []string) {
	summary, rest, _ := strings.Cut(page, "\n\n")
	summary += "\n\n"
	found := pageHeading.FindAllStringSubmatchIndex(rest, -1)
	for i, at := range found {
		end := len(rest)
		if i+1 < len(found) {
			end = found[i+1][0]
		}
		headings = append(headings, rest[at[2]:at[3]])
		bodies = append(bodies, rest[at[1]+1:end])
	}
	return summary, headings, bodies
}

// part answers the body under heading, empty when the page has none.
func part(page, heading string) string {
	_, headings, bodies := pageParts(page)
	if i := slices.Index(headings, heading); i >= 0 {
		return bodies[i]
	}
	return ""
}

// quietLast finds each flag's row under Flags, so that the last can be held
// to --quiet, which every command takes.
var quietLast = regexp.MustCompile(`(?m)^  -.*$`)

// TestEveryHelpPageHasItsPartsInOrder holds each page to one sentence of what
// the command does, its usage, at least one example, a line for each flag,
// then what the environment gives it, its exit codes and the address of its
// page in the documentation, with whatever else it says under headings
// between the flags and the environment.
func TestEveryHelpPageHasItsPartsInOrder(t *testing.T) {
	flags := commandFlags(t)
	for _, entry := range commands {
		t.Run(entry.name, func(t *testing.T) {
			summary, headings, bodies := pageParts(entry.help)
			if sentence := strings.Join(strings.Fields(summary), " "); !oneSentence.MatchString(sentence) || !strings.HasPrefix(sentence, entry.name+" ") {
				t.Errorf("the page does not open with one sentence of what %s does:\n%s", entry.name, summary)
			}
			if len(headings) == 0 || !strings.HasPrefix(entry.help, summary+headings[0]+":\n") {
				t.Fatalf("the first heading does not stand one blank line below the first sentence:\n%s", entry.help)
			}
			for i, body := range bodies {
				for line := range strings.SplitSeq(strings.TrimSuffix(body, "\n"), "\n") {
					if line != "" && !strings.HasPrefix(line, "  ") {
						t.Errorf("under %q, a line is not indented: %q", headings[i], line)
					}
				}
				if strings.TrimSpace(body) == "" {
					t.Errorf("nothing stands under %q", headings[i])
				}
				if i+1 < len(bodies) && !strings.HasSuffix(body, "\n\n") || strings.HasSuffix(body, "\n\n\n") {
					t.Errorf("%q is not one blank line above the next heading", headings[i])
				}
			}
			want := []string{"Usage", "Examples", "Flags"}
			if rows := quietLast.FindAllString(part(entry.help, "Flags"), -1); len(rows) == 0 || !strings.HasPrefix(rows[len(rows)-1], "  "+cli.QuietRow[0]+"  ") {
				t.Errorf("the page's flags do not end on %s:\n%s", cli.QuietRow[0], part(entry.help, "Flags"))
			}
			if len(headings) < len(want) || !slices.Equal(headings[:len(want)], want) {
				t.Fatalf("the page opens with the headings %q, want %q", headings, want)
			}
			last := []string{"Exit codes of " + entry.name, "Documentation"}
			rest := headings[len(want):]
			if len(rest) < len(last) || !slices.Equal(rest[len(rest)-len(last):], last) {
				t.Fatalf("the page ends with the headings %q, want %q", rest, last)
			}
			between := rest[:len(rest)-len(last)]
			for i, heading := range between {
				if slices.Contains(append(slices.Clone(want), "Flags"), heading) || strings.HasPrefix(heading, "Exit codes") ||
					heading == "Environment" && i != len(between)-1 || slices.Index(between, heading) != i {
					t.Errorf("the heading %q is out of its place among %q", heading, headings)
				}
			}
			for i, form := range strings.Split(strings.TrimSuffix(part(entry.help, "Usage"), "\n\n"), "\n") {
				if i == 0 && !strings.HasPrefix(form, "  costroid "+entry.name) || !strings.HasPrefix(form, "  costroid ") && !strings.HasPrefix(form, "    ") {
					t.Errorf("a usage line is no form of the command: %q", form)
				}
			}
			if len(pageExamples(entry.help)) == 0 {
				t.Error("the page has no example")
			}
			for _, defined := range flags[entry.name] {
				if !regexp.MustCompile(`(?m)^  --` + regexp.QuoteMeta(defined.name) + `(?: \S+)?(?: {2,}\S|$)`).MatchString(part(entry.help, "Flags")) {
					t.Errorf("--%s has no line under Flags", defined.name)
				}
			}
			address := strings.TrimSpace(part(entry.help, "Documentation"))
			name, found := strings.CutPrefix(address, cli.Documentation+"/")
			if !found || strings.ContainsAny(name, "/ \n") {
				t.Fatalf("the page ends on %q, which is no page under %s", address, cli.Documentation)
			}
			if _, err := os.Stat(filepath.Join(repositoryRoot, "docs", name+".md")); err != nil {
				t.Errorf("the page's address names %s, and docs holds no such page: %v", name, err)
			}
		})
	}
}

func TestEveryHelpPageFitsEightyColumns(t *testing.T) {
	pages := map[string]string{"the bare call": usage.String()}
	for _, entry := range commands {
		pages["help "+entry.name] = entry.help
	}
	for name, page := range pages {
		for number, line := range strings.Split(page, "\n") {
			if width := utf8.RuneCountInString(line); width > usageWidth {
				t.Errorf("%s, line %d, is %d columns: %s", name, number+1, width, line)
			}
			if strings.TrimRight(line, " ") != line {
				t.Errorf("%s, line %d, ends in a space: %q", name, number+1, line)
			}
		}
	}
}

// TestHelpIsTheSameBytesThreeWays asks for each command's help as costroid
// help <command>, <command> --help and <command> -h, and wants the page each
// time, whole, on stdout, with exit 0.
func TestHelpIsTheSameBytesThreeWays(t *testing.T) {
	for _, entry := range commands {
		t.Run(entry.name, func(t *testing.T) {
			if strings.TrimSpace(entry.help) == "" {
				t.Fatal("the command has no help page")
			}
			asks := [][]string{{"help", entry.name}, {entry.name, "--help"}, {entry.name, "-h"}, {entry.name, "-help"}}
			if slices.Contains(subcommandHelp, entry.name) {
				asks = append(asks, []string{entry.name, "help"})
			}
			for _, args := range asks {
				code, stdout, stderr := execute(t, args...)
				if code != 0 || stdout != entry.help || stderr != "" {
					t.Errorf("%v: exit %d, stderr %q, stdout:\n%s", args, code, stderr, stdout)
				}
			}
		})
	}
}

// exitRow is one row of an exit code table: the code, then what it means from
// column 20. A row's meaning goes on at column 20 on the lines after it.
var (
	exitRow      = regexp.MustCompile(`^  (\d+|the agent's own) +\S`)
	exitRowWrap  = regexp.MustCompile(`^ {20}\S`)
	exitRowStart = 20
)

func TestEveryHelpStatesItsExitCodes(t *testing.T) {
	for _, entry := range commands {
		table := strings.TrimSuffix(part(entry.help, "Exit codes of "+entry.name), "\n\n")
		if table == "" {
			t.Errorf("help %s has no exit code table", entry.name)
			continue
		}
		rows := strings.Split(table, "\n")
		if !exitRow.MatchString(rows[0]) {
			t.Errorf("help %s: the table opens with %q", entry.name, rows[0])
		}
		for _, line := range rows {
			if len(line) <= exitRowStart || line[exitRowStart-1] != ' ' || line[exitRowStart] == ' ' ||
				!exitRow.MatchString(line) && !exitRowWrap.MatchString(line) {
				t.Errorf("help %s: %q is not a row of the exit code table", entry.name, line)
			}
		}
	}
}

func TestNoArgumentsExitsTwo(t *testing.T) {
	code, stdout, stderr := execute(t)
	if code != 2 || stderr != "" || stdout != usage.String() {
		t.Errorf("exit %d, stderr %q, stdout:\n%s", code, stderr, stdout)
	}
}

// pageExamples reads the command lines under a page's Examples: each line
// that opens with costroid, joined with the lines a backslash carries it on
// to. A line that ends in a colon says what the lines below it are for.
func pageExamples(page string) []string {
	var examples []string
	carried := false
	for line := range strings.SplitSeq(strings.TrimSpace(part(page, "Examples")), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case carried:
			examples[len(examples)-1] += " " + strings.TrimSuffix(line, `\`)
		case strings.HasSuffix(line, ":"):
			continue
		default:
			examples = append(examples, strings.TrimSuffix(line, `\`))
		}
		carried = strings.HasSuffix(line, `\`)
	}
	return examples
}

// shellWords cuts a command line into its words as a shell would, as far as
// the examples go: quotes group, and a redirection ends the command.
func shellWords(t *testing.T, line string) []string {
	t.Helper()
	var words []string
	word, quote, open := "", rune(0), false
	for _, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				word += string(r)
			}
		case r == '"' || r == '\'':
			quote, open = r, true
		case r == ' ':
			if open {
				words = append(words, word)
			}
			word, open = "", false
		default:
			word += string(r)
			open = true
		}
	}
	if quote != 0 {
		t.Fatalf("%q leaves a quote open", line)
	}
	if open {
		words = append(words, word)
	}
	if end := slices.Index(words, ">"); end >= 0 {
		words = words[:end]
	}
	return words
}

// usageForm is one way to call a command, read from a usage line: the words
// it must hold as they are, the flags it must and may hold, whether the flags
// end with --, and how many values it needs beside them.
type usageForm struct {
	words    []string
	required []string
	allowed  []string
	dashes   bool
	values   int
}

// usageForms reads the forms under a page's Usage. A word in brackets is
// optional; a flag's value follows it.
func usageForms(page string, valued func(name string) bool) []usageForm {
	var texts []string
	for line := range strings.SplitSeq(strings.TrimSuffix(part(page, "Usage"), "\n\n"), "\n") {
		if strings.HasPrefix(line, "    ") {
			texts[len(texts)-1] += " " + strings.TrimSpace(line)
		} else {
			texts = append(texts, strings.TrimSpace(line))
		}
	}
	var forms []usageForm
	for _, text := range texts {
		form, depth, skip, inside := usageForm{}, 0, false, false
		for _, word := range strings.Fields(text)[2:] {
			outer := depth == 0 && !strings.HasPrefix(word, "[")
			depth += strings.Count(word, "[") - strings.Count(word, "]")
			bare := strings.Trim(word, "[].|")
			switch {
			case bare == "":
			case inside:
				inside = !strings.HasSuffix(bare, ">")
			case bare == "--":
				form.dashes = form.dashes || outer
			case strings.HasPrefix(bare, "--"):
				name := strings.TrimPrefix(bare, "--")
				form.allowed = append(form.allowed, name)
				if outer {
					form.required = append(form.required, name)
				}
				skip = valued(name)
				continue
			case skip:
			case !outer:
			case strings.HasPrefix(bare, "<") || strings.ToUpper(bare) == bare:
				form.values++
				inside = strings.HasPrefix(bare, "<") && !strings.HasSuffix(bare, ">")
			default:
				form.words = append(form.words, bare)
			}
			skip = false
		}
		forms = append(forms, form)
	}
	return forms
}

// TestEveryHelpExampleParsesWithItsCommandsFlags reads each example of each
// page as a command line. Its command must be in the table. Its flags must
// parse with the command's own flag set, which the test reaches by asking for
// help where the flags end, so nothing runs. And one of the command's usage
// forms must hold it: every flag that form requires, and no flag it lacks.
func TestEveryHelpExampleParsesWithItsCommandsFlags(t *testing.T) {
	flags := commandFlags(t)
	for _, entry := range commands {
		for _, example := range pageExamples(entry.help) {
			t.Run(example, func(t *testing.T) {
				words := shellWords(t, example)
				if len(words) < 2 || words[0] != "costroid" {
					t.Fatalf("the example is no costroid command line")
				}
				called, ok := lookup(words[1])
				if !ok {
					t.Fatalf("the example calls %s, which is no command", words[1])
				}
				boolean := map[string]bool{}
				for _, defined := range flags[called.name] {
					boolean[defined.name] = defined.boolean
				}
				valued := func(name string) bool { return !boolean[name] }
				var given, values []string
				args, end, dashes := words[2:], 0, false
				for i := 0; i < len(args); i++ {
					arg := args[i]
					switch name, _, inline := strings.Cut(strings.TrimPrefix(arg, "--"), "="); {
					case arg == "--":
						dashes = true
						values = append(values, args[i+1:]...)
						i = len(args)
					case !strings.HasPrefix(arg, "--"):
						values = append(values, arg)
					default:
						if _, defined := boolean[name]; !defined {
							t.Fatalf("the example gives --%s, which %s does not have", name, called.name)
						}
						given = append(given, name)
						if valued(name) && !inline {
							i++
							if i >= len(args) {
								t.Fatalf("--%s has no value", name)
							}
						}
						end = i + 1
					}
				}
				if end > 0 {
					code, stdout, stderr := execute(t, append(slices.Concat([]string{called.name}, args[:end]), "--help")...)
					if code != 0 || stdout+stderr != called.help {
						t.Fatalf("%s does not parse the example's flags: exit %d, %s", called.name, code, stderr)
					}
				}
				var why []string
				for _, form := range usageForms(called.help, valued) {
					missing := slices.DeleteFunc(slices.Clone(form.required), func(name string) bool { return slices.Contains(given, name) })
					extra := slices.DeleteFunc(slices.Clone(given), func(name string) bool { return slices.Contains(form.allowed, name) })
					words := slices.DeleteFunc(slices.Clone(form.words), func(word string) bool { return slices.Contains(values, word) })
					switch {
					case len(missing) > 0:
						why = append(why, fmt.Sprintf("it lacks --%s", strings.Join(missing, ", --")))
					case len(extra) > 0:
						why = append(why, fmt.Sprintf("the form has no --%s", strings.Join(extra, ", --")))
					case len(words) > 0:
						why = append(why, fmt.Sprintf("it lacks %s", strings.Join(words, ", ")))
					case form.dashes && !dashes:
						why = append(why, "it lacks --")
					case len(values)-len(form.words) < form.values:
						why = append(why, fmt.Sprintf("it gives %d values of %d", len(values)-len(form.words), form.values))
					default:
						return
					}
				}
				t.Errorf("no usage form of %s holds the example: %s", called.name, strings.Join(why, "; "))
			})
		}
	}
}

// flagMistakes are command lines with one flag wrong, and the lines each
// prints: the flag as it was typed and what is wrong with it, the nearest flag
// when one is near, and the command whose help lists the flags.
var flagMistakes = []struct {
	args  []string
	lines []string
}{
	{[]string{"run", "--frob"}, []string{"costroid: --frob is not a flag of costroid run", "Run costroid help run to list its flags."}},
	{[]string{"run", "-budget", "5"}, []string{"costroid: -budget is not a flag of costroid run", "The nearest flag is --budget-usd.", "Run costroid help run to list its flags."}},
	{[]string{"run", "--buget-usd=5"}, []string{"costroid: --buget-usd is not a flag of costroid run", "The nearest flag is --budget-usd.", "Run costroid help run to list its flags."}},
	{[]string{"report", "--preview", "--rows"}, []string{"costroid: --rows needs a value", "Run costroid help report to list its flags."}},
	{[]string{"result", "--pr", "abc"}, []string{`costroid: --pr takes a whole number, not "abc"`, "Run costroid help result to list its flags."}},
	{[]string{"report", "--json=maybe"}, []string{`costroid: --json takes true or false, not "maybe"`, "Run costroid help report to list its flags."}},
	{[]string{"gateway", "close", "--retry-after", "soon"}, []string{`costroid: --retry-after takes a duration, such as 30s, not "soon"`, "Run costroid help gateway to list its flags."}},
	{[]string{"ingest-run", "--from", "nowhere"}, []string{"costroid: --from: takes KIND=PATH, with KIND one of execution-file, codex-home, gh-aw or focus", "Run costroid help ingest-run to list its flags."}},
	{[]string{"gateway", "mint", "--budget", "5"}, []string{"costroid: --budget is not a flag of costroid gateway mint", "The nearest flag is --budget-usd.", "Run costroid help gateway to list its flags."}},
	{[]string{"init", "github", "--agnet", "claude"}, []string{"costroid: --agnet is not a flag of costroid init", "The nearest flag is --agent.", "Run costroid help init to list its flags."}},
	{[]string{"doctor", "--cli"}, []string{"costroid: --cli is not a flag of costroid doctor", "The nearest flag is --ci.", "Run costroid help doctor to list its flags."}},
	{[]string{"validate", "--frob"}, []string{"costroid: --frob is not a flag of costroid validate", "Run costroid help validate to list its flags."}},
	{[]string{"serve", "--adr", ":1"}, []string{"costroid: --adr is not a flag of costroid serve", "The nearest flag is --addr.", "Run costroid help serve to list its flags."}},
	{[]string{"keys", "create", "--ownr", "github.com:example"}, []string{"costroid: --ownr is not a flag of costroid keys create", "The nearest flag is --owner.", "Run costroid help keys to list its flags."}},
	{[]string{"egress", "report", "--jsn"}, []string{"costroid: --jsn is not a flag of costroid egress report", "The nearest flag is --json.", "Run costroid help egress to list its flags."}},
}

// TestAFlagMistakeNamesTheFlagAsTypedAndNeverPrintsTheHelp holds what a wrong
// flag prints, on every command that takes flags: its own lines on stderr, the
// flag first, and never the help page or the flag package's own message.
func TestAFlagMistakeNamesTheFlagAsTypedAndNeverPrintsTheHelp(t *testing.T) {
	for _, mistake := range flagMistakes {
		code, stdout, stderr := execute(t, mistake.args...)
		if want := strings.Join(mistake.lines, "\n") + "\n"; code != mistakeExit(mistake.args[0]) || stdout != "" || stderr != want {
			t.Errorf("%v: exit %d, stdout %q, stderr:\n%swant:\n%s", mistake.args, code, stdout, stderr, want)
		}
	}
	lines := map[string][][]string{"serve": {{"serve"}}, "keys": {{"keys", "create"}}, "egress": {{"egress", "report"}}, "validate": {{"validate"}}, "lint-recipes": {{"lint-recipes"}}, "receipt": {{"receipt"}}}
	for name, given := range helpAfterFlags {
		lines[name] = given
	}
	for _, entry := range commands {
		if len(lines[entry.name]) == 0 && entry.name != "version" {
			t.Errorf("%s has no command line to get a flag wrong on", entry.name)
		}
		for _, line := range lines[entry.name] {
			args := append(slices.Clone(line), "--no-such-flag")
			code, stdout, stderr := execute(t, args...)
			said := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
			if code != mistakeExit(entry.name) || stdout != "" || len(said) != 2 ||
				!strings.HasPrefix(said[0], "costroid: --no-such-flag is not a flag of costroid "+entry.name) ||
				said[1] != "Run costroid help "+entry.name+" to list its flags." {
				t.Errorf("%v: exit %d, stdout %q, stderr:\n%s", args, code, stdout, stderr)
			}
			if strings.Contains(stderr, "Usage:") || strings.Contains(stderr, "flag provided but not defined") {
				t.Errorf("%v prints the help page or the flag package's message:\n%s", args, stderr)
			}
		}
	}
}

// TestAGroupCommandAloneListsItsSubcommands holds what a command that takes a
// subcommand prints with none: the sentence its help page opens with, each
// subcommand of its usage once with what it does, one of the page's own
// examples and where the whole page is, on stdout, in one screen. A mistyped
// subcommand gets what a mistyped command gets.
func TestAGroupCommandAloneListsItsSubcommands(t *testing.T) {
	for name, mistyped := range map[string][2]string{
		"gateway": {"mnit", "The nearest command is mint."},
		"app":     {"frob", "No command is near it."},
		"keys":    {"crate", "The nearest command is create."},
		"egress":  {"reprot", "The nearest command is report."},
	} {
		entry, _ := lookup(name)
		code, stdout, stderr := execute(t, name)
		if code != exitUsage || stderr != "" {
			t.Fatalf("%s: exit %d, stderr %q", name, code, stderr)
		}
		summary, _, _ := pageParts(entry.help)
		if !strings.HasPrefix(stdout, summary) {
			t.Errorf("%s alone does not open with the sentence its help opens with:\n%s", name, stdout)
		}
		lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
		if len(lines) > screenLines || lines[len(lines)-1] != "Help for this command:  costroid help "+name {
			t.Errorf("%s alone prints %d lines, and ends on %q", name, len(lines), lines[len(lines)-1])
		}
		for _, line := range lines {
			if utf8.RuneCountInString(line) > usageWidth {
				t.Errorf("%s alone prints a line of %d columns: %s", name, utf8.RuneCountInString(line), line)
			}
		}
		subcommands := 0
		for _, form := range usageForms(entry.help, func(string) bool { return true }) {
			if len(form.words) == 0 {
				t.Fatalf("a usage form of %s names no subcommand", name)
			}
			subcommands++
			if found := len(regexp.MustCompile(`(?m)^  `+regexp.QuoteMeta(form.words[0])+` {2,}\S`).FindAllString(stdout, -1)); found != 1 {
				t.Errorf("%s alone gives %s a line %d times, want once:\n%s", name, form.words[0], found, stdout)
			}
		}
		if listed := len(regexp.MustCompile(`(?m)^  [a-z-]+ {2,}\S`).FindAllString(stdout, -1)); listed != subcommands {
			t.Errorf("%s alone lists %d subcommands, and its usage has %d", name, listed, subcommands)
		}
		_, example, found := strings.Cut(stdout, "\nExample:\n")
		example, _, _ = strings.Cut(example, "\n\n")
		joined := strings.Join(strings.Fields(strings.ReplaceAll(example, "\\\n", " ")), " ")
		held := slices.ContainsFunc(pageExamples(entry.help), func(line string) bool { return strings.Join(strings.Fields(line), " ") == joined })
		if !found || !held {
			t.Errorf("%s alone shows an example its help page does not hold: %q", name, joined)
		}
		if code, page, _ := execute(t, "help", name); code != 0 || page != entry.help || page == stdout {
			t.Errorf("costroid help %s does not print the whole page: exit %d", name, code)
		}
		code, stdout, stderr = execute(t, name, mistyped[0])
		want := fmt.Sprintf("costroid: unknown %s command %q\n%s\nRun costroid help %s to list its commands.\n", name, mistyped[0], mistyped[1], name)
		if code != exitUsage || stdout != "" || stderr != want {
			t.Errorf("%s %s: exit %d, stdout %q, stderr:\n%swant:\n%s", name, mistyped[0], code, stdout, stderr, want)
		}
	}
}

// TestValidatesHelpSaysWhatValidateDoes runs validate on each value its help
// names: the amounts it calls valid, the amount it calls a problem, and the
// float it says approval.expires_days may not be.
func TestValidatesHelpSaysWhatValidateDoes(t *testing.T) {
	help := strings.Join(strings.Fields(validateHelp), " ")
	amounts := `A policy amount is a plain decimal, quoted or not: 5, 5.0 and "2.50" are valid, and an amount written another way, such as 1e3, is a problem.`
	days := "An approval.expires_days that YAML reads as a float, such as 7.0, is a problem too, because the check refuses it."
	for _, sentence := range []string{amounts, days} {
		if !strings.Contains(help, sentence) {
			t.Fatalf("validate's help does not say: %s", sentence)
		}
	}
	dir := t.TempDir()
	t.Chdir(dir)
	for file, valid := range map[string]bool{
		"policy:\n  run_budget_usd: 5\n":        true,
		"policy:\n  run_budget_usd: 5.0\n":      true,
		"policy:\n  run_budget_usd: \"2.50\"\n": true,
		"policy:\n  run_budget_usd: 1e3\n":      false,
		"approval:\n  expires_days: 7\n":        true,
		"approval:\n  expires_days: 7.0\n":      false,
	} {
		writeFile(t, filepath.Join(dir, "costroid.yml"), file)
		code, stdout, stderr := execute(t, "validate")
		if valid && (code != 0 || !strings.HasPrefix(stdout, "costroid.yml: valid\n")) || !valid && (code != exitRejected || strings.Contains(stdout, "valid\n")) {
			t.Errorf("%q: valid %t in the help, and validate exits %d with %q %q", file, valid, code, stdout, stderr)
		}
	}
}

// TestAMistypedCommandGetsASuggestionAndIsNeverRun holds what an unknown
// command prints: the command as typed, the nearest command when one is near,
// and how to list the commands. The nearest command is named, never run.
func TestAMistypedCommandGetsASuggestionAndIsNeverRun(t *testing.T) {
	for typed, near := range map[string]string{
		"lnit":       "The nearest commands are lint and init.",
		"innit":      "The nearest command is init.",
		"demmo":      "The nearest command is demo.",
		"vresion":    "The nearest command is version.",
		"follow":     "The nearest command is follow-up.",
		"reconsile":  "The nearest command is reconcile.",
		"frobnicate": "No command is near it.",
		"-v":         "No command is near it.",
		"x":          "No command is near it.",
	} {
		for _, args := range [][]string{{typed}, {"help", typed}} {
			code, stdout, stderr := execute(t, args...)
			want := fmt.Sprintf("costroid: unknown command %q\n%s\nRun costroid help to list the commands.\n", typed, near)
			if code != exitUsage || stdout != "" || stderr != want {
				t.Errorf("%v: exit %d, stdout %q, stderr:\n%swant:\n%s", args, code, stdout, stderr, want)
			}
		}
	}
	code, stdout, stderr := execute(t, "demmo", "--markdown")
	if code != exitUsage || stdout != "" || strings.Contains(stderr, "Costroid") {
		t.Errorf("a mistyped demo ran: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

// said is the directory that holds each command's help page as it was before
// help took its parts: what a page said then, it says now.
const said = "testdata/said"

// saidUnits cuts a page of that time into what it said, piece by piece: each
// sentence of its prose, and each row of a table or line of a usage with the
// lines that carry it on. White space is folded, so a unit reads the same
// however it wraps.
func saidUnits(page string) []string {
	var units []string
	prose, row := "", ""
	flush := func() {
		if row != "" {
			units = append(units, strings.Join(strings.Fields(row), " "))
		}
		units = append(units, sentences(strings.Join(strings.Fields(prose), " "))...)
		prose, row = "", ""
	}
	for line := range strings.SplitSeq(page, "\n") {
		switch {
		case strings.TrimSpace(line) == "":
			flush()
		case !strings.HasPrefix(line, " "):
			if row != "" {
				flush()
			}
			prose += " " + line
		case strings.HasPrefix(line, "   ") && row != "":
			row += " " + line
		default:
			flush()
			row = line
		}
	}
	flush()
	return units
}

// sentences cuts prose after each full stop that ends a word and has a space
// behind it.
func sentences(prose string) []string {
	var found []string
	start := 0
	for i := 1; i+1 < len(prose); i++ {
		if prose[i] == '.' && prose[i-1] != ' ' && prose[i+1] == ' ' {
			found = append(found, prose[start:i+1])
			start = i + 2
		}
	}
	if start < len(prose) {
		found = append(found, prose[start:])
	}
	return found
}

// withdrawn are the sentences of an earlier page that are false now and are
// no longer said. validate takes a policy amount such as 5.0, as the schema
// and the check do. The rest are rows of an exit code table from when a wrong
// command line exited 125 and init outside a repository exited 2: each page
// now gives the command line a row of its own.
var withdrawn = map[string][]string{
	"validate":     {"A policy amount or approval.expires_days that YAML reads as a float, such as 5.0, is a problem too, because the check refuses it.", "costroid validate [costroid.yml]"},
	"approve":      {"125 GitHub Actions was not detected, or an argument was given"},
	"demo":         {"125 an argument or an unknown --state was given, or costroid could not build the comment"},
	"egress":       {"125 costroid could not read the command line or the database"},
	"follow-up":    {"125 GitHub Actions was not detected, or the flags could not be read"},
	"ingest-run":   {"125 costroid could not read the flags or the source, or write the rows"},
	"init":         {"2 the directory is in no git repository", "125 costroid could not read or write a file, refused to, or a command --pr runs failed"},
	"keys":         {"125 costroid could not read the command line, reach the database or find the tenant or the key"},
	"lint-recipes": {"Each rejected line goes to stderr."},
	"lint":         {"costroid lint [--strict] [paths]"},
	"mcp":          {"125 costroid could not read the flags or --ledger, or the session failed"},
	"merged":       {"125 costroid could not read the flags, the run or GitHub's answer, or reach Costroid Platform, or the platform does not serve integration/v0.5 or later"},
	"report":       {"125 the flags could not be read, neither GitHub Actions nor GitLab CI was detected, or --preview could not build the comment"},
	"review":       {"125 costroid could not read the flags or write --out, or --post found neither GitHub Actions nor GitLab CI"},
}

// TestNothingAHelpPageSaidIsDropped holds every sentence and every table row
// of each command's earlier help page to the page the command prints now:
// each stands there whole, wherever it moved to.
func TestNothingAHelpPageSaidIsDropped(t *testing.T) {
	pages, err := filepath.Glob(filepath.Join(said, "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) < 26 {
		t.Fatalf("%s holds %d pages, which is fewer than the commands that had one", said, len(pages))
	}
	for _, path := range pages {
		name := strings.TrimSuffix(filepath.Base(path), ".txt")
		entry, ok := lookup(name)
		if !ok {
			t.Errorf("%s had a help page, and is no command now", name)
			continue
		}
		now := strings.Join(strings.Fields(entry.help), " ")
		units := saidUnits(readFile(t, path))
		if len(units) < 5 {
			t.Errorf("%s: %d units read from %s, which is too few to be a page", name, len(units), path)
		}
		for _, unit := range units {
			if slices.Contains(withdrawn[name], unit) {
				continue
			}
			if !strings.Contains(now, unit) {
				t.Errorf("help %s no longer says: %s", name, unit)
			}
		}
	}
}

func TestEveryFlagIsNamedInItsHelp(t *testing.T) {
	flags := commandFlags(t)
	for _, entry := range commands {
		names := flags[entry.name]
		if len(names) == 0 != slices.Contains(flagless, entry.name) {
			t.Errorf("%s: %d flags found, and flagless lists it: %t", entry.name, len(names), slices.Contains(flagless, entry.name))
		}
		for _, defined := range names {
			if !regexp.MustCompile(`--` + regexp.QuoteMeta(defined.name) + `([^a-z0-9-]|$)`).MatchString(entry.help) {
				t.Errorf("help %s does not name --%s", entry.name, defined.name)
			}
		}
		delete(flags, entry.name)
	}
	for name := range flags {
		t.Errorf("a flag set names %q, which is no command", name)
	}
}

// flagDefiners are the methods of flag.FlagSet that define a flag, with the
// index of the argument that names it.
var flagDefiners = map[string]int{
	"Bool": 0, "BoolFunc": 0, "Duration": 0, "Float64": 0, "Func": 0, "Int": 0, "Int64": 0,
	"String": 0, "Uint": 0, "Uint64": 0, "BoolVar": 1, "DurationVar": 1, "Float64Var": 1,
	"Int64Var": 1, "IntVar": 1, "StringVar": 1, "TextVar": 1, "Uint64Var": 1, "UintVar": 1, "Var": 1,
}

// definedFlag is a flag a command defines, and whether it stands without a
// value.
type definedFlag struct {
	name    string
	boolean bool
}

// commandFlags reads, from the source, the flags every command's flag sets
// define. A flag set belongs to the command its name opens with. A function
// that takes a flag set, such as addAccountFlags, gives its flags to every
// command whose set is passed to it.
func commandFlags(t *testing.T) map[string][]definedFlag {
	t.Helper()
	var funcs []*ast.FuncDecl
	for _, dir := range flagSources {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range entries {
			if !strings.HasSuffix(file.Name(), ".go") || strings.HasSuffix(file.Name(), "_test.go") {
				continue
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, file.Name()), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, decl := range parsed.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
					funcs = append(funcs, fn)
				}
			}
		}
	}
	// makers are the functions that return a new flag set, by the command
	// it belongs to.
	makers := map[string]string{}
	for _, fn := range funcs {
		if command := newFlagSetCommand(fn.Body); command != "" && returnsFlagSet(fn) {
			makers[fn.Name.Name] = command
		}
	}
	flags := map[string][]definedFlag{}
	helpers := map[string][]definedFlag{}
	passes := map[string][]string{}
	for _, fn := range funcs {
		sets := map[string]string{}
		for _, param := range fn.Type.Params.List {
			if isFlagSetPointer(param.Type) {
				for _, name := range param.Names {
					sets[name.Name] = ""
				}
			}
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.AssignStmt:
				if len(node.Lhs) != 1 || len(node.Rhs) != 1 {
					break
				}
				if set, ok := node.Lhs[0].(*ast.Ident); ok {
					if command := setCommand(node.Rhs[0], makers); command != "" {
						sets[set.Name] = command
					}
				}
			case *ast.CallExpr:
				if command, defined, ok := flagDefinition(t, node, sets); ok {
					if command == "" {
						helpers[fn.Name.Name] = append(helpers[fn.Name.Name], defined)
					} else {
						flags[command] = append(flags[command], defined)
					}
				}
				if callee, ok := node.Fun.(*ast.Ident); ok {
					for _, arg := range node.Args {
						if set, ok := arg.(*ast.Ident); ok && sets[set.Name] != "" {
							passes[sets[set.Name]] = append(passes[sets[set.Name]], callee.Name)
						}
					}
				}
			}
			return true
		})
	}
	for command, callees := range passes {
		for _, callee := range callees {
			flags[command] = append(flags[command], helpers[callee]...)
		}
	}
	for command, defined := range flags {
		isComputed := func(defined definedFlag) bool { return defined.name == computed }
		if slices.ContainsFunc(defined, isComputed) {
			compute, ok := computedFlags[command]
			if !ok {
				t.Fatalf("%s defines a flag whose name is not a literal, and computedFlags does not list it", command)
			}
			defined = slices.DeleteFunc(defined, isComputed)
			for _, name := range compute() {
				defined = append(defined, definedFlag{name: name})
			}
			flags[command] = defined
		}
	}
	return flags
}

// computed stands for a flag whose name the source computes.
const computed = "\x00computed"

// flagDefinition reads a call that defines a flag on one of sets, and answers
// the set's command and the flag.
func flagDefinition(t *testing.T, call *ast.CallExpr, sets map[string]string) (string, definedFlag, bool) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", definedFlag{}, false
	}
	receiver, ok := selector.X.(*ast.Ident)
	if !ok {
		return "", definedFlag{}, false
	}
	command, isSet := sets[receiver.Name]
	index, defines := flagDefiners[selector.Sel.Name]
	if !isSet || !defines {
		return "", definedFlag{}, false
	}
	literal, ok := call.Args[index].(*ast.BasicLit)
	if !ok {
		return command, definedFlag{name: computed}, true
	}
	name, err := strconv.Unquote(literal.Value)
	if err != nil {
		t.Fatal(err)
	}
	return command, definedFlag{name: name, boolean: strings.HasPrefix(selector.Sel.Name, "Bool")}, true
}

// setCommand answers the command of a new flag set an expression makes, or
// nothing when it makes none.
func setCommand(expression ast.Expr, makers map[string]string) string {
	call, ok := expression.(*ast.CallExpr)
	if !ok {
		return ""
	}
	if callee, ok := call.Fun.(*ast.Ident); ok {
		return makers[callee.Name]
	}
	if !isFlagFunc(call.Fun, "NewFlagSet") {
		return ""
	}
	name := call.Args[0]
	for {
		binary, ok := name.(*ast.BinaryExpr)
		if !ok {
			break
		}
		name = binary.X
	}
	literal, ok := name.(*ast.BasicLit)
	if !ok {
		return ""
	}
	text, err := strconv.Unquote(literal.Value)
	if err != nil {
		return ""
	}
	command, _, _ := strings.Cut(text, " ")
	return command
}

// newFlagSetCommand answers the command of the flag set a body makes.
func newFlagSetCommand(body *ast.BlockStmt) string {
	command := ""
	ast.Inspect(body, func(node ast.Node) bool {
		if expression, ok := node.(ast.Expr); ok && command == "" {
			command = setCommand(expression, nil)
		}
		return command == ""
	})
	return command
}

func returnsFlagSet(fn *ast.FuncDecl) bool {
	results := fn.Type.Results
	return results != nil && len(results.List) == 1 && isFlagSetPointer(results.List[0].Type)
}

func isFlagSetPointer(expression ast.Expr) bool {
	star, ok := expression.(*ast.StarExpr)
	return ok && isFlagFunc(star.X, "FlagSet")
}

func isFlagFunc(expression ast.Expr, name string) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && pkg.Name == "flag" && selector.Sel.Name == name
}

func TestGatewayMintWarnsWhenTheEventFileCannotBeRead(t *testing.T) {
	job := newGatewayJob(t)
	job.request(t)
	request := filepath.Join(job.dir, gateway.RequestFile)
	stub := gatewaytest.New(t, gatewayAdmin)
	if code, _, stderr := job.mint(t, stub, "--request", request); code != 0 || strings.Contains(stderr, "warning") {
		t.Fatalf("a readable event file: exit %d, %s", code, stderr)
	}
	absent := filepath.Join(job.dir, "absent-event.json")
	t.Setenv("GITHUB_EVENT_PATH", absent)
	stub = gatewaytest.New(t, gatewayAdmin)
	code, _, stderr := job.mint(t, stub, "--request", request, "--author-daily-cap-usd", "5")
	if code != 0 {
		t.Fatalf("exit %d, %s", code, stderr)
	}
	if !strings.Contains(stderr, "costroid: warning: the event file "+strconv.Quote(absent)+" could not be read") {
		t.Errorf("the warning does not name the event file:\n%s", stderr)
	}
	got, ok := stub.Key(gatewayAlias)
	if !ok || got.Metadata[gateway.MetaPullRequest] != "" || got.User != "" {
		t.Errorf("registered %+v", got)
	}
}

// TestInitHelpNamesEveryAgentItAccepts holds init's --agent sentence to the
// agents each host's recipes take, so an agent added to init is named there.
func TestInitHelpNamesEveryAgentItAccepts(t *testing.T) {
	text := strings.Join(strings.Fields(initHelp), " ")
	sentence := regexp.MustCompile(`--agent is (.+?) on GitHub, and (.+?) on GitLab\.`).FindStringSubmatch(text)
	if sentence == nil {
		t.Fatalf("init's help has no --agent sentence:\n%s", initHelp)
	}
	for host, named := range map[string]string{githubHost: sentence[1], gitlabHost: sentence[2]} {
		listed := strings.FieldsFunc(strings.ReplaceAll(named, " or ", ", "), func(r rune) bool { return r == ',' || r == ' ' })
		for agent := range initRecipes[host] {
			if !slices.Contains(listed, agent) {
				t.Errorf("init's help does not name the %s agent %s: %q", host, agent, named)
			}
		}
	}
}

// twoMeanings are the flags whose name means one thing on one command and
// another on the next, with what each command's own line says the value is
// there.
var twoMeanings = map[string]map[string]string{
	"--from": {
		"ingest-run": "KIND=PATH the source to read",
		"review":     "KIND=PATH where the run left its answer",
		"reconcile":  "the first UTC day, YYYY-MM-DD",
	},
	"--agent": {
		"run":  "claude-code, claude-code-sdk, codex, codex-sdk, opencode, copilot",
		"init": "claude, codex, claude-code-action, codex-action, claude-agent-sdk or codex-sdk on GitHub, and claude or codex on GitLab",
	},
	"--budget-usd": {
		"run":     "the run's cap in USD",
		"report":  "the run's cap in USD, read only when the run left no result object",
		"init":    "the cap in USD --existing adds to claude_args that hold none",
		"gateway": "the run's budget in USD, the key's max_budget",
	},
	"--out": {
		"run":        "the file the FOCUS rows go to",
		"ingest-run": "the file the FOCUS rows go to",
		"review":     "the file the answer goes to",
		"export":     "the file --format writes, or the directory for cloudability, ternary and focus-1.5-cr",
		"reconcile":  "the file reconciliation rows are kept in",
		"init":       "the file to write the recipe to",
		"gateway":    "the directory the key and the sealed request go to",
	},
	"--installation-id": {
		"report":    "github:<id> or the bare number",
		"export":    "github:<id>, github-owner:<id> or gitlab-namespace:<id>",
		"merged":    "github:<id>, github-owner:<id> or gitlab-namespace:<id>",
		"reconcile": "github:<id>, github-owner:<id> or gitlab-namespace:<id>",
		"connect":   "github-owner:<id> or gitlab-namespace:<id>",
	},
	"--json": {
		"run":      "print the result object on stdout",
		"report":   "print the result object on stdout",
		"result":   "print the result object whole",
		"policy":   "print the verdict as the machine block carries it",
		"merged":   "print one object per fact sent",
		"egress":   "print the counters as one JSON object",
		"doctor":   "print the checks as one JSON object",
		"lint":     "print the findings as one JSON object",
		"validate": "print what validate found as one JSON object",
		"version":  "print the same facts as one JSON object",
	},
}

// flagLines reads a page's Flags part into one line a flag, by the flag's
// name. A flag two subcommands share has each of its lines.
func flagLines(page string) map[string]string {
	lines := map[string]string{}
	name := ""
	for line := range strings.SplitSeq(part(page, "Flags"), "\n") {
		text := strings.Join(strings.Fields(line), " ")
		if strings.HasPrefix(line, "  --") {
			name, _, _ = strings.Cut(text, " ")
		}
		if name != "" && text != "" {
			lines[name] = strings.TrimSpace(lines[name] + " " + text)
		}
	}
	return lines
}

// TestAFlagWithTwoMeaningsSaysItsValueOnEachPage holds the six flags whose
// name is shared across commands with different values: each command's page
// says, in the flag's own line, what the value is on that command, so no page
// relies on the reader knowing another.
func TestAFlagWithTwoMeaningsSaysItsValueOnEachPage(t *testing.T) {
	for _, entry := range commands {
		lines := flagLines(entry.help)
		for flag, says := range twoMeanings {
			line, has := lines[flag]
			want, listed := says[entry.name]
			switch {
			case has && !listed:
				t.Errorf("help %s has %s, and twoMeanings does not say what its value is there", entry.name, flag)
			case !has && listed:
				t.Errorf("twoMeanings names %s on %s, whose help has no such flag", flag, entry.name)
			case has && !strings.Contains(line, want):
				t.Errorf("help %s: the line of %s does not say %q:\n%s", entry.name, flag, want, line)
			}
		}
	}
}
