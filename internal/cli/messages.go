package cli

import (
	"fmt"
	"io"
)

// Messages is a command's standard error. Standard output holds what a
// command was asked to print, and Messages holds what it says about its work.
// A warning or an error is written to it as to any writer, and stays under
// --quiet. Every other line goes through Say or Notes, and --quiet drops it.
type Messages struct {
	to    io.Writer
	quiet bool
}

// NewMessages is the standard error of one command line, written to to.
func NewMessages(to io.Writer) *Messages {
	return &Messages{to: to}
}

func (m *Messages) Write(p []byte) (int, error) { return m.to.Write(p) }

// The flag every command takes, under both its names. Parse defines it, so no
// command does.
const (
	quietName  = "quiet"
	quietShort = "q"
)

// QuietRow is the flag's line under Flags on every help page.
var QuietRow = Row{"-q, --quiet", "keep warnings and errors on standard error, and drop every other line"}

// Quiet reports whether the command line stderr belongs to gave --quiet.
func Quiet(stderr io.Writer) bool {
	messages, ok := stderr.(*Messages)
	return ok && messages.quiet
}

// Under is the writer stderr was made from, which a child process is given so
// that it holds the file itself.
func Under(stderr io.Writer) io.Writer {
	if messages, ok := stderr.(*Messages); ok {
		return messages.to
	}
	return stderr
}

// Notes is where a command writes the lines --quiet drops: what it did, what
// it is about to wait for, and what to do next.
func Notes(stderr io.Writer) io.Writer { return notes{stderr} }

type notes struct{ stderr io.Writer }

func (n notes) Write(p []byte) (int, error) {
	if Quiet(n.stderr) {
		return len(p), nil
	}
	return n.stderr.Write(p)
}

// Say prints one of the lines --quiet drops on stderr.
func Say(stderr io.Writer, format string, args ...any) {
	fmt.Fprintf(Notes(stderr), format, args...)
}
