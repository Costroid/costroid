package cli

import (
	"os"
	"syscall"
	"unsafe"
)

// Terminal reports whether file is a terminal, by asking for its settings as
// isatty does. A pipe, a file and /dev/null have none.
func Terminal(file *os.File) bool {
	var settings syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), readSettings, uintptr(unsafe.Pointer(&settings)))
	return errno == 0
}
