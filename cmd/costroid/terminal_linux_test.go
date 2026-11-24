//go:build linux

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"unicode/utf8"
	"unsafe"

	costroid "github.com/Costroid/costroid"
)

// openTerminal opens a pseudo-terminal. It answers the side a command writes
// to, which is a terminal, and a function that closes it and answers what
// the terminal showed, a line feed at each line's end.
func openTerminal(t *testing.T) (*os.File, func() string) {
	t.Helper()
	control, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("this system opens no pseudo-terminal: %v", err)
	}
	var unlocked, number int32
	for request, value := range map[uintptr]*int32{syscall.TIOCSPTLCK: &unlocked, syscall.TIOCGPTN: &number} {
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, control.Fd(), request, uintptr(unsafe.Pointer(value))); errno != 0 {
			t.Fatalf("set up the pseudo-terminal: %v", errno)
		}
	}
	terminal, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	var shown bytes.Buffer
	done := make(chan struct{})
	go func() {
		// The read ends in an error once the other side is closed.
		_, _ = io.Copy(&shown, control)
		close(done)
	}()
	return terminal, func() string {
		terminal.Close()
		<-done
		control.Close()
		return strings.ReplaceAll(shown.String(), "\r\n", "\n")
	}
}

// unhung is text with each line a command hung at 80 columns put back on one
// line: a line that stands under the text of the label above it, such as
// "warn: " or "costroid: ", joins the line it carries on.
func unhung(text string) string {
	var lines []string
	indent := -1
	for _, line := range strings.Split(text, "\n") {
		if indent > 0 && len(line) > indent && strings.TrimLeft(line[:indent], " ") == "" && line[indent] != ' ' {
			lines[len(lines)-1] += " " + line[indent:]
			continue
		}
		indent = -1
		if label, _, found := strings.Cut(line, ": "); found && !strings.ContainsAny(label, " \t") {
			indent = len(label) + 2
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// tooWide answers the lines of shown wider than 80 columns that a wrap could
// have broken: more than one word stands after the line's indent or label. A
// command to copy, which opens with two spaces or a dollar sign, is left
// whole.
func tooWide(shown string) []string {
	var wide []string
	for _, line := range strings.Split(shown, "\n") {
		text := strings.TrimLeft(line, " ")
		if label, rest, found := strings.Cut(text, ": "); found && !strings.Contains(label, " ") {
			text = rest
		}
		copied := strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") || strings.HasPrefix(line, "$ ")
		if utf8.RuneCountInString(line) > usageWidth && strings.Contains(text, " ") && !copied {
			wide = append(wide, line)
		}
	}
	return wide
}

// TestProseOutsideTheHelpFitsEightyColumnsAtATerminal runs doctor, init and
// policy twice: into a pipe, where each prints its lines whole, and at a
// terminal, where every line of prose fits 80 columns. The terminal's lines
// put back together are the pipe's, byte for byte, so a wrap changes where a
// line breaks and nothing else. doctor's state stays the first word of each
// check, and policy ends on the verdict sentence as a paragraph of its own.
func TestProseOutsideTheHelpFitsEightyColumnsAtATerminal(t *testing.T) {
	t.Run("doctor", func(t *testing.T) {
		for name, root := range map[string]string{"an empty repository": doctorRepository(t, nil), "what init writes": initialized(t, "github")} {
			_, piped, _ := runDoctor(t, root, ghHost("", ""))
			terminal, shown := openTerminal(t)
			code := doctor(context.Background(), root, releaseBuild, ghHost("", "").host(), nil, terminal, terminal)
			printed := shown()
			if wide := tooWide(printed); code > exitRejected || len(wide) > 0 || unhung(printed) != piped {
				t.Errorf("%s: exit %d, lines past 80 columns %q, and at the terminal doctor printed\n%sinto a pipe\n%s", name, code, wide, printed, piped)
			}
			if !strings.Contains(piped, "\nwarn: ") || len(tooWide(piped)) == 0 {
				t.Errorf("%s: the pipe holds no long check to wrap:\n%s", name, piped)
			}
			for _, line := range strings.Split(strings.TrimSuffix(printed, "\n"), "\n") {
				if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "ok: ") && !strings.HasPrefix(line, "warn: ") && !strings.HasPrefix(line, "fail: ") && !strings.HasPrefix(line, "costroid: ") {
					t.Errorf("%s: a line opens with neither a state nor an indent: %q", name, line)
				}
			}
		}
	})
	t.Run("init", func(t *testing.T) {
		run := func(stderr io.Writer) int {
			root := t.TempDir()
			repo := repository{root: root, dir: root}
			return initRepository(func() (repository, error) { return repo, nil }, releaseBuild, costroid.Recipes, testPR, []string{"github", "--agent", "claude", "--platform-url", "https://app.costroid.com"}, io.Discard, stderr)
		}
		var piped bytes.Buffer
		terminal, shown := openTerminal(t)
		pipedCode, code := run(&piped), run(terminal)
		printed := shown()
		if wide := tooWide(printed); code != 0 || pipedCode != 0 || len(wide) > 0 || unhung(printed) != piped.String() {
			t.Errorf("exit %d, lines past 80 columns %q, and at the terminal init printed\n%sinto a pipe\n%s", code, wide, printed, piped.String())
		}
		if first, _, _ := strings.Cut(piped.String(), "\n"); utf8.RuneCountInString(first) <= usageWidth || !strings.HasPrefix(first, "costroid: wrote ") {
			t.Errorf("the pipe's first line is no long line to wrap: %q", first)
		}
	})
	t.Run("policy", func(t *testing.T) {
		config := writeBaseConfig(t, "comment:\n  show_cost: true\n")
		args := []string{"policy", "--rows", realRows, "--config", config, "--at", "2026-11-21T09:00:00Z"}
		code, piped, _ := execute(t, args...)
		terminal, shown := openTerminal(t)
		atTerminal := dispatch(context.Background(), args, strings.NewReader(""), terminal, io.Discard)
		printed := shown()
		var sentences []string
		for _, paragraph := range strings.Split(strings.TrimSuffix(printed, "\n"), "\n\n") {
			sentences = append(sentences, strings.ReplaceAll(paragraph, "\n", " "))
		}
		if wide := tooWide(printed); atTerminal != code || len(wide) > 0 || strings.Join(sentences, "\n")+"\n" != piped {
			t.Errorf("exit %d, lines past 80 columns %q, and at the terminal policy printed\n%sinto a pipe\n%s", atTerminal, wide, printed, piped)
		}
		if len(sentences) < 2 || len(tooWide(piped)) == 0 || !strings.HasPrefix(sentences[len(sentences)-1], "No budget set: ") {
			t.Errorf("the pipe holds no long sentence to wrap, or the last paragraph is not the verdict sentence:\n%s", printed)
		}
	})
}
