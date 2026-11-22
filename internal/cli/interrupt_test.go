package cli

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"
)

// interrupted runs a command under untilInterrupted and sends the interrupt
// once the command says it has started.
func interrupted(t *testing.T, run func(ctx context.Context, started chan<- struct{}) int) (int, string, time.Duration) {
	t.Helper()
	signals := make(chan os.Signal, 1)
	started := make(chan struct{})
	go func() {
		<-started
		signals <- os.Interrupt
	}()
	var stderr bytes.Buffer
	begun := time.Now()
	code := untilInterrupted(context.Background(), signals, &stderr, func(ctx context.Context) int { return run(ctx, started) })
	return code, stderr.String(), time.Since(begun)
}

// TestAnInterruptEndsTheContextAndExits130 holds what Ctrl-C does to each
// kind of command. One that returns when its context ends exits 130 at
// once, whatever it answers. One that never returns is left after a moment.
// One that asked for time to stop what it started gets it, says what it is
// doing, and has its terminal restored first. One that ends by its own rule
// keeps its own code. A command no interrupt reaches keeps its code too.
func TestAnInterruptEndsTheContextAndExits130(t *testing.T) {
	if code := untilInterrupted(context.Background(), make(chan os.Signal), nil, func(context.Context) int { return ExitFailed }); code != ExitFailed {
		t.Errorf("a command no interrupt reaches exits %d, want its own %d", code, ExitFailed)
	}
	code, said, took := interrupted(t, func(ctx context.Context, started chan<- struct{}) int {
		close(started)
		<-ctx.Done()
		return ExitCouldNot
	})
	if code != ExitInterrupted || said != "" || took > settle {
		t.Errorf("a command that returns when its context ends: exit %d after %s, stderr %q", code, took, said)
	}
	never := make(chan struct{})
	defer close(never)
	code, _, took = interrupted(t, func(_ context.Context, started chan<- struct{}) int {
		close(started)
		<-never
		return 0
	})
	if code != ExitInterrupted || took < settle || took > 4*settle {
		t.Errorf("a command that never returns: exit %d after %s, want %d after %s", code, took, ExitInterrupted, settle)
	}
	restored, stopped := false, false
	code, said, took = interrupted(t, func(ctx context.Context, started chan<- struct{}) int {
		forget := OnInterrupt(Interrupt{Wait: time.Minute, Doing: "stopping the agent", Restore: func() { restored = true }})
		defer forget()
		close(started)
		<-ctx.Done()
		time.Sleep(2 * settle)
		stopped = true
		return 143
	})
	if code != ExitInterrupted || !restored || !stopped || said != "costroid: interrupted, stopping the agent\n" || took > 10*settle {
		t.Errorf("a command that holds a child: exit %d after %s, restored %t, stopped %t, stderr %q", code, took, restored, stopped, said)
	}
	code, said, _ = interrupted(t, func(ctx context.Context, started chan<- struct{}) int {
		forget := OnInterrupt(Interrupt{Own: true})
		defer forget()
		close(started)
		<-ctx.Done()
		time.Sleep(2 * settle)
		return ExitDone
	})
	if code != ExitDone || said != "" {
		t.Errorf("a command that ends by its own rule: exit %d, stderr %q", code, said)
	}
	interrupts.Lock()
	left := len(interrupts.asked)
	interrupts.Unlock()
	if left != 0 {
		t.Errorf("%d commands are still held after each returned", left)
	}
}
