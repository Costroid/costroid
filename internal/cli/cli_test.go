package cli

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestAPagePrintsItsPartsInOneOrder(t *testing.T) {
	page := Page{
		Command:  "tally",
		Summary:  "tally adds up what a rows file holds.",
		Usage:    []string{"costroid tally --rows FILE [--json]"},
		Examples: []string{"costroid tally --rows rows.jsonl"},
		Flags:    []Row{{"--rows FILE", "the rows file"}, {"--json", "print one object"}},
		Sections: []Section{{Heading: "Currencies", Body: []Block{
			Text("Each currency is added up alone.\n\nNo amount is rounded."),
			Rows{{"USD", "United States dollars"}},
			Lines{"costroid tally --rows rows.jsonl --json"},
		}}},
		Environment: []Block{Rows{{"TALLY_ROWS", "the rows file, when --rows is not given"}}},
		Exits:       []Row{{"0", "printed"}, {"125", "the rows could not be read"}},
		Docs:        "export",
	}.String()
	want := `tally adds up what a rows file holds.

Usage:
  costroid tally --rows FILE [--json]

Examples:
  costroid tally --rows rows.jsonl

Flags:
  --rows FILE       the rows file
  --json            print one object
  -q, --quiet       keep warnings and errors on standard error, and drop every
                    other line

Currencies:
  Each currency is added up alone.

  No amount is rounded.

  USD               United States dollars

  costroid tally --rows rows.jsonl --json

Environment:
  TALLY_ROWS        the rows file, when --rows is not given

Exit codes of tally:
  0                 printed
  125               the rows could not be read

Documentation:
  https://docs.costroid.com/export
`
	if page != want {
		t.Errorf("the page prints:\n%s\nwant:\n%s", page, want)
	}
}

func TestAPageWrapsAtEightyColumnsAndHangsATablesText(t *testing.T) {
	long := strings.Repeat("word ", 40)
	page := Page{
		Command: "tally", Summary: long + "end.", Usage: []string{"costroid tally"}, Examples: []string{"costroid tally"},
		Flags: []Row{{"--short", long}, {"--a-flag-with-a-name-far-too-long-to-set-text-beside VALUE", long}},
		Exits: []Row{{"0", long}}, Docs: "export",
	}.String()
	for line := range strings.SplitSeq(page, "\n") {
		if len(line) > Width || strings.HasSuffix(line, " ") {
			t.Errorf("a line is %d columns, or ends in a space: %q", len(line), line)
		}
	}
	for _, want := range []string{
		"\n  --short           word word",
		"\n                    word word",
		"\n  --a-flag-with-a-name-far-too-long-to-set-text-beside VALUE\n                    word",
		"\n  0                 word word",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q:\n%s", want, page)
		}
	}
}

func newSet() *flag.FlagSet {
	set := flag.NewFlagSet("tally rows", flag.ContinueOnError)
	set.String("rows", "", "")
	set.Bool("json", false, "")
	set.Int("limit", 0, "")
	set.Duration("wait", 0, "")
	set.Func("tag", "", func(value string) error {
		if !strings.Contains(value, "=") {
			return errors.New("takes KEY=VALUE")
		}
		return nil
	})
	return set
}

func TestParseSaysWhichFlagWasRefusedAndWhy(t *testing.T) {
	help := "Run costroid help tally to list its flags."
	for _, c := range []struct {
		args []string
		want []string
	}{
		{[]string{"--rwos", "x"}, []string{"--rwos is not a flag of costroid tally rows", "The nearest flag is --rows.", help}},
		{[]string{"-rwos=x"}, []string{"-rwos is not a flag of costroid tally rows", "The nearest flag is --rows.", help}},
		{[]string{"--json", "--zzz"}, []string{"--zzz is not a flag of costroid tally rows", help}},
		{[]string{"---json"}, []string{"---json is not a flag of costroid tally rows", "The nearest flag is --json.", help}},
		{[]string{"--rows", "x", "--=y"}, []string{"--=y is not a flag of costroid tally rows", help}},
		{[]string{"-="}, []string{"-= is not a flag of costroid tally rows", help}},
		{[]string{"--json", "-=x"}, []string{"-=x is not a flag of costroid tally rows", help}},
		{[]string{"--limit", "2", "--tag", "a=b", "--limit", "x", "--rows"}, []string{`--limit takes a whole number, not "x"`, help}},
		{[]string{"--json", "--rows"}, []string{"--rows needs a value", help}},
		{[]string{"--rows", "--json", "--limit", "many"}, []string{`--limit takes a whole number, not "many"`, help}},
		{[]string{"--limit=1.5"}, []string{`--limit takes a whole number, not "1.5"`, help}},
		{[]string{"--json=perhaps"}, []string{`--json takes true or false, not "perhaps"`, help}},
		{[]string{"--wait", "long"}, []string{`--wait takes a duration, such as 30s, not "long"`, help}},
		{[]string{"--tag", "team"}, []string{"--tag: takes KEY=VALUE", help}},
	} {
		var mistake *Mistake
		if err := Parse(newSet(), c.args, io.Discard); !errors.As(err, &mistake) || err.Error() != strings.Join(c.want, "\n") {
			t.Errorf("%v: %v, want %q", c.args, err, c.want)
		}
	}
}

