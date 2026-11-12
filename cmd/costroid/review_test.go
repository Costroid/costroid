package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	reviewcomment "github.com/Costroid/costroid/internal/review"
	"github.com/Costroid/costroid/internal/scm/scmtest"
)

const (
	keptAnswer = "Two bugs: main.go:12 divides by zero, and auth.go:40 logs the token."
	// onTheWay stands for everything a run writes and reads before its
	// answer: replies, reasoning, tool input and output.
	onTheWay        = "ON THE WAY: a reply, a command or a file the agent read"
	claudeRecording = "../../internal/adapter/claudecode/testdata/2.1.316/"
	codexRecording  = "../../internal/adapter/codex/testdata/0.164.0-alpha.11.1/"
)

// fill rewrites a recorded JSON message: set returns the value to put at a
// key of any object in it, or nil to leave the key as it is.
func fill(t *testing.T, raw []byte, set func(object map[string]any)) []byte {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	var walk func(any)
	walk = func(node any) {
		switch node := node.(type) {
		case map[string]any:
			set(node)
			for _, child := range node {
				walk(child)
			}
		case []any:
			for _, child := range node {
				walk(child)
			}
		}
	}
	walk(value)
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// fillLines fills each line of a recorded JSON lines file.
func fillLines(t *testing.T, path string, set func(object map[string]any)) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	for line := range bytes.Lines(data) {
		out.Write(fill(t, bytes.TrimSpace(line), set))
		out.WriteByte('\n')
	}
	return out.Bytes()
}

// claudeText puts the answer into a result and the on-the-way text into every
// other message's content.
func claudeText(object map[string]any) {
	switch {
	case object["type"] == "result":
		object["result"] = keptAnswer
	case object["type"] == "text" || object["type"] == "thinking":
		object["text"] = onTheWay
	case object["type"] == "tool_use":
		object["input"] = map[string]string{"command": onTheWay}
	case object["type"] == "tool_result":
		object["content"] = onTheWay
	}
}

// answerSourcesOnDisk writes one source of each kind, each holding the answer
// and the on-the-way text, from the recordings.
func answerSourcesOnDisk(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	execution, err := os.ReadFile(claudeRecording + "action-session/claude-execution-output.json")
	if err != nil {
		t.Fatal(err)
	}
	agentMessages := 0
	codexStream := fillLines(t, codexRecording+"exec/stream.jsonl", func(object map[string]any) {
		if object["type"] == "agent_message" {
			agentMessages++
			object["text"] = onTheWay
			if agentMessages == 2 {
				object["text"] = keptAnswer
			}
		}
		if object["type"] == "command_execution" {
			object["command"], object["aggregated_output"] = onTheWay, onTheWay
		}
	})
	rollout := fillLines(t, codexRecording+"action/rollout.jsonl", func(object map[string]any) {
		switch object["type"] {
		case "task_complete":
			object["last_agent_message"] = keptAnswer
		case "message", "custom_tool_call", "custom_tool_call_output":
			object["content"] = onTheWay
		}
	})
	write("codex-home/sessions/2026/11/07/rollout-2026-11-07T13-28-51-x.jsonl", rollout)
	return map[string]string{
		"claude-stream":  write("claude.jsonl", fillLines(t, claudeRecording+"subagent/stream.jsonl", claudeText)),
		"execution-file": write("execution.json", fill(t, execution, claudeText)),
		"codex-stream":   write("codex.jsonl", codexStream),
		"codex-home":     filepath.Join(dir, "codex-home"),
	}
}

func TestRunKeepsTheFinalResultTextAndNotTheStream(t *testing.T) {
	for kind, path := range answerSourcesOnDisk(t) {
		t.Run(kind, func(t *testing.T) {
			if kind != "codex-home" {
				if source, err := os.ReadFile(path); err != nil || !bytes.Contains(source, []byte(onTheWay)) {
					t.Fatalf("the source holds no on-the-way text to leave out: %v", err)
				}
			}
			out := filepath.Join(t.TempDir(), "costroid-review.md")
			code, stdout, stderr := execute(t, "review", "--keep", "--from", kind+"="+path, "--out", out)
			if code != 0 || stdout != "" || !strings.Contains(stderr, fmt.Sprintf("costroid: kept the agent's answer, %d bytes, for the review comment", len(keptAnswer))) {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
			}
			if kept := readText(t, out); kept != keptAnswer {
				t.Fatalf("kept %q", kept)
			}
		})
	}
}

