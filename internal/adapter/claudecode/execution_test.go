package claudecode

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/focus"
)

const notRecordedExecutionFile = "testdata/not-recorded/execution-file/claude-execution-output.json"

func readNotRecorded(t *testing.T) adapter.Report {
	t.Helper()
	file, err := os.Open(notRecordedExecutionFile)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	report, err := Agent{ConfigDir: t.TempDir()}.ReadExecutionFile(file, recorded)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestExecutionFileGivesTheLastResultsPerModelTotals(t *testing.T) {
	report := readNotRecorded(t)
	if report.CostSource != SourceExecutionFile || report.CostBasis != focus.ListEquivalent {
		t.Fatalf("%s, %s", report.CostSource, report.CostBasis)
	}
	if report.SessionID != "00000000-0000-4000-8000-000000000002" || report.AgentVersion != "2.1.316" {
		t.Fatalf("%s %s", report.SessionID, report.AgentVersion)
	}
	if len(report.Requests) != 1 || report.Requests[0].Grain != adapter.PerRun {
		t.Fatalf("%+v", report.Requests)
	}
	got := report.Requests[0].Tokens
	if got.InputUncached != 8 || got.CacheWrite5m != 3495 || got.CacheWrite1h != 4450 || got.CacheRead != 15605 || got.Output != 272 {
		t.Fatalf("%+v", got)
	}
}

func TestExecutionFileGradesNoneBecauseCostroidHeldNoBudget(t *testing.T) {
	enforcement := readNotRecorded(t).Enforcement
	if enforcement.Grade != adapter.GradeNone || !strings.Contains(enforcement.Note, "not started by costroid") {
		t.Fatalf("%+v", enforcement)
	}
}

func TestExecutionFileTakesItsSpanFromTheMessages(t *testing.T) {
	requests := readNotRecorded(t).Requests
	start, end := requests[0].Start, requests[0].End
	if !end.After(start) || end.After(recorded.Add(time.Hour)) || start.Format(time.DateOnly) != "2026-11-01" {
		t.Fatalf("%s to %s", start, end)
	}
}

func TestExecutionFileWithNoTimestampsEndsWhenSavedAndStartsByTheResultsDuration(t *testing.T) {
	body := `[{"type":"system","subtype":"init","session_id":"s","apiKeySource":"ANTHROPIC_API_KEY"},
	{"type":"result","duration_ms":90000,"session_id":"s","modelUsage":{"claude-haiku-5-5":{"inputTokens":1,"outputTokens":2}}}]`
	report, err := Agent{ConfigDir: t.TempDir()}.ReadExecutionFile(strings.NewReader(body), recorded)
	if err != nil {
		t.Fatal(err)
	}
	request := report.Requests[0]
	if !request.End.Equal(recorded) || !request.Start.Equal(recorded.Add(-90*time.Second)) {
		t.Fatalf("%s to %s", request.Start, request.End)
	}
	if report.CostBasis != focus.Measured {
		t.Fatalf("a key-billed run reads %s", report.CostBasis)
	}
}

func TestExecutionFileLeavesEveryContentFieldBehind(t *testing.T) {
	raw, err := json.Marshal(readNotRecorded(t))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("SECRET-PROMPT-TEXT")) {
		t.Fatalf("prompt text in the report: %s", raw)
	}
}

func TestExecutionFileThatIsNotAnArrayIsRefused(t *testing.T) {
	for _, body := range []string{`{"type":"result"}`, ``, `[{"type":"result"`} {
		if _, err := (Agent{ConfigDir: t.TempDir()}).ReadExecutionFile(strings.NewReader(body), recorded); err == nil {
			t.Errorf("%q was read", body)
		}
	}
}

func TestExecutionFileWithNoResultIsRefusedRatherThanReadAsZero(t *testing.T) {
	body := `[{"type":"system","subtype":"init","session_id":"s"}]`
	if _, err := (Agent{ConfigDir: t.TempDir()}).ReadExecutionFile(strings.NewReader(body), recorded); err == nil {
		t.Fatal("a run with no result was read")
	}
}

func TestExecutionFileAgreeingWithSessionFilesKeepsPerRequestRows(t *testing.T) {
	const session = "00000000-0000-4000-8000-000000000002"
	config := installSession(t, "subagent", session)
	file, err := os.Open(filepath.Clean(notRecordedExecutionFile))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	report, err := Agent{ConfigDir: config}.ReadExecutionFile(file, recorded)
	if err != nil {
		t.Fatal(err)
	}
	if report.CostSource != SourceSessionFile || len(report.Requests) != 4 {
		t.Fatalf("%s, %d requests", report.CostSource, len(report.Requests))
	}
	if report.Enforcement.Grade != adapter.GradeNone {
		t.Fatalf("%+v", report.Enforcement)
	}
}