func TestParseTakesWhatTheSetTakesAndPassesHelpOn(t *testing.T) {
	set := newSet()
	if err := Parse(set, []string{"--rows", "r.jsonl", "--json", "--wait", "2s", "--tag", "team=a", "--", "--rest"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if rows, wait := set.Lookup("rows").Value.String(), set.Lookup("wait").Value.String(); rows != "r.jsonl" || wait != (2*time.Second).String() || !slices.Equal(set.Args(), []string{"--rest"}) {
		t.Errorf("rows %q, wait %q, the rest %q", rows, wait, set.Args())
	}
	for _, ask := range []string{"-h", "-help", "--help"} {
		if err := Parse(newSet(), []string{"--json", ask}, io.Discard); !errors.Is(err, flag.ErrHelp) {
			t.Errorf("%s: %v, want the request for help", ask, err)
		}
	}
}

// TestParseCallsAFlagAfterAnArgumentWhatItIs holds the mistake of a flag that
// stands after an argument: the flag as typed, and the command line with its
// flags first, each flag with its value. A word the set does not define, a
// lone dash and every word after two dashes are arguments.
func TestParseCallsAFlagAfterAnArgumentWhatItIs(t *testing.T) {
	const wrong = " stands after an argument, and flags come before arguments"
	for _, c := range []struct {
		args []string
		want []string
	}{
		{[]string{"a.yml", "--json"}, []string{"--json" + wrong, "With the flags first: costroid tally rows --json a.yml"}},
		{[]string{"--limit", "2", "a.yml", "-json", "b.yml"}, []string{"-json" + wrong, "With the flags first: costroid tally rows --limit 2 -json a.yml b.yml"}},
		{[]string{"a.yml", "--rows", "r.jsonl", "b.yml"}, []string{"--rows" + wrong, "With the flags first: costroid tally rows --rows r.jsonl a.yml b.yml"}},
		{[]string{"a.yml", "--rows=r.jsonl", "b.yml", "--json"}, []string{"--rows" + wrong, "With the flags first: costroid tally rows --rows=r.jsonl --json a.yml b.yml"}},
		{[]string{"a file.yml", "--rows"}, []string{"--rows" + wrong, "With the flags first: costroid tally rows --rows 'a file.yml'"}},
		{[]string{"a.yml", "--quiet"}, []string{"--quiet" + wrong, "With the flags first: costroid tally rows --quiet a.yml"}},
		{[]string{"a.yml", "-q"}, []string{"-q" + wrong, "With the flags first: costroid tally rows -q a.yml"}},
		{[]string{"a.yml", "--help"}, []string{"--help" + wrong, "With the flags first: costroid tally rows --help a.yml"}},
		{[]string{"a.yml", "--json", "--", "--rows"}, []string{"--json" + wrong, "With the flags first: costroid tally rows --json a.yml -- --rows"}},
	} {
		var mistake *Mistake
		if err := Parse(newSet(), c.args, io.Discard); !errors.As(err, &mistake) || err.Error() != strings.Join(c.want, "\n") {
			t.Errorf("%v: %v, want %q", c.args, err, c.want)
		}
		set := newSet()
		if err := ParseLeading(set, c.args, io.Discard); err != nil || set.NArg() == 0 {
			t.Errorf("%v, for a command that leaves its arguments as they are: %v, %d arguments", c.args, err, set.NArg())
		}
	}
	for _, args := range [][]string{
		{"a.yml", "b.yml"},
		{"a.yml", "--no-such-flag"},
		{"a.yml", "-"},
		{"a.yml", "-5"},
		{"--", "a.yml", "--json"},
		{"--json", "--", "a.yml", "--rows"},
		{"a.yml", "--", "--json"},
	} {
		if err := Parse(newSet(), args, io.Discard); err != nil {
			t.Errorf("%v: %v, want no mistake", args, err)
		}
	}
}

func TestNearestNamesWhatAMistypedWordStandsFor(t *testing.T) {
	names := []string{"lint", "init", "run", "report", "reconcile", "follow-up", "policy", "version", "validate"}
	for typed, want := range map[string][]string{
		"innit":    {"init"},
		"lnit":     {"lint", "init"},
		"rum":      {"run"},
		"polcy":    {"policy"},
		"reconsle": {"reconcile"},
		"follow":   {"follow-up"},
		"ver":      {"version"},
		"v":        nil,
		"re":       nil,
		"val":      {"validate"},
		"va":       nil,
		"deploy":   nil,
		"repotr":   {"report"},
	} {
		if got := Nearest(typed, names); !slices.Equal(got, want) {
			t.Errorf("%q: %v, want %v", typed, got, want)
		}
	}
	if got := Nearest("rep", []string{"report", "repeat"}); got != nil {
		t.Errorf("a word that opens two names names %v", got)
	}
	for near, want := range map[string]string{
		"":      "",
		"a":     "The nearest flag is --a.",
		"a b":   "The nearest flags are --a and --b.",
		"a b c": "The nearest flags are --a, --b and --c.",
	} {
		if got := NearestLine("flag", "--", strings.Fields(near)); got != want {
			t.Errorf("%q: %q, want %q", near, got, want)
		}
	}
}

// TestHangWrapsALineUnderItsLabelAndBreaksNoWord holds a line at 80 columns:
// the label stays on the first line, each line after it stands under the
// label's text, a line that fits is as it was, and a word wider than a line
// keeps a line to itself.
func TestHangWrapsALineUnderItsLabelAndBreaksNoWord(t *testing.T) {
	long := "warn: gh is not signed in, so doctor did not read the repository's secrets, variables or visibility: run gh auth login, then doctor again"
	want := "warn: gh is not signed in, so doctor did not read the repository's secrets,\n      variables or visibility: run gh auth login, then doctor again"
	if got := Hang(long); got != want {
		t.Errorf("a check hangs as\n%s\nwant\n%s", got, want)
	}
	for _, line := range []string{"", "ok: costroid.yml: valid", strings.Repeat("x", Width)} {
		if got := Hang(line); got != line {
			t.Errorf("a line that fits reads %q, want %q", got, line)
		}
	}
	// One column past the width is wrapped, as 60 past it is.
	if barely := "ok: " + strings.Repeat("word ", 15) + "ok"; len(barely) != Width+1 || strings.Count(Hang(barely), "\n") != 1 {
		t.Errorf("a line of %d columns hangs as %q", len(barely), Hang(barely))
	}
	address := "https://example.com/" + strings.Repeat("a", Width)
	if got, want := Hang("costroid: wrote the file at "+address+" today"), "costroid: wrote the file at\n          "+address+"\n          today"; got != want {
		t.Errorf("a word wider than a line:\n%s\nwant\n%s", got, want)
	}
	prose := strings.TrimSuffix(strings.Repeat("no label opens this line ", 5), " ")
	if got := Hang(prose); strings.Contains(got, "\n ") || strings.ReplaceAll(got, "\n", " ") != prose {
		t.Errorf("a line without a label hangs as %q", got)
	}
	var written bytes.Buffer
	fmt.Fprintf(Hanging(&written), "$ %s\n%s\n", strings.Repeat("git add ", 20), long)
	if got := written.String(); !strings.HasPrefix(got, "$ "+strings.Repeat("git add ", 20)+"\n") || strings.HasSuffix(got, want+"\n") {
		t.Errorf("a writer hangs a command to copy, or a line under another label than costroid's:\n%s", got)
	}
	if AtTerminal(&written) || AtTerminal(NewMessages(&written)) {
		t.Error("a buffer reads as a terminal")
	}
}
