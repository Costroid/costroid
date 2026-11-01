package fanout

import (
	"errors"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"
)

func TestEachVisitsEveryIndexOnce(t *testing.T) {
	for _, n := range []int{0, 1, minChunk - 1, minChunk, 2 * minChunk, 1000 * minChunk} {
		seen := make([]atomic.Int32, n)
		if err := Each(n, func(_, index int) error {
			seen[index].Add(1)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		for index := range seen {
			if got := seen[index].Load(); got != 1 {
				t.Fatalf("n %d: index %d visited %d times", n, index, got)
			}
		}
	}
}

func TestEachKeepsWorkerNumbersInRangeAndItsOwnIndexesAscending(t *testing.T) {
	const n = 100 * minChunk
	last := make([]int, Workers(n))
	for i := range last {
		last[i] = -1
	}
	err := Each(n, func(worker, index int) error {
		if index <= last[worker] {
			return fmt.Errorf("worker %d went from %d to %d", worker, last[worker], index)
		}
		last[worker] = index
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestASmallListStaysOnOneWorker(t *testing.T) {
	if got := Workers(minChunk - 1); got != 1 {
		t.Fatalf("%d workers", got)
	}
	if got := Workers(0); got != 1 {
		t.Fatalf("%d workers", got)
	}
}

func TestWorkersNeverPassesTheProcessLimit(t *testing.T) {
	if got := Workers(1 << 30); got != runtime.GOMAXPROCS(0) {
		t.Fatalf("%d workers", got)
	}
}

func TestEachReturnsTheLowestIndexThatFailed(t *testing.T) {
	runtime.GOMAXPROCS(max(4, runtime.GOMAXPROCS(0)))
	const n = 100 * minChunk
	failing := map[int]bool{n / 3: true, n / 2: true, n - 1: true}
	err := Each(n, func(_, index int) error {
		if failing[index] {
			return fmt.Errorf("index %d", index)
		}
		return nil
	})
	if err == nil || err.Error() != fmt.Sprintf("index %d", n/3) {
		t.Fatalf("err = %v", err)
	}
}

func TestAPanicReachesTheCaller(t *testing.T) {
	const n = 100 * minChunk
	boom := errors.New("boom")
	defer func() {
		if got := recover(); got != boom {
			t.Fatalf("recovered %v", got)
		}
	}()
	_ = Each(n, func(_, index int) error {
		if index == n-1 {
			panic(boom)
		}
		return nil
	})
	t.Fatal("no panic")
}
