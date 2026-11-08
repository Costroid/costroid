package run

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/adapter/claudecode"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

const perRunInit = `{"type":"system","subtype":"init","session_id":"per-run","apiKeySource":"ANTHROPIC_API_KEY","claude_code_version":"2.1.319"}`

// perRunRows feeds a Claude Code stream with no session file beside it, so
// the run falls to one set of rows per model, and prices it. It returns the
// report and the rows by class and model.
func perRunRows(t *testing.T, lines ...string) (adapter.Report, map[string]map[string]any) {
	t.Helper()
	outsideCIForRun(t)
	session := claudecode.Agent{ConfigDir: t.TempDir()}.NewSession(adapter.Plan{})
	for _, line := range append([]string{perRunInit}, lines...) {
		if err := session.Feed([]byte(line), time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
	}
	report, err := session.Finish()
	if err != nil {
		t.Fatal(err)
	}
	var written bytes.Buffer
	if _, err := Record(Options{Rows: &written, Catalog: pinned(t)}, claudecode.Name, report); err != nil {
		t.Fatal(err)
	}
	rows := map[string]map[string]any{}
	for _, row := range parseRows(t, written.Bytes()) {
		tags := row["Tags"].(map[string]any)
		rows[tags["costroid/model"].(string)+" "+row["SkuMeter"].(string)] = row
	}
	return report, rows
}

func assistantLine(model, usage string) string {
	return `{"type":"assistant","session_id":"per-run","message":{"model":"` + model + `","type":"message","role":"assistant","usage":` + usage + `}}`
}

func resultLine(models string) string {
	return `{"type":"result","subtype":"success","is_error":false,"session_id":"per-run","modelUsage":{` + models + `}}`
}

func wantRow(t *testing.T, rows map[string]map[string]any, key, sku, price string, notes ...string) {
	t.Helper()
	row, ok := rows[key]
	if !ok {
		t.Fatalf("no %s row", key)
	}
	if row["SkuPriceId"] != sku || row["ListUnitPrice"] != price {
		t.Errorf("%s: %v at %v, want %s at %s", key, row["SkuPriceId"], row["ListUnitPrice"], sku, price)
	}
	description, _ := row["ChargeDescription"].(string)
	for _, note := range notes {
		if !strings.Contains(description, note) {
			t.Errorf("%s: description %q does not say %q", key, description, note)
		}
	}
}

// rowsTotal adds the list cost of the rows.
func rowsTotal(t *testing.T, rows map[string]map[string]any) decimal.Decimal {
	t.Helper()
	var total decimal.Decimal
	for key, row := range rows {
		cost, ok := row["ListCost"].(string)
		if !ok {
			continue
		}
		figure, err := decimal.Parse(cost)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		total = total.Add(figure)
	}
	return total
}

// wantEveryRowAt holds every billed row to one context tier's price id and
// to the note it must carry, and no row to an upper bound.
func wantEveryRowAt(t *testing.T, rows map[string]map[string]any, context string, notes ...string) {
	t.Helper()
	for key, row := range rows {
		sku := row["SkuPriceId"].(string)
		if strings.Contains(sku, ":informational:") {
			continue
		}
		model, class, _ := strings.Cut(key, " ")
		wantRow(t, rows, key, "anthropic:"+model+":global:standard:"+context+":"+class, row["ListUnitPrice"].(string), notes...)
		if description := row["ChargeDescription"].(string); strings.Contains(description, "upper bound") {
			t.Errorf("%s calls a sum's price an upper bound: %q", key, description)
		}
	}
}

// standardGlobal is a stream message's routing as Claude Code states it.
const standardGlobal = `{"service_tier":"standard","speed":"standard","inference_geo":"global"}`

// TestPerModelGrainNeverTakesTheLongTierFromASum holds a claude-code-action
// run's shape, read from the result's per-model totals: Claude Haiku 5.5
// over 8 requests, 143642 input tokens in all, no request past 100000. Its
// sum passes the 100000-token boundary, and says nothing about any one
// request. Claude Code's own figure, USD 0.008837335, is these tokens at the
// prices up to 100000 input tokens, so that tier prices every row, and the
// rows come to that figure; the tier above would read USD 0.044186675. With
// no figure, or one that matches neither tier, the lowest tier prices the
// rows, which say the tier is unknown and may read low. A sum within the
// lowest tier needs no figure.
func TestPerModelGrainNeverTakesTheLongTierFromASum(t *testing.T) {
	const totals = `"inputTokens":1231,"cacheReadInputTokens":122736,"cacheCreationInputTokens":19675,` +
		`"cacheCreationEphemeral5mInputTokens":19675,"outputTokens":10055`
	turns := make([]string, 8)
	for i := range turns {
		turns[i] = assistantLine("claude-haiku-5-5", standardGlobal)
	}
	const sum = "this model's input of 143642 tokens is a sum over its requests, which selects no context tier"
	cases := []struct {
		name, cost string
		notes      []string
	}{
		{"its own figure", `,"costUSD":0.008837335`, []string{sum,
			"Claude Code's own figure for it, USD 0.008837335, is its tokens at the prices up to 100000 input tokens, so that tier prices them"}},
		{"no figure", ``, []string{sum,
			"Claude Code states no figure for it, so its context tier is unknown, and the prices up to 100000 input tokens apply, which may read low"}},
		{"a figure of neither tier", `,"costUSD":0.0089`, []string{sum,
			"Claude Code's own figure for it, USD 0.0089, is its tokens at neither tier's prices, so its context tier is unknown, and the prices up to 100000 input tokens apply, which may read low"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report, rows := perRunRows(t, append(turns, resultLine(`"claude-haiku-5-5":{`+totals+tc.cost+`}`))...)
			if len(report.Requests) != 1 || report.Requests[0].Grain != adapter.PerRun || report.Requests[0].ContextInputTokens != 0 {
				t.Fatalf("requests %+v", report.Requests)
			}
			if len(rows) != 5 {
				t.Fatalf("%d rows", len(rows))
			}
			wantEveryRowAt(t, rows, "to-100000", tc.notes...)
			if total := rowsTotal(t, rows); total.String() != "0.008837335" {
				t.Errorf("the rows come to USD %s, not Claude Code's own 0.008837335", total)
			}
		})
	}

	_, rows := perRunRows(t,
		resultLine(`"claude-haiku-5-5":{"inputTokens":40000,"cacheReadInputTokens":50000,"outputTokens":1000}`))
	wantEveryRowAt(t, rows, "to-100000",
		"one run's input of 90000 tokens is within the 100000-token context tier, so each of its requests is too")
	for key, row := range rows {
		if strings.Contains(row["ChargeDescription"].(string), "Claude Code") {
			t.Errorf("%s reads Claude Code's figure for a sum within the lowest tier: %v", key, row["ChargeDescription"])
		}
	}
}

// TestPerModelGrainTakesTheLongTierWhenTheAgentsCostSaysSo keeps the case of
// one request past 100000 input tokens, read from the result's per-model
// totals: 150000 input and 1002 output tokens. Claude Code's own figure is
// USD 0.07750499999999999, the long-context prices' 0.077505 as binary64
// arithmetic leaves it, so the long-context tier prices the rows. The lowest
// tier would read USD 0.015501, five times under.
func TestPerModelGrainTakesTheLongTierWhenTheAgentsCostSaysSo(t *testing.T) {
	report, rows := perRunRows(t, assistantLine("claude-haiku-5-5", standardGlobal),
		resultLine(`"claude-haiku-5-5":{"inputTokens":150000,"outputTokens":1002,"costUSD":0.07750499999999999}`))
	if len(report.Requests) != 1 || report.Requests[0].Grain != adapter.PerRun || report.Requests[0].ContextInputTokens != 150000 {
		t.Fatalf("requests %+v", report.Requests)
	}
	wantEveryRowAt(t, rows, "over-100000",
		"this model's input of 150000 tokens is a sum over its requests, which selects no context tier",
		"Claude Code's own figure for it, USD 0.07750499999999999, is its tokens at the prices above 100000 input tokens, so that tier prices them")
	wantRow(t, rows, "claude-haiku-5-5 input_uncached", "anthropic:claude-haiku-5-5:global:standard:over-100000:input_uncached", "0.5")
	wantRow(t, rows, "claude-haiku-5-5 output", "anthropic:claude-haiku-5-5:global:standard:over-100000:output", "2.5")
	if total := rowsTotal(t, rows); total.String() != "0.077505" {
		t.Errorf("the rows come to USD %s, want 0.077505", total)
	}
}

// TestPerRunGrainKeepsFastModeSeenOnTheStream holds that a run read from the
// result's per-model totals is priced at the tier and region its stream
// messages stated. Fast mode or US residency on any message prices the whole
// run there, which is exact when every message stated it and an upper bound
// when only some did. Fast-mode cache reads compose the fast input price with
// the sheet's prompt_cache multiplier. A model no message names says its tier
// is unknown.
func TestPerRunGrainKeepsFastModeSeenOnTheStream(t *testing.T) {
	const fastUS = `{"service_tier":"standard","speed":"fast","inference_geo":"us"}`
	totals := `"claude-opus-5-5":{"inputTokens":1000,"cacheReadInputTokens":10000,"outputTokens":100},` +
		`"claude-haiku-5-5":{"inputTokens":500,"outputTokens":50}`

	_, rows := perRunRows(t,
		assistantLine("claude-opus-5-5", fastUS), assistantLine("claude-opus-5-5", standardGlobal), resultLine(totals))
	// claude-opus-5-5 fast is USD 8 input and 40 output per million; a fast
	// cache read is 8 times the model's 0.05 read multiplier; US residency
	// multiplies each by 1.1.
	wantRow(t, rows, "claude-opus-5-5 input_uncached", "anthropic:claude-opus-5-5:global:fast:all:input_uncached", "8.8",
		"fast mode shows on some of this model's stream messages, so the fast tier prices all of them, an upper bound",
		"data residency in us shows on some of this model's stream messages, so it prices all of them, an upper bound",
		"list price times 1.1 for data residency in us")
	wantRow(t, rows, "claude-opus-5-5 output", "anthropic:claude-opus-5-5:global:fast:all:output", "44")
	wantRow(t, rows, "claude-opus-5-5 cache_read", "anthropic:claude-opus-5-5:global:fast:all:cache_read", "0.44",
		"input price times 0.05 for cache_read, by anthropic:multiplier:prompt-cache-read-claude-opus-5-5")
	if row := rows["claude-opus-5-5 cache_read"]; strings.Contains(row["ChargeDescription"].(string), "fallback") {
		t.Errorf("a fast cache read took the fallback: %v", row["ChargeDescription"])
	}
	wantRow(t, rows, "claude-haiku-5-5 input_uncached", "anthropic:claude-haiku-5-5:global:standard:to-100000:input_uncached", "0.1",
		"no stream message states this model's service tier or region, so the standard tier and global price it")

	_, rows = perRunRows(t, assistantLine("claude-opus-5-5", fastUS), resultLine(`"claude-opus-5-5":{"inputTokens":1000,"outputTokens":100}`))
	wantRow(t, rows, "claude-opus-5-5 input_uncached", "anthropic:claude-opus-5-5:global:fast:all:input_uncached", "8.8")
	if description := rows["claude-opus-5-5 input_uncached"]["ChargeDescription"].(string); strings.Contains(description, "upper bound") ||
		strings.Contains(description, "no stream message") {
		t.Errorf("a run every message of which stated fast mode in us says otherwise: %q", description)
	}
}

// TestCoarseRowWithNoStatedInputSaysItsContextTierIsUnknown holds that an
// entry coarser than a request, with input tokens and no whole input stated,
// says its context tier is unknown, and that one with no input does not.
func TestCoarseRowWithNoStatedInputSaysItsContextTierIsUnknown(t *testing.T) {
	const unknown = "one turn's total, and its source states no whole input, so its context tier is unknown and the lowest tier prices it, which may read low"
	cases := map[string]struct {
		tokens adapter.Tokens
		says   bool
	}{
		"with input":   {adapter.Tokens{InputUncached: 150000, Output: 10}, true},
		"output alone": {adapter.Tokens{Output: 10}, false},
	}
	for name, tc := range cases {
		request := haikuRequest("turn", tc.tokens)
		request.Grain = adapter.PerTurn
		var written bytes.Buffer
		if _, err := Record(Options{Rows: &written, Catalog: pinned(t)}, "codex", adapter.Report{CostBasis: focus.Measured, Requests: []adapter.Request{request}}); err != nil {
			t.Fatal(err)
		}
		for _, row := range parseRows(t, written.Bytes()) {
			if said := strings.Contains(row["ChargeDescription"].(string), unknown); said != tc.says {
				t.Errorf("%s, %s: %q", name, row["SkuMeter"], row["ChargeDescription"])
			}
		}
	}
}
