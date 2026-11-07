package config

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/policy"
)

func TestEveryKeyOfAFullFileIsValid(t *testing.T) {
	problems, err := Validate([]byte(`
comment:
  show_cost: false
policy:
  run_budget_usd: "5.00"
  max_run_cost_usd: "0.75"
  enforce: true
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

// TestSchemaAndThePolicyAgree holds the policy keys to one meaning: a value the
// schema accepts must reach the policy, and one it refuses must not.
func TestSchemaAndThePolicyAgree(t *testing.T) {
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{"policy:\n  run_budget_usd: \"5\"\n", true},
		{"policy:\n  run_budget_usd: \"0.000000000001\"\n", true},
		{"policy:\n  max_run_cost_usd: \"12.50\"\n  enforce: false\n", true},
		{"policy:\n  enforce: true\n", true},
		{"policy: {}\n", true},
		{"policy:\n  run_budget_usd: 5\n", true},
		{"policy:\n  pr_budget_usd: 40\n  monthly_budget_usd: \"900.50\"\n", true},
		{"policy:\n  max_increase_usd: \"0.5\"\n  max_increase_percent: 25\n", true},
		{"policy:\n  models: [claude-sonnet-5-5, \"gpt-5*\"]\n", true},
		{"policy:\n  run_budget_usd: -1\n", false},
		{"policy:\n  run_budget_usd: 0.5\n", false},
		{"policy:\n  models: []\n", false},
		{"policy:\n  models: [codex, codex]\n", false},
		{"policy:\n  models: [\"*\"]\n", false},
		{"policy:\n  models: claude-sonnet-5-5\n", false},
		{"policy:\n  run_budget_usd: \"0.5 USD\"\n", false},
		{"policy:\n  run_budget_usd: \"0.1234567890123\"\n", false},
		{"policy:\n  run_budget_usd: \"05\"\n", false},
		{"policy:\n  enforce: \"true\"\n", false},
		{"policy:\n  budget: \"5\"\n", false},
		{"policy: 5\n", false},
	} {
		problems, err := Validate([]byte(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		_, parseErr := policy.ParseSettings([]byte(tc.body))
		if (len(problems) == 0) != tc.valid || (parseErr == nil) != tc.valid {
			t.Errorf("%q: schema %v, policy %v, want valid %v", tc.body, problems, parseErr, tc.valid)
		}
	}
}

// TestEveryPolicyExampleInTheDocsIsValid holds each YAML block of the pages
// that teach the policy keys to the schema and to the policy's own reader.
func TestEveryPolicyExampleInTheDocsIsValid(t *testing.T) {
	block := regexp.MustCompile("(?s)```yaml\n(.*?)```")
	examples := 0
	for _, page := range []string{"../../docs/configuration.md", "../../docs/policy.md"} {
		data, err := os.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range block.FindAllSubmatch(data, -1) {
			examples++
			problems, err := Validate(match[1])
			if err != nil || len(problems) != 0 {
				t.Errorf("%s: %q: %v, %v", page, match[1], problems, err)
			}
			if _, err := policy.ParseSettings(match[1]); err != nil {
				t.Errorf("%s: %q: %v", page, match[1], err)
			}
		}
	}
	if examples < 3 {
		t.Fatalf("found %d examples", examples)
	}
}
