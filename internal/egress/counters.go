package egress

import (
	"cmp"
	"context"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"
)

const (
	dayLayout    = "2006-01-02"
	flushTimeout = 5 * time.Second
)

// Count is one line of the report: how many calls to a host ended one way on
// one UTC day. It has no field for a path, a query, a header or a body.
type Count struct {
	Day     string `json:"day"`
	Host    string `json:"host"`
	Outcome string `json:"outcome"`
	Count   int64  `json:"count"`
}

// Sink stores counters.
type Sink interface {
	AddEgress(ctx context.Context, counts []Count) error
}

type counterKey struct {
	day, host, outcome string
}

// Counters holds counts in memory and writes them in the background, so a call
// never waits on the database and a failed write never reaches the call. A
// refused call costs an unauthenticated sender nothing but a map update.
type Counters struct {
	sink   Sink
	now    func() time.Time
	logger *slog.Logger

	mu      sync.Mutex
	pending map[counterKey]int64
}

// NewCounters returns Counters that write to sink.
func NewCounters(sink Sink, now func() time.Time, logger *slog.Logger) *Counters {
	return &Counters{sink: sink, now: now, logger: logger, pending: map[counterKey]int64{}}
}

// Record counts one call on today's UTC date.
func (c *Counters) Record(host, outcome string) {
	key := counterKey{c.now().UTC().Format(dayLayout), host, outcome}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending[key]++
}

func (c *Counters) take() map[counterKey]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	taken := c.pending
	c.pending = map[counterKey]int64{}
	return taken
}

func (c *Counters) putBack(counts map[counterKey]int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, count := range counts {
		c.pending[key] += count
	}
}

// Flush writes what is pending. On a failed write the counts wait for the next.
func (c *Counters) Flush(ctx context.Context) error {
	taken := c.take()
	if len(taken) == 0 {
		return nil
	}
	if err := c.sink.AddEgress(ctx, sorted(taken)); err != nil {
		c.putBack(taken)
		return err
	}
	return nil
}

func sorted(counts map[counterKey]int64) []Count {
	keys := slices.SortedFunc(maps.Keys(counts), func(a, b counterKey) int {
		return cmp.Or(cmp.Compare(a.day, b.day), cmp.Compare(a.host, b.host), cmp.Compare(a.outcome, b.outcome))
	})
	lines := make([]Count, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, Count{key.day, key.host, key.outcome, counts[key]})
	}
	return lines
}

// Run flushes at each interval until ctx ends, then once more. A failed write
// is logged and never stops the service.
func (c *Counters) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.flushLogged(ctx)
		case <-ctx.Done():
			last, cancel := context.WithTimeout(context.WithoutCancel(ctx), flushTimeout)
			defer cancel()
			c.flushLogged(last)
			return
		}
	}
}

func (c *Counters) flushLogged(ctx context.Context) {
	if err := c.Flush(ctx); err != nil {
		c.logger.Warn("egress counters not written", "error", err)
	}
}
