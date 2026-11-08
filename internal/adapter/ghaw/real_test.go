package ghaw_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/adapter/ghaw"
	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

// recordedCodex is dogfood run 42469416753, gh-aw v1.0.1 running Codex
// 0.164.0 behind firewall v0.29.0 with max-ai-credits: 10, recorded with
// scripts/record as gh run download wrote its usage and info artifacts.
var recordedCodex = filepath.Join("testdata", "0.29.0", "codex")

// recordedClaude is dogfood run 42486902241, gh-aw v1.0.1 running Claude Code
// 2.1.310 on claude-sonnet-5 behind firewall v0.29.0 with max-ai-credits: 10,
// recorded the same way. gh-aw stated 11.80342 AI credits for it, and opened
// an issue that the run hit its max-ai-credits guardrail.
var recordedClaude = filepath.Join("testdata", "0.29.0", "claude")

// readRecorded reads a recorded run as ingest-run does, with its info
// artifact beside its usage artifact.
func readRecorded(t *testing.T, dir string) adapter.Report {
	t.Helper()
	report, err := ghaw.ReadUsage(os.DirFS(filepath.Join(dir, "usage")), os.DirFS(filepath.Join(dir, "info")))
	if err != nil {
		t.Fatal(err)
	}
	return report
}

// listTotal adds up the rows' list cost.
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

// enforcementCell is the Enforcement cell of the comment the report's rows
// get, with amounts shown and gh-aw's credits as the run's object states them.
func enforcementCell(t *testing.T, report adapter.Report) string {
	t.Helper()
	ledger, err := comment.ReadRows(bytes.NewReader(writtenRows(t, report)))
	if err != nil {
		t.Fatal(err)
	}
	text, err := comment.Comment(comment.Input{Ledger: ledger, State: comment.Reported, Amounts: comment.AmountsShown, AgentStated: report.AgentStated})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(text, "\n") {
		if cells := strings.Split(line, " | "); strings.HasPrefix(line, "| USD ") && len(cells) == 4 {
			return strings.TrimSuffix(cells[3], " |")
		}
	}
	t.Fatalf("the comment has no figure line:\n%s", text)
	return ""
}

// TestGhAwRealCodexRun reads the recorded Codex run. Its 5 requests price to
// USD 0.0370553 at list, measured, gh-aw stated 3.70553 AI credits, and the
// note and the comment's Enforcement cell name the cap of 10 that gh-aw
// checks. The credits are under it, so neither states anything past it.
//
// gh-aw states "One AIC equals $0.01 USD" (docs/src/content/docs/reference/
// cost-management.md at github/gh-aw v1.0.1, read 2026-11-08). On this run the
// credits gh-aw stated are the USD figure Costroid priced from the catalog,
// in hundredths, to every digit. That is a match observed on one run of one
// model, not a rule Costroid applies: it never converts credits.
func TestGhAwRealCodexRun(t *testing.T) {
	report := readRecorded(t, recordedCodex)
	if len(report.Requests) != 5 || report.CostBasis != focus.Measured || len(report.Warnings) != 0 {
		t.Errorf("%d requests, basis %s, warnings %q", len(report.Requests), report.CostBasis, report.Warnings)
	}
	rows := rowsOf(t, report)
	total := listTotal(t, rows)
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
	const capped = "a cap of 10 AI credits that gh-aw checks, priced by gh-aw, not by Costroid"
	if want := "none: " + capped; report.Enforcement != (adapter.Enforcement{Grade: adapter.GradeNone, Note: want}) {
		t.Errorf("enforcement %+v, want grade none with %q", report.Enforcement, want)
	}
	if cell := enforcementCell(t, report); cell != "none: "+capped {
		t.Errorf("the Enforcement cell reads %q, want %q", cell, "none: "+capped)
	}
}

