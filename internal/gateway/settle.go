package gateway

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrUnsettled marks a figure that was still moving when the wait for it
// ended. The figure is answered beside it.
var ErrUnsettled = errors.New("may not have settled")

// settling is how a key's spend is read until it settles. LiteLLM queues a
// request's spend and by default writes the queue to its store every 10 to 15
// seconds: PROXY_BATCH_WRITE_AT, 10, plus a random 0 to 5 drawn at startup
// (litellm/constants.py and initialize_scheduled_background_jobs in
// litellm/proxy/proxy_server.py at v1.108.1). Both /key/info and /key/list read
// the store, so a figure read before the next write lacks the run's last
// requests, and an unchanged figure proves nothing until a write has passed.
type settling struct {
	// poll is how often the figure is read. stable is how long a figure that
	// has moved must then hold, and bound ends the wait; it must outlast one
	// write.
	poll, stable, bound time.Duration
}

// settle reads a count until done accepts it, or until it has moved during the
// wait and then held for stable, because the move was a write made after the
// run ended, or until bound has passed. A count that never moved is settled at
// bound. A count still moving then is answered with ErrUnsettled.
func (s settling) settle(ctx context.Context, read func(context.Context) (Count, error), done func(Count) bool) (Count, error) {
	deadline := time.Now().Add(s.bound)
	last, err := read(ctx)
	if err != nil {
		return Count{}, err
	}
	since, moved := time.Now(), false
	for {
		held, late := time.Since(since) >= s.stable, time.Now().After(deadline)
		switch {
		case done(last), held && (moved || late):
			return last, nil
		case late:
			return last, fmt.Errorf("gateway: the key's spend still moved in the last %s of a %s wait, so its last figure, USD %s, %w", s.stable, s.bound, last.Spend, ErrUnsettled)
		}
		select {
		case <-ctx.Done():
			return Count{}, ctx.Err()
		case <-time.After(s.poll):
		}
		next, err := read(ctx)
		if err != nil {
			return Count{}, err
		}
		if next.KeyID != last.KeyID || next.Spend.Cmp(last.Spend) != 0 {
			last, since, moved = next, time.Now(), true
		}
	}
}
