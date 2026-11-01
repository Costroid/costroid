package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
)

const (
	thread      = "00000000-0000-4000-8000-000000000004"
	countLine   = `{"timestamp":"2026-11-01T11:17:13.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":7},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":7}}}}`
	metaLine    = `{"timestamp":"2026-11-01T11:17:09.000Z","type":"session_meta","payload":{"id":"x","cli_version":"0.164.0-alpha.11.1"}}`
	contextLine = `{"timestamp":"2026-11-01T11:17:10.000Z","type":"turn_context","payload":{"model":"gpt-5.6-sol"}}`
)

func applied(session *Session, lines ...string) *Session {
	session.threadID = thread
	session.log.opened = true
	for _, line := range lines {
		session.applyRollout([]byte(line))
	}
	return session
}

func recordedRollout(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", version, "exec", "rollout.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestRecordedRolloutGivesOneRequestPerModelCall(t *testing.T) {
	requests := applied(watchedSession(""), recordedRollout(t)...).log.requests
	want := []adapter.Tokens{
		{InputUncached: 5624, CacheRead: 11264, Output: 106, CachedInputReported: 11264},
		{InputUncached: 251, CacheRead: 16768, Output: 5, CachedInputReported: 16768},
	}
	if len(requests) != len(want) {
		t.Fatalf("%d requests", len(requests))
	}
	for i, request := range requests {
		if request.Tokens != want[i] {
			t.Errorf("request %d: %+v", i+1, request.Tokens)
		}
		if request.Grain != adapter.PerRequest || request.Model != "gpt-5.6-sol" {
			t.Errorf("%+v", request)
		}
	}
	if requests[0].ID != "response-1" || requests[1].ID != "response-2" {
		t.Errorf("ids %s, %s", requests[0].ID, requests[1].ID)
	}
	if requests[0].ContextInputTokens != 16888 || requests[1].ContextInputTokens != 17019 {
		t.Errorf("context %d, %d", requests[0].ContextInputTokens, requests[1].ContextInputTokens)
	}
}

func TestRecordedStopSettledOnTheUsageRecordBeforeAnyTokenCount(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", version, "stopped", "rollout.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"token_count"`) {
		t.Fatal("the recording holds a token_count: Codex writes it only after the tool returns")
	}
	session := replay(t, "stopped")
	session.planned = watchedSession("").planned
	applied(session, strings.Split(strings.TrimSpace(string(data)), "\n")...)
	report, err := session.Finish()
	if err != nil {
		t.Fatal(err)
	}
	want := adapter.Tokens{InputUncached: 5632, CacheRead: 11264, Output: 64, CachedInputReported: 11264}
	if len(report.Requests) != 1 || report.Requests[0].Tokens != want || report.Requests[0].ID != "response-1" {
		t.Fatalf("%+v", report.Requests)
	}
	if report.CostSource != costSourceRollout || report.Enforcement.Grade != adapter.GradeWrapperWatchdog {
		t.Errorf("%+v", report)
	}
}

func TestRequestSpansRunFromTheTaskStartAndThenFromTheLastRequest(t *testing.T) {
	requests := applied(watchedSession(""), recordedRollout(t)...).log.requests
	layout := "15:04:05.000"
	if got := requests[0].Start.Format(layout) + " " + requests[0].End.Format(layout); got != "11:17:09.505 11:17:13.397" {
		t.Errorf("first %s", got)
	}
	if got := requests[1].Start.Format(layout) + " " + requests[1].End.Format(layout); got != "11:17:13.397 11:17:14.363" {
		t.Errorf("second %s", got)
	}
}

func TestRolloutRowsSumToTheStreamsTotal(t *testing.T) {
	session := replay(t, "exec")
	session.planned = watchedSession("").planned
	applied(session, recordedRollout(t)...)
	report, err := session.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if report.CostSource != costSourceRollout || report.Enforcement.Grade != adapter.GradeWrapperWatchdog {
		t.Fatalf("%+v", report)
	}
	var sum adapter.Tokens
	for _, request := range report.Requests {
		sum = sum.Add(request.Tokens)
	}
	if sum != turns(t, replay(t, "exec"))[0].Tokens {
		t.Errorf("%+v", sum)
	}
}

func TestTokenCountAloneSettlesARequestUnderAStandInID(t *testing.T) {
	session := applied(watchedSession(""), metaLine, contextLine, countLine)
	if len(session.log.requests) != 1 {
		t.Fatalf("%d requests", len(session.log.requests))
	}
	request := session.log.requests[0]
	if request.ID != thread+":request:1" || request.Tokens.InputUncached != 60 || request.Tokens.CacheRead != 40 {
		t.Errorf("%+v", request)
	}
}

func TestRepeatedTokenCountCountsOnce(t *testing.T) {
	rateLimitUpdate := countLine
	session := applied(watchedSession(""), metaLine, countLine, rateLimitUpdate, rateLimitUpdate)
	if len(session.log.requests) != 1 {
		t.Errorf("%d requests", len(session.log.requests))
	}
}

func TestTokenCountWithNoInfoIsNotARequest(t *testing.T) {
	line := `{"timestamp":"2026-11-01T11:17:13.000Z","type":"event_msg","payload":{"type":"token_count","info":null}}`
	session := applied(watchedSession(""), metaLine, line)
	if len(session.log.requests) != 0 || session.log.problem != "" {
		t.Errorf("%+v", session.log)
	}
}

func TestRunningTotalThatSkipsARequestStopsTheReading(t *testing.T) {
	skipped := strings.Replace(countLine, `"last_token_usage":{"input_tokens":100`, `"last_token_usage":{"input_tokens":90`, 1)
	session := applied(watchedSession(""), metaLine, skipped, countLine)
	if !strings.Contains(session.log.problem, "did not move by its last request") || len(session.log.requests) != 0 {
		t.Errorf("%+v", session.log)
	}
}

func TestUsageBeforeAVersionIsRefused(t *testing.T) {
	session := applied(watchedSession(""), countLine)
	if !strings.Contains(session.log.problem, "names no Codex version") {
		t.Errorf("problem %q", session.log.problem)
	}
}

func TestLineThatIsNotJSONStopsTheReading(t *testing.T) {
	session := applied(watchedSession(""), metaLine, "{not json", countLine)
	if !strings.Contains(session.log.problem, "could not be parsed") || len(session.log.requests) != 0 {
		t.Errorf("%+v", session.log)
	}
}

func TestOtherLinesAreNeverDecodedPastTheirEnvelope(t *testing.T) {
	line := `{"timestamp":"2026-11-01T11:17:13.000Z","type":"response_item","payload":{"usage":"a prompt could write this","info":7}}`
	session := applied(watchedSession(""), metaLine, line)
	if session.log.problem != "" {
		t.Errorf("problem %q", session.log.problem)
	}
}

func TestStoppedRunKeepsTheRolloutRowsAndSaysARequestMayBeMissing(t *testing.T) {
	session := watchedSession("")
	for _, line := range []string{`{"type":"thread.started","thread_id":"` + thread + `"}`, `{"type":"turn.started"}`} {
		if err := session.Feed([]byte(line), recorded); err != nil {
			t.Fatal(err)
		}
	}
	applied(session, metaLine, countLine)
	report, err := session.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if report.CostSource != costSourceRollout || len(report.Requests) != 1 || report.Enforcement.Grade != adapter.GradeWrapperWatchdog {
		t.Fatalf("%+v", report)
	}
	if len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0], "request in flight") {
		t.Errorf("warnings %v", report.Warnings)
	}
}

func TestStopInALaterTurnIsNotAMismatch(t *testing.T) {
	session := watchedSession("")
	lines := []string{
		`{"type":"thread.started","thread_id":"` + thread + `"}`,
		`{"type":"turn.started"}`,
		`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":7}}`,
		`{"type":"turn.started"}`,
	}
	for _, line := range lines {
		if err := session.Feed([]byte(line), recorded); err != nil {
			t.Fatal(err)
		}
	}
	second := `{"timestamp":"2026-11-01T11:17:14.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":150,"cached_input_tokens":40,"output_tokens":7},"last_token_usage":{"input_tokens":50}}}}`
	applied(session, metaLine, countLine, second)
	report, err := session.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if report.Enforcement.Grade != adapter.GradeWrapperWatchdog || len(report.Requests) != 2 {
		t.Fatalf("%+v", report)
	}
}

func TestPartialLineWaitsForItsNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(path, []byte("one\ntw"), 0o644); err != nil {
		t.Fatal(err)
	}
	opened, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	file := rolloutFile{file: opened}
	defer file.close()
	first, err := file.lines()
	if err != nil || len(first) != 1 || string(first[0]) != "one" {
		t.Fatalf("%q, %v", first, err)
	}
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := file.lines()
	if err != nil || len(second) != 1 || string(second[0]) != "two" {
		t.Errorf("%q, %v", second, err)
	}
}

func TestRolloutIsFoundByThreadWhateverTheDateDirectory(t *testing.T) {
	home := t.TempDir()
	directory := filepath.Join(home, "sessions", "2026", "11", "02")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	data := strings.Join(recordedRollout(t), "\n") + "\n"
	if err := os.WriteFile(filepath.Join(directory, "rollout-2026-11-02T00-10-00-"+thread+".jsonl"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	session := watchedSession(home)
	if err := session.Feed([]byte(`{"type":"thread.started","thread_id":"`+thread+`"}`), recorded); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls := make(chan int, 4)
	go func() {
		session.Tail(ctx, func(requests []adapter.Request) { calls <- len(requests) })
		close(calls)
	}()
	select {
	case count := <-calls:
		if count != 2 {
			t.Errorf("%d requests settled", count)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the tail never settled a request")
	}
	cancel()
	for range calls {
		t.Error("the tail called back after its first settlement")
	}
}

func TestThreadIDCannotWalkOutOfTheSessionsDirectory(t *testing.T) {
	var file rolloutFile
	if file.open(t.TempDir(), "../../etc") {
		t.Error("a thread id with a path opened a file")
	}
}

func TestTailAfterTheStreamClosedTheTurnDoesNotCallBack(t *testing.T) {
	session := replay(t, "exec")
	session.planned = watchedSession("").planned
	session.log.opened = true
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(recordedRollout(t), "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opened, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	file := rolloutFile{file: opened}
	defer file.close()
	if _, working := session.follow(&file); working {
		t.Error("a request read after the turn closed reported the agent as working")
	}
}
