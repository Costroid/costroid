package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"time"
)

// costroidPackage is built when no binary is named. Its import path builds
// from anywhere inside this module.
const costroidPackage = "github.com/Costroid/costroid/cmd/costroid"

func goBuild(ctx context.Context, binary string, stderr io.Writer) error {
	command := exec.CommandContext(ctx, "go", "build", "-o", binary, costroidPackage)
	command.Stderr = stderr
	return command.Run()
}

// checkPage refuses a page that is not exactly its recomputation, then asks
// for the run of consecutive passing days.
func checkPage(ctx context.Context, opts options, stdout, stderr io.Writer) error {
	page, err := os.ReadFile(opts.check)
	if err != nil {
		return err
	}
	lines, err := compute(ctx, opts, stderr)
	if err != nil {
		return err
	}
	if difference := firstDifference(page, render(lines)); difference != "" {
		return fmt.Errorf("%w: %s %s", errRefused, opts.check, difference)
	}
	first, last, err := consecutive(lines, opts.days)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "accuracy: %d consecutive UTC days passed, %s to %s\n", opts.days, first, last)
	return nil
}

// firstDifference names the first line where the page and its recomputation
// part, or returns nothing when they are the same bytes.
func firstDifference(page, recomputed []byte) string {
	if bytes.Equal(page, recomputed) {
		return ""
	}
	have := bytes.Split(page, []byte("\n"))
	want := bytes.Split(recomputed, []byte("\n"))
	for number := range max(len(have), len(want)) {
		if number >= len(have) || number >= len(want) || !bytes.Equal(have[number], want[number]) {
			return fmt.Sprintf("line %d does not match its inputs.\n  page:   %q\n  inputs: %q", number+1, lineAt(have, number), lineAt(want, number))
		}
	}
	return "does not match its inputs"
}

func lineAt(lines [][]byte, number int) []byte {
	if number < len(lines) {
		return lines[number]
	}
	return nil
}

// consecutive passes when the last n days on the page follow one another and
// every line of each passed.
func consecutive(lines []line, n int) (string, string, error) {
	passed := map[string]bool{}
	var days []string
	for _, l := range lines {
		if _, seen := passed[l.day]; !seen {
			days = append(days, l.day)
			passed[l.day] = true
		}
		passed[l.day] = passed[l.day] && l.passed()
	}
	slices.Sort(days)
	if len(days) < n {
		return "", "", fmt.Errorf("%w: the page holds %d days, and %d consecutive passing days are needed", errRefused, len(days), n)
	}
	last := days[len(days)-n:]
	for i, day := range last {
		if i > 0 && !follows(last[i-1], day) {
			return "", "", fmt.Errorf("%w: %s does not follow %s, so the last %d days are not consecutive", errRefused, day, last[i-1], n)
		}
		if !passed[day] {
			return "", "", fmt.Errorf("%w: %s did not pass", errRefused, day)
		}
	}
	return last[0], last[n-1], nil
}

func follows(previous, day string) bool {
	before, errBefore := time.Parse(dayLayout, previous)
	after, errAfter := time.Parse(dayLayout, day)
	return errBefore == nil && errAfter == nil && before.AddDate(0, 0, 1).Equal(after)
}
