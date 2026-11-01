package run

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	catalogdata "github.com/Costroid/costroid/catalog"
	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

const (
	fakeMode        = "COSTROID_FAKE_AGENT"
	fakeLog         = "COSTROID_FAKE_LOG"
	fakeRollout     = "COSTROID_FAKE_ROLLOUT"
	fakeVersion     = "COSTROID_FAKE_VERSION"
	fakeStreamInput = "COSTROID_FAKE_STREAM_INPUT"
	fakeCount       = 20
	fakeThread      = "00000000-0000-4000-8000-0000000000aa"
	claudeData      = "../adapter/claudecode/testdata/2.1.316"
	codexData       = "../adapter/codex/testdata/0.164.0-alpha.11.1"
	recordedCodex   = "0.164.0-alpha.11.1"
	recordedThread  = "00000000-0000-4000-8000-000000000004"
	requestPause    = 200 * time.Millisecond
	requestInput    = 250000
	stubbornEnv     = "stubborn"
)

// TestMain turns the test binary into the fake agent when the harness runs it
// under the name of a real one.
func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeMode); mode != "" {
		fakeAgent(mode)
		return
	}
	os.Exit(m.Run())
}

func fakeAgent(mode string) {
	switch filepath.Base(os.Args[0]) {
	case "codex":
		fakeCodex(mode)
	case "claude":
		fakeClaude()
	default:
		os.Exit(99)
	}
}

// fakeCodex stands in for `codex exec`. Each mode logs how far it got, so a
// test can see where the run stopped.
func fakeCodex(mode string) {
	if slices.Contains(os.Args[1:], "--version") {
		fmt.Println("codex-cli " + recordedCodex)
		return
	}
	if mode == stubbornEnv {
		signal.Ignore(syscall.SIGTERM)
	}
	logFile, err := os.OpenFile(os.Getenv(fakeLog), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		os.Exit(98)
	}
	defer logFile.Close()
	switch mode {
	case "stream":
		fakeStreamOnly(logFile)
	case "replay":
		fakeReplay(logFile)
	case "stdin":
		data, err := io.ReadAll(os.Stdin)
		fmt.Fprintf(logFile, "%d %v\n", len(data), err)
		fmt.Printf(`{"type":"thread.started","thread_id":%q}`+"\n", fakeThread)
	default:
		fakeRequests(logFile)
	}
}

// fakeStreamOnly writes no rollout file, as an ephemeral run does.
func fakeStreamOnly(log io.Writer) {
	fmt.Printf(`{"type":"thread.started","thread_id":%q}`+"\n", fakeThread)
	for turn := 1; turn <= fakeCount; turn++ {
		fmt.Fprintln(log, turn)
		fmt.Println(`{"type":"turn.started"}`)
		fmt.Printf(`{"type":"turn.completed","usage":{"input_tokens":%d,"cached_input_tokens":0,"output_tokens":0}}`+"\n", turn*requestInput)
	}
}

func openFakeRollout(thread string) *os.File {
	directory := filepath.Join(os.Getenv("CODEX_HOME"), "sessions", "2026", "11", "01")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		os.Exit(96)
	}
	file, err := os.Create(filepath.Join(directory, "rollout-2026-11-01T14-17-09-"+thread+".jsonl"))
	if err != nil {
		os.Exit(96)
	}
	return file
}

// fakeReplay writes a recorded rollout line by line while one turn is open,
// pausing after each usage line so the tail can see it. The turn's closing
// event comes last, as on a real run.
func fakeReplay(log io.Writer) {
	data, err := os.ReadFile(os.Getenv(fakeRollout))
	if err != nil {
		os.Exit(97)
	}
	rollout := openFakeRollout(fakeThread)
	defer rollout.Close()
	fmt.Printf(`{"type":"thread.started","thread_id":%q}`+"\n", fakeThread)
	fmt.Println(`{"type":"turn.started"}`)
	for _, line := range strings.SplitAfter(string(data), "\n") {
		line = strings.ReplaceAll(line, recordedThread, fakeThread)
		if version := os.Getenv(fakeVersion); version != "" {
			line = strings.ReplaceAll(line, recordedCodex, version)
		}
		rollout.WriteString(line)
		if strings.Contains(line, `"token_count"`) {
			fmt.Fprintln(log, "token_count")
			time.Sleep(requestPause)
		}
	}
	input := os.Getenv(fakeStreamInput)
	if input == "" {
		input = "33907"
	}
	fmt.Fprintln(log, "turn.completed")
	fmt.Printf(`{"type":"turn.completed","usage":{"input_tokens":%s,"cached_input_tokens":28032,"output_tokens":111}}`+"\n", input)
}

