package config

import (
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/comment"
)

func TestEveryKeyOfAFullFileIsValid(t *testing.T) {
	problems, err := Validate([]byte(`
comment:
  show_cost: false
policy_pack: costroid/default
budget_owner: platform-team
assumptions:
  calls_per_day: 100
catalog: 0.1.0
agents: [claude-code, codex]
`))
	if err != nil || len(problems) != 0 {
		t.Fatalf("%v, %v", problems, err)
	}
}

func TestAnEmptyFileIsValid(t *testing.T) {
	for _, body := range []string{"", "# nothing\n", "---\n"} {
		if problems, err := Validate([]byte(body)); err != nil || len(problems) != 0 {
			t.Fatalf("%q: %v, %v", body, problems, err)
		}
	}
}

func TestABrokenFileNamesThePlace(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"typo in a key", "commnet:\n  show_cost: false\n", "commnet"},
		{"typo in a nested key", "comment:\n  showcost: false\n", "showcost"},
		{"string for a boolean", "comment:\n  show_cost: \"no\"\n", "/comment/show_cost"},
		{"a pin that is no version", "catalog: latest\n", "/catalog"},
		{"an agent that is no name", "agents: [Claude Code]\n", "/agents/0"},
		{"a repeated agent", "agents: [codex, codex]\n", "/agents"},
		{"not a mapping", "- one\n- two\n", "array"},
		{"not YAML", "comment: [\n", "not YAML"},
		{"a key that is no string", "1: 2\n", "1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems, err := Validate([]byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if len(problems) == 0 {
				t.Fatal("accepted")
			}
			if text := problems[0].String(); !strings.Contains(text, tc.want) {
				t.Fatalf("%s", text)
			}
		})
	}
}

// TestSchemaAndTheCommentAgreeOnShowCost holds the one key both read: a value
// the schema accepts must parse, and one it refuses must not reach the comment
// as a setting.
func TestSchemaAndTheCommentAgreeOnShowCost(t *testing.T) {
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{"comment:\n  show_cost: true\n", true},
		{"comment:\n  show_cost: false\n", true},
		{"comment:\n  show_cost: maybe\n", false},
		{"comment:\n  show_cost: 1\n", false},
	} {
		problems, err := Validate([]byte(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		_, parseErr := comment.ParseConfig([]byte(tc.body))
		if (len(problems) == 0) != tc.valid || (parseErr == nil) != tc.valid {
			t.Errorf("%q: schema %v, parser %v, want valid %v", tc.body, problems, parseErr, tc.valid)
		}
	}
}
