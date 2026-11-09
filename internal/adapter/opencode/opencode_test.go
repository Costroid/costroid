package opencode

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

const (
	recordedVersion = "1.19.4"
	recordedModel   = "anthropic/claude-haiku-5-5"
)

var budget = decimal.MustParse("5")

// testdata is read before a test leaves the package's directory.
var testdata, _ = filepath.Abs("testdata")

// isolate gives a test a home, a config directory and a working directory of
// its own, with no OpenCode variable set, and returns the working directory.
func isolate(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	for _, name := range []string{"OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR", "OPENCODE_CONFIG_CONTENT", "OPENCODE_DISABLE_PROJECT_CONFIG", "OPENCODE_AUTH_CONTENT", "ANTHROPIC_API_KEY", "OPENAI_API_KEY"} {
		t.Setenv(name, "")
	}
	dir := t.TempDir()
	t.Chdir(dir)
	return dir
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// session plans argv and starts reading it.
func session(t *testing.T, argv ...string) *Session {
	t.Helper()
	plan, err := Agent{}.Plan(argv, budget)
	if err != nil {
		t.Fatal(err)
	}
	return Agent{}.NewSession(plan).(*Session)
}

// feed reads every line of a stream.
func feed(t *testing.T, s *Session, stream string) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(stream), "\n") {
		if err := s.Feed([]byte(line), time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
}

func recordedStream(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testdata, recordedVersion, "opencode-run", "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestOpenCodeStepFinishRows(t *testing.T) {
	isolate(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	s := session(t, "opencode", "run", "--model", recordedModel, "work")
	feed(t, s, recordedStream(t))
	report, err := s.Finish()
	if err != nil {
		t.Fatal(err)
	}
	// The recorded steps' tokens: input holds no cache, and the cache write
	// is Anthropic's five-minute class.
	want := []adapter.Request{{
		ID:                 "ses_fixture_0001:prt_fixture_0003",
		Provider:           "anthropic",
		Model:              "claude-haiku-5-5",
		Start:              time.UnixMilli(1794232422502).UTC(),
		End:                time.UnixMilli(1794232422811).UTC(),
		ServiceTier:        "standard",
		Region:             "global",
		ContextInputTokens: 10536,
		Grain:              adapter.PerRequest,
		Tokens:             adapter.Tokens{InputUncached: 4, CacheWrite5m: 10532, Output: 75},
		AssumedWrite:       focus.CacheWrite5m,
	}, {
		ID:                 "ses_fixture_0001:prt_fixture_0006",
		Provider:           "anthropic",
		Model:              "claude-haiku-5-5",
		Start:              time.UnixMilli(1794232425144).UTC(),
		End:                time.UnixMilli(1794232425181).UTC(),
		ServiceTier:        "standard",
		Region:             "global",
		ContextInputTokens: 10808,
		Grain:              adapter.PerRequest,
		Tokens:             adapter.Tokens{InputUncached: 2, CacheRead: 10532, CacheWrite5m: 274, Output: 78},
		AssumedWrite:       focus.CacheWrite5m,
	}}
	if !slices.Equal(report.Requests, want) {
		t.Fatalf("requests\n got %+v\nwant %+v", report.Requests, want)
	}
	if report.SessionID != "ses_fixture_0001" || report.CostBasis != focus.Measured || report.CostSource != "agent_stream" {
		t.Errorf("session %q, basis %s, source %s", report.SessionID, report.CostBasis, report.CostSource)
	}
	if report.Enforcement.Grade != adapter.GradeWrapperWatchdog || !report.Enforcement.Watchdog {
		t.Errorf("enforcement %+v", report.Enforcement)
	}
	if stated := report.AgentStated; stated == nil || *stated != (adapter.Stated{Unit: "USD", Quantity: "0.00153317", Source: Name}) {
		t.Errorf("stated %+v, want OpenCode's two costs added, 0.0013544 and 0.00017877", stated)
	}
	if report.Unrecorded != 0 || len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0], "--title= titles the session") {
		t.Errorf("unrecorded %d, warnings %q: the plan titled the session, so no request is missing", report.Unrecorded, report.Warnings)
	}

	t.Run("reasoning is output, and an openai write takes 30 minutes", func(t *testing.T) {
		s := session(t, "opencode", "run", "-m", "openai/gpt-5.6-sol", "--title", "review", "work")
		feed(t, s, `{"type":"step_start","timestamp":1794232422000,"sessionID":"ses_a","part":{"type":"step-start"}}
{"type":"step_finish","timestamp":1794232423000,"sessionID":"ses_a","part":{"id":"prt_b","type":"step-finish","tokens":{"total":1137,"input":100,"output":5,"reasoning":7,"cache":{"read":1000,"write":25}},"cost":1.5e-7}}`)
		report, err := s.Finish()
		if err != nil {
			t.Fatal(err)
		}
		got := report.Requests[0]
		if got.Tokens != (adapter.Tokens{InputUncached: 100, CacheRead: 1000, CacheWrite30m: 25, Output: 12, ReasoningOutput: 7}) || got.AssumedWrite != focus.CacheWrite30m || got.ContextInputTokens != 1125 {
			t.Errorf("%+v", got)
		}
		if report.CostBasis != focus.ListEquivalent || report.AgentStated.Quantity != "0.00000015" || len(report.Warnings) != 0 || report.Unrecorded != 0 {
			t.Errorf("basis %s, stated %+v, warnings %q", report.CostBasis, report.AgentStated, report.Warnings)
		}
	})

	t.Run("a total that is not its parts is noted", func(t *testing.T) {
		s := session(t, "opencode", "run", "-m", recordedModel, "--title=review", "work")
		feed(t, s, `{"type":"step_finish","timestamp":1794232423000,"sessionID":"ses_a","part":{"id":"prt_b","type":"step-finish","tokens":{"total":9,"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}}}`)
		report, err := s.Finish()
		if err != nil {
			t.Fatal(err)
		}
		if report.AgentStated != nil || len(report.Warnings) != 2 || !strings.Contains(report.Warnings[0], "stated no cost") || !strings.Contains(report.Warnings[1], "not the sum") {
			t.Errorf("stated %+v, warnings %q", report.AgentStated, report.Warnings)
		}
	})

	t.Run("a step that cannot be read stops the reading", func(t *testing.T) {
		s := session(t, "opencode", "run", "-m", recordedModel, "work")
		feed(t, s, `{"type":"step_finish","sessionID":"ses_a","part":{"tokens":{"input":-1}}}`)
		if _, err := s.Finish(); err == nil || !strings.Contains(err.Error(), "below zero") {
			t.Errorf("err %v", err)
		}
	})

	t.Run("subagents spend outside the stream", func(t *testing.T) {
		s := session(t, "opencode", "run", "-m", recordedModel, "--title", "review", "work")
		feed(t, s, `{"type":"step_start","sessionID":"ses_a","part":{"messageID":"msg_a"}}
{"type":"tool_use","sessionID":"ses_a","part":{"tool":"task","messageID":"msg_a","state":{"status":"completed"}}}
{"type":"step_finish","sessionID":"ses_a","part":{"id":"prt_b","messageID":"msg_a","tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}},"cost":0.000006}}
{"type":"step_start","sessionID":"ses_a","part":{"messageID":"msg_b"}}`)
		report, err := s.Finish()
		if err != nil {
			t.Fatal(err)
		}
		if report.Enforcement.Grade != adapter.GradeNone || report.Unrecorded != 2 || len(report.Warnings) != 2 {
			t.Errorf("grade %s, unrecorded %d, warnings %q: a step in flight and one subagent", report.Enforcement.Grade, report.Unrecorded, report.Warnings)
		}
	})
}

