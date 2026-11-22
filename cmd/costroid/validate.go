package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"

	"github.com/Costroid/costroid/internal/cli"
	"github.com/Costroid/costroid/internal/config"
)

// validateSchema names the object validate --json prints.
const validateSchema = "costroid.validate/0.1"

// The states a file is in, as the object names them.
const (
	fileValid   = "valid"
	fileInvalid = "invalid"
	fileAbsent  = "absent"
)

// validation is what validate found in one file: costroid.validate/0.1.
type validation struct {
	Schema string `json:"schema"`
	// File is the file as it was named, or standard input.
	File     string          `json:"file"`
	State    string          `json:"state"`
	Problems []schemaProblem `json:"problems"`
}

// schemaProblem is one place the file breaks the schema. Pointer is a JSON
// pointer, empty for the document.
type schemaProblem struct {
	Pointer string `json:"pointer"`
	Message string `json:"message"`
}

// lines are what validate prints without --json.
func (v validation) lines() []string {
	switch v.State {
	case fileAbsent:
		return []string{v.File + ": no file, so the defaults apply"}
	case fileValid:
		return []string{v.File + ": valid"}
	}
	lines := make([]string, len(v.Problems))
	for i, problem := range v.Problems {
		lines[i] = v.File + ": " + config.Problem{Path: problem.Pointer, Message: problem.Message}.String()
	}
	return lines
}

// validateCommand checks one file. With no argument it reads costroid.yml here,
// and a repository with no such file has nothing to break: zero configuration
// is a supported state.
func validateCommand(_ context.Context, args []string, std stdio) int {
	stdout, stderr := std.stdout, std.stderr
	set := flag.NewFlagSet("validate", flag.ContinueOnError)
	asJSON := set.Bool("json", false, "print what validate found as one JSON object")
	if err := parseFlags(set, args, validateHelp, stdout, stderr); errors.Is(err, flag.ErrHelp) {
		return 0
	} else if err != nil {
		return cli.Fail(stderr, cli.Mistaken("validate", err))
	}
	if set.NArg() > 1 {
		return cli.Fail(stderr, cli.Mistaken("validate", errors.New("validate takes one path")))
	}
	path := configFile
	if set.NArg() == 1 {
		path = set.Arg(0)
	}
	data, err := readNamed(path, std.stdin)
	if path == standardInput {
		path = "standard input"
	}
	found := validation{Schema: validateSchema, File: path, State: fileValid, Problems: []schemaProblem{}}
	switch {
	case errors.Is(err, fs.ErrNotExist) && set.NArg() == 0:
		found.State = fileAbsent
	case err != nil:
		return cli.Fail(stderr, err)
	default:
		problems, err := config.Validate(data)
		if err != nil {
			return cli.Fail(stderr, err)
		}
		for _, problem := range problems {
			found.State = fileInvalid
			found.Problems = append(found.Problems, schemaProblem{Pointer: problem.Path, Message: problem.Message})
		}
	}
	if *asJSON {
		if err := printObject(stdout, found); err != nil {
			return cli.Fail(stderr, err)
		}
	} else {
		for _, line := range found.lines() {
			fmt.Fprintln(stdout, line)
		}
	}
	switch found.State {
	case fileInvalid:
		return exitRejected
	case fileValid:
		cli.Say(stderr, "%s\n", nextAfterValidate)
	}
	return 0
}
