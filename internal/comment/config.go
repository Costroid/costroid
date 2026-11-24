package comment

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Costroid/costroid/internal/policy"
)

// Config is what the comment reads from costroid.yml.
type Config struct {
	ShowCost bool
	// ReceiptsOff is true when comment.receipts turns receipts off, or is
	// not a switch, so a report reads none whatever the workflow asks.
	ReceiptsOff bool
}

// reservedKeys are named by the schema and read by nothing yet.
var reservedKeys = map[string]bool{
	"budget_owner": true,
	"assumptions":  true,
	"catalog":      true,
	"agents":       true,
}

// knownKeys are the keys this release reads, by the mapping that holds them.
// The policy's own parser names its keys, so the two never drift apart.
var knownKeys = map[string]map[string]bool{
	"":         {"comment": true, "policy": true, "approval": true, policy.PackKey: true},
	"comment":  {"show_cost": true, "receipts": true},
	"policy":   keySet(policy.Keys()),
	"approval": keySet(policy.ApprovalKeys()),
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

// ParseConfig reads costroid.yml. A file that is empty, ~, null or only ---,
// a section with no value, or an absent key leaves the default. A key the
// comment does not read never fails the read: it is a warning. The warnings
// come back in the file's order, with or without an error, each the line a
// job's log shows for it, without its line feed. The caller prints or logs
// them: the report job's log is where a reader of costroid.yml looks, and a
// service's log has a form of its own.
func ParseConfig(data []byte) (Config, []string, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return Config{}, nil, fmt.Errorf("comment: parse costroid.yml: %w", err)
	}
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 && root.Content[0].Kind != yaml.MappingNode && root.Content[0].Tag != "!!null" {
		return Config{}, nil, errors.New("comment: costroid.yml is not a mapping")
	}
	var warnings []string
	for _, unread := range unreadKeys(&root) {
		warnings = append(warnings, fmt.Sprintf("costroid: warning: costroid.yml: %s", unread))
	}
	showCost, word, err := readSwitch(&root, "show_cost")
	warnings = append(warnings, word...)
	if err != nil {
		return Config{}, warnings, err
	}
	// A receipts value that is not a switch turns receipts off, and leaves
	// show_cost as the file sets it.
	receipts, word, err := readSwitch(&root, "receipts")
	warnings = append(warnings, word...)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("costroid: warning: no receipt is read: %s", strings.TrimPrefix(err.Error(), "comment: ")))
	}
	return Config{ShowCost: showCost, ReceiptsOff: !receipts || err != nil}, warnings, nil
}

// readSwitch reads comment.show_cost or comment.receipts, each true when
// absent. A YAML 1.1 word such as yes or no keeps its meaning and comes back
// with a warning, because YAML 1.2 reads it as a string. Any other value is
// an error. The error never repeats the value, which is the repository's own
// text.
func readSwitch(root *yaml.Node, key string) (on bool, warnings []string, err error) {
	section := child(root, "comment")
	if section == nil || section.Tag == "!!null" {
		return true, nil, nil
	}
	if section.Kind != yaml.MappingNode {
		return false, nil, errors.New("comment: comment in costroid.yml is not a mapping")
	}
	value := child(section, key)
	if value == nil {
		return true, nil, nil
	}
	if value.Kind == yaml.ScalarNode && value.Tag == "!!bool" && value.Decode(&on) == nil {
		return on, nil, nil
	}
	if value.Kind == yaml.ScalarNode && value.Decode(&on) == nil {
		return on, []string{fmt.Sprintf("costroid: warning: costroid.yml: comment.%s is a YAML 1.1 word and reads as %t; write true or false", key, on)}, nil
	}
	return false, nil, fmt.Errorf("comment: comment.%s in costroid.yml must be true or false", key)
}

// child is the value of a mapping's key, nil when the node is not a mapping
// or lacks the key. A document is read as the mapping it holds, and an alias
// as the node it names, as validate reads it.
func child(node *yaml.Node, key string) *yaml.Node {
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = node.Content[0]
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value != key {
			continue
		}
		if value := node.Content[i+1]; value.Kind != yaml.AliasNode {
			return value
		}
		return node.Content[i+1].Alias
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
