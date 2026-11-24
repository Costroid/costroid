package cli

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFailExitsTwoOnAMistakeAnd125OnAnythingElse(t *testing.T) {
	set := flag.NewFlagSet("tally", flag.ContinueOnError)
	refused := Parse(set, []string{"--frob"}, io.Discard)
	for name, check := range map[string]struct {
		err  error
		code int
		want string
	}{
		"a flag the set refuses":      {refused, ExitUsage, "costroid: --frob is not a flag of costroid tally\nRun costroid help tally to list its flags.\n"},
		"what a command finds":        {Mistaken("tally", errors.New("tally takes no argument")), ExitUsage, "costroid: tally takes no argument\nRun costroid help tally to see its usage.\n"},
		"a subcommand's finding":      {Mistaken("tally rows", errors.New("tally rows needs --rows")), ExitUsage, "costroid: tally rows needs --rows\nRun costroid help tally to see its usage.\n"},
		"a mistake another one wraps": {fmt.Errorf("read the flags: %w", Mistaken("tally", errors.New("tally takes no argument"))), ExitUsage, "costroid: read the flags: tally takes no argument\nRun costroid help tally to see its usage.\n"},
		"work that could not be done": {errors.New("the ledger did not answer"), ExitCouldNot, "costroid: the ledger did not answer\nSee " + Troubleshooting + ".\n"},
		"an error with its next step": {Stopped("no rows file at rows.jsonl", "costroid run writes one at --out."), ExitCouldNot, "costroid: no rows file at rows.jsonl\ncostroid run writes one at --out.\n"},
	} {
		var stderr bytes.Buffer
		if code := Fail(&stderr, check.err); code != check.code || stderr.String() != check.want {
			t.Errorf("%s: exit %d, stderr:\n%swant exit %d:\n%s", name, code, stderr.String(), check.code, check.want)
		}
	}
}

// TestAnErrorWithNoNextStepEndsOnItsSectionsAddress holds the address Explain
// adds: the section its command named, the section for any command where a
// file is what is wrong, and no address under a mistake or an error that
// names its own next step.
func TestAnErrorWithNoNextStepEndsOnItsSectionsAddress(t *testing.T) {
	_, absent := os.ReadFile(filepath.Join(t.TempDir(), "rows.jsonl"))
	for name, check := range map[string]struct {
		err  error
		want string
	}{
		"work that could not be done": {errors.New("the ledger did not answer"), "\nSee " + Troubleshooting + "#tallying-rows.\n"},
		"a file that is not there":    {fmt.Errorf("read the rows: %w", absent), "\nSee " + Troubleshooting + "#" + AnyCommand + ".\n"},
		"an error with its next step": {Stopped("no rows file at rows.jsonl", "costroid run writes one at --out."), "\ncostroid run writes one at --out.\n"},
		"a wrong command line":        {Mistaken("tally", errors.New("tally takes no argument")), "\nRun costroid help tally to see its usage.\n"},
	} {
		var written bytes.Buffer
		stderr := NewMessages(&written)
		About(stderr, "tallying-rows")
		Explain(stderr, check.err)
		if got := written.String(); !strings.HasSuffix(got, check.want) || strings.Count(got, "See ") > 1 {
			t.Errorf("%s prints %q, want it to end on %q", name, got, check.want)
		}
	}
	var written bytes.Buffer
	stderr := NewMessages(&written)
	Explain(stderr, errors.New("the ledger did not answer"))
	if want := "costroid: the ledger did not answer\nSee " + Troubleshooting + ".\n"; written.String() != want {
		t.Errorf("with no section named: %q, want %q", written.String(), want)
	}
}

