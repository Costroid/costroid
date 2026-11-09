package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// agentEntry is one agent the harness records. Adding an agent is one entry
// in agents: collect, local, scrub, verify and fixture read nothing else.
type agentEntry struct {
	// name is collect's --agent and the manifest's agent.
	name string
	// title names the agent in a fixture's README, before its version.
	title string
	// adapter is the package under internal/adapter whose testdata holds the
	// agent's fixtures.
	adapter string
	// stream says how the agent prints the stream collect reads from its
	// stdout. Empty when the agent leaves no stream, so local cannot run it.
	stream string
	// homeVariable names the directory the agent writes its files to, and
	// homeDefault is that directory under the user's home when the variable
	// is unset. With no variable, collect takes --home.
	homeVariable string
	homeDefault  string
	// collect copies one run's files into c. stream and execution are the
	// files collect was given, already added to c.
	collect func(e *agentEntry, c *collected, src source, stream, execution []byte) error
	// allowlist is every key the agent's recordings keep, declared in
	// allowlistFile in this directory.
	allowlist     *allowlist
	allowlistFile string
	// recognizes says whether a line of a run with no manifest is this
	// agent's. Nil when the agent's files cannot be told from another's.
	recognizes func(line *value) bool
	// version reads the agent's own version from the scrubbed files.
	version func(files []*recordingFile) string
	// total states the run's own figure for a README.
	total func(files []*recordingFile) string
}

// agents is every agent the harness records, in the order a run with no
// manifest is matched against them.
var agents = []*agentEntry{
	{
		name:          agentClaudeCode,
		title:         "Claude Code",
		adapter:       "claudecode",
		stream:        "claude -p with --output-format stream-json --verbose",
		homeVariable:  "CLAUDE_CONFIG_DIR",
		homeDefault:   ".claude",
		collect:       collectClaudeRun,
		allowlist:     sessionAllowlist,
		allowlistFile: "allowlist.go",
		recognizes: func(line *value) bool {
			return line.fieldText("claude_code_version") != "" || line.fieldText("sessionId") != ""
		},
		version: agentVersion,
		total:   claudeTotal,
	},
	{
		name:          agentCodex,
		title:         "Codex",
		adapter:       "codex",
		stream:        "codex exec --json",
		homeVariable:  "CODEX_HOME",
		homeDefault:   ".codex",
		collect:       collectCodexRun,
		allowlist:     sessionAllowlist,
		allowlistFile: "allowlist.go",
		recognizes: func(line *value) bool {
			return line.fieldText("type") == "session_meta" || line.fieldText("type") == "thread.started"
		},
		version: agentVersion,
		total:   codexTotal,
	},
	{
		name:          agentGhAw,
		title:         "gh-aw's token-usage records, schema",
		adapter:       "ghaw",
		collect:       collectGhAw,
		allowlist:     ghawAllowlist,
		allowlistFile: "allowlist_ghaw.go",
		recognizes: func(line *value) bool {
			return line.fieldText("event") == "token_usage" && strings.HasPrefix(line.fieldText("_schema"), ghawSchemaPrefix)
		},
		version: ghawVersion,
		total:   ghawTotal,
	},
	{
		name:          "claude-agent-sdk",
		title:         "the Claude Agent SDK, Claude Code",
		adapter:       "claudecode",
		stream:        "a program that prints every message of query() as one JSON line",
		homeVariable:  "CLAUDE_CONFIG_DIR",
		homeDefault:   ".claude",
		collect:       collectClaudeRun,
		allowlist:     sessionAllowlist,
		allowlistFile: "allowlist.go",
		version:       agentVersion,
		total:         claudeTotal,
	},
	{
		name:          "codex-sdk",
		title:         "the Codex SDK, Codex",
		adapter:       "codex",
		stream:        "a program that prints every event of runStreamed() as one JSON line",
		homeVariable:  "CODEX_HOME",
		homeDefault:   ".codex",
		collect:       collectCodexRun,
		allowlist:     sessionAllowlist,
		allowlistFile: "allowlist.go",
		version:       agentVersion,
		total:         codexTotal,
	},

	{
		name:          agentCopilot,
		title:         "Copilot CLI",
		adapter:       "copilot",
		stream:        "copilot -p with --output-format json",
		homeVariable:  "COPILOT_HOME",
		homeDefault:   ".copilot",
		collect:       collectCopilot,
		allowlist:     copilotAllowlist,
		allowlistFile: "allowlist_copilot.go",
		recognizes: func(line *value) bool {
			return line.fieldText("type") == "session.start" && line.field("data").fieldText("producer") == "copilot-agent"
		},
		version: copilotVersion,
		total:   copilotTotal,
	},
}

// agentFor is the entry named name.
func agentFor(name string) (*agentEntry, error) {
	for _, entry := range agents {
		if entry.name == name {
			return entry, nil
		}
	}
	return nil, fmt.Errorf("agent %q is not one of %s", name, agentList(agents, "or"))
}

// agentList names the entries as a list in prose, joined by word.
func agentList(entries []*agentEntry, word string) string {
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.name
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " " + word + " " + names[len(names)-1]
}

// streamAgents are the entries local can run, those that print a stream.
func streamAgents() []*agentEntry {
	var found []*agentEntry
	for _, entry := range agents {
		if entry.stream != "" {
			found = append(found, entry)
		}
	}
	return found
}

// recognizedAgent names the agent whose files these are, by the first line an
// entry recognizes, or "" when none does.
func recognizedAgent(files []*recordingFile) string {
	for _, file := range files {
		for _, line := range file.lines {
			for _, entry := range agents {
				if entry.recognizes != nil && entry.recognizes(line) {
					return entry.name
				}
			}
		}
	}
	return ""
}

// allowlistOf is the allowlist a recording of agent is held to. A recording
// that names no agent the registry holds, a host's included, is held to the
// list of the sessions recorded before the registry.
func allowlistOf(agent string) *allowlist {
	if entry, err := agentFor(agent); err == nil {
		return entry.allowlist
	}
	return sessionAllowlist
}

// home is the directory the agent writes its files to.
func (e *agentEntry) home(getenv func(string) string) (string, error) {
	if e.homeVariable == "" {
		return "", fmt.Errorf("%s keeps no home directory: give --home the directory that holds the run's files", e.name)
	}
	if dir := getenv(e.homeVariable); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find the agent's directory: set %s: %w", e.homeVariable, err)
	}
	return filepath.Join(home, e.homeDefault), nil
}