// A run that stopped at its cap has no answer. The file an earlier step or
// the agent left at --out is removed, so nothing stands in for the answer.
func TestKeepLeavesNoFileForARunWithNoAnswer(t *testing.T) {
	stopped := filepath.Join(t.TempDir(), "claude.jsonl")
	if err := os.WriteFile(stopped, fillLines(t, claudeRecording+"budget-stop/stream.jsonl", claudeText), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, from := range map[string]string{
		"a run stopped at its cap": "claude-stream=" + stopped,
		"no stream file":           "claude-stream=" + filepath.Join(t.TempDir(), "absent.jsonl"),
	} {
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "costroid-review.md")
			if err := os.WriteFile(out, []byte("a planted answer"), 0o600); err != nil {
				t.Fatal(err)
			}
			code, _, stderr := execute(t, "review", "--keep", "--from", from, "--out", out)
			if code != 0 || !strings.Contains(stderr, "costroid: no answer was kept: ") {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatalf("a file is left at --out: %v", err)
			}
		})
	}
}

func TestReviewRefusesFlagsThatDoNotFit(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"--keep", "--post", "--file", "x"},
		{"--post"},
		{"--post", "--file", "x", "--out", "y"},
		{"--keep", "--out", "x"},
		{"--keep", "--from", "transcript=x", "--out", "y"},
		{"--keep", "--from", "claude-stream=", "--out", "y"},
		{"--keep", "--from", "claude-stream=x", "--out", "y", "--since", "2026-11-08T00:00:00Z"},
		{"--keep", "--from", "codex-home=x", "--out", "y", "--since", "yesterday"},
		{"--post", "--file", "x", "extra"},
	} {
		code, stdout, stderr := execute(t, append([]string{"review"}, args...)...)
		if code != exitFailure || stdout != "" || !strings.HasPrefix(stderr, "costroid: ") {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args, code, stdout, stderr)
		}
	}
}

// readText reads a file the test needs.
func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// actionDocument reads an action file's top mapping.
func actionDocument(t *testing.T, path string) *yaml.Node {
	t.Helper()
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(readText(t, path)), &document); err != nil {
		t.Fatal(err)
	}
	return document.Content[0]
}

func yamlKey(parent *yaml.Node, key string) *yaml.Node {
	if parent == nil {
		return nil
	}
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			return parent.Content[i+1]
		}
	}
	return nil
}

func yamlValue(parent *yaml.Node, key string) string {
	if found := yamlKey(parent, key); found != nil {
		return found.Value
	}
	return ""
}

func TestPostReviewIsOffByDefault(t *testing.T) {
	for _, file := range []string{"../../action/action.yml", "../../marketplace/action.yml"} {
		if got := yamlValue(yamlKey(yamlKey(actionDocument(t, file), "inputs"), "post-review"), "default"); got != "false" {
			t.Errorf("%s gives post-review the default %q", file, got)
		}
	}
	action := actionDocument(t, "../../action/action.yml")
	const optedIn = "inputs.post-review == 'true'"
	reviewSteps := 0
	for _, step := range yamlKey(yamlKey(action, "runs"), "steps").Content {
		switch name := yamlValue(step, "name"); {
		case strings.Contains(yamlValue(step, "run"), "costroid review"):
			reviewSteps++
			if !strings.Contains(yamlValue(step, "if"), optedIn) {
				t.Errorf("the step %q runs without post-review: %q", name, yamlValue(step, "if"))
			}
		case name == "Keep the rows":
			lines := strings.Split(strings.TrimSpace(yamlValue(yamlKey(step, "with"), "path")), "\n")
			want := "${{ " + optedIn + " && format('{0}/costroid-review.md', runner.temp) || '' }}"
			if len(lines) != 4 || lines[3] != want {
				t.Errorf("the upload's paths read %q", lines)
			}
		case name == "Install costroid":
			if got := yamlValue(yamlKey(step, "env"), "COSTROID_POST_REVIEW"); got != "${{ inputs.post-review }}" {
				t.Errorf("install reads post-review from %q", got)
			}
		}
	}
	if reviewSteps != 2 {
		t.Errorf("%d steps run costroid review, want the keep and the post", reviewSteps)
	}
	recipes, err := filepath.Glob("../../recipes/*.yml")
	if err != nil || len(recipes) == 0 {
		t.Fatalf("no recipes: %v", err)
	}
	for _, recipe := range recipes {
		if strings.Contains(readText(t, recipe), "post-review") {
			t.Errorf("%s turns post-review on", recipe)
		}
	}
	// Without --keep or --post the command does nothing at all.
	stub := reportEnvironment(t, pullRequestEvent())
	if code, _, _ := execute(t, "review"); code != exitFailure || len(stub.Writes()) != 0 {
		t.Fatalf("exit %d, writes %v", code, stub.Writes())
	}
}

