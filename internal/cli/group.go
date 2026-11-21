package cli

import (
	"fmt"
	"strings"
)

// Group is the short page of a command that takes a subcommand, which a call
// with none prints: what the command does, each subcommand with what it does,
// one call to copy and where the whole page is.
type Group struct {
	Command string
	// Summary is one sentence, the one the command's Page opens with.
	Summary     string
	Subcommands []Row
	// Example is one command line, on as many lines as it needs.
	Example []string
}

func (g Group) String() string {
	var text strings.Builder
	text.WriteString(wrap(g.Summary, "", "") + "\n")
	widest := 0
	for _, sub := range g.Subcommands {
		widest = max(widest, len(sub[0]))
	}
	for _, sub := range g.Subcommands {
		fmt.Fprintf(&text, "%s%-*s  %s\n", indent, widest, sub[0], sub[1])
	}
	text.WriteString("\nExample:\n")
	Lines(g.Example).write(&text)
	fmt.Fprintf(&text, "\nHelp for this command:  costroid help %s\n", g.Command)
	return text.String()
}

// Unknown is the mistake of a subcommand the group does not have: it names
// typed and the nearest subcommand, when one is near, and runs neither.
func (g Group) Unknown(typed string) error {
	names := make([]string, len(g.Subcommands))
	for i, sub := range g.Subcommands {
		names[i] = sub[0]
	}
	near := NearestLine("command", "", Nearest(typed, names))
	if near == "" {
		near = "No command is near it."
	}
	return &Mistake{lines: []string{
		fmt.Sprintf("unknown %s command %q", g.Command, typed),
		near,
		fmt.Sprintf("Run costroid help %s to list its commands.", g.Command),
	}}
}
