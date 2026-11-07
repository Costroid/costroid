package run

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
)

// TestTheRunSharesItsStderrWithTheAgent holds that the run's warnings and the
// agent's own stderr reach one writer whole, one write at a time, whether the
// agent's stdout goes elsewhere or to the same writer, as under --json. Run it
// with GOFLAGS=-race: os/exec copies the agent's stderr into a writer that is
// not a file on a goroutine of its own, and the race detector names any write
// the run makes beside it.
func TestTheRunSharesItsStderrWithTheAgent(t *testing.T) {
	const agentLine = "agent: a line on stderr"
	const warning = "costroid: warning: 1 line of the agent's stdout was not read: claude-code: stream line is not JSON\n"
	for _, name := range []string{"separate writers", "one writer"} {
		t.Run(name, func(t *testing.T) {
			installClaudeRun(t, "subagent", subagentSession)
			recorded, err := os.ReadFile(os.Getenv("COSTROID_FAKE_STREAM"))
			if err != nil {
				t.Fatal(err)
			}
			stream := append([]byte("npm notice one\n"), recorded...)
			path := filepath.Join(t.TempDir(), "stream.jsonl")
			if err := os.WriteFile(path, stream, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("COSTROID_FAKE_STREAM", path)
			t.Setenv(fakeStderr, agentLine)
			var rows, stdout, stderr bytes.Buffer
			options := Options{
				Argv:    []string{agentNamed(t, "claude"), "-p", "work"},
				Budget:  decimal.MustParse("5"),
				Rows:    &rows,
				Stdin:   strings.NewReader(""),
				Stdout:  &stdout,
				Stderr:  &stderr,
				Catalog: pinned(t),
				Grace:   200 * time.Millisecond,
			}
			want := string(stream) + warning
			if name == "one writer" {
				options.Stdout = &stderr
			} else {
				want = warning
			}
			if _, err := Run(context.Background(), options); err != nil {
				t.Fatal(err)
			}
			got := stderr.String()
			// The agent writes its stderr line at its start, so the copy of it
			// can land before or between any of the run's own whole writes.
			if rest := strings.Replace(got, agentLine+"\n", "", 1); rest == got || rest != want {
				t.Errorf("stderr:\n%q\nwant %q with the line %q once, between two writes", got, want, agentLine)
			}
		})
	}
}

// TestAFileStderrIsHandedToTheAgentAsItIs holds that a file, the terminal or
// a CI log, is not wrapped: os/exec then gives the agent the file itself, so
// what the agent writes reaches it directly and no copy can hold the run.
func TestAFileStderrIsHandedToTheAgentAsItIs(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if got := (Options{Stdout: file, Stderr: file}).lockStderr(); got.Stderr != file || got.Stdout != file {
		t.Errorf("stdout %T, stderr %T, want the file itself", got.Stdout, got.Stderr)
	}
	if got := (Options{}).lockStderr(); got.Stderr != nil {
		t.Errorf("no stderr became %T", got.Stderr)
	}
}