// fakeRequests makes many model requests inside one turn, the way one prompt
// does, and reports usage on the stream only when the turn closes.
func fakeRequests(log io.Writer) {
	rollout := openFakeRollout(fakeThread)
	defer rollout.Close()
	fmt.Printf(`{"type":"thread.started","thread_id":%q}`+"\n", fakeThread)
	fmt.Println(`{"type":"turn.started"}`)
	fmt.Fprintf(rollout, `{"timestamp":"2026-11-01T11:17:09.000Z","type":"session_meta","payload":{"id":%q,"cli_version":%q}}`+"\n", fakeThread, recordedCodex)
	fmt.Fprintln(rollout, `{"timestamp":"2026-11-01T11:17:09.000Z","type":"turn_context","payload":{"model":"gpt-5.6-sol"}}`)
	for request := 1; request <= fakeCount; request++ {
		fmt.Fprintln(log, request)
		fmt.Fprintf(rollout, `{"timestamp":"2026-11-01T11:17:10.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d},"last_token_usage":{"input_tokens":%d}}}}`+"\n", request*requestInput, requestInput)
		time.Sleep(requestPause)
	}
	fmt.Fprintln(log, "turn.completed")
	fmt.Printf(`{"type":"turn.completed","usage":{"input_tokens":%d,"cached_input_tokens":0,"output_tokens":0}}`+"\n", fakeCount*requestInput)
}

// fakeClaude records its argv and replays a recorded run, session files included.
func fakeClaude() {
	logFile, err := os.Create(os.Getenv(fakeLog))
	if err != nil {
		os.Exit(98)
	}
	defer logFile.Close()
	fmt.Fprintln(logFile, strings.Join(os.Args[1:], "\n"))
	data, err := os.ReadFile(os.Getenv("COSTROID_FAKE_STREAM"))
	if err != nil {
		os.Exit(97)
	}
	os.Stdout.Write(data)
}

// agentNamed links the test binary under an agent's name.
func agentNamed(t *testing.T, name string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.Symlink(self, path); err != nil {
		t.Fatal(err)
	}
	return path
}

func pinned(t *testing.T) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type outcome struct {
	result Result
	rows   []map[string]any
	stdout string
	stderr string
	err    error
}

func runAgent(t *testing.T, argv []string, budget string, c *catalog.Catalog) outcome {
	t.Helper()
	var rows, stdout, stderr bytes.Buffer
	result, err := Run(context.Background(), Options{
		Argv:    argv,
		Budget:  decimal.MustParse(budget),
		Rows:    &rows,
		Stdin:   strings.NewReader(""),
		Stdout:  &stdout,
		Stderr:  &stderr,
		Catalog: c,
		Grace:   200 * time.Millisecond,
	})
	got := outcome{result: result, stdout: stdout.String(), stderr: stderr.String(), err: err}
	scanner := bufio.NewScanner(&rows)
	scanner.Buffer(nil, 1<<20)
	for scanner.Scan() {
		var row map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		got.rows = append(got.rows, row)
	}
	return got
}

func tagOf(row map[string]any, key string) string {
	tags, _ := row["Tags"].(map[string]any)
	value, _ := tags[key].(string)
	return value
}

func logged(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(data))
}

// codexRun runs a fake Codex in a mode and returns its outcome and its log.
func codexRun(t *testing.T, mode, budget string, extra ...string) (outcome, string) {
	t.Helper()
	log := filepath.Join(t.TempDir(), "progress")
	t.Setenv(fakeMode, mode)
	t.Setenv(fakeLog, log)
	t.Setenv(fakeRollout, mustAbs(t, filepath.Join(codexData, "exec", "rollout.jsonl")))
	t.Setenv("CODEX_HOME", t.TempDir())
	argv := append([]string{agentNamed(t, "codex"), "exec", "-m", "gpt-5.6-sol"}, extra...)
	return runAgent(t, append(argv, "work"), budget, pinned(t)), log
}

