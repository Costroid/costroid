package claudecode

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/Costroid/costroid/internal/adapter"
)

// claimFile is the lock a run holds in the config directory. Claude Code
// itself never reads it.
const claimFile = "costroid-run.lock"

// Claim holds the config directory for one run. A run reads every session in
// the directory that starts and ends inside it as nested, so a second run in
// the same directory would be counted twice: once by itself, and once by the
// run around it. The lock is the kernel's, so it ends with the process that
// held it, however that process ends.
func (a Agent) Claim() (func(), error) {
	dir := a.configDir()
	if dir == "" {
		return nil, errors.New("claude-code: no config directory to claim")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("claude-code: create the config directory: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(dir, claimFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("claude-code: open the run lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// The lock was not taken, so closing the file releases nothing.
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w %s: run one agent at a time per directory, or give each run its own CLAUDE_CONFIG_DIR", adapter.ErrClaimed, dir)
		}
		return nil, fmt.Errorf("claude-code: lock %s: %w", dir, err)
	}
	return func() {
		// Closing the file ends the lock, and a file opened to hold a lock
		// has nothing to flush.
		_ = file.Close()
	}, nil
}
