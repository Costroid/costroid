package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/Costroid/costroid/internal/cli"
)

// saidEvery is how often a command that waits on a host says it still waits.
const saidEvery = 10 * time.Second

// stillWaiting is one wait of a command on a host. While the command sleeps
// between its requests, it says at a fixed interval what it waits for and how
// far into its longest wait it is: a plain line on stderr, which a terminal
// and a job's log show alike and --quiet drops. It runs on the command's own
// goroutine, so its lines never cross another's.
type stillWaiting struct {
	stderr io.Writer
	// what names what the command waits for, such as the verdict on a batch.
	what string
	// every is the interval, and no line is said when it is zero. limit is
	// the longest the command waits.
	every, limit time.Duration
	// said counts the lines said, so each states a whole number of intervals.
	said  int
	began time.Time
}

// sleep waits for d, and answers false when ctx ended first.
func (w *stillWaiting) sleep(ctx context.Context, d time.Duration) bool {
	if w.began.IsZero() {
		w.began = time.Now()
	}
	end := time.Now().Add(d)
	for {
		next := end
		if due := w.began.Add(time.Duration(w.said+1) * w.every); w.every > 0 && due.Before(end) {
			next = due
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Until(next)):
		}
		if next == end {
			return true
		}
		w.said++
		cli.Say(w.stderr, "costroid: still waiting for %s, %s of at most %s\n", w.what, span(time.Duration(w.said)*w.every), span(w.limit))
	}
}

// span is a duration in whole seconds, as costroid's lines state one, or as
// Go prints it when it is not whole seconds.
func span(d time.Duration) string {
	if d%time.Second == 0 {
		return fmt.Sprintf("%d s", d/time.Second)
	}
	return d.String()
}
