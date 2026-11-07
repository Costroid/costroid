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
	"github.com/Costroid/costroid/internal/decimal"
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

// budgetStopFile is the recording of a claude-code-action run with
// claude_args --max-turns 5 --max-budget-usd 0.0001. Claude Code stopped it
// with error_max_budget_usd once its own total, USD 0.0001285, passed the cap.
const budgetStopFile = "testdata/2.1.316/action-budget-stop/claude-execution-output.json"

// recordedArgs are the claude_args that recording's step was given.
const recordedArgs = "--max-turns 5 --max-budget-usd 0.0001"

func usd(text string) *decimal.Decimal {
	value := decimal.MustParse(text)
	return &value
}

func readBudgetStop(t *testing.T, stated Cap) adapter.Enforcement {
	t.Helper()
	file, err := os.Open(budgetStopFile)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	report, err := Agent{ConfigDir: t.TempDir()}.ReadCappedExecutionFile(file, recorded, stated)
	if err != nil {
		t.Fatal(err)
	}
	return report.Enforcement
}

func TestRecordedBudgetStopGradesAgentFlag(t *testing.T) {
	for _, stated := range []Cap{
		{USD: usd("0.0001")},
		{USD: usd("0.000100")},
		{USD: usd("0.0001"), Args: recordedArgs},
	} {
		got := readBudgetStop(t, stated)
		if got.Grade != adapter.GradeAgentFlag || got.Note != enforcementNote {
			t.Errorf("cap %s, claude-args %q: %+v", stated.USD, stated.Args, got)
		}
	}
	got := readBudgetStop(t, Cap{})
	if got.Grade != adapter.GradeNone || got.Note != "none: the run stopped at its own --max-budget-usd, and no cap was stated to hold it to" {
		t.Errorf("with no cap: %+v", got)
	}
}

// executionEnding is a run that ended with result, read with the stated cap.
func executionEnding(t *testing.T, result string, stated Cap) adapter.Enforcement {
	t.Helper()
	report, err := Agent{ConfigDir: t.TempDir()}.ReadCappedExecutionFile(strings.NewReader(`[`+executionInit+`,`+result+`]`), recorded, stated)
	if err != nil {
		t.Fatal(err)
	}
	return report.Enforcement
}

const (
	endedUnder  = `{"type":"result","subtype":"success","total_cost_usd":0.0420,"session_id":"s","modelUsage":{"claude-haiku-5-5":{"inputTokens":1,"outputTokens":2}}}`
	endedOver   = `{"type":"result","subtype":"success","total_cost_usd":5.25,"session_id":"s","modelUsage":{"claude-haiku-5-5":{"inputTokens":1,"outputTokens":2}}}`
	stoppedBare = `{"type":"result","subtype":"error_max_budget_usd","is_error":true,"total_cost_usd":5.25,"session_id":"s","modelUsage":{"claude-haiku-5-5":{"inputTokens":1,"outputTokens":2}}}`
)

func TestCapThatDiffersFromTheFlagGradesNone(t *testing.T) {
	stopped := readBudgetStop(t, Cap{USD: usd("5"), Args: "--max-budget-usd 5"})
	if stopped.Grade != adapter.GradeNone || stopped.Note != "none: the stated cap is USD 5 and --max-budget-usd is USD 0.0001; the cap and the flag differ" {
		t.Errorf("a stop at another cap: %+v", stopped)
	}
	under := executionEnding(t, endedUnder, Cap{USD: usd("0.5"), Args: "--max-budget-usd 1"})
	if under.Grade != adapter.GradeNone || under.Note != "none: the stated cap is USD 0.5 and --max-budget-usd is USD 1; the cap and the flag differ" {
		t.Errorf("a run under both: %+v", under)
	}
}

func TestRunUnderItsStatedFlagGradesAgentFlag(t *testing.T) {
	for _, args := range []string{
		"--max-turns 10 --max-budget-usd 5",
		"--max-budget-usd=5.00",
		"--max-budget-usd 1\n--max-budget-usd 5",
		"# --max-budget-usd 1\n--model 'claude haiku' --max-budget-usd \"5\"",
	} {
		got := executionEnding(t, endedUnder, Cap{USD: usd("5"), Args: args})
		if got.Grade != adapter.GradeAgentFlag || got.Note != enforcementNote {
			t.Errorf("claude-args %q: %+v", args, got)
		}
	}
}

func TestCapWithNothingToHoldItToGradesNone(t *testing.T) {
	for _, tc := range []struct {
		name, result, args, note string
	}{
		{"no flag stated", endedUnder, "--max-turns 10", "none: the stated cap is USD 5, and the stated claude_args hold no --max-budget-usd, so nothing shows a flag held the run"},
		{"no claude-args", endedUnder, "", "none: the stated cap is USD 5, and no claude_args were stated, so nothing shows a flag held the run"},
		{"a flag with no figure", endedUnder, "--max-budget-usd --max-turns 5", `none: --max-budget-usd reads "" in the stated claude_args, which is not a plain decimal`},
		{"a figure that is not plain", endedUnder, "--max-budget-usd 5e0", `none: --max-budget-usd reads "5e0" in the stated claude_args, which is not a plain decimal`},
		{"an unclosed quote", endedUnder, "--max-budget-usd '5", "none: the stated claude_args cannot be read: a ' quote is not closed"},
		{"a total past the cap", endedOver, "--max-budget-usd 5", "none: the run's own total of USD 5.25 passed USD 5 without a budget stop, so the flag did not hold it"},
		{"no total", executionResult, "--max-budget-usd 5", "none: the run did not stop at its budget, and its result states no total_cost_usd, so nothing shows it ended under USD 5"},
		{"a stop that names no figure", stoppedBare, "", "none: the run stopped at its budget, and neither its result nor the stated claude_args give the figure"},
	} {
		got := executionEnding(t, tc.result, Cap{USD: usd("5"), Args: tc.args})
		if got.Grade != adapter.GradeNone || got.Note != tc.note {
			t.Errorf("%s: %+v", tc.name, got)
		}
	}
	if got := executionEnding(t, stoppedBare, Cap{USD: usd("5"), Args: "--max-budget-usd 5"}); got.Grade != adapter.GradeAgentFlag {
		t.Errorf("a stop that names no figure, with the flag stated: %+v", got)
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
