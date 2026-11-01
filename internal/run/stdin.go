package run

import (
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// stdinWaitMillis is how long an idle pipe has to show data or close before
// the agent starts without it.
const stdinWaitMillis = 100

// childStdin decides what the agent reads. A pipe nobody writes to would hold
// an agent that reads stdin: `claude -p` waits three seconds and warns. So an
// idle pipe gives the agent none, and it reads end-of-file at once. Terminals,
// files and any reader that is not a file pass through. A producer slower than
// the wait loses its data to the agent; pipe data before the run starts.
func childStdin(stdin io.Reader) io.Reader {
	file, ok := stdin.(*os.File)
	if !ok {
		return stdin
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&(os.ModeNamedPipe|os.ModeSocket) == 0 {
		return stdin
	}
	if hasDataOrClosed(file) {
		return stdin
	}
	return nil
}

func hasDataOrClosed(file *os.File) bool {
	ready := []unix.PollFd{{Fd: int32(file.Fd()), Events: unix.POLLIN}}
	for {
		count, err := unix.Poll(ready, stdinWaitMillis)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return err != nil || count > 0
	}
}
