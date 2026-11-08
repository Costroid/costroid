package agenttest_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/agenttest"
)

// recorder stands in for a test, and records how the helper stopped it.
type recorder struct {
	testing.TB
	stopped string
}

func (r *recorder) Fatalf(format string, args ...any) {
	r.stopped = "fatal: " + fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func (r *recorder) Skip(args ...any) {
	r.stopped = "skip: " + fmt.Sprint(args...)
	runtime.Goexit()
}

// within runs f as a test body, which Fatalf and Skip end.
func within(f func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	<-done
}

// TestPluginValidateHelperRefusesOtherCommands puts a claude that records its
// arguments on PATH as it stood before the stubs. The helper runs it for
// `plugin validate` only, fails the test on any other arguments without
// starting it, and skips when no claude is there.
func TestPluginValidateHelperRefusesOtherCommands(t *testing.T) {
	installed := t.TempDir()
	argv := filepath.Join(t.TempDir(), "argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argv + "'\n"
	if err := os.WriteFile(filepath.Join(installed, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(agenttest.PathBeforeStubs, installed)
	for _, args := range [][]string{nil, {"-p", "x"}, {"plugin"}, {"plugin", "install", "x"}, {"validate", "plugin"}, {"--print", "plugin", "validate"}} {
		r := &recorder{TB: t}
		within(func() { agenttest.ClaudeForPluginValidate(r)(args...) })
		if !strings.HasPrefix(r.stopped, "fatal: ") {
			t.Errorf("claude %q was not refused: %q", args, r.stopped)
		}
		if _, err := os.Stat(argv); !os.IsNotExist(err) {
			t.Fatalf("claude %q ran", args)
		}
	}
	r := &recorder{TB: t}
	within(func() {
		if output, err := agenttest.ClaudeForPluginValidate(r)("plugin", "validate", "--strict", "dir"); err != nil {
			t.Errorf("plugin validate: %v\n%s", err, output)
		}
	})
	if r.stopped != "" {
		t.Fatalf("plugin validate stopped the test: %s", r.stopped)
	}
	if got, err := os.ReadFile(argv); err != nil || string(got) != "plugin\nvalidate\n--strict\ndir\n" {
		t.Errorf("claude ran with %q (%v)", got, err)
	}
	t.Setenv(agenttest.PathBeforeStubs, t.TempDir())
	r = &recorder{TB: t}
	within(func() { agenttest.ClaudeForPluginValidate(r) })
	if r.stopped != "skip: claude is not on PATH" {
		t.Errorf("with no claude: %q", r.stopped)
	}
}
