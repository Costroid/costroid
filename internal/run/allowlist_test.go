package run_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/run"
)

// TestAModelRowStatesIsOneTheCommentShows holds the rows' model allowlist to
// the comment's own: a name the comment would show stands, and every other
// reads unknown-model.
func TestAModelRowStatesIsOneTheCommentShows(t *testing.T) {
	names := []string{
		"claude-haiku-5-5", "gpt-5.6-sol", "us.anthropic.claude-opus-5-5:0", "models/gemini+x_1", "0",
		strings.Repeat("m", 128), strings.Repeat("m", 129), "", "-x", ".x", "_x", "a b", "a`b", "a@b", "a\nb", "aé", "<a>", "=1+2",
	}
	pinned, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, name := range names {
		report := adapter.Report{CostBasis: focus.Measured, Requests: []adapter.Request{{
			ID: "r", Provider: "anthropic", Model: name, Grain: adapter.PerRequest,
			ServiceTier: catalog.TierStandard, Region: catalog.RegionGlobal, Start: at, End: at.Add(time.Second),
			Tokens: adapter.Tokens{Output: 1},
		}}}
		var rows bytes.Buffer
		if _, err := run.Record(run.Options{Rows: &rows, Catalog: pinned}, "claude-code", report); err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		want := "unknown-model"
		if string(comment.Clean(name)) == name {
			want = name
		}
		scanner := bufio.NewScanner(&rows)
		for scanner.Scan() {
			var row struct {
				Tags map[string]string
			}
			if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
				t.Fatal(err)
			}
			if got := row.Tags[focus.TagModel]; got != want {
				t.Errorf("%q reads %q, want %q", name, got, want)
			}
		}
	}
}
