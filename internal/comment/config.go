package comment

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"

	"go.yaml.in/yaml/v3"

	"github.com/Costroid/costroid/internal/policy"
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
// The policy's own parser names its keys, so the two never drift apart.
var knownKeys = map[string]map[string]bool{
	"":        {"comment": true, "policy": true},
	"comment": {"show_cost": true},
	"policy":  keySet(policy.Keys()),
}

func keySet(keys []string) map[string]bool {
	set := make(map[string]bool, len(keys))
	for _, key := range keys {
		set[key] = true
	}
	return set
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
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return Config{}, fmt.Errorf("comment: parse costroid.yml: %w", err)
	}
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 && root.Content[0].Kind != yaml.MappingNode {
		return Config{}, errors.New("comment: costroid.yml is not a mapping")
	}
	for _, warning := range unreadKeys(&root) {
		fmt.Fprintf(configWarnings, "costroid: warning: costroid.yml: %s\n", warning)
	}
	showCost, err := readShowCost(&root)
	if err != nil {
		return Config{}, err
	}
	return Config{ShowCost: showCost}, nil
}

// readShowCost reads comment.show_cost. A YAML 1.1 word such as yes or no
// keeps its meaning and warns, because YAML 1.2 reads it as a string. Any
// other value is an error, and the report withholds amounts on a repository
// not known to be private. The error never repeats the value, which is the
// repository's own text.
func readShowCost(root *yaml.Node) (bool, error) {
	section := child(root, "comment")
	if section == nil || section.Tag == "!!null" {
		return true, nil
	}
	if section.Kind != yaml.MappingNode {
		return false, errors.New("comment: comment in costroid.yml is not a mapping")
	}
	value := child(section, "show_cost")
	if value == nil {
		return true, nil
	}
	if value.Kind == yaml.ScalarNode && value.Tag == "!!bool" {
		return value.Value == "true", nil
	}
	var show bool
	if value.Kind == yaml.ScalarNode && value.Decode(&show) == nil {
		fmt.Fprintf(configWarnings, "costroid: warning: costroid.yml: comment.show_cost is a YAML 1.1 word and reads as %t; write true or false\n", show)
		return show, nil
	}
	return false, errors.New("comment: comment.show_cost in costroid.yml must be true or false")
}

// child is the value of a mapping's key, nil when the node is not a mapping
// or lacks the key. A document is read as the mapping it holds.
func child(node *yaml.Node, key string) *yaml.Node {
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = node.Content[0]
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
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
