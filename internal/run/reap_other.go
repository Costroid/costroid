//go:build !linux

package run

import (
	"os/exec"
	"syscall"
)

// containment holds the agent's process group. Outside Linux there is no
// child subreaper, so a process that leaves the group, a setsid one included,
// outlives the run.
type containment struct {
	pid int
}

// startContained starts the command build returns in its own process group.
func startContained(build func() *exec.Cmd) (*exec.Cmd, *containment, error) {
	command := build()
	if err := command.Start(); err != nil {
		return command, nil, err
	}
	return command, &containment{pid: command.Process.Pid}, nil
}

// end returns at once: the group is killed when the agent has been waited for.
func (c *containment) end() int {
	return 0
}

// release kills what is left of the agent's process group.
func (c *containment) release() {
	// A group that is already gone needs no signal.
	_ = syscall.Kill(-c.pid, syscall.SIGKILL)
}
