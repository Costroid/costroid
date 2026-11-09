package copilot_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/adapter/copilot"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/run"
)

// recorded is dogfood run 42629874786: Copilot CLI 1.0.103 on the user's own
// Anthropic key, claude-haiku-5-5, three model requests, recorded with
// scripts/record. Its README states the run's own totals.
var recorded = filepath.Join("testdata", "1.0.103", "byok")

// recordedSession is the synthetic session id the recording's files carry.
const recordedSession = "00000000-0000-4000-8000-000000000001"

// line is one event of a session file, its numbers kept as written.
type line = map[string]any

func readLines(t *testing.T, name string) []line {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(recorded, name))
	if err != nil {
		t.Fatal(err)
	}
	var lines []line
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(nil, 1<<20)
	for scanner.Scan() {
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.UseNumber()
		var l line
		if err := decoder.Decode(&l); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, l)
	}
	return lines
}

func encodeLines(t *testing.T, lines []line) []byte {
	t.Helper()
	var out bytes.Buffer
	for _, l := range lines {
		data, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(append(data, '\n'))
	}
	return out.Bytes()
}

// editedEvents is the recorded session file after edit has changed its
// events.
func editedEvents(t *testing.T, edit func(events []line)) []byte {
	t.Helper()
	events := readLines(t, "events.jsonl")
	edit(events)
	return encodeLines(t, events)
}

// ofType lists the events of one type, in order.
func ofType(events []line, kind string) []line {
	var found []line
	for _, event := range events {
		if event["type"] == kind {
			found = append(found, event)
		}
	}
	return found
}

func object(t *testing.T, l line, path ...string) line {
	t.Helper()
	current := l
	for _, key := range path {
		next, ok := current[key].(map[string]any)
		if !ok {
			t.Fatalf("no object at %s", strings.Join(path, "."))
		}
		current = next
	}
	return current
}

func usageOf(t *testing.T, record line) line {
	return object(t, record, "data", "usage")
}

func metricOf(t *testing.T, events []line, model string) line {
	t.Helper()
	return object(t, ofType(events, "session.shutdown")[0], "data", "modelMetrics", model)
}

