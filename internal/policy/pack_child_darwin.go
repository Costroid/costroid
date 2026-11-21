package policy

import (
	"os"
	"syscall"
)

// descriptorDirectory lists this process's open descriptors.
const descriptorDirectory = "/dev/fd"

// selfExecutable is this process's own executable, as the kernel reports it.
func selfExecutable() (string, error) {
	return os.Executable()
}

// childAttributes is empty: macOS has no signal on a parent's death. A child
// whose parent died ends at its CPU limit.
func childAttributes() *syscall.SysProcAttr {
	return nil
}
