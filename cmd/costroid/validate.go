package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/Costroid/costroid/internal/cli"
	"github.com/Costroid/costroid/internal/config"
	"github.com/Costroid/costroid/internal/policy"
)

// validateSchema names the object validate --json prints.
const validateSchema = "costroid.validate/0.2"

// The states a file is in, as the object names them.
const (
	fileValid   = "valid"
	fileInvalid = "invalid"
	fileAbsent  = "absent"
)

// The states of the policy pack a valid file names, as the object names
// them. unread says validate found no file to compile.
const (
	packCompiles = "valid"
	packNotValid = "invalid"
	packNotRead  = "unread"
)

// packNotValidLine is what validate says of a file whose own keys are valid
// and whose policy pack is not: the refusal first, then what holds.
const packNotValidLine = "the policy pack it names is not valid, and its own keys are"

// validation is what validate found in one file: costroid.validate/0.2.
type validation struct {
	Schema string `json:"schema"`
	// File is the file as it was named, or standard input.
	File     string          `json:"file"`
	State    string          `json:"state"`
	Problems []schemaProblem `json:"problems"`
	// Pack is the policy pack a valid file names, nil when it names none.
	Pack *packCheck `json:"pack"`
}

// packCheck is what validate found of the policy pack a file names.
type packCheck struct {
	// Path is the pack's path as policy_pack names it.
	Path string `json:"path"`
	// File is where validate looked for the pack, beside the file it
	// validated, and empty when it had nowhere to look.
	File  string `json:"file"`
	State string `json:"state"`
	// Message is the fault of a pack that is not valid, or why validate did
	// not read it.
	Message string `json:"message,omitempty"`
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
		switch {
		case v.Pack == nil || v.Pack.State == packNotRead:
		case v.Pack.State == packCompiles:
			return []string{v.File + ": valid", v.Pack.File + ": valid"}
		case v.Pack.State == packNotValid:
			// The first line is the one a reader takes the answer from, so
			// it never reads valid above a pack that is not.
			// The message holds a line for each thing the compiler found.
			return append([]string{v.File + ": " + packNotValidLine}, strings.Split(v.Pack.File+": "+v.Pack.Message, "\n")...)
		}
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
func validateCommand(ctx context.Context, args []string, std stdio) int {
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
		path = standardInputName
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
		if found.State == fileValid {
			found.Pack = checkPack(ctx, set.NArg() == 1 && set.Arg(0) == standardInput, path, data)
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
	switch {
	case found.State == fileInvalid:
		return exitRejected
	case found.Pack != nil && found.Pack.State == packNotValid:
		cli.Say(stderr, "%s\n", policy.NextAfterInvalidPack)
		return exitRejected
	case found.Pack != nil && found.Pack.State == packNotRead:
		fmt.Fprintf(stderr, "costroid: warning: validate did not read the policy pack %s: %s. costroid policy --rows FILE --pack FILE reads it\n", found.Pack.Path, found.Pack.Message)
	}
	if found.State == fileValid {
		cli.Say(stderr, "%s\n", nextAfterValidate)
	}
	return 0
}

// checkPack compiles the policy pack a valid file names, from the path it
// names beside that file, as the report reads it beside costroid.yml at the
// base commit. Standard input has no directory, so its pack is not read, and
// neither is a pack whose file is not there.
func checkPack(ctx context.Context, fromStandardInput bool, named string, data []byte) *packCheck {
	file := policy.ConfigFile{Data: data, Found: true}
	path := file.PackPath()
	if path == "" {
		return nil
	}
	check := &packCheck{Path: path, State: packNotRead}
	if fromStandardInput {
		check.Message = "standard input has no directory to read it from"
		return check
	}
	check.File = filepath.Join(filepath.Dir(named), filepath.FromSlash(path))
	read, problem := policy.CheckPackFile(ctx, check.File, file)
	switch {
	case !read:
		check.Message = "there is no " + check.File
	case problem != nil:
		check.State, check.Message = packNotValid, problem.Detail()
	default:
		check.State = packCompiles
	}
	return check
}