func count(t *testing.T, l line, key string) int64 {
	t.Helper()
	number, ok := l[key].(json.Number)
	if !ok {
		t.Fatalf("%s is not a number", key)
	}
	value, err := number.Int64()
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func readEvents(t *testing.T, data []byte, keyed bool) adapter.Report {
	t.Helper()
	report, err := copilot.ReadEvents(bytes.NewReader(data), keyed)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func recordedEvents(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(recorded, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// runSession reads a run as costroid run does: the planned command, the
// stream fed line by line, and the session file under COPILOT_HOME.
func runSession(t *testing.T, events, stream []byte) (adapter.Report, error) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "session-state", recordedSession)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), events, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COPILOT_HOME", home)
	agent := copilot.Agent{}
	plan, err := agent.Plan([]string{"copilot", "-p", "a prompt"}, decimal.MustParse("1"))
	if err != nil {
		t.Fatal(err)
	}
	session := agent.NewSession(plan)
	for _, raw := range bytes.Split(stream, []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		if err := session.Feed(raw, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	return session.Finish()
}

func recordedStream(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(recorded, "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// writtenRows prices the report as costroid run does.
func writtenRows(t *testing.T, report adapter.Report) []byte {
	t.Helper()
	pinned, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	var written bytes.Buffer
	if _, err := run.Record(run.Options{Rows: &written, Catalog: pinned}, copilot.Name, report); err != nil {
		t.Fatal(err)
	}
	return written.Bytes()
}

// row is the part of a FOCUS row these tests read.
type row struct {
	ListCost          string            `json:"ListCost"`
	EffectiveCost     string            `json:"EffectiveCost"`
	ChargeDescription string            `json:"ChargeDescription"`
	Tags              map[string]string `json:"Tags"`
}

func rowsOf(t *testing.T, written []byte) []row {
	t.Helper()
	var rows []row
	for _, raw := range bytes.Split(bytes.TrimSpace(written), []byte("\n")) {
		var r row
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, r)
	}
	return rows
}

func listTotal(t *testing.T, rows []row) decimal.Decimal {
	t.Helper()
	var total decimal.Decimal
	for _, r := range rows {
		cost, err := decimal.Parse(r.ListCost)
		if err != nil {
			t.Fatal(err)
		}
		total = total.Add(cost)
	}
	return total
}

// billedInput is a request's whole input across its billed classes.
func billedInput(tokens adapter.Tokens) int64 {
	return tokens.InputUncached + tokens.CacheRead + tokens.CacheWrite5m + tokens.CacheWrite1h + tokens.CacheWrite30m
}

// TestCopilotUsageRowsEqualShutdownMetrics reads the recorded run: its three
// usage records give three requests, and their sums equal session.shutdown's
// modelMetrics as the file states them, read here with no help from the
// reader. The rows price to USD 0.00329423 at the pinned catalog: 8 uncached
// input tokens at 0.10, 43418 cache reads at 0.01, 21958 five-minute writes at
// 0.125 and 229 output tokens at 0.50, each per million. A usage record or a
// shutdown total that differs by one token, a request count that differs and
// a model the shutdown does not name each refuse the file with the model and
// the field, and no request reaches a row.
func TestCopilotUsageRowsEqualShutdownMetrics(t *testing.T) {
	report, err := runSession(t, recordedEvents(t), recordedStream(t))
	if err != nil {
		t.Fatal(err)
	}
	events := readLines(t, "events.jsonl")
	metric := object(t, metricOf(t, events, "claude-haiku-5-5"), "usage")
	if len(report.Requests) != 3 || count(t, object(t, metricOf(t, events, "claude-haiku-5-5"), "requests"), "count") != 3 {
		t.Fatalf("read %d requests against the shutdown's 3", len(report.Requests))
	}
	var sum adapter.Tokens
	for _, request := range report.Requests {
		if request.Model != "claude-haiku-5-5" || request.Provider != "anthropic" || request.Grain != adapter.PerRequest {
			t.Errorf("request %s reads %s on %s at grain %s", request.ID, request.Model, request.Provider, request.Grain)
		}
		sum = sum.Add(request.Tokens)
	}
	for _, check := range []struct {
		field string
		got   int64
	}{
		{"inputTokens", billedInput(sum)},
		{"cacheReadTokens", sum.CacheRead},
		{"cacheWriteTokens", sum.CacheWrite5m},
		{"outputTokens", sum.Output},
		{"reasoningTokens", sum.ReasoningOutput},
	} {
		if want := count(t, metric, check.field); check.got != want {
			t.Errorf("the requests sum %s to %d; session.shutdown states %d", check.field, check.got, want)
		}
	}
	if total := listTotal(t, rowsOf(t, writtenRows(t, report))); total.Cmp(decimal.MustParse("0.00329423")) != 0 {
		t.Errorf("the rows come to USD %s at list, not 0.00329423", total)
	}

	refusals := []struct {
		name  string
		edit  func(events []line)
		words []string
	}{
		{"a usage record one token over", func(events []line) {
			usageOf(t, ofType(events, "session.usage_record")[1])["outputTokens"] = json.Number("74")
		}, []string{"claude-haiku-5-5 outputTokens: 230 in the usage records, 229 in session.shutdown"}},
		{"a shutdown total one token under", func(events []line) {
			object(t, metricOf(t, events, "claude-haiku-5-5"), "usage")["cacheReadTokens"] = json.Number("43417")
		}, []string{"claude-haiku-5-5 cacheReadTokens: 43418 in the usage records, 43417 in session.shutdown"}},
		{"a usage record lost", func(events []line) {
			record := ofType(events, "session.usage_record")[2]
			record["type"] = "session.usage_record_lost"
		}, []string{"claude-haiku-5-5 inputTokens: 43424 in the usage records, 65384 in session.shutdown", "claude-haiku-5-5 requests: 2 usage records, a requests.count of 3"}},
		{"a request count that differs", func(events []line) {
			object(t, metricOf(t, events, "claude-haiku-5-5"), "requests")["count"] = json.Number("4")
		}, []string{"claude-haiku-5-5 requests: 3 usage records, a requests.count of 4 in session.shutdown"}},
		{"a model the shutdown does not name", func(events []line) {
			usageOf(t, ofType(events, "session.usage_record")[0])["model"] = "claude-sonnet-5"
		}, []string{"claude-sonnet-5 has 1 usage record and no modelMetrics entry"}},
	}
	for _, refusal := range refusals {
		t.Run(refusal.name, func(t *testing.T) {
			report, err := copilot.ReadEvents(bytes.NewReader(editedEvents(t, refusal.edit)), false)
			if err == nil {
				t.Fatalf("read %d requests from a file that disagrees with its own totals", len(report.Requests))
			}
			for _, words := range refusal.words {
				if !strings.Contains(err.Error(), words) {
					t.Errorf("the refusal lacks %q: %v", words, err)
				}
			}
			if len(report.Requests) != 0 {
				t.Errorf("a refused file still gave %d requests", len(report.Requests))
			}
		})
	}
}

// providerVariables are Copilot CLI's variables that name a provider and its
// credential.
var providerVariables = []string{"COPILOT_PROVIDER_BASE_URL", "COPILOT_PROVIDER_API_KEY", "COPILOT_PROVIDER_API_KEY_COMMAND", "COPILOT_PROVIDER_BEARER_TOKEN"}

// TestCopilotByokReadsMeasured reads the recorded run as measured: every
// usage record states isByok on a remote provider. A record that does not
// state isByok follows the provider variables: a base URL and a key read
// measured, and a base URL alone does not. Copilot's own routing reads
// list_equivalent with a note on each row, a model on the device reads
// list_equivalent, and a run with both kinds reads list_equivalent with a
// warning.
func TestCopilotByokReadsMeasured(t *testing.T) {
	for _, name := range providerVariables {
		t.Setenv(name, "")
	}
	report, err := runSession(t, recordedEvents(t), recordedStream(t))
	if err != nil {
		t.Fatal(err)
	}
	if report.CostBasis != focus.Measured {
		t.Fatalf("the recorded run reads %s", report.CostBasis)
	}
	for _, r := range rowsOf(t, writtenRows(t, report)) {
		if r.Tags[focus.TagCostBasis] != string(focus.Measured) || r.Tags[focus.TagAgent] != copilot.Name {
			t.Errorf("a row reads %s for %s", r.Tags[focus.TagCostBasis], r.Tags[focus.TagAgent])
		}
	}

	unstated := editedEvents(t, func(events []line) {
		for _, record := range ofType(events, "session.usage_record") {
			delete(usageOf(t, record), "isByok")
		}
	})
	for _, variables := range []struct {
		name string
		set  map[string]string
		want focus.CostBasis
	}{
		{"a base URL and an API key", map[string]string{"COPILOT_PROVIDER_BASE_URL": "https://api.anthropic.com", "COPILOT_PROVIDER_API_KEY": "planted"}, focus.Measured},
		{"a base URL and a bearer token", map[string]string{"COPILOT_PROVIDER_BASE_URL": "https://api.anthropic.com", "COPILOT_PROVIDER_BEARER_TOKEN": "planted"}, focus.Measured},
		{"a base URL and a key command", map[string]string{"COPILOT_PROVIDER_BASE_URL": "https://api.anthropic.com", "COPILOT_PROVIDER_API_KEY_COMMAND": "printf planted"}, focus.Measured},
		{"a base URL alone", map[string]string{"COPILOT_PROVIDER_BASE_URL": "http://localhost:11434/v1"}, focus.ListEquivalent},
		{"a key alone", map[string]string{"COPILOT_PROVIDER_API_KEY": "planted"}, focus.ListEquivalent},
		{"no variable", nil, focus.ListEquivalent},
	} {
		t.Run(variables.name, func(t *testing.T) {
			for _, name := range providerVariables {
				t.Setenv(name, variables.set[name])
			}
			report, err := runSession(t, unstated, recordedStream(t))
			if err != nil {
				t.Fatal(err)
			}
			if report.CostBasis != variables.want {
				t.Errorf("read %s, want %s", report.CostBasis, variables.want)
			}
		})
	}

	served := readEvents(t, editedEvents(t, func(events []line) {
		for _, record := range ofType(events, "session.usage_record") {
			usage := usageOf(t, record)
			usage["isByok"] = false
			delete(usage, "byokKind")
			delete(usage, "modelProvider")
		}
	}), true)
	if served.CostBasis != focus.ListEquivalent || len(served.Requests) != 3 || served.Requests[0].Provider != "anthropic" || !strings.Contains(served.Requests[0].Note, "Copilot served this request") {
		t.Errorf("Copilot's own routing read %s with %d requests, the first on %q noted %q", served.CostBasis, len(served.Requests), served.Requests[0].Provider, served.Requests[0].Note)
	}

	local := readEvents(t, editedEvents(t, func(events []line) {
		for _, record := range ofType(events, "session.usage_record") {
			usageOf(t, record)["byokKind"] = "local_user"
		}
	}), true)
	if local.CostBasis != focus.ListEquivalent {
		t.Errorf("a model on the device read %s", local.CostBasis)
	}

	mixed := readEvents(t, editedEvents(t, func(events []line) {
		usageOf(t, ofType(events, "session.usage_record")[0])["isByok"] = false
	}), true)
	if mixed.CostBasis != focus.ListEquivalent || !slices.ContainsFunc(mixed.Warnings, func(w string) bool {
		return strings.Contains(w, "2 requests billed the user's own provider key and 1 request did not")
	}) {
		t.Errorf("a mixed run read %s with the warnings %q", mixed.CostBasis, mixed.Warnings)
	}
}

// plantedPaths are file paths as Copilot writes them into codeChanges.
var plantedPaths = []string{"/home/runner/work/app/app/internal/secret_plan.go", "src/planted-file-name.ts"}

// TestCopilotScrubDropsFilesModified holds file paths out of every row and
// every fixture. The recorded fixture holds no filesModified key and no path,
// and a session whose session.shutdown and result line both list modified
// files reads to rows, a report and warnings that name none of them.
func TestCopilotScrubDropsFilesModified(t *testing.T) {
	err := filepath.WalkDir("testdata", func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, forbidden := range []string{"filesModified", "/home/", "/Users/", "/runner/work/", "/tmp/", "costroid-dogfood"} {
			if bytes.Contains(data, []byte(forbidden)) {
				t.Errorf("%s holds %q", path, forbidden)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	paths := make([]any, len(plantedPaths))
	for i, path := range plantedPaths {
		paths[i] = path
	}
	events := editedEvents(t, func(events []line) {
		object(t, ofType(events, "session.shutdown")[0], "data", "codeChanges")["filesModified"] = paths
		object(t, ofType(events, "session.shutdown")[0], "data")["accountingSnapshot"] = map[string]any{"codeChanges": map[string]any{"filesModified": paths, "filesModifiedCount": len(paths)}}
	})
	stream := readLines(t, "stream.jsonl")
	object(t, stream[len(stream)-1], "usage", "codeChanges")["filesModified"] = paths
	report, err := runSession(t, events, encodeLines(t, stream))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Requests) != 3 {
		t.Fatalf("read %d requests", len(report.Requests))
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	everything := string(encoded) + string(writtenRows(t, report))
	for _, path := range plantedPaths {
		if strings.Contains(everything, path) {
			t.Errorf("the report or a row names the modified file %s", path)
		}
		if strings.Contains(everything, filepath.Base(path)) {
			t.Errorf("the report or a row names the modified file's name %s", filepath.Base(path))
		}
	}
}

// TestCopilotCreditsAreNeverConverted states Copilot's own credits as it
// wrote them and keeps them out of every row. The recorded run on the user's
// key states a totalNanoAiu of 0 and a requests.cost of 0, and both are said,
// with Copilot's note that unavailable credits are not a zero cost. A session
// that states 2500000000 nano-AI units and a cost of 1.5 writes rows
// identical to the recorded run's, and its figure reads in nano-AI units,
// never as dollars or as 2.5 credits.
func TestCopilotCreditsAreNeverConverted(t *testing.T) {
	report := readEvents(t, recordedEvents(t), false)
	want := adapter.Stated{Unit: copilot.UnitNanoAIU, Quantity: "0", Source: copilot.Name}
	if report.AgentStated == nil || *report.AgentStated != want {
		t.Fatalf("the recorded run states %+v, want %+v", report.AgentStated, want)
	}
	sentence := "Copilot CLI stated a totalNanoAiu of 0 nano-AI units, and a requests.cost of 0 for claude-haiku-5-5. They are Copilot's own units: Costroid never converts them to dollars, and no row holds them. Every request's aiCreditsStatus reads unavailable, which Copilot documents as no amount reported, not a zero cost"
	if !slices.Contains(report.Warnings, sentence) {
		t.Errorf("the recorded run does not state its credits as 0:\n%q", report.Warnings)
	}
	recordedRows := writtenRows(t, report)

	charged := readEvents(t, editedEvents(t, func(events []line) {
		data := object(t, ofType(events, "session.shutdown")[0], "data")
		data["totalNanoAiu"] = json.Number("2500000000")
		object(t, metricOf(t, events, "claude-haiku-5-5"), "requests")["cost"] = json.Number("1.5")
		for _, record := range ofType(events, "session.usage_record") {
			usageOf(t, record)["cost"] = json.Number("0.5")
			usageOf(t, record)["aiCreditsStatus"] = "complete"
		}
	}), false)
	want.Quantity = "2500000000"
	if charged.AgentStated == nil || *charged.AgentStated != want {
		t.Errorf("a charged run states %+v, want %+v", charged.AgentStated, want)
	}
	if !slices.Contains(charged.Warnings, "Copilot CLI stated a totalNanoAiu of 2500000000 nano-AI units, and a requests.cost of 1.5 for claude-haiku-5-5. They are Copilot's own units: Costroid never converts them to dollars, and no row holds them") {
		t.Errorf("a charged run's credits are not stated as written:\n%q", charged.Warnings)
	}
	chargedRows := writtenRows(t, charged)
	if !bytes.Equal(chargedRows, recordedRows) {
		t.Errorf("Copilot's credits changed the rows:\n%s\nagainst\n%s", chargedRows, recordedRows)
	}
	for _, figure := range []string{"2500000000", "2.5", "1.5", "nano"} {
		if bytes.Contains(chargedRows, []byte(figure)) {
			t.Errorf("a row holds %q", figure)
		}
	}

	silent := readEvents(t, editedEvents(t, func(events []line) {
		data := object(t, ofType(events, "session.shutdown")[0], "data")
		delete(data, "totalNanoAiu")
		delete(object(t, metricOf(t, events, "claude-haiku-5-5"), "requests"), "cost")
	}), false)
	if silent.AgentStated != nil || !slices.ContainsFunc(silent.Warnings, func(w string) bool {
		return strings.HasPrefix(w, "Copilot CLI stated no totalNanoAiu, and no requests.cost for claude-haiku-5-5.")
	}) {
		t.Errorf("a run that states no credits reads %+v with the warnings %q", silent.AgentStated, silent.Warnings)
	}
}

// TestCopilotInputIncludesTheCache holds the reading the recording proves:
// each usage record's inputTokens is its uncached input plus its cache reads
// and writes, so the requests' uncached input is 4, 2 and 2 tokens, 8 in all,
// and each request's whole input is its inputTokens. A record whose
// inputTokens is below its cache reads and writes cannot be that, and the
// file is refused.
func TestCopilotInputIncludesTheCache(t *testing.T) {
	report := readEvents(t, recordedEvents(t), false)
	records := ofType(readLines(t, "events.jsonl"), "session.usage_record")
	if len(report.Requests) != len(records) {
		t.Fatalf("read %d requests from %d records", len(report.Requests), len(records))
	}
	uncached := []int64{4, 2, 2}
	for i, request := range report.Requests {
		usage := usageOf(t, records[i])
		input := count(t, usage, "inputTokens")
		if request.Tokens.InputUncached != uncached[i] || billedInput(request.Tokens) != input || request.ContextInputTokens != input {
			t.Errorf("request %d: uncached %d, billed input %d, context %d; the record states inputTokens %d, cache reads %d, cache writes %d",
				i+1, request.Tokens.InputUncached, billedInput(request.Tokens), request.ContextInputTokens, input, count(t, usage, "cacheReadTokens"), count(t, usage, "cacheWriteTokens"))
		}
		if request.Tokens.CacheWrite5m != count(t, usage, "cacheWriteTokens") || request.AssumedWrite != focus.CacheWrite5m {
			t.Errorf("request %d's writes read %d at %q", i+1, request.Tokens.CacheWrite5m, request.AssumedWrite)
		}
	}

	_, err := copilot.ReadEvents(bytes.NewReader(editedEvents(t, func(events []line) {
		usageOf(t, ofType(events, "session.usage_record")[1])["inputTokens"] = json.Number("21761")
		object(t, metricOf(t, events, "claude-haiku-5-5"), "usage")["inputTokens"] = json.Number("65381")
	})), false)
	if err == nil || !strings.Contains(err.Error(), "inputTokens 21761 is below its 21656 cache reads and 106 cache writes") {
		t.Errorf("a record whose input cannot hold its cache read: %v", err)
	}
}

// TestCopilotPlanAddsJSONAndRefusesResume plans a prompt with the json
// stream, keeps a json stream the command asks for, and refuses a command
// that is not a prompt, another stream, and every flag that resumes a
// session. A prompt that reads like a flag is the prompt's text.
func TestCopilotPlanAddsJSONAndRefusesResume(t *testing.T) {
	budget := decimal.MustParse("1")
	for _, planned := range []struct{ argv, want []string }{
		{[]string{"copilot", "-p", "review"}, []string{"copilot", "--output-format", "json", "-p", "review"}},
		{[]string{"copilot", "--prompt=review", "--output-format=json"}, []string{"copilot", "--prompt=review", "--output-format=json"}},
		{[]string{"copilot", "-p", "--continue", "--model", "claude-haiku-5-5"}, []string{"copilot", "--output-format", "json", "-p", "--continue", "--model", "claude-haiku-5-5"}},
	} {
		plan, err := copilot.Agent{}.Plan(planned.argv, budget)
		if err != nil {
			t.Errorf("%q: %v", planned.argv, err)
			continue
		}
		if !slices.Equal(plan.Argv, planned.want) || plan.Enforcement.Grade != adapter.GradeNone {
			t.Errorf("%q planned %q at %s", planned.argv, plan.Argv, plan.Enforcement.Grade)
		}
	}
	for _, refused := range [][]string{
		{"copilot"},
		{"copilot", "-i", "review"},
		{"copilot", "-p", "review", "--output-format", "text"},
		{"copilot", "-p", "review", "--resume"},
		{"copilot", "-p", "review", "-r", "0cb916d"},
		{"copilot", "-p", "review", "--continue"},
		{"copilot", "-p", "review", "--session-id", recordedSession},
		{"copilot", "-p", "review", "--session-id=" + recordedSession},
		{"copilot", "-p", "review", "--connect"},
	} {
		if plan, err := (copilot.Agent{}).Plan(refused, budget); err == nil {
			t.Errorf("%q planned %q", refused, plan.Argv)
		}
	}
}

// TestCopilotRunNeedsTheResultLine refuses a run whose stream names no
// session, and one whose session file names another.
func TestCopilotRunNeedsTheResultLine(t *testing.T) {
	stream := readLines(t, "stream.jsonl")
	if _, err := runSession(t, recordedEvents(t), encodeLines(t, stream[:len(stream)-1])); err == nil || !strings.Contains(err.Error(), "no result line naming the session") {
		t.Errorf("a stream with no result line: %v", err)
	}
	other := editedEvents(t, func(events []line) {
		object(t, ofType(events, "session.start")[0], "data")["sessionId"] = "00000000-0000-4000-8000-000000000002"
	})
	if _, err := runSession(t, other, recordedStream(t)); err == nil || !strings.Contains(err.Error(), "names another session") {
		t.Errorf("a session file of another session: %v", err)
	}
}
