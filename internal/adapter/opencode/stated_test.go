package opencode_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/adapter/opencode"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/run"
)

// fakeOpenCode stands in for OpenCode 1.19.4: it answers --version, and for
// run prints the recorded stream.
const fakeOpenCode = `#!/bin/sh
if [ "$1" = --version ]; then echo 1.19.4; exit 0; fi
cat "$FAKE_STREAM"
`

// TestOpenCodeStatesItsOwnUSD runs the recorded OpenCode run under costroid
// run and reports it. OpenCode's own figure, its two step costs 0.0013544 and
// 0.00017877 added, reaches the result object as USD 0.00153317 from
// opencode, and the comment states it once on its own line. Costroid's
// figure is the rows' own, USD 0.00153317 at catalog 0.4.2, and the cap
// compares with it alone: no amount reads the two added, 0.00306634.
func TestOpenCodeStatesItsOwnUSD(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "opencode")
	if err := os.WriteFile(command, []byte(fakeOpenCode), 0o755); err != nil {
		t.Fatal(err)
	}
	stream, err := filepath.Abs(filepath.Join("testdata", "1.19.4", "opencode-run", "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"FAKE_STREAM": stream, "ANTHROPIC_API_KEY": "test-key", "OPENCODE_AUTH_CONTENT": "",
		"HOME": t.TempDir(), "XDG_CONFIG_HOME": t.TempDir(), "XDG_DATA_HOME": t.TempDir(),
	} {
		t.Setenv(name, value)
	}
	t.Chdir(t.TempDir())
	pinned, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	var rows, stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	result, err := run.Run(ctx, run.Options{
		Agent:   opencode.Name,
		Argv:    []string{command, "run", "--model", "anthropic/claude-haiku-5-5", "work"},
		Budget:  decimal.MustParse("1"),
		Rows:    &rows,
		Stdout:  &stdout,
		Stderr:  &stderr,
		Catalog: pinned,
		Grace:   time.Second,
	})
	if err != nil {
		t.Fatalf("the run failed: %v\n%s", err, stderr.String())
	}
	want := adapter.Stated{Unit: opencode.StatedUnit, Quantity: "0.00153317", Source: opencode.Name}
	if stated := result.Report.AgentStated; stated == nil || *stated != want {
		t.Fatalf("the run states %+v, want %+v", stated, want)
	}

	ledger, err := comment.ReadRows(bytes.NewReader(rows.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	budget := decimal.MustParse("1")
	input := comment.Input{Ledger: ledger, State: comment.Reported, HeadSHA: strings.Repeat("a", 40), Budget: &budget, Amounts: comment.AmountsShown, AgentStated: result.Report.AgentStated}
	encoded, err := comment.Result(input)
	if err != nil {
		t.Fatal(err)
	}
	var block comment.Block
	if err := json.Unmarshal([]byte(encoded), &block); err != nil {
		t.Fatal(err)
	}
	if block.AgentStated == nil || *block.AgentStated != (comment.BlockAgentStated{Unit: "USD", Quantity: "0.00153317", Source: "opencode"}) {
		t.Errorf("the result object states %+v", block.AgentStated)
	}
	if len(block.Figures) != 1 || block.Figures[0].ListCost != "0.00153317" || block.Cap == nil || block.Cap.Cost == nil || *block.Cap.Cost != "0.00153317" {
		t.Errorf("figures %+v, cap %+v: want the rows' USD 0.00153317 alone", block.Figures, block.Cap)
	}
	body, err := comment.Comment(input)
	if err != nil {
		t.Fatal(err)
	}
	line := "OpenCode stated USD 0.00153317 for this run by its own prices, which Costroid never adds to its own figures."
	if strings.Count(body, line) != 1 {
		t.Errorf("the comment does not state %q once:\n%s", line, body)
	}
	if strings.Contains(body, "0.00306634") {
		t.Errorf("the comment adds OpenCode's figure to Costroid's:\n%s", body)
	}
}
