// Package cli holds what every costroid command shares on the command line:
// the shape of a help page, what a mistake and an error print, and the codes a
// command exits with.
package cli

import (
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	// Width is the widest line a help page prints.
	Width = 80
	// Documentation is the address every page's own address starts with.
	Documentation = "https://docs.costroid.com"

	indent = "  "
	// exitColumn is where the meaning of an exit code starts.
	exitColumn = 20
	// widestTerm is the longest term a table sets its text beside. A longer
	// term stands on a line of its own, above its text.
	widestTerm = 34
)

// Page is one command's help. String prints its parts in one order: what the
// command does, how it is called, calls to copy, its flags, whatever else the
// command needs said, what it reads from the environment, its exit codes and
// the address of its page in the documentation.
type Page struct {
	// Command names the exit code table.
	Command string
	// Summary is one sentence.
	Summary string
	// Usage holds one line per element. A form too long for one line goes on
	// in the next element, which opens with the spaces that align it.
	Usage []string
	// Examples are command lines to run as written, the commonest first.
	Examples []string
	// Flags gives each flag, with its value's name, and what it does.
	Flags    []Row
	Sections []Section
	// Environment says what the command reads from the environment.
	Environment []Block
	Exits       []Row
	// Docs is the command's page under Documentation.
	Docs string
}

// Row is a term and what it means.
type Row [2]string

// Section is text under a heading that says what it is.
type Section struct {
	Heading string
	Body    []Block
}

// Block is one piece of a section: Text, Rows or Lines.
type Block interface {
	write(text *strings.Builder)
}

// Text is prose. A blank line separates its paragraphs, and each is wrapped
// again, so the line breaks of the source do not show.
type Text string

// Rows is a table of terms.
type Rows []Row

// Lines print as they are, below the prose they belong to.
type Lines []string

func (p Page) String() string {
	var text strings.Builder
	text.WriteString(wrap(p.Summary, "", ""))
	heading(&text, "Usage")
	Lines(p.Usage).write(&text)
	heading(&text, "Examples")
	Lines(p.Examples).write(&text)
	if len(p.Flags) > 0 {
		heading(&text, "Flags")
		Rows(p.Flags).write(&text)
	}
	sections := p.Sections
	if len(p.Environment) > 0 {
		sections = append(slices.Clone(sections), Section{Heading: "Environment", Body: p.Environment})
	}
	for _, section := range sections {
		heading(&text, section.Heading)
		for i, block := range section.Body {
			if i > 0 {
				text.WriteString("\n")
			}
			block.write(&text)
		}
	}
	heading(&text, "Exit codes of "+p.Command)
	writeRows(&text, p.Exits, exitColumn)
	heading(&text, "Documentation")
	text.WriteString(indent + Documentation + "/" + p.Docs + "\n")
	return text.String()
}

func heading(text *strings.Builder, name string) {
	text.WriteString("\n" + name + ":\n")
}

func (t Text) write(text *strings.Builder) {
	for i, paragraph := range strings.Split(string(t), "\n\n") {
		if i > 0 {
			text.WriteString("\n")
		}
		text.WriteString(wrap(paragraph, indent, indent))
	}
}

func (l Lines) write(text *strings.Builder) {
	for _, line := range l {
		text.WriteString(indent + line + "\n")
	}
}

func (r Rows) write(text *strings.Builder) {
	column := exitColumn
	for _, row := range r {
		if width := len(indent) + utf8.RuneCountInString(row[0]) + 2; width > column && width <= len(indent)+widestTerm+2 {
			column = width
		}
	}
	writeRows(text, r, column)
}

// writeRows sets each row's text from column on, and on the lines after it.
func writeRows(text *strings.Builder, rows []Row, column int) {
	hang := strings.Repeat(" ", column)
	for _, row := range rows {
		first := indent + row[0]
		if gap := column - utf8.RuneCountInString(first); gap >= 2 {
			text.WriteString(wrap(row[1], first+strings.Repeat(" ", gap), hang))
			continue
		}
		text.WriteString(first + "\n" + wrap(row[1], hang, hang))
	}
}

// wrap breaks text at its spaces into lines of at most Width columns. The
// first line opens with first, and each line after it with rest.
func wrap(text, first, rest string) string {
	var lines strings.Builder
	line := first
	empty := true
	for _, word := range strings.Fields(text) {
		if !empty && utf8.RuneCountInString(line)+1+utf8.RuneCountInString(word) > Width {
			lines.WriteString(line + "\n")
			line, empty = rest, true
		}
		if !empty {
			line += " "
		}
		line += word
		empty = false
	}
	return lines.String() + line + "\n"
}
