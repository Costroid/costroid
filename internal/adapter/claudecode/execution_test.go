package claudecode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

func TestExecutionFileThatIsNotAnArrayIsRefusedAndNamed(t *testing.T) {
	_, err := Agent{ConfigDir: t.TempDir()}.ReadExecutionFile(strings.NewReader(`{"type":"result"}`), recorded)
	if err == nil || !strings.Contains(err.Error(), "not a JSON array") {
		t.Fatalf("%v", err)
	}
}

func TestExecutionFileThatDoesNotParseIsRefused(t *testing.T) {
	for _, body := range []string{``, `[{"type":"result"`} {
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

const (
	executionInit   = `{"type":"system","subtype":"init","session_id":"s","apiKeySource":"ANTHROPIC_API_KEY"}`
	executionResult = `{"type":"result","duration_ms":90000,"session_id":"s","modelUsage":{"claude-haiku-5-5":{"inputTokens":1,"outputTokens":2}}}`
)

func readExecution(t *testing.T, body string) (adapter.Report, error) {
	t.Helper()
	return Agent{ConfigDir: t.TempDir()}.ReadExecutionFile(strings.NewReader(body), recorded)
}

func TestExecutionFileTolerates(t *testing.T) {
	want := adapter.Tokens{InputUncached: 1, Output: 2}
	cases := []struct {
		name, body, warning string
		start               time.Time
	}{
		{
			name:    "an element that is not an object",
			body:    `[` + executionInit + `,"a note",` + executionResult + `]`,
			warning: "the execution file held 1 element that is not a message, so it was skipped",
			start:   recorded.Add(-90 * time.Second),
		},
		{
			name:  "a duration with a fraction of a millisecond",
			body:  `[` + executionInit + `,{"type":"result","duration_ms":1234.5,"session_id":"s","modelUsage":{"claude-haiku-5-5":{"inputTokens":1,"outputTokens":2}}}]`,
			start: recorded.Add(-1234500 * time.Microsecond),
		},
		{
			name:  "a duration stated as text",
			body:  `[` + executionInit + `,{"type":"result","duration_ms":"90000","session_id":"s","modelUsage":{"claude-haiku-5-5":{"inputTokens":1,"outputTokens":2}}}]`,
			start: recorded.Add(-90 * time.Second),
		},
		{
			name:    "a timestamp that is a number",
			body:    `[` + executionInit + `,{"type":"user","timestamp":1793530500},` + executionResult + `]`,
			warning: "the execution file held 1 message whose timestamp is not RFC 3339 text, so the run's window comes from the others",
			start:   recorded.Add(-90 * time.Second),
		},
	}
	for _, tc := range cases {
		report, err := readExecution(t, tc.body)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if len(report.Requests) != 1 || report.Requests[0].Tokens != want {
			t.Errorf("%s: %+v", tc.name, report.Requests)
			continue
		}
		if start := report.Requests[0].Start; !start.Equal(tc.start) {
			t.Errorf("%s: starts %s, want %s", tc.name, start, tc.start)
		}
		var said []string
		for _, warning := range report.Warnings {
			if strings.HasPrefix(warning, "the execution file") {
				said = append(said, warning)
			}
		}
		if tc.warning == "" && len(said) != 0 || tc.warning != "" && (len(said) != 1 || said[0] != tc.warning) {
			t.Errorf("%s: warnings %q", tc.name, report.Warnings)
		}
	}
	for _, body := range []string{
		`[`,
		`[` + executionInit,
		`[` + executionInit + `,`,
		`[` + executionInit + `,{"type":"res`,
		`[` + executionInit + `,` + executionResult,
	} {
		_, err := readExecution(t, body)
		if err == nil || err.Error() != "claude-code: the execution file ends inside its array; a later result may be missing, so no rows were written" {
			t.Errorf("%q: %v", body, err)
		}
	}
}

func TestExecutionFileWithBadSyntaxIsNotCalledCutOff(t *testing.T) {
	for _, body := range []string{`[1 2]`, `[1 2`, `[1,]`, `[` + executionInit + `}`, `[` + executionInit + `x`, `[` + executionInit + `,` + executionResult + `}`} {
		_, err := readExecution(t, body)
		if err == nil || !strings.HasPrefix(err.Error(), "claude-code: read the execution file: ") {
			t.Errorf("%q: %v", body, err)
		}
	}
}

func TestExecutionFileOfEveryOddElementKeepsItsResult(t *testing.T) {
	file, err := os.Open(filepath.Join("testdata", "synthetic", "execution", "odd-elements.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	report, err := Agent{ConfigDir: t.TempDir()}.ReadExecutionFile(file, recorded)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Requests) != 1 || report.Requests[0].Tokens != (adapter.Tokens{InputUncached: 1, Output: 2}) {
		t.Fatalf("%+v", report.Requests)
	}
	joined := strings.Join(report.Warnings, "\n")
	if !strings.Contains(joined, "held 3 elements that are not a message, so they were skipped") || !strings.Contains(joined, "held 1 message whose timestamp") {
		t.Errorf("warnings %q", report.Warnings)
	}
	if start := report.Requests[0].Start.Format(time.RFC3339Nano); start != "2026-11-01T11:14:00Z" {
		t.Errorf("starts %s, want the one readable timestamp", start)
	}
}

func TestTruncatedExecutionFileFixtureIsRefusedAndSaysWhy(t *testing.T) {
	file, err := os.Open(filepath.Join("testdata", "synthetic", "execution", "truncated.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	_, err = Agent{ConfigDir: t.TempDir()}.ReadExecutionFile(file, recorded)
	if err == nil || !strings.Contains(err.Error(), "ends inside its array") {
		t.Fatalf("%v", err)
	}
}

func TestExecutionFileWithAnUnreadableResultSaysSo(t *testing.T) {
	_, err := readExecution(t, `[`+executionInit+`,{"type":"result","modelUsage":{"claude-haiku-5-5":{"inputTokens":10.5}}}]`)
	if err == nil || !strings.Contains(err.Error(), "the result object could not be read") {
		t.Fatalf("%v", err)
	}
}

// requestFigures is what a request counts, apart from when it ran.
func requestFigures(requests []adapter.Request) []string {
	figures := make([]string, 0, len(requests))
	for _, request := range requests {
		figures = append(figures, fmt.Sprintf("%s %s %s %+v %s", request.ID, request.Model, request.Grain, request.Tokens, request.AssumedWrite))
	}
	return figures
}

// FuzzReadExecutionFile reads any file without a panic, and proves that a
// junk element before a valid result leaves the requests unchanged.
func FuzzReadExecutionFile(f *testing.F) {
	for _, seed := range []string{`"a note"`, `42`, `null`, `[]`, `{}`, `{"type":7}`, `{"type":"user","timestamp":1793530500}`,
		`{"duration_ms":1234.5}`, `{"type":"result","modelUsage":{"claude-haiku-5-5":{"inputTokens":1.5}}}`,
		`{"type":"system","subtype":"init","session_id":"t","apiKeySource":"none"}`, executionInit, executionResult} {
		f.Add([]byte(seed))
	}
	addStreamSeeds(f)
	config := f.TempDir()
	read := func(body []byte) (adapter.Report, error) {
		return Agent{ConfigDir: config}.ReadExecutionFile(bytes.NewReader(body), recorded)
	}
	clean, err := read([]byte(`[` + executionInit + `,` + fuzzResult + `]`))
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, junk []byte) {
		_, _ = read(junk)
		if !json.Valid(junk) {
			return
		}
		body := slices.Concat([]byte(`[`+executionInit+`,`), junk, []byte(`,`+fuzzResult+`]`))
		report, err := read(body)
		if err != nil {
			t.Fatalf("a junk element refused the file: %v", err)
		}
		if got, want := requestFigures(report.Requests), requestFigures(clean.Requests); !slices.Equal(got, want) {
			t.Fatalf("requests %q, want %q", got, want)
		}
	})
}
