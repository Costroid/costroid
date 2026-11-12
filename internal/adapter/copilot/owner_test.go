package copilot_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

// addCount adds n to a count of l, which may not hold it yet.
func addCount(t *testing.T, l line, key string, n int64) {
	t.Helper()
	var held int64
	if _, ok := l[key]; ok {
		held = count(t, l, key)
	}
	l[key] = json.Number(strconv.FormatInt(held+n, 10))
}

// serveRecord gives usage record index the model named, on Copilot's own
// routing, and moves its counts in session.shutdown from the recorded model's
// totals to that model's, so the file still agrees with its own totals.
func serveRecord(t *testing.T, events []line, index int, model string) {
	t.Helper()
	usage := usageOf(t, ofType(events, "session.usage_record")[index])
	from := metricOf(t, events, usage["model"].(string))
	metrics := object(t, ofType(events, "session.shutdown")[0], "data", "modelMetrics")
	if _, ok := metrics[model]; !ok {
		metrics[model] = map[string]any{"requests": map[string]any{}, "usage": map[string]any{}}
	}
	to := metricOf(t, events, model)
	for _, key := range []string{"inputTokens", "outputTokens", "cacheReadTokens", "cacheWriteTokens", "reasoningTokens"} {
		n := count(t, usage, key)
		addCount(t, object(t, from, "usage"), key, -n)
		addCount(t, object(t, to, "usage"), key, n)
	}
	addCount(t, object(t, from, "requests"), "count", -1)
	addCount(t, object(t, to, "requests"), "count", 1)
	if count(t, object(t, from, "requests"), "count") == 0 {
		delete(metrics, usage["model"].(string))
	}
	usage["model"] = model
	usage["isByok"] = false
	delete(usage, "byokKind")
	delete(usage, "modelProvider")
}

// writeEvents writes a session file for the stand-in to replay.
func writeEvents(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestCopilotUnpricedModelDropsTheGrade runs the stand-in under costroid run
// on the recorded session with its second request served on
// mai-code-1.1-flash, a model Copilot serves whose publisher has no sheet in
// the catalog. That request writes no row and the watch cannot count it, so
// the run earns the grade none, and the result, its rows and a warning name
// the model. The other two requests write their rows, USD 0.00302772 at
// list. A request on the user's key through a provider with no sheet is
// named by its model and that provider.
func TestCopilotUnpricedModelDropsTheGrade(t *testing.T) {
	events := writeEvents(t, editedEvents(t, func(events []line) {
		serveRecord(t, events, 1, "mai-code-1.1-flash")
	}))
	result, rows, err := runFakeOn(t, "copilot", "", "1", "0.05", events)
	if err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	want := adapter.Enforcement{Grade: adapter.GradeNone, Note: `none: the budget was not enforced: the catalog holds no price for the model "mai-code-1.1-flash", so the watch could not count 1 request`}
	if result.Enforcement != want {
		t.Errorf("the run earned %+v, want %+v", result.Enforcement, want)
	}
	if len(result.Report.Requests) != 2 || listTotal(t, rows).Cmp(decimal.MustParse("0.00302772")) != 0 {
		t.Errorf("the run wrote %d requests at USD %s", len(result.Report.Requests), listTotal(t, rows))
	}
	for _, r := range rows {
		if r.Tags[focus.TagEnforcementGrade] != adapter.GradeNone {
			t.Errorf("a row reads the grade %q", r.Tags[focus.TagEnforcementGrade])
		}
	}
	warning := `1 request on the model "mai-code-1.1-flash" wrote no row: the catalog holds no sheet of the provider that publishes the model, so their spend is not stated`
	if !slices.Contains(result.Report.Warnings, warning) {
		t.Errorf("no warning names the model: %q", result.Report.Warnings)
	}
	if slices.ContainsFunc(result.Report.Warnings, func(w string) bool { return strings.Contains(w, `"copilot"`) }) {
		t.Errorf("a warning names Copilot as a provider with no sheet: %q", result.Report.Warnings)
	}

	keyed := readEvents(t, editedEvents(t, func(events []line) {
		usageOf(t, ofType(events, "session.usage_record")[2])["modelProvider"] = "azure"
	}), false)
	warning = `1 request on the model "claude-haiku-5-5" through the provider "azure" wrote no row: the catalog holds no sheet for that provider, so their spend is not stated`
	if len(keyed.Requests) != 2 || !slices.Contains(keyed.Warnings, warning) {
		t.Errorf("a request through a provider with no sheet read %d requests with %q", len(keyed.Requests), keyed.Warnings)
	}
}

// TestCopilotGeminiRequestPricesAtGoogle reads requests Copilot served on
// gemini-3.8-flash, a model the github-copilot sheet lists. No run on a
// Gemini model was recorded: these usage records are constructed from the
// recorded session's, their counts kept, the model renamed and Copilot's own
// routing in place of the user's key. Each request prices at Google's sheet.
// Google prices no cache write, so the writes count as uncached input, and
// each row says so: 21966 uncached input tokens at 0.75, 43418 cache reads at
// 0.075 and 229 output tokens at 3.75 USD per million, USD 0.0205896 at list.
// Under costroid run the watch prices them too, and the run keeps the grade
// wrapper-watchdog.
func TestCopilotGeminiRequestPricesAtGoogle(t *testing.T) {
	gemini := editedEvents(t, func(events []line) {
		for i := range ofType(events, "session.usage_record") {
			serveRecord(t, events, i, "gemini-3.8-flash")
		}
	})
	report := readEvents(t, gemini, false)
	if len(report.Requests) != 3 {
		t.Fatalf("read %d requests", len(report.Requests))
	}
	var uncached int64
	for _, request := range report.Requests {
		uncached += request.Tokens.InputUncached
		if request.Provider != "google" || request.Tokens.CacheWrite5m+request.Tokens.CacheWrite30m+request.Tokens.CacheWrite1h != 0 || request.AssumedWrite != "" {
			t.Errorf("request %s reads %s with writes %+v at %q", request.ID, request.Provider, request.Tokens, request.AssumedWrite)
		}
		if !strings.Contains(request.Note, "Copilot served this request") || !strings.Contains(request.Note, "cache writes are counted as uncached input") {
			t.Errorf("request %s is noted %q", request.ID, request.Note)
		}
	}
	if uncached != 21966 {
		t.Errorf("the requests hold %d uncached input tokens, not 21966", uncached)
	}
	written := writtenRows(t, report)
	for _, raw := range bytes.Split(bytes.TrimSpace(written), []byte("\n")) {
		var priced struct {
			SkuPriceID string `json:"SkuPriceId"`
		}
		if err := json.Unmarshal(raw, &priced); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(priced.SkuPriceID, "google:gemini-3.8-flash:global:standard:all:") && priced.SkuPriceID != "google:informational:cached_input_reported" {
			t.Errorf("a row is priced by %q", priced.SkuPriceID)
		}
	}
	if total := listTotal(t, rowsOf(t, written)); total.Cmp(decimal.MustParse("0.0205896")) != 0 {
		t.Errorf("the rows come to USD %s at list, not 0.0205896", total)
	}

	result, rows, err := runFakeOn(t, "copilot", "", "1", "0.05", writeEvents(t, gemini))
	if err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	if result.Enforcement.Grade != adapter.GradeWrapperWatchdog || listTotal(t, rows).Cmp(decimal.MustParse("0.0205896")) != 0 {
		t.Errorf("the run reads %s at USD %s", result.Enforcement.Grade, listTotal(t, rows))
	}
}
