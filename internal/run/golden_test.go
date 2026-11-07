package run

import (
	"bytes"
	"context"
	"flag"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/decimal"
)

var update = flag.Bool("update", false, "rewrite the golden rows from the recorded subagent run")

// goldenRows are the two committed copies of the rows the recorded Claude
// Code 2.1.316 subagent run writes. The comment's tests render them and the
// server's tests ingest them. Both are written here, from the recording, so
// neither can fall behind what costroid run writes.
var goldenRows = []string{
	"../comment/testdata/rows.jsonl",
	"../server/testdata/claude-code-2.1.316-subagent.rows.jsonl",
}

// recordedSubagentRows runs the fake claude over the recorded subagent run,
// outside CI and with no billing account or credential, and returns the rows
// file it writes.
func recordedSubagentRows(t *testing.T) []byte {
	t.Helper()
	outsideCIForRun(t)
	t.Setenv("ENABLE_PROMPT_CACHING_1H", "")
	installClaudeRun(t, "subagent", subagentSession)
	var rows, stderr bytes.Buffer
	result, err := Run(context.Background(), Options{
		Argv: []string{agentNamed(t, "claude"), "-p", "work"}, Budget: decimal.MustParse("5"),
		Rows: &rows, Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: &stderr, Catalog: pinned(t),
	})
	if err != nil {
		t.Fatalf("%v: %s", err, stderr.String())
	}
	if result.Rows != 19 || len(result.Report.Warnings) != 0 {
		t.Fatalf("%d rows, warnings %q", result.Rows, result.Report.Warnings)
	}
	if err := checkListTotal(result.ListTotal, subagentList); err != nil {
		t.Fatal(err)
	}
	return rows.Bytes()
}

// TestRecordedRunWritesTheGoldenRows holds both committed copies of the
// golden rows to what the wrapper writes from the recording today. A change
// to the rows a run writes fails it until it is rerun with -update, which
// rewrites both copies, so the goldens every other package reads never go
// stale.
func TestRecordedRunWritesTheGoldenRows(t *testing.T) {
	written := recordedSubagentRows(t)
	for _, path := range goldenRows {
		if *update {
			if err := os.WriteFile(path, written, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		saved, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if line, differs := firstDifference(saved, written); differs {
			t.Errorf("%s is stale from line %d, rerun with -update", path, line)
		}
	}
}

// firstDifference is the first line at which two files differ, counted from 1.
func firstDifference(saved, written []byte) (int, bool) {
	if bytes.Equal(saved, written) {
		return 0, false
	}
	savedLines := strings.SplitAfter(string(saved), "\n")
	writtenLines := strings.SplitAfter(string(written), "\n")
	for i := range min(len(savedLines), len(writtenLines)) {
		if savedLines[i] != writtenLines[i] {
			return i + 1, true
		}
	}
	return min(len(savedLines), len(writtenLines)) + 1, true
}
