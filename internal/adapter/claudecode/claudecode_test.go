package claudecode

import (
	"encoding/json"
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
	config := t.TempDir()
	project := filepath.Join(config, "projects", "-work")
	if err := os.MkdirAll(filepath.Join(project, sessionID, "subagents"), 0o755); err != nil {
		t.Fatal(err)
	}
	copyFile(t, fixture(scenario, "session.jsonl"), filepath.Join(project, sessionID+".jsonl"))
	agents, err := filepath.Glob(fixture(scenario, "subagents/*.jsonl"))
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
	b := newRequestBuilder(false)
	tokens, assumed := b.tokens(usage{CacheCreated: 10, CacheCreation: &cacheCreation{}})
	if tokens.CacheWrite1h != 10 || assumed != focus.CacheWrite1h {
		t.Errorf("subscription: %+v %v", tokens, assumed)
	}
	b = newRequestBuilder(true)
	tokens, assumed = b.tokens(usage{CacheCreated: 10})
	if tokens.CacheWrite5m != 10 || assumed != focus.CacheWrite5m {
		t.Errorf("api key: %+v %v", tokens, assumed)
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