func TestRunStopsBeforeItsFinalTurnEventWhenTheRolloutCrossesTheCap(t *testing.T) {
	got, log := codexRun(t, "replay", "0.02")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if !got.result.Stopped || got.result.AgentExit != 128+int(syscall.SIGTERM) {
		t.Fatalf("stopped %v, exit %d", got.result.Stopped, got.result.AgentExit)
	}
	if strings.Contains(got.stdout, "turn.completed") {
		t.Error("the turn closed: the stop came after the agent had finished")
	}
	if progress := logged(t, log); len(progress) != 1 {
		t.Errorf("progress %v, want the run stopped after its first of two token_count events", progress)
	}
	// Request one of the recording, from the sheet's gpt-5.6-sol rows per million:
	// 5624 × 4.00 + 11264 × 0.40 + 106 × 20.00.
	if err := checkListTotal(got.result.ListTotal, "0.0291216"); err != nil {
		t.Error(err)
	}
	if len(got.result.Report.Warnings) != 1 || !strings.Contains(got.result.Report.Warnings[0], "request in flight is not counted") {
		t.Errorf("warnings %v", got.result.Report.Warnings)
	}
	for _, row := range got.rows {
		if grade := tagOf(row, focus.TagEnforcementGrade); grade != "wrapper-watchdog" {
			t.Errorf("grade %s", grade)
		}
	}
}

func TestWatchdogStopsARunOfManyRequestsInsideOneTurn(t *testing.T) {
	got, log := codexRun(t, "requests", "2")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if !got.result.Stopped {
		t.Fatal("the run was not stopped")
	}
	// 250000 uncached input tokens at 4.00 USD per million is 1 USD a request.
	if got.result.ListTotal.Cmp(decimal.MustParse("2")) < 0 || got.result.ListTotal.Cmp(decimal.MustParse("4")) > 0 {
		t.Errorf("list total %s", got.result.ListTotal)
	}
	if progress := logged(t, log); len(progress) >= fakeCount {
		t.Errorf("the agent ran all %d requests", len(progress))
	}
}

func TestWatchdogKillsAnAgentThatIgnoresTheStop(t *testing.T) {
	started := time.Now()
	got, _ := codexRun(t, stubbornEnv, "1")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if !got.result.Stopped || got.result.AgentExit != 128+int(syscall.SIGKILL) {
		t.Errorf("stopped %v, exit %d", got.result.Stopped, got.result.AgentExit)
	}
	if elapsed := time.Since(started); elapsed > fakeCount*requestPause/2 {
		t.Errorf("the stop took %s", elapsed)
	}
}

func TestWatchedRowsStateTheLimitOfTheGrade(t *testing.T) {
	got, _ := codexRun(t, "requests", "1")
	if got.err != nil || len(got.rows) == 0 {
		t.Fatalf("%v, %d rows", got.err, len(got.rows))
	}
	for _, row := range got.rows {
		if grade := tagOf(row, focus.TagEnforcementGrade); grade != "wrapper-watchdog" {
			t.Errorf("grade %s", grade)
		}
		if text, _ := row["ChargeDescription"].(string); !strings.Contains(text, "requests in flight at the stop can pass it") {
			t.Errorf("description %q", text)
		}
		if row["x_CostroidOvershootBound"] != nil {
			t.Errorf("bound %v: a watchdog has no numeric bound", row["x_CostroidOvershootBound"])
		}
		if tagOf(row, focus.TagAttributionGrade) != "unattributed" || tagOf(row, focus.TagRunIdentity) != "unverified" {
			t.Errorf("attribution %v", row["Tags"])
		}
	}
}

