package run

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/adapter/claudecode"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
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

// TestRowsDigestInResult holds that a run's result states the SHA-256 of the
// rows file's bytes as written, and the SHA-256 of its rows' sorted ids, each
// followed by a line feed. A run with no rows states the empty file's digest
// and no rows digest.
func TestRowsDigestInResult(t *testing.T) {
	installClaudeRun(t, "subagent", subagentSession)
	var rows, stderr bytes.Buffer
	result, err := Run(context.Background(), Options{
		Argv: []string{agentNamed(t, "claude"), "-p", "work"}, Budget: decimal.MustParse("5"),
		Rows: &rows, Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: &stderr, Catalog: pinned(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	file := sha256.Sum256(rows.Bytes())
	if result.RowsFileSHA256 != hex.EncodeToString(file[:]) {
		t.Errorf("rows file sha256 %q, the file's bytes hash to %x", result.RowsFileSHA256, file)
	}
	var ids []string
	for _, row := range parseRows(t, rows.Bytes()) {
		ids = append(ids, row["x_CostroidRowId"].(string))
	}
	if len(ids) != 19 {
		t.Fatalf("%d rows", len(ids))
	}
	slices.Sort(ids)
	byID := sha256.Sum256([]byte(strings.Join(ids, "\n") + "\n"))
	if result.RowsSHA256 != hex.EncodeToString(byID[:]) {
		t.Errorf("rows sha256 %q, the sorted ids hash to %x", result.RowsSHA256, byID)
	}

	var none bytes.Buffer
	empty, err := Record(Options{Rows: &none, Catalog: pinned(t)}, "claude-code", adapter.Report{CostBasis: focus.Measured})
	if err != nil {
		t.Fatal(err)
	}
	const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if empty.RowsFileSHA256 != emptySHA256 || empty.RowsSHA256 != "" {
		t.Errorf("no rows: file %q, rows %q", empty.RowsFileSHA256, empty.RowsSHA256)
	}
}

// TestModelOutsideAllowlistWritesUnknownModel holds that a model name the
// comment's allowlist refuses never reaches a row: the row reads
// unknown-model in its tag, its SkuId and its description, is priced at the
// fallback as the name itself would have been, and one warning counts the
// requests, nested sessions included. A name the allowlist passes stands, and
// the caller's report is left as it was.
func TestModelOutsideAllowlistWritesUnknownModel(t *testing.T) {
	hostile := []string{
		"evil\n::error title=Costroid::Budget exceeded",
		"=HYPERLINK(\"https://evil.example\")",
		"-claude-haiku-5-5",
		strings.Repeat("a", 129),
		"",
	}
	var requests []adapter.Request
	for i, model := range hostile[:4] {
		request := haikuRequest(fmt.Sprintf("hostile-%d", i), adapter.Tokens{Output: 1000})
		request.Model = model
		requests = append(requests, request)
	}
	kept := haikuRequest("kept", adapter.Tokens{Output: 1000})
	kept.Model = strings.Repeat("a", 128)
	nested := haikuRequest("nested", adapter.Tokens{Output: 1000})
	nested.Model = hostile[4]
	report := adapter.Report{CostBasis: focus.Measured, Requests: append(requests, haikuRequest("allowed", adapter.Tokens{Output: 1000}), kept),
		Nested: []adapter.Report{{SessionID: "nested", CostBasis: focus.Measured, Requests: []adapter.Request{nested}}}}
	var rows bytes.Buffer
	result, err := Record(Options{Rows: &rows, Catalog: pinned(t)}, "claude-code", report)
	if err != nil {
		t.Fatal(err)
	}
	const warning = "5 requests named a model outside the allowed characters, so their rows read unknown-model"
	if !slices.Contains(result.Report.Warnings, warning) {
		t.Errorf("warnings %q", result.Report.Warnings)
	}
	models := map[string]int{}
	for _, row := range parseRows(t, rows.Bytes()) {
		model := tagOf(row, focus.TagModel)
		models[model]++
		if model == "unknown-model" && (row["SkuId"] != "anthropic/unknown-model/output" || row["SkuPriceId"] != "anthropic:fallback:output" || !strings.HasPrefix(row["ChargeDescription"].(string), "unknown-model output tokens")) {
			t.Errorf("%v %v %q", row["SkuId"], row["SkuPriceId"], row["ChargeDescription"])
		}
	}
	for _, part := range []string{"::error", "HYPERLINK", "-claude-haiku-5-5", strings.Repeat("a", 129)} {
		if strings.Contains(rows.String(), part) {
			t.Errorf("the rows hold %q", part)
		}
	}
	want := map[string]int{"unknown-model": 5, "claude-haiku-5-5": 1, strings.Repeat("a", 128): 1}
	if !maps.Equal(models, want) {
		t.Errorf("models %v", models)
	}
	if report.Requests[0].Model != hostile[0] || report.Nested[0].Requests[0].Model != "" {
		t.Error("the caller's report was changed")
	}
}
