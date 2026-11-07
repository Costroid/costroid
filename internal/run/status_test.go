package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/status"
)

// statusSchemaPath is status/v0.1 of Costroid/costroid-schemas, copied.
const statusSchemaPath = "../status/testdata/status.schema.json"

func compileStatusSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	file, err := os.Open(statusSchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	document, err := jsonschema.UnmarshalJSON(file)
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("status.schema.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("status.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func statusSchemaErrors(schema *jsonschema.Schema, data []byte) error {
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return err
	}
	return schema.Validate(instance)
}

// statusPath points the run's status file at a new path.
func statusPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "costroid-status.json")
	t.Setenv(status.FileVariable, path)
	return path
}

func readStatusFile(t *testing.T, path string) (map[string]any, []byte) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("%v: %s", err, data)
	}
	return parsed, data
}

// statusWatch reads the status file until it is stopped, and keeps every
// distinct content it read.
func statusWatch(path string) func() [][]byte {
	var (
		mu    sync.Mutex
		seen  [][]byte
		stop  = make(chan struct{})
		ended = make(chan struct{})
	)
	go func() {
		defer close(ended)
		for {
			if data, err := os.ReadFile(path); err == nil {
				mu.Lock()
				if len(seen) == 0 || !bytes.Equal(seen[len(seen)-1], data) {
					seen = append(seen, data)
				}
				mu.Unlock()
			}
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	return func() [][]byte {
		close(stop)
		<-ended
		return seen
	}
}

// givenSession reads nothing and finishes with the report or the error given.
type givenSession struct {
	report adapter.Report
	err    error
}

func (s *givenSession) Feed([]byte, time.Time) error { return nil }

func (s *givenSession) Finish() (adapter.Report, error) { return s.report, s.err }

// givenTailer settles the requests it holds, one more each time a test asks.
type givenTailer struct {
	givenSession
	settle chan []adapter.Request
}

func (s *givenTailer) Tail(_ context.Context, settled func([]adapter.Request)) {
	for requests := range s.settle {
		settled(requests)
	}
}

// codexRequest is request one of the recorded Codex run, as the watch reads
// it from the rollout file.
func codexRequest(id string) adapter.Request {
	at := time.Date(2026, 11, 1, 11, 17, 10, 0, time.UTC)
	return adapter.Request{
		ID: id, Provider: "openai", Model: "gpt-5.6-sol", Start: at, End: at,
		ServiceTier: catalog.TierStandard, Region: catalog.RegionGlobal, ContextInputTokens: 16994, Grain: adapter.PerRequest,
		Tokens: adapter.Tokens{InputUncached: 5624, CacheRead: 11264, Output: 106, CachedInputReported: 11264},
	}
}

// drivenStatus writes a status file through every state a watched run passes
// through, and returns each file the run wrote, in order.
func drivenStatus(t *testing.T, finish adapter.Report, finishErr error) [][]byte {
	t.Helper()
	path := statusPath(t)
	tailer := &givenTailer{givenSession: givenSession{report: finish, err: finishErr}, settle: make(chan []adapter.Request)}
	clock := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	options := Options{Budget: decimal.MustParse("0.05"), Catalog: pinned(t), Now: func() time.Time { return clock }}
	plan := adapter.Plan{Enforcement: adapter.Enforcement{Grade: adapter.GradeWrapperWatchdog, Watchdog: true}}
	session := options.sessionWithStatus("codex", plan, tailer)
	watched, ok := session.(adapter.Tailer)
	if !ok {
		t.Fatal("a watched session lost its tail")
	}
	var files [][]byte
	keep := func() {
		_, data := readStatusFile(t, path)
		files = append(files, data)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a status file exists before the agent wrote a line: %v", err)
	}
	if err := session.Feed([]byte(`{"type":"thread.started"}`), clock); err != nil {
		t.Fatal(err)
	}
	keep()
	tailed := make(chan struct{})
	go func() {
		defer close(tailed)
		watched.Tail(context.Background(), func([]adapter.Request) {})
	}()
	tailer.settle <- []adapter.Request{codexRequest("response-1")}
	tailer.settle <- []adapter.Request{codexRequest("response-1"), codexRequest("response-2")}
	close(tailer.settle)
	<-tailed
	keep()
	if _, err := session.Finish(); !errors.Is(err, finishErr) {
		t.Fatal(err)
	}
	keep()
	return files
}

// drivenUnwatchedStatus writes a status file through the states of a run
// whose agent states its usage only when it ends, and returns each file.
func drivenUnwatchedStatus(t *testing.T) [][]byte {
	t.Helper()
	path := statusPath(t)
	ended := adapter.Report{CostBasis: focus.ListEquivalent, Requests: []adapter.Request{codexRequest("response-1")}}
	options := Options{Budget: decimal.MustParse("5"), Catalog: pinned(t), Now: time.Now}
	plan := adapter.Plan{Enforcement: adapter.Enforcement{Grade: adapter.GradeAgentFlag}}
	session := options.sessionWithStatus("claude-code", plan, &givenSession{report: ended})
	if _, tails := session.(adapter.Tailer); tails {
		t.Fatal("a session with no tail gained one")
	}
	if err := session.Feed([]byte(`{"type":"system","subtype":"init"}`), time.Now()); err != nil {
		t.Fatal(err)
	}
	_, running := readStatusFile(t, path)
	if _, err := session.Finish(); err != nil {
		t.Fatal(err)
	}
	_, final := readStatusFile(t, path)
	return [][]byte{running, final}
}

func TestStatusFileMatchesSchema(t *testing.T) {
	schema := compileStatusSchema(t)
	check := func(name string, data []byte) {
		t.Helper()
		if err := statusSchemaErrors(schema, data); err != nil {
			t.Errorf("%s: %v\n%s", name, err, data)
		}
	}

	measured := adapter.Report{CostBasis: focus.Measured, Requests: []adapter.Request{codexRequest("response-1"), codexRequest("response-2")}}
	for i, data := range drivenStatus(t, measured, nil) {
		check("driven write "+string(rune('1'+i)), data)
	}
	for i, data := range drivenStatus(t, adapter.Report{}, errors.New("the reader refused the stream")) {
		check("refused write "+string(rune('1'+i)), data)
	}

	for i, data := range drivenUnwatchedStatus(t) {
		check("unwatched write "+string(rune('1'+i)), data)
	}

	path := statusPath(t)
	stop := statusWatch(path)
	got, _ := codexRun(t, "replay", "5")
	snapshots := stop()
	if got.err != nil {
		t.Fatal(got.err)
	}
	if len(snapshots) == 0 {
		t.Fatal("the watched Codex run wrote no status file")
	}
	for i, data := range snapshots {
		check("Codex snapshot "+string(rune('1'+i)), data)
	}
	_, final := readStatusFile(t, path)
	check("Codex at its end", final)

	stopped := statusPath(t)
	if got, _ := codexRun(t, "replay", "0.02"); got.err != nil {
		t.Fatal(got.err)
	}
	_, final = readStatusFile(t, stopped)
	check("Codex stopped at its cap", final)

	claude := statusPath(t)
	if got, _ := claudeRun(t, "subagent", subagentSession, pinned(t)); got.err != nil {
		t.Fatal(got.err)
	}
	_, final = readStatusFile(t, claude)
	check("Claude Code at its end", final)
}

func TestStatusFileFollowsAWatchedRunThroughItsStates(t *testing.T) {
	measured := adapter.Report{CostBasis: focus.Measured, Requests: []adapter.Request{codexRequest("response-1"), codexRequest("response-2")}}
	files := drivenStatus(t, measured, nil)
	var states []map[string]any
	for _, data := range files {
		var parsed map[string]any
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatal(err)
		}
		states = append(states, parsed)
	}
	begun, settled, ended := states[0], states[1], states[2]
	if begun["state"] != "running" || begun["requests"] != 0.0 || len(begun["spend"].([]any)) != 0 {
		t.Errorf("before a request settled: %s", files[0])
	}
	if remainder := begun["cap"].(map[string]any)["remainder"]; remainder != "0.05" {
		t.Errorf("an empty run has %v left of 0.05", remainder)
	}
	// Two of request one, from the sheet's gpt-5.6-sol rows per million:
	// 2 × (5624 × 4.00 + 11264 × 0.40 + 106 × 20.00).
	want := []any{map[string]any{"currency": "USD", "cost_basis": nil, "list_cost": "0.0582432"}}
	if settled["state"] != "running" || settled["requests"] != 2.0 || !jsonEqual(settled["spend"], want) {
		t.Errorf("after two requests settled: %s", files[1])
	}
	if overshoot := settled["cap"].(map[string]any)["overshoot"]; overshoot != "0.0082432" {
		t.Errorf("overshoot %v, want 0.0082432", overshoot)
	}
	want[0].(map[string]any)["cost_basis"] = "measured"
	if ended["state"] != "ended" || ended["requests"] != 2.0 || !jsonEqual(ended["spend"], want) {
		t.Errorf("at the end: %s", files[2])
	}
	refused := drivenStatus(t, adapter.Report{}, errors.New("the reader refused the stream"))
	var last map[string]any
	if err := json.Unmarshal(refused[2], &last); err != nil {
		t.Fatal(err)
	}
	if last["state"] != "ended" || last["spend"] != nil || last["requests"] != nil {
		t.Errorf("a refused reader's end states spend: %s", refused[2])
	}
}

func jsonEqual(a, b any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
}

func TestStatusFileTellsAnEmptyRunFromNoRun(t *testing.T) {
	path := statusPath(t)
	got, _ := codexRun(t, "silent", "5")
	if got.err != nil {
		t.Fatal(got.err)
	}
	parsed, data := readStatusFile(t, path)
	if parsed["state"] != "ended" || parsed["requests"] != 0.0 || len(parsed["spend"].([]any)) != 0 {
		t.Errorf("a run that spent nothing reads %s", data)
	}

	missing := statusPath(t)
	t.Setenv("CODEX_HOME", t.TempDir())
	if _, err := Run(t.Context(), Options{
		Argv: []string{filepath.Join(t.TempDir(), "codex"), "exec", "work"}, Budget: decimal.MustParse("5"),
		Rows: &bytes.Buffer{}, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Catalog: pinned(t),
	}); err == nil {
		t.Fatal("a run of an agent that is not there started")
	}
	if _, err := os.Stat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a run whose agent never started left a status file: %v", err)
	}
}

