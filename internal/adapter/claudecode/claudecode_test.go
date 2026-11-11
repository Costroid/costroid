package claudecode

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

const version = "2.1.316"

var recorded = time.Date(2026, 11, 1, 11, 15, 0, 0, time.UTC)

func fixture(scenario, file string) string {
	return filepath.Join("testdata", version, scenario, file)
}

// installSession lays a scenario's session files out as Claude Code does.
func installSession(t *testing.T, scenario, sessionID string) string {
	t.Helper()
	return installFrom(t, filepath.Join("testdata", version, scenario), sessionID)
}

// installFrom lays the session files of a fixture directory out as Claude
// Code does.
func installFrom(t *testing.T, directory, sessionID string) string {
	t.Helper()
	config := t.TempDir()
	project := filepath.Join(config, "projects", "-work")
	if err := os.MkdirAll(filepath.Join(project, sessionID, "subagents"), 0o755); err != nil {
		t.Fatal(err)
	}
	copyFile(t, filepath.Join(directory, "session.jsonl"), filepath.Join(project, sessionID+".jsonl"))
	agents, err := filepath.Glob(filepath.Join(directory, "subagents", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range agents {
		copyFile(t, agent, filepath.Join(project, sessionID, "subagents", filepath.Base(agent)))
	}
	return config
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func feedStream(t *testing.T, session *Session, scenario string) {
	t.Helper()
	data, err := os.ReadFile(fixture(scenario, "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if err := session.Feed([]byte(line), recorded); err != nil {
			t.Fatal(err)
		}
	}
}

func replay(t *testing.T, scenario, sessionID string) adapter.Report {
	t.Helper()
	session := Agent{ConfigDir: installSession(t, scenario, sessionID)}.NewSession(adapter.Plan{}).(*Session)
	feedStream(t, session, scenario)
	report, err := session.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func sum(requests []adapter.Request) adapter.Tokens {
	var total adapter.Tokens
	for _, request := range requests {
		total = total.Add(request.Tokens)
	}
	return total
}

type providerTotals struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
	CacheRead    int64 `json:"cacheReadInputTokens"`
	CacheCreated int64 `json:"cacheCreationInputTokens"`
	FiveMinute   int64 `json:"cacheCreationEphemeral5mInputTokens"`
	OneHour      int64 `json:"cacheCreationEphemeral1hInputTokens"`
	Thinking     int64 `json:"thinkingTokens"`
}

// resultTotals reads the last result object without using the adapter.
func resultTotals(t *testing.T, scenario string) providerTotals {
	t.Helper()
	data, err := os.ReadFile(fixture(scenario, "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var last providerTotals
	found := false
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var result struct {
			Type       string                    `json:"type"`
			ModelUsage map[string]providerTotals `json:"modelUsage"`
		}
		if err := json.Unmarshal([]byte(line), &result); err != nil {
			t.Fatal(err)
		}
		if result.Type == "result" {
			last, found = result.ModelUsage["claude-haiku-5-5"], true
		}
	}
	if !found {
		t.Fatal("no result object in the fixture")
	}
	return last
}

func TestTokenCountsEqualTheProviderTotalsInTheResultObject(t *testing.T) {
	scenarios := map[string]string{
		"one-request": "00000000-0000-4000-8000-000000000001",
		"subagent":    "00000000-0000-4000-8000-000000000002",
		"budget-stop": "00000000-0000-4000-8000-000000000003",
	}
	for scenario, sessionID := range scenarios {
		t.Run(scenario, func(t *testing.T) {
			got := sum(replay(t, scenario, sessionID).Requests)
			want := resultTotals(t, scenario)
			if got.InputUncached != want.InputTokens {
				t.Errorf("input %d, result says %d", got.InputUncached, want.InputTokens)
			}
			if got.Output != want.OutputTokens {
				t.Errorf("output %d, result says %d", got.Output, want.OutputTokens)
			}
			if got.CacheRead != want.CacheRead {
				t.Errorf("cache read %d, result says %d", got.CacheRead, want.CacheRead)
			}
			if got.CacheWrite5m != want.FiveMinute || got.CacheWrite1h != want.OneHour {
				t.Errorf("cache writes %d and %d, result says %d and %d", got.CacheWrite5m, got.CacheWrite1h, want.FiveMinute, want.OneHour)
			}
			if got.CacheWrite5m+got.CacheWrite1h != want.CacheCreated {
				t.Errorf("cache writes add to %d, result says %d", got.CacheWrite5m+got.CacheWrite1h, want.CacheCreated)
			}
			if got.ReasoningOutput != want.Thinking {
				t.Errorf("thinking %d, result says %d", got.ReasoningOutput, want.Thinking)
			}
		})
	}
}

func TestSubagentRunMapsEachClassFromHandReadFigures(t *testing.T) {
	report := replay(t, "subagent", "00000000-0000-4000-8000-000000000002")
	want := adapter.Tokens{
		InputUncached:       8,
		CacheRead:           15605,
		CacheWrite5m:        3495,
		CacheWrite1h:        4450,
		Output:              272,
		ReasoningOutput:     121,
		CachedInputReported: 15605,
	}
	if got := sum(report.Requests); got != want {
		t.Errorf("got %+v", got)
	}
	if len(report.Requests) != 4 {
		t.Errorf("%d requests, want 4: three in the session and one in the subagent", len(report.Requests))
	}
}

func TestInputExcludesCacheReadsAndWrites(t *testing.T) {
	one := replay(t, "one-request", "00000000-0000-4000-8000-000000000001").Requests[0]
	if one.Tokens.InputUncached != 2 || one.Tokens.CacheRead != 649 || one.Tokens.CacheWrite1h != 2986 {
		t.Errorf("%+v", one.Tokens)
	}
	if one.ContextInputTokens != 3637 {
		t.Errorf("context %d, want the whole input of 2 + 649 + 2986", one.ContextInputTokens)
	}
}

func TestReasoningIsInformationalAndNeverAddedToOutput(t *testing.T) {
	report := replay(t, "subagent", "00000000-0000-4000-8000-000000000002")
	last := report.Requests[len(report.Requests)-1].Tokens
	if last.Output != 124 || last.ReasoningOutput != 121 {
		t.Errorf("%+v", last)
	}
}

func TestRepeatedRequestIDCountsOnce(t *testing.T) {
	data, err := os.ReadFile(fixture("subagent", "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), `"requestId":"req_fixture_0005"`); got != 2 {
		t.Fatalf("the fixture repeats the request %d times, want 2", got)
	}
	report := replay(t, "subagent", "00000000-0000-4000-8000-000000000002")
	ids := []string{}
	for _, request := range report.Requests {
		ids = append(ids, request.ID)
	}
	slices.Sort(ids)
	if len(slices.Compact(ids)) != 4 || sum(report.Requests).InputUncached != 8 {
		t.Errorf("ids %v, total %+v", ids, sum(report.Requests))
	}
}

func TestResultUsageIsNotTheRunTotal(t *testing.T) {
	data, err := os.ReadFile(fixture("subagent", "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), `"type":"result"`); got != 2 {
		t.Fatalf("the fixture holds %d results, want 2", got)
	}
	session := &Session{}
	feedStream(t, session, "subagent")
	report, err := session.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if got := sum(report.Requests); got.Output != 272 {
		t.Errorf("output %d: results were added or the first was kept", got.Output)
	}
}

func TestMissingSessionFilesFallToOneRowSetPerModel(t *testing.T) {
	session := Agent{ConfigDir: t.TempDir()}.NewSession(adapter.Plan{}).(*Session)
	feedStream(t, session, "subagent")
	report, err := session.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if report.CostSource != SourceStream || len(report.Requests) != 1 || report.Requests[0].Grain != adapter.PerRun {
		t.Fatalf("%+v", report)
	}
	if len(report.Warnings) == 0 {
		t.Error("the fall to run grain carried no warning")
	}
	if got := report.Requests[0].Tokens; got.Output != 272 || got.CacheWrite5m != 3495 || got.CacheWrite1h != 4450 {
		t.Errorf("%+v", got)
	}
}

func TestSessionFilesThatDisagreeWithTheResultAreNotUsed(t *testing.T) {
	sessionID := "00000000-0000-4000-8000-000000000002"
	config := installSession(t, "subagent", sessionID)
	path := filepath.Join(config, "projects", "-work", sessionID, "subagents", "agent-1.jsonl")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	session := Agent{ConfigDir: config}.NewSession(adapter.Plan{}).(*Session)
	feedStream(t, session, "subagent")
	report, err := session.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if report.CostSource != SourceStream || sum(report.Requests).Output != 272 || len(report.Warnings) == 0 {
		t.Errorf("%+v", report)
	}
}

func TestBudgetStopZeroesResultUsageAndRowsStillCarryTheSpend(t *testing.T) {
	data, err := os.ReadFile(fixture("budget-stop", "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"subtype":"error_max_budget_usd"`) {
		t.Fatal("the fixture is not a budget stop")
	}
	if !strings.Contains(string(data), `"usage":{"output_tokens_details":{"thinking_tokens":0},"input_tokens":0`) {
		t.Fatal("the fixture's result usage is not zero")
	}
	got := sum(replay(t, "budget-stop", "00000000-0000-4000-8000-000000000003").Requests)
	if got.Output != 107 || got.CacheWrite1h != 3279 {
		t.Errorf("%+v", got)
	}
}

func TestAssistantStreamOutputIsAPlaceholderButTheSessionFileIsFinal(t *testing.T) {
	data, err := os.ReadFile(fixture("subagent", "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"output_tokens":16,`) {
		t.Fatal("the stream no longer shows the placeholder")
	}
	first := replay(t, "subagent", "00000000-0000-4000-8000-000000000002").Requests[0]
	if first.Tokens.Output != 84 {
		t.Errorf("output %d, want the final 84", first.Tokens.Output)
	}
}

func TestSessionOutputEqualsTheOTelEventAndTheFinalMessageDelta(t *testing.T) {
	data, err := os.ReadFile(fixture("subagent", "otel-api-requests.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	type event struct {
		RequestID  string `json:"request_id"`
		Input      int64  `json:"input_tokens"`
		Output     int64  `json:"output_tokens"`
		CacheRead  int64  `json:"cache_read_tokens"`
		FiveMinute int64  `json:"cache_creation_5m_tokens"`
		OneHour    int64  `json:"cache_creation_1h_tokens"`
	}
	events := map[string]event{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var e event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		events[e.RequestID] = e
	}
	report := replay(t, "subagent", "00000000-0000-4000-8000-000000000002")
	for _, request := range report.Requests {
		e, ok := events[request.ID]
		if !ok {
			t.Fatalf("no OTel event for %s", request.ID)
		}
		got := event{request.ID, request.Tokens.InputUncached, request.Tokens.Output, request.Tokens.CacheRead, request.Tokens.CacheWrite5m, request.Tokens.CacheWrite1h}
		if got != e {
			t.Errorf("session %+v, OTel %+v", got, e)
		}
	}
	stream, err := os.ReadFile(fixture("subagent", "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(stream)), "\n") {
		var parsed struct {
			Event struct {
				Type  string `json:"type"`
				Usage struct {
					Output int64 `json:"output_tokens"`
				} `json:"usage"`
			} `json:"event"`
		}
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			t.Fatal(err)
		}
		if parsed.Event.Type == "message_delta" && !slices.Contains([]int64{84, 60, 124}, parsed.Event.Usage.Output) {
			t.Errorf("message_delta output %d is no session figure", parsed.Event.Usage.Output)
		}
	}
}

func TestRequestTimesComeFromTheSessionFile(t *testing.T) {
	first := replay(t, "subagent", "00000000-0000-4000-8000-000000000002").Requests[0]
	if first.Start.Format(time.RFC3339Nano) != "2026-11-01T11:15:12.814Z" || first.End.Format(time.RFC3339Nano) != "2026-11-01T11:15:14.363Z" {
		t.Errorf("%s to %s", first.Start, first.End)
	}
}

func TestSubscriptionRunIsListEquivalent(t *testing.T) {
	report := replay(t, "one-request", "00000000-0000-4000-8000-000000000001")
	if report.CostBasis != focus.ListEquivalent {
		t.Errorf("basis %s", report.CostBasis)
	}
}

func TestAPIKeyRunIsMeasured(t *testing.T) {
	session := &Session{}
	line := `{"type":"system","subtype":"init","session_id":"s","apiKeySource":"ANTHROPIC_API_KEY"}`
	if err := session.Feed([]byte(line), recorded); err != nil {
		t.Fatal(err)
	}
	if session.costBasis() != focus.Measured {
		t.Error("an API key run did not read as measured")
	}
}

func TestUnreportedCacheLifetimeIsAssignedByRuleAndFlagged(t *testing.T) {
	b := newRequestBuilder(focus.CacheWrite1h)
	tokens, assumed, err := b.tokens(usage{CacheCreated: 10, CacheCreation: &cacheCreation{}})
	if err != nil || tokens.CacheWrite1h != 10 || assumed != focus.CacheWrite1h {
		t.Errorf("subscription: %+v %v %v", tokens, assumed, err)
	}
	b = newRequestBuilder(focus.CacheWrite5m)
	tokens, assumed, err = b.tokens(usage{CacheCreated: 10})
	if err != nil || tokens.CacheWrite5m != 10 || assumed != focus.CacheWrite5m {
		t.Errorf("api key: %+v %v %v", tokens, assumed, err)
	}
}

func TestPlanAddsTheBudgetAndTheStream(t *testing.T) {
	plan, err := Agent{}.Plan([]string{"claude", "-p", "hello"}, decimal.MustParse("5"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "--output-format", "stream-json", "--verbose", "--max-budget-usd", "5", "-p", "hello"}
	if !slices.Equal(plan.Argv, want) {
		t.Errorf("argv %v", plan.Argv)
	}
	if plan.Enforcement.Grade != adapter.GradeAgentFlag || plan.Enforcement.Watchdog {
		t.Errorf("enforcement %+v", plan.Enforcement)
	}
	if !strings.Contains(plan.Enforcement.Note, "can pass the cap") {
		t.Errorf("note %q", plan.Enforcement.Note)
	}
}

func TestPlanRefusesACommandTheGradeWouldOverstate(t *testing.T) {
	commands := [][]string{
		{"claude", "hello"},
		{"claude", "-p", "--max-budget-usd", "9", "hello"},
		{"claude", "-p", "--max-budget-usd=9", "hello"},
		{"claude", "-p", "--output-format", "text", "hello"},
		{"claude", "-p", "--output-format=json", "hello"},
	}
	for _, argv := range commands {
		if _, err := (Agent{}).Plan(argv, decimal.MustParse("5")); err == nil {
			t.Errorf("%v was accepted", argv)
		}
	}
}

func TestStreamWithNoResultAndNoSessionIsAnErrorNotZeroUsage(t *testing.T) {
	session := Agent{ConfigDir: t.TempDir()}.NewSession(adapter.Plan{})
	if _, err := session.Finish(); err == nil {
		t.Error("a run with no usage record reported nothing")
	}
}

// TestFixturesCarryNoPathsNamesOrProviderIDs scans every fixture. A
// rate_limit_event message keeps its type and no other rate-limit data.
func TestFixturesCarryNoPathsNamesOrProviderIDs(t *testing.T) {
	forbidden := regexp.MustCompile(`/home/|/tmp/|/Users/|req_0|msg_0|toolu_0|sk-ant|organizationUuid|accountUuid|rate_limit`)
	err := filepath.WalkDir("testdata", func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		data = bytes.ReplaceAll(data, []byte(`"rate_limit_event"`), nil)
		if match := forbidden.Find(data); match != nil {
			t.Errorf("%s carries %q", path, match)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestAMessageThatIsAStringIsRead feeds lines whose message is a plain string,
// as some stream lines carry, beside an assistant line whose message names its
// model. None of them may stop the run's reading.
func TestAMessageThatIsAStringIsRead(t *testing.T) {
	session := &Session{}
	for _, line := range []string{
		`{"type":"system","subtype":"init","session_id":"s","message":"a note from the runner"}`,
		`{"type":"user","message":"plain text"}`,
		`{"type":"assistant","message":"plain text"}`,
		`{"type":"assistant","message":{"model":"claude-opus-5-5"}}`,
	} {
		if err := session.Feed([]byte(line), recorded); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
	}
	if !session.answered["claude-opus-5-5"] {
		t.Error("the assistant line's model was not read")
	}
}

func synthetic(format, file string) string {
	return filepath.Join("testdata", "synthetic", format, file)
}

// feedLines reads a stream file into a new session whose config directory
// holds no session file, and finishes it.
func feedLines(t *testing.T, path string) (adapter.Report, error) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	session := Agent{ConfigDir: t.TempDir()}.NewSession(adapter.Plan{})
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if err := session.Feed([]byte(line), recorded); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
	}
	return session.Finish()
}

const initLine = `{"type":"system","subtype":"init","session_id":"s","apiKeySource":"ANTHROPIC_API_KEY","claude_code_version":"2.1.316"}`

// finishResult feeds an init line and one result line whose haiku totals are
// the given JSON object, and finishes.
func finishResult(t *testing.T, haiku string) (adapter.Report, error) {
	t.Helper()
	session := &Session{}
	for _, line := range []string{initLine, `{"type":"result","subtype":"success","session_id":"s","modelUsage":{"claude-haiku-5-5":` + haiku + `}}`} {
		if err := session.Feed([]byte(line), recorded); err != nil {
			t.Fatal(err)
		}
	}
	return session.Finish()
}

func TestResultCountsAcceptIntegralForms(t *testing.T) {
	for _, form := range []string{`10`, `10.0`, `"10"`, `"10.0"`, `1e1`, `100e-1`, `1.0E+1`} {
		report, err := finishResult(t, `{"inputTokens":`+form+`,"outputTokens":2}`)
		if err != nil {
			t.Errorf("%s: %v", form, err)
			continue
		}
		if got := report.Requests[0].Tokens.InputUncached; got != 10 {
			t.Errorf("%s read as %d", form, got)
		}
	}
	refused := map[string]string{
		`10.5`:                    "is not a whole number",
		`"10.5"`:                  "is not a whole number",
		`1e-1`:                    "is not a whole number",
		`-1`:                      "is negative",
		`"-10"`:                   "is negative",
		`"ten"`:                   "is not a number",
		`" 10"`:                   "is not a number",
		`true`:                    "is not a number",
		`{"n":10}`:                "is not a number",
		`9223372036854775808`:     "is past the largest count",
		`1e19`:                    "is past the largest count",
		`1e99999999999`:           "is past the largest count",
		`92233720368547758080e-1`: "is past the largest count",
	}
	for form, reason := range refused {
		_, err := finishResult(t, `{"inputTokens":`+form+`,"outputTokens":2}`)
		if err == nil || !strings.Contains(err.Error(), "the result object could not be read: modelUsage claude-haiku-5-5 inputTokens: ") || !strings.Contains(err.Error(), reason) {
			t.Errorf("%s: %v", form, err)
		}
		if err != nil && strings.Contains(err.Error(), "no result object") {
			t.Errorf("%s: an unread result was called absent: %v", form, err)
		}
	}
}

func TestResultCountsTakeTheLargestWholeCount(t *testing.T) {
	report, err := finishResult(t, `{"outputTokens":9223372036854775807,"thinkingTokens":"9.223372036854775807e18"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Requests[0].Tokens; got.Output != 1<<63-1 || got.ReasoningOutput != 1<<63-1 {
		t.Errorf("%+v", got)
	}
}

func TestResultWithUnknownFieldsAndNullTotalsIsRead(t *testing.T) {
	report, err := feedLines(t, synthetic("stream", "every-shape.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	want := adapter.Tokens{InputUncached: 3, CacheRead: 100, CacheWrite5m: 40, CacheWrite1h: 60, Output: 20, CachedInputReported: 100}
	if len(report.Requests) != 1 || report.Requests[0].Tokens != want {
		t.Fatalf("%+v", report.Requests)
	}
	session := &Session{}
	for _, line := range []string{initLine, `{"type":"result","subtype":"success","modelUsage":null}`} {
		if err := session.Feed([]byte(line), recorded); err != nil {
			t.Fatal(err)
		}
	}
	if report, err := session.Finish(); err != nil || len(report.Requests) != 0 {
		t.Errorf("a null modelUsage: %+v, %v", report, err)
	}
}

func TestOddStreamLinesAreSkippedAndCountedInOneWarning(t *testing.T) {
	report, err := feedLines(t, synthetic("stream", "every-shape.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if report.SessionID != "00000000-0000-4000-8000-00000000000a" || report.CostBasis != focus.Measured {
		t.Errorf("the init line was not read, or a later odd init replaced it: %s %s", report.SessionID, report.CostBasis)
	}
	var skipped []string
	for _, warning := range report.Warnings {
		if strings.Contains(warning, "skipped") {
			skipped = append(skipped, warning)
		}
	}
	if len(skipped) != 1 || !strings.HasPrefix(skipped[0], "6 stream lines were skipped") {
		t.Errorf("warnings %q", report.Warnings)
	}
}

func TestAResultThatCannotBeReadIsNamedNotCalledAbsent(t *testing.T) {
	session := Agent{ConfigDir: t.TempDir()}.NewSession(adapter.Plan{})
	for _, line := range []string{initLine, `{"type":"result","session_id":"s","modelUsage":"SYNTHETIC-TEXT"}`} {
		if err := session.Feed([]byte(line), recorded); err != nil {
			t.Fatal(err)
		}
	}
	_, err := session.Finish()
	if err == nil || err.Error() != "claude-code: the result object could not be read: modelUsage is not an object of models" {
		t.Fatalf("%v", err)
	}
}

func TestAResultThatReadsReplacesAnEarlierOneThatDidNot(t *testing.T) {
	report, err := finishResult(t, `{"inputTokens":4}`)
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{}
	for _, line := range []string{
		initLine,
		`{"type":"result","session_id":"s","modelUsage":{"claude-haiku-5-5":{"inputTokens":1.5}}}`,
		`{"type":"result","session_id":"s","modelUsage":{"claude-haiku-5-5":{"inputTokens":4}}}`,
	} {
		if err := session.Feed([]byte(line), recorded); err != nil {
			t.Fatal(err)
		}
	}
	later, err := session.Finish()
	if err != nil || later.Requests[0].Tokens != report.Requests[0].Tokens {
		t.Fatalf("%+v, %v", later.Requests, err)
	}
	session = &Session{}
	for _, line := range []string{
		initLine,
		`{"type":"result","session_id":"s","modelUsage":{"claude-haiku-5-5":{"inputTokens":4}}}`,
		`{"type":"result","session_id":"s","modelUsage":{"claude-haiku-5-5":{"inputTokens":1.5}}}`,
	} {
		if err := session.Feed([]byte(line), recorded); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := session.Finish(); err == nil {
		t.Error("a last result that could not be read was passed over for an earlier one")
	}
}

func TestResultUsageWithoutModelUsageFails(t *testing.T) {
	report, err := feedLines(t, synthetic("stream", "usage-without-model-usage.jsonl"))
	want := "claude-code: the result states usage but no per-model totals; Claude Code 1.0.0 is not read"
	if err == nil || err.Error() != want {
		t.Fatalf("%v, %+v", err, report)
	}
	session := &Session{}
	if err := session.Feed([]byte(`{"type":"result","usage":{"output_tokens":"7"},"modelUsage":{}}`), recorded); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Finish(); err == nil || !strings.Contains(err.Error(), "Claude Code of an unstated version is not read") {
		t.Errorf("%v", err)
	}
	session = &Session{}
	if err := session.Feed([]byte(`{"type":"result","usage":"SYNTHETIC-TEXT"}`), recorded); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Finish(); err == nil {
		t.Error("a usage that cannot be read was taken for none")
	}
}

func TestResultWithZeroUsageAndNoModelUsageReadsAsNoUsage(t *testing.T) {
	report, err := feedLines(t, synthetic("stream", "auth-failure.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Requests) != 0 {
		t.Errorf("%+v", report.Requests)
	}
}

func TestAgentErrorClassFromFields(t *testing.T) {
	report, err := feedLines(t, synthetic("stream", "auth-failure.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if report.AgentError != adapter.AgentErrorAuthentication {
		t.Errorf("the fixture read as %q", report.AgentError)
	}
	assistant := func(class string) string {
		return `{"type":"assistant","message":{"model":"claude-haiku-5-5"},"error":` + class + `}`
	}
	ended := `{"type":"result","subtype":"success","is_error":true,"result":"authentication_failed","modelUsage":{}}`
	succeeded := `{"type":"result","subtype":"success","is_error":false,"result":"rate_limit","modelUsage":{}}`
	cases := []struct {
		name  string
		lines []string
		want  adapter.AgentError
	}{
		{"authentication", []string{assistant(`"authentication_failed"`), ended}, adapter.AgentErrorAuthentication},
		{"billing", []string{assistant(`"billing_error"`), ended}, adapter.AgentErrorBilling},
		{"rate limit", []string{assistant(`"rate_limit"`), ended}, adapter.AgentErrorRateLimit},
		{"invalid request", []string{assistant(`"invalid_request"`), ended}, adapter.AgentErrorInvalidRequest},
		{"server", []string{assistant(`"server_error"`), ended}, adapter.AgentErrorServer},
		{"a class not on the list", []string{assistant(`"overloaded"`), ended}, adapter.AgentErrorUnknown},
		{"a class that is not text", []string{assistant(`{"type":"server_error"}`), ended}, adapter.AgentErrorUnknown},
		{"the last stated class", []string{assistant(`"rate_limit"`), assistant(`"billing_error"`), ended}, adapter.AgentErrorBilling},
		{"an error result with no class", []string{ended}, adapter.AgentErrorUnknown},
		{"a class the run recovered from", []string{assistant(`"rate_limit"`), succeeded}, ""},
		{"a null class", []string{assistant(`null`), ended}, adapter.AgentErrorUnknown},
		{"a budget stop", []string{`{"type":"result","subtype":"error_max_budget_usd","is_error":true,"modelUsage":{}}`}, ""},
		{"a turn limit", []string{`{"type":"result","subtype":"error_max_turns","is_error":true,"modelUsage":{}}`}, ""},
		{"an execution error", []string{`{"type":"result","subtype":"error_during_execution","is_error":true,"modelUsage":{}}`}, adapter.AgentErrorUnknown},
		{"text alone", []string{`{"type":"assistant","message":{"content":[{"type":"text","text":"authentication_failed"}]}}`, succeeded}, ""},
		{"a stated class and no result", []string{assistant(`"server_error"`)}, adapter.AgentErrorServer},
	}
	for _, tc := range cases {
		session := &Session{}
		for _, line := range append([]string{initLine}, tc.lines...) {
			if err := session.Feed([]byte(line), recorded); err != nil {
				t.Fatal(err)
			}
		}
		report, _ := session.Finish()
		if report.AgentError != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, report.AgentError, tc.want)
		}
	}
}

func TestRecordedRunsEndInNoAgentError(t *testing.T) {
	scenarios := map[string]string{
		"one-request": "00000000-0000-4000-8000-000000000001",
		"subagent":    "00000000-0000-4000-8000-000000000002",
		"budget-stop": "00000000-0000-4000-8000-000000000003",
	}
	for scenario, sessionID := range scenarios {
		if class := replay(t, scenario, sessionID).AgentError; class != "" {
			t.Errorf("%s: %q", scenario, class)
		}
	}
}

func TestTokensCheckRefusesNegative(t *testing.T) {
	if err := (adapter.Tokens{InputUncached: 1, Output: 2}).Check(); err != nil {
		t.Errorf("a whole count was refused: %v", err)
	}
	classes := map[string]adapter.Tokens{
		"input_uncached":        {InputUncached: -1},
		"cache_read":            {CacheRead: -1},
		"cache_write_5m":        {CacheWrite5m: -1},
		"cache_write_1h":        {CacheWrite1h: -1},
		"cache_write_30m":       {CacheWrite30m: -1},
		"output":                {Output: 5, ReasoningOutput: -1},
		"cached_input_reported": {CachedInputReported: -1},
	}
	for name, tokens := range classes {
		if err := tokens.Check(); err == nil || !strings.Contains(err.Error(), "below zero") {
			t.Errorf("%s: %v", name, err)
		}
	}
	_, err := finishResult(t, `{"inputTokens":1,"cacheCreationInputTokens":100,"cacheCreationEphemeral1hInputTokens":150}`)
	if err == nil || !strings.Contains(err.Error(), "the result's totals for claude-haiku-5-5 do not add up") || !strings.Contains(err.Error(), "pass the written total of 100") {
		t.Errorf("lifetimes past the written total: %v", err)
	}
}

// fuzzResult is a result whose cache writes all state their lifetime, so its
// rows do not depend on how the run bills.
const fuzzResult = `{"type":"result","subtype":"success","is_error":false,"session_id":"fuzz","modelUsage":{"claude-haiku-5-5":{"inputTokens":3,"outputTokens":20,"cacheReadInputTokens":100,"cacheCreationInputTokens":100,"cacheCreationEphemeral5mInputTokens":40,"cacheCreationEphemeral1hInputTokens":60}}}`

var fuzzWant = adapter.Tokens{InputUncached: 3, CacheRead: 100, CacheWrite5m: 40, CacheWrite1h: 60, Output: 20, CachedInputReported: 100}

func addStreamSeeds(f *testing.F) {
	paths, err := filepath.Glob(filepath.Join("testdata", "*", "*", "*.jsonl"))
	if err != nil {
		f.Fatal(err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			f.Add([]byte(line))
		}
	}
}

// FuzzClaudeFeed feeds any lines. No line may panic the reader, a finished
// run never holds a negative class, and a valid result after the lines still
// sets the run's totals.
func FuzzClaudeFeed(f *testing.F) {
	addStreamSeeds(f)
	f.Add([]byte(`{"type":"result","modelUsage":{"claude-haiku-5-5":{"inputTokens":1.5}}}`))
	f.Add([]byte(`{"type":"result","usage":{"input_tokens":5}}`))
	f.Add([]byte(`{"type":"result","modelUsage":{"claude-haiku-5-5":{"cacheCreationInputTokens":1,"cacheCreationEphemeral5mInputTokens":5}}}`))
	f.Fuzz(func(t *testing.T, garbage []byte) {
		lines := bytes.Split(garbage, []byte("\n"))
		alone := &Session{}
		for _, line := range lines {
			_ = alone.Feed(line, recorded)
		}
		if report, err := alone.Finish(); err == nil {
			for _, request := range report.Requests {
				if err := request.Tokens.Check(); err != nil {
					t.Fatalf("%s: %v", request.ID, err)
				}
			}
		}
		session := &Session{}
		_ = session.Feed([]byte(initLine), recorded)
		for _, line := range lines {
			_ = session.Feed(line, recorded)
		}
		if err := session.Feed([]byte(fuzzResult), recorded); err != nil {
			t.Fatal(err)
		}
		report, err := session.Finish()
		if err != nil {
			t.Fatalf("a valid last result was refused: %v", err)
		}
		if len(report.Requests) != 1 || report.Requests[0].ID != "fuzz:claude-haiku-5-5" || report.Requests[0].Tokens != fuzzWant {
			t.Fatalf("%+v", report.Requests)
		}
	})
}

const oneRequestID = "00000000-0000-4000-8000-000000000001"

// replayWith replays a recorded scenario over its session files, with the
// main file's text changed by edit and only the stream lines keep allows.
func replayWith(t *testing.T, scenario, sessionID string, edit func(string) string, keep func(string) bool) (adapter.Report, error) {
	t.Helper()
	config := installSession(t, scenario, sessionID)
	main := filepath.Join(config, "projects", "-work", sessionID+".jsonl")
	data, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(main, []byte(edit(string(data))), 0o644); err != nil {
		t.Fatal(err)
	}
	stream, err := os.ReadFile(fixture(scenario, "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	session := Agent{ConfigDir: config}.NewSession(adapter.Plan{})
	for _, line := range strings.Split(strings.TrimSpace(string(stream)), "\n") {
		if !keep(line) {
			continue
		}
		if err := session.Feed([]byte(line), recorded); err != nil {
			t.Fatal(err)
		}
	}
	return session.Finish()
}

func everyLine(string) bool { return true }

// noResult keeps every stream line but the results, as a run stopped before
// its end streams.
func noResult(line string) bool { return !strings.Contains(line, `"type":"result"`) }

// insertAfterFirstLine puts lines after a file's first line.
func insertAfterFirstLine(lines ...string) func(string) string {
	return func(text string) string {
		first, rest, _ := strings.Cut(text, "\n")
		return first + "\n" + strings.Join(lines, "\n") + "\n" + rest
	}
}

// passedOverTypes are the session-file line types the reader passes over,
// each seen in a recording or in Claude Code's own files.
var passedOverTypes = []string{
	"ai-title", "api-request-blob", "api-request-shape", "atis-latch", "attachment", "cost-state",
	"last-prompt", "message", "queue-operation", "summary", "tools-used", "user",
}

// recordedLineTypes are the line types in every recorded session file.
func recordedLineTypes(t *testing.T) map[string]bool {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "*", "*", "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]bool{}
	for _, path := range paths {
		if strings.Contains(path, "synthetic") || strings.Contains(path, "not-recorded") {
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(string(mustReadFile(t, path))), "\n") {
			var typed struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal([]byte(line), &typed); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			types[typed.Type] = true
		}
	}
	return types
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSessionFileSkipsUnreadLineTypes(t *testing.T) {
	for kind := range recordedLineTypes(t) {
		if kind != "api-request" && kind != "assistant" && !slices.Contains(passedOverTypes, kind) {
			t.Errorf("a recording holds the line type %q, which no case below passes over", kind)
		}
	}
	want := replay(t, "one-request", oneRequestID)
	shapes := []string{`{"role":"user","content":[{"type":"text"}]}`, `"SYNTHETIC-TEXT"`, `null`, `7`, `[1]`}
	for _, kind := range passedOverTypes {
		var lines []string
		for _, shape := range shapes {
			lines = append(lines,
				`{"type":"`+kind+`","sessionId":"`+oneRequestID+`","message":`+shape+`}`,
				`{"type":"`+kind+`","timestamp":12,"isSidechain":"no","message":`+shape+`}`)
		}
		lines = append(lines, `{"type":"`+kind+`","message":{"model":"claude-haiku-5-5","usage":{"input_tokens":-1}},"requestId":"req_synthetic_0009"}`)
		got, err := replayWith(t, "one-request", oneRequestID, insertAfterFirstLine(lines...), everyLine)
		if err != nil {
			t.Errorf("%s: %v", kind, err)
			continue
		}
		if got.CostSource != SourceSessionFile || !slices.Equal(got.Requests, want.Requests) || len(got.Warnings) != 0 {
			t.Errorf("%s: %s %+v, warnings %q", kind, got.CostSource, got.Requests, got.Warnings)
		}
	}
	odd := []string{`[1,2]`, `"SYNTHETIC-TEXT"`, `{"type":7,"message":{"usage":{"input_tokens":1}}}`, `{}`, `null`, `{"type":"api-request","id":"00000000-0000-4000-9000-0000000000e9"}`}
	got, err := replayWith(t, "one-request", oneRequestID, insertAfterFirstLine(odd...), everyLine)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Requests, want.Requests) || !slices.Equal(got.Warnings, []string{"6 session file lines were skipped: their shape is not one this reader knows"}) {
		t.Errorf("%+v, warnings %q", got.Requests, got.Warnings)
	}
}

func TestEveryShapeSessionFileIsRead(t *testing.T) {
	builder := newRequestBuilder(focus.CacheWrite5m)
	facts, err := builder.readData(mustReadFile(t, synthetic("session", "every-shape.jsonl")))
	if err != nil {
		t.Fatal(err)
	}
	if facts.fileCounts != (fileCounts{skipped: 6, untimed: 1, inFlight: 1}) {
		t.Errorf("counts %+v", facts.fileCounts)
	}
	if facts.cwd != "/work" || facts.version != "2.1.319" || !facts.sessions["00000000-0000-4000-8000-0000000000e1"] || facts.sidechain {
		t.Errorf("facts %+v", facts)
	}
	at := func(seconds int) time.Time { return time.Date(2026, 11, 7, 10, 0, seconds, 0, time.UTC) }
	requests := builder.requests(time.Time{}, time.Time{})
	want := []struct {
		id         string
		start, end time.Time
		tokens     adapter.Tokens
	}{
		{"req_synthetic_0001", at(1), at(3), adapter.Tokens{InputUncached: 2, CacheRead: 100, CacheWrite5m: 40, Output: 20, ReasoningOutput: 5, CachedInputReported: 100}},
		{"req_synthetic_0002", at(3), at(3), adapter.Tokens{InputUncached: 1, Output: 5}},
	}
	if len(requests) != len(want) {
		t.Fatalf("%+v", requests)
	}
	for i, request := range requests {
		w := want[i]
		if request.ID != w.id || !request.Start.Equal(w.start) || !request.End.Equal(w.end) || request.Tokens != w.tokens || request.ServiceTier != "standard" || request.Region != "global" {
			t.Errorf("%+v, want %+v", request, w)
		}
	}
}

// cutLastLine leaves a file's last line half-written, with no line break.
func cutLastLine(text string) string {
	text = strings.TrimSuffix(text, "\n")
	cut := strings.LastIndex(text, "\n") + 1
	return text[:cut+(len(text)-cut)/2]
}

// dropLastLine leaves a file without its last line.
func dropLastLine(text string) string {
	text = strings.TrimSuffix(text, "\n")
	return text[:strings.LastIndex(text, "\n")+1]
}

func TestHalfWrittenLastLineIsInFlight(t *testing.T) {
	inFlight := "the session file ends inside a line, which is the request in flight: no result counts it, so its tokens reach no row"
	halfAssistant := func(text string) string {
		line := strings.Split(text, "\n")[1]
		line = strings.ReplaceAll(line, "req_fixture_0001", "req_fixture_0099")
		return text + line[:len(line)/2]
	}
	want := replay(t, "one-request", oneRequestID)
	stopped, err := replayWith(t, "one-request", oneRequestID, halfAssistant, noResult)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Unrecorded != 1 || !slices.Equal(stopped.Requests, want.Requests) || !slices.Contains(stopped.Warnings, inFlight) {
		t.Errorf("no result: unrecorded %d, %+v, warnings %q", stopped.Unrecorded, stopped.Requests, stopped.Warnings)
	}
	finished, err := replayWith(t, "one-request", oneRequestID, halfAssistant, everyLine)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Unrecorded != 0 || !slices.Equal(finished.Requests, want.Requests) || len(finished.Warnings) != 0 {
		t.Errorf("with a result: unrecorded %d, %+v, warnings %q", finished.Unrecorded, finished.Requests, finished.Warnings)
	}
	builder := newRequestBuilder(focus.CacheWrite5m)
	text := string(mustReadFile(t, fixture("one-request", "session.jsonl")))
	facts, err := builder.readData([]byte(halfAssistant(text) + "\n"))
	if err != nil || facts.inFlight != 0 || facts.skipped != 1 {
		t.Errorf("a cut line followed by a line break: %+v, %v", facts.fileCounts, err)
	}
	whole := strings.Split(text, "\n")[1]
	builder = newRequestBuilder(focus.CacheWrite5m)
	facts, err = builder.readData([]byte(text + strings.ReplaceAll(whole, "req_fixture_0001", "req_fixture_0099")))
	if err != nil || facts.inFlight != 0 || len(builder.requests(time.Time{}, time.Time{})) != 2 {
		t.Errorf("a whole last line with no line break: %+v, %v", facts.fileCounts, err)
	}
	onlyInFlight := func(string) string {
		return `{"type":"assistant","requestId":"req_fixture_0099","message":{"usage":{"inp`
	}
	alone, err := replayWith(t, "one-request", oneRequestID, onlyInFlight, noResult)
	if err != nil || alone.Unrecorded != 1 || len(alone.Requests) != 0 || !slices.Equal(alone.Warnings, []string{inFlight}) {
		t.Errorf("a file whose only line is in flight: %+v, %v", alone, err)
	}
}

// TestStoppedRunKeepsSessionRows stops the recorded subagent run inside its
// last response: no result streamed, and the main file's last line is cut.
// The rows the files hold are kept, and the request in flight is counted as
// unrecorded.
func TestStoppedRunKeepsSessionRows(t *testing.T) {
	const sessionID = "00000000-0000-4000-8000-000000000002"
	upToLastLine := func(text string) string { return dropLastLine(dropLastLine(text)) }
	whole, err := replayWith(t, "subagent", sessionID, upToLastLine, noResult)
	if err != nil {
		t.Fatal(err)
	}
	stopped, err := replayWith(t, "subagent", sessionID, func(text string) string { return cutLastLine(dropLastLine(text)) }, noResult)
	if err != nil {
		t.Fatalf("a stopped run wrote no rows: %v", err)
	}
	if stopped.CostSource != SourceSessionFile || len(stopped.Requests) != 4 || !slices.Equal(stopped.Requests, whole.Requests) {
		t.Errorf("%s, %+v", stopped.CostSource, stopped.Requests)
	}
	if stopped.Unrecorded != 1 || whole.Unrecorded != 0 {
		t.Errorf("unrecorded %d, and %d with no line cut", stopped.Unrecorded, whole.Unrecorded)
	}
	if !slices.Contains(stopped.Warnings, "no result object: the session files are the only record of this run") {
		t.Errorf("warnings %q", stopped.Warnings)
	}
}

func TestAnUnreadableSessionCountDiscardsTheSessionFiles(t *testing.T) {
	cases := map[string]struct{ from, to, reason string }{
		"negative":    {`"output_tokens":4,`, `"output_tokens":-4,`, "usage output_tokens: -4 is negative"},
		"fraction":    {`"output_tokens":4,`, `"output_tokens":4.5,`, "usage output_tokens: 4.5 is not a whole number"},
		"text":        {`"output_tokens":4,`, `"output_tokens":"four",`, `usage output_tokens: "four" is not a number`},
		"no usage":    {`"usage":{`, `"usage_moved":{`, "the message states no usage object"},
		"lifetimes":   {`"ephemeral_5m_input_tokens":0}`, `"ephemeral_5m_input_tokens":1}`, "cache writes of 1 tokens for five minutes and 2986 for one hour pass the written total of 2986"},
		"model":       {`"model":"claude-haiku-5-5"`, `"model":5`, "model 5 is not text"},
		"request id":  {`"requestId":"req_fixture_0001"`, `"requestId":1`, "requestId 1 is not text"},
		"string body": {`"message":{"model"`, `"message":"SYNTHETIC-TEXT","moved":{"model"`, "the message is not an object"},
	}
	totals := sum(replay(t, "one-request", oneRequestID).Requests)
	for name, tc := range cases {
		edit := func(text string) string {
			if !strings.Contains(text, tc.from) {
				t.Fatalf("%s: the fixture holds no %s", name, tc.from)
			}
			return strings.Replace(text, tc.from, tc.to, 1)
		}
		report, err := replayWith(t, "one-request", oneRequestID, edit, everyLine)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		wantWarning := "claude-code: the session files were not used, and the result's totals stand: session file line 2: "
		if name != "request id" {
			wantWarning += "request req_fixture_0001: "
		}
		if report.CostSource != SourceStream || sum(report.Requests) != totals || !slices.Equal(report.Warnings, []string{wantWarning + tc.reason}) {
			t.Errorf("%s: %s %+v, warnings %q", name, report.CostSource, sum(report.Requests), report.Warnings)
		}
		_, err = replayWith(t, "one-request", oneRequestID, edit, noResult)
		if err == nil || !strings.HasPrefix(err.Error(), "claude-code: no result object, and the session files could not be read: session file line 2: ") || !strings.HasSuffix(err.Error(), tc.reason) {
			t.Errorf("%s with no result: %v", name, err)
		}
	}
}

func TestSplitWritesRefusesOverrun(t *testing.T) {
	const largest = 1<<63 - 1
	accepted := []struct {
		created, fiveMinute, oneHour int64
		unsplit                      focus.TokenClass
		want5m, want1h               int64
		assumed                      focus.TokenClass
	}{
		{100, 40, 60, focus.CacheWrite5m, 40, 60, ""},
		{100, 0, 0, focus.CacheWrite5m, 100, 0, focus.CacheWrite5m},
		{100, 30, 0, focus.CacheWrite1h, 30, 70, focus.CacheWrite1h},
		{largest, largest, 0, focus.CacheWrite5m, largest, 0, ""},
		{0, 0, 0, focus.CacheWrite1h, 0, 0, ""},
	}
	for _, tc := range accepted {
		fiveMinute, oneHour, assumed, err := splitWrites(tc.created, tc.fiveMinute, tc.oneHour, tc.unsplit)
		if err != nil || fiveMinute != tc.want5m || oneHour != tc.want1h || assumed != tc.assumed {
			t.Errorf("%+v: %d %d %q %v", tc, fiveMinute, oneHour, assumed, err)
		}
	}
	refused := [][3]int64{{100, 150, 0}, {100, 0, 101}, {100, 50, 51}, {0, largest, largest}, {10, largest, 0}, {largest - 1, largest, 0}}
	for _, tc := range refused {
		if _, _, _, err := splitWrites(tc[0], tc[1], tc[2], focus.CacheWrite5m); err == nil || !strings.Contains(err.Error(), "pass the written total") {
			t.Errorf("%v: %v", tc, err)
		}
	}
}

func TestContextSumOverflowRefuses(t *testing.T) {
	const largest = 1<<63 - 1
	for _, counts := range [][3]int64{{1e12, 0, 0}, {0, 0, 1e12}, {4e11, 3e11, 3e11}} {
		if total, err := contextInput(counts[0], counts[1], counts[2]); err != nil || total != 1e12 {
			t.Errorf("%v: %d %v", counts, total, err)
		}
	}
	for _, counts := range [][3]int64{{1e12 + 1, 0, 0}, {5e11, 5e11, 1}, {largest, largest, largest}, {2, 9223372036854775000, 0}, {0, 0, largest}} {
		if _, err := contextInput(counts[0], counts[1], counts[2]); !errors.Is(err, errPastBound) {
			t.Errorf("%v: %v", counts, err)
		}
	}
	huge := func(text string) string {
		return strings.Replace(text, `"cache_read_input_tokens":649`, `"cache_read_input_tokens":9223372036854775000`, 1)
	}
	_, err := replayWith(t, "one-request", oneRequestID, huge, everyLine)
	want := "claude-code: session file line 2: request req_fixture_0001: the input is past the bound of 1000000000000 tokens one request can hold: input 2, cache reads 9223372036854775000, cache writes 2986"
	if err == nil || err.Error() != want {
		t.Errorf("the session file: %v", err)
	}
	_, err = replayWith(t, "one-request", oneRequestID, huge, noResult)
	if err == nil || err.Error() != want {
		t.Errorf("the session file with no result: %v", err)
	}
	_, err = finishResult(t, `{"inputTokens":2,"cacheReadInputTokens":9223372036854775000}`)
	if err == nil || err.Error() != "claude-code: the result object could not be read: modelUsage claude-haiku-5-5: the input is past the bound of 1000000000000 tokens one request can hold: input 2, cache reads 9223372036854775000, cache writes 0" {
		t.Errorf("the result: %v", err)
	}
}

// oneHourRun reads the synthetic one-hour-cache run, with or without its
// session file, as the given apiKeySource.
func oneHourRun(t *testing.T, withFile bool, apiKeySource string) adapter.Report {
	t.Helper()
	const sessionID = "00000000-0000-4000-8000-0000000000e2"
	config := t.TempDir()
	if withFile {
		project := filepath.Join(config, "projects", "-work")
		if err := os.MkdirAll(project, 0o755); err != nil {
			t.Fatal(err)
		}
		copyFile(t, synthetic("session", "one-hour-cache.jsonl"), filepath.Join(project, sessionID+".jsonl"))
	}
	session := Agent{ConfigDir: config}.NewSession(adapter.Plan{})
	stream := string(mustReadFile(t, synthetic("session", "one-hour-cache-stream.jsonl")))
	stream = strings.ReplaceAll(stream, `"apiKeySource":"ANTHROPIC_API_KEY"`, `"apiKeySource":"`+apiKeySource+`"`)
	for _, line := range strings.Split(strings.TrimSpace(stream), "\n") {
		if err := session.Feed([]byte(line), recorded); err != nil {
			t.Fatal(err)
		}
	}
	report, err := session.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestOneHourCachingEnv(t *testing.T) {
	cases := []struct {
		name, variable, apiKeySource string
		class                        focus.TokenClass
	}{
		{"an API key", "", "ANTHROPIC_API_KEY", focus.CacheWrite5m},
		{"an API key with the variable", "1", "ANTHROPIC_API_KEY", focus.CacheWrite1h},
		{"an API key with the variable as text", "true", "ANTHROPIC_API_KEY", focus.CacheWrite1h},
		{"a subscription", "", "none", focus.CacheWrite1h},
		{"a subscription with the variable", "1", "none", focus.CacheWrite1h},
	}
	for _, tc := range cases {
		t.Setenv(oneHourCacheVariable, tc.variable)
		for _, withFile := range []bool{true, false} {
			report := oneHourRun(t, withFile, tc.apiKeySource)
			wantSource := SourceStream
			if withFile {
				wantSource = SourceSessionFile
			}
			if len(report.Requests) != 1 || report.CostSource != wantSource {
				t.Fatalf("%s, file %v: %s %+v", tc.name, withFile, report.CostSource, report.Requests)
			}
			request := report.Requests[0]
			want := adapter.Tokens{InputUncached: 2, CacheRead: 1000, Output: 30, CachedInputReported: 1000}
			if tc.class == focus.CacheWrite5m {
				want.CacheWrite5m = 500
			} else {
				want.CacheWrite1h = 500
			}
			if request.Tokens != want || request.AssumedWrite != tc.class {
				t.Errorf("%s, file %v: %+v, assumed %q", tc.name, withFile, request.Tokens, request.AssumedWrite)
			}
		}
	}
}

// TestNewestRecordedRunReadsWithNoWarning reads Claude Code 2.1.319's
// recorded run, the newest release recorded, with its session file. It reads
// with no warning, and its per-request counts add up to the result's.
func TestNewestRecordedRunReadsWithNoWarning(t *testing.T) {
	directory := filepath.Join("testdata", "2.1.319", "current")
	session := Agent{ConfigDir: installFrom(t, directory, oneRequestID)}.NewSession(adapter.Plan{})
	stream := strings.TrimSpace(string(mustReadFile(t, filepath.Join(directory, "stream.jsonl"))))
	for _, line := range strings.Split(stream, "\n") {
		if err := session.Feed([]byte(line), recorded); err != nil {
			t.Fatal(err)
		}
	}
	report, err := session.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Warnings) != 0 || report.CostSource != SourceSessionFile || report.AgentVersion != "2.1.319" || report.CostBasis != focus.Measured {
		t.Fatalf("%s %s %s, warnings %q", report.CostSource, report.AgentVersion, report.CostBasis, report.Warnings)
	}
	if len(report.Requests) != 1 || report.Requests[0].Grain != adapter.PerRequest || report.Requests[0].ID != "req_fixture_0001" {
		t.Fatalf("%+v", report.Requests)
	}
	var result struct {
		ModelUsage map[string]providerTotals `json:"modelUsage"`
	}
	lines := strings.Split(stream, "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &result); err != nil {
		t.Fatal(err)
	}
	totals := result.ModelUsage["claude-haiku-5-5"]
	want := adapter.Tokens{
		InputUncached: totals.InputTokens, CacheRead: totals.CacheRead, CacheWrite5m: totals.FiveMinute, CacheWrite1h: totals.OneHour,
		Output: totals.OutputTokens, ReasoningOutput: totals.Thinking, CachedInputReported: totals.CacheRead,
	}
	if got := sum(report.Requests); got != want || want.CacheWrite5m+want.CacheWrite1h != totals.CacheCreated || want.InputUncached == 0 {
		t.Errorf("rows %+v, result %+v", got, want)
	}
}

// passedOverLine is a line of a type the reader passes over, in a shape no
// recording holds.
const passedOverLine = `{"type":"attachment","message":"SYNTHETIC-TEXT","requestId":"req_synthetic_0009","timestamp":12}`

// readSessionData reads one file's text into a new builder.
func readSessionData(data []byte) ([]adapter.Request, fileFacts, error) {
	builder := newRequestBuilder(focus.CacheWrite5m)
	facts, err := builder.readData(data)
	return builder.requests(time.Time{}, time.Time{}), facts, err
}

// FuzzSessionFile reads any text as a session file, seeded with every
// recorded and synthetic session file. No text may panic the reader; a file
// that reads holds no negative class and no input past the bound; and a line
// of a type the reader passes over, added at the end, changes nothing it read.
func FuzzSessionFile(f *testing.F) {
	for _, pattern := range []string{"*/*/session.jsonl", "*/*/subagents/*.jsonl", "synthetic/session/*.jsonl"} {
		paths, err := filepath.Glob(filepath.Join("testdata", pattern))
		if err != nil {
			f.Fatal(err)
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				f.Fatal(err)
			}
			f.Add(data)
		}
	}
	f.Add([]byte(`{"type":"assistant","requestId":"r","message":{"usage":{"cache_creation_input_tokens":1,"cache_creation":{"ephemeral_1h_input_tokens":2}}}}`))
	f.Add([]byte(`{"type":"assistant","requestId":"r","message":{"usage":{"input_tokens":1e12,"cache_read_input_tokens":1}}}`))
	f.Add([]byte(`{"type":"assistant","requestId":"r","message":{"usage":{"output_tokens":-1}}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		requests, facts, err := readSessionData(data)
		if err == nil {
			for _, request := range requests {
				if err := request.Tokens.Check(); err != nil {
					t.Fatalf("%s: %v", request.ID, err)
				}
				if request.ContextInputTokens < 0 || request.ContextInputTokens > requestBound {
					t.Fatalf("%s: input %d", request.ID, request.ContextInputTokens)
				}
			}
		}
		appended := append(append(slices.Clone(data), '\n'), passedOverLine+"\n"...)
		again, againFacts, againErr := readSessionData(appended)
		if (err == nil) != (againErr == nil) || (err != nil && err.Error() != againErr.Error()) {
			t.Fatalf("an added line changed the error: %v, then %v", err, againErr)
		}
		if err == nil && !slices.Equal(requests, again) {
			t.Fatalf("an added line changed the requests: %+v, then %+v", requests, again)
		}
		if err == nil && againFacts.skipped < facts.skipped {
			t.Fatalf("skipped %d, then %d", facts.skipped, againFacts.skipped)
		}
	})
}
