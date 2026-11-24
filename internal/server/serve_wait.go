package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/Costroid/costroid/internal/store"
)

// databasePace is how often serve tries a database that does not answer yet
// at start, and for how long. The bound stays under the 120 seconds the
// chart's startup probe allows, so a start that runs out ends on the service's
// own line rather than on the probe's kill.
var databasePace = struct{ interval, bound time.Duration }{interval: 2 * time.Second, bound: 110 * time.Second}

// databaseWait is the one wait of a start, which each step that reaches the
// database shares.
type databaseWait struct {
	logger   *slog.Logger
	deadline time.Time
	tries    int
}

func newDatabaseWait(logger *slog.Logger) *databaseWait {
	return &databaseWait{logger: logger, deadline: time.Now().Add(databasePace.bound)}
}

// until runs step, which reaches the database at url, until the database
// answers it. While the database does not answer yet, as store.NotAnswering
// reads the error, it logs the try and runs step again after the interval.
// Any other error ends the start at once, and so does the last try's error
// at the bound.
func (w *databaseWait) until(ctx context.Context, url string, step func() error) error {
	for {
		err := step()
		if err == nil || !store.NotAnswering(err) || !time.Now().Add(databasePace.interval).Before(w.deadline) {
			return err
		}
		w.tries++
		w.logger.Warn("the database does not answer yet", "tried", store.Address(url), "answered", err.Error(),
			"try", w.tries, "again_in", databasePace.interval.String(), "bound", databasePace.bound.String())
		timer := time.NewTimer(databasePace.interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
	}
}
