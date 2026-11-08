package ghaw_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/adapter/ghaw"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

// recordedCodex is dogfood run 42469416753, gh-aw v1.0.1 running Codex
// 0.164.0 behind firewall v0.29.0 with max-ai-credits: 10, recorded with
// scripts/record as gh run download wrote its usage and info artifacts.
var recordedCodex = filepath.Join("testdata", "0.29.0", "codex")

// TestGhAwRealCodexRun reads the recorded run as ingest-run does, with its
// info artifact beside its usage artifact. Its 5 requests price to USD
// 0.0370553 at list, measured, gh-aw stated 3.70553 AI credits, and the note
// names the proxy's cap of 10.
//
// gh-aw states "One AIC equals $0.01 USD" (docs/src/content/docs/reference/
// cost-management.md at github/gh-aw v1.0.1, read 2026-11-08). On this run the
// credits gh-aw stated are the USD figure Costroid priced from the catalog,
// in hundredths, to every digit. That is a match observed on one run of one
// model, not a rule Costroid applies: it never converts credits.
func TestGhAwRealCodexRun(t *testing.T) {
	report, err := ghaw.ReadUsage(os.DirFS(filepath.Join(recordedCodex, "usage")), os.DirFS(filepath.Join(recordedCodex, "info")))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Requests) != 5 || report.CostBasis != focus.Measured || len(report.Warnings) != 0 {
		t.Errorf("%d requests, basis %s, warnings %q", len(report.Requests), report.CostBasis, report.Warnings)
	}
	rows := rowsOf(t, report)
	var total decimal.Decimal
	for _, r := range rows {
		cost, err := decimal.Parse(r.ListCost)
		if err != nil {
			t.Fatal(err)
		}
		total = total.Add(cost)
	}
	if len(rows) != 18 || total.String() != "0.0370553" {
		t.Errorf("%d rows come to USD %s, want 18 rows and USD 0.0370553", len(rows), total)
	}
	file, classes := fileSums(t, filepath.Join(recordedCodex, "usage")), rowSums(t, rows)
	if classes[focus.InputUncached]+classes[focus.CacheRead] != file["input_tokens"] || classes[focus.Output] != file["output_tokens"] || classes[focus.CacheRead] != file["cache_read_tokens"] {
		t.Errorf("the rows hold %v, and the records %v", classes, file)
	}
	stated := report.AgentStated
	if stated == nil || *stated != (adapter.Stated{Unit: adapter.UnitAICredits, Quantity: "3.70553", Source: ghaw.Name}) {
		t.Fatalf("stated %+v, want 3.70553 AIC from gh-aw", stated)
	}
	if hundredths := total.Mul(decimal.FromInt(100)); hundredths.String() != stated.Quantity {
		t.Errorf("USD %s is %s hundredths of a dollar, and gh-aw stated %s AI credits", total, hundredths, stated.Quantity)
	}
	if want := "none: gh-aw's proxy held this run to 10 AI credits, priced by gh-aw"; report.Enforcement != (adapter.Enforcement{Grade: adapter.GradeNone, Note: want}) {
		t.Errorf("enforcement %+v, want grade none with %q", report.Enforcement, want)
	}
}

// TestGhAwReadsTheCapFromEitherArtifact names the cap either artifact's
// aw_info.json states, and names none when the two state different caps.
func TestGhAwReadsTheCapFromEitherArtifact(t *testing.T) {
	stating := func(limit string) *fstest.MapFile {
		return &fstest.MapFile{Data: []byte(`{"model_config":{"max_ai_credits":` + limit + `}}`)}
	}
	for name, tc := range map[string]struct {
		usage, info   *fstest.MapFile
		note, warning string
	}{
		"the info artifact alone":      {info: stating("10"), note: "held this run to 10 AI credits"},
		"the usage artifact alone":     {usage: stating("12"), note: "held this run to 12 AI credits"},
		"both, stating one cap":        {usage: stating("10"), info: stating("10.0"), note: "held this run to 10 AI credits"},
		"both, stating two caps":       {usage: stating("12"), info: stating("10"), note: "no budget held it", warning: "states a cap of 12 AI credits and the info artifact's 10, so no cap is named"},
		"an info artifact with no cap": {info: &fstest.MapFile{Data: []byte(`{}`)}, note: "no budget held it"},
	} {
		t.Run(name, func(t *testing.T) {
			usage := artifactWith(record("openai", "gpt-6.1-sol", `"input_tokens":1`))
			if tc.usage != nil {
				usage["aw_info.json"] = tc.usage
			}
			var info fstest.MapFS
			if tc.info != nil {
				info = fstest.MapFS{"aw_info.json": tc.info}
			}
			report, err := ghaw.ReadUsage(usage, infoFS(info))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(report.Enforcement.Note, tc.note) || tc.warning != "" && !hasWarning(report, tc.warning) {
				t.Errorf("note %q, warnings %q", report.Enforcement.Note, report.Warnings)
			}
		})
	}
	bad := fstest.MapFS{"aw_info.json": &fstest.MapFile{Data: []byte(`{"model_config":{"max_ai_credits":0}}`)}}
	if _, err := ghaw.ReadUsage(artifactWith(record("openai", "gpt-6.1-sol", `"input_tokens":1`)), bad); err == nil || !strings.Contains(err.Error(), "info/aw_info.json states max_ai_credits 0") {
		t.Errorf("a cap of 0 in the info artifact: %v", err)
	}
}

// infoFS is info as ReadUsage takes it, nil for no info artifact.
func infoFS(info fstest.MapFS) fs.FS {
	if info == nil {
		return nil
	}
	return info
}