func TestClaudeCodeStatusStatesNoSpendUntilTheAgentEnds(t *testing.T) {
	path := statusPath(t)
	stop := statusWatch(path)
	got, _ := claudeRun(t, "subagent", subagentSession, pinned(t))
	snapshots := stop()
	if got.err != nil {
		t.Fatal(got.err)
	}
	for _, data := range snapshots {
		var parsed map[string]any
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatal(err)
		}
		if parsed["updates"] != "agent_end" {
			t.Errorf("updates %v", parsed["updates"])
		}
		if parsed["state"] == "running" && (parsed["spend"] != nil || parsed["requests"] != nil) {
			t.Errorf("a running Claude Code states spend it cannot see: %s", data)
		}
	}
	parsed, data := readStatusFile(t, path)
	want := []any{map[string]any{"currency": "USD", "cost_basis": "list_equivalent", "list_cost": subagentList}}
	if parsed["state"] != "ended" || !jsonEqual(parsed["spend"], want) || parsed["enforcement_grade"] != "agent-flag" {
		t.Errorf("at the end: %s", data)
	}
	if got.result.ListTotal.String() != subagentList {
		t.Errorf("the result reads %s and the status %s", got.result.ListTotal, subagentList)
	}
}

// canary marks every piece of content the agent's stream carries.
const canary = "canary"

