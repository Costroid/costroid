package cli

import (
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// Hang wraps one line at Width, when it is wider. A line that opens with a
// label, one word and a colon such as "costroid:" or "warn:", keeps the label
// on its first line, and each line after it stands under the text the label
// is followed by. Any other line is wrapped from its first column. A word is
// never broken, so a path or an address wider than a line keeps a line to
// itself.
func Hang(line string) string {
	if utf8.RuneCountInString(line) <= Width {
		return line
	}
	label, text, found := strings.Cut(line, ": ")
	if !found || strings.ContainsAny(label, " \t") {
		return strings.TrimSuffix(wrap(line, "", ""), "\n")
	}
	label += ": "
	return strings.TrimSuffix(wrap(text, label, strings.Repeat(" ", utf8.RuneCountInString(label))), "\n")
}

// AtTerminal reports whether a person reads w at a terminal. A pipe, a file
// and a job's log are read by a program or kept, so a command writes them
// its lines whole, one to a line, and wraps prose for a terminal alone.
func AtTerminal(w io.Writer) bool {
	file, ok := Under(w).(*os.File)
	return ok && Terminal(file)
}

// Hanging is to with every line written to it that opens with costroid's
// name hung at Width. Every other line passes as it is: a command to copy, a
// list, a paragraph its writer wrapped already. Each write must hold whole
// lines.
func Hanging(to io.Writer) io.Writer { return hanging{to} }

// NotesFor is where a command writes the lines of stderr that --quiet drops,
// as Notes is, with a line under costroid's name wrapped when a person reads
// stderr at a terminal.
func NotesFor(stderr io.Writer) io.Writer {
	if AtTerminal(stderr) {
		return Hanging(Notes(stderr))
	}
	return Notes(stderr)
}

type hanging struct{ to io.Writer }

// ownName opens every line costroid says about its own work.
const ownName = "costroid:"

func (h hanging) Write(p []byte) (int, error) {
	lines := strings.Split(string(p), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, ownName+" ") {
			lines[i] = Hang(line)
		}
	}
	if _, err := io.WriteString(h.to, strings.Join(lines, "\n")); err != nil {
		return 0, err
	}
	return len(p), nil
}
