// Command pin moves the release that the README, the docs, the recipes, the
// Marketplace listing and the packaging name to another release, by version
// and commit.
//
// It refuses, and changes nothing, when the old version stands in a sentence
// that states history or a requirement, so a pin never rewrites what an
// earlier release did.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

const usage = `Usage:
  go run ./scripts/pin -version VERSION -commit COMMIT [-root DIR]

pin reads the release the recipes pin, then moves each reference to its
version and its commit to VERSION and COMMIT, the commit the tag vVERSION
names. It reads README.md, docs/, recipes/, marketplace/, the copy of the
two-block example under internal/recipelint/testdata and the $id of
costroid.schema.json. It sets the Helm chart's version and appVersion and the
Compose file's default image to VERSION.

It never changes CHANGELOG.md, docs_test.go, version_test.go or a recorded
fixture. When the old version stands in a sentence that states history or a
requirement, after "after", "before", "since", "until" or "up to", before
"and earlier" or "or later", in a sentence that says "in no release" or that
names a recorded run, it names the file and line and changes nothing. Reword
that sentence by date or commit first.

Exit codes: 0 moved, 1 refused or failed, 2 usage.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("pin", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprint(stderr, usage) }
	version := set.String("version", "", "the release to pin, such as 1.0.0-alpha.24")
	commit := set.String("commit", "", "the 40-hex commit the release's tag names")
	root := set.String("root", ".", "the repository's root")
	if err := set.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if set.NArg() != 0 {
		fmt.Fprintf(stderr, "pin: unexpected argument %q\n", set.Arg(0))
		return 2
	}
	target := release{version: *version, commit: *commit}
	if err := target.check(); err != nil {
		fmt.Fprintf(stderr, "pin: %v\n", err)
		return 2
	}
	report, err := pin(*root, target)
	var refused refusals
	if errors.As(err, &refused) {
		for _, refusal := range refused {
			fmt.Fprintf(stderr, "%s\n", refusal)
		}
		fmt.Fprintf(stderr, "pin: %d sentences state history with %s; reword each by date or commit, then pin again. Nothing was changed.\n", len(refused), report.from.version)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "pin: %v\n", err)
		return 1
	}
	for _, path := range report.changed {
		fmt.Fprintln(stdout, path)
	}
	fmt.Fprintf(stdout, "pin: %s at %s to %s at %s: %d references moved in %d files\n",
		report.from.version, report.from.commit, target.version, target.commit, report.moved, len(report.changed))
	return 0
}
