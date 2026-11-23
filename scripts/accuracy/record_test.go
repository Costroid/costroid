package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recordBeside is where a test writes the record of a page it writes.
func recordBeside(page string) string {
	return filepath.Join(filepath.Dir(page), "record.go")
}

// TestWriteKeepsThePageAndTheRecordTogether writes 15 kept days. The record
// holds the lines of the page's last 14, the day that missed included, each
// with the page's verdict and its largest token error as an exact fraction,
// and leaves out the first day as the window does.
func TestWriteKeepsThePageAndTheRecordTogether(t *testing.T) {
	days := consecutiveDays(15)
	miss := &days[1]
	miss.outputReported = 50600
	miss.amountCents, miss.listCents = "81.600000", "81.600000"
	edge := &days[2]
	edge.outputReported = 50505
	edge.amountCents, edge.listCents = "81.505000", "81.505000"
	page, _ := writeAndCheck(t, keepAll(t, days...), 14)
	record, err := os.ReadFile(recordBeside(page))
	if err != nil {
		t.Fatalf("-write wrote no record beside the page: %v", err)
	}
	text := string(record)
	agent := `Sources: []string{"anthropic"}, Agents: []Agent{{Name: "claude-code", Version: "2.1.316"}}`
	for _, want := range []string{
		`{Day: "2026-11-11", ` + agent + `, Verdict: "token miss", TokenError: "300/253"},`,
		`{Day: "2026-11-12", ` + agent + `, Verdict: "pass", TokenError: "10100/10101"},`,
		`{Day: "2026-11-24", ` + agent + `, Verdict: "pass", TokenError: "0"},`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the record holds no %s:\n%s", want, text)
		}
	}
	if lines := strings.Count(text, "{Day: "); lines != 14 {
		t.Errorf("the record holds %d lines, want the 14 of the page's last 14 days:\n%s", lines, text)
	}
	if strings.Contains(text, firstDay) {
		t.Errorf("the record holds %s, a day before the page's last 14:\n%s", firstDay, text)
	}
	for _, day := range []string{"2026-11-11", "2026-11-12", "2026-11-24"} {
		if verdict := verdictOf(t, dayLine(t, page, day)); !strings.Contains(text, `{Day: "`+day+`", `+agent+`, Verdict: "`+verdict+`"`) {
			t.Errorf("the page's verdict for %s, %q, is not the record's", day, verdict)
		}
	}
}

func TestRecordGoesWithWrite(t *testing.T) {
	inputs := keepAll(t, passingDay(firstDay))
	page := filepath.Join(t.TempDir(), "accuracy.md")
	for _, args := range [][]string{
		{"-write", page, "-inputs", inputs, "-costroid", costroid, "-changelog", changelogFile},
		{"-check", page, "-record", recordBeside(page), "-inputs", inputs, "-costroid", costroid, "-changelog", changelogFile},
	} {
		got := runAccuracy(t, offline(t), args...)
		if got.code != exitFailure || !strings.Contains(got.stderr, "-write and -record go together") {
			t.Errorf("%q exited %d: %s", args, got.code, got.stderr)
		}
	}
	if _, err := os.Stat(page); err == nil {
		t.Error("-write without -record wrote the page")
	}
}
