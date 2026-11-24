package cli

import "syscall"

// readSettings is the request that reads a terminal's settings.
const readSettings = syscall.TIOCGETA
