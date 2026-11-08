package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/adapter/claudecode"
	"github.com/Costroid/costroid/internal/adapter/codex"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/reason"
)

// sheetPrice asks the pinned sheet for one gpt-5.6-sol class at a context size.
func sheetPrice(t *testing.T, c *catalog.Catalog, class focus.TokenClass, input int64) catalog.Price {
	t.Helper()
	price, err := c.Lookup(catalog.Query{
		Provider: "openai", Model: "gpt-5.6-sol", Class: class,
		ServiceTier: catalog.TierStandard, Region: catalog.RegionGlobal,
		ContextInputTokens: input, At: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return price
}

// TestCodexTurnSumNeverTakesTheLongTier runs an unwatched Codex turn on each
// side of the sheet's context tier boundary, 272000 input tokens on the pinned
// gpt-5.6-sol sheet, with 1000 output tokens. The boundary comes from the
// sheet. A turn's input is a sum over its requests, and the stream states no
// request's own. At the boundary every request is in the lowest tier, and the
// row says so. Past it no single request is shown past the boundary, so the
// lowest tier still prices the turn, and the row says the tier is unknown and
// the figure may read low, never an upper bound.
func TestCodexTurnSumNeverTakesTheLongTier(t *testing.T) {
	c := pinned(t)
	inputPrice, outputPrice := sheetPrice(t, c, focus.InputUncached, 1), sheetPrice(t, c, focus.Output, 1)
	if inputPrice.ContextTierMax == nil {
		t.Fatal("the sheet states no context tier for gpt-5.6-sol")
	}
	boundary := *inputPrice.ContextTierMax
	if above := sheetPrice(t, c, focus.InputUncached, boundary+1); above.SkuPriceID == inputPrice.SkuPriceID {
		t.Fatalf("the sheet prices %d input tokens in the lowest tier", boundary+1)
	}
	for name, tc := range map[string]struct {
		input int64
		says  string
	}{
		"at the boundary": {boundary, fmt.Sprintf("one turn's input of %d tokens is within the %d-token context tier, so each of its requests is too", boundary, boundary)},
		"past the boundary": {boundary + 1, fmt.Sprintf("one turn's input of %d tokens is a sum over its requests and passes the %d-token context tier boundary, "+
			"and its requests' own inputs are unknown, so its context tier is unknown, and the prices up to %d input tokens apply, which may read low", boundary+1, boundary, boundary)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(fakeStreamInput, strconv.FormatInt(tc.input, 10))
			got, _ := codexRun(t, "one-turn", "1", "--ephemeral")
			if got.err != nil {
				t.Fatal(got.err)
			}
			if got.result.Enforcement.Grade != adapter.GradeNone || len(got.rows) != 2 {
				t.Fatalf("%+v, %d rows", got.result.Enforcement, len(got.rows))
			}
			for _, row := range got.rows {
				want := inputPrice.SkuPriceID
				if row["SkuMeter"] == string(focus.Output) {
					want = outputPrice.SkuPriceID
				}
				if row["SkuPriceId"] != want {
					t.Errorf("%v priced at %v, want %s", row["SkuMeter"], row["SkuPriceId"], want)
				}
				text, _ := row["ChargeDescription"].(string)
				if !strings.Contains(text, tc.says) || strings.Contains(text, "upper bound") {
					t.Errorf("the row does not say %q: %q", tc.says, text)
				}
			}
			want := inputPrice.ListUnitPrice.Mul(decimal.FromInt(tc.input).DivPow10(6)).
				Add(outputPrice.ListUnitPrice.Mul(decimal.FromInt(1000).DivPow10(6)))
			if err := checkListTotal(got.result.ListTotal, want.String()); err != nil {
				t.Error(err)
			}
		})
	}
}

// codexHomeRollout is a Codex home whose one rollout file settles two
// requests by their token_count events: 300000 input and 100 output tokens,
// then 20000 input and 50 output, on gpt-5.6-sol.
func codexHomeRollout(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	directory := filepath.Join(home, "sessions", "2026", "11", "08")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	count := func(at string, total, last [2]int64) string {
		usage := func(counts [2]int64) string {
			return fmt.Sprintf(`{"input_tokens":%d,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":%d,"reasoning_output_tokens":0,"total_tokens":%d}`,
				counts[0], counts[1], counts[0]+counts[1])
		}
		return `{"timestamp":"` + at + `","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":` + usage(total) + `,"last_token_usage":` + usage(last) + `}}}`
	}
	lines := []string{
		`{"timestamp":"2026-11-08T10:00:00.000Z","type":"session_meta","payload":{"id":"00000000-0000-4000-8000-0000000000c1","cli_version":"0.164.0-alpha.11.1"}}`,
		`{"timestamp":"2026-11-08T10:00:00.100Z","type":"turn_context","payload":{"model":"gpt-5.6-sol"}}`,
		count("2026-11-08T10:00:05.000Z", [2]int64{300000, 100}, [2]int64{300000, 100}),
		count("2026-11-08T10:00:09.000Z", [2]int64{320000, 150}, [2]int64{20000, 50}),
	}
	path := filepath.Join(directory, "rollout-2026-11-08T10-00-00-00000000-0000-4000-8000-0000000000c1.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

// TestCodexRequestOverTheBoundaryTakesTheLongTier reads a Codex home whose
// rollout file states each request's own usage. A request of 300000 input
// tokens is past the sheet's 272000-token boundary, so the tier above prices
// its rows; a request of 20000 in the same turn is priced in the lowest tier.
// Each row is one request's, so neither says anything about a context tier.
func TestCodexRequestOverTheBoundaryTakesTheLongTier(t *testing.T) {
	report, err := codex.Agent{}.ReadHome(codexHomeRollout(t), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Requests) != 2 || report.Requests[0].Grain != adapter.PerRequest ||
		report.Requests[0].ContextInputTokens != 300000 || report.Requests[1].ContextInputTokens != 20000 {
		t.Fatalf("requests %+v", report.Requests)
	}
	var written bytes.Buffer
	result, err := Record(Options{Rows: &written, Catalog: pinned(t)}, codex.Agent{}.Name(), report)
	if err != nil {
		t.Fatal(err)
	}
	c := pinned(t)
	want := decimal.Decimal{}
	priceOf := map[string]string{}
	for i, request := range report.Requests {
		for _, count := range request.Tokens.Counts() {
			price := sheetPrice(t, c, count.Class, request.ContextInputTokens)
			want = want.Add(price.ListUnitPrice.Mul(decimal.FromInt(count.Tokens).DivPow10(6)))
			priceOf[focus.RowIdentity(request.ID, count.Class)] = price.SkuPriceID
			if long := strings.Contains(price.SkuPriceID, ":over-272000:"); long != (i == 0) {
				t.Errorf("the sheet prices request %d's %s at %s", i+1, count.Class, price.SkuPriceID)
			}
		}
	}
	rows := parseRows(t, written.Bytes())
	if len(rows) != len(priceOf) {
		t.Fatalf("%d rows, want %d", len(rows), len(priceOf))
	}
	for _, row := range rows {
		if sku, want := row["SkuPriceId"], priceOf[row["x_CostroidRowId"].(string)]; sku != want {
			t.Errorf("%v %v tokens priced at %v, want %s", row["ConsumedQuantity"], row["SkuMeter"], sku, want)
		}
		if text := row["ChargeDescription"].(string); strings.Contains(text, "context tier") || strings.Contains(text, "upper bound") {
			t.Errorf("a request's own row speaks of a context tier: %q", text)
		}
	}
	if err := checkListTotal(result.ListTotal, want.String()); err != nil {
		t.Error(err)
	}
}

const (
	actionRun      = "../adapter/claudecode/testdata/2.1.316/action-session/claude-execution-output.json"
	actionSession  = "../adapter/claudecode/testdata/2.1.316/action-session/session.jsonl"
	actionID       = "00000000-0000-4000-8000-000000000001"
	actionRunTotal = "0.0391529"
)

// readActionRun reads the recorded claude-code-action run with the session
// file its runner held beside it, as the given text.
func readActionRun(t *testing.T, session string) adapter.Report {
	t.Helper()
	outsideCIForRun(t)
	config := t.TempDir()
	project := filepath.Join(config, "projects", "-work")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, actionID+".jsonl"), []byte(session), 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(actionRun)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	report, err := claudecode.Agent{ConfigDir: config}.ReadExecutionFile(file, time.Date(2026, 11, 7, 11, 33, 30, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func outsideCIForRun(t *testing.T) {
	t.Helper()
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("GITLAB_CI", "")
}

// TestNoUnexplainedSessionWarning reads the recorded claude-code-action run.
// Its result counts a claude-haiku-5-5 request that no message names, which
// Claude Code makes outside the conversation. The session file agrees on every
// model it holds, so the run reads with no warning: per-request rows for those,
// and the result's total for the other, saying where it came from. The rows
// come to the run's own total_cost_usd.
func TestNoUnexplainedSessionWarning(t *testing.T) {
	report := readActionRun(t, string(mustRead(t, actionSession)))
	if len(report.Warnings) != 0 {
		t.Fatalf("warnings %q", report.Warnings)
	}
	if report.CostSource != claudecode.SourceSessionFile || len(report.Requests) != 3 {
		t.Fatalf("%s, %d entries", report.CostSource, len(report.Requests))
	}
	for _, request := range report.Requests {
		side := request.Model == "claude-haiku-5-5"
		if side != (request.Grain == adapter.PerRun) || side != (request.CostSource == claudecode.SourceExecutionFile) || side != (request.Note != "") {
			t.Errorf("%s: grain %s, source %q, note %q", request.Model, request.Grain, request.CostSource, request.Note)
		}
	}
	var rows bytes.Buffer
	result, err := Record(Options{Rows: &rows, Catalog: pinned(t)}, claudecode.Name, report)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkListTotal(result.ListTotal, actionRunTotal); err != nil {
		t.Error(err)
	}
	for _, row := range parseRows(t, rows.Bytes()) {
		text, _ := row["ChargeDescription"].(string)
		if tagOf(row, focus.TagModel) == "claude-haiku-5-5" {
			if tagOf(row, focus.TagCostSource) != claudecode.SourceExecutionFile || !strings.Contains(text, "outside the conversation") || !strings.Contains(text, "input of 1200 tokens is within the 100000-token context tier") {
				t.Errorf("the side request's row: %s, %q", tagOf(row, focus.TagCostSource), text)
			}
		} else if tagOf(row, focus.TagCostSource) != claudecode.SourceSessionFile {
			t.Errorf("a session file row reads %s", tagOf(row, focus.TagCostSource))
		}
	}
}

// TestASessionFileThatDisagreesStillWarns keeps the check the side model rule
// narrows: a model the file holds must still add up to the result.
func TestASessionFileThatDisagreesStillWarns(t *testing.T) {
	session := strings.Replace(string(mustRead(t, actionSession)), `"output_tokens":97`, `"output_tokens":96`, 1)
	report := readActionRun(t, session)
	if len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0], "disagree") {
		t.Fatalf("warnings %q", report.Warnings)
	}
	if report.CostSource != claudecode.SourceExecutionFile || len(report.Requests) != 2 {
		t.Fatalf("%s, %d entries", report.CostSource, len(report.Requests))
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// The two shapes of SkuPriceId that docs/adapters.md documents. Both open with
// the provider and close with the token class.
var (
	unitPriceID = regexp.MustCompile(`^([a-z]+):([A-Za-z0-9._-]+):([a-z0-9-]+):([a-z]+):([a-z0-9-]+):([a-z0-9_]+)$`)
	kindPriceID = regexp.MustCompile(`^([a-z]+):(fallback|informational):([a-z0-9_]+)$`)
)

const (
	adaptersDoc    = "../../docs/adapters.md"
	unitPriceShape = "`<provider>:<model>:<region>:<tier>:<context>:<class>`"
	kindPriceShape = "`<provider>:<kind>:<class>`"
)

// priceIDShapeError says why a row's SkuPriceId has neither documented shape,
// or names a provider, model or class other than its SkuId's.
func priceIDShapeError(row map[string]any) error {
	id, _ := row["SkuPriceId"].(string)
	sku := strings.Split(row["SkuId"].(string), "/")
	if match := unitPriceID.FindStringSubmatch(id); match != nil {
		if match[1] != sku[0] || match[2] != sku[1] || match[6] != sku[2] {
			return fmt.Errorf("%s does not name the SkuId %v", id, sku)
		}
		return nil
	}
	if match := kindPriceID.FindStringSubmatch(id); match != nil {
		informational := focus.TokenClass(sku[2]).Informational()
		if match[1] != sku[0] || match[3] != sku[2] || informational != (match[2] == "informational") {
			return fmt.Errorf("%s does not fit the SkuId %v", id, sku)
		}
		return nil
	}
	return fmt.Errorf("%s has no documented shape", id)
}

// TestEveryRowPriceIDShape writes the rows of every recorded run, and of a
// fallback-priced and a turn-grain run, and holds each SkuPriceId to one of
// the two shapes docs/adapters.md documents.
func TestEveryRowPriceIDShape(t *testing.T) {
	doc := string(mustRead(t, adaptersDoc))
	for _, shape := range []string{unitPriceShape, kindPriceShape} {
		if !strings.Contains(doc, shape) {
			t.Fatalf("%s does not document %s", adaptersDoc, shape)
		}
	}
	runs := map[string]func(t *testing.T) []map[string]any{
		"one-request": func(t *testing.T) []map[string]any {
			return claudeRows(t, "one-request", "00000000-0000-4000-8000-000000000001")
		},
		"subagent": func(t *testing.T) []map[string]any { return claudeRows(t, "subagent", subagentSession) },
		"budget-stop": func(t *testing.T) []map[string]any {
			return claudeRows(t, "budget-stop", "00000000-0000-4000-8000-000000000003")
		},
		"codex exec":  func(t *testing.T) []map[string]any { return codexRows(t, "replay") },
		"codex turns": func(t *testing.T) []map[string]any { return codexRows(t, "stream", "--ephemeral") },
		"claude-code-action": func(t *testing.T) []map[string]any {
			var rows bytes.Buffer
			if _, err := Record(Options{Rows: &rows, Catalog: pinned(t)}, claudecode.Name, readActionRun(t, string(mustRead(t, actionSession)))); err != nil {
				t.Fatal(err)
			}
			return parseRows(t, rows.Bytes())
		},
		"fallback": func(t *testing.T) []map[string]any {
			report := recordedReport()
			report.Requests[0].Provider, report.Requests[0].Model = "openai", "gpt-9-preview"
			report.Requests[0].Tokens = adapter.Tokens{Output: 10, ReasoningOutput: 4}
			var rows bytes.Buffer
			if _, err := Record(Options{Rows: &rows, Catalog: pinned(t)}, "codex", report); err != nil {
				t.Fatal(err)
			}
			return parseRows(t, rows.Bytes())
		},
	}
	seen := map[string]bool{}
	for name, rowsOf := range runs {
		t.Run(name, func(t *testing.T) {
			rows := rowsOf(t)
			if len(rows) == 0 {
				t.Fatal("no rows")
			}
			for _, row := range rows {
				if err := priceIDShapeError(row); err != nil {
					t.Error(err)
				}
				id, _ := row["SkuPriceId"].(string)
				if match := kindPriceID.FindStringSubmatch(id); match != nil {
					seen[match[2]] = true
				} else {
					seen["unit price"] = true
				}
			}
		})
	}
	for _, kind := range []string{"unit price", "fallback", "informational"} {
		if !seen[kind] {
			t.Errorf("no row priced by a %s id, so its shape went untested", kind)
		}
	}
}

func claudeRows(t *testing.T, scenario, session string) []map[string]any {
	t.Helper()
	got, _ := claudeRun(t, scenario, session, pinned(t))
	if got.err != nil {
		t.Fatal(got.err)
	}
	return got.rows
}

func codexRows(t *testing.T, mode string, extra ...string) []map[string]any {
	t.Helper()
	got, _ := codexRun(t, mode, "5", extra...)
	if got.err != nil {
		t.Fatal(got.err)
	}
	return got.rows
}

// TestEmptyRunReasonCode holds a run that wrote no rows to a reason from the
// closed list, stated on the run, and a run with rows to none.
func TestEmptyRunReasonCode(t *testing.T) {
	silent, _ := codexRun(t, "silent", "5")
	if silent.err != nil {
		t.Fatal(silent.err)
	}
	var recorded bytes.Buffer
	empty, err := Record(Options{Rows: &recorded, Catalog: pinned(t)}, claudecode.Name, adapter.Report{CostBasis: focus.ListEquivalent})
	if err != nil {
		t.Fatal(err)
	}
	for name, result := range map[string]Result{"a run": silent.result, "a recorded run": empty} {
		if result.Rows != 0 || result.Reason != reason.NoUsage {
			t.Errorf("%s: %d rows, reason %q", name, result.Rows, result.Reason)
		}
		said := false
		for _, warning := range result.Report.Warnings {
			said = said || strings.HasPrefix(warning, "reason no_usage: ")
		}
		if !said {
			t.Errorf("%s does not state its reason: %q", name, result.Report.Warnings)
		}
	}
	if len(silent.rows) != 0 || recorded.Len() != 0 {
		t.Errorf("%d rows, %d bytes", len(silent.rows), recorded.Len())
	}
	if reason.NoUsage.Sentence() == "" || reason.ConcurrentRun.Sentence() == "" {
		t.Error("a reason has no sentence")
	}
	spent, _ := codexRun(t, "replay", "5")
	if spent.err != nil || spent.result.Rows == 0 || spent.result.Reason != "" {
		t.Errorf("a run with rows: %v, %d rows, reason %q", spent.err, spent.result.Rows, spent.result.Reason)
	}
}

// TestConcurrentAgentsRefused starts a second Claude Code run in the config
// directory of one still running. The first would count the second's session
// as nested, and the second would count it again, so the second is refused
// before its agent starts. The first keeps its own total, and once it ends the
// directory takes a run again.
func TestConcurrentAgentsRefused(t *testing.T) {
	log := installClaudeRun(t, "subagent", subagentSession)
	hold := log + ".hold"
	if err := os.WriteFile(hold, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	argv := []string{agentNamed(t, "claude"), "-p", "work"}
	c := pinned(t)
	type finished struct {
		result Result
		err    error
	}
	outer := make(chan finished, 1)
	go func() {
		result, err := Run(context.Background(), Options{
			Argv: argv, Budget: decimal.MustParse("5"), Rows: io.Discard, Stdin: strings.NewReader(""),
			Stdout: io.Discard, Stderr: io.Discard, Catalog: c, Grace: 200 * time.Millisecond,
		})
		outer <- finished{result, err}
	}()
	waitForStarts(t, log, 1)

	inner := runAgent(t, argv, "5", pinned(t))
	var refused *RefusedError
	if !errors.As(inner.err, &refused) || refused.Reason != reason.ConcurrentRun || !errors.Is(inner.err, adapter.ErrClaimed) {
		t.Errorf("the second run: %v", inner.err)
	}
	if inner.result.Reason != reason.ConcurrentRun || len(inner.rows) != 0 {
		t.Errorf("the second run: reason %q, %d rows", inner.result.Reason, len(inner.rows))
	}
	if err := os.Remove(hold); err != nil {
		t.Fatal(err)
	}
	first := <-outer
	if first.err != nil {
		t.Fatal(first.err)
	}
	if err := checkListTotal(first.result.ListTotal, subagentList); err != nil {
		t.Error(err)
	}
	if starts := logged(t, log+".starts"); len(starts) != 1 {
		t.Errorf("%d agents started, want the first alone", len(starts))
	}
	if after := runAgent(t, argv, "5", pinned(t)); after.err != nil {
		t.Errorf("a run after the first ended: %v", after.err)
	}
}

// waitForStarts waits until the fake claude has started count times.
func waitForStarts(t *testing.T, log string, count int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(log + ".starts"); err == nil && len(strings.Fields(string(data))) >= count {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the agent did not start %d times", count)
}

const (
	subagentFixture = "../adapter/codex/testdata/not-recorded/subagent/rollout.jsonl"
	// withSubagentList is the recorded exec run at list, 0.0369328 USD, plus
	// the fixture subagent's 3808 × 0.20 + 8192 × 0.02 + 300 × 1.20 per
	// million, from the sheet's gpt-5.6-luna rows.
	withSubagentList = "0.03821824"
)

// TestCodexSubagentRolloutCounted runs Codex with a subagent thread that
// writes its own rollout file. Its request reaches the rows once, under its
// own response id, whether the stream's total leaves its spend out or holds it.
func TestCodexSubagentRolloutCounted(t *testing.T) {
	for name, usage := range map[string]string{
		"a stream total without the subagent": `{"input_tokens":33907,"cached_input_tokens":28032,"output_tokens":111}`,
		"a stream total with the subagent":    `{"input_tokens":45907,"cached_input_tokens":36224,"output_tokens":411,"reasoning_output_tokens":64}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(fakeSubagentRollout, mustAbs(t, subagentFixture))
			t.Setenv(fakeStreamUsage, usage)
			got, _ := codexRun(t, "subagent", "5")
			if got.err != nil {
				t.Fatal(got.err)
			}
			if got.result.Enforcement.Grade != adapter.GradeWrapperWatchdog || got.result.Report.CostSource != "agent_session_file" {
				t.Fatalf("%+v, %s, warnings %q", got.result.Enforcement, got.result.Report.CostSource, got.result.Report.Warnings)
			}
			if err := checkListTotal(got.result.ListTotal, withSubagentList); err != nil {
				t.Error(err)
			}
			subagentRows := 0
			for _, row := range got.rows {
				if tagOf(row, focus.TagModel) == "gpt-5.6-luna" {
					subagentRows++
				}
			}
			if subagentRows != 5 {
				t.Errorf("%d subagent rows, want its three billed and two informational classes", subagentRows)
			}
			if tokens := consumed(t, got.rows); tokens["reasoning_output"] != 64 || tokens["output"] != 411 {
				t.Errorf("tokens %v", tokens)
			}
		})
	}
}

// TestCodexInFlightAtStop stops two Codex runs at the cap. In one, the tool
// had returned and its output had gone to the model, so a request was in
// flight: the run records every request that settled and says the one in
// flight reached no row. In the other, a tool was still running, so nothing
// was in flight and the run says that too.
func TestCodexInFlightAtStop(t *testing.T) {
	for name, tc := range map[string]struct {
		rollout, cut, total string
		lost                int
		said                string
	}{
		"a request in flight": {"exec", `"custom_tool_call_output"`, "0.0291216", 1, "with 1 model request in flight, sent at 2026-11-01T11:17:13.601Z"},
		"a tool running":      {"stopped", "", "0.0283136", 0, "show no model request in flight"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(fakeCutAfter, tc.cut)
			got := codexReplayOf(t, tc.rollout, "0.001")
			if got.err != nil {
				t.Fatal(got.err)
			}
			if !got.result.Stopped || got.result.Enforcement.Grade != adapter.GradeWrapperWatchdog {
				t.Fatalf("stopped %v, %+v", got.result.Stopped, got.result.Enforcement)
			}
			if err := checkListTotal(got.result.ListTotal, tc.total); err != nil {
				t.Error(err)
			}
			if got.result.Report.Unrecorded != tc.lost {
				t.Errorf("%d requests unrecorded, want %d", got.result.Report.Unrecorded, tc.lost)
			}
			if warnings := strings.Join(got.result.Report.Warnings, "\n"); !strings.Contains(warnings, tc.said) {
				t.Errorf("warnings %q", warnings)
			}
		})
	}
}

// codexReplayOf runs a fake Codex that writes a recorded scenario's rollout
// file and then waits inside its turn.
func codexReplayOf(t *testing.T, scenario, budget string) outcome {
	t.Helper()
	t.Setenv(fakeMode, "replay-wait")
	t.Setenv(fakeLog, filepath.Join(t.TempDir(), "progress"))
	t.Setenv(fakeRollout, mustAbs(t, filepath.Join(codexData, scenario, "rollout.jsonl")))
	t.Setenv("CODEX_HOME", t.TempDir())
	return runAgent(t, []string{agentNamed(t, "codex"), "exec", "-m", "gpt-5.6-sol", "work"}, budget, pinned(t))
}
