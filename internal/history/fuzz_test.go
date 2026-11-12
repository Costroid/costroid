package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/comment"
)

// FuzzHistoryParse reads any check run text, as a pull request's checks can
// carry it. A text reads the same way twice. An object it takes is in a
// schema this release adds up, shows its amounts, leaves no figure out, and
// prices every figure; a digest it keeps is a SHA-256 digest from a schema
// that states one.
func FuzzHistoryParse(f *testing.F) {
	data, err := os.ReadFile(filepath.Join("testdata", "check-runs", "check-runs.json"))
	if err != nil {
		f.Fatal(err)
	}
	var recorded struct {
		CheckRuns []struct {
			Output struct {
				Text string `json:"text"`
			} `json:"output"`
		} `json:"check_runs"`
	}
	if err := json.Unmarshal(data, &recorded); err != nil {
		f.Fatal(err)
	}
	for _, run := range recorded.CheckRuns {
		f.Add(run.Output.Text)
	}
	f.Add(fence + `{"schema":"costroid.result/0.9","amounts":"shown","figures_omitted":0,"rows_sha256":"` + strings.Repeat("g", 64) + `","figures":[{"currency":"USD","list_cost":"-1"}]}` + fenceEnd)
	f.Add(fence + `{"schema":"costroid.result/0.6","amounts":"shown","rows_sha256":"0000000000000000000000000000000000000000000000000000000000000000","figures":[]}` + fenceEnd)
	f.Add(fence + `{"schema":"costroid.result/0.9","amounts":"withheld","figures_omitted":0,"figures":[]}` + fenceEnd)
	f.Add("```json\n{}")
	f.Fuzz(func(t *testing.T, text string) {
		report, err := parseText(text)
		again, againErr := parseText(text)
		if (err == nil) != (againErr == nil) || (err != nil && err.Error() != againErr.Error()) || !reflect.DeepEqual(report, again) {
			t.Fatalf("one text read two ways: %v, then %v", err, againErr)
		}
		if err != nil {
			return
		}
		block := report.Block
		if !slices.Contains(schemas, block.Schema) || block.Amounts != "shown" || block.FiguresOmitted != 0 {
			t.Fatalf("took schema %q, amounts %q, %d figures left out", block.Schema, block.Amounts, block.FiguresOmitted)
		}
		for _, figure := range block.Figures {
			if _, err := comment.FigureCost(figure); err != nil {
				t.Fatalf("took a figure it cannot price: %v", err)
			}
		}
		if report.RowsSHA256 != "" && (!digestPattern.MatchString(report.RowsSHA256) || !slices.Contains(digestSchemas, block.Schema)) {
			t.Fatalf("kept the digest %q from %s", report.RowsSHA256, block.Schema)
		}
	})
}
