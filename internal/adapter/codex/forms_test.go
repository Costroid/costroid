package codex

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

// newer is a recorded Codex release after the version the recipes pin.
const newer = "0.167.0"

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// newerRun reads the recorded 0.167.0 run, with the rollout's version
// replaced by version, under a session that names no model of its own.
func newerRun(t *testing.T, version string, edit func([]string) []string) (adapter.Report, *Session) {
	t.Helper()
	session := watchedSession("")
	session.model = UnknownModel
	for _, line := range readLines(t, filepath.Join("testdata", newer, "stopped", "stream.jsonl")) {
		if err := session.Feed([]byte(line), recorded); err != nil {
			t.Fatal(err)
		}
	}
	rollout := readLines(t, filepath.Join("testdata", newer, "stopped", "rollout.jsonl"))
	for i := range rollout {
		rollout[i] = strings.Replace(rollout[i], `"cli_version":"`+newer+`"`, `"cli_version":"`+version+`"`, 1)
	}
	if edit != nil {
		rollout = edit(rollout)
	}
	applied(session, rollout...)
	report, err := session.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return report, session
}

// listCost prices requests at the pinned catalog's list prices. The
// informational class repeats tokens a billed class counts, so it is left out.
func listCost(t *testing.T, requests []adapter.Request) decimal.Decimal {
	t.Helper()
	sheet, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	total := decimal.FromInt(0)
	for _, request := range requests {
		for _, count := range request.Tokens.Counts() {
			if count.Class == focus.CachedInputReported {
				continue
			}
			price, err := sheet.Lookup(catalog.Query{
				Provider: request.Provider, Model: request.Model, Class: count.Class,
				ServiceTier: request.ServiceTier, Region: request.Region,
				ContextInputTokens: request.ContextInputTokens, At: request.End,
			})
			if err != nil {
				t.Fatal(err)
			}
			if price.Fallback || price.PricingUnit != "1000000 Tokens" {
				t.Fatalf("%s priced %+v", count.Class, price)
			}
			total = total.Add(price.ListUnitPrice.Mul(decimal.FromInt(count.Tokens)).DivPow10(6))
		}
	}
	return total
}

func TestRolloutShapeGate(t *testing.T) {
	t.Run("a newer recorded release reads per request at the watchdog grade", func(t *testing.T) {
		report, _ := newerRun(t, newer, nil)
		if report.CostSource != costSourceRollout || report.Enforcement.Grade != adapter.GradeWrapperWatchdog {
			t.Fatalf("source %s, enforcement %+v", report.CostSource, report.Enforcement)
		}
		if report.AgentVersion != newer {
			t.Errorf("version %q", report.AgentVersion)
		}
		want := []adapter.Tokens{
			{InputUncached: 3, CacheWrite30m: 13181, Output: 51},
			{InputUncached: 3, CacheRead: 13181, CacheWrite30m: 74, Output: 5, CachedInputReported: 13181},
		}
		if len(report.Requests) != len(want) {
			t.Fatalf("%d requests", len(report.Requests))
		}
		for i, request := range report.Requests {
			if request.Tokens != want[i] || request.Grain != adapter.PerRequest || request.Model != "gpt-6.1-sol" {
				t.Errorf("request %d: %+v", i+1, request)
			}
		}
		if report.Requests[0].ID != "response-1" || report.Requests[1].ID != "response-2" {
			t.Errorf("ids %s, %s", report.Requests[0].ID, report.Requests[1].ID)
		}
		for _, warning := range report.Warnings {
			if strings.Contains(warning, "not a recorded version") {
				t.Errorf("warning %q", warning)
			}
		}
		if got := listCost(t, report.Requests).String(); got != "0.0350276" {
			t.Errorf("list cost %s USD, the recorded run's own total is 0.0350276 USD", got)
		}
	})
	t.Run("the rows sum to the stream's own total", func(t *testing.T) {
		report, session := newerRun(t, newer, nil)
		var sum adapter.Tokens
		for _, request := range report.Requests {
			sum = sum.Add(request.Tokens)
		}
		if len(session.turns) != 1 || sum != session.turns[0].Tokens {
			t.Errorf("rows %+v, stream %+v", sum, session.turns)
		}
	})
	t.Run("an unrecorded version that keeps the shape reads with a note", func(t *testing.T) {
		report, _ := newerRun(t, "9.9.9", nil)
		if report.CostSource != costSourceRollout || report.Enforcement.Grade != adapter.GradeWrapperWatchdog || len(report.Requests) != 2 {
			t.Fatalf("%+v", report)
		}
		if report.AgentVersion != "9.9.9" {
			t.Errorf("version %q", report.AgentVersion)
		}
		note := "Codex 9.9.9 is not a recorded version; the rollout file passed every check"
		if !containsWarning(report.Warnings, note) {
			t.Errorf("warnings %v", report.Warnings)
		}
	})
	t.Run("an unrecorded version that breaks the shape is refused and named", func(t *testing.T) {
		skip := func(lines []string) []string {
			for i, line := range lines {
				if strings.Contains(line, `"response_id":"response-2"`) {
					lines[i] = strings.Replace(line, `"usage":{"input_tokens":13258`, `"usage":{"input_tokens":13250`, 1)
				}
			}
			return lines
		}
		report, _ := newerRun(t, "9.9.9", skip)
		if report.CostSource != costSourceStream || report.Enforcement.Grade != adapter.GradeNone {
			t.Fatalf("source %s, enforcement %+v", report.CostSource, report.Enforcement)
		}
		if !strings.Contains(report.Enforcement.Note, "did not move by its last request") {
			t.Errorf("note %q", report.Enforcement.Note)
		}
		if !containsWarning(report.Warnings, "Codex 9.9.9 is not a recorded version, and its rollout file failed a check") {
			t.Errorf("warnings %v", report.Warnings)
		}
	})
	t.Run("usage before a session_meta is refused", func(t *testing.T) {
		session := applied(watchedSession(""), countLine)
		if !strings.Contains(session.log.problem, "names no Codex version") {
			t.Errorf("problem %q", session.log.problem)
		}
	})
}