func TestMistakenLeavesHelpAndNoErrorAsTheyAre(t *testing.T) {
	if err := Mistaken("tally", nil); err != nil {
		t.Errorf("no error reads %v", err)
	}
	if err := Mistaken("tally", flag.ErrHelp); !errors.Is(err, flag.ErrHelp) || errors.As(err, new(*Mistake)) {
		t.Errorf("a request for help reads %v", err)
	}
	first := Mistaken("tally", errors.New("tally takes no argument"))
	if again := Mistaken("count", first); again != first {
		t.Errorf("a mistake wrapped again reads %v", again)
	}
}

func TestPlainSaysWhatTheSystemSaidInWords(t *testing.T) {
	directory := t.TempDir()
	_, absent := os.ReadFile(filepath.Join(directory, "rows.jsonl"))
	_, isDirectory := os.ReadFile(directory)
	_, below := os.Create(filepath.Join(directory, "absent", "rows.jsonl"))
	_, exists := os.OpenFile(directory, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	refused := &url.Error{Op: "Get", URL: "https://ledger.example/v1/rows", Err: errors.New("connection refused")}
	for name, check := range map[string]struct {
		err  error
		want string
	}{
		"a file that is not there":       {absent, filepath.Join(directory, "rows.jsonl") + " does not exist"},
		"a directory where a file goes":  {isDirectory, directory + " is a directory"},
		"a directory that is not there":  {below, filepath.Join(directory, "absent", "rows.jsonl") + " is in a directory that does not exist"},
		"a file that exists":             {exists, directory + " exists"},
		"a file error with words before": {fmt.Errorf("read the gateway key: %w", absent), "read the gateway key: " + filepath.Join(directory, "rows.jsonl") + " does not exist"},
		"a host that refuses":            {fmt.Errorf("send the rows: %w", refused), "send the rows: ledger.example did not answer: connection refused"},
		"an error that is no system's":   {errors.New("policy needs --rows"), "policy needs --rows"},
	} {
		if got := Plain(check.err); got != check.want {
			t.Errorf("%s reads %q, want %q", name, got, check.want)
		}
	}
	program := &exec.Error{Name: "claude", Err: exec.ErrNotFound}
	if got, want := Plain(fmt.Errorf("start the agent: %w", program)), "start the agent: claude is not on PATH"; got != want {
		t.Errorf("a program that is not installed reads %q, want %q", got, want)
	}
}

func TestWithoutTakesAPackagesNameOffTheFrontAndKeepsTheError(t *testing.T) {
	_, absent := os.Stat(filepath.Join(t.TempDir(), "rows.jsonl"))
	named := fmt.Errorf("policy: read rows: %w", absent)
	if got, want := Plain(Without("policy", named)), "read rows: "+Plain(absent); got != want {
		t.Errorf("reads %q, want %q", got, want)
	}
	if mistake := Mistaken("tally", errors.New("tally: no rows")); Fail(&bytes.Buffer{}, Without("tally", mistake)) != ExitUsage {
		t.Error("a mistake without its package's name is no mistake")
	}
	if got, want := Plain(Without("scm", errors.New("read pull request 7: scm: GET /pulls/7 answered 404"))), "read pull request 7: GET /pulls/7 answered 404"; got != want {
		t.Errorf("a name after a colon reads %q, want %q", got, want)
	}
	if other := errors.New("the policy: none"); Without("policy", other) != other {
		t.Error("an error that opens otherwise changed")
	}
}

func TestFileSaysAnErrorByTheFileItEndsOn(t *testing.T) {
	_, absent := os.Stat(filepath.Join(t.TempDir(), "workflows"))
	wrapped := fmt.Errorf("read a workflow: %w", absent)
	if got := Plain(File(wrapped)); got == Plain(wrapped) || got != Plain(absent) {
		t.Errorf("an error that ends on a file reads %q", got)
	}
	middle := fmt.Errorf("no terminal to ask on: %w, so name the variable", absent)
	if File(middle) != middle {
		t.Errorf("an error with words after the file reads %q", Plain(File(middle)))
	}
	if plain := errors.New("the ledger did not answer"); File(plain) != plain {
		t.Error("an error that names no file changed")
	}
}
