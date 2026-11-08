package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/reconcile"
)

// editDay rewrites a kept day's day.json through edit.
func editDay(t *testing.T, inputs, date string, edit func(record map[string]any)) {
	t.Helper()
	path := filepath.Join(inputs, date, dayFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	edit(record)
	edited, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(edited, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// nameWriter records writer in a kept day's day.json as the release that
// kept it.
func nameWriter(t *testing.T, inputs, date, writer string) {
	t.Helper()
	editDay(t, inputs, date, func(record map[string]any) { record["writer"] = writer })
}

// recordedWriter reads the writer a kept day's day.json records.
func recordedWriter(t *testing.T, inputs, date string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(inputs, date, dayFile))
	if err != nil {
		t.Fatal(err)
	}
	var record dayRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	return record.Writer
}

func TestAccuracyPageNamesTheWriterVersion(t *testing.T) {
	saved, prefixed := passingDay(firstDay), passingDay("2026-11-11")
	inputs := keepAll(t, saved, prefixed)
	nameWriter(t, inputs, prefixed.date, "v"+shipped)
	page, got := writeAndCheck(t, inputs, 2)
	if got.code != 0 {
		t.Fatalf("-check exited %d: %s", got.code, got.stderr)
	}
	for _, date := range []string{saved.date, prefixed.date} {
		text := dayLine(t, page, date)
		if want := "| claude-code 2.1.316, Costroid " + shipped + " |"; !strings.Contains(text, want) {
			t.Errorf("the line does not name the release that wrote its rows, %q: %s", want, text)
		}
		if verdictOf(t, text) != "pass" {
			t.Errorf("verdict %q, want pass: %s", verdictOf(t, text), text)
		}
	}
}

func TestSaveRecordsTheWriter(t *testing.T) {
	inputs := keepAll(t, passingDay(firstDay))
	if writer := recordedWriter(t, inputs, firstDay); writer != shipped {
		t.Errorf("-save recorded the writer %q, want the release it was built as, %s", writer, shipped)
	}

	built := version
	t.Cleanup(func() { version = built })
	version = "dev"
	inputs = keepAll(t, passingDay(firstDay))
	if writer := recordedWriter(t, inputs, firstDay); writer != "dev" {
		t.Errorf("a development build recorded the writer %q, want dev", writer)
	}
	page, got := writeAndCheck(t, inputs, 1)
	if got.code != exitRefused {
		t.Fatalf("-check exited %d on a day a development build kept, want %d", got.code, exitRefused)
	}
	text := dayLine(t, page, firstDay)
	if verdictOf(t, text) != failWriterUnreleased || !strings.Contains(text, "| claude-code 2.1.316, Costroid dev |") {
		t.Errorf("a day a development build kept does not read as no release: %s", text)
	}
}

func TestADayWithNoWriterFails(t *testing.T) {
	inputs := keepAll(t, passingDay(firstDay))
	editDay(t, inputs, firstDay, func(record map[string]any) { delete(record, "writer") })
	page, got := writeAndCheck(t, inputs, 1)
	if got.code != exitRefused {
		t.Fatalf("-check exited %d on a day that names no writer, want %d", got.code, exitRefused)
	}
	text := dayLine(t, page, firstDay)
	if verdictOf(t, text) != failNoWriter || !strings.Contains(text, "| claude-code 2.1.316, Costroid none |") {
		t.Errorf("a day that names no writer does not fail as one: %s", text)
	}
	if !strings.Contains(got.stderr, firstDay+" did not pass") {
		t.Errorf("-check did not name the day: %s", got.stderr)
	}
}

func TestAccuracyPinLagsNoRelease(t *testing.T) {
	releases := []release{
		{"1.0.0-alpha.4", "2026-11-09"},
		{"1.0.0-alpha.3", "2026-11-07"},
		{"1.0.0-alpha.2", "2026-11-07"},
		{"1.0.0-alpha.1", "2026-11-01"},
	}
	for _, test := range []struct {
		name, writer, day, want string
	}{
		{"the newest release out on the day", "1.0.0-alpha.3", "2026-11-08", ""},
		{"one release behind", "1.0.0-alpha.2", "2026-11-08", ""},
		{"two releases behind", "1.0.0-alpha.1", "2026-11-08", failWriterLags},
		{"two behind a release out that day", "1.0.0-alpha.2", "2026-11-09", failWriterLags},
		{"a release out only after the day", "1.0.0-alpha.4", "2026-11-08", failWriterUnreleased},
		{"a day before any release", "1.0.0-alpha.1", "2026-10-31", failWriterUnreleased},
		{"a build the changelog does not list", "dev", "2026-11-08", failWriterUnreleased},
		{"a day that names no writer", "", "2026-11-08", failNoWriter},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := writerFailure(test.writer, test.day, releases); got != test.want {
				t.Errorf("writer %q on %s: got %q, want %q", test.writer, test.day, got, test.want)
			}
		})
	}

	for writer, want := range map[string]string{"1.0.0-alpha.2": "pass", "1.0.0-alpha.1": failWriterLags} {
		inputs := keepAll(t, passingDay(firstDay))
		nameWriter(t, inputs, firstDay, writer)
		page, got := writeAndCheck(t, inputs, 1)
		if verdict := verdictOf(t, dayLine(t, page, firstDay)); verdict != want {
			t.Errorf("%s against %s: verdict %q, want %q", writer, shipped, verdict, want)
		}
		if failed := got.code != 0; failed != (want != "pass") {
			t.Errorf("-check exited %d with the writer %s: %s", got.code, writer, got.stderr)
		}
	}
}

