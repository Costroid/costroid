package config

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"

	costroid "github.com/Costroid/costroid"
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
		{"policy:\n  run_budget_usd: 0.5\n", true},
		{"policy:\n  run_budget_usd: 5.0\n", true},
		{"policy:\n  run_budget_usd: 017\n", false},
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
		{"budget_owner: &amount \"5\"\ncomment:\n  show_cost: &on true\npolicy:\n  run_budget_usd: *amount\n  enforce: *on\n", true},
		{"budget_owner: &amount \"0x10\"\npolicy:\n  run_budget_usd: *amount\n", false},
		{"policy_pack: &key run_budget_usd\npolicy:\n  *key : 1e3\n", false},
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

// TestValidateRefusesANonDecimalAmount holds validate to the policy's reading
// of an amount's text: 1e3, 0x10, 017 and 1_000 are whole in JSON, so the
// schema alone takes them, and the policy refuses them. Validate names each in
// the policy's sentence, in place of what the schema says, for every amount
// key the policy reads.
func TestValidateRefusesANonDecimalAmount(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"run_budget_usd", "1e3"},
		{"max_run_cost_usd", "0x10"},
		{"pr_budget_usd", "017"},
		{"monthly_budget_usd", "1_000"},
		{"max_increase_usd", "1."},
		{"max_increase_percent", "+10"},
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
		_, isAmount := amounts[key]
		if _, refused := policy.ParseSettings([]byte("policy:\n  " + key + ": 1e3\n")); isAmount != (refused != nil && strings.Contains(refused.Error(), "must be a plain decimal")) {
			t.Errorf("%s: the schema states it as an amount %t, the policy refuses 1e3 as one: %v", key, isAmount, refused)
		}
	}
	problems, err := Validate([]byte("policy:\n  run_budget_usd: 017\n  max_run_cost_usd: 1e3\n  pr_budget_usd: 2.50\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 2 || !strings.HasPrefix(problems[0].Message, "policy.max_run_cost_usd must be a plain decimal") || !strings.HasPrefix(problems[1].Message, "policy.run_budget_usd must be a plain decimal") {
		t.Errorf("two refused forms beside a decimal: %v", problems)
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

// integerSettings are the settings the published schema states as an
// integer, as section.key.
func integerSettings(t *testing.T) []string {
	t.Helper()
	var document struct {
		Properties map[string]struct {
			Properties map[string]struct {
				Type any `json:"type"`
			} `json:"properties"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(costroid.ConfigSchema, &document); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for section, properties := range document.Properties {
		for key, property := range properties.Properties {
			if property.Type == "integer" {
				keys = append(keys, section+"."+key)
			}
		}
	}
	slices.Sort(keys)
	return keys
}

// TestIntegerSettingsRefuseNonDecimalForms holds every integer setting to
// plain digits in the reader, in validate and in the schema. YAML reads 0x7,
// 0o7, 017, 1_0 and +7 as whole numbers, and 017 would be 15 days: the reader
// and validate refuse each in one sentence that names the key. JSON has none
// of those forms, and the schema refuses a sign and a string; it reads 7.0 by
// its value alone, and validate refuses it by its text.
func TestIntegerSettingsRefuseNonDecimalForms(t *testing.T) {
	if keys := integerSettings(t); !slices.Equal(keys, []string{"approval.expires_days"}) {
		t.Fatalf("the schema states the integer settings %v: give each a reader that takes plain digits alone, and hold it here", keys)
	}
	schema, err := compile()
	if err != nil {
		t.Fatal(err)
	}
	const sentence = "approval.expires_days must be a whole number of days from 1 to 90, written in plain digits such as 7"
	for _, tc := range []struct {
		literal   string
		days      int
		valueOnly bool
	}{
		{"7", 7, false}, {"1", 1, false}, {"90", 90, false}, {"10", 10, false},
		{"0x7", 0, false}, {"0X7", 0, false}, {"0o7", 0, false}, {"017", 0, false}, {"07", 0, false},
		{"0010", 0, false}, {"1_0", 0, false}, {"+7", 0, false}, {"-7", 0, false}, {"+0x7", 0, false},
		{"0b111", 0, false}, {`"7"`, 0, false}, {`"017"`, 0, false}, {"7.0", 0, true}, {"1e1", 0, true},
	} {
		body := "approval:\n  expires_days: " + tc.literal + "\n"
		settings, readErr := policy.ParseApproval([]byte(body))
		problems, err := Validate([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		read := tc.days != 0
		switch {
		case read && (readErr != nil || settings.ExpiresDays != tc.days || len(problems) != 0):
			t.Errorf("%s: the reader %d, %v, validate %v, want %d days", tc.literal, settings.ExpiresDays, readErr, problems, tc.days)
		case !read && (readErr == nil || readErr.Error() != "policy: "+sentence):
			t.Errorf("%s: the reader read %d days, %v", tc.literal, settings.ExpiresDays, readErr)
		case !read && (len(problems) != 1 || problems[0] != Problem{Path: "/approval/expires_days", Message: sentence}):
			t.Errorf("%s: validate says %v", tc.literal, problems)
		}
		schemaTakes := false
		if instance, err := jsonschema.UnmarshalJSON(strings.NewReader(`{"approval": {"expires_days": ` + tc.literal + `}}`)); err == nil {
			schemaTakes = schema.Validate(instance) == nil
		}
		if want := read || tc.valueOnly; schemaTakes != want {
			t.Errorf("%s: the schema takes it %t, want %t", tc.literal, schemaTakes, want)
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

// TestAmountFormsAgreeAcrossReaderValidatorAndSchema holds the policy's
// reader, costroid validate and the published schema to one table of amount
// literals. The reader and validate agree on every one. The schema reads the
// literal as a JSON document would carry it: JSON itself has no hex, octal,
// leading zero, underscore, leading +, infinity or trailing point, and the
// schema refuses a sign and a string that is not a plain decimal. A JSON
// number keeps only its value, so the schema takes 1e3 as 1000, -0 as 0 and
// thirteen fractional digits as a number; validate refuses those by their
// text, and the schema never refuses an amount the reader takes.
func TestAmountFormsAgreeAcrossReaderValidatorAndSchema(t *testing.T) {
	long := "1" + strings.Repeat("0", 64)
	cases := []struct {
		literal   string
		read      bool
		valueOnly bool
	}{
		{"5", true, false}, {"0", true, false}, {"0.5", true, false}, {"2.50", true, false}, {"5.0", true, false},
		{"0.000000000001", true, false}, {`"2.50"`, true, false}, {`"0"`, true, false}, {long[:64], true, false},
		{"0x10", false, false}, {"0o17", false, false}, {"017", false, false}, {"00", false, false},
		{"1_000", false, false}, {"+5", false, false}, {"-5", false, false}, {".inf", false, false},
		{".nan", false, false}, {"1.", false, false}, {".5", false, false}, {`"017"`, false, false},
		{`"1e3"`, false, false}, {`"0x10"`, false, false}, {`"-1"`, false, false}, {`"` + long + `"`, false, false},
		{"1e3", false, true}, {"1E3", false, true}, {"1e-3", false, true}, {"-0", false, true},
		{"0.1234567890123", false, true}, {long, false, true},
	}
	schema, err := compile()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		body := "policy:\n  run_budget_usd: " + tc.literal + "\n"
		_, readErr := policy.ParseSettings([]byte(body))
		problems, err := Validate([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if (readErr == nil) != tc.read || (len(problems) == 0) != tc.read {
			t.Errorf("%s: the reader %v, validate %v, want read %t", tc.literal, readErr, problems, tc.read)
		}
		if !tc.read && (len(problems) != 1 || problems[0].Message != strings.TrimPrefix(readErr.Error(), "policy: ")) {
			t.Errorf("%s: validate says %v, the reader %v", tc.literal, problems, readErr)
		}
		schemaTakes := false
		if instance, err := jsonschema.UnmarshalJSON(strings.NewReader(`{"policy": {"run_budget_usd": ` + tc.literal + `}}`)); err == nil {
			schemaTakes = schema.Validate(instance) == nil
		}
		if want := tc.read || tc.valueOnly; schemaTakes != want {
			t.Errorf("%s: the schema takes it %t, want %t", tc.literal, schemaTakes, want)
		}
	}
}

// TestModelsListAgreesAcrossReaderValidatorAndSchema holds the policy's
// reader, costroid validate and the published schema to one table of
// policy.models lists. The reader and validate agree on every one, and
// validate names a refused list in the reader's sentence, which the schema's
// description of the key states. The schema reads the list YAML decodes, so
// it cannot tell a date, a !!binary value or a scalar under the file's own
// tag from a string: validate refuses those by their YAML node, and the
// schema never refuses a list the reader takes. An alias reads as the node it
// names on every side.
func TestModelsListAgreesAcrossReaderValidatorAndSchema(t *testing.T) {
	long := strings.Repeat("a", 128)
	cases := []struct {
		before, models string
		read           bool
		nodeOnly       bool
	}{
		{"", "[claude-sonnet-5-5]", true, false},
		{"", `[claude-sonnet-5-5, "gpt-5*"]`, true, false},
		{"", "\n    - codex\n    - gpt-5.5", true, false},
		{"", `["claude-*", anthropic/claude-opus-5-5, "openai:gpt-5.5", a+b, a_b]`, true, false},
		{"", `["2026-11-14", !!str 2026-11-14x, ! codex]`, true, false},
		{"", "[" + long + "]", true, false},
		{"budget_owner: &id codex\n", "[*id, gpt-5.5]", true, false},
		{"agents: &list [codex]\n", "*list", true, false},
		{"", "[]", false, false},
		{"", "", false, false},
		{"", "~", false, false},
		{"", "codex", false, false},
		{"", "{codex: true}", false, false},
		{"", "[codex, codex]", false, false},
		{"", "[&id codex, *id]", false, false},
		{"", `["*"]`, false, false},
		{"", `["a**"]`, false, false},
		{"", `["-codex"]`, false, false},
		{"", `["gpt 5"]`, false, false},
		{"", `["codex\n"]`, false, false},
		{"", "[" + long + "a]", false, false},
		{"", "[5]", false, false},
		{"", "[0x10]", false, false},
		{"", "[true]", false, false},
		{"", "[null]", false, false},
		{"", "[[codex]]", false, false},
		{"", "[{codex: 1}]", false, false},
		{"", "[!0 0000]", false, true},
		{"", "[!x codex]", false, true},
		{"", "[2026-11-14]", false, true},
		{"", "[!!timestamp 2026-11-14]", false, true},
		{"", "[!!binary Y29kZXg=]", false, true},
	}
	schema, err := compile()
	if err != nil {
		t.Fatal(err)
	}
	var stated struct {
		Properties struct {
			Policy struct {
				Properties struct {
					Models struct {
						Description string `json:"description"`
					} `json:"models"`
				} `json:"properties"`
			} `json:"policy"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(costroid.ConfigSchema, &stated); err != nil {
		t.Fatal(err)
	}
	sentence := fmt.Sprintf(modelsSentence, len(long))
	if description := stated.Properties.Policy.Properties.Models.Description; !strings.Contains(description, " "+sentence+". ") {
		t.Errorf("the schema describes policy.models as %q, which does not state %q", description, sentence)
	}
	for _, tc := range cases {
		body := tc.before + "policy:\n  models: " + tc.models + "\n"
		settings, readErr := policy.ParseSettings([]byte(body))
		problems, err := Validate([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if (readErr == nil) != tc.read || (len(problems) == 0) != tc.read {
			t.Errorf("%q: the reader %v, validate %v, want read %t", body, readErr, problems, tc.read)
			continue
		}
		if tc.read && len(settings.Models) == 0 {
			t.Errorf("%q: the reader took no model", body)
		}
		if !tc.read {
			want := Problem{Path: "/policy/models", Message: sentence}
			if readErr.Error() != "policy: "+sentence || len(problems) != 1 || problems[0] != want {
				t.Errorf("%q: validate says %v, the reader %v, want %q", body, problems, readErr, sentence)
			}
		}
		var document struct {
			Policy struct {
				Models any `yaml:"models"`
			} `yaml:"policy"`
		}
		if err := yaml.Unmarshal([]byte(body), &document); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(map[string]any{"policy": map[string]any{"models": document.Policy.Models}})
		if err != nil {
			t.Fatal(err)
		}
		instance, err := jsonschema.UnmarshalJSON(strings.NewReader(string(encoded)))
		if err != nil {
			t.Fatal(err)
		}
		if takes, want := schema.Validate(instance) == nil, tc.read || tc.nodeOnly; takes != want {
			t.Errorf("%q: the schema takes %s %t, want %t", body, encoded, takes, want)
		}
	}
}

// TestValidateReadsAMergeKeyAsAKey holds validate to the way the policy, the
// approval rule and the comment read a YAML merge key: as a key named <<,
// never as the mapping it names. The schema refuses it, and the policy and
// the approval rule refuse it under their sections.
func TestValidateReadsAMergeKeyAsAKey(t *testing.T) {
	for _, tc := range []struct{ body, path string }{
		{"assumptions: &shared {enforce: true}\npolicy:\n  <<: *shared\n", "/policy"},
		{"assumptions: &shared {expires_days: 7}\napproval:\n  <<: *shared\n", "/approval"},
		{"assumptions: &shared {show_cost: false}\ncomment:\n  <<: *shared\n", "/comment"},
		{"<<: {policy: {enforce: true}}\n", ""},
		{"policy:\n  !!merge <<: {enforce: true}\n", "/policy"},
		{"policy:\n  !<tag:yaml.org,2002:merge> <<: {enforce: true}\n", "/policy"},
		{"policy:\n  ! <<: {enforce: true}\n", "/policy"},
		{"budget_owner: &key <<\npolicy:\n  *key : {enforce: true}\n", "/policy"},
	} {
		problems, err := Validate([]byte(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		if len(problems) == 0 || !slices.ContainsFunc(problems, func(problem Problem) bool { return problem.Path == tc.path }) {
			t.Errorf("%q: validate says %v, want a problem at %q", tc.body, problems, tc.path)
		}
		_, settingsErr := policy.ParseSettings([]byte(tc.body))
		_, approvalErr := policy.ParseApproval([]byte(tc.body))
		if (tc.path == "/policy") != (settingsErr != nil) || (tc.path == "/approval") != (approvalErr != nil) {
			t.Errorf("%q: the policy %v, the approval rule %v", tc.body, settingsErr, approvalErr)
		}
	}
}
