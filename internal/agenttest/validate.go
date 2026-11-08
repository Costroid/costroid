package agenttest

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// ClaudeForPluginValidate finds the first claude on PATH as it stood before
// Main put the stubs first, and skips t when there is none. The function it
// returns runs that claude with args, in a settings directory of its own and
// with nonessential traffic off. It fails t on any args but `plugin validate`
// and what follows, so no test reaches a model through it.
func ClaudeForPluginValidate(t testing.TB) func(args ...string) ([]byte, error) {
	t.Helper()
	before, ok := os.LookupEnv(PathBeforeStubs)
	if !ok {
		t.Fatalf("%s is not set: call agenttest.Main from TestMain", PathBeforeStubs)
	}
	claude := installed("claude", before)
	if claude == "" {
		t.Skip("claude is not on PATH")
	}
	return func(args ...string) ([]byte, error) {
		t.Helper()
		if len(args) < 2 || !slices.Equal(args[:2], []string{"plugin", "validate"}) {
			t.Fatalf("claude %q: only claude plugin validate may run", args)
		}
		command := exec.Command(claude, args...)
		command.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+t.TempDir(), "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1")
		return command.CombinedOutput()
	}
}

// installed is the first executable file called name in an absolute
// directory of path, or "" when there is none.
func installed(name, path string) string {
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}
		file := filepath.Join(dir, name)
		if info, err := os.Stat(file); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return file
		}
	}
	return ""
}
