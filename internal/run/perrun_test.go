package run

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/adapter/claudecode"
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

// TestPerRunGrainPricesTheUpperContextTier holds that a run read from the
// result's per-model totals takes the model's whole input as the bound of
// every request's in it. Claude Haiku 5.5 bills input above 100,000 tokens at
// USD 0.50 per million against 0.10 below, so a run whose input passes that
// line is priced at the upper tier and says the price is an upper bound; a
// run whose whole input is below it is priced at the lower tier, exactly.
func TestPerRunGrainPricesTheUpperContextTier(t *testing.T) {
	report, rows := perRunRows(t,
		resultLine(`"claude-haiku-5-5":{"inputTokens":150000,"outputTokens":1000}`))
	if len(report.Requests) != 1 || report.Requests[0].Grain != adapter.PerRun || report.Requests[0].ContextInputTokens != 150000 {
		t.Fatalf("requests %+v", report.Requests)
	}
	wantRow(t, rows, "claude-haiku-5-5 input_uncached", "anthropic:claude-haiku-5-5:global:standard:over-100000:input_uncached", "0.5",
		"one run's input of 150000 tokens passes the 100000-token context tier boundary", "upper bound")
	wantRow(t, rows, "claude-haiku-5-5 output", "anthropic:claude-haiku-5-5:global:standard:over-100000:output", "2.5", "upper bound")

	_, rows = perRunRows(t,
		resultLine(`"claude-haiku-5-5":{"inputTokens":40000,"cacheReadInputTokens":50000,"outputTokens":1000}`))
	wantRow(t, rows, "claude-haiku-5-5 input_uncached", "anthropic:claude-haiku-5-5:global:standard:to-100000:input_uncached", "0.1",
		"one run's input of 90000 tokens is within the 100000-token context tier, so each of its requests is too")
	for key, row := range rows {
		if strings.Contains(row["ChargeDescription"].(string), "lowest context tier") {
			t.Errorf("%s still reads the lowest tier by default: %v", key, row["ChargeDescription"])
		}
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
	const standardGlobal = `{"service_tier":"standard","speed":"standard","inference_geo":"global"}`
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
	const unknown = "one turn's total, and its source states no whole input, so its context tier is unknown and the lowest tier prices it, which may read under"
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
