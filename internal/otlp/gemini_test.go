package otlp

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"testing"

	catalogdata "github.com/Costroid/costroid/catalog"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/focus"
)

// The Gemini fixtures are built from Gemini CLI's telemetry source at
// v0.67.0, not recorded from a run. The response's prompt holds 12000 tokens,
// 8000 of them cached; its tool results add 600 more. It wrote 300 candidate
// tokens and 450 thoughts, and its total is 13350.
const (
	geminiLogs  = "api-response-logs.from-source.json"
	geminiSpans = "llm-call-span.from-source.json"
)

// parseGemini reads a fixture, after each old and new pair of edits.
func parseGemini(t *testing.T, name string, signal Signal, edits ...string) Batch {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "gemini", name))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(edits); i += 2 {
		if !bytes.Contains(body, []byte(edits[i])) {
			t.Fatalf("%s holds no %s", name, edits[i])
		}
		body = bytes.Replace(body, []byte(edits[i]), []byte(edits[i+1]), 1)
	}
	batch, err := Parse(body, JSON, signal)
	if err != nil {
		t.Fatal(err)
	}
	assertNoContent(t, batch)
	return batch
}

func TestGeminiApiResponseIsPricedNotRejected(t *testing.T) {
	usage := one(t, parseGemini(t, geminiLogs, Logs))
	if usage.Dialect != Gemini || usage.Provider != providerGoogle || usage.Model != "gemini-2.5-pro" || usage.Agent != "gemini" {
		t.Fatalf("%+v", usage)
	}
	if usage.AgentVersion != "" {
		t.Errorf("agent version %q is the Node.js version", usage.AgentVersion)
	}
	prices, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, count := range usage.Counts {
		if count.Class.Informational() {
			continue
		}
		price, err := prices.Lookup(catalog.Query{
			Provider: usage.Provider, Model: usage.Model, Class: count.Class,
			ServiceTier: usage.ServiceTier, Region: catalog.RegionGlobal,
			ContextInputTokens: usage.ContextInputTokens, At: usage.Start,
		})
		if err != nil {
			t.Fatalf("%s: %v", count.Class, err)
		}
		if price.Fallback || price.ListUnitPrice.Sign() <= 0 {
			t.Errorf("%s: %+v", count.Class, price)
		}
	}
}

func TestGeminiSpanAndOperationLogCountNothing(t *testing.T) {
	spans := parseGemini(t, geminiSpans, Traces)
	if len(spans.Usage) != 0 || spans.Rejected != 0 {
		t.Fatalf("llm_call span: %d usages, %d rejected", len(spans.Usage), spans.Rejected)
	}
	logs := parseGemini(t, geminiLogs, Logs)
	if len(logs.Usage) != 1 || logs.Rejected != 0 {
		t.Fatalf("api_response and operation details: %d usages, %d rejected", len(logs.Usage), logs.Rejected)
	}
}

func TestGeminiCachedIsNotUncached(t *testing.T) {
	usage := one(t, parseGemini(t, geminiLogs, Logs))
	assertClasses(t, usage.Counts, map[focus.TokenClass]int64{
		focus.InputUncached:       4000 + 600,
		focus.CacheRead:           8000,
		focus.Output:              750,
		focus.ReasoningOutput:     450,
		focus.CachedInputReported: 8000,
	})
	if usage.ContextInputTokens != 12000+600 {
		t.Errorf("context %d, want the prompt's 12000 and the tool results' 600", usage.ContextInputTokens)
	}
}

func TestGeminiToolTokensArePricedAsInput(t *testing.T) {
	noTool := one(t, parseGemini(t, geminiLogs, Logs,
		`"tool_token_count", "value": {"intValue": "600"}`, `"tool_token_count", "value": {"intValue": "0"}`,
		`"intValue": "13350"`, `"intValue": "12750"`))
	withTool := one(t, parseGemini(t, geminiLogs, Logs))
	added := classes(withTool.Counts)[focus.InputUncached] - classes(noTool.Counts)[focus.InputUncached]
	if added != 600 {
		t.Fatalf("600 tool tokens added %d uncached input tokens", added)
	}
	if len(withTool.Notes) != 0 || len(noTool.Notes) != 0 {
		t.Fatalf("a total that agrees drew notes %v and %v", withTool.Notes, noTool.Notes)
	}
}

func TestGeminiTotalThatDisagreesIsNoted(t *testing.T) {
	for name, total := range map[string]string{"under the counts": "13000", "over the counts": "14000", "unreported": "0"} {
		t.Run(name, func(t *testing.T) {
			usage := one(t, parseGemini(t, geminiLogs, Logs, `"intValue": "13350"`, `"intValue": "`+total+`"`))
			if !slices.Equal(usage.Notes, []string{NoteTotalDisagrees}) {
				t.Fatalf("notes %v", usage.Notes)
			}
			if classes(usage.Counts)[focus.InputUncached] != 4600 {
				t.Fatalf("a disagreeing total changed the counts: %v", classes(usage.Counts))
			}
		})
	}
}

// TestGeminiThoughtsArePricedAsTheCatalogBillsGoogleReasoning reads the class
// from the pinned Google sheet's reasoning row, so a sheet that moved
// thinking tokens to another class would fail here.
func TestGeminiThoughtsArePricedAsTheCatalogBillsGoogleReasoning(t *testing.T) {
	usage := one(t, parseGemini(t, geminiLogs, Logs))
	billedAs := googleReasoningClass(t, usage.Model)
	have := classes(usage.Counts)
	if have[billedAs] != 300+450 {
		t.Fatalf("%s holds %d, want 300 candidates and 450 thoughts: %v", billedAs, have[billedAs], have)
	}
	if have[focus.ReasoningOutput] != 450 || !focus.ReasoningOutput.Informational() {
		t.Fatalf("reasoning_output %d, want the 450 thoughts, repeated", have[focus.ReasoningOutput])
	}
}

func googleReasoningClass(t *testing.T, model string) focus.TokenClass {
	t.Helper()
	names, err := fs.Glob(catalogdata.Sheets, path.Join(catalogdata.Version, "google-*.json"))
	if err != nil || len(names) != 1 {
		t.Fatalf("google sheets %v: %v", names, err)
	}
	data, err := fs.ReadFile(catalogdata.Sheets, names[0])
	if err != nil {
		t.Fatal(err)
	}
	var sheet struct {
		Rows []struct {
			RowType           string
			AppliesToModels   []string
			ReasoningBilledAs string
		} `json:"rows"`
	}
	if err := json.Unmarshal(data, &sheet); err != nil {
		t.Fatal(err)
	}
	for _, row := range sheet.Rows {
		if row.RowType == "reasoning" && slices.Contains(row.AppliesToModels, model) {
			return focus.TokenClass(row.ReasoningBilledAs)
		}
	}
	t.Fatalf("%s states no reasoning row for %s", names[0], model)
	return ""
}
