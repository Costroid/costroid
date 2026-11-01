// Package fanout runs CPU-bound work over a list on every core the process has.
package fanout

import (
	"runtime"
	"sync"
)

// minChunk keeps a small list on the caller's goroutine, where a chunk of a
// few rows would cost more to hand off than to run.
const minChunk = 256

// Workers says how many chunks Each cuts a list of n items into.
func Workers(n int) int {
	return max(1, min(runtime.GOMAXPROCS(0), n/minChunk))
}

// Each calls fn for every index below n. A worker's indexes ascend, and each
// worker has its own number below Workers(n), so fn can write to per-worker
// state. A worker stops at its first error. Each returns the error of the
// lowest index that failed in any worker, which is what a sequential loop would
// have returned, and panics again on the caller's goroutine if fn panicked.
func Each(n int, fn func(worker, index int) error) error {
	workers := Workers(n)
	results := make([]outcome, workers)
	if workers == 1 {
		results[0] = runChunk(0, 0, n, fn)
	} else {
		var group sync.WaitGroup
		for worker := range workers {
			group.Go(func() {
				results[worker] = runChunk(worker, n*worker/workers, n*(worker+1)/workers, fn)
			})
		}
		group.Wait()
	}
	for _, result := range results {
		if result.panicked != nil {
			panic(result.panicked)
		}
		if result.err != nil {
			return result.err
		}
	}
	return nil
}

type outcome struct {
	err      error
	panicked any
}

func runChunk(worker, low, high int, fn func(worker, index int) error) (result outcome) {
	defer func() { result.panicked = recover() }()
	for index := low; index < high; index++ {
		if err := fn(worker, index); err != nil {
			return outcome{err: err}
		}
	}
	return outcome{}
}
