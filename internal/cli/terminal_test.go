package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestTerminalIsTrueOfATerminalAlone(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "file"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if Terminal(file) {
		t.Error("a file reads as a terminal")
	}
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if Terminal(null) {
		t.Errorf("%s reads as a terminal", os.DevNull)
	}
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	if Terminal(read) {
		t.Error("a pipe reads as a terminal")
	}
	// On Linux the controlling side of a new pseudo-terminal is a terminal,
	// and opening it needs none.
	if runtime.GOOS != "linux" {
		return
	}
	pseudo, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("this system opens no pseudo-terminal: %v", err)
	}
	defer pseudo.Close()
	if !Terminal(pseudo) {
		t.Error("a pseudo-terminal does not read as a terminal")
	}
}
