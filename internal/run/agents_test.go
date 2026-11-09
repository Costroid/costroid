package run

import (
	"slices"
	"testing"

	"github.com/Costroid/costroid/internal/adapter/copilot"
)

// TestAgentForNamesEveryAgent holds the error for a command no adapter reads
// by its name to Agents: it lists every name --agent takes, and a name added
// to Agents joins the list with no other change. Each name it lists is one
// AgentNamed takes, Copilot CLI's among them.
func TestAgentForNamesEveryAgent(t *testing.T) {
	_, err := AgentFor([]string{"/usr/local/bin/aider", "--yes"})
	want := `run: no adapter for "aider": costroid runs claude-code, claude-code-sdk, codex, codex-sdk, opencode and copilot; --agent names the one the command starts`
	if err == nil || err.Error() != want {
		t.Errorf("AgentFor reads %v, want %s", err, want)
	}
	for _, name := range Agents {
		if _, err := AgentNamed(name); err != nil {
			t.Errorf("the error lists %s, which --agent does not take: %v", name, err)
		}
	}
	if agent, err := AgentNamed(copilot.Name); err != nil || agent.Name() != copilot.Name {
		t.Errorf("--agent copilot picks %v, %v", agent, err)
	}

	kept := Agents
	t.Cleanup(func() { Agents = kept })
	Agents = append(slices.Clone(kept), "next-agent")
	_, err = AgentFor([]string{"aider"})
	want = `run: no adapter for "aider": costroid runs claude-code, claude-code-sdk, codex, codex-sdk, opencode, copilot and next-agent; --agent names the one the command starts`
	if err == nil || err.Error() != want {
		t.Errorf("with an agent added, AgentFor reads %v, want %s", err, want)
	}
}
