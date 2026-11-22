package comment

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/docstest"
)

// shownParts are the blocks of the documentation that show a part of a
// comment or of its machine block, each with the golden state that renders
// it. Where a place holds two blocks, the states stand in the page's order.
// part picks what a block shows out of the state's machine block, and is nil
// for lines of the comment itself.
var shownParts = []struct {
	at     docstest.Place
	states []string
	part   func(block map[string]any) any
}{
	{docstest.Place{Page: "docs/agents.md", Heading: "Read the figures", Language: "json"}, []string{"codex-stopped-overshoot"}, first("figures")},
	{docstest.Place{Page: "docs/agents.md", Heading: "Read the cap", Language: "json"}, []string{"codex-stopped-overshoot"}, field("cap")},
	{docstest.Place{Page: "docs/agents.md", Heading: "Read the pull request's total", Language: "json"}, []string{"lifetime"}, field("lifetime")},
	{docstest.Place{Page: "docs/infrastructure.md", Heading: "What the comment shows", Language: "json"}, []string{"infrastructure"}, first("estimates")},
	{docstest.Place{Page: "docs/policy.md", Heading: "What a verdict records", Language: "json"}, []string{"fail-shown"}, member("policy")},
	{docstest.Place{Page: "docs/infrastructure.md", Heading: "What the comment shows", Language: "markdown"}, []string{"infrastructure"}, nil},
	{docstest.Place{Page: "docs/infrastructure.md", Heading: "A fork's pull request", Language: "markdown"}, []string{"infrastructure-not-available"}, nil},
	{docstest.Place{Page: "docs/receipt.md", Heading: "What the line says", Language: "text"}, []string{"receipt", "receipt-none"}, nil},
}

// field is one field of the machine block.
func field(name string) func(map[string]any) any {
	return func(block map[string]any) any { return block[name] }
}

// first is the first entry of a list the machine block holds.
func first(name string) func(map[string]any) any {
	return func(block map[string]any) any {
		list, _ := block[name].([]any)
		if len(list) == 0 {
			return nil
		}
		return list[0]
	}
}

// member is one field of the machine block with its name, as a page shows a
// field cut out of the object: "name": value.
func member(name string) func(map[string]any) any {
	return func(block map[string]any) any { return map[string]any{name: block[name]} }
}

// stoodFor is a value a page writes in place of one that names nothing a
// reader needs, such as a digest: <SHA-256 of costroid.yml>.
var stoodFor = regexp.MustCompile(`^<[^<>]+>$`)

// shows reports whether a page's value is the state's. A string the page
// writes as <words> stands for any string the state holds there.
func shows(page, state any) bool {
	switch page := page.(type) {
	case map[string]any:
		held, ok := state.(map[string]any)
		if !ok || len(held) != len(page) {
			return false
		}
		for key, value := range page {
			if inner, found := held[key]; !found || !shows(value, inner) {
				return false
			}
		}
		return true
	case []any:
		held, ok := state.([]any)
		if !ok || len(held) != len(page) {
			return false
		}
		for i, value := range page {
			if !shows(value, held[i]) {
				return false
			}
		}
		return true
	case string:
		if text, ok := state.(string); ok && text != "" && stoodFor.MatchString(page) {
			return true
		}
	}
	return reflect.DeepEqual(page, state)
}

// TestEveryDocumentedOutputIsWhatTheCommandPrints holds each block of the
// documentation that shows a part of a comment, or of the machine block a
// check run carries, to what the report renders for a golden state: the lines
// stand in that state's comment as the page shows them, and the JSON is the
// state's own, value for value. So a change to what the report writes cannot
// leave a page's example behind.
func TestEveryDocumentedOutputIsWhatTheCommandPrints(t *testing.T) {
	states := goldenStates(t)
	for _, shown := range shownParts {
		blocks := docstest.At(t, "../..", shown.at)
		if len(blocks) != len(shown.states) {
			t.Errorf("%s, %q: the page holds %d %s blocks, and this test %d states for them", shown.at.Page, shown.at.Heading, len(blocks), shown.at.Language, len(shown.states))
			continue
		}
		for i, name := range shown.states {
			state, known := states[name]
			if !known {
				t.Fatalf("%s is no golden state", name)
			}
			body, err := CommentWithLifetime(state, state.Lifetime)
			if err != nil {
				t.Fatal(err)
			}
			if shown.part == nil {
				if !strings.Contains(body, "\n"+blocks[i]+"\n") {
					t.Errorf("%s, %q: the comment of the state %s does not hold the lines the page shows:\n%s", shown.at.Page, shown.at.Heading, name, blocks[i])
				}
				continue
			}
			var block map[string]any
			fenced := strings.TrimSuffix(strings.TrimPrefix(fencedBlock.FindString(body), "```json\n"), "\n```")
			if err := json.Unmarshal([]byte(fenced), &block); err != nil {
				t.Fatalf("the state %s renders no machine block: %v", name, err)
			}
			text := blocks[i]
			if !strings.HasPrefix(text, "{") {
				text = "{" + text + "}"
			}
			var page any
			if err := json.Unmarshal([]byte(text), &page); err != nil {
				t.Errorf("%s, %q: the block is not JSON: %v", shown.at.Page, shown.at.Heading, err)
				continue
			}
			if held := shown.part(block); held == nil || !shows(page, held) {
				printed, _ := json.MarshalIndent(held, "", "  ")
				t.Errorf("%s, %q: the page shows\n%s\nand the state %s holds\n%s", shown.at.Page, shown.at.Heading, blocks[i], name, printed)
			}
		}
	}
}
