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
	"maps"
	"os"
	"os/exec"
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
	"github.com/Costroid/costroid/internal/agenttest"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/cicontext"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

const (
	fakeMode            = "COSTROID_FAKE_AGENT"
	fakeLog             = "COSTROID_FAKE_LOG"
	fakeRollout         = "COSTROID_FAKE_ROLLOUT"
	fakeVersion         = "COSTROID_FAKE_VERSION"
	fakeStreamInput     = "COSTROID_FAKE_STREAM_INPUT"
	fakeNoPause         = "COSTROID_FAKE_NO_PAUSE"
	fakeNested          = "COSTROID_FAKE_NESTED"
	fakeNestedCWD       = "COSTROID_FAKE_NESTED_CWD"
	fakeStreamUsage     = "COSTROID_FAKE_STREAM_USAGE"
	fakeCutAfter        = "COSTROID_FAKE_CUT_AFTER"
	fakeSubagentRollout = "COSTROID_FAKE_SUBAGENT_ROLLOUT"
	fakeDetach          = "COSTROID_FAKE_DETACH"
	fakeStderr          = "COSTROID_FAKE_STDERR"
	subagentThread      = "00000000-0000-4000-8000-0000000000c1"
	fakeCount           = 20
	fakeThread          = "00000000-0000-4000-8000-0000000000aa"
	claudeData          = "../adapter/claudecode/testdata/2.1.316"
	nestedData          = "../adapter/claudecode/testdata/not-recorded/nested"
	codexData           = "../adapter/codex/testdata/0.164.0-alpha.11.1"
	recordedCodex       = "0.164.0-alpha.11.1"
	recordedThread      = "00000000-0000-4000-8000-000000000004"
	requestPause        = 200 * time.Millisecond
	requestInput        = 250000
	stubbornEnv         = "stubborn"
	cacheWriteMode      = "cache-write"
)

// TestMain turns the test binary into the fake agent when the harness runs it
// under the name of a real one. Otherwise it runs the tests with a stub first
// on PATH for every agent, so no test can start the machine's own.
func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeMode); mode != "" {
		fakeAgent(mode)
		return
	}
	os.Exit(agenttest.Main(m.Run))
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
	if err := os.WriteFile(os.Getenv(fakeLog)+".otel", []byte(os.Getenv(cicontext.ResourceAttributesVariable)), 0o644); err != nil {
		os.Exit(98)
	}
	switch mode {
	case "stream":
		fakeStreamOnly(logFile)
	case cacheWriteMode:
		fakeCacheWrite()
	case "replay":
		fakeReplay(logFile)
	case "one-turn":
		fakeOneTurn()
	case "subagent":
		fakeSubagent()
		fakeReplay(logFile)
	case "replay-wait":
		fakeReplayThenWait()
	case "silent":
		fmt.Printf(`{"type":"thread.started","thread_id":%q}`+"\n", fakeThread)
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

// fakeCacheWrite reports one turn holding 200 written tokens in its 1000 input.
func fakeCacheWrite() {
	fmt.Printf(`{"type":"thread.started","thread_id":%q}`+"\n", fakeThread)
	fmt.Println(`{"type":"turn.started"}`)
	fmt.Println(`{"type":"turn.completed","usage":{"input_tokens":1000,"cached_input_tokens":300,"cache_write_input_tokens":200,"output_tokens":50}}`)
}

// fakeOneTurn reports one turn of the input the environment names, and 1000
// output tokens, on the stream alone.
func fakeOneTurn() {
	fmt.Printf(`{"type":"thread.started","thread_id":%q}`+"\n", fakeThread)
	fmt.Println(`{"type":"turn.started"}`)
	fmt.Printf(`{"type":"turn.completed","usage":{"input_tokens":%s,"cached_input_tokens":0,"output_tokens":1000}}`+"\n", os.Getenv(fakeStreamInput))
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
			if os.Getenv(fakeNoPause) == "" {
				time.Sleep(requestPause)
			}
		}
	}
	input := os.Getenv(fakeStreamInput)
	if input == "" {
		input = "33907"
	}
	fmt.Fprintln(log, "turn.completed")
	if usage := os.Getenv(fakeStreamUsage); usage != "" {
		fmt.Printf(`{"type":"turn.completed","usage":%s}`+"\n", usage)
		return
	}
	fmt.Printf(`{"type":"turn.completed","usage":{"input_tokens":%s,"cached_input_tokens":28032,"output_tokens":111}}`+"\n", input)
}

