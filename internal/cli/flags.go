package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
)

// Mistake is a command line that is wrong: a flag the command does not have,
// a flag without its value, a value of the wrong form, an argument too many
// or one left out. Its text opens with what was typed and what is wrong with
// it, then names the nearest name the command has, when one is near, and the
// command's help. A command that stops on one exits ExitUsage.
type Mistake struct {
	lines []string
}

func (m *Mistake) Error() string { return strings.Join(m.lines, "\n") }

// Parse parses args with set and prints nothing. It answers flag.ErrHelp when
// help was asked for and a *Mistake when a flag was refused; the caller prints
// either. The set's name is the command's, with its subcommand when it has
// one. Parse gives every set --quiet, and under it stderr drops the lines
// that are neither a warning nor an error.
//
// A flag of the set that stands after an argument is a mistake too. The flag
// package stops at the first argument and hands the command every word after
// it, so a flag there would be read as a file, or as an argument too many.
func Parse(set *flag.FlagSet, args []string, stderr io.Writer) error {
	err := ParseLeading(set, args, stderr)
	if err == nil {
		if late := misplaced(set, args); late != nil {
			return late
		}
	}
	return err
}

// ParseLeading is Parse for a command whose arguments are not its own to
// judge: another program's command line, or words it ignores. Every word
// after the first argument is left as it is.
func ParseLeading(set *flag.FlagSet, args []string, stderr io.Writer) error {
	set.SetOutput(io.Discard)
	set.Usage = func() {}
	quiet := false
	set.BoolVar(&quiet, quietName, false, QuietRow[1])
	set.BoolVar(&quiet, quietShort, false, QuietRow[1])
	err := set.Parse(args)
	if messages, ok := stderr.(*Messages); ok && err == nil {
		messages.quiet = quiet
	}
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return err
	}
	return mistake(set, args, err)
}

// misplaced is the mistake of a flag of set that stands after an argument,
// nil when none does. It shows the command line with its flags first, and
// reads nothing into the flag: the line is shown, never run. Words after a
// "--" are arguments, whatever they look like.
func misplaced(set *flag.FlagSet, args []string) *Mistake {
	taken := len(args) - set.NArg()
	if taken > 0 && args[taken-1] == "--" {
		return nil
	}
	var first string
	var flags, arguments []string
	rest := set.Args()
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--" {
			arguments = append(arguments, rest[i:]...)
			break
		}
		typed, _, hasValue := strings.Cut(rest[i], "=")
		name := strings.TrimPrefix(strings.TrimPrefix(typed, "-"), "-")
		defined := set.Lookup(name)
		if name == typed || defined == nil && name != "h" && name != "help" {
			arguments = append(arguments, rest[i])
			continue
		}
		if first == "" {
			first = typed
		}
		flags = append(flags, rest[i])
		if defined != nil && !isBool(defined) && !hasValue && i+1 < len(rest) {
			i++
			flags = append(flags, rest[i])
		}
	}
	if first == "" {
		return nil
	}
	line := slices.Concat([]string{"costroid"}, strings.Fields(set.Name()), args[:taken], flags, arguments)
	return &Mistake{lines: []string{
		first + " stands after an argument, and flags come before arguments",
		"With the flags first: " + CommandLine(line),
	}}
}

// bareWord is a word a shell reads as it is, which a printed command line
// shows without quotes: a plain word, or a placeholder such as <commit>.
var bareWord = regexp.MustCompile(`^(?:[A-Za-z0-9_./:=@,+-]+|<[a-z ]+>)$`)

// CommandLine prints args as a shell reads them, quoting what it must.
func CommandLine(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = arg
		if !bareWord.MatchString(arg) {
			quoted[i] = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
		}
	}
	return strings.Join(quoted, " ")
}

// NoArgument is the mistake of an argument where set's command takes none.
// It answers nil when the command line gave none.
func NoArgument(set *flag.FlagSet) error {
	if set.NArg() == 0 {
		return nil
	}
	return Mistaken(set.Name(), fmt.Errorf("%s takes no argument", set.Name()))
}

// mistake walks args as the flag package does, to the flag the parse stopped
// at: one it cannot read a name from, one the set does not define, one with no
// value behind it, or the last one the parse took, whose value the flag
// refused.
func mistake(set *flag.FlagSet, args []string, cause error) *Mistake {
	taken := len(args) - set.NArg()
	var typed, name, value string
	var defined *flag.Flag
	hasValue := false
	for i := 0; i < len(args); i++ {
		if badSyntax(args[i]) {
			typed, name, defined = args[i], strings.TrimLeft(args[i], "-="), nil
			break
		}
		typed, value, hasValue = strings.Cut(args[i], "=")
		name = strings.TrimPrefix(strings.TrimPrefix(typed, "-"), "-")
		defined = set.Lookup(name)
		if defined == nil {
			break
		}
		if !hasValue && !isBool(defined) {
			if i+1 >= len(args) {
				break
			}
			i++
			value, hasValue = args[i], true
		}
		if hasValue && i+1 >= taken && (i+1 >= len(args) || !badSyntax(args[i+1])) {
			break
		}
	}
	command, _, _ := strings.Cut(set.Name(), " ")
	help := fmt.Sprintf("Run costroid help %s to list its flags.", command)
	switch {
	case defined == nil:
		lines := []string{fmt.Sprintf("%s is not a flag of costroid %s", typed, set.Name())}
		if near := NearestLine("flag", "--", Nearest(name, flagNames(set))); near != "" {
			lines = append(lines, near)
		}
		return &Mistake{lines: append(lines, help)}
	case !hasValue:
		return &Mistake{lines: []string{typed + " needs a value", help}}
	}
	return &Mistake{lines: []string{wrongForm(defined, typed, value, cause), help}}
}

// badSyntax reports an argument the flag package reads as a flag and cannot
// read a name from, such as ---json or -=5. It leaves such an argument
// untaken.
func badSyntax(arg string) bool {
	name := strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-")
	return len(arg) > 2 && arg[0] == '-' && (name[0] == '-' || name[0] == '=') || arg == "-="
}

// wrongForm says what a flag takes in place of the value it was given. A flag
// that checks its own value gives its own reason.
func wrongForm(defined *flag.Flag, typed, value string, cause error) string {
	form := ""
	switch kind, _ := flag.UnquoteUsage(defined); {
	case isBool(defined):
		form = "true or false"
	case kind == "int" || kind == "uint":
		form = "a whole number"
	case kind == "float":
		form = "a number"
	case kind == "duration":
		form = "a duration, such as 30s"
	}
	if form != "" {
		return fmt.Sprintf("%s takes %s, not %q", typed, form, value)
	}
	if reason, found := strings.CutPrefix(cause.Error(), fmt.Sprintf("invalid value %q for flag -%s: ", value, defined.Name)); found {
		return typed + ": " + reason
	}
	return fmt.Sprintf("%s does not take %q", typed, value)
}

func isBool(defined *flag.Flag) bool {
	boolean, ok := defined.Value.(interface{ IsBoolFlag() bool })
	return ok && boolean.IsBoolFlag()
}

// flagNames lists the flags a mistyped one is compared with. --quiet stands
// under its long name alone: one letter is near every other letter.
func flagNames(set *flag.FlagSet) []string {
	var names []string
	set.VisitAll(func(defined *flag.Flag) {
		if defined.Name != quietShort {
			names = append(names, defined.Name)
		}
	})
	return names
}