func keptFile(t *testing.T, answer string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "costroid-review.md")
	if err := os.WriteFile(path, []byte(answer), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSecondCommentIsEditedInPlace(t *testing.T) {
	stub := reportEnvironment(t, pullRequestEvent())
	// A person who quotes the marker owns that comment, and it stays theirs.
	stub.Comments = append(stub.Comments, scmtest.Comment{ID: 1, Body: reviewcomment.Marker + "\nmine", Login: "octocat"})
	if code, _, stderr := execute(t, "report", "--rows", realRows); code != 0 || len(stub.Comments) != 2 {
		t.Fatalf("exit %d, %d comments: %s", code, len(stub.Comments), stderr)
	}
	report := stub.Comments[1]
	for i, answer := range []string{"The first answer.", "The second answer."} {
		code, _, stderr := execute(t, "review", "--post", "--file", keptFile(t, answer))
		if code != 0 || !strings.Contains(stderr, "costroid: posted the agent's answer in a comment of its own") {
			t.Fatalf("post %d: exit %d: %s", i+1, code, stderr)
		}
		if len(stub.Comments) != 3 {
			t.Fatalf("post %d: %d comments", i+1, len(stub.Comments))
		}
		if body := stub.Comments[2].Body; body != reviewcomment.Render(reviewcomment.Answer{Text: answer, Head: headCommit}) {
			t.Fatalf("post %d: the answer's comment reads\n%s", i+1, body)
		}
	}
	if stub.Comments[0].Body != reviewcomment.Marker+"\nmine" || stub.Comments[1] != report {
		t.Fatalf("another comment changed: %+v", stub.Comments[:2])
	}
	answerWrites := requestsWith(stub.Writes(), "/comments")
	if len(answerWrites) != 3 || !strings.HasPrefix(answerWrites[1], "POST ") || !strings.HasPrefix(answerWrites[2], "PATCH ") {
		t.Fatalf("comment writes: %v", answerWrites)
	}
	// A later run that kept no answer says so in the same comment, so an old
	// answer never stands for a new commit.
	if code, _, _ := execute(t, "review", "--post", "--file", filepath.Join(t.TempDir(), "absent.md")); code != 0 || len(stub.Comments) != 3 ||
		!strings.Contains(stub.Comments[2].Body, "The agent's job kept no answer from its run on `9f2c6e1`.") {
		t.Fatalf("exit %d, comments %+v", code, stub.Comments)
	}
}

func TestPostReadsTheKeptFileAsDataUpToItsCap(t *testing.T) {
	stub := reportEnvironment(t, pullRequestEvent())
	huge := keptFile(t, strings.Repeat("x", 3*reviewcomment.MaxFileBytes))
	if code, _, stderr := execute(t, "review", "--post", "--file", huge); code != 0 || len(stub.Comments) != 1 || !strings.Contains(stub.Comments[0].Body, "runs past 30000 bytes") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	link := filepath.Join(t.TempDir(), "costroid-review.md")
	if err := os.Symlink(keptFile(t, "a secret elsewhere"), link); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := execute(t, "review", "--post", "--file", link)
	if code != 0 || !strings.Contains(stderr, "is not a plain file") || strings.Contains(stub.Comments[0].Body, "secret") {
		t.Fatalf("exit %d, body %q: %s", code, stub.Comments[0].Body, stderr)
	}
}

// A run that kept no answer creates no comment: a pull request whose runs
// never answered shows Costroid's report alone. An earlier answer is edited to
// say this run kept none, so it never stands for a later commit.
func TestPostWithoutAnswerCreatesNoComment(t *testing.T) {
	stub := reportEnvironment(t, pullRequestEvent())
	stub.Comments = []scmtest.Comment{{ID: 1, Body: reviewcomment.Marker + "\nmine", Login: "octocat"}}
	for name, file := range map[string]string{
		"no file":     filepath.Join(t.TempDir(), "absent.md"),
		"blank lines": keptFile(t, "\n \t\n"),
	} {
		code, _, stderr := execute(t, "review", "--post", "--file", file)
		if code != 0 || len(stub.Comments) != 1 || len(stub.Writes()) != 0 ||
			!strings.Contains(stderr, "costroid: no answer to post, and the pull request holds no earlier answer") {
			t.Fatalf("%s: exit %d, comments %+v, writes %v: %s", name, code, stub.Comments, stub.Writes(), stderr)
		}
	}
	if code, _, stderr := execute(t, "review", "--post", "--file", keptFile(t, keptAnswer)); code != 0 || len(stub.Comments) != 2 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	code, _, stderr := execute(t, "review", "--post", "--file", filepath.Join(t.TempDir(), "absent.md"))
	want := reviewcomment.Render(reviewcomment.Answer{Head: headCommit})
	if code != 0 || len(stub.Comments) != 2 || stub.Comments[1].Body != want || stub.Comments[0].Body != reviewcomment.Marker+"\nmine" ||
		!strings.Contains(stderr, "costroid: the earlier answer now says this run kept none") {
		t.Fatalf("exit %d, comments %+v: %s", code, stub.Comments, stderr)
	}
	stub.Fail["GET /comments"] = 500
	if code, _, stderr := execute(t, "review", "--post", "--file", filepath.Join(t.TempDir(), "absent.md")); code != 0 ||
		!strings.Contains(stderr, "costroid: warning: the earlier answer was not replaced: ") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

func TestPostOutsideAPullRequestPostsNothing(t *testing.T) {
	stub := reportEnvironment(t, pullRequestEvent())
	t.Setenv("GITHUB_EVENT_NAME", "push")
	if code, _, stderr := execute(t, "review", "--post", "--file", keptFile(t, keptAnswer)); code != 0 ||
		!strings.Contains(stderr, `costroid: not posted: review posts on the pull_request event, and this run's event is "push"`) || len(stub.Writes()) != 0 {
		t.Fatalf("exit %d, writes %v: %s", code, stub.Writes(), stderr)
	}
	stub.Fail["POST /comments"] = 403
	t.Setenv("GITHUB_EVENT_NAME", "pull_request")
	if code, _, stderr := execute(t, "review", "--post", "--file", keptFile(t, keptAnswer)); code != 0 || !strings.Contains(stderr, "costroid: warning: the agent's answer was not posted: ") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	t.Setenv("GITHUB_ACTIONS", "")
	if code, _, stderr := execute(t, "review", "--post", "--file", keptFile(t, keptAnswer)); code != exitFailure || !strings.Contains(stderr, "costroid: review refused: ") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

// The answer travels in a file of its own. The rows, the run's object, the
// report's result object, the check and the report's comment never hold it.
func TestResultObjectHoldsNoAgentText(t *testing.T) {
	sources := answerSourcesOnDisk(t)
	dir := t.TempDir()
	rows := filepath.Join(dir, "costroid-rows.jsonl")
	if code, _, stderr := execute(t, "ingest-run", "--execution-file", sources["execution-file"], "--out", rows); code != 0 {
		t.Fatalf("ingest-run: exit %d: %s", code, stderr)
	}
	stub := reportEnvironment(t, pullRequestEvent())
	code, stdout, stderr := execute(t, "report", "--rows", rows, "--json")
	if code != 0 || len(stub.Checks) != 1 || len(stub.Comments) != 1 {
		t.Fatalf("report: exit %d: %s", code, stderr)
	}
	runObject := strings.TrimSuffix(rows, ".jsonl") + ".run.json"
	for name, text := range map[string]string{
		"rows": readText(t, rows), "run object": readText(t, runObject), "result object": stdout,
		"check": stub.Checks[0].Summary + stub.Checks[0].Text, "report's comment": stub.Comments[0].Body,
	} {
		if text == "" {
			t.Fatalf("the %s is empty", name)
		}
		for _, words := range []string{keptAnswer, onTheWay} {
			if strings.Contains(text, words) {
				t.Errorf("the %s holds the agent's text %q", name, words)
			}
		}
	}
}