// canaryStream is the one-request recording with a prompt, a model's answer
// and a path from the pull request on its lines.
func canaryStream(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(claudeData, "one-request", "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.NewReplacer(
		`"subtype":"init",`, `"subtype":"init","cwd":"/work/canary-pull-request-path/src",`,
		`"role":"assistant",`, `"role":"assistant","content":[{"type":"text","text":"canary model answer"}],`,
		`"type":"result",`, `"type":"result","result":"canary model answer","errors":["canary error text"],`,
	).Replace(string(data))
	prompt := `{"type":"user","message":{"role":"user","content":"canary prompt: read /work/canary-pull-request-path/src"}}` + "\n"
	path := filepath.Join(t.TempDir(), "stream.jsonl")
	if err := os.WriteFile(path, []byte(prompt+text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// statusWords are the only strings a status file may hold, past amounts,
// currencies and times.
var statusWords = []string{
	status.Schema, "running", "ended", "claude-code", "codex", "each_request", "agent_end",
	"measured", "list_equivalent", "gateway-precheck", "agent-flag", "wrapper-watchdog", "none",
}

var (
	amountPattern   = regexp.MustCompile(`^[0-9]{1,26}(\.[0-9]{1,12})?$`)
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
	timePattern     = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?Z$`)
)

// strangeStrings lists every string in a decoded status that is no word, no
// amount, no currency and no time.
func strangeStrings(value any) []string {
	switch value := value.(type) {
	case string:
		if slices.Contains(statusWords, value) || amountPattern.MatchString(value) || currencyPattern.MatchString(value) || timePattern.MatchString(value) {
			return nil
		}
		return []string{value}
	case map[string]any:
		var strange []string
		for key, inner := range value {
			strange = append(strange, strangeStrings(key)...)
			strange = append(strange, strangeStrings(inner)...)
		}
		return strange
	case []any:
		var strange []string
		for _, inner := range value {
			strange = append(strange, strangeStrings(inner)...)
		}
		return strange
	}
	return nil
}

// statusKeys are the keys a status file holds.
var statusKeys = []string{
	"schema", "state", "agent", "updates", "started_at", "updated_at", "spend", "requests", "cap",
	"enforcement_grade", "currency", "cost_basis", "list_cost", "amount", "remainder", "overshoot",
}

func TestStatusFileHoldsNoContent(t *testing.T) {
	path := statusPath(t)
	installClaudeRun(t, "one-request", "00000000-0000-4000-8000-000000000001")
	t.Setenv("COSTROID_FAKE_STREAM", canaryStream(t))
	stop := statusWatch(path)
	got := runAgent(t, []string{agentNamed(t, "claude"), "-p", "canary prompt"}, "5", pinned(t))
	snapshots := stop()
	if got.err != nil {
		t.Fatal(got.err)
	}
	if !strings.Contains(got.stdout, "canary model answer") {
		t.Fatal("the agent's stream carried no content to leave out")
	}
	_, final := readStatusFile(t, path)
	for _, data := range append(snapshots, final) {
		if bytes.Contains(bytes.ToLower(data), []byte(canary)) || bytes.Contains(data, []byte("/work")) {
			t.Errorf("the status file holds the agent's content: %s", data)
		}
		var parsed any
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatal(err)
		}
		for _, strange := range strangeStrings(parsed) {
			if !slices.Contains(statusKeys, strange) {
				t.Errorf("the status file holds %q, outside its closed lists", strange)
			}
		}
	}
}

// amountKeys are the fields that hold an amount.
var amountKeys = []string{"list_cost", "amount", "remainder", "overshoot"}

// numbersOutsideCounts lists every JSON number in a decoded status that is
// not the request count, and every amount that is not a decimal string.
func numbersOutsideCounts(key string, value any) []string {
	switch value := value.(type) {
	case json.Number:
		if key != "requests" {
			return []string{key + " is the number " + value.String()}
		}
	case string:
		if slices.Contains(amountKeys, key) && !amountPattern.MatchString(value) {
			return []string{key + " is " + value}
		}
	case map[string]any:
		var found []string
		for inner, field := range value {
			found = append(found, numbersOutsideCounts(inner, field)...)
		}
		return found
	case []any:
		var found []string
		for _, inner := range value {
			found = append(found, numbersOutsideCounts(key, inner)...)
		}
		return found
	default:
		if slices.Contains(amountKeys, key) && value != nil {
			return []string{key + " is not a string"}
		}
	}
	return nil
}

func TestStatusFileAmountsAreDecimalStrings(t *testing.T) {
	for _, tc := range []struct {
		name, budget, listCost, remainder, overshoot string
	}{
		// The recording at list, from the sheet's gpt-5.6-sol rows per million:
		// 5875 × 4.00 + 28032 × 0.40 + 111 × 20.00.
		{"under its cap", "5", "0.0369328", "4.9630672", "0"},
		// Request one of the recording: 5624 × 4.00 + 11264 × 0.40 + 106 × 20.00.
		{"stopped past its cap", "0.02", "0.0291216", "0", "0.0091216"},
	} {
		path := statusPath(t)
		if got, _ := codexRun(t, "replay", tc.budget); got.err != nil {
			t.Fatal(got.err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		var parsed map[string]any
		if err := decoder.Decode(&parsed); err != nil {
			t.Fatal(err)
		}
		for _, problem := range numbersOutsideCounts("", parsed) {
			t.Errorf("%s: %s", tc.name, problem)
		}
		spend := parsed["spend"].([]any)
		cap := parsed["cap"].(map[string]any)
		if len(spend) != 1 || spend[0].(map[string]any)["list_cost"] != tc.listCost || spend[0].(map[string]any)["currency"] != "USD" {
			t.Errorf("%s: spend %v, want USD %s", tc.name, spend, tc.listCost)
		}
		if cap["amount"] != tc.budget || cap["remainder"] != tc.remainder || cap["overshoot"] != tc.overshoot || cap["currency"] != "USD" {
			t.Errorf("%s: cap %v, want USD %s with %s left and %s over", tc.name, cap, tc.budget, tc.remainder, tc.overshoot)
		}
	}
}

func TestAgentsPageStatusExampleIsWhatTheRunWrites(t *testing.T) {
	page, err := os.ReadFile("../../docs/agents.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, found := strings.Cut(string(page), "\n## Read the run's status\n")
	example := regexp.MustCompile("(?s)```json\n(.*?)\n```").FindStringSubmatch(section)
	if !found || example == nil {
		t.Fatal("docs/agents.md shows no status")
	}
	path := statusPath(t)
	tailer := &givenTailer{settle: make(chan []adapter.Request, 1)}
	options := Options{Budget: decimal.MustParse("5"), Catalog: pinned(t), Now: time.Now}
	plan := adapter.Plan{Enforcement: adapter.Enforcement{Grade: adapter.GradeWrapperWatchdog, Watchdog: true}}
	session := options.sessionWithStatus("codex", plan, tailer)
	if err := session.Feed([]byte(`{"type":"thread.started"}`), time.Now()); err != nil {
		t.Fatal(err)
	}
	tailer.settle <- []adapter.Request{codexRequest("response-1")}
	close(tailer.settle)
	session.(adapter.Tailer).Tail(context.Background(), func([]adapter.Request) {})
	written, data := readStatusFile(t, path)
	var shown map[string]any
	if err := json.Unmarshal([]byte(example[1]), &shown); err != nil {
		t.Fatal(err)
	}
	for _, times := range []map[string]any{written, shown} {
		delete(times, "started_at")
		delete(times, "updated_at")
	}
	if !jsonEqual(written, shown) {
		t.Errorf("the page shows\n%s\nand the run wrote\n%s", example[1], data)
	}
}

func TestStatusFileEndsWithTheEarnedGrade(t *testing.T) {
	for _, tc := range []struct {
		name, keyInfo, readiness string
	}{
		{"the gateway's store failed", keyInfoHolding, `{"status":"healthy","db":"disconnected"}`},
		{"the run passed the overshoot bound", keyInfoWith("0.0001", "100"), `{"status":"healthy","db":"connected"}`},
		{"the gateway held the run", keyInfoHolding, `{"status":"healthy","db":"connected"}`},
	} {
		path := statusPath(t)
		got, _ := gatewayRunWith(t, fakeGateway(t, tc.keyInfo, tc.readiness))
		if got.err != nil {
			t.Fatalf("%s: %v", tc.name, got.err)
		}
		parsed, data := readStatusFile(t, path)
		if parsed["state"] != "ended" || parsed["enforcement_grade"] != got.result.Enforcement.Grade {
			t.Errorf("%s: the result reads %s and the status file %s", tc.name, got.result.Enforcement.Grade, data)
		}
	}
}
