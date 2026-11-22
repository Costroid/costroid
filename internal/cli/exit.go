package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

// The codes a costroid command exits with, one for each way a command ends.
// No command picks a number of its own. costroid run passes the agent's code
// through, so every code below ExitCapped is the agent's there, and a wrong
// command line of run itself exits ExitCouldNot.
const (
	// ExitDone: the command did its work, and a verdict is success or neutral.
	ExitDone = 0
	// ExitFailed: the command did its work and the answer is a failure.
	ExitFailed = 1
	// ExitUsage: the command line is wrong.
	ExitUsage = 2
	// ExitCapped: run stopped the agent at the cap.
	ExitCapped = 124
	// ExitCouldNot: costroid could not do its work.
	ExitCouldNot = 125
	// ExitInterrupted: Ctrl-C ended the command. It is 128 and the number of
	// SIGINT, which a shell reports for a process the signal ended.
	ExitInterrupted = 130
)

// UsageExit is the row every command's help lists for ExitUsage. run's lists
// none: its own wrong command line exits ExitCouldNot.
var UsageExit = Row{"2", "the command line is wrong"}

// Troubleshooting is the page that lists the lines costroid prints.
const Troubleshooting = Documentation + "/troubleshooting"

// Mistaken says the command line of command is wrong for the reason err
// gives: an argument too many, a required flag left out, a value of the wrong
// form. The lines end on where the command's usage is. An error that is
// already a Mistake comes back as it is, and so does a request for help,
// which is no mistake.
func Mistaken(command string, err error) error {
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return err
	}
	if mistake := new(Mistake); errors.As(err, &mistake) {
		return err
	}
	return &Mistake{lines: []string{err.Error(), Usage(command)}}
}

// Usage is the line that says where the usage of command is, which a
// command's own finding about its command line ends on.
func Usage(command string) string {
	command, _, _ = strings.Cut(command, " ")
	return fmt.Sprintf("Run costroid help %s to see its usage.", command)
}

// stopped is work a command could not do, in the lines Explain prints.
type stopped struct {
	lines []string
}

func (s *stopped) Error() string { return strings.Join(s.lines, "\n") }

// Stopped is work a command could not do: what is wrong, then why when
// costroid knows it or the choices the reader has, then what to do next. A
// line of why or of what to do is a sentence, and the last line names a
// command, a file or a page.
func Stopped(what string, then ...string) error {
	return &stopped{lines: append([]string{what}, then...)}
}

// Explain prints what stopped a command, under costroid's name: what is wrong
// on the first line, then why and what to do next when the error says them.
// An error that names no next step ends on the page that lists costroid's
// lines.
func Explain(stderr io.Writer, err error) {
	text := Plain(err)
	if mistake := new(Mistake); !errors.As(err, &mistake) && !strings.Contains(text, "\n") {
		text += "\nSee " + Troubleshooting + "."
	}
	fmt.Fprintf(stderr, "costroid: %s\n", text)
}

// Fail explains err and answers the code its command exits with: ExitUsage
// for a command line that is wrong, ExitCouldNot for anything else.
func Fail(stderr io.Writer, err error) int {
	Explain(stderr, err)
	if mistake := new(Mistake); errors.As(err, &mistake) {
		return ExitUsage
	}
	return ExitCouldNot
}
