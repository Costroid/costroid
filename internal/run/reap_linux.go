//go:build linux

package run

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	// prSetChildSubreaper is PR_SET_CHILD_SUBREAPER from linux/prctl.h.
	prSetChildSubreaper = 36
	// pPID is P_PID from linux/wait.h.
	pPID = 1
	// cgroup2Magic is CGROUP2_SUPER_MAGIC from linux/magic.h.
	cgroup2Magic = 0x63677270
	cgroupRoot   = "/sys/fs/cgroup"
	// maxReapRounds bounds the kills of a process tree that forks as fast as
	// it is killed. Each round kills every child the run holds then.
	maxReapRounds = 100
)

// liveAgents are the agents this process is running now. A run that ends
// stops every child of this process that started after its own agent, except
// these, which belong to other runs.
var liveAgents = struct {
	sync.Mutex
	pids map[int]bool
}{pids: map[int]bool{}}

// containment holds every process an agent starts. The run is a child
// subreaper, so a process the agent leaves behind, a setsid one included,
// becomes the run's own child when its parent exits. Where a writable cgroup
// v2 subtree exists, the agent also starts in a cgroup of its own.
type containment struct {
	pid    int
	since  uint64
	cgroup *agentCgroup
	done   chan struct{}
	left   int
}

// startContained starts the command build returns, contained. A cgroup the
// kernel refuses to start the agent in is dropped, and the agent starts
// without it.
func startContained(build func() *exec.Cmd) (*exec.Cmd, *containment, error) {
	if err := setSubreaper(); err != nil {
		return nil, nil, err
	}
	contained := &containment{done: make(chan struct{}), cgroup: newAgentCgroup()}
	command := build()
	if contained.cgroup != nil {
		command.SysProcAttr.UseCgroupFD = true
		command.SysProcAttr.CgroupFD = int(contained.cgroup.dir.Fd())
		if err := command.Start(); err == nil {
			return contained.started(command)
		}
		contained.cgroup.remove()
		contained.cgroup = nil
		command = build()
	}
	if err := command.Start(); err != nil {
		return command, nil, err
	}
	return contained.started(command)
}

func setSubreaper() error {
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0); errno != 0 {
		return fmt.Errorf("become the agent's subreaper: %w", errno)
	}
	return nil
}

func (c *containment) started(command *exec.Cmd) (*exec.Cmd, *containment, error) {
	c.pid = command.Process.Pid
	liveAgents.Lock()
	liveAgents.pids[c.pid] = true
	liveAgents.Unlock()
	if stat, err := readStat(c.pid); err == nil {
		c.since = stat.start
	}
	go func() {
		defer close(c.done)
		// An agent this process can no longer wait for has ended too.
		_ = waitExited(c.pid)
		c.left = c.stopDescendants()
	}()
	return command, c, nil
}

// end waits for the agent to exit, then for every process it left to be
// stopped, and says how many were still running.
func (c *containment) end() int {
	<-c.done
	return c.left
}

// release forgets the agent once it has been waited for, and removes its
// cgroup.
func (c *containment) release() {
	liveAgents.Lock()
	delete(liveAgents.pids, c.pid)
	liveAgents.Unlock()
	if c.cgroup != nil {
		c.cgroup.remove()
	}
}

// waitExited blocks until the process exits, and leaves it to be waited for.
func waitExited(pid int) error {
	var info [128]byte
	for {
		_, _, errno := syscall.Syscall6(syscall.SYS_WAITID, pPID, uintptr(pid), uintptr(unsafe.Pointer(&info[0])), syscall.WEXITED|syscall.WNOWAIT, 0, 0)
		if errno != syscall.EINTR {
			if errno != 0 {
				return errno
			}
			return nil
		}
	}
}

// stopDescendants kills the agent's cgroup, then kills and waits for every
// child the run holds that started after the agent and is no run's agent. A
// killed process's own children become the run's, so it repeats until none
// is left.
func (c *containment) stopDescendants() int {
	running := 0
	if c.cgroup != nil {
		running = c.cgroup.kill()
	}
	for range maxReapRounds {
		children := c.leftChildren()
		if len(children) == 0 {
			return running
		}
		for _, child := range children {
			if child.state != 'Z' {
				running++
			}
			_ = syscall.Kill(child.pid, syscall.SIGKILL)
		}
		for _, child := range children {
			reap(child.pid)
		}
	}
	return running
}

func reap(pid int) {
	for {
		if _, err := syscall.Wait4(pid, nil, 0, nil); !errors.Is(err, syscall.EINTR) {
			return
		}
	}
}

