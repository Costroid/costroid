package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/decimal"
)

// ghawArtifact is a gh-aw usage artifact written by hand, as
// internal/adapter/ghaw/testdata/not-recorded/README.md says.
const ghawArtifact = "../../internal/adapter/ghaw/testdata/not-recorded/claude"

// creditsField matches both AI-credit figures of a token-usage record.
var creditsField = regexp.MustCompile(`"ai_credits_(this_response|total)":[0-9.]+`)

// copyArtifact copies the artifact to a new directory, with every AI-credit
// figure in its records replaced by credits, or as it is when credits is
// empty.
func copyArtifact(t *testing.T, credits string) string {
	t.Helper()
	dir := t.TempDir()
	err := filepath.WalkDir(ghawArtifact, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		name, err := filepath.Rel(ghawArtifact, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if credits != "" {
			data = creditsField.ReplaceAll(data, []byte(`"ai_credits_$1":`+credits))
		}
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, name), data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// ingestGhAw runs ingest-run on an artifact and returns the rows file, the
// run's object and what it printed.
func ingestGhAw(t *testing.T, artifact string) (rows, object []byte, stderr string) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "rows.jsonl")
	code, _, stderr := execute(t, "ingest-run", "--from", "gh-aw="+artifact, "--out", out)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	rows, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	object, err = os.ReadFile(runResultPath(out))
	if err != nil {
		t.Fatal(err)
	}
	return rows, object, stderr
}

// TestGhAwStatedCreditsNeverAddToUSD prices a gh-aw run from its tokens alone.
// The same records with AI-credit figures a million times larger write the
// same rows, the same object and the same USD line; only gh-aw's own line,
// which names its unit, differs.
func TestGhAwStatedCreditsNeverAddToUSD(t *testing.T) {
	clearCI(t)
	rows, object, stderr := ingestGhAw(t, copyArtifact(t, "1.5"))
	larger, largerObject, largerStderr := ingestGhAw(t, copyArtifact(t, "1500000.5"))
	if !bytes.Equal(rows, larger) || !bytes.Equal(object, largerObject) {
		t.Error("the AI credits changed the rows or the run's object")
	}
	var total decimal.Decimal
	for _, line := range strings.Split(strings.TrimSpace(string(rows)), "\n") {
		var row struct{ ListCost string }
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		cost, err := decimal.Parse(row.ListCost)
		if err != nil {
			t.Fatal(err)
		}
		total = total.Add(cost)
	}
	usdLine := "costroid: USD " + total.String() + " at list, Measured\n"
	if !strings.Contains(stderr, usdLine) || !strings.Contains(largerStderr, usdLine) {
		t.Errorf("no %q in\n%s\n%s", usdLine, stderr, largerStderr)
	}
	for output, credits := range map[string]string{stderr: "3", largerStderr: "3000001"} {
		want := "costroid: gh-aw stated " + credits + " AI credits for this run. Costroid does not convert credits.\n"
		if !strings.Contains(output, want) {
			t.Errorf("no %q in %s", want, output)
		}
	}
}

// TestIngestRunRefusesAGhAwArtifactWithAnExtraEntry writes no rows from an
// artifact that holds a file gh-aw does not write, and says which.
func TestIngestRunRefusesAGhAwArtifactWithAnExtraEntry(t *testing.T) {
	clearCI(t)
	artifact := copyArtifact(t, "1")
	if err := os.WriteFile(filepath.Join(artifact, "costroid"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "rows.jsonl")
	code, _, stderr := execute(t, "ingest-run", "--from", "gh-aw="+artifact, "--out", out)
	if code != exitFailure || !strings.Contains(stderr, "the usage artifact holds costroid, which gh-aw does not write there") {
		t.Errorf("exit %d: %s", code, stderr)
	}
	if rows, err := os.ReadFile(out); err != nil || len(rows) != 0 {
		t.Errorf("rows %q, %v", rows, err)
	}
}

// TestGhAwDocShowsWhatIngestRunPrints runs the command docs/gh-aw.md shows on
// the artifact it names, and finds every line the page quotes.
func TestGhAwDocShowsWhatIngestRunPrints(t *testing.T) {
	clearCI(t)
	page, err := os.ReadFile("../../docs/gh-aw.md")
	if err != nil {
		t.Fatal(err)
	}
	command, printed := docBlock(t, string(page), "sh", "costroid ingest-run"), docBlock(t, string(page), "text", "costroid: gh-aw,")
	artifact := copyArtifact(t, "")
	dir := t.TempDir()
	if err := os.Rename(artifact, filepath.Join(dir, "gh-aw-usage")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	code, _, stderr := execute(t, strings.Fields(command)[1:]...)
	if code != 0 {
		t.Fatalf("%s: exit %d: %s", command, code, stderr)
	}
	for _, line := range strings.Split(printed, "\n") {
		if !strings.Contains(stderr, line+"\n") {
			t.Errorf("the page quotes %q, and the command printed:\n%s", line, stderr)
		}
	}
}

// docBlock is the line of a fenced block of the page that starts with
// prefix, or for a text block, the whole block that starts with it.
func docBlock(t *testing.T, page, language, prefix string) string {
	t.Helper()
	for _, block := range strings.Split(page, "```"+language+"\n")[1:] {
		body, _, _ := strings.Cut(block, "```")
		if language == "text" && strings.HasPrefix(body, prefix) {
			return strings.TrimSuffix(body, "\n")
		}
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(line, prefix) {
				return line
			}
		}
	}
	t.Fatalf("docs/gh-aw.md has no %s block with %q", language, prefix)
	return ""
}
