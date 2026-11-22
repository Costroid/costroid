package recipelint

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestReceiptsInputIsHeldToTheReportStage edits the Claude Code recipe, and
// holds lint-recipes to a finding and costroid lint to a note for a receipts
// input that reads no receipt: on the agent's stage, or neither true nor
// false. On the report's step, true and an expression are kept.
func TestReceiptsInputIsHeldToTheReportStage(t *testing.T) {
	source := read(t, filepath.Join(recipesDir, "github-claude-code.yml"))
	const run, report = "          stage: run\n", "          stage: report\n"
	if !strings.Contains(source, run) || !strings.Contains(source, report) {
		t.Fatalf("the recipe has no run step or no report step")
	}
	edits := []edit{
		{"receipts on the agent's stage", run, run + "          receipts: \"true\"\n", RuleReceiptsInput},
		{"receipts neither true nor false", report, report + "          receipts: \"yes\"\n", RuleReceiptsInput},
	}
	for _, change := range edits {
		t.Run(change.name, func(t *testing.T) {
			expectRule(t, source, change)
			edited := strings.Replace(source, change.from, change.to, 1)
			notes := LintCustomer("x.yml", []byte(edited)).Notes
			if !slices.ContainsFunc(notes, func(f Finding) bool { return f.Rule == RuleReceiptsInput && f.Severity == Note }) {
				t.Errorf("costroid lint notes %v", notes)
			}
		})
	}
	for _, kept := range []string{"\"true\"", "\"false\"", "${{ inputs.receipts }}"} {
		edited := strings.Replace(source, report, report+"          receipts: "+kept+"\n", 1)
		if found := LintBytes("x.yml", []byte(edited)).Findings; slices.ContainsFunc(found, func(f Finding) bool { return f.Rule == RuleReceiptsInput }) {
			t.Errorf("receipts %s on the report step: %v", kept, found)
		}
	}
}