// leftChildren lists this process's children that started at or after the
// agent and are not an agent a run is still waiting for.
func (c *containment) leftChildren() []procStat {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	self := os.Getpid()
	liveAgents.Lock()
	defer liveAgents.Unlock()
	var children []procStat
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == c.pid || liveAgents.pids[pid] {
			continue
		}
		stat, err := readStat(pid)
		if err == nil && stat.parent == self && stat.start >= c.since {
			children = append(children, stat)
		}
	}
	return children
}

// procStat is what /proc/<pid>/stat says about a process.
type procStat struct {
	pid    int
	state  byte
	parent int
	// start is the process's start time, in clock ticks after boot.
	start uint64
}

// readStat reads /proc/<pid>/stat. The command name sits in parentheses and
// can hold any character, so the fields are read after the last ')'.
func readStat(pid int) (procStat, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return procStat{}, err
	}
	return parseStat(pid, data)
}

func parseStat(pid int, data []byte) (procStat, error) {
	end := bytes.LastIndexByte(data, ')')
	if end < 0 {
		return procStat{}, fmt.Errorf("run: /proc/%d/stat names no command", pid)
	}
	// After the name: state, ppid, then starttime as the 20th field.
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 || len(fields[0]) != 1 {
		return procStat{}, fmt.Errorf("run: /proc/%d/stat holds %d fields", pid, len(fields))
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return procStat{}, fmt.Errorf("run: /proc/%d/stat: %w", pid, err)
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return procStat{}, fmt.Errorf("run: /proc/%d/stat: %w", pid, err)
	}
	return procStat{pid: pid, state: fields[0][0], parent: parent, start: start}, nil
}

// agentCgroup is a cgroup v2 directory made for one agent below the run's own
// cgroup.
type agentCgroup struct {
	dir  *os.File
	path string
}

var cgroupSequence struct {
	sync.Mutex
	next int
}

// newAgentCgroup makes the agent's cgroup when the run's own cgroup is on
// cgroup v2 and the run may write to it. Nil means there is none.
func newAgentCgroup() *agentCgroup {
	own, ok := ownCgroup()
	if !ok {
		return nil
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(cgroupRoot, &fs); err != nil || fs.Type != cgroup2Magic {
		return nil
	}
	parent := filepath.Join(cgroupRoot, own)
	// Starting a process in the new cgroup moves it out of the run's own, which
	// needs write access to the run's cgroup.procs.
	if syscall.Access(filepath.Join(parent, "cgroup.procs"), 2) != nil {
		return nil
	}
	cgroupSequence.Lock()
	cgroupSequence.next++
	name := fmt.Sprintf("costroid-run-%d-%d", os.Getpid(), cgroupSequence.next)
	cgroupSequence.Unlock()
	path := filepath.Join(parent, name)
	if err := os.Mkdir(path, 0o755); err != nil {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		_ = os.Remove(path)
		return nil
	}
	return &agentCgroup{dir: dir, path: path}
}

// ownCgroup reads this process's cgroup v2 path from /proc/self/cgroup.
func ownCgroup() (string, bool) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if path, ok := strings.CutPrefix(line, "0::"); ok && strings.HasPrefix(path, "/") {
			return path, true
		}
	}
	return "", false
}

// kill ends every process in the cgroup, and returns how many it held. The
// agent has exited by then, so none of them is the agent. cgroup.kill ends
// them at once from Linux 5.14; before it, each listed process is killed
// until none is left.
func (g *agentCgroup) kill() int {
	held := len(g.processes())
	if os.WriteFile(filepath.Join(g.path, "cgroup.kill"), []byte("1"), 0) != nil {
		for range maxReapRounds {
			pids := g.processes()
			if len(pids) == 0 {
				break
			}
			for _, pid := range pids {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	}
	// A process leaves cgroup.procs when it exits, before it is waited for.
	for range 100 {
		if len(g.processes()) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return held
}

func (g *agentCgroup) processes() []int {
	data, err := os.ReadFile(filepath.Join(g.path, "cgroup.procs"))
	if err != nil {
		return nil
	}
	var pids []int
	for _, field := range strings.Fields(string(data)) {
		if pid, err := strconv.Atoi(field); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// remove deletes the cgroup. A killed process leaves it a moment after it
// exits, so the removal is tried for up to a second.
func (g *agentCgroup) remove() {
	_ = g.dir.Close()
	for range 100 {
		if err := os.Remove(g.path); err == nil || errors.Is(err, os.ErrNotExist) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
