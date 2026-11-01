package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

const version = "0.164.0-alpha.11.1"

var recorded = time.Date(2026, 11, 1, 11, 17, 0, 0, time.UTC)

func replaySession(model string) *Session {
	return &Session{model: model, costBasis: focus.ListEquivalent}
}

func watchedSession(home string) *Session {
	return &Session{
		model:     "gpt-5.6-sol",
		costBasis: focus.ListEquivalent,
		home:      home,
		planned:   adapter.Enforcement{Grade: adapter.GradeWrapperWatchdog, Note: watchedNote, Watchdog: true},
	}
}

func replay(t *testing.T, scenario string) *Session {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", version, scenario, "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	session := replaySession("gpt-5.6-sol")
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if err := session.Feed([]byte(line), recorded); err != nil {
			t.Fatal(err)
		}
	}
	return session
}

func turns(t *testing.T, session *Session) []adapter.Request {
	t.Helper()
	report, err := session.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return report.Requests
}

func TestInputIncludesCachedTokensAndCacheWrites(t *testing.T) {
	report, err := replay(t, "exec").Finish()
	if err != nil {
		t.Fatal(err)
	}
	want := adapter.Tokens{InputUncached: 5875, CacheRead: 28032, Output: 111, CachedInputReported: 28032}
	if len(report.Requests) != 1 || report.Requests[0].Tokens != want {
		t.Fatalf("%+v", report.Requests)
	}
	got := report.Requests[0].Tokens
	if got.InputUncached+got.CacheRead+got.CacheWrite30m != 33907 {
		t.Errorf("billed input classes do not add back to the stream's 33907")
	}
}

func TestCacheWritesLeaveUncachedInputAndKeepTheCatalogClass(t *testing.T) {
	got, err := usage{InputTokens: 1000, CachedInput: 300, CacheWriteInput: 200, OutputTokens: 50, ReasoningOutput: 20}.tokens()
	if err != nil {
		t.Fatal(err)
	}
	want := adapter.Tokens{InputUncached: 500, CacheRead: 300, CacheWrite30m: 200, Output: 50, ReasoningOutput: 20, CachedInputReported: 300}
	if got != want {
		t.Errorf("%+v", got)
	}
	var classes []focus.TokenClass
	for _, count := range got.Counts() {
		classes = append(classes, count.Class)
	}
	if !slices.Contains(classes, focus.CacheWrite30m) {
		t.Errorf("classes %v", classes)
	}
}

func TestACacheWriteOnTheStreamIsAssumedThirtyMinutes(t *testing.T) {
	session := replaySession("gpt-5.6-sol")
	lines := []string{
		`{"type":"thread.started","thread_id":"t"}`,
		`{"type":"turn.completed","usage":{"input_tokens":1000,"cached_input_tokens":300,"cache_write_input_tokens":200,"output_tokens":50}}`,
	}
	for _, line := range lines {
		if err := session.Feed([]byte(line), recorded); err != nil {
			t.Fatal(err)
		}
	}
	if got := turns(t, session)[0]; got.AssumedWrite != focus.CacheWrite30m || got.Tokens.CacheWrite30m != 200 {
		t.Errorf("%+v", got)
	}
}

func TestATurnWithNoCacheWriteAssumesNothing(t *testing.T) {
	session := replaySession("gpt-5.6-sol")
	lines := []string{
		`{"type":"thread.started","thread_id":"t"}`,
		`{"type":"turn.completed","usage":{"input_tokens":1000,"cached_input_tokens":300,"output_tokens":50}}`,
	}
	for _, line := range lines {
		if err := session.Feed([]byte(line), recorded); err != nil {
			t.Fatal(err)
		}
	}
	if got := turns(t, session)[0].AssumedWrite; got != "" {
		t.Errorf("assumed %s", got)
	}
}

func TestAbsentCacheWriteFieldReadsAsZero(t *testing.T) {
	session := replaySession("gpt-5.6-sol")
	lines := []string{
		`{"type":"thread.started","thread_id":"t"}`,
		`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":7}}`,
	}
	for _, line := range lines {
		if err := session.Feed([]byte(line), recorded); err != nil {
			t.Fatal(err)
		}
	}
	if got := turns(t, session)[0].Tokens; got.InputUncached != 60 || got.CacheWrite30m != 0 {
		t.Errorf("%+v", got)
	}
}

func TestInputBelowItsPartsIsAnError(t *testing.T) {
	if _, err := (usage{InputTokens: 10, CachedInput: 20}).tokens(); err == nil {
		t.Error("negative uncached input was accepted")
	}
}

func TestTurnCompletedIsARunningTotalSoTurnsAreDifferences(t *testing.T) {
	session := replaySession("gpt-5.6-sol")
	lines := []string{
		`{"type":"thread.started","thread_id":"t"}`,
		`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":0,"output_tokens":10}}`,
		`{"type":"turn.completed","usage":{"input_tokens":250,"cached_input_tokens":80,"output_tokens":30}}`,
	}
	for _, line := range lines {
		if err := session.Feed([]byte(line), recorded); err != nil {
			t.Fatal(err)
		}
	}
	second := turns(t, session)[1]
	if second.Tokens.InputUncached != 70 || second.Tokens.CacheRead != 80 || second.Tokens.Output != 20 {
		t.Errorf("%+v", second.Tokens)
	}
	if second.ID != "t:turn:2" || second.Grain != adapter.PerTurn {
		t.Errorf("%+v", second)
	}
}

