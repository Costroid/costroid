package policy

import "syscall"

// descriptorDirectory lists this process's open descriptors.
const descriptorDirectory = "/proc/self/fd"

// selfExecutable is this process's own executable. The kernel resolves it,
// so no lookup takes place, and it names the file the process runs even
// after the file on disk was replaced.
func selfExecutable() (string, error) {
	return "/proc/self/exe", nil
}

// childAttributes has the kernel kill the child when the thread that started
// it ends, so a parent that dies leaves no child running.
func childAttributes() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
