package config

import (
	"fmt"
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
		{"policy:\n  run_budget_usd: 5.0\n", false},
		{"policy:\n  pr_budget_usd: 1e23\n", false},
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

// TestValidateRefusesFloatAmount holds validate to the policy's reading of a
// YAML float: 5.0 and 1e3 are whole in JSON, so the schema alone takes them,
// and the policy refuses them. Validate names each in the policy's sentence,
// in place of what the schema says, for every amount key the policy reads.
func TestValidateRefusesFloatAmount(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"run_budget_usd", "5.0"},
		{"max_run_cost_usd", "0.5"},
		{"pr_budget_usd", "1e3"},
		{"monthly_budget_usd", "900.00"},
		{"max_increase_usd", "1."},
		{"max_increase_percent", "10.0"},
	} {
		body := "policy:\n  " + tc.key + ": " + tc.value + "\n"
		problems, err := Validate([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		_, refused := policy.ParseSettings([]byte(body))
		if refused == nil {
			t.Fatalf("%q: the policy reads it", body)
		}
		want := Problem{Path: "/policy/" + tc.key, Message: strings.TrimPrefix(refused.Error(), "policy: ")}
		if len(problems) != 1 || problems[0] != want {
			t.Errorf("%q: %v, want %v", body, problems, want)
		}
	}
	amounts, err := amountKeys()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range policy.Keys() {
		if _, refused := policy.ParseSettings([]byte("policy:\n  " + key + ": 5.0\n")); amounts[key] != (refused != nil && strings.Contains(refused.Error(), "must be a whole number")) {
			t.Errorf("%s: the schema states it as an amount %t, the policy refuses 5.0 as one: %v", key, amounts[key], refused)
		}
	}
	problems, err := Validate([]byte("policy:\n  run_budget_usd: 5.0\n  max_run_cost_usd: 1e3\n  pr_budget_usd: 40\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 2 || !strings.HasPrefix(problems[0].Message, "policy.max_run_cost_usd must be a whole number") || !strings.HasPrefix(problems[1].Message, "policy.run_budget_usd must be a whole number") {
		t.Errorf("two floats beside an integer: %v", problems)
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

// TestValidateRefusesFloatDays holds validate to the approval rule's reading
// of a YAML float: 7.0 is whole in JSON, so the schema alone takes it, and the
// approval rule refuses it. Validate names it in the approval rule's sentence.
func TestValidateRefusesFloatDays(t *testing.T) {
	for _, value := range []string{"7.0", "7.", "1e1", "7.5", "90.0"} {
		body := "approval:\n  expires_days: " + value + "\n"
		problems, err := Validate([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		_, refused := policy.ParseApproval([]byte(body))
		if refused == nil {
			t.Fatalf("%q: the approval rule reads it", body)
		}
		want := Problem{Path: "/approval/expires_days", Message: strings.TrimPrefix(refused.Error(), "policy: ")}
		if len(problems) != 1 || problems[0] != want {
			t.Errorf("%q: %v, want %v", body, problems, want)
		}
	}
	for _, body := range []string{"approval:\n  expires_days: 7\n", "approval:\n  expires_days: 90\n"} {
		if problems, err := Validate([]byte(body)); err != nil || len(problems) != 0 {
			t.Errorf("%q: %v, %v", body, problems, err)
		}
	}
}

// readers are the four that read costroid.yml, each saying whether it read
// a file as its defaults.
var readers = map[string]func(data []byte) (bool, error){
	"validate": func(data []byte) (bool, error) {
		problems, err := Validate(data)
		if err != nil {
			return false, err
		}
		if len(problems) != 0 {
			return false, fmt.Errorf("%v", problems)
		}
		return true, nil
	},
	"comment": func(data []byte) (bool, error) {
		config, err := comment.ParseConfig(data)
		return config == comment.Config{ShowCost: true}, err
	},
	"policy": func(data []byte) (bool, error) {
		settings, err := policy.ParseSettings(data)
		return len(settings.Amounts) == 0 && len(settings.Models) == 0 && !settings.Enforce, err
	},
	"approval": func(data []byte) (bool, error) {
		settings, err := policy.ParseApproval(data)
		return settings == policy.ApprovalSettings{ExpiresDays: policy.DefaultApprovalDays}, err
	},
}

// assertDefaults holds every reader to reading body as its defaults.
func assertDefaults(t *testing.T, body string) {
	t.Helper()
	for name, read := range readers {
		defaults, err := read([]byte(body))
		if err != nil || !defaults {
			t.Errorf("%s reads %q as defaults %t: %v", name, body, defaults, err)
		}
	}
}

func TestEmptyDocumentReadsAsDefaults(t *testing.T) {
	for _, body := range []string{"", "\n", "# nothing\n", "~", "~\n", "null\n", "Null\n", "NULL\n", "---", "---\n", "--- ~\n", "--- null\n", "---\n...\n", "---\n# nothing\n"} {
		assertDefaults(t, body)
	}
}

func TestNullSectionReadsAsDefaults(t *testing.T) {
	for _, section := range []string{"comment", "policy", "approval"} {
		for _, value := range []string{"", " ~", " null", " Null", " NULL", " !!null"} {
			assertDefaults(t, section+":"+value+"\n")
		}
		for _, value := range []string{` ""`, " []", " 0", " false"} {
			body := section + ":" + value + "\n"
			for _, name := range []string{"validate", section} {
				if _, err := readers[name]([]byte(body)); err == nil {
					t.Errorf("%s reads %q, a section that has a value and is no mapping", name, body)
				}
			}
		}
	}
	assertDefaults(t, "comment:\npolicy:\napproval:\n")
	assertDefaults(t, "policy: &none\napproval: *none\ncomment: *none\n")
	if problems, err := Validate([]byte("agents:\n")); err != nil || len(problems) == 0 {
		t.Errorf("validate reads agents with no value, a key that is a list and no section: %v, %v", problems, err)
	}
}
