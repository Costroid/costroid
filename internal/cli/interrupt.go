package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// settle is how long an interrupt waits for a command that holds nothing to
// return, so that a child the command's context ends is gone before costroid
// is.
const settle = 250 * time.Millisecond

// Interrupt is what a command needs when Ctrl-C reaches it. The zero value
// asks for nothing: the command's context ends and costroid exits
// ExitInterrupted.
type Interrupt struct {
	// Wait is how long costroid waits for the command to return once its
	// context has ended, which a command that holds a child process needs to
	// stop it.
	Wait time.Duration
	// Doing says what the command does in that time, such as "stopping
	// claude". It is printed at once.
	Doing string
	// Restore runs at once, for a command that changed the terminal.
	Restore func()
	// Own says the command ends on an interrupt by its own rule, as a
	// service that drains does, and exits with its own code.
	Own bool
}

// interrupts holds what the commands running now asked for.
var interrupts = struct {
	sync.Mutex
	next  int
	asked map[int]Interrupt
}{asked: map[int]Interrupt{}}

// OnInterrupt records what a command needs when Ctrl-C reaches it, until the
// function it answers is called.
func OnInterrupt(need Interrupt) (forget func()) {
	interrupts.Lock()
	defer interrupts.Unlock()
	id := interrupts.next
	interrupts.next++
	interrupts.asked[id] = need
	return func() {
		interrupts.Lock()
		defer interrupts.Unlock()
		delete(interrupts.asked, id)
	}
}

// Main runs a command and answers the code costroid exits with. SIGTERM ends
// the command's context, and the command ends as it chooses. Ctrl-C ends the
// context too, and costroid then exits ExitInterrupted, whatever the command
// answers, once the command has returned or the time it asked for has passed.
func Main(run func(ctx context.Context) int) int {
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	defer signal.Stop(interrupt)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	return untilInterrupted(ctx, interrupt, os.Stderr, run)
}

func untilInterrupted(ctx context.Context, interrupt <-chan os.Signal, stderr io.Writer, run func(ctx context.Context) int) int {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- run(ctx) }()
	select {
	case code := <-done:
		return code
	case <-interrupt:
	}
	wait, own := settle, false
	interrupts.Lock()
	for _, need := range interrupts.asked {
		wait = max(wait, need.Wait)
		own = own || need.Own
		if need.Restore != nil {
			need.Restore()
		}
		if need.Doing != "" {
			fmt.Fprintf(stderr, "costroid: interrupted, %s\n", need.Doing)
		}
	}
	interrupts.Unlock()
	cancel()
	if own {
		return <-done
	}
	select {
	case <-done:
	case <-time.After(wait):
	}
	return ExitInterrupted
}