// fakeSubagent leaves the rollout file of a subagent thread the run's thread
// started.
func fakeSubagent() {
	data, err := os.ReadFile(os.Getenv(fakeSubagentRollout))
	if err != nil {
		os.Exit(97)
	}
	rollout := openFakeRollout(subagentThread)
	defer rollout.Close()
	rollout.Write(bytes.ReplaceAll(data, []byte("@parent"), []byte(fakeThread)))
}

// fakeReplayThenWait writes a recorded rollout up to the first line that holds
// the cut, opens a turn and waits in it, the way a run waits on a tool or on a
// model request, until it is stopped.
func fakeReplayThenWait() {
	data, err := os.ReadFile(os.Getenv(fakeRollout))
	if err != nil {
		os.Exit(97)
	}
	text := strings.ReplaceAll(string(data), recordedThread, fakeThread)
	text = strings.ReplaceAll(text, "00000000-0000-4000-8000-000000000005", fakeThread)
	if cut := os.Getenv(fakeCutAfter); cut != "" {
		at := strings.Index(text, cut)
		text = text[:at+strings.Index(text[at:], "\n")+1]
	}
	rollout := openFakeRollout(fakeThread)
	rollout.WriteString(text)
	rollout.Close()
	fmt.Printf(`{"type":"thread.started","thread_id":%q}`+"\n", fakeThread)
	fmt.Println(`{"type":"turn.started"}`)
	time.Sleep(5 * time.Second)
	fmt.Println(`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":0}}`)
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
// It counts its starts, and while a hold file exists it waits, up to a bound,
// before it replays.
func fakeClaude() {
	starts, err := os.OpenFile(os.Getenv(fakeLog)+".starts", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		os.Exit(98)
	}
	fmt.Fprintln(starts, os.Getpid())
	starts.Close()
	if detach := os.Getenv(fakeDetach); detach != "" {
		cgroup, _ := os.ReadFile("/proc/self/cgroup")
		if err := os.WriteFile(os.Getenv(fakeLog)+".cgroup", cgroup, 0o644); err != nil {
			os.Exit(98)
		}
		leaveDetachedChild(detach)
	}
	for wait := 0; wait < 100; wait++ {
		if _, err := os.Stat(os.Getenv(fakeLog) + ".hold"); err != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	logFile, err := os.Create(os.Getenv(fakeLog))
	if err != nil {
		os.Exit(98)
	}
	defer logFile.Close()
	fmt.Fprintln(logFile, strings.Join(os.Args[1:], "\n"))
	attributes := os.Getenv(cicontext.ResourceAttributesVariable)
	if err := os.WriteFile(os.Getenv(fakeLog)+".otel", []byte(attributes), 0o644); err != nil {
		os.Exit(98)
	}
	var credentials []string
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "ANTHROPIC_") {
			credentials = append(credentials, entry)
		}
	}
	if err := os.WriteFile(os.Getenv(fakeLog)+".anthropic", []byte(strings.Join(credentials, "\n")), 0o644); err != nil {
		os.Exit(98)
	}
	if line := os.Getenv(fakeStderr); line != "" {
		fmt.Fprintln(os.Stderr, line)
	}
	data, err := os.ReadFile(os.Getenv("COSTROID_FAKE_STREAM"))
	if err != nil {
		os.Exit(97)
	}
	if template := os.Getenv(fakeNested); template != "" {
		writeNestedSession(template)
		data = bytes.Replace(data, []byte(`"subtype":"init",`), []byte(`"subtype":"init","cwd":"/work",`), -1)
	}
	os.Stdout.Write(data)
}

