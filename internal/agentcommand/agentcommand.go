// Package agentcommand names the agent a command starts by the command's own
// name. costroid run picks its adapter by it, and the recipe linter judges a
// step by it, so both read one rule.
package agentcommand

import "path/filepath"

// Agent names the agent a command starts, as costroid run --agent takes the
// name, from the command's base name. It is false for any other command.
func Agent(command string) (string, bool) {
	switch filepath.Base(command) {
	case "claude":
		return "claude-code", true
	case "codex":
		return "codex", true
	case "copilot":
		return "copilot", true
	case "opencode":
		return "opencode", true
	}
	return "", false
}