func TestCodexUnderTheCapRunsToItsEndWithPerRequestRows(t *testing.T) {
	got, log := codexRun(t, "replay", "5")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.result.Stopped || got.result.AgentExit != 0 {
		t.Errorf("stopped %v, exit %d", got.result.Stopped, got.result.AgentExit)
	}
	if progress := logged(t, log); progress[len(progress)-1] != "turn.completed" {
		t.Errorf("progress %v", progress)
	}
	if got.result.Report.CostSource != "agent_session_file" || len(got.result.Report.Requests) != 2 {
		t.Fatalf("%+v", got.result.Report)
	}
	if len(got.rows) != 8 {
		t.Errorf("%d rows, want four classes for each of two requests", len(got.rows))
	}
	// The recording at list, from the sheet's gpt-5.6-sol rows per million:
	// 5875 × 4.00 + 28032 × 0.40 + 111 × 20.00.
	if err := checkListTotal(got.result.ListTotal, "0.0369328"); err != nil {
		t.Error(err)
	}
	first := sha256.Sum256([]byte("response-1:input_uncached"))
	if got.rows[0]["x_CostroidRowId"] != hex.EncodeToString(first[:]) {
		t.Errorf("row id %v", got.rows[0]["x_CostroidRowId"])
	}
	for _, row := range got.rows {
		if text, _ := row["ChargeDescription"].(string); strings.Contains(text, "lowest context tier") {
			t.Errorf("a per-request row carries the turn caveat: %q", text)
		}
		if tagOf(row, focus.TagCostSource) != "agent_session_file" {
			t.Errorf("source %v", row["Tags"])
		}
	}
}

