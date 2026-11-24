package cli

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"testing"
)

// TestParseGivesEverySetQuietAndQuietsItsMessages parses a command line with
// a set that defines no flag for it. Both names of --quiet are read, a
// warning and an error still reach stderr, and a line said through Say or
// Notes does not. A command line that is refused quiets nothing, and a writer
// that is no Messages is never quiet.
func TestParseGivesEverySetQuietAndQuietsItsMessages(t *testing.T) {
	for _, check := range []struct {
		args  []string
		quiet bool
		// parse is ParseLeading where a word after an argument is left to the
		// command, which Parse refuses when it names a flag.
		parse func(*flag.FlagSet, []string, io.Writer) error
	}{
		{nil, false, Parse}, {[]string{"-q"}, true, Parse}, {[]string{"--quiet"}, true, Parse}, {[]string{"--rows", "r.jsonl", "-q", "rest"}, true, Parse},
		{[]string{"--quiet=false"}, false, Parse}, {[]string{"rest", "-q"}, false, ParseLeading}, {[]string{"--", "-q"}, false, Parse},
	} {
		var written bytes.Buffer
		stderr := NewMessages(&written)
		set := flag.NewFlagSet("tally", flag.ContinueOnError)
		set.String("rows", "", "the rows file")
		if err := check.parse(set, check.args, stderr); err != nil {
			t.Fatalf("%v: %v", check.args, err)
		}
		Say(stderr, "costroid: wrote %d rows\n", 3)
		fmt.Fprint(Notes(stderr), "Next: costroid tally\n")
		fmt.Fprint(stderr, "costroid: warning: a row was skipped\n")
		Explain(stderr, errors.New("the ledger did not answer"))
		want := "costroid: warning: a row was skipped\ncostroid: the ledger did not answer\nSee " + Troubleshooting + ".\n"
		if !check.quiet {
			want = "costroid: wrote 3 rows\nNext: costroid tally\n" + want
		}
		if Quiet(stderr) != check.quiet || written.String() != want {
			t.Errorf("%v: quiet %t, stderr:\n%swant quiet %t:\n%s", check.args, Quiet(stderr), written.String(), check.quiet, want)
		}
		if Under(stderr) != &written {
			t.Errorf("%v: the writer under the messages is not the one they were made from", check.args)
		}
	}
	var written bytes.Buffer
	stderr := NewMessages(&written)
	if err := Parse(flag.NewFlagSet("tally", flag.ContinueOnError), []string{"-q", "--frob"}, stderr); err == nil || Quiet(stderr) {
		t.Errorf("a command line that is refused: %v, quiet %t", err, Quiet(stderr))
	}
	var plain bytes.Buffer
	if err := Parse(flag.NewFlagSet("tally", flag.ContinueOnError), []string{"-q"}, &plain); err != nil || Quiet(&plain) || Under(&plain) != &plain {
		t.Errorf("a writer that is no Messages: %v, quiet %t", err, Quiet(&plain))
	}
	Say(&plain, "costroid: wrote %d rows\n", 3)
	if plain.String() != "costroid: wrote 3 rows\n" {
		t.Errorf("a writer that is no Messages lost a line: %q", plain.String())
	}
}

// TestAMistypedFlagIsNeverSentToTheOneLetterQuiet holds the nearest flag to
// a flag's long name: -q is one edit from every other letter.
func TestAMistypedFlagIsNeverSentToTheOneLetterQuiet(t *testing.T) {
	for typed, want := range map[string]string{
		"-p":      "costroid: -p is not a flag of costroid tally\nRun costroid help tally to list its flags.\n",
		"--quite": "costroid: --quite is not a flag of costroid tally\nThe nearest flag is --quiet.\nRun costroid help tally to list its flags.\n",
	} {
		set := flag.NewFlagSet("tally", flag.ContinueOnError)
		set.String("rows", "", "the rows file")
		var stderr bytes.Buffer
		if code := Fail(&stderr, Parse(set, []string{typed}, &stderr)); code != ExitUsage || stderr.String() != want {
			t.Errorf("%s: exit %d, stderr:\n%swant:\n%s", typed, code, stderr.String(), want)
		}
	}
}

func TestNoArgumentRefusesAnArgumentAndNamesTheForm(t *testing.T) {
	set := flag.NewFlagSet("tally rows", flag.ContinueOnError)
	if err := Parse(set, []string{"-q"}, nil); err != nil || NoArgument(set) != nil {
		t.Fatalf("no argument: %v, %v", err, NoArgument(set))
	}
	set = flag.NewFlagSet("tally rows", flag.ContinueOnError)
	if err := Parse(set, []string{"extra"}, nil); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	want := "costroid: tally rows takes no argument\nRun costroid help tally to see its usage.\n"
	if code := Fail(&stderr, NoArgument(set)); code != ExitUsage || stderr.String() != want {
		t.Errorf("exit %d, stderr:\n%swant:\n%s", code, stderr.String(), want)
	}
}
