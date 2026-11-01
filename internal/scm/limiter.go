package scm

import (
	"fmt"
	"sync"
	"time"
)

// GitHub's limits per installation.
const (
	writesPerMinute = 80
	writesPerHour   = 500
	maxConcurrent   = 100
	concurrentWait  = time.Second
)

// LimitError says a request was held back, and for how long to wait.
type LimitError struct {
	Wait time.Duration
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("scm: installation limit reached, retry after %s", e.Wait)
}

// bucket is a token bucket whose tokens return one period after they are
// spent. A bucket that refills steadily would pass twice its limit in a rolling
// period, a full burst and then the refill, and GitHub's limits are rolling.
type bucket struct {
	spent  []time.Time
	next   int
	period time.Duration
}

func newBucket(limit int, period time.Duration) *bucket {
	return &bucket{spent: make([]time.Time, limit), period: period}
}

// wait is how long until a token returns, zero when one is there.
func (b *bucket) wait(now time.Time) time.Duration {
	oldest := b.spent[b.next]
	if oldest.IsZero() {
		return 0
	}
	return max(oldest.Add(b.period).Sub(now), 0)
}

func (b *bucket) take(now time.Time) {
	b.spent[b.next] = now
	b.next = (b.next + 1) % len(b.spent)
}

type quota struct {
	minute *bucket
	hour   *bucket
	active int
}

// Limiter holds GitHub's limits per installation: two token buckets for
// content writes, and a ceiling on requests in flight.
type Limiter struct {
	mu            sync.Mutex
	now           func() time.Time
	installations map[int64]*quota
}

// NewLimiter returns a Limiter reading the given clock.
func NewLimiter(now func() time.Time) *Limiter {
	return &Limiter{now: now, installations: map[int64]*quota{}}
}

// Admit claims a place for one request. The caller runs release when the
// request ends. A refused request takes nothing from either bucket.
func (l *Limiter) Admit(installation int64, write bool) (release func(), err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	state, found := l.installations[installation]
	if !found {
		state = &quota{
			minute: newBucket(writesPerMinute, time.Minute),
			hour:   newBucket(writesPerHour, time.Hour),
		}
		l.installations[installation] = state
	}
	if state.active >= maxConcurrent {
		return nil, &LimitError{Wait: concurrentWait}
	}
	if write {
		if wait := max(state.minute.wait(now), state.hour.wait(now)); wait > 0 {
			return nil, &LimitError{Wait: wait}
		}
		state.minute.take(now)
		state.hour.take(now)
	}
	state.active++
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		state.active--
	}, nil
}