func TestPerRequestRowsTakeTheRequestsContextTier(t *testing.T) {
	got, _ := codexRun(t, "requests", "1")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if id := got.rows[0]["SkuPriceId"]; id != "openai:gpt-5.6-sol:global:standard:to-272000:input_uncached" {
		t.Errorf("a 250000 token request priced at %v", id)
	}
	builder := rowBuilder{catalog: pinned(t), agent: "codex", enforcement: adapter.Enforcement{Grade: adapter.GradeWrapperWatchdog}}
	rows, err := builder.rows(adapter.Report{CostBasis: focus.ListEquivalent, CostSource: "agent_session_file", Requests: []adapter.Request{{
		ID: "response-1", Provider: "openai", Model: "gpt-5.6-sol", Grain: adapter.PerRequest,
		ServiceTier: catalog.TierStandard, Region: catalog.RegionGlobal, ContextInputTokens: 272001,
		Start: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 1, 0, 0, 1, 0, time.UTC),
		Tokens: adapter.Tokens{InputUncached: 272001},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].SkuPriceID != "openai:gpt-5.6-sol:global:standard:over-272000:input_uncached" {
		t.Errorf("a 272001 token request priced at %s", rows[0].SkuPriceID)
	}
}

// assertUnwatched checks a run nothing watched: it ran to its end, and its
// rows are turn grain, grade none, and say why.
func assertUnwatched(t *testing.T, got outcome, why string) {
	t.Helper()
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.result.Stopped {
		t.Error("an unwatched run reported a stop")
	}
	if got.result.Enforcement.Grade != adapter.GradeNone || !strings.Contains(got.result.Enforcement.Note, why) {
		t.Errorf("%+v", got.result.Enforcement)
	}
	if len(got.rows) == 0 {
		t.Fatal("no rows")
	}
	for _, row := range got.rows {
		text, _ := row["ChargeDescription"].(string)
		if tagOf(row, focus.TagEnforcementGrade) != "none" || !strings.Contains(text, why) {
			t.Errorf("%s: %q", tagOf(row, focus.TagEnforcementGrade), text)
		}
		if tagOf(row, focus.TagCostSource) != "agent_stream" {
			t.Errorf("source %v", row["Tags"])
		}
		if row["ListCost"] != "0" && !strings.Contains(text, "lowest context tier") {
			t.Errorf("a priced turn row lost its caveat: %q", text)
		}
	}
}

func TestRunWithNoRolloutFileRecordsNoneAndSaysWhy(t *testing.T) {
	got, log := codexRun(t, "stream", "0.000001")
	assertUnwatched(t, got, "no rollout file appeared")
	if progress := logged(t, log); len(progress) != fakeCount {
		t.Errorf("progress %v: an unwatched run is not stopped", progress)
	}
}

func TestEphemeralRunRecordsNoneAndIsNotStopped(t *testing.T) {
	got, _ := codexRun(t, "stream", "0.000001", "--ephemeral")
	assertUnwatched(t, got, "--ephemeral writes no rollout file")
}

func TestUnrecordedCodexVersionRecordsNoneAndNamesTheVersion(t *testing.T) {
	t.Setenv(fakeVersion, "9.9.9")
	got, log := codexRun(t, "replay", "0.000001")
	assertUnwatched(t, got, `no recorded fixture covers Codex "9.9.9"`)
	if progress := logged(t, log); progress[len(progress)-1] != "turn.completed" {
		t.Errorf("progress %v", progress)
	}
	if got.result.Report.AgentVersion != "9.9.9" {
		t.Errorf("version %s", got.result.Report.AgentVersion)
	}
}

func TestRolloutThatDisagreesWithTheStreamYieldsToIt(t *testing.T) {
	t.Setenv(fakeStreamInput, "33908")
	got, _ := codexRun(t, "replay", "5")
	assertUnwatched(t, got, "total differs from the stream's")
}

func TestIdlePipeGivesTheAgentEndOfFileAtOnce(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	log := filepath.Join(t.TempDir(), "stdin")
	t.Setenv(fakeMode, "stdin")
	t.Setenv(fakeLog, log)
	t.Setenv("CODEX_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err = Run(ctx, Options{
		Argv:    []string{agentNamed(t, "codex"), "exec", "work"},
		Budget:  decimal.MustParse("1"),
		Rows:    io.Discard,
		Stdin:   reader,
		Stdout:  io.Discard,
		Stderr:  io.Discard,
		Catalog: pinned(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Error("the agent was still waiting on stdin")
	}
	if progress := logged(t, log); len(progress) != 2 || progress[0] != "0" {
		t.Errorf("progress %v", progress)
	}
}

func TestChildStdinKeepsDataFilesAndOtherReaders(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if childStdin(reader) != nil {
		t.Error("an idle pipe reached the agent")
	}
	if _, err := writer.WriteString("prompt"); err != nil {
		t.Fatal(err)
	}
	if childStdin(reader) != io.Reader(reader) {
		t.Error("a pipe with data did not reach the agent")
	}
	writer.Close()
	file := filepath.Join(t.TempDir(), "prompt")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	regular, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()
	if childStdin(regular) != io.Reader(regular) {
		t.Error("a file did not reach the agent")
	}
	other := strings.NewReader("")
	if childStdin(other) != io.Reader(other) {
		t.Error("a plain reader did not reach the agent")
	}
}

func TestClosedPipeReachesTheAgentAsEndOfFile(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	writer.Close()
	if childStdin(reader) != io.Reader(reader) {
		t.Error("a closed pipe did not reach the agent")
	}
}

func TestBillingAccountFillsTheColumnAndUnknownIsTheDefault(t *testing.T) {
	report := adapter.Report{CostBasis: focus.ListEquivalent, CostSource: "agent_stream", Requests: []adapter.Request{{
		ID: "t:turn:1", Provider: "openai", Model: "gpt-5.6-sol", Grain: adapter.PerTurn,
		ServiceTier: catalog.TierStandard, Region: catalog.RegionGlobal,
		Start: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 1, 0, 0, 1, 0, time.UTC),
		Tokens: adapter.Tokens{Output: 1},
	}}}
	for account, want := range map[string]string{"": "unknown", "org-example": "org-example"} {
		builder := rowBuilder{catalog: pinned(t), agent: "codex", account: account}
		rows, err := builder.rows(report)
		if err != nil {
			t.Fatal(err)
		}
		if rows[0].BillingAccountID != want {
			t.Errorf("%q gave %s", account, rows[0].BillingAccountID)
		}
	}
}

// claudeRun replays a scenario through the whole wrapper.
func claudeRun(t *testing.T, scenario, sessionID string, c *catalog.Catalog) (outcome, string) {
	t.Helper()
	config := t.TempDir()
	project := filepath.Join(config, "projects", "-work")
	if err := os.MkdirAll(filepath.Join(project, sessionID, "subagents"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(claudeData, scenario)
	session, err := os.ReadFile(filepath.Join(source, "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, sessionID+".jsonl"), session, 0o644); err != nil {
		t.Fatal(err)
	}
	agents, err := filepath.Glob(filepath.Join(source, "subagents", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range agents {
		data, err := os.ReadFile(agent)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(project, sessionID, "subagents", filepath.Base(agent)), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(t.TempDir(), "argv")
	t.Setenv(fakeMode, "replay")
	t.Setenv(fakeLog, log)
	t.Setenv("COSTROID_FAKE_STREAM", filepath.Join(mustAbs(t, source), "stream.jsonl"))
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	return runAgent(t, []string{agentNamed(t, "claude"), "-p", "work"}, "5", c), log
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

const (
	subagentSession = "00000000-0000-4000-8000-000000000002"
	// subagentList is hand-derived from the sheet's claude-haiku-5-5 rows:
	// 8 × 0.10 + 15605 × 0.01 + 3495 × 0.125 + 4450 × 0.20 + 272 × 0.50,
	// all per million tokens.
	subagentList = "0.001619725"
)

func TestClaudeCodeRunPassesTheBudgetAndRecordsTheFlagGrade(t *testing.T) {
	got, log := claudeRun(t, "subagent", subagentSession, pinned(t))
	if got.err != nil {
		t.Fatal(got.err)
	}
	argv := logged(t, log)
	at := slices.Index(argv, "--max-budget-usd")
	if at < 0 || argv[at+1] != "5" {
		t.Errorf("argv %v", argv)
	}
	if got.result.Enforcement.Grade != adapter.GradeAgentFlag {
		t.Errorf("grade %s", got.result.Enforcement.Grade)
	}
	for _, row := range got.rows {
		if grade := tagOf(row, focus.TagEnforcementGrade); grade != "agent-flag" {
			t.Errorf("grade %s", grade)
		}
		if text, _ := row["ChargeDescription"].(string); !strings.Contains(text, "can pass the cap") {
			t.Errorf("description %q", text)
		}
	}
}

func checkListTotal(got decimal.Decimal, want string) error {
	if got.Cmp(decimal.MustParse(want)) != 0 {
		return fmt.Errorf("list total %s, want %s", got, want)
	}
	return nil
}

// consumed sums ConsumedQuantity per class across the rows.
func consumed(t *testing.T, rows []map[string]any) map[string]int64 {
	t.Helper()
	totals := map[string]int64{}
	for _, row := range rows {
		quantity, err := strconv.ParseInt(row["ConsumedQuantity"].(string), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		totals[row["SkuMeter"].(string)] += quantity
	}
	return totals
}

func TestRowsAddBackToTheResultObjectsTotals(t *testing.T) {
	got, _ := claudeRun(t, "subagent", subagentSession, pinned(t))
	if got.err != nil {
		t.Fatal(got.err)
	}
	want := map[string]int64{
		"input_uncached":        8,
		"cache_read":            15605,
		"cache_write_5m":        3495,
		"cache_write_1h":        4450,
		"output":                272,
		"reasoning_output":      121,
		"cached_input_reported": 15605,
	}
	totals := consumed(t, got.rows)
	for class, tokens := range want {
		if totals[class] != tokens {
			t.Errorf("%s: %d, want %d", class, totals[class], tokens)
		}
	}
}

func TestListTotalIsTheHandDerivedFigure(t *testing.T) {
	got, _ := claudeRun(t, "subagent", subagentSession, pinned(t))
	if got.err != nil {
		t.Fatal(got.err)
	}
	if err := checkListTotal(got.result.ListTotal, subagentList); err != nil {
		t.Error(err)
	}
}

// priceChanged is a test copy of the pinned sheets with one row's price replaced.
func priceChanged(t *testing.T, sku, from, to string) *catalog.Catalog {
	t.Helper()
	release := fstest.MapFS{}
	edited := false
	err := fs.WalkDir(catalogdata.Sheets, catalogdata.Version, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := fs.ReadFile(catalogdata.Sheets, path)
		if err != nil {
			return err
		}
		lines := bytes.Split(data, []byte("\n"))
		for i, line := range lines {
			if bytes.Contains(line, []byte(`"SkuPriceId":"`+sku+`"`)) {
				lines[i] = bytes.Replace(line, []byte(`"ListUnitPrice":"`+from+`"`), []byte(`"ListUnitPrice":"`+to+`"`), 1)
				edited = !bytes.Equal(line, lines[i])
			}
		}
		release[filepath.Base(path)] = &fstest.MapFile{Data: bytes.Join(lines, []byte("\n"))}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !edited {
		t.Fatalf("no row %s at price %s", sku, from)
	}
	c, err := catalog.Parse(catalogdata.Version, release)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestListTotalAssertionFailsWhenOnePriceMoves(t *testing.T) {
	c := priceChanged(t, "anthropic:claude-haiku-5-5:global:standard:to-100000:output", "0.50", "0.51")
	got, _ := claudeRun(t, "subagent", subagentSession, c)
	if got.err != nil {
		t.Fatal(got.err)
	}
	if err := checkListTotal(got.result.ListTotal, subagentList); err == nil {
		t.Errorf("the money assertion passed with a moved price: total %s", got.result.ListTotal)
	}
	if err := checkListTotal(got.result.ListTotal, "0.001622445"); err != nil {
		t.Errorf("272 output tokens moved by 0.01 USD per million: %v", err)
	}
}

func TestPinnedCatalogMatchesTheAgentsOwnEstimateOnEveryRecordedRun(t *testing.T) {
	recorded := map[string]struct{ session, total string }{
		"one-request": {"00000000-0000-4000-8000-000000000001", "0.00060589"},
		"subagent":    {subagentSession, "0.001619725"},
		"budget-stop": {"00000000-0000-4000-8000-000000000003", "0.0007324"},
	}
	for scenario, want := range recorded {
		t.Run(scenario, func(t *testing.T) {
			got, _ := claudeRun(t, scenario, want.session, pinned(t))
			if got.err != nil {
				t.Fatal(got.err)
			}
			if err := checkListTotal(got.result.ListTotal, want.total); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestSubscriptionRowsStayOutOfEffectiveCost(t *testing.T) {
	got, _ := claudeRun(t, "subagent", subagentSession, pinned(t))
	if got.err != nil {
		t.Fatal(got.err)
	}
	for _, row := range got.rows {
		if tagOf(row, focus.TagCostBasis) != "list_equivalent" {
			t.Errorf("basis %v", row["Tags"])
		}
		if row["EffectiveCost"] != "0" || row["BilledCost"] != "0" {
			t.Errorf("effective %v, billed %v", row["EffectiveCost"], row["BilledCost"])
		}
	}
}

func TestInformationalRowsCarryNoPriceAndTheirOwnUnit(t *testing.T) {
	got, _ := claudeRun(t, "subagent", subagentSession, pinned(t))
	if got.err != nil {
		t.Fatal(got.err)
	}
	seen := 0
	for _, row := range got.rows {
		class := row["SkuMeter"].(string)
		if class != "reasoning_output" && class != "cached_input_reported" {
			continue
		}
		seen++
		if row["ListCost"] != "0" || row["ListUnitPrice"] != "0" || row["PricingUnit"] != "Informational Tokens" || row["ConsumedUnit"] != "Informational Tokens" {
			t.Errorf("%v", row)
		}
	}
	if seen == 0 {
		t.Fatal("no informational row")
	}
}

func TestRowsCarryTheRequestAndTheRunKeys(t *testing.T) {
	got, _ := claudeRun(t, "one-request", "00000000-0000-4000-8000-000000000001", pinned(t))
	if got.err != nil {
		t.Fatal(got.err)
	}
	row := got.rows[0]
	checks := map[string]string{
		"ChargePeriodStart":  "2026-11-01T11:14:36Z",
		"ChargePeriodEnd":    "2026-11-01T11:14:38Z",
		"BillingPeriodStart": "2026-11-01T00:00:00Z",
		"BillingPeriodEnd":   "2026-12-01T00:00:00Z",
		"SkuMeter":           "input_uncached",
		"SkuId":              "anthropic/claude-haiku-5-5/input_uncached",
		"SkuPriceId":         "anthropic:claude-haiku-5-5:global:standard:to-100000:input_uncached",
		"PricingQuantity":    "0.000002",
		"PricingUnit":        "1000000 Tokens",
		"ListCost":           "0.0000002",
		"ConsumedUnit":       "Tokens",
	}
	for column, want := range checks {
		if row[column] != want {
			t.Errorf("%s = %v, want %s", column, row[column], want)
		}
	}
	if row["x_CostroidCatalogVersion"] != "0.1.0" {
		t.Errorf("catalog version %v", row["x_CostroidCatalogVersion"])
	}
	if tagOf(row, focus.TagSessionID) != "00000000-0000-4000-8000-000000000001" || tagOf(row, focus.TagAgentVersion) != "2.1.316" {
		t.Errorf("tags %v", row["Tags"])
	}
	if tagOf(row, focus.TagCostSource) != "agent_session_file" {
		t.Errorf("source %v", row["Tags"])
	}
}

func TestUnknownModelRowSaysItWasPricedAtTheFallback(t *testing.T) {
	builder := rowBuilder{catalog: pinned(t), agent: "codex", enforcement: adapter.Enforcement{Grade: adapter.GradeWrapperWatchdog, Note: "note"}}
	report := adapter.Report{CostBasis: focus.ListEquivalent, CostSource: "agent_stream", Requests: []adapter.Request{{
		ID: "t:turn:1", Provider: "openai", Model: "unknown", Grain: adapter.PerTurn,
		ServiceTier: catalog.TierStandard, Region: catalog.RegionGlobal,
		Start: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 1, 0, 0, 1, 0, time.UTC),
		Tokens: adapter.Tokens{Output: 1000000},
	}}}
	rows, err := builder.rows(report)
	if err != nil {
		t.Fatal(err)
	}
	row := rows[0]
	if row.SkuPriceID != "openai:fallback:output" || row.ListCost.String() != "180" {
		t.Errorf("%s %s", row.SkuPriceID, row.ListCost)
	}
	if !strings.Contains(row.ChargeDescription, "fallback tier") {
		t.Errorf("description %q", row.ChargeDescription)
	}
}

func TestCacheWrite30mIsWrittenUnderTheCatalogClass(t *testing.T) {
	builder := rowBuilder{catalog: pinned(t), agent: "codex", enforcement: adapter.Enforcement{Grade: adapter.GradeWrapperWatchdog}}
	report := adapter.Report{CostBasis: focus.ListEquivalent, CostSource: "agent_stream", Requests: []adapter.Request{{
		ID: "t:turn:1", Provider: "openai", Model: "gpt-5.6-sol", Grain: adapter.PerTurn,
		ServiceTier: catalog.TierStandard, Region: catalog.RegionGlobal,
		Start: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 1, 0, 0, 1, 0, time.UTC),
		Tokens: adapter.Tokens{CacheWrite30m: 2000},
	}}}
	rows, err := builder.rows(report)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SkuMeter != focus.CacheWrite30m || rows[0].ListCost.String() != "0.01" {
		t.Errorf("%+v", rows)
	}
}

func TestChargePeriodIsWholeSecondsAndEndsAfterItStarts(t *testing.T) {
	base := time.Date(2026, 11, 1, 11, 15, 12, 0, time.UTC)
	cases := []struct {
		start, end time.Time
		seconds    time.Duration
	}{
		{base.Add(814 * time.Millisecond), base.Add(2363 * time.Millisecond), 3 * time.Second},
		{base, base, time.Second},
		{base, base.Add(2 * time.Second), 2 * time.Second},
	}
	for _, tc := range cases {
		first, last := chargePeriod(tc.start, tc.end)
		if !first.Equal(base) || last.Sub(first) != tc.seconds {
			t.Errorf("%s to %s", first, last)
		}
	}
}

func TestBillingPeriodCrossesTheYear(t *testing.T) {
	first, last := billingPeriod(time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC))
	if first.Format(instantLayout) != "2026-12-01T00:00:00Z" || last.Format(instantLayout) != "2027-01-01T00:00:00Z" {
		t.Errorf("%s to %s", first, last)
	}
}

func TestRowIDIsStableAndSeparatesClasses(t *testing.T) {
	// sha256 of the text req:output, computed outside this package.
	const want = "8ffecdf34ee2f4b71af971bb3720fa114774e9c270450353ad019926c5f1326c"
	output := rowID("req", focus.Output)
	if output == rowID("req", focus.CacheRead) || output == rowID("other", focus.Output) {
		t.Error("row ids do not follow the request and class")
	}
	if output != want {
		t.Errorf("row id %s", output)
	}
}

func TestUnknownAgentIsRefusedByName(t *testing.T) {
	_, err := AgentFor([]string{"/usr/bin/aider"})
	if err == nil || !strings.Contains(err.Error(), "aider") {
		t.Errorf("err = %v", err)
	}
}

func TestAgentThatFailsToStartReportsTheCommand(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	got := runAgent(t, []string{filepath.Join(t.TempDir(), "codex"), "exec", "work"}, "1", pinned(t))
	if got.err == nil {
		t.Error("a missing binary reported nothing")
	}
}
