package comment

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	costroid "github.com/Costroid/costroid"
)

func parseWarnings(t *testing.T, document string) (Config, string) {
	t.Helper()
	var output bytes.Buffer
	previous := configWarnings
	configWarnings = &output
	t.Cleanup(func() { configWarnings = previous })
	config, err := ParseConfig([]byte(document))
	if err != nil {
		t.Fatalf("%q: %v", document, err)
	}
	return config, output.String()
}

func TestConfigWarnsOnAReservedKeyAndStillReadsTheFile(t *testing.T) {
	config, output := parseWarnings(t, "catalog: 0.1.0\nbudget_owner: finance\ncomment:\n  show_cost: false\n")
	want := "costroid: warning: costroid.yml: catalog is reserved and not read yet, so it has no effect\n" +
		"costroid: warning: costroid.yml: budget_owner is reserved and not read yet, so it has no effect\n"
	if output != want {
		t.Fatalf("output:\n%s\nwant:\n%s", output, want)
	}
	if config.ShowCost {
		t.Fatal("a reserved key stopped show_cost from being read")
	}
}

func TestConfigNamesAnUnknownKeyAtAnyDepth(t *testing.T) {
	config, output := parseWarnings(t, "colour: blue\ncomment:\n  show_costs: false\n")
	want := "costroid: warning: costroid.yml: colour is not a key Costroid reads, so it has no effect\n" +
		"costroid: warning: costroid.yml: comment.show_costs is not a key Costroid reads, so it has no effect\n"
	if output != want {
		t.Fatalf("output:\n%s\nwant:\n%s", output, want)
	}
	if !config.ShowCost {
		t.Fatal("a misspelled key changed the setting")
	}
}

func TestConfigNeverPrintsAKeyThatCouldCarryAWorkflowCommand(t *testing.T) {
	_, output := parseWarnings(t, "\"x\\n::warning::forged\": 1\n")
	if strings.Contains(output, "::warning::") || strings.Count(output, "\n") != 1 {
		t.Fatalf("output:\n%s", output)
	}
	if !strings.Contains(output, "a key with characters outside") {
		t.Fatalf("output:\n%s", output)
	}
}

func TestConfigWithOnlyKnownKeysPrintsNothing(t *testing.T) {
	for _, document := range []string{"", "comment:\n  show_cost: true\n", "comment: {}\n", "# nothing\n"} {
		if _, output := parseWarnings(t, document); output != "" {
			t.Errorf("%q printed:\n%s", document, output)
		}
	}
}

// TestConfigKeysMatchTheSchema holds the parser to the published schema: every
// property it names is read or reserved, and the schema calls a key reserved
// exactly when the parser does.
func TestConfigKeysMatchTheSchema(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(costroid.ConfigSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Properties) != len(reservedKeys)+len(knownKeys[""]) {
		t.Fatalf("the schema names %d keys, the parser %d", len(schema.Properties), len(reservedKeys)+len(knownKeys[""]))
	}
	for key, property := range schema.Properties {
		reserved := strings.Contains(property.Description, "Reserved:")
		if reserved != reservedKeys[key] || (!reserved && !knownKeys[""][key]) {
			t.Errorf("%s: the schema says reserved %v, the parser %v", key, reserved, reservedKeys[key])
		}
	}
}
