package run

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/adapter/claudecode"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/focus"
)

func haikuRequest(id string, tokens adapter.Tokens) adapter.Request {
	return adapter.Request{
		ID: id, Provider: "anthropic", Model: "claude-haiku-5-5", Grain: adapter.PerRequest,
		ServiceTier: catalog.TierStandard, Region: catalog.RegionGlobal,
		Start: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 1, 0, 0, 1, 0, time.UTC),
		Tokens: tokens,
	}
}

// TestRowsRefuseNegativeTokens holds that a negative class never reaches the
// price: the run writes no row, beside a whole request or inside a nested
// session, and the error names the request.
func TestRowsRefuseNegativeTokens(t *testing.T) {
	whole := haikuRequest("whole", adapter.Tokens{InputUncached: 10, Output: 2})
	negative := haikuRequest("negative", adapter.Tokens{InputUncached: 10, CacheWrite1h: -50})
	reports := map[string]adapter.Report{
		"beside a whole request": {CostBasis: focus.Measured, Requests: []adapter.Request{whole, negative}},
		"in a nested session": {CostBasis: focus.Measured, Requests: []adapter.Request{whole}, Nested: []adapter.Report{
			{SessionID: "nested", CostBasis: focus.Measured, Requests: []adapter.Request{negative}},
		}},
	}
	for name, report := range reports {
		var rows bytes.Buffer
		result, err := Record(Options{Rows: &rows, Catalog: pinned(t)}, "claude-code", report)
		if err == nil || !strings.Contains(err.Error(), "request negative: cache_write_1h holds -50 tokens, below zero") {
			t.Errorf("%s: %v", name, err)
		}
		if rows.Len() != 0 || result.Rows != 0 {
			t.Errorf("%s: %d rows written: %s", name, result.Rows, rows.String())
		}
	}
	builder := rowBuilder{catalog: pinned(t), agent: "claude-code", enforcement: adapter.Enforcement{Grade: adapter.GradeAgentFlag}}
	if rows, err := builder.rows(adapter.Report{CostBasis: focus.Measured, Requests: []adapter.Request{whole}}); err != nil || len(rows) != 2 {
		t.Errorf("a whole request: %d rows, %v", len(rows), err)
	}
}

// TestNewestRecordedRunRowsComeToItsOwnTotal prices Claude Code 2.1.319's
// recorded run from its session file. The rows come to the result's own
// total_cost_usd, 0.0008778950000000001 as a float, which is 0.000877895 USD.
func TestNewestRecordedRunRowsComeToItsOwnTotal(t *testing.T) {
	const directory = "../adapter/claudecode/testdata/2.1.319/current/"
	const sessionID = "00000000-0000-4000-8000-000000000001"
	outsideCIForRun(t)
	config := t.TempDir()
	project := filepath.Join(config, "projects", "-work")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, sessionID+".jsonl"), mustRead(t, directory+"session.jsonl"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := claudecode.Agent{ConfigDir: config}.NewSession(adapter.Plan{})
	for _, line := range strings.Split(strings.TrimSpace(string(mustRead(t, directory+"stream.jsonl"))), "\n") {
		if err := session.Feed([]byte(line), time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
	}
	report, err := session.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if report.CostSource != claudecode.SourceSessionFile || len(report.Warnings) != 0 {
		t.Fatalf("%s, warnings %q", report.CostSource, report.Warnings)
	}
	var rows bytes.Buffer
	result, err := Record(Options{Rows: &rows, Catalog: pinned(t)}, claudecode.Name, report)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkListTotal(result.ListTotal, "0.000877895"); err != nil {
		t.Error(err)
	}
}