// A continued session prints only the steps of this run: OpenCode subscribes
// to the session's events after the run starts. A tool part of an earlier
// message that OpenCode prints again, as pruning does, is not this run's.
func TestOpenCodeContinuesASession(t *testing.T) {
	isolate(t)
	for _, args := range [][]string{
		{"--continue"},
		{"-c"},
		{"--session", "ses_earlier"},
		{"-s", "ses_earlier"},
		{"--session=ses_earlier", "--fork"},
	} {
		argv := slices.Concat([]string{"opencode", "run", "--model", recordedModel}, args, []string{"work"})
		if _, err := (Agent{}).Plan(argv, budget); err != nil {
			t.Errorf("%q: %v", args, err)
		}
	}
	s := session(t, "opencode", "run", "--continue", "-m", recordedModel, "work")
	feed(t, s, `{"type":"tool_use","sessionID":"ses_a","part":{"tool":"task","messageID":"msg_earlier","state":{"status":"completed"}}}
{"type":"step_start","sessionID":"ses_a","part":{"messageID":"msg_now"}}
{"type":"step_finish","sessionID":"ses_a","part":{"id":"prt_b","messageID":"msg_now","tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}},"cost":0.000006}}`)
	report, err := s.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Requests) != 1 || report.Unrecorded != 0 || report.Enforcement.Grade != adapter.GradeWrapperWatchdog {
		t.Errorf("%d requests, unrecorded %d, grade %s: an earlier message's subagent is not this run's", len(report.Requests), report.Unrecorded, report.Enforcement.Grade)
	}

	explicit := []string{"opencode", "run", "--format=json", "--title", "x", "-m", recordedModel, "work"}
	if plan, err := (Agent{}).Plan(explicit, budget); err != nil || !slices.Equal(plan.Argv, explicit) {
		t.Errorf("an explicit --format json and title: %q, %v", plan.Argv, err)
	}
	for _, argv := range [][]string{
		{"opencode", "run", "--format", "default", "-m", recordedModel, "work"},
		{"opencode", "-m", recordedModel, "work"},
	} {
		if _, err := (Agent{}).Plan(argv, budget); err == nil {
			t.Errorf("%q was planned", argv)
		}
	}
}

