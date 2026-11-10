package agentcommand_test

import (
	"testing"

	"github.com/Costroid/costroid/internal/agentcommand"
	"github.com/Costroid/costroid/internal/run"
)

// TestAgentNamesWhatRunStarts holds each command's name to the agent
// costroid run starts for it, by its path or alone, and to a name --agent
// takes. A command no agent is known by names none.
func TestAgentNamesWhatRunStarts(t *testing.T) {
	for command, want := range map[string]string{
		"claude":                       "claude-code",
		"/usr/local/bin/codex":         "codex",
		"copilot":                      "copilot",
		"./node_modules/.bin/opencode": "opencode",
	} {
		name, known := agentcommand.Agent(command)
		if !known || name != want {
			t.Errorf("Agent(%q) = %q, %v, want %q", command, name, known, want)
		}
		agent, err := run.AgentFor([]string{command, "-p", "x"})
		if err != nil || agent.Name() != want {
			t.Errorf("costroid run starts %v, %v for %q, want %s", agent, err, command, want)
		}
		if _, err := run.AgentNamed(name); err != nil {
			t.Errorf("--agent does not take %q: %v", name, err)
		}
	}
	for _, command := range []string{"aider", "node", "npx", "claude-code", "copilot;", ""} {
		if name, known := agentcommand.Agent(command); known {
			t.Errorf("Agent(%q) = %q, want no agent", command, name)
		}
	}
}
