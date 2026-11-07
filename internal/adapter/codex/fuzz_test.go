package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
)

// fixtureLines seeds a fuzz target with every line of the named fixture files.
func fixtureLines(f *testing.F, name string) {
	f.Helper()
	found, err := filepath.Glob(filepath.Join("testdata", "*", "*", name))
	if err != nil {
		f.Fatal(err)
	}
	for _, path := range found {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			f.Add([]byte(line))
		}
	}
}

// assertNoNegative fails on any request that carries a negative class.
func assertNoNegative(t *testing.T, requests []adapter.Request) {
	t.Helper()
	for _, request := range requests {
		if err := request.Tokens.Check(); err != nil {
			t.Fatalf("%s: %v", request.ID, err)
		}
	}
}

// streamKinds are the stream line types the reader acts on.
var streamKinds = []string{"thread.started", "turn.started", "turn.completed", "turn.failed", "error"}

func feedAll(session *Session, lines ...[]byte) {
	for _, line := range lines {
		// A line that is not JSON is reported and carries nothing.
		_ = session.Feed(line, recorded)
	}
}

// FuzzCodexFeed feeds one line between a thread's start and its one turn.
// Whatever the line, no request carries a negative class, and a line of a
// type the reader does not act on leaves the requests as they were.
func FuzzCodexFeed(f *testing.F) {
	fixtureLines(f, "stream.jsonl")
	f.Add([]byte(`{"type":"turn.completed","message":{"x":1},"usage":{"input_tokens":5,"output_tokens":-1}}`))
	f.Add([]byte(`{"type":"turn.completed","usage":{"input_tokens":0,"cached_input_tokens":4611686018427387904,"cache_write_input_tokens":9223372036854775807}}`))
	f.Add([]byte(`{"type":7}`))
	start := []byte(`{"type":"thread.started","thread_id":"t"}`)
	turn := []byte(`{"type":"turn.completed","usage":{"input_tokens":1000,"cached_input_tokens":300,"cache_write_input_tokens":200,"output_tokens":50}}`)
	baseline := replaySession("m")
	feedAll(baseline, start, turn)
	want, err := baseline.Finish()
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, line []byte) {
		alone := replaySession("m")
		feedAll(alone, start, line)
		if report, err := alone.Finish(); err == nil {
			assertNoNegative(t, report.Requests)
		}
		session := replaySession("m")
		feedAll(session, start, line, turn)
		report, err := session.Finish()
		if err != nil {
			return
		}
		assertNoNegative(t, report.Requests)
		var envelope struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &envelope) == nil && slices.Contains(streamKinds, envelope.Type) {
			return
		}
		if !slices.EqualFunc(report.Requests, want.Requests, sameRequest) {
			t.Errorf("a %q line changed the requests: %+v", envelope.Type, report.Requests)
		}
	})
}

func sameRequest(a, b adapter.Request) bool {
	return a.ID == b.ID && a.Model == b.Model && a.Tokens == b.Tokens && a.Start.Equal(b.Start) && a.End.Equal(b.End)
}

// rolloutKinds are the rollout line types the reader parses past their
// envelope for usage.
var rolloutKinds = []string{"session_meta", "turn_context", "token_usage_record", "event_msg"}

// FuzzRolloutApply reads one line between a session_meta and two requests.
// Whatever the line, no request carries a negative class and the requests sum
// to the running total settled. A well-formed line of a type that carries no
// usage leaves the requests as they were.
func FuzzRolloutApply(f *testing.F) {
	fixtureLines(f, "rollout.jsonl")
	f.Add([]byte(`{"timestamp":"2026-11-01T11:17:13.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":-1},"last_token_usage":{"input_tokens":-1}}}}`))
	f.Add([]byte(`{"timestamp":"2026-11-01T11:17:13.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"output_tokens":-7},"last_token_usage":{"output_tokens":-7}}}}`))
	f.Add([]byte(`{"timestamp":"2026-11-01T11:17:13.000Z","type":"response_item","payload":{"type":"message","message":{"text":1}}}`))
	second := `{"timestamp":"2026-11-01T11:17:14.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":150,"cached_input_tokens":40,"output_tokens":9},"last_token_usage":{"input_tokens":50,"output_tokens":2}}}}`
	read := func(line []byte) rolloutLog {
		log := rolloutLog{thread: "t", opened: true}
		for _, each := range [][]byte{[]byte(metaLine), line, []byte(countLine), []byte(second)} {
			log.apply(each, "m")
		}
		return log
	}
	want := read([]byte(`{"timestamp":"2026-11-01T11:17:10.000Z","type":"response_item","payload":{}}`))
	if want.problem != "" || len(want.requests) != 2 {
		f.Fatalf("baseline %+v", want)
	}
	f.Fuzz(func(t *testing.T, line []byte) {
		log := read(line)
		assertNoNegative(t, log.requests)
		var sum adapter.Tokens
		for _, request := range log.requests {
			sum = sum.Add(request.Tokens)
		}
		settled := log.settled
		if sum.InputUncached+sum.CacheRead+sum.CacheWrite30m != settled.InputTokens || sum.CacheRead != settled.CachedInput ||
			sum.CacheWrite30m != settled.CacheWriteInput || sum.Output != settled.OutputTokens || sum.ReasoningOutput != settled.ReasoningOutput {
			t.Fatalf("requests sum to %+v, settled %+v", sum, settled)
		}
		var entry struct {
			Timestamp time.Time       `json:"timestamp"`
			Type      string          `json:"type"`
			Payload   json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(line, &entry) != nil || slices.Contains(rolloutKinds, entry.Type) {
			return
		}
		if log.problem != "" || !slices.EqualFunc(log.requests, want.requests, sameRequest) {
			t.Errorf("a %q line changed the reading: problem %q, requests %+v", entry.Type, log.problem, log.requests)
		}
	})
}