// leaveDetachedChild starts a shell in a session of its own that sleeps for a
// minute, the way an agent's tool can leave one, and waits until it has
// written its pid beside the log. A quiet child's output goes nowhere; a
// holding one keeps the agent's stdout and stderr open.
func leaveDetachedChild(how string) {
	pidFile := os.Getenv(fakeLog) + ".detached"
	child := exec.Command("setsid", "sh", "-c", `echo $$ > "$0.part" && mv "$0.part" "$0" && exec sleep 60`, pidFile)
	if how == "holding" {
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
	}
	if err := child.Start(); err != nil {
		os.Exit(94)
	}
	for wait := 0; wait < 100; wait++ {
		if _, err := os.Stat(pidFile); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	os.Exit(94)
}

// writeNestedSession leaves the session file a hook's own claude -p would, in
// the run's project directory, timed now. Its working directory comes from
// the environment, and the run's reads /work.
func writeNestedSession(template string) {
	data, err := os.ReadFile(template)
	if err != nil {
		os.Exit(95)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	session := strings.NewReplacer(
		"@session", nestedSession, "@request", "req_fixture_nested", "@start", now, "@end", now,
		`"cwd":"/work"`, `"cwd":"`+os.Getenv(fakeNestedCWD)+`"`,
	).Replace(string(data))
	path := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects", "-work", nestedSession+".jsonl")
	if err := os.WriteFile(path, []byte(session), 0o644); err != nil {
		os.Exit(95)
	}
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
	return runAgentIn(t, cicontext.Context{}, argv, budget, c)
}

func runAgentIn(t *testing.T, ci cicontext.Context, argv []string, budget string, c *catalog.Catalog) outcome {
	t.Helper()
	var rows, stdout, stderr bytes.Buffer
	result, err := Run(context.Background(), Options{
		Argv:    argv,
		Budget:  decimal.MustParse(budget),
		Rows:    &rows,
		CI:      ci,
		Stdin:   strings.NewReader(""),
		Stdout:  &stdout,
		Stderr:  &stderr,
		Catalog: c,
		Grace:   200 * time.Millisecond,
	})
	return outcome{result: result, stdout: stdout.String(), stderr: stderr.String(), err: err, rows: parseRows(t, rows.Bytes())}
}

func parseRows(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	var parsed []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(nil, 1<<20)
	for scanner.Scan() {
		var row map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		parsed = append(parsed, row)
	}
	return parsed
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
		if text, _ := row["ChargeDescription"].(string); !strings.Contains(text, "a stop at the cap that requests in flight can pass") {
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

func TestLinesWrittenAsTheAgentExitsAreStillRead(t *testing.T) {
	t.Setenv(fakeNoPause, "1")
	got, _ := codexRun(t, "replay", "5")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.result.Report.CostSource != "agent_session_file" || got.result.Enforcement.Grade != adapter.GradeWrapperWatchdog {
		t.Errorf("%s, %+v", got.result.Report.CostSource, got.result.Enforcement)
	}
}

func TestCapReadAfterTheAgentClosedItsOutputIsNotAStop(t *testing.T) {
	// Past Linux's pid limit, so a stop that wrongly fires signals nobody.
	const noProcess = 1 << 23
	stop := newStopper(noProcess, time.Millisecond)
	defer stop.release()
	stop.agentDone()
	stop.stopForBudget()
	if stop.byBudget() {
		t.Error("a cap read after the run reported a stop")
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
		if row["ListCost"] != "0" && !strings.Contains(text, "one turn's input of") {
			t.Errorf("a priced turn row does not say how far its context tier holds: %q", text)
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

func TestUnrecordedCodexVersionIsWatchedAndNamed(t *testing.T) {
	t.Setenv(fakeVersion, "9.9.9")
	got, _ := codexRun(t, "replay", "5")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.result.Enforcement.Grade != adapter.GradeWrapperWatchdog || got.result.Stopped {
		t.Errorf("%+v", got.result)
	}
	if got.result.Report.AgentVersion != "9.9.9" {
		t.Errorf("version %s", got.result.Report.AgentVersion)
	}
	note := "Codex 9.9.9 is not a recorded version; the rollout file passed every check"
	if !slices.Contains(got.result.Report.Warnings, note) {
		t.Errorf("warnings %v", got.result.Report.Warnings)
	}
}

func TestRolloutThatDisagreesWithTheStreamYieldsToIt(t *testing.T) {
	t.Setenv(fakeStreamInput, "33908")
	got, _ := codexRun(t, "replay", "5")
	assertUnwatched(t, got, "total differs from the stream's")
}

func TestSlowStdinProducerReachesTheAgentUnchanged(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	log := filepath.Join(t.TempDir(), "stdin")
	t.Setenv(fakeMode, "stdin")
	t.Setenv(fakeLog, log)
	t.Setenv("CODEX_HOME", t.TempDir())
	go func() {
		defer writer.Close()
		time.Sleep(requestPause)
		fmt.Fprint(writer, "prompt")
	}()
	_, err = Run(context.Background(), Options{
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
	if progress := logged(t, log); len(progress) != 2 || progress[0] != "6" {
		t.Errorf("progress %v, want the agent to read all 6 bytes", progress)
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
	return claudeRunWith(t, scenario, sessionID, c, cicontext.Context{})
}

func claudeRunWith(t *testing.T, scenario, sessionID string, c *catalog.Catalog, ci cicontext.Context) (outcome, string) {
	t.Helper()
	log := installClaudeRun(t, scenario, sessionID)
	return runAgentIn(t, ci, []string{agentNamed(t, "claude"), "-p", "work"}, "5", c), log
}

// installClaudeRun lays out a scenario's session files in a new config
// directory, points the fake claude at its stream, and returns its log.
func installClaudeRun(t *testing.T, scenario, sessionID string) string {
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
	return log
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

const (
	nestedSession = "00000000-0000-4000-8000-0000000000b1"
	// nestedList adds the nested session's 1000 input and 200 output tokens,
	// at 0.10 and 0.50 USD per million, to subagentList.
	nestedList = "0.001819725"
)

// nestedRun replays the subagent run while a hook's claude -p, started in the
// given working directory, leaves its own session file. A system names the CI
// variable the run sees as "true"; empty means outside CI.
func nestedRun(t *testing.T, cwd, system string) outcome {
	t.Helper()
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("GITLAB_CI", "")
	if system != "" {
		t.Setenv(system, "true")
	}
	t.Setenv(fakeNested, mustAbs(t, filepath.Join(nestedData, "session.jsonl")))
	t.Setenv(fakeNestedCWD, cwd)
	got, _ := claudeRun(t, "subagent", subagentSession, pinned(t))
	if got.err != nil {
		t.Fatal(got.err)
	}
	return got
}

func nestedRows(rows []map[string]any) []map[string]any {
	var nested []map[string]any
	for _, row := range rows {
		if tagOf(row, focus.TagSessionID) == nestedSession {
			nested = append(nested, row)
		}
	}
	return nested
}

func TestASessionAHookStartsInsideTheRunIsCountedUnderItsOwnID(t *testing.T) {
	got := nestedRun(t, "/work", "")
	if err := checkListTotal(got.result.ListTotal, nestedList); err != nil {
		t.Error(err)
	}
	nested := nestedRows(got.rows)
	if len(nested) != 2 {
		t.Fatalf("%d rows carry the nested session's id, want its input and output", len(nested))
	}
	for _, row := range nested {
		if basis := tagOf(row, focus.TagCostBasis); basis != "list_equivalent" {
			t.Errorf("a subscription run's nested row reads %s", basis)
		}
	}
	for _, row := range got.rows {
		description, _ := row["ChargeDescription"].(string)
		isNested := tagOf(row, focus.TagSessionID) == nestedSession
		if isNested != strings.Contains(description, nestedNote) {
			t.Errorf("session %s reads %q", tagOf(row, focus.TagSessionID), description)
		}
	}
}

func TestASessionInAnotherWorkingDirectoryIsNotCounted(t *testing.T) {
	got := nestedRun(t, "/elsewhere", "")
	if err := checkListTotal(got.result.ListTotal, subagentList); err != nil {
		t.Error(err)
	}
	if nested := nestedRows(got.rows); len(nested) != 0 {
		t.Errorf("%d rows from a session in another directory", len(nested))
	}
	if warnings := got.result.Report.Warnings; len(warnings) != 1 || !strings.Contains(warnings[0], "another working directory") {
		t.Errorf("warnings %q", warnings)
	}
}

func TestInCIASessionInAnotherWorkingDirectoryIsCounted(t *testing.T) {
	got := nestedRun(t, "/elsewhere", "GITHUB_ACTIONS")
	if err := checkListTotal(got.result.ListTotal, nestedList); err != nil {
		t.Error(err)
	}
	if nested := nestedRows(got.rows); len(nested) != 2 {
		t.Errorf("%d rows carry the nested session's id", len(nested))
	}
}

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
	if row["x_CostroidCatalogVersion"] != "0.3.0" {
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
	if row.SkuPriceID != "openai:fallback:output" || row.ListCost.String() != "600" {
		t.Errorf("%s %s", row.SkuPriceID, row.ListCost)
	}
	if !strings.Contains(row.ChargeDescription, "fallback tier") {
		t.Errorf("description %q", row.ChargeDescription)
	}
}

func TestACodexCacheWriteReadsCacheWrite30mWithTheAssumedTag(t *testing.T) {
	got, _ := codexRun(t, cacheWriteMode, "5", "--ephemeral")
	if got.err != nil {
		t.Fatal(got.err)
	}
	tokens := map[string]string{}
	for _, row := range got.rows {
		meter, _ := row["SkuMeter"].(string)
		tokens[meter], _ = row["ConsumedQuantity"].(string)
		want := ""
		if meter == string(focus.CacheWrite30m) {
			want = "30m"
		}
		if assumed := tagOf(row, focus.TagAssumedTTL); assumed != want {
			t.Errorf("%s: assumed %q, want %q", meter, assumed, want)
		}
		if meter == string(focus.CacheWrite30m) && row["SkuPriceId"] != "openai:gpt-5.6-sol:global:standard:to-272000:cache_write_30m" {
			t.Errorf("priced under %v", row["SkuPriceId"])
		}
	}
	want := map[string]string{"input_uncached": "500", "cache_read": "300", "cache_write_30m": "200", "output": "50", "cached_input_reported": "300"}
	if !maps.Equal(tokens, want) {
		t.Errorf("tokens %v", tokens)
	}
	// From the sheet's gpt-5.6-sol rows per million:
	// 500 × 4.00 + 300 × 0.40 + 200 × 5.00 + 50 × 20.00.
	if err := checkListTotal(got.result.ListTotal, "0.00412"); err != nil {
		t.Error(err)
	}
}

func TestAnAssumedTagSitsOnTheClassThatTookTheAssumption(t *testing.T) {
	builder := rowBuilder{catalog: pinned(t), agent: "claude-code", enforcement: adapter.Enforcement{Grade: adapter.GradeAgentFlag}}
	report := adapter.Report{CostBasis: focus.Measured, CostSource: "agent_stream", Requests: []adapter.Request{{
		ID: "r", Provider: "anthropic", Model: "claude-haiku-5-5", Grain: adapter.PerRequest,
		ServiceTier: catalog.TierStandard, Region: catalog.RegionGlobal,
		Start: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 1, 0, 0, 1, 0, time.UTC),
		Tokens:       adapter.Tokens{InputUncached: 10, CacheWrite5m: 30, CacheWrite1h: 20},
		AssumedWrite: focus.CacheWrite5m,
	}}}
	rows, err := builder.rows(report)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("%d rows", len(rows))
	}
	for _, row := range rows {
		want := ""
		if row.SkuMeter == focus.CacheWrite5m {
			want = "5m"
		}
		if got := row.Tags[focus.TagAssumedTTL]; got != want {
			t.Errorf("%s: assumed %q, want %q", row.SkuMeter, got, want)
		}
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

func TestCredentialFillsSubAccountIdOnEveryRowAndRowsStayUnverified(t *testing.T) {
	var rows bytes.Buffer
	_, err := Record(Options{
		Credential: "apikey_01ExampleCi",
		CI:         cicontext.Context{Repository: "github.com/acme/widgets", PullRequest: "7", Head: strings.Repeat("a", 40), Run: "1234"},
		Rows:       &rows,
		Catalog:    pinned(t),
	}, "claude-code", recordedReport())
	if err != nil {
		t.Fatal(err)
	}
	parsed := parseRows(t, rows.Bytes())
	if len(parsed) == 0 {
		t.Fatal("no rows")
	}
	for _, row := range parsed {
		if row["SubAccountId"] != "apikey_01ExampleCi" {
			t.Errorf("SubAccountId %v", row["SubAccountId"])
		}
		for tag, want := range map[string]string{
			focus.TagRepo: "github.com/acme/widgets", focus.TagPR: "7", focus.TagCommitSHA: strings.Repeat("a", 40), focus.TagWorkflowRunID: "1234",
			focus.TagRunIdentity: "unverified", focus.TagAttributionGrade: "unattributed",
		} {
			if got := tagOf(row, tag); got != want {
				t.Errorf("%s reads %q", tag, got)
			}
		}
	}
}

func TestRowsOutsideCIStateNoJoinKeys(t *testing.T) {
	var rows bytes.Buffer
	if _, err := Record(Options{Rows: &rows, Catalog: pinned(t)}, "claude-code", recordedReport()); err != nil {
		t.Fatal(err)
	}
	for _, row := range parseRows(t, rows.Bytes()) {
		if row["SubAccountId"] != nil {
			t.Errorf("SubAccountId %v", row["SubAccountId"])
		}
		for _, tag := range []string{focus.TagRepo, focus.TagPR, focus.TagCommitSHA, focus.TagWorkflowRunID} {
			if got := tagOf(row, tag); got != "" {
				t.Errorf("%s reads %q", tag, got)
			}
		}
	}
}

func TestRecordKeepsTheReportsOwnGrade(t *testing.T) {
	report := recordedReport()
	report.Enforcement = adapter.Enforcement{Grade: adapter.GradeNone, Note: "none: not started by costroid"}
	var rows bytes.Buffer
	result, err := Record(Options{Rows: &rows, Catalog: pinned(t)}, "claude-code", report)
	if err != nil {
		t.Fatal(err)
	}
	if result.Enforcement.Grade != adapter.GradeNone || result.Rows == 0 {
		t.Fatalf("%+v", result)
	}
	for _, row := range parseRows(t, rows.Bytes()) {
		if grade := tagOf(row, focus.TagEnforcementGrade); grade != "none" {
			t.Errorf("grade %s", grade)
		}
	}
}

func TestAResidencyRequestStatesItsRegionAndTier(t *testing.T) {
	report := recordedReport()
	report.Requests[0].Region = "us"
	report.Requests[0].ServiceTier = "standard"
	var rows bytes.Buffer
	if _, err := Record(Options{Rows: &rows, Catalog: pinned(t)}, "claude-code", report); err != nil {
		t.Fatal(err)
	}
	row := parseRows(t, rows.Bytes())[0]
	if row["RegionId"] != "us" || row["RegionName"] != "us" || row["x_CostroidServiceTier"] != "standard" {
		t.Fatalf("%v %v %v", row["RegionId"], row["RegionName"], row["x_CostroidServiceTier"])
	}
}

func TestTheAgentInheritsTheJoinKeysThroughItsResourceAttributes(t *testing.T) {
	t.Setenv(cicontext.ResourceAttributesVariable, "service.name=ci")
	got, log := claudeRunWith(t, "subagent", subagentSession, pinned(t), cicontext.Context{PullRequest: "7", Run: "1234"})
	if got.err != nil {
		t.Fatal(got.err)
	}
	const want = "service.name=ci,cicd.pipeline.run.id=1234,vcs.change.id=7"
	data, err := os.ReadFile(log + ".otel")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("the agent read %q", data)
	}
}

// TestJoinAttributes holds that costroid run hands every agent the six vcs.*
// and cicd.* join keys through OTEL_RESOURCE_ATTRIBUTES, beside the job's own
// attributes, so the agent's telemetry names the pull request, both commits,
// the repository and the CI run.
func TestJoinAttributes(t *testing.T) {
	head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	ci := cicontext.Context{
		Repository: "github.com/acme/widgets", RepositoryURL: "https://github.com/Acme/Widgets",
		PullRequest: "7", Head: head, Base: base, Run: "1234", Task: "agent",
	}
	want := strings.Join([]string{
		"service.name=ci",
		"cicd.pipeline.run.id=1234",
		"cicd.pipeline.task.run.id=agent",
		"vcs.change.id=7",
		"vcs.ref.base.revision=" + base,
		"vcs.ref.head.revision=" + head,
		"vcs.repository.url.full=https://github.com/Acme/Widgets",
	}, ",")
	t.Setenv(cicontext.ResourceAttributesVariable, "service.name=ci")
	check := func(agent string, got outcome, log string) {
		t.Helper()
		if got.err != nil {
			t.Fatalf("%s: %v", agent, got.err)
		}
		data, err := os.ReadFile(log + ".otel")
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != want {
			t.Errorf("%s read %s", agent, data)
		}
	}
	claudeLog := installClaudeRun(t, "subagent", subagentSession)
	check("claude", runAgentIn(t, ci, []string{agentNamed(t, "claude"), "-p", "work"}, "5", pinned(t)), claudeLog)

	codexLog := filepath.Join(t.TempDir(), "progress")
	t.Setenv(fakeMode, "one-turn")
	t.Setenv(fakeStreamInput, "1000")
	t.Setenv(fakeLog, codexLog)
	t.Setenv("CODEX_HOME", t.TempDir())
	check("codex", runAgentIn(t, ci, []string{agentNamed(t, "codex"), "exec", "--ephemeral", "-m", "gpt-5.6-sol", "work"}, "5", pinned(t)), codexLog)
}

func recordedReport() adapter.Report {
	at := time.Date(2026, 11, 1, 11, 15, 0, 0, time.UTC)
	return adapter.Report{
		SessionID: subagentSession, AgentVersion: "2.1.316", CostBasis: focus.ListEquivalent, CostSource: "agent_stream",
		Requests: []adapter.Request{{
			ID: "req_1", Provider: "anthropic", Model: "claude-haiku-5-5", Start: at, End: at.Add(time.Second),
			Grain: adapter.PerRequest, Tokens: adapter.Tokens{Output: 1000},
		}},
	}
}
