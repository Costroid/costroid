package recipelint

import "go.yaml.in/yaml/v3"

// EventNames lists the events of a workflow's on: key, read through aliases
// and merge keys as lint reads them. root is the workflow's top mapping.
func EventNames(root *yaml.Node) []string {
	return eventNames(field(root, "on"))
}

// Uses calls visit once for every uses: key under node whose value is a
// scalar, read through aliases and merge keys. A step that an alias repeats
// is visited once, at the node the anchor marks.
func Uses(node *yaml.Node, visit func(key, value *yaml.Node)) {
	seen := map[*yaml.Node]bool{}
	walkEntries(node, func(item entry) {
		if item.key.Value == "uses" && item.value.Kind == yaml.ScalarNode && !seen[item.value] {
			seen[item.value] = true
			visit(item.key, item.value)
		}
	})
}

// Field returns the value of key in the mapping node, with aliases followed
// and merge keys expanded, or nil when node holds no such key.
func Field(node *yaml.Node, key string) *yaml.Node {
	return field(node, key)
}