func containsWarning(warnings []string, part string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, part) {
			return true
		}
	}
	return false
}

func TestModelFromArgsForms(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.toml")
	text := "model = \"gpt-top\"\n[profiles.fast]\nmodel = 'gpt-fast'\n[profiles.slow]\nmodel_reasoning_effort = \"high\"\n"
	if err := os.WriteFile(config, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"-m", "gpt-m"}, "gpt-m"},
		{[]string{"--model", "gpt-m"}, "gpt-m"},
		{[]string{"--model=gpt-m"}, "gpt-m"},
		{[]string{"-m=gpt-m"}, "gpt-m"},
		{[]string{"-c", `model="gpt-c"`}, "gpt-c"},
		{[]string{"-c", `model = "gpt-c"`}, "gpt-c"},
		{[]string{"-c", `model='gpt-c'`}, "gpt-c"},
		{[]string{"-c", `model = 'gpt-c'`}, "gpt-c"},
		{[]string{"-c", "model=gpt-c"}, "gpt-c"},
		{[]string{"--config", `model = "gpt-c"`}, "gpt-c"},
		{[]string{"--config=model=gpt-c"}, "gpt-c"},
		{[]string{`--config=model="gpt-c"`}, "gpt-c"},
		{[]string{"--profile", "fast"}, "gpt-fast"},
		{[]string{"-p", "fast"}, "gpt-fast"},
		{[]string{"--profile=fast"}, "gpt-fast"},
		{[]string{"-c", `profile = "fast"`}, "gpt-fast"},
		{[]string{"-p", "slow"}, "gpt-top"},
		{[]string{"-p", "fast", "-c", "model=gpt-c"}, "gpt-fast"},
		{[]string{"-m", "gpt-m", "-p", "fast"}, "gpt-m"},
		{[]string{"-c", "model=gpt-first", "-c", "model=gpt-last"}, "gpt-last"},
		{[]string{}, "gpt-top"},
		{[]string{"--", "-m", "gpt-m"}, "gpt-top"},
	}
	for _, tc := range cases {
		argv := append([]string{"codex", "exec"}, tc.argv...)
		argv = append(argv, "hi")
		if len(tc.argv) > 0 && tc.argv[0] == "--" {
			argv = append([]string{"codex", "exec"}, tc.argv...)
		}
		if got := modelFrom(argv, config); got != tc.want {
			t.Errorf("%q: %q, want %q", argv, got, tc.want)
		}
	}
}

