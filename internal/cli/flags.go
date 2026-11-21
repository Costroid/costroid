package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
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
// one.
func Parse(set *flag.FlagSet, args []string) error {
	set.SetOutput(io.Discard)
	set.Usage = func() {}
	err := set.Parse(args)
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return err
	}
	return mistake(set, args, err)
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

func flagNames(set *flag.FlagSet) []string {
	var names []string
	set.VisitAll(func(defined *flag.Flag) { names = append(names, defined.Name) })
	return names
}
