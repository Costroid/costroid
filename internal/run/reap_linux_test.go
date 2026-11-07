//go:build linux

package run

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// detachedRun replays a Claude Code run whose agent leaves a setsid child
// sleeping for a minute, and returns the run's outcome and the child's pid.
func detachedRun(t *testing.T, how string) (outcome, int) {
	t.Helper()
	got, pid, _ := detachedRunLogged(t, how)
	return got, pid
}

func detachedRunLogged(t *testing.T, how string) (outcome, int, string) {
	t.Helper()
	log := installClaudeRun(t, "subagent", subagentSession)
	t.Setenv(fakeDetach, how)
	got := runAgent(t, []string{agentNamed(t, "claude"), "-p", "work"}, "5", pinned(t))
	data, err := os.ReadFile(log + ".detached")
	if err != nil {
		t.Fatalf("the agent left no child: %v; run: %v", err, got.err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if syscall.Kill(pid, syscall.SIGKILL) == nil {
			_, _ = syscall.Wait4(pid, nil, syscall.WNOHANG, nil)
		}
	})
	if got.err != nil {
		t.Fatal(got.err)
	}
	return got, pid, log
}

func assertGone(t *testing.T, pid int) {
	t.Helper()
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		state, _ := readStat(pid)
		t.Errorf("the detached child %d outlived the run: signal 0 answered %v, state %q", pid, err, state.state)
	}
}

// TestDetachedChildIsKilled holds that a process the agent starts in a session
// of its own, outside the agent's process group, is killed and waited for
// before the run returns, so it cannot keep spending or rewrite the rows.
func TestDetachedChildIsKilled(t *testing.T) {
	got, pid := detachedRun(t, "quiet")
	assertGone(t, pid)
	if !strings.Contains(got.stderr, "costroid: warning: the agent left 1 process running, and costroid stopped it\n") {
		t.Errorf("stderr %q", got.stderr)
	}
	if len(got.rows) == 0 {
		t.Error("the run wrote no rows")
	}
}

// TestADetachedChildHoldingStdoutDoesNotHoldTheRun holds that a child which
// keeps the agent's stdout and stderr open is killed when the agent exits,
// rather than holding the run until it ends a minute later.
func TestADetachedChildHoldingStdoutDoesNotHoldTheRun(t *testing.T) {
	started := time.Now()
	_, pid := detachedRun(t, "holding")
	if took := time.Since(started); took > 20*time.Second {
		t.Errorf("the run took %s", took)
	}
	assertGone(t, pid)
}

// TestAWritableCgroupHoldsTheAgent holds that, where the run may write to its
// own cgroup v2 directory, the agent starts in a cgroup of its own, the
// detached child dies with it, and the cgroup is gone after the run. It runs
// only under such a cgroup, for example one systemd-run --user --scope -p
// Delegate=yes makes.
func TestAWritableCgroupHoldsTheAgent(t *testing.T) {
	probe := newAgentCgroup()
	if probe == nil {
		t.Skip("this process may not write to a cgroup v2 subtree of its own")
	}
	probe.remove()
	got, pid, log := detachedRunLogged(t, "quiet")
	assertGone(t, pid)
	data, err := os.ReadFile(log + ".cgroup")
	if err != nil {
		t.Fatal(err)
	}
	own, _ := ownCgroup()
	if !strings.HasPrefix(strings.TrimSpace(string(data)), "0::"+own+"/costroid-run-") {
		t.Errorf("the agent ran in %q, the run in %q", data, own)
	}
	left, err := filepath.Glob(filepath.Join(cgroupRoot, own, "costroid-run-*"))
	if err != nil || len(left) != 0 {
		t.Errorf("cgroups left: %v %v", left, err)
	}
	if !strings.Contains(got.stderr, "the agent left 1 process running") {
		t.Errorf("stderr %q", got.stderr)
	}
}

func TestProcStatIsReadPastACommandNameWithParentheses(t *testing.T) {
	line := "4321 (a) S 9 (b)) Z 77 4321 4321 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 123456 1000 10 18446744073709551615\n"
	stat, err := parseStat(4321, []byte(line))
	if err != nil {
		t.Fatal(err)
	}
	if stat != (procStat{pid: 4321, state: 'Z', parent: 77, start: 123456}) {
		t.Errorf("%+v", stat)
	}
	own, err := readStat(os.Getpid())
	if err != nil || own.parent != os.Getppid() || own.start == 0 {
		t.Errorf("%+v, %v", own, err)
	}
}