func TestOpenCodePlanTitlesTheSession(t *testing.T) {
	isolate(t)
	plan, err := Agent{}.Plan([]string{"opencode", "run", "-m", recordedModel, "work"}, budget)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"opencode", "run", "--format", "json", "--title=", "-m", recordedModel, "work"}; !slices.Equal(plan.Argv, want) {
		t.Errorf("planned %q, want %q", plan.Argv, want)
	}
	for _, own := range [][]string{{"--title", "review"}, {"--title=review"}, {"--title="}} {
		argv := slices.Concat([]string{"opencode", "run", "--format", "json"}, own, []string{"-m", recordedModel, "work"})
		plan, err := Agent{}.Plan(argv, budget)
		if err != nil || !slices.Equal(plan.Argv, argv) {
			t.Errorf("the command's own %q: planned %q, %v", own, plan.Argv, err)
		}
	}
	says := func(argv ...string) int {
		t.Helper()
		s := session(t, argv...)
		feed(t, s, `{"type":"step_finish","sessionID":"ses_a","part":{"id":"prt_b","tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}},"cost":0.000006}}`)
		report, err := s.Finish()
		if err != nil {
			t.Fatal(err)
		}
		return len(slices.DeleteFunc(report.Warnings, func(w string) bool { return !strings.Contains(w, "--title= titles the session") }))
	}
	if got := says("opencode", "run", "-m", recordedModel, "work"); got != 1 {
		t.Errorf("a planned title is stated %d times, want once", got)
	}
	if got := says("opencode", "run", "--title", "review", "-m", recordedModel, "work"); got != 0 {
		t.Errorf("the command's own title is stated %d times, want none", got)
	}
}

