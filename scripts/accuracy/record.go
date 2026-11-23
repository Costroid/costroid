package main

import (
	"slices"
	"strings"

	"github.com/Costroid/costroid/internal/accuracy"
)

// recordOf is the record a release carries: the lines of the page's last
// accuracy.Window days, in the page's order, each with what the page states
// of it. Every line of those days is kept, the failed ones included.
func recordOf(lines []line) accuracy.Record {
	var days []string
	for _, l := range lines {
		if !slices.Contains(days, l.day) {
			days = append(days, l.day)
		}
	}
	slices.Sort(days)
	window := days[max(0, len(days)-accuracy.Window):]
	var record accuracy.Record
	for _, l := range lines {
		if !slices.Contains(window, l.day) {
			continue
		}
		record.Lines = append(record.Lines, accuracy.Line{
			Day:        l.day,
			Sources:    namesOf(l.source),
			Agents:     agentsOf(l.agent),
			Verdict:    l.verdict(),
			TokenError: tokenErrorOf(l.tokens),
		})
	}
	return record
}

// namesOf splits a cell's names, as joined wrote them.
func namesOf(text string) []string {
	if text == noFigure {
		return nil
	}
	return strings.Split(text, ", ")
}

// agentsOf reads each "agent version" the line names.
func agentsOf(text string) []accuracy.Agent {
	var agents []accuracy.Agent
	for _, named := range namesOf(text) {
		name, version, _ := strings.Cut(named, " ")
		agents = append(agents, accuracy.Agent{Name: name, Version: version})
	}
	return agents
}

// tokenErrorOf is the largest token class's error, exact, as the page's
// largest token error reads it: a class with no provider count is larger
// than any other. It is empty for a line that compares no tokens.
func tokenErrorOf(checks []tokenFigure) string {
	var gaps largest
	for _, check := range checks {
		gap, _ := gapOf(check.Delta, check.Provider)
		gaps = gaps.with(gap)
	}
	if !gaps.seen {
		return ""
	}
	size, known := gaps.gap.Size()
	if !known {
		return accuracy.NoTokenError
	}
	return size.RatString()
}