func TestModuleChangelogListsItsReleases(t *testing.T) {
	releases, err := readReleases("")
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) == 0 || !slices.IsSortedFunc(releases, func(a, b release) int { return strings.Compare(b.date, a.date) }) {
		t.Errorf("CHANGELOG.md lists %d releases, newest first by date expected: %v", len(releases), releases)
	}
}

func TestAccuracyVerdictReadsTheClassLines(t *testing.T) {
	settled := func(classes ...classFigure) reconciledRow {
		return reconciledRow{Day: firstDay, State: string(reconcile.Matched), Dollars: &dollarFigure{
			Classes: classes, Verdict: string(reconcile.Within), Ledger: "0.81", Provider: "0.816", Residual: "0.006", Unattributed: "0.006",
		}}
	}
	input := classFigure{Class: "input_uncached", Ledger: "0.2", Provider: "0.2", Delta: "0", Within: true}
	output := classFigure{Class: "output", Ledger: "0.5", Provider: "0.506", Delta: "0.006", Within: false}
	for name, test := range map[string]struct {
		row  reconciledRow
		want []string
	}{
		"every class within 1%":       {settled(input), nil},
		"one class outside 1%":        {settled(input, output), []string{failClassDollars}},
		"a report not split by class": {settled(), nil},
		"a class outside on a missed day": {func() reconciledRow {
			r := settled(output)
			r.State = string(reconcile.TokensMiss)
			return r
		}(), []string{failTokens, failClassDollars}},
	} {
		t.Run(name, func(t *testing.T) {
			judged, err := judge(test.row)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(judged.failures, test.want) {
				t.Errorf("failures %q, want %q", judged.failures, test.want)
			}
		})
	}

	missed := passingDay(firstDay)
	missed.outputReported = 50600
	inputs := t.TempDir()
	keepSplit(t, inputs, missed, "50.600000")
	page, got := writeAndCheck(t, inputs, 1)
	if got.code != exitRefused {
		t.Fatalf("-check exited %d with output's dollars outside 1%%, want %d: %s", got.code, exitRefused, got.stderr)
	}
	text := dayLine(t, page, firstDay)
	if want := failTokens + ", " + failClassDollars; verdictOf(t, text) != want {
		t.Errorf("verdict %q, want %q: %s", verdictOf(t, text), want, text)
	}
	if !strings.Contains(text, "| 0.81 / 0.816 (0.74%); input_uncached 0.2 / 0.2 (0.00%);") || !strings.Contains(text, "; output 0.5 / 0.506 (1.19%) |") {
		t.Errorf("the line does not state the day's dollars and each class's: %s", text)
	}
}
