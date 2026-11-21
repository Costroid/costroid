package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"

	"github.com/Costroid/costroid/internal/config"
)

// validateCommand checks one file. With no argument it reads costroid.yml here,
// and a repository with no such file has nothing to break: zero configuration
// is a supported state.
func validateCommand(_ context.Context, args []string, std stdio) int {
	stdout, stderr := std.stdout, std.stderr
	set := flag.NewFlagSet("validate", flag.ContinueOnError)
	if err := parseFlags(set, args, validateHelp, stdout); errors.Is(err, flag.ErrHelp) {
		return 0
	} else if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	if set.NArg() > 1 {
		fmt.Fprintf(stderr, "costroid: validate takes one path\n")
		return exitFailure
	}
	path := configFile
	if set.NArg() == 1 {
		path = set.Arg(0)
	}
	data, err := readNamed(path, std.stdin)
	if path == standardInput {
		path = "standard input"
	}
	if errors.Is(err, fs.ErrNotExist) && set.NArg() == 0 {
		fmt.Fprintf(stdout, "%s: no file, so the defaults apply\n", path)
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	problems, err := config.Validate(data)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	for _, problem := range problems {
		fmt.Fprintf(stdout, "%s: %s\n", path, problem)
	}
	if len(problems) > 0 {
		return exitRejected
	}
	fmt.Fprintf(stdout, "%s: valid\n%s\n", path, nextAfterValidate)
	return 0
}
