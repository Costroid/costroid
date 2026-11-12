package ghaw_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/adapter/ghaw"
)

// FuzzGhawUsage reads any agent and threat detection usage files. An artifact
// reads the same way twice, no request holds a negative class, and the grade
// is none: Costroid did not start the run.
func FuzzGhawUsage(f *testing.F) {
	for _, dir := range []string{claudeArtifact, codexArtifact} {
		agent, err := os.ReadFile(filepath.Join(dir, "agent", "token_usage.jsonl"))
		if err != nil {
			f.Fatal(err)
		}
		detection, err := os.ReadFile(filepath.Join(dir, "detection", "token_usage.jsonl"))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(agent, detection)
	}
	f.Add([]byte(record("copilot", "gemini-3.8-flash", `"input_tokens":5,"cache_read_tokens":9,"cache_write_tokens":9`)+"\n"), []byte{})
	f.Add([]byte(record("openai", "o3", `"input_tokens":-1`)+"\n"), []byte(record("anthropic", "claude-haiku-5-5", `"ai_credits_total":"1e9"`)+"\n"))
	f.Fuzz(func(t *testing.T, agent, detection []byte) {
		artifact := fstest.MapFS{
			"agent/token_usage.jsonl":     &fstest.MapFile{Data: agent},
			"detection/token_usage.jsonl": &fstest.MapFile{Data: detection},
		}
		report, err := ghaw.ReadUsage(artifact, nil)
		again, againErr := ghaw.ReadUsage(artifact, nil)
		if (err == nil) != (againErr == nil) || (err != nil && err.Error() != againErr.Error()) || !reflect.DeepEqual(report, again) {
			t.Fatalf("one artifact read two ways: %v, then %v", err, againErr)
		}
		if err != nil {
			return
		}
		if report.Enforcement.Grade != adapter.GradeNone {
			t.Fatalf("a gh-aw run reads the grade %s", report.Enforcement.Grade)
		}
		for _, request := range report.Requests {
			if err := request.Tokens.Check(); err != nil {
				t.Fatalf("request %s: %v", request.ID, err)
			}
		}
	})
}
