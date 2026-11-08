package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/run"
	"github.com/Costroid/costroid/internal/vcs"
)

// commentLimit is the most characters GitHub stores in a comment or a check
// run's summary or text.
const commentLimit = 65535

// warningPrefix opens every line a report writes to the job log about the
// files the agent's job wrote.
const warningPrefix = "costroid: warning: "

var amountAfterCurrency = regexp.MustCompile(`\b[A-Z]{3} [0-9]`)

// FuzzReportInput reads a rows file and the result object beside it, both of
// which the agent's job wrote, under any agent result the CI system states,
// and renders what a report posts. The reason and the agent's error come
// only from their closed lists, every log line is one warning, and every
// rendering keeps GC1's invariants with amounts shown and withheld.
func FuzzReportInput(f *testing.F) {
	rows, err := os.ReadFile(filepath.Join("..", "..", "internal", "comment", "testdata", "rows.jsonl"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(rows, []byte(`{"run_reason":"","reader_error":"","agent_error":null}`), "success")
	f.Add([]byte{}, []byte(`{"run_reason":"reader_refused","reader_error":"claude-code: the result object could not be read","agent_error":{"class":"rate_limit"}}`), "failure")
	f.Add([]byte{}, []byte(`{"run_reason":"no_such_reason","agent_error":{"class":"::error::x\n::set-output name=a::b"}}`), "")
	f.Add([]byte{}, []byte(`{"run_reason":"no_usage"}`), "skipped")
	f.Add([]byte("{\n"), []byte(`[`), "neither")
	f.Fuzz(func(t *testing.T, rows, result []byte, agentResult string) {
		dir := t.TempDir()
		path := filepath.Join(dir, "rows.jsonl")
		if err := os.WriteFile(path, rows, 0o600); err != nil {
			t.Fatal(err)
		}
		if len(result) > 0 {
			if err := os.WriteFile(runResultPath(path), result, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		var log bytes.Buffer
		input := rowsInput(reportFlags{rows: path, agentResult: agentResult}, &log)
		for _, line := range strings.Split(strings.TrimSuffix(log.String(), "\n"), "\n") {
			if line != "" && !strings.HasPrefix(line, warningPrefix) {
				t.Fatalf("the log holds a line that is not a warning: %q", line)
			}
		}
		if input.RunReason != "" && (input.RunReason.Sentence() == "" || input.State != comment.NoRows) {
			t.Fatalf("the run reason %q came through beside state %v", input.RunReason, input.State)
		}
		if input.AgentError != "" && run.AgentErrorFix(input.AgentError) == "" {
			t.Fatalf("the agent error %q is not on the list", input.AgentError)
		}
		for _, amounts := range []comment.Amounts{comment.AmountsShown, comment.AmountsOff} {
			input.Amounts = amounts
			assertRendered(t, input)
		}
	})
}

// assertRendered renders the comment, the job summary, the check run and the
// step's outputs, and holds them to GC1's invariants.
func assertRendered(t *testing.T, input comment.Input) {
	t.Helper()
	body, err := comment.Comment(input)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := comment.Summary(input)
	if err != nil {
		t.Fatal(err)
	}
	check, err := comment.Check(input)
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := comment.Outputs(input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(body, comment.Marker) != 1 || !strings.HasPrefix(body, comment.Marker+"\n") {
		t.Fatalf("the comment holds the marker %d times, or does not open with it", strings.Count(body, comment.Marker))
	}
	for name, text := range map[string]string{"comment": body, "summary": summary, "check summary": check.Summary, "check text": check.Text} {
		if len(text) > commentLimit {
			t.Fatalf("the %s is %d bytes", name, len(text))
		}
	}
	for _, output := range outputs {
		if strings.ContainsAny(output.Value, "\r\n") {
			t.Fatalf("output %s holds a line break", output.Name)
		}
	}
	if input.Amounts != comment.AmountsShown {
		for name, text := range map[string]string{"comment": body, "summary": summary} {
			if found := amountAfterCurrency.FindString(text); found != "" {
				t.Fatalf("withheld, the %s holds %q", name, found)
			}
		}
	}
}

// FuzzForkRun reads the workflow_run event a follow-up job is given. A run
// it accepts was started by a pull request from another repository, and
// names a full head commit.
func FuzzForkRun(f *testing.F) {
	const repository = "example/widgets"
	head := strings.Repeat("ab", 20)
	f.Add([]byte(`{"workflow_run":{"id":7,"event":"pull_request","head_sha":"` + head + `","head_branch":"fix","head_repository":{"full_name":"fork/widgets","owner":{"login":"fork"}}}}`))
	f.Add([]byte(`{"workflow_run":{"event":"pull_request","head_sha":"` + head + `","head_repository":{"full_name":"Example/Widgets"}}}`))
	f.Add([]byte(`{"workflow_run":{"event":"push","head_sha":"HEAD"}}`))
	f.Add([]byte(`{"workflow_run":null}`))
	f.Fuzz(func(t *testing.T, event []byte) {
		path := filepath.Join(t.TempDir(), "event.json")
		if err := os.WriteFile(path, event, 0o600); err != nil {
			t.Fatal(err)
		}
		finished, sha, err := finishedPullRun(followUpTrigger, path, repository, false)
		if err != nil {
			if sha != "" {
				t.Fatalf("a refused run named head %q", sha)
			}
			return
		}
		if finished.Event != "pull_request" || finished.HeadRepository == nil || strings.EqualFold(finished.HeadRepository.FullName, repository) {
			t.Fatalf("accepted %+v", finished)
		}
		if sha != finished.HeadSHA || !vcs.IsCommit(sha) {
			t.Fatalf("the head reads %q", sha)
		}
	})
}