func TestResumeRepeatsEarlierSpendOnTheStream(t *testing.T) {
	type counts struct {
		Input int64 `json:"input_tokens"`
	}
	type rollout struct {
		Total counts `json:"total_token_usage"`
		Last  counts `json:"last_token_usage"`
	}
	data, err := os.ReadFile(filepath.Join("testdata", version, "resume", "rollout-token-counts.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var last rollout
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if err := json.Unmarshal([]byte(line), &last); err != nil {
			t.Fatal(err)
		}
	}
	resumed := turns(t, replay(t, "resume"))[0].Tokens
	exec := turns(t, replay(t, "exec"))[0].Tokens
	streamInput := resumed.InputUncached + resumed.CacheRead
	if streamInput != last.Total.Input {
		t.Errorf("resume reported %d, the rollout's thread total is %d", streamInput, last.Total.Input)
	}
	if streamInput != exec.InputUncached+exec.CacheRead+last.Last.Input {
		t.Errorf("resume's %d is not the first run's input plus its own %d", streamInput, last.Last.Input)
	}
}

func TestPlanAddsJSONAndTheWatchdogGradeWithItsLimit(t *testing.T) {
	plan, err := Agent{}.Plan([]string{"codex", "exec", "hello"}, decimal.MustParse("5"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Argv, []string{"codex", "exec", "--json", "hello"}) {
		t.Errorf("argv %v", plan.Argv)
	}
	if plan.Enforcement.Grade != adapter.GradeWrapperWatchdog || !plan.Enforcement.Watchdog {
		t.Errorf("%+v", plan.Enforcement)
	}
	if !strings.Contains(plan.Enforcement.Note, "in flight") {
		t.Errorf("note %q", plan.Enforcement.Note)
	}
}

func TestEphemeralPlanHasNothingToWatchAndSaysSo(t *testing.T) {
	plan, err := Agent{}.Plan([]string{"codex", "exec", "--ephemeral", "hello"}, decimal.MustParse("5"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Enforcement.Grade != adapter.GradeNone || plan.Enforcement.Watchdog {
		t.Errorf("%+v", plan.Enforcement)
	}
	if !strings.Contains(plan.Enforcement.Note, "--ephemeral writes no rollout file") {
		t.Errorf("note %q", plan.Enforcement.Note)
	}
}

func TestPlanRefusesWhatItCannotCountOrWatch(t *testing.T) {
	for _, argv := range [][]string{{"codex", "hello"}, {"codex", "exec", "resume", "id", "hello"}} {
		if _, err := (Agent{}).Plan(argv, decimal.MustParse("5")); err == nil {
			t.Errorf("%v was accepted", argv)
		}
	}
}

func TestModelComesFromTheCommandThenTheConfig(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(config, []byte("# comment\nmodel = \"gpt-5.6-sol\"\n[otel]\nmodel = \"no\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"gpt-6-sol":  {"codex", "exec", "-m", "gpt-6-sol", "hi"},
		"gpt-6-luna": {"codex", "exec", "--model=gpt-6-luna", "hi"},
		"gpt-5.5":    {"codex", "exec", "-c", `model="gpt-5.5"`, "hi"},
	}
	for want, argv := range cases {
		if got := modelFromArgs(argv); got != want {
			t.Errorf("%v: %q", argv, got)
		}
	}
	if got := modelFromConfig(config); got != "gpt-5.6-sol" {
		t.Errorf("config: %q", got)
	}
}

func TestConfigWithoutATopLevelModelNamesNone(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(config, []byte("[profiles.x]\nmodel = \"no\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := modelFromConfig(config); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestCostBasisIsMeasuredOnlyOnAnAPIKey(t *testing.T) {
	if costBasisOf("") != focus.ListEquivalent || costBasisOf("set") != focus.Measured {
		t.Error("basis does not follow the key")
	}
}

func TestStreamWithNoThreadIsAnError(t *testing.T) {
	if _, err := replaySession("m").Finish(); err == nil {
		t.Error("an empty stream reported nothing")
	}
}

func TestFailedTurnIsNamedInTheReport(t *testing.T) {
	session := replaySession("m")
	for _, line := range []string{`{"type":"thread.started","thread_id":"t"}`, `{"type":"turn.failed"}`} {
		if err := session.Feed([]byte(line), recorded); err != nil {
			t.Fatal(err)
		}
	}
	report, err := session.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Warnings) != 2 {
		t.Errorf("warnings %v", report.Warnings)
	}
}

func TestFixturesCarryNoPathsNamesOrAccountIDs(t *testing.T) {
	forbidden := regexp.MustCompile(`/home/|/tmp/|/Users/|creator_|user-|resp_|plan_type`)
	err := filepath.WalkDir("testdata", func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if match := forbidden.Find(data); match != nil {
			t.Errorf("%s carries %q", path, match)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
