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
	Usage:    []string{"costroid lint [--strict] [--json] [paths]"},
	Examples: []string{"costroid lint", "costroid lint --strict .github/workflows/claude.yml", "costroid lint --json"},
	Flags: []cli.Row{
		{"--strict", "fail on a medium finding too"},
		{"--json", "print the findings as one JSON object"},
	},
	Sections: []cli.Section{
		{Heading: "Findings", Body: []cli.Block{cli.Text(`Each finding is high, medium or a
note. https://docs.costroid.com/lint lists the rules.

lint prints one line per finding on stdout, the notes last, then the count.
--json prints one object in their place, costroid.lint/0.1: each finding's
rule, severity, file, line and message, and the counts. https://docs.costroid.com/output
describes the object.`)}},
	},
	Exits: []cli.Row{
		{"0", "no high finding, and with --strict no medium one"},
		{"1", "a high finding, or with --strict a medium one"},
		cli.UsageExit,
		{"125", "costroid could not read a path, or found no YAML file in it"},
	},
	Docs: "lint",
}.String()

// lintSchema names the object lint --json prints.
const lintSchema = "costroid.lint/0.1"

// lintFindings is what lint found: costroid.lint/0.1. Findings holds the high
// and medium findings, then the notes, as lint prints them.
type lintFindings struct {
	Schema string `json:"schema"`
	// Files counts the files read, and Agents those that start an agent.
	Files    int           `json:"files"`
	Agents   int           `json:"agents"`
	Findings []lintFinding `json:"findings"`
	Count    lintCount     `json:"count"`
}

type lintFinding struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Message  string `json:"message"`
}

type lintCount struct {
	High   int `json:"high"`
	Medium int `json:"medium"`
	Note   int `json:"note"`
}

// graded names a finding by where it stands and the rule it breaks.
type graded struct {
	path string
	line int
	rule string
}

// lintGrades is the severity costroid lint gives each finding it has in the
// files at paths. lint-recipes states its rejected lines by it, so one
// finding reads the same in the object of either command. Files lint cannot
// read give no grades.
func lintGrades(paths []string) map[graded]recipelint.Severity {
	grades := map[graded]recipelint.Severity{}
	report, err := recipelint.LintWith(paths, recipelint.Options{Profile: recipelint.Customer})
	if err != nil {
		return grades
	}
	for _, finding := range report.Findings {
		grades[graded{finding.Path, finding.Line, finding.Rule}] = finding.Severity
	}
	for _, note := range report.Notes {
		grades[graded{note.Path, note.Line, note.Rule}] = recipelint.Note
	}
	return grades
}

// lintObject is what a lint found as the object --json prints, beside the
// lines that say the same. A line lint-recipes rejects carries no severity of
// its own. The object states it as lint grades the same finding, by grades,
// and as high where the rule is lint-recipes' alone: it fails the command, as
// a high finding fails lint.
func lintObject(report recipelint.Report, grades map[graded]recipelint.Severity) (lintFindings, []string) {
	found := lintFindings{Schema: lintSchema, Files: report.Files, Agents: report.Agents, Findings: []lintFinding{}}
	var lines []string
	for _, finding := range report.Findings {
		lines = append(lines, finding.String())
		severity := finding.Severity
		if severity == "" {
			severity = recipelint.High
			if grade, known := grades[graded{finding.Path, finding.Line, finding.Rule}]; known {
				severity = grade
			}
		}
		found.Findings = append(found.Findings, lintFinding{Rule: finding.Rule, Severity: string(severity), File: finding.Path, Line: finding.Line, Message: finding.Message})
		switch severity {
		case recipelint.High:
			found.Count.High++
		case recipelint.Medium:
			found.Count.Medium++
		case recipelint.Note:
			found.Count.Note++
		}
	}
	for _, note := range report.Notes {
		lines = append(lines, note.String())
		found.Findings = append(found.Findings, lintFinding{Rule: note.Rule, Severity: string(recipelint.Note), File: note.Path, Line: note.Line, Message: note.Message})
		found.Count.Note++
	}
	return found, lines
}

func lintCommand(args []string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("lint", flag.ContinueOnError)
	strict := set.Bool("strict", false, "fail on a medium finding too")
	asJSON := set.Bool("json", false, "print the findings as one JSON object")
	err := parseFlags(set, args, lintHelp, stdout, stderr)
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
		return cli.Fail(stderr, cli.Stopped(defaultWorkflows+" does not exist", "lint reads the GitHub workflows of the repository it runs in.", "Run costroid lint at a repository's root, or name the files: costroid lint <paths>."))
	}
	if err != nil {
		return cli.Fail(stderr, cli.File(err))
	}
	found, lines := lintObject(report, nil)
	if *asJSON {
		if err := printObject(stdout, found); err != nil {
			return cli.Fail(stderr, err)
		}
	} else {
		for _, line := range lines {
			fmt.Fprintln(stdout, line)
		}
		fmt.Fprintf(stdout, "costroid: %d %s, %d with an agent: %d high, %d medium, %d %s\n",
			found.Files, plural("file", found.Files), found.Agents, found.Count.High, found.Count.Medium, found.Count.Note, plural("note", found.Count.Note))
	}
	if found.Count.High > 0 || *strict && found.Count.Medium > 0 {
		return exitRejected
	}
	return 0
}
