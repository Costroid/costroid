// Package agenttest keeps a test from starting a real coding agent. A test
// that runs an agent by name would otherwise start whichever claude or codex
// the machine has, on its owner's account.
package agenttest

import (
	"fmt"
	"os"
	"path/filepath"
)

// Names are the commands that start an agent, or start a package or script
// that is one.
var Names = []string{"bunx", "claude", "codex", "node", "npx"}

// ExitCode is the status every stub exits with.
const ExitCode = 97

// Main puts a directory that holds one stub for each of Names first on PATH,
// runs the tests through run and removes the directory. A test that means to
// run an agent names its own fake by path. A package whose tests run an agent
// by name calls it from TestMain:
//
//	func TestMain(m *testing.M) {
//		os.Exit(agenttest.Main(m.Run))
//	}
func Main(run func() int) int {
	dir, err := os.MkdirTemp("", "agent-stubs-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "agenttest:", err)
		return 1
	}
	defer os.RemoveAll(dir)
	if err := writeStubs(dir); err != nil {
		fmt.Fprintln(os.Stderr, "agenttest:", err)
		return 1
	}
	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		fmt.Fprintln(os.Stderr, "agenttest:", err)
		return 1
	}
	return run()
}

// Refusal is what the stub for name prints on stderr.
func Refusal(name string) string {
	return "test stub: " + name + " must not run"
}

func writeStubs(dir string) error {
	for _, name := range Names {
		script := fmt.Sprintf("#!/bin/sh\necho '%s' >&2\nexit %d\n", Refusal(name), ExitCode)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			return fmt.Errorf("write the %s stub: %w", name, err)
		}
	}
	return nil
}
