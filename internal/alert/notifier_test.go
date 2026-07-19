// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package alert

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/storage"
)

type recordingChannel struct {
	name string
	err  error

	mu            sync.Mutex
	sent          []Message
	sentAnomalies []AnomalyMessage
}

func (c *recordingChannel) Name() string { return c.name }

func (c *recordingChannel) Send(_ context.Context, msg Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, msg)
	return c.err
}

func (c *recordingChannel) SendAnomaly(_ context.Context, msg AnomalyMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sentAnomalies = append(c.sentAnomalies, msg)
	return c.err
}

// anomalies returns a copy of the anomaly messages recorded so far.
func (c *recordingChannel) anomalies() []AnomalyMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]AnomalyMessage, len(c.sentAnomalies))
	copy(out, c.sentAnomalies)
	return out
}

func (c *recordingChannel) kinds() []TransitionKind {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]TransitionKind, len(c.sent))
	for i, m := range c.sent {
		out[i] = m.Kind
	}
	return out
}

func equalKinds(got, want []TransitionKind) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func run(source, outcome string, finished time.Time) storage.SyncRun {
	return storage.SyncRun{
		SourceName: source, Connector: "aws-focus-s3", TenantID: "default",
		Outcome: outcome, StartedAt: finished.Add(-time.Minute), FinishedAt: finished,
	}
}

func TestNotifierEdgeTriggerReminderRecovery(t *testing.T) {
	base := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	ch := &recordingChannel{name: "w"}
	n := NewNotifier([]Channel{ch}, nil, nil)
	ctx := context.Background()

	n.NotifySyncRun(ctx, run("s", "error", base))                     // healthy -> failing
	n.NotifySyncRun(ctx, run("s", "partial", base.Add(6*time.Hour)))  // suppressed (< 24h)
	n.NotifySyncRun(ctx, run("s", "error", base.Add(24*time.Hour)))   // reminder (>= 24h since last alert)
	n.NotifySyncRun(ctx, run("s", "success", base.Add(30*time.Hour))) // recovered
	n.NotifySyncRun(ctx, run("s", "success", base.Add(40*time.Hour))) // healthy -> healthy (nothing)

	want := []TransitionKind{KindFailing, KindReminder, KindRecovered}
	if got := ch.kinds(); !equalKinds(got, want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
}

func TestNotifierReminderResetsCadenceFromLastAlert(t *testing.T) {
	base := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	ch := &recordingChannel{name: "w"}
	n := NewNotifier([]Channel{ch}, nil, nil)
	ctx := context.Background()

	n.NotifySyncRun(ctx, run("s", "error", base))                   // failing, lastAlert = base
	n.NotifySyncRun(ctx, run("s", "error", base.Add(25*time.Hour))) // reminder, lastAlert = base+25h
	n.NotifySyncRun(ctx, run("s", "error", base.Add(30*time.Hour))) // 5h since last alert: suppressed
	n.NotifySyncRun(ctx, run("s", "error", base.Add(49*time.Hour))) // 24h since last alert: reminder

	want := []TransitionKind{KindFailing, KindReminder, KindReminder}
	if got := ch.kinds(); !equalKinds(got, want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
}

func TestNotifierSeededFailingRecoversAfterRestart(t *testing.T) {
	seed := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	statuses := []storage.SyncStatus{{Latest: storage.SyncRun{SourceName: "s", TenantID: "default", Outcome: "error", FinishedAt: seed}}}
	ch := &recordingChannel{name: "w"}
	n := NewNotifier([]Channel{ch}, nil, statuses)

	n.NotifySyncRun(context.Background(), run("s", "success", seed.Add(1*time.Hour)))
	if got := ch.kinds(); !equalKinds(got, []TransitionKind{KindRecovered}) {
		t.Fatalf("a seeded-failing source that succeeds should fire exactly one recovered; got %v", got)
	}
}

func TestNotifierSeededFailingSuppressedThenReminder(t *testing.T) {
	seed := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	statuses := []storage.SyncStatus{{Latest: storage.SyncRun{SourceName: "s", TenantID: "default", Outcome: "error", FinishedAt: seed}}}
	ch := &recordingChannel{name: "w"}
	n := NewNotifier([]Channel{ch}, nil, statuses)

	n.NotifySyncRun(context.Background(), run("s", "error", seed.Add(1*time.Hour))) // within 24h of seed: suppressed
	if got := ch.kinds(); len(got) != 0 {
		t.Fatalf("seeded-failing within cadence should not re-page; got %v", got)
	}
	n.NotifySyncRun(context.Background(), run("s", "error", seed.Add(25*time.Hour))) // >= 24h since seed: reminder
	if got := ch.kinds(); !equalKinds(got, []TransitionKind{KindReminder}) {
		t.Fatalf("kinds = %v, want [reminder]", got)
	}
}

func TestNotifierHealthySuccessSendsNothing(t *testing.T) {
	ch := &recordingChannel{name: "w"}
	n := NewNotifier([]Channel{ch}, nil, nil)
	n.NotifySyncRun(context.Background(), run("s", "success", time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)))
	if got := ch.kinds(); len(got) != 0 {
		t.Fatalf("a previously-healthy success should send nothing; got %v", got)
	}
}

func TestNotifierChannelErrorIsolatedAndLogged(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	bad := &recordingChannel{name: "bad", err: errors.New("channel down")}
	good := &recordingChannel{name: "good"}
	n := NewNotifier([]Channel{bad, good}, logger, nil)

	// NotifySyncRun returns normally (no panic, no error propagated).
	n.NotifySyncRun(context.Background(), run("s", "error", time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)))

	if len(bad.sent) != 1 {
		t.Fatalf("first channel not attempted: %d sends", len(bad.sent))
	}
	if len(good.sent) != 1 {
		t.Fatalf("second channel did not receive the alert after the first errored: %d sends", len(good.sent))
	}
	if !bytes.Contains(buf.Bytes(), []byte("bad")) {
		t.Errorf("channel error was not logged: %s", buf.String())
	}
}

func TestNotifierNoChannelsIsNoop(t *testing.T) {
	n := NewNotifier(nil, nil, nil)
	// Must not panic and must return normally.
	n.NotifySyncRun(context.Background(), run("s", "error", time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)))
}

func TestNotifierWithWebhookPostsOnceForFailureNothingForHealthySuccess(t *testing.T) {
	var mu sync.Mutex
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		posts++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	channel, err := NewWebhookChannel("ops", srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	n := NewNotifier([]Channel{channel}, nil, nil)
	base := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	n.NotifySyncRun(context.Background(), run("failing-source", "error", base))
	n.NotifySyncRun(context.Background(), run("healthy-source", "success", base))

	mu.Lock()
	defer mu.Unlock()
	if posts != 1 {
		t.Fatalf("expected exactly one POST (the failure only), got %d", posts)
	}
}
