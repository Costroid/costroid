// Command notice writes NOTICE from the modules a release build of costroid
// links and the licence and notice files each holds in the module cache.
//
// A licence file whose text the command does not know stops it, naming the
// module, so no module ships under a licence nobody has read.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
)

const usage = `Usage:
  go run ./scripts/notice [-binary BINARY] [-o FILE]

notice reads the modules BINARY links from its build information, and each
module's licence and notice files from the module cache, and writes NOTICE to
FILE. Without -binary, it builds cmd/costroid as a release does, for
linux/amd64 with cgo off.

Exit codes: 0 written, 1 a licence it does not know or a failure.
`

// costroidPackage builds from anywhere inside this module.
const costroidPackage = "github.com/Costroid/costroid/cmd/costroid"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("notice", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprint(stderr, usage) }
	binary := set.String("binary", "", "the costroid binary whose modules NOTICE names")
	out := set.String("o", "NOTICE", "the file to write")
	if err := set.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if set.NArg() != 0 {
		fmt.Fprintf(stderr, "notice: unexpected argument %q\n", set.Arg(0))
		return 1
	}
	if err := write(ctx, *binary, *out); err != nil {
		fmt.Fprintf(stderr, "notice: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "notice: wrote %s\n", *out)
	return 0
}

func write(ctx context.Context, binary, out string) error {
	if binary == "" {
		scratch, err := os.MkdirTemp("", "notice")
		if err != nil {
			return err
		}
		defer os.RemoveAll(scratch)
		binary = filepath.Join(scratch, "costroid")
		if err := buildRelease(ctx, binary); err != nil {
			return err
		}
	}
	text, err := generate(ctx, binary)
	if err != nil {
		return err
	}
	return os.WriteFile(out, []byte(text), 0o644)
}

// buildRelease builds cmd/costroid with the release workflow's flags for
// linux/amd64, the platform of NOTICE. Every release platform links the same
// modules.
func buildRelease(ctx context.Context, binary string) error {
	command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", binary, costroidPackage)
	command.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("build costroid: %w: %s", err, output)
	}
	return nil
}

// generate gives NOTICE's text for the modules binary links.
func generate(ctx context.Context, binary string) (string, error) {
	modules, err := linkedModules(ctx, binary)
	if err != nil {
		return "", err
	}
	sections := make([]section, 0, len(modules))
	for _, m := range modules {
		files, err := licenceFiles(m)
		if err != nil {
			return "", err
		}
		sections = append(sections, section{module: m, files: files})
	}
	return render(sections), nil
}
