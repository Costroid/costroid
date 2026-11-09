package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestRunJSONStatesTheAgentsOwnFigure runs the recorded OpenCode run under
// costroid run --agent opencode --json. The object it prints and writes
// beside the rows holds to result/v0.9 and states OpenCode's own figure, USD
// 0.00153317 from opencode, under agent_stated, apart from the rows' own
// figure, so a later report can copy it from there. No opencode.json lies
// between the package's directory and the repository's root.
func TestRunJSONStatesTheAgentsOwnFigure(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "opencode")
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 1.19.4; exit 0; fi\ncat \"$FAKE_STREAM\"\n"
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	stream, err := filepath.Abs(filepath.Join("..", "..", "internal", "adapter", "opencode", "testdata", "1.19.4", "opencode-run", "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"FAKE_STREAM": stream, "ANTHROPIC_API_KEY": "test-key", "OPENCODE_AUTH_CONTENT": "",
		"HOME": t.TempDir(), "XDG_CONFIG_HOME": t.TempDir(), "XDG_DATA_HOME": t.TempDir(),
	} {
		t.Setenv(name, value)
	}
	out := filepath.Join(dir, "rows.jsonl")
	code, stdout, stderr := execute(t, "run", "--agent", "opencode", "--budget-usd", "1", "--out", out, "--json", "--", command, "run", "--model", "anthropic/claude-haiku-5-5", "work")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	validateResult(t, stdout)
	if written := readFile(t, runResultPath(out)); written != stdout {
		t.Errorf("the object beside the rows is not the one printed:\n%s", written)
	}
	var object struct {
		Figures []struct {
			ListCost string `json:"list_cost"`
		} `json:"figures"`
		AgentStated *struct {
			Unit     string `json:"unit"`
			Quantity string `json:"quantity"`
			Source   string `json:"source"`
		} `json:"agent_stated"`
	}
	if err := json.Unmarshal([]byte(stdout), &object); err != nil {
		t.Fatal(err)
	}
	if stated := object.AgentStated; stated == nil || stated.Unit != "USD" || stated.Quantity != "0.00153317" || stated.Source != "opencode" {
		t.Errorf("agent_stated %+v, want OpenCode's USD 0.00153317", stated)
	}
	if len(object.Figures) != 1 || object.Figures[0].ListCost != "0.00153317" {
		t.Errorf("figures %+v, want the rows' USD 0.00153317 alone", object.Figures)
	}
}
