package main

import (
	"fmt"
	"io"

	"github.com/Costroid/costroid/internal/recipelint"
)

const exitRejected = 1

func lintRecipes(paths []string, stdout, stderr io.Writer) int {
	if len(paths) == 0 {
		fmt.Fprint(stderr, "costroid: lint-recipes needs at least one path\n")
		return exitFailure
	}
	report, err := recipelint.Lint(paths)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	for _, note := range report.Notes {
		fmt.Fprintln(stdout, note)
	}
	for _, finding := range report.Findings {
		fmt.Fprintln(stderr, finding)
	}
	fmt.Fprintf(stdout, "costroid: %d files, %d rejected lines\n", report.Files, len(report.Findings))
	if len(report.Findings) > 0 {
		return exitRejected
	}
	return 0
}
