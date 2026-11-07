package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/Costroid/costroid/internal/recipelint"
)

// defaultWorkflows is where GitHub reads a repository's workflows.
const defaultWorkflows = ".github/workflows"

const lintHelp = `Usage:
  costroid lint [--strict] [paths]

lint reads your own GitHub workflows, .github/workflows when no path is named,
and judges each one that starts a coding agent: who can start a run that spends
your key, and what the agent's job holds. Each finding is high, medium or a
note. docs/lint.md lists the rules.

Exit codes of lint:
  0                 no high finding, and with --strict no medium one
  1                 a high finding, or with --strict a medium one
  125               costroid could not read a path, or found no YAML file in it
`

func lintCommand(args []string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("lint", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprint(stderr, lintHelp) }
	strict := set.Bool("strict", false, "fail on a medium finding too")
	err := set.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return exitFailure
	}
	paths := set.Args()
	if len(paths) == 0 {
		paths = []string{defaultWorkflows}
	}
	report, err := recipelint.LintWith(paths, recipelint.Options{Profile: recipelint.Customer})
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
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
