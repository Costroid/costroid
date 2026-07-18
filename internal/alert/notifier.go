// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package alert

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/Costroid/costroid/internal/storage"
)

// reminderInterval is the daily re-alert cadence for a source that stays
// failing. "Now" is always the run's own FinishedAt (derived from the
// scheduler's injected clock), so the cadence is deterministic and needs no
// wall-clock read.
const reminderInterval = 24 * time.Hour

type sourceKey struct {
	source string
	tenant string
}

// sourceState is the per-source dedup state. failing records whether the last
// observed run was a failure; lastAlertAt is the FinishedAt of the run that
// produced the last failing or reminder alert, which drives the reminder
// cadence.
type sourceState struct {
	failing     bool
	lastAlertAt time.Time
}

// Notifier owns the channels and the per-source, edge-triggered dedup state. It
// is the concrete alerter the scheduler calls once per completed run. It never
// returns an error to the scheduler.
type Notifier struct {
	channels []Channel
	logger   *slog.Logger

	mu     sync.Mutex
	states map[sourceKey]sourceState
}

// NewNotifier builds a Notifier over channels, logging channel failures to
// logger (a nil logger discards). It seeds per-source state from statuses (the
// store's SyncStatuses): a source whose latest persisted run failed starts in
// the failing state with its last-alert time set to that run's finish time, so a
// serve restart mid-outage does not immediately re-page and a post-restart
// recovery still fires exactly one recovered alert.
func NewNotifier(channels []Channel, logger *slog.Logger, statuses []storage.SyncStatus) *Notifier {
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	states := make(map[sourceKey]sourceState, len(statuses))
	for _, status := range statuses {
		if isFailureOutcome(status.Latest.Outcome) {
			key := sourceKey{source: status.Latest.SourceName, tenant: status.Latest.TenantID}
			states[key] = sourceState{failing: true, lastAlertAt: status.Latest.FinishedAt.UTC()}
		}
	}
	return &Notifier{channels: channels, logger: logger, states: states}
}

// NotifySyncRun applies the state machine to run and, if the decision is to
// alert, fans the resulting Message out to every channel SEQUENTIALLY. Each send
// is isolated: a channel error is logged and the next channel still runs.
// NotifySyncRun never returns an error, so a broken channel can never affect the
// scheduler or the other channels.
func (n *Notifier) NotifySyncRun(ctx context.Context, run storage.SyncRun) {
	kind, send := n.decide(run)
	if !send {
		return
	}
	msg := buildMessage(run, kind)
	for _, channel := range n.channels {
		if err := channel.Send(ctx, msg); err != nil {
			n.logger.Error("sending sync alert",
				"channel", channel.Name(), "source", run.SourceName,
				"tenant", run.TenantID, "kind", string(kind), "error", err)
		}
	}
}

// decide advances the per-source dedup state for run and reports the transition
// kind plus whether to send. The state is updated on the DECISION (not on
// delivery success), so a persistently-down endpoint does not re-alert every
// run. "Now" is run.FinishedAt.
func (n *Notifier) decide(run storage.SyncRun) (TransitionKind, bool) {
	key := sourceKey{source: run.SourceName, tenant: run.TenantID}
	now := run.FinishedAt.UTC()

	n.mu.Lock()
	defer n.mu.Unlock()
	state := n.states[key]

	if isFailureOutcome(run.Outcome) {
		switch {
		case !state.failing:
			// Healthy to failing edge.
			n.states[key] = sourceState{failing: true, lastAlertAt: now}
			return KindFailing, true
		case now.Sub(state.lastAlertAt) >= reminderInterval:
			// Still failing, and the daily cadence has elapsed.
			n.states[key] = sourceState{failing: true, lastAlertAt: now}
			return KindReminder, true
		default:
			// Still failing, within the reminder cadence: suppress.
			return "", false
		}
	}

	// Success.
	if state.failing {
		// Failing to healthy edge.
		n.states[key] = sourceState{failing: false}
		return KindRecovered, true
	}
	// Healthy and staying healthy: nothing to send.
	return "", false
}

func isFailureOutcome(outcome string) bool {
	return outcome == "partial" || outcome == "error"
}