func TestModelFromConfigForms(t *testing.T) {
	cases := []struct {
		name, config, want string
	}{
		{"basic string", `model = "gpt-a"`, "gpt-a"},
		{"literal string", `model = 'gpt-a'`, "gpt-a"},
		{"no spaces", `model="gpt-a"`, "gpt-a"},
		{"wide spaces", "  model   =   \"gpt-a\"  ", "gpt-a"},
		{"trailing comment", `model = "gpt-a" # the default`, "gpt-a"},
		{"quoted key", `"model" = "gpt-a"`, "gpt-a"},
		{"escape", `model = "gpt-a"`, "gpt-a"},
		{"profile table", "profile = \"fast\"\nmodel = \"gpt-a\"\n[profiles.fast]\nmodel = \"gpt-fast\"", "gpt-fast"},
		{"quoted profile table", "profile = 'fast'\n[profiles.\"fast\"]\nmodel = \"gpt-fast\"", "gpt-fast"},
		{"dotted profile key", "profile = \"fast\"\nprofiles.fast.model = \"gpt-fast\"", "gpt-fast"},
		{"profile with no model", "profile = \"fast\"\nmodel = \"gpt-a\"\n[profiles.fast]\nsandbox = \"x\"", "gpt-a"},
		{"model under another table", "[otel]\nmodel = \"no\"", UnknownModel},
		{"model under an array of tables", "[[mcp]]\nmodel = \"no\"", UnknownModel},
		{"commented out", `# model = "no"`, UnknownModel},
	}
	for _, tc := range cases {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(tc.config+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := modelFrom([]string{"codex", "exec", "hi"}, path); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := modelFrom([]string{"codex", "exec", "hi"}, filepath.Join(t.TempDir(), "absent.toml")); got != UnknownModel {
		t.Errorf("absent config: %q", got)
	}
}

func TestRolloutModelStandsAboveTheCommand(t *testing.T) {
	session := watchedSession("")
	session.model = "gpt-from-the-command"
	lines := []string{
		`{"type":"thread.started","thread_id":"` + thread + `"}`,
		`{"type":"turn.started"}`,
		`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":7}}`,
	}
	for _, line := range lines {
		if err := session.Feed([]byte(line), recorded); err != nil {
			t.Fatal(err)
		}
	}
	// A rollout whose totals disagree with the stream leaves the turn rows,
	// and the turn_context it read still names the model.
	disagrees := strings.Replace(countLine, `"output_tokens":7},"last`, `"output_tokens":8},"last`, 1)
	disagrees = strings.Replace(disagrees, `"output_tokens":7}}}}`, `"output_tokens":8}}}}`, 1)
	applied(session, metaLine, contextLine, disagrees)
	report, err := session.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if report.CostSource != costSourceStream || len(report.Requests) != 1 || report.Requests[0].Model != "gpt-5.6-sol" {
		t.Errorf("%+v", report)
	}
}

func TestResumeGuardArgvForms(t *testing.T) {
	refused := [][]string{
		{"codex", "exec", "resume", "--last", "go"},
		{"codex", "exec", "--json", "resume", "--last", "go"},
		{"codex", "exec", "--skip-git-repo-check", "resume", "abc", "go"},
		{"codex", "exec", "-m", "gpt-5", "resume", "abc", "go"},
		{"codex", "exec", "--model=gpt-5", "resume", "--last"},
		{"codex", "exec", "-c", "model=x", "--full-auto", "resume", "abc"},
		{"codex", "exec", "-C", "dir", "-s", "read-only", "resume", "abc"},
		{"codex", "exec", "--", "resume"},
	}
	for _, argv := range refused {
		_, err := Agent{}.Plan(argv, decimal.MustParse("5"))
		if err == nil || !strings.Contains(err.Error(), "exec resume") {
			t.Errorf("%q: %v", argv, err)
		}
	}
	accepted := [][]string{
		{"codex", "exec", "resume the work"},
		{"codex", "exec", "-m", "resume", "hi"},
		{"codex", "exec", "--json", "hi", "resume"},
		{"codex", "exec", "-C", "resume", "hi"},
		{"codex", "exec", "-", "resume"},
	}
	for _, argv := range accepted {
		if _, err := (Agent{}).Plan(argv, decimal.MustParse("5")); err != nil {
			t.Errorf("%q: %v", argv, err)
		}
	}
}

func TestRolloutCreatedBeforeTheRunIsStated(t *testing.T) {
	assertStated := func(t *testing.T, report adapter.Report) {
		t.Helper()
		if report.Enforcement.Grade != adapter.GradeNone || !strings.Contains(report.Enforcement.Note, predatesProblem) {
			t.Errorf("enforcement %+v", report.Enforcement)
		}
		if !containsWarning(report.Warnings, "this run's own total is unknown") {
			t.Errorf("warnings %v", report.Warnings)
		}
		if len(report.Requests) == 0 {
			t.Fatal("no rows")
		}
		for _, request := range report.Requests {
			if request.Note != predatesNote {
				t.Errorf("note %q", request.Note)
			}
		}
	}
	streamed := func(t *testing.T, session *Session) {
		t.Helper()
		for _, line := range []string{
			`{"type":"thread.started","thread_id":"` + thread + `"}`,
			`{"type":"turn.started"}`,
			`{"type":"turn.completed","usage":{"input_tokens":200,"cached_input_tokens":80,"output_tokens":14}}`,
		} {
			if err := session.Feed([]byte(line), recorded); err != nil {
				t.Fatal(err)
			}
		}
	}
	task := `{"timestamp":"2026-11-01T11:17:09.000Z","type":"event_msg","payload":{"type":"task_started"}}`
	second := strings.ReplaceAll(strings.Replace(countLine, `"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":7}`, `"total_token_usage":{"input_tokens":200,"cached_input_tokens":80,"output_tokens":14}`, 1), "11:17:13", "11:18:13")

	t.Run("the file holds a task the stream did not start", func(t *testing.T) {
		session := watchedSession("")
		streamed(t, session)
		applied(session, metaLine, task, countLine, task, second)
		report, err := session.Finish()
		if err != nil {
			t.Fatal(err)
		}
		assertStated(t, report)
	})
	t.Run("the file was last written before the run started", func(t *testing.T) {
		home := t.TempDir()
		directory := filepath.Join(home, "sessions", "2026", "11", "01")
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, "rollout-2026-11-01T11-17-09-"+thread+".jsonl")
		data := strings.Join([]string{metaLine, task, countLine, second}, "\n") + "\n"
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		before := time.Now().Add(-time.Hour)
		if err := os.Chtimes(path, before, before); err != nil {
			t.Fatal(err)
		}
		session := watchedSession(home)
		streamed(t, session)
		var file rolloutFile
		defer file.close()
		session.follow(&file)
		session.RunWindow(time.Now(), time.Now())
		report, err := session.Finish()
		if err != nil {
			t.Fatal(err)
		}
		assertStated(t, report)
	})
	t.Run("a file written during the run is not stated", func(t *testing.T) {
		home := t.TempDir()
		directory := filepath.Join(home, "sessions", "2026", "11", "01")
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		path := filepath.Join(directory, "rollout-2026-11-01T11-17-09-"+thread+".jsonl")
		data := strings.Join([]string{metaLine, task, strings.Replace(second, `"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":7}`, `"last_token_usage":{"input_tokens":200,"cached_input_tokens":80,"output_tokens":14}`, 1)}, "\n") + "\n"
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		session := watchedSession(home)
		streamed(t, session)
		var file rolloutFile
		defer file.close()
		session.follow(&file)
		session.RunWindow(start, time.Now())
		report, err := session.Finish()
		if err != nil {
			t.Fatal(err)
		}
		if report.Enforcement.Grade != adapter.GradeWrapperWatchdog || containsWarning(report.Warnings, "created before the run") {
			t.Errorf("%+v", report)
		}
	})
}

func TestObjectMessageLineKeepsTurn(t *testing.T) {
	session := replaySession("gpt-5.6-sol")
	lines := []string{
		`{"type":"thread.started","thread_id":"t"}`,
		`{"type":"turn.started"}`,
		`{"type":"error","message":{"code":"rate_limit","text":"slow down"}}`,
		`{"type":"item.completed","item":{"type":"agent_message"},"message":["a",1]}`,
		`{"type":"turn.completed","message":{"text":"done"},"usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":7}}`,
	}
	for _, line := range lines {
		if err := session.Feed([]byte(line), recorded); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
	}
	report, err := session.Finish()
	if err != nil {
		t.Fatal(err)
	}
	want := adapter.Tokens{InputUncached: 60, CacheRead: 40, Output: 7, CachedInputReported: 40}
	if len(report.Requests) != 1 || report.Requests[0].Tokens != want {
		t.Fatalf("%+v", report.Requests)
	}
}

func TestLineOfAnUnknownShapeIsSkippedAndCounted(t *testing.T) {
	session := replaySession("m")
	for _, line := range []string{`{"type":"thread.started","thread_id":"t"}`, `{"type":7}`, `[1,2]`, `"text"`} {
		if err := session.Feed([]byte(line), recorded); err != nil {
			t.Fatal(err)
		}
	}
	report, err := session.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if !containsWarning(report.Warnings, "3 stream lines were skipped") {
		t.Errorf("warnings %v", report.Warnings)
	}
}

func TestUnreadableTurnStopsTheReading(t *testing.T) {
	session := replaySession("m")
	for _, line := range []string{`{"type":"thread.started","thread_id":"t"}`, `{"type":"turn.completed","usage":{"input_tokens":"many"}}`} {
		if err := session.Feed([]byte(line), recorded); err != nil {
			t.Fatal(err)
		}
	}
	_, err := session.Finish()
	if err == nil || !strings.Contains(err.Error(), "turn.completed could not be read") {
		t.Errorf("err %v", err)
	}
}

func TestNoThreadNamesTheLikelyCauses(t *testing.T) {
	_, err := replaySession("m").Finish()
	want := "codex: the stream held no thread.started event. Codex did not start a thread, often because it is not signed in or the version is older than 0.164.0-alpha.11.1"
	if err == nil || err.Error() != want {
		t.Errorf("err %v", err)
	}
}

func TestCodexRefusesNegativeTokens(t *testing.T) {
	t.Run("any negative field", func(t *testing.T) {
		for _, u := range []usage{
			{InputTokens: -1},
			{InputTokens: 10, CachedInput: -1},
			{InputTokens: 10, CacheWriteInput: -1},
			{OutputTokens: -1},
			{ReasoningOutput: -1},
		} {
			if tokens, err := u.tokens(); err == nil {
				t.Errorf("%+v gave %+v", u, tokens)
			}
		}
	})
	t.Run("input below its parts never wraps", func(t *testing.T) {
		for _, u := range []usage{
			{InputTokens: 0, CachedInput: 1 << 62, CacheWriteInput: math.MaxInt64},
			{InputTokens: 10, CachedInput: math.MaxInt64},
			{InputTokens: math.MaxInt64, CachedInput: 1, CacheWriteInput: math.MaxInt64},
		} {
			if tokens, err := u.tokens(); err == nil {
				t.Errorf("%+v gave %+v", u, tokens)
			}
		}
	})
	t.Run("plus is checked", func(t *testing.T) {
		if sum, ok := (usage{OutputTokens: math.MaxInt64}).plus(usage{OutputTokens: 1}); ok {
			t.Errorf("overflow gave %+v", sum)
		}
		if sum, ok := (usage{InputTokens: 2}).plus(usage{InputTokens: 3}); !ok || sum.InputTokens != 5 {
			t.Errorf("%+v %v", sum, ok)
		}
	})
	t.Run("a stream total that falls refuses the run", func(t *testing.T) {
		session := replaySession("m")
		for _, line := range []string{
			`{"type":"thread.started","thread_id":"t"}`,
			`{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":20}}`,
			`{"type":"turn.completed","usage":{"input_tokens":200,"output_tokens":10}}`,
		} {
			if err := session.Feed([]byte(line), recorded); err != nil {
				t.Fatal(err)
			}
		}
		_, err := session.Finish()
		if err == nil || !strings.Contains(err.Error(), "output_tokens is -10, below zero") {
			t.Errorf("err %v", err)
		}
	})
	t.Run("a negative stream total refuses the run", func(t *testing.T) {
		session := replaySession("m")
		for _, line := range []string{
			`{"type":"thread.started","thread_id":"t"}`,
			`{"type":"turn.completed","usage":{"input_tokens":-9223372036854775808,"output_tokens":1}}`,
		} {
			if err := session.Feed([]byte(line), recorded); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := session.Finish(); err == nil {
			t.Error("a negative running total was read")
		}
	})
	t.Run("a negative rollout count stops the rollout reading", func(t *testing.T) {
		negative := strings.Replace(countLine, `"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":7}`, `"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":-7}`, 1)
		negative = strings.Replace(negative, `"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":7}`, `"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":-7}`, 1)
		session := applied(watchedSession(""), metaLine, negative)
		if !strings.Contains(session.log.problem, "below zero") || len(session.log.requests) != 0 {
			t.Errorf("%+v", session.log)
		}
	})
}
