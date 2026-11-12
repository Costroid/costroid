package copilot_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/Costroid/costroid/internal/adapter/copilot"
)

// FuzzCopilotEvents reads any session file, on the user's key or not. A file
// reads the same way twice, and every request it gives is priced at a sheet,
// holds no negative class, and bills the input its record states, cache
// reads and writes included.
func FuzzCopilotEvents(f *testing.F) {
	found, err := filepath.Glob(filepath.Join("testdata", "*", "*", "events.jsonl"))
	if err != nil {
		f.Fatal(err)
	}
	for _, path := range found {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data, false)
		f.Add(data, true)
		f.Add(data[:len(data)/2], false)
	}
	f.Add([]byte(`{"type":"session.start","data":{"sessionId":"s"}}
{"type":"session.usage_record","timestamp":"2026-11-09T13:56:44Z","data":{"usage":{"model":"gemini-3.8-flash","inputTokens":5,"cacheReadTokens":2,"cacheWriteTokens":3,"outputTokens":1}}}
{"type":"session.usage_record","timestamp":"2026-11-09T13:56:45Z","data":{"usage":{"model":"gemini-3.8-flash","inputTokens":5,"outputTokens":-1}}}
`), false)
	f.Add([]byte(`{"type":"session.start","data":{"sessionId":"s"}}
{"type":"session.usage_record","timestamp":"2026-11-09T13:56:44Z","data":{"usage":{"model":"gemini-3.8-flash","inputTokens":5,"cacheReadTokens":2,"cacheWriteTokens":3,"outputTokens":1}}}
`), false)
	f.Add([]byte(`{"type":"session.start","data":{"sessionId":"s"}}
{"type":"session.usage_record","timestamp":"2026-11-09T13:56:44Z","data":{"usage":{"model":"mai-code-1.1-flash","inputTokens":9223372036854775807,"cacheReadTokens":9223372036854775807,"duration":1e400}}}
`), true)
	f.Fuzz(func(t *testing.T, data []byte, keyed bool) {
		report, err := copilot.ReadEvents(bytes.NewReader(data), keyed)
		again, againErr := copilot.ReadEvents(bytes.NewReader(data), keyed)
		if (err == nil) != (againErr == nil) || (err != nil && err.Error() != againErr.Error()) || !reflect.DeepEqual(report, again) {
			t.Fatalf("one file read two ways: %v, then %v", err, againErr)
		}
		if err != nil {
			return
		}
		for _, request := range report.Requests {
			if !slices.Contains([]string{"anthropic", "openai", "google"}, request.Provider) {
				t.Fatalf("request %s is priced at %q", request.ID, request.Provider)
			}
			if err := request.Tokens.Check(); err != nil {
				t.Fatalf("request %s: %v", request.ID, err)
			}
			if billedInput(request.Tokens) != request.ContextInputTokens {
				t.Fatalf("request %s bills %d input tokens of the %d its record states", request.ID, billedInput(request.Tokens), request.ContextInputTokens)
			}
		}
	})
}