// fakeOpenCode writes an opencode that prints a version, and returns it.
func fakeOpenCode(t *testing.T, output string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode")
	write(t, path, "#!/bin/sh\necho '"+output+"'\n")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenCodeReadsItsVersion(t *testing.T) {
	isolate(t)
	version := func(command string) string {
		t.Helper()
		s := session(t, command, "run", "-m", recordedModel, "work")
		feed(t, s, `{"type":"step_finish","sessionID":"ses_a","part":{"id":"prt_b","tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}},"cost":0.000006}}`)
		report, err := s.Finish()
		if err != nil {
			t.Fatal(err)
		}
		return report.AgentVersion
	}
	if got := version(fakeOpenCode(t, recordedVersion)); got != recordedVersion {
		t.Errorf("version %q, want %s", got, recordedVersion)
	}
	if got := version("opencode"); got != "" {
		t.Errorf("the stub on PATH answered version %q", got)
	}
	if got := version(fakeOpenCode(t, "not a version")); got != "" {
		t.Errorf("version %q from output that holds none", got)
	}
	if got := version("npx"); got != "" {
		t.Errorf("a launcher's version %q was read as OpenCode's", got)
	}
}

func TestOpenCodeBasisNeedsAKeyAndNoOAuth(t *testing.T) {
	isolate(t)
	store := filepath.Join(t.TempDir(), "opencode", "auth.json")
	t.Setenv("XDG_DATA_HOME", filepath.Dir(filepath.Dir(store)))
	t.Setenv("OPENCODE_AUTH_CONTENT", "")
	basis := func() focus.CostBasis {
		t.Helper()
		return costBasis("anthropic", localEnvironment())
	}
	if got := basis(); got != focus.ListEquivalent {
		t.Errorf("no key: %s", got)
	}
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	if got := basis(); got != focus.Measured {
		t.Errorf("a key and no store: %s", got)
	}
	write(t, store, `{"anthropic":{"type":"oauth","refresh":"r","access":"a","expires":1},"openai":{"type":"api","key":"k"}}`)
	if got := basis(); got != focus.ListEquivalent {
		t.Errorf("a key and an OAuth login for the provider: %s", got)
	}
	t.Setenv("OPENAI_API_KEY", "test-key")
	if got := costBasis("openai", localEnvironment()); got != focus.Measured {
		t.Errorf("a key and an API key in the store: %s", got)
	}
	t.Setenv("OPENCODE_AUTH_CONTENT", `{"openai":{"type":"oauth","refresh":"r","access":"a","expires":1}}`)
	if got := basis(); got != focus.Measured {
		t.Errorf("OPENCODE_AUTH_CONTENT replaces the file, and holds no login for anthropic: %s", got)
	}
	if got := costBasis("openai", localEnvironment()); got != focus.ListEquivalent {
		t.Errorf("an OAuth login in OPENCODE_AUTH_CONTENT: %s", got)
	}
	s := session(t, "opencode", "run", "-m", "openai/gpt-5.6-sol", "work")
	feed(t, s, `{"type":"step_finish","sessionID":"ses_a","part":{"id":"prt_b","tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}},"cost":0.000006}}`)
	if report, err := s.Finish(); err != nil || report.CostBasis != focus.ListEquivalent {
		t.Errorf("the session's basis %s, %v", report.CostBasis, err)
	}
}

func TestOpenCodeModelFromFlagThenConfig(t *testing.T) {
	root := isolate(t)
	resolve := func(flag string) model {
		t.Helper()
		got, err := resolveModel(flag, localEnvironment())
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	dir := filepath.Join(root, "service")
	write(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	if _, err := resolveModel("", localEnvironment()); err == nil || !strings.Contains(err.Error(), "pass --model") {
		t.Errorf("no model anywhere: %v", err)
	}
	if _, err := (Agent{}).Plan([]string{"opencode", "run", "work"}, budget); err == nil {
		t.Error("a run with no model was planned")
	}

	write(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "opencode", "opencode.json"), `{"model": "anthropic/claude-opus-5-5"}`)
	if got := resolve(""); got != (model{provider: "anthropic", id: "claude-opus-5-5", fromConfig: true}) {
		t.Errorf("the global config: %+v", got)
	}
	write(t, filepath.Join(root, "opencode.json"), `{"model": "anthropic/claude-sonnet-5"}`)
	write(t, filepath.Join(root, "opencode.jsonc"), `{
		// The repository's model.
		"$schema": "https://opencode.ai/config.json",
		"model": "anthropic/claude-haiku-5-5", /* the cheap one */
	}`)
	if got := resolve(""); got.id != "claude-haiku-5-5" {
		t.Errorf("the repository's opencode.jsonc over its opencode.json and the global config: %+v", got)
	}
	write(t, filepath.Join(dir, "opencode.json"), `{"model": "{env:REVIEW_MODEL}"}`)
	t.Setenv("REVIEW_MODEL", "openai/gpt-5.6-sol")
	if got := resolve(""); got != (model{provider: "openai", id: "gpt-5.6-sol", fromConfig: true}) {
		t.Errorf("the nearer directory, through {env:}: %+v", got)
	}
	t.Setenv("OPENCODE_DISABLE_PROJECT_CONFIG", "1")
	if got := resolve(""); got.id != "claude-opus-5-5" {
		t.Errorf("project config off: %+v", got)
	}
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"model":"anthropic/claude-sonnet-5",}`)
	if got := resolve(""); got.id != "claude-sonnet-5" {
		t.Errorf("OPENCODE_CONFIG_CONTENT: %+v", got)
	}
	if got := resolve("anthropic/claude-haiku-5-5"); got != (model{provider: "anthropic", id: "claude-haiku-5-5"}) {
		t.Errorf("--model over every config: %+v", got)
	}

	for flag, want := range map[string]string{
		"openrouter/anthropic/claude-haiku-5-5": "costroid prices anthropic and openai",
		"claude-haiku-5-5":                      "not provider/model",
	} {
		if _, err := resolveModel(flag, localEnvironment()); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", flag, err)
		}
	}
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"model": "{file:./model.txt}"}`)
	if _, err := resolveModel("", localEnvironment()); err == nil || !strings.Contains(err.Error(), "{file:}") {
		t.Errorf("a model through {file:}: %v", err)
	}
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"model": `)
	if _, err := resolveModel("", localEnvironment()); err == nil || !strings.Contains(err.Error(), "OPENCODE_CONFIG_CONTENT") {
		t.Errorf("a config that does not parse: %v", err)
	}

	t.Setenv("OPENCODE_CONFIG_CONTENT", "")
	s := session(t, "opencode", "run", "work")
	feed(t, s, `{"type":"step_finish","sessionID":"ses_a","part":{"id":"prt_b","tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}},"cost":0.000006}}`)
	report, err := s.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if report.Requests[0].Model != "claude-opus-5-5" || !slices.ContainsFunc(report.Warnings, func(w string) bool { return strings.Contains(w, "the model in OpenCode's config") }) {
		t.Errorf("model %s, warnings %q", report.Requests[0].Model, report.Warnings)
	}
}

func TestOpenCodeTailSettlesEachStep(t *testing.T) {
	isolate(t)
	s := session(t, "opencode", "run", "-m", recordedModel, "work")
	ctx, cancel := context.WithCancel(context.Background())
	settled := make(chan int, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Tail(ctx, func(requests []adapter.Request) { settled <- len(requests) })
	}()
	// The recording's steps are three lines each: start, a tool or text, finish.
	lines := strings.Split(strings.TrimSpace(recordedStream(t)), "\n")
	for i, want := range []int{1, 2} {
		feed(t, s, strings.Join(lines[3*i:3*(i+1)], "\n"))
		select {
		case got := <-settled:
			if got != want {
				t.Errorf("settled %d steps, want %d", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("step %d never settled", want)
		}
	}
	cancel()
	<-done
}