// TestGhAwRealClaudeRunPassedItsCap reads the recorded Claude run, which
// ended past its cap. gh-aw's firewall checks the cap before it forwards a
// request, so the second request, sent at 8.87465 AI credits, went through
// and took the run to 11.80342. Its 2 requests price to 7 rows and USD
// 0.1180342 at list, measured, and the note and the Enforcement cell state
// the credits and the 1.80342 AI credits past the cap of 10, with every
// digit, and never that the cap held the run. The credits are USD x 100 here
// too, observed and never applied.
func TestGhAwRealClaudeRunPassedItsCap(t *testing.T) {
	report := readRecorded(t, recordedClaude)
	if len(report.Requests) != 2 || report.CostBasis != focus.Measured || len(report.Warnings) != 0 {
		t.Errorf("%d requests, basis %s, warnings %q", len(report.Requests), report.CostBasis, report.Warnings)
	}
	rows := rowsOf(t, report)
	total := listTotal(t, rows)
	if len(rows) != 7 || total.String() != "0.1180342" {
		t.Errorf("%d rows come to USD %s, want 7 rows and USD 0.1180342", len(rows), total)
	}
	file, classes := fileSums(t, filepath.Join(recordedClaude, "usage")), rowSums(t, rows)
	if classes[focus.InputUncached] != file["input_tokens"] || classes[focus.CacheRead] != file["cache_read_tokens"] || classes[focus.CacheWrite5m] != file["cache_write_tokens"] || classes[focus.Output] != file["output_tokens"] {
		t.Errorf("the rows hold %v, and the records %v", classes, file)
	}
	stated := report.AgentStated
	if stated == nil || *stated != (adapter.Stated{Unit: adapter.UnitAICredits, Quantity: "11.80342", Source: ghaw.Name}) {
		t.Fatalf("stated %+v, want 11.80342 AIC from gh-aw", stated)
	}
	if hundredths := total.Mul(decimal.FromInt(100)); hundredths.String() != stated.Quantity {
		t.Errorf("USD %s is %s hundredths of a dollar, and gh-aw stated %s AI credits", total, hundredths, stated.Quantity)
	}
	const passed = "none: a cap of 10 AI credits that gh-aw checks, priced by gh-aw, not by Costroid; gh-aw stated 11.80342 AI credits, over the cap by 1.80342 AI credits"
	if report.Enforcement != (adapter.Enforcement{Grade: adapter.GradeNone, Note: passed}) {
		t.Errorf("enforcement %+v, want grade none with %q", report.Enforcement, passed)
	}
	if cell := enforcementCell(t, report); cell != passed {
		t.Errorf("the Enforcement cell reads %q, want %q", cell, passed)
	}
}

// TestGhAwCapSentenceStatesOnlyCreditsPastTheCap states gh-aw's credits and
// the difference only when they pass the cap. Credits at or under it, credits
// another source states, and no credits name the cap alone.
func TestGhAwCapSentenceStatesOnlyCreditsPastTheCap(t *testing.T) {
	const alone = "a cap of 10 AI credits that gh-aw checks, priced by gh-aw, not by Costroid"
	credits := func(quantity string) *adapter.Stated {
		return &adapter.Stated{Unit: adapter.UnitAICredits, Quantity: quantity, Source: ghaw.Name}
	}
	for name, tc := range map[string]struct {
		stated *adapter.Stated
		want   string
	}{
		"no credits":           {nil, alone},
		"under the cap":        {credits("9.99999"), alone},
		"at the cap":           {credits("10.000"), alone},
		"past it by a digit":   {credits("10.00001"), alone + "; gh-aw stated 10.00001 AI credits, over the cap by 0.00001 AI credits"},
		"past it many times":   {credits("1500000.5"), alone + "; gh-aw stated 1500000.5 AI credits, over the cap by 1499990.5 AI credits"},
		"another source":       {&adapter.Stated{Unit: adapter.UnitAICredits, Quantity: "11", Source: "codex"}, alone},
		"credits not a figure": {credits("11e0"), alone},
	} {
		if got := ghaw.CapSentence("10", tc.stated); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
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
		"the info artifact alone":      {info: stating("10"), note: "a cap of 10 AI credits that gh-aw checks"},
		"the usage artifact alone":     {usage: stating("12"), note: "a cap of 12 AI credits that gh-aw checks"},
		"both, stating one cap":        {usage: stating("10"), info: stating("10.0"), note: "a cap of 10 AI credits that gh-aw checks"},
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
