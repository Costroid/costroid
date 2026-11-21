package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"

	"github.com/Costroid/costroid/internal/cli"
	"github.com/Costroid/costroid/internal/recipelint"
)

// defaultWorkflows is where GitHub reads a repository's workflows.
const defaultWorkflows = ".github/workflows"

var lintHelp = cli.Page{
	Command: "lint",
	Summary: `lint reads your own GitHub workflows, .github/workflows when no path is named,
and judges each one that starts a coding agent: who can start a run that spends
your key, and what the agent's job holds.`,
	Usage:    []string{"costroid lint [--strict] [paths]"},
	Examples: []string{"costroid lint", "costroid lint --strict .github/workflows/claude.yml"},
	Flags:    []cli.Row{{"--strict", "fail on a medium finding too"}},
	Sections: []cli.Section{
		{Heading: "Findings", Body: []cli.Block{cli.Text("Each finding is high, medium or a\nnote. docs/lint.md lists the rules.")}},
	},
	Exits: []cli.Row{
		{"0", "no high finding, and with --strict no medium one"},
		{"1", "a high finding, or with --strict a medium one"},
		cli.UsageExit,
		{"125", "costroid could not read a path, or found no YAML file in it"},
	},
	Docs: "lint",
}.String()

func lintCommand(args []string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("lint", flag.ContinueOnError)
	strict := set.Bool("strict", false, "fail on a medium finding too")
	err := parseFlags(set, args, lintHelp, stdout)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return cli.Fail(stderr, cli.Mistaken("lint", err))
	}
	paths := set.Args()
	if len(paths) == 0 {
		paths = []string{defaultWorkflows}
	}
	report, err := recipelint.LintWith(paths, recipelint.Options{Profile: recipelint.Customer})
	if errors.Is(err, fs.ErrNotExist) && set.NArg() == 0 {
		return cli.Fail(stderr, fmt.Errorf("%s does not exist\nlint reads the GitHub workflows of the repository it runs in.\nRun costroid lint at a repository's root, or name the files: costroid lint <paths>.", defaultWorkflows))
	}
	if err != nil {
		return cli.Fail(stderr, cli.File(err))
	}
	for _, note := range report.Notes {
		fmt.Fprintln(stdout, note)
	}
	counts := map[recipelint.Severity]int{recipelint.Note: len(report.Notes)}
	for _, finding := range report.Findings {
		fmt.Fprintln(stderr, finding)
		counts[finding.Severity]++
	}
	notes := counts[recipelint.Note]
	fmt.Fprintf(stdout, "costroid: %d %s, %d with an agent: %d high, %d medium, %d %s\n",
		report.Files, plural("file", report.Files), report.Agents, counts[recipelint.High], counts[recipelint.Medium], notes, plural("note", notes))
	if counts[recipelint.High] > 0 || *strict && counts[recipelint.Medium] > 0 {
		return exitRejected
	}
	return 0
}
