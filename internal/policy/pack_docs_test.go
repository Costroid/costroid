package policy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fencedAfter is the body of the first block fenced as language after marker
// in page, empty when there is none.
func fencedAfter(page, marker, language string) string {
	_, rest, found := strings.Cut(page, marker)
	if !found {
		return ""
	}
	_, block, found := strings.Cut(rest, "```"+language+"\n")
	if !found {
		return ""
	}
	body, _, _ := strings.Cut(block, "\n```\n")
	return body + "\n"
}

// TestPolicyPageRunsItsPackExample runs the pack docs/policy.md gives, under
// the costroid.yml it gives, over the recorded run the page names, through
// the child the report starts. The verdict concludes as the page says, and
// the pack returns exactly the results the page's JSON shows.
func TestPolicyPageRunsItsPackExample(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("..", "..", "docs", "policy.md"))
	if err != nil {
		t.Fatal(err)
	}
	const section = "\n## A repository's own pack\n"
	config, source, shown := fencedAfter(string(page), section, "yaml"), fencedAfter(string(page), section, "rego"), fencedAfter(string(page), section, "json")
	if config == "\n" || source == "\n" || shown == "\n" || !strings.Contains(string(page), section) {
		t.Fatal("docs/policy.md has no pack section with a costroid.yml, a pack and the verdict's pack")
	}
	file := filepath.Join(t.TempDir(), "policy.rego")
	if err := os.WriteFile(file, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	rows, err := os.Open(filepath.Join("..", "comment", "testdata", "rows.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	usage, err := ReadUsage(rows)
	if err != nil {
		t.Fatal(err)
	}
	read := ConfigFile{Data: []byte(config), Found: true}
	verdict := evaluate(t, Request{Usage: usage, Trust: Unverified, Config: read, Pack: PackFromFile(file, read)})
	var stated struct {
		Pack struct {
			Path    string       `json:"path"`
			Results []PackResult `json:"results"`
		} `json:"pack"`
	}
	if err := json.Unmarshal([]byte("{"+shown+"}"), &stated); err != nil {
		t.Fatalf("the page's JSON: %v", err)
	}
	if verdict.Pack == nil || verdict.Pack.Path != stated.Pack.Path || !slices.Equal(verdict.Pack.Results, stated.Pack.Results) || verdict.Conclusion != Neutral {
		t.Errorf("the page's pack concludes %s with %+v, and the page shows %+v", verdict.Conclusion, verdict.Pack, stated.Pack)
	}
	if len(verdict.Results) == 0 || verdict.Results[len(verdict.Results)-1].Rule == RuleCustomerPack {
		t.Errorf("the page's pack did not run: %+v", verdict.Results)
	}
}
