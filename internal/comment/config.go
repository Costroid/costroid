package comment

import (
	"fmt"
	"io"
	"os"
	"regexp"

	"go.yaml.in/yaml/v3"
)

// Config is what the comment reads from costroid.yml.
type Config struct {
	ShowCost bool
}

// reservedKeys are named by the schema and read by nothing yet.
var reservedKeys = map[string]bool{
	"policy_pack":  true,
	"budget_owner": true,
	"assumptions":  true,
	"catalog":      true,
	"agents":       true,
}

// knownKeys are the keys this release reads, by the mapping that holds them.
var knownKeys = map[string]map[string]bool{
	"":        {"comment": true, "policy": true},
	"comment": {"show_cost": true},
	"policy":  {"run_budget_usd": true, "max_run_cost_usd": true, "enforce": true},
}

// printableKey is what a key may hold to be named in the job log. A key is the
// repository's own text, and a log line must never carry a workflow command.
var printableKey = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// configWarnings is where ParseConfig names the keys it does not read. The
// report job's log is where a reader of costroid.yml looks.
var configWarnings io.Writer = os.Stderr

// ParseConfig reads costroid.yml. An empty document or an absent key leaves
// the default. A key the comment does not read is named on stderr and never
// fails the read.
func ParseConfig(data []byte) (Config, error) {
	var file struct {
		Comment struct {
			ShowCost *bool `yaml:"show_cost"`
		} `yaml:"comment"`
	}
	if err := yaml.Unmarshal(data, &file); err != nil {
		return Config{}, fmt.Errorf("comment: parse costroid.yml: %w", err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return Config{}, fmt.Errorf("comment: parse costroid.yml: %w", err)
	}
	for _, warning := range unreadKeys(&root) {
		fmt.Fprintf(configWarnings, "costroid: warning: costroid.yml: %s\n", warning)
	}
	if file.Comment.ShowCost == nil {
		return Config{ShowCost: true}, nil
	}
	return Config{ShowCost: *file.Comment.ShowCost}, nil
}

// unreadKeys says, for each key this release does not read, why it has no
// effect, in the file's order.
func unreadKeys(root *yaml.Node) []string {
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil
	}
	var warnings []string
	var walk func(node *yaml.Node, parent string)
	walk = func(node *yaml.Node, parent string) {
		if node.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i].Value, node.Content[i+1]
			path := key
			if parent != "" {
				path = parent + "." + key
			}
			switch {
			case parent == "" && reservedKeys[key]:
				warnings = append(warnings, key+" is reserved and not read yet, so it has no effect")
			case !knownKeys[parent][key]:
				warnings = append(warnings, unknownKey(path))
			case knownKeys[path] != nil:
				walk(value, path)
			}
		}
	}
	walk(root.Content[0], "")
	return warnings
}

func unknownKey(path string) string {
	if !printableKey.MatchString(path) {
		return "a key with characters outside A-Z, a-z, 0-9, _, . and - is not one Costroid reads, so it has no effect"
	}
	return path + " is not a key Costroid reads, so it has no effect"
}
