package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/Costroid/costroid/internal/config"
)

// validateCommand checks one file. With no argument it reads costroid.yml here,
// and a repository with no such file has nothing to break: zero configuration
// is a supported state.
func validateCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		fmt.Fprintf(stderr, "costroid: validate takes one path\n")
		return exitFailure
	}
	path := configFile
	if len(args) == 1 {
		path = args[0]
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) && len(args) == 0 {
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
	fmt.Fprintf(stdout, "%s: valid\n", path)
	return 0
}
