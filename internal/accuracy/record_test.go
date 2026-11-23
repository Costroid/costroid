package accuracy

import (
	"math/big"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const (
	page       = "../../docs/accuracy.md"
	recordFile = "record.go"
	// daysHeading opens the page's table of days, one line per row.
	daysHeading = "\n## Days\n\n"
	dayCells    = 11
	noFigure    = "none"
)

// pageRecord reads the record from the page alone: the lines of its last
// Window days, each as its cells state it. It shares no code with the
// command that writes the page.
func pageRecord(t *testing.T, text string) Record {
	t.Helper()
	_, table, found := strings.Cut(text, daysHeading)
	if !found {
		t.Fatalf("the page holds no %q", strings.TrimSpace(daysHeading))
	}
	var lines []Line
	for number, row := range strings.Split(strings.TrimSpace(table), "\n") {
		if number < 2 {
			continue
		}
		cells := strings.Split(strings.TrimSuffix(strings.TrimPrefix(row, "| "), " |"), " | ")
		if len(cells) != dayCells {
			t.Fatalf("a day's row holds %d cells, want %d: %s", len(cells), dayCells, row)
		}
		lines = append(lines, Line{
			Day:        cells[0],
			Sources:    listed(cells[1]),
			Agents:     agentsOf(t, cells[2]),
			Verdict:    cells[9],
			TokenError: tokenError(t, cells[5]),
		})
	}
	var days []string
	for _, line := range lines {
		if !slices.Contains(days, line.Day) {
			days = append(days, line.Day)
		}
	}
	slices.Sort(days)
	window := days[max(0, len(days)-Window):]
	var record Record
	for _, line := range lines {
		if slices.Contains(window, line.Day) {
			record.Lines = append(record.Lines, line)
		}
	}
	return record
}

func listed(text string) []string {
	if text == noFigure {
		return nil
	}
	return strings.Split(text, ", ")
}

// agentsOf reads the cell "claude-code 2.1.316, opencode 1.19.4, Costroid
// 1.0.0-alpha.24": every agent with its version, then the release.
func agentsOf(t *testing.T, text string) []Agent {
	t.Helper()
	named, _, found := cutLast(text, ", Costroid ")
	if !found {
		t.Fatalf("the agent cell names no release: %s", text)
	}
	var agents []Agent
	for _, agent := range listed(named) {
		name, version, _ := strings.Cut(agent, " ")
		agents = append(agents, Agent{Name: name, Version: version})
	}
	return agents
}

func cutLast(text, separator string) (string, string, bool) {
	at := strings.LastIndex(text, separator)
	if at < 0 {
		return text, "", false
	}
	return text[:at], text[at+len(separator):], true
}

// tokenError reads the cell "input_uncached 6 / 6 (0.00%); output 412 / 413
// (0.24%)" and computes the largest class's error from its two counts, in
// percent and exact. The rounded percentage the page prints is not read.
func tokenError(t *testing.T, text string) string {
	t.Helper()
	if text == noFigure {
		return ""
	}
	var largest *big.Rat
	for _, class := range strings.Split(text, "; ") {
		fields := strings.Fields(class)
		if len(fields) != 5 || fields[2] != "/" {
			t.Fatalf("a token class reads %q", class)
		}
		ledger, okLedger := new(big.Rat).SetString(fields[1])
		provider, okProvider := new(big.Rat).SetString(fields[3])
		if !okLedger || !okProvider {
			t.Fatalf("a token class's counts are not numbers: %q", class)
		}
		if provider.Sign() == 0 {
			return NoTokenError
		}
		gap := new(big.Rat).Sub(provider, ledger)
		gap.Abs(gap).Quo(gap, provider).Mul(gap, big.NewRat(100, 1))
		if largest == nil || gap.Cmp(largest) > 0 {
			largest = gap
		}
	}
	return largest.RatString()
}

// TestTheRecordIsThePagesLastFourteenDays reads the committed page and holds
// the record this build carries to it, field by field: the same lines of the
// same days, the failed ones included. The record's file is the one the
// writer writes for those lines, byte for byte.
func TestTheRecordIsThePagesLastFourteenDays(t *testing.T) {
	data, err := os.ReadFile(page)
	if err != nil {
		t.Fatal(err)
	}
	want := pageRecord(t, string(data))
	if len(want.Lines) == 0 {
		t.Fatal("the page holds no day")
	}
	if !reflect.DeepEqual(Built, want) {
		for index := range max(len(Built.Lines), len(want.Lines)) {
			var built, read Line
			if index < len(Built.Lines) {
				built = Built.Lines[index]
			}
			if index < len(want.Lines) {
				read = want.Lines[index]
			}
			if !reflect.DeepEqual(built, read) {
				t.Errorf("line %d of the record is\n  %+v\nand the page's is\n  %+v", index+1, built, read)
			}
		}
		t.Fatal("the record is not the page's last days: run go run ./scripts/accuracy -write docs/accuracy.md -record internal/accuracy/record.go")
	}
	source, err := want.GoSource()
	if err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile(recordFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(committed) != string(source) {
		t.Errorf("%s is not the file the writer writes for the page's lines", recordFile)
	}
}

// TestTheRecordReadsBackAsItWasWritten writes a record of each kind of line
// and holds the Go it writes to the values it was written from.
func TestTheRecordReadsBackAsItWasWritten(t *testing.T) {
	record := Record{Lines: []Line{
		{Day: "2026-11-05", Verdict: "no rows, day names no writer"},
		{Day: "2026-11-06", Sources: []string{"anthropic", "openai"}, Agents: []Agent{{Name: "claude-code", Version: "2.1.316"}, {Name: "codex"}}, Verdict: "no provider report"},
		{Day: "2026-11-07", Sources: []string{"anthropic"}, Agents: []Agent{{Name: "claude-code", Version: "2.1.316"}}, Verdict: VerdictPass, TokenError: "1/1000"},
	}}
	source, err := record.GoSource()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`{Day: "2026-11-05", Verdict: "no rows, day names no writer"},`,
		`{Day: "2026-11-06", Sources: []string{"anthropic", "openai"}, Agents: []Agent{{Name: "claude-code", Version: "2.1.316"}, {Name: "codex", Version: ""}}, Verdict: "no provider report"},`,
		`TokenError: "1/1000"},`,
	} {
		if !strings.Contains(string(source), want) {
			t.Errorf("the record's Go holds no %s:\n%s", want, source)
		}
	}
	if !strings.HasPrefix(string(source), "// Code generated ") || !strings.Contains(string(source), " DO NOT EDIT.\n") {
		t.Errorf("the record's Go is not marked as generated:\n%s", source)
	}
	empty, err := Record{}.GoSource()
	if err != nil || !strings.HasSuffix(string(empty), "var Built = Record{}\n") {
		t.Errorf("an empty record writes %s, %v", empty, err)
	}
}
