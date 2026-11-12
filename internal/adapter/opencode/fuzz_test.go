package opencode

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

// fuzzRead feeds every line of stream to a session on the recorded model and
// finishes it.
func fuzzRead(stream []byte) (adapter.Report, error) {
	s := &Session{
		planned:   adapter.Enforcement{Grade: adapter.GradeNone, Note: unprovenNote, Watchdog: true},
		model:     model{provider: "anthropic", id: "claude-haiku-5-5"},
		costBasis: focus.Measured,
		notify:    make(chan struct{}, 1),
	}
	at := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	for _, line := range bytes.Split(stream, []byte("\n")) {
		// A line that is not JSON is refused, and the run reads on.
		_ = s.Feed(line, at)
	}
	return s.Finish()
}

// FuzzOpenCodeFeed reads any stream. A stream reads the same way twice, no
// step holds a negative class or less output than its reasoning, and
// OpenCode's own figure, when stated, is not below zero.
func FuzzOpenCodeFeed(f *testing.F) {
	found, err := filepath.Glob(filepath.Join("testdata", "*", "*", "stream.jsonl"))
	if err != nil {
		f.Fatal(err)
	}
	for _, path := range found {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
		for _, line := range bytes.Split(data, []byte("\n")) {
			f.Add(line)
		}
	}
	f.Add([]byte(`{"type":"step_finish","sessionID":"s","part":{"tokens":{"input":9223372036854775807,"output":1,"reasoning":0,"cache":{"read":0,"write":0}},"cost":-1}}`))
	f.Add([]byte(`{"type":"step_finish","sessionID":"s","part":{"tokens":{"total":4,"input":1,"output":1,"reasoning":2,"cache":{"read":0,"write":0}},"cost":1e-400}}`))
	f.Fuzz(func(t *testing.T, stream []byte) {
		report, err := fuzzRead(stream)
		again, againErr := fuzzRead(stream)
		if (err == nil) != (againErr == nil) || (err != nil && err.Error() != againErr.Error()) || !reflect.DeepEqual(report, again) {
			t.Fatalf("one stream read two ways: %v, then %v", err, againErr)
		}
		if err != nil {
			return
		}
		for _, request := range report.Requests {
			if err := request.Tokens.Check(); err != nil {
				t.Fatalf("step %s: %v", request.ID, err)
			}
			if request.Tokens.Output < request.Tokens.ReasoningOutput {
				t.Fatalf("step %s holds %d output tokens and %d reasoning", request.ID, request.Tokens.Output, request.Tokens.ReasoningOutput)
			}
		}
		if stated := report.AgentStated; stated != nil {
			if quantity, err := decimal.Parse(stated.Quantity); err != nil || quantity.Sign() < 0 {
				t.Fatalf("OpenCode's figure reads %q", stated.Quantity)
			}
		}
	})
}
