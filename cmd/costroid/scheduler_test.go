// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/storage"
)

type fakeSchedulerClock struct {
	mu      sync.Mutex
	now     time.Time
	timers  []*fakeSchedulerTimer
	created chan time.Duration
}

type fakeSchedulerTimer struct {
	clock    *fakeSchedulerClock
	deadline time.Time
	channel  chan time.Time
	active   bool
}

func newFakeSchedulerClock(now time.Time) *fakeSchedulerClock {
	return &fakeSchedulerClock{now: now.UTC(), created: make(chan time.Duration, 128)}
}

func (c *fakeSchedulerClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeSchedulerClock) NewTimer(duration time.Duration) schedulerTimer {
	c.mu.Lock()
	timer := &fakeSchedulerTimer{
		clock: c, deadline: c.now.Add(duration), channel: make(chan time.Time, 1), active: true,
	}
	c.timers = append(c.timers, timer)
	if duration <= 0 {
		timer.active = false
		timer.channel <- c.now
	}
	c.mu.Unlock()
	c.created <- duration
	return timer
}

func (c *fakeSchedulerClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	now := c.now
	for _, timer := range c.timers {
		if timer.active && !timer.deadline.After(now) {
			timer.active = false
			timer.channel <- now
		}
	}
	c.mu.Unlock()
}

func (t *fakeSchedulerTimer) C() <-chan time.Time { return t.channel }

func (t *fakeSchedulerTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	wasActive := t.active
	t.active = false
	return wasActive
}

type syncRunMemoryRecorder struct {
	mu            sync.Mutex
	runs          []storage.SyncRun
	recordCtxErrs []error
	recorded      chan storage.SyncRun
	closed        bool
	useAfterClose bool
}

func newSyncRunMemoryRecorder() *syncRunMemoryRecorder {
	return &syncRunMemoryRecorder{recorded: make(chan storage.SyncRun, 128)}
}

func (r *syncRunMemoryRecorder) RecordSyncRun(ctx context.Context, run storage.SyncRun) error {
	r.mu.Lock()
	if r.closed {
		r.useAfterClose = true
		r.mu.Unlock()
		return errors.New("recorder closed")
	}
	r.runs = append(r.runs, run)
	// The real store rejects work on a cancelled context, so tests assert
	// the scheduler always records with a live one.
	r.recordCtxErrs = append(r.recordCtxErrs, ctx.Err())
	r.mu.Unlock()
	r.recorded <- run
	return nil
}

func (r *syncRunMemoryRecorder) close() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
}

func testScheduledSource(name string, interval time.Duration) scheduledSource {
	return scheduledSource{
		name: name, connector: "probe", tenant: "default",
		interval: interval, intervalText: interval.String(),
	}
}

func waitForFakeTimer(t *testing.T, clock *fakeSchedulerClock, duration time.Duration) {
	t.Helper()
	for {
		if got := <-clock.created; got == duration {
			return
		}
	}
}

func successfulScheduledResult() scheduledRunResult {
	return scheduledRunResult{jobs: []ingestJobResult{{outcome: "success", recordsIngested: 1}}}
}

func TestSchedulerImmediateSerialAndFailureIsolation(t *testing.T) {
	clock := newFakeSchedulerClock(time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC))
	recorder := newSyncRunMemoryRecorder()
	var mu sync.Mutex
	active, maxActive := 0, 0
	order := []string{}
	runner := func(_ context.Context, source scheduledSource, _ ingestOutput) scheduledRunResult {
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		order = append(order, source.name)
		mu.Unlock()
		result := successfulScheduledResult()
		if source.name == "a" {
			result = scheduledRunResult{discoveryErr: errors.New("a failed discovery")}
		}
		mu.Lock()
		active--
		mu.Unlock()
		return result
	}
	scheduler := newIngestScheduler(context.Background(), clock, recorder, []scheduledSource{
		testScheduledSource("a", 24*time.Hour), testScheduledSource("b", 24*time.Hour),
	}, runner, nil)
	scheduler.Start()
	t.Cleanup(scheduler.Stop)
	first, second := <-recorder.recorded, <-recorder.recorded
	if first.SourceName != "a" || first.Outcome != "error" || second.SourceName != "b" || second.Outcome != "success" {
		t.Fatalf("immediate runs = %+v then %+v", first, second)
	}
	mu.Lock()
	defer mu.Unlock()
	if maxActive != 1 || fmt.Sprint(order) != "[a b]" {
		t.Fatalf("max active = %d, order = %v", maxActive, order)
	}
}

func TestSchedulerRerunsAfterInterval(t *testing.T) {
	clock := newFakeSchedulerClock(time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC))
	recorder := newSyncRunMemoryRecorder()
	starts := make(chan struct{}, 4)
	runner := func(context.Context, scheduledSource, ingestOutput) scheduledRunResult {
		starts <- struct{}{}
		return successfulScheduledResult()
	}
	scheduler := newIngestScheduler(context.Background(), clock, recorder,
		[]scheduledSource{testScheduledSource("source", 30*time.Minute)}, runner, nil)
	scheduler.Start()
	t.Cleanup(scheduler.Stop)
	<-starts
	<-recorder.recorded
	waitForFakeTimer(t, clock, 30*time.Minute)
	clock.Advance(29 * time.Minute)
	select {
	case <-starts:
		t.Fatal("source reran before its interval elapsed")
	default:
	}
	clock.Advance(time.Minute)
	<-starts
	<-recorder.recorded
}

func TestSchedulerTimeoutUsesFakeClock(t *testing.T) {
	clock := newFakeSchedulerClock(time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC))
	recorder := newSyncRunMemoryRecorder()
	started := make(chan struct{})
	cancelled := make(chan struct{})
	runner := func(ctx context.Context, _ scheduledSource, _ ingestOutput) scheduledRunResult {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return scheduledRunResult{discoveryErr: ctx.Err()}
	}
	scheduler := newIngestScheduler(context.Background(), clock, recorder,
		[]scheduledSource{testScheduledSource("slow", 24*time.Hour)}, runner, nil)
	scheduler.Start()
	t.Cleanup(scheduler.Stop)
	<-started
	waitForFakeTimer(t, clock, scheduledRunTimeout)
	clock.Advance(scheduledRunTimeout + time.Nanosecond)
	<-cancelled
	run := <-recorder.recorded
	if run.Outcome != "error" || !strings.Contains(run.Error, "timed out after 1h0m0s") {
		t.Fatalf("timeout run = %+v", run)
	}
}

// TestSchedulerShutdownStillRecordsInterruptedRun pins that a run in flight
// when Stop cancels the scheduler still leaves its sync_runs row, recorded
// with a non-cancelled context (the real store rejects work on a cancelled
// context, which would silently drop the record).
func TestSchedulerShutdownStillRecordsInterruptedRun(t *testing.T) {
	clock := newFakeSchedulerClock(time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC))
	recorder := newSyncRunMemoryRecorder()
	started := make(chan struct{})
	runner := func(ctx context.Context, _ scheduledSource, _ ingestOutput) scheduledRunResult {
		close(started)
		<-ctx.Done()
		return scheduledRunResult{discoveryErr: ctx.Err()}
	}
	scheduler := newIngestScheduler(context.Background(), clock, recorder,
		[]scheduledSource{testScheduledSource("interrupted", 24*time.Hour)}, runner, nil)
	scheduler.Start()
	<-started
	scheduler.Stop()
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.runs) != 1 {
		t.Fatalf("recorded runs = %d, want 1 (the interrupted attempt)", len(recorder.runs))
	}
	if recorder.runs[0].Outcome != "error" {
		t.Fatalf("interrupted run outcome = %q, want error", recorder.runs[0].Outcome)
	}
	if recorder.recordCtxErrs[0] != nil {
		t.Fatalf("recording context was cancelled (%v); the real store would drop the row", recorder.recordCtxErrs[0])
	}
}

func TestSchedulerRecordsPartialOutcome(t *testing.T) {
	clock := newFakeSchedulerClock(time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC))
	recorder := newSyncRunMemoryRecorder()
	runner := func(context.Context, scheduledSource, ingestOutput) scheduledRunResult {
		return scheduledRunResult{jobs: []ingestJobResult{
			{period: "2026-05", outcome: "success", recordsIngested: 3},
			{period: "2026-06", outcome: "error", err: errors.New("June failed")},
		}}
	}
	scheduler := newIngestScheduler(context.Background(), clock, recorder,
		[]scheduledSource{testScheduledSource("mixed", 24*time.Hour)}, runner, nil)
	scheduler.Start()
	t.Cleanup(scheduler.Stop)
	run := <-recorder.recorded
	if run.Outcome != "partial" || run.PeriodsProcessed != 1 || run.RecordsIngested != 3 || !strings.Contains(run.Error, "June failed") {
		t.Fatalf("partial run = %+v", run)
	}
}

func TestSchedulerShutdownCancelsAndJoinsBeforeClose(t *testing.T) {
	clock := newFakeSchedulerClock(time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC))
	recorder := newSyncRunMemoryRecorder()
	started := make(chan struct{})
	exited := make(chan struct{})
	runner := func(ctx context.Context, _ scheduledSource, _ ingestOutput) scheduledRunResult {
		close(started)
		<-ctx.Done()
		close(exited)
		return scheduledRunResult{discoveryErr: ctx.Err()}
	}
	scheduler := newIngestScheduler(context.Background(), clock, recorder,
		[]scheduledSource{testScheduledSource("source", time.Hour)}, runner, nil)
	scheduler.Start()
	<-started
	scheduler.Stop()
	select {
	case <-exited:
	default:
		t.Fatal("Stop returned before the in-flight runner exited")
	}
	recorder.close()
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.useAfterClose {
		t.Fatal("scheduler used the recorder after close")
	}
}

func TestSchedulerCoalescesMissedTicks(t *testing.T) {
	clock := newFakeSchedulerClock(time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC))
	recorder := newSyncRunMemoryRecorder()
	starts := make(chan int, 4)
	releases := make(chan struct{}, 4)
	var mu sync.Mutex
	count := 0
	runner := func(context.Context, scheduledSource, ingestOutput) scheduledRunResult {
		mu.Lock()
		count++
		current := count
		mu.Unlock()
		starts <- current
		<-releases
		return successfulScheduledResult()
	}
	scheduler := newIngestScheduler(context.Background(), clock, recorder,
		[]scheduledSource{testScheduledSource("source", 30*time.Minute)}, runner, nil)
	scheduler.Start()
	t.Cleanup(scheduler.Stop)
	if got := <-starts; got != 1 {
		t.Fatalf("first run number = %d", got)
	}
	waitForFakeTimer(t, clock, scheduledRunTimeout)
	clock.Advance(45 * time.Minute)
	releases <- struct{}{}
	<-recorder.recorded
	if got := <-starts; got != 2 {
		t.Fatalf("coalesced run number = %d", got)
	}
	releases <- struct{}{}
	<-recorder.recorded
	waitForFakeTimer(t, clock, 30*time.Minute)
	select {
	case got := <-starts:
		t.Fatalf("missed ticks produced extra immediate run %d", got)
	default:
	}
}

type recordingAlerter struct {
	ch chan storage.SyncRun
}

func (a recordingAlerter) NotifySyncRun(_ context.Context, run storage.SyncRun) {
	a.ch <- run
}

// TestSchedulerNotifiesAlerterForEveryRun pins that run() hands EVERY completed
// run (success and failure alike) to the injected alerter after recording it.
// The recovery transition depends on the alerter observing successes, so the
// hook is deliberately unconditional; removing it makes this test time out.
func TestSchedulerNotifiesAlerterForEveryRun(t *testing.T) {
	clock := newFakeSchedulerClock(time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC))
	recorder := newSyncRunMemoryRecorder()
	runner := func(_ context.Context, source scheduledSource, _ ingestOutput) scheduledRunResult {
		if source.name == "b" {
			return scheduledRunResult{discoveryErr: errors.New("b failed discovery")}
		}
		return successfulScheduledResult()
	}
	scheduler := newIngestScheduler(context.Background(), clock, recorder,
		[]scheduledSource{testScheduledSource("a", 24*time.Hour), testScheduledSource("b", 24*time.Hour)}, runner, nil)
	alerter := recordingAlerter{ch: make(chan storage.SyncRun, 8)}
	scheduler.alerter = alerter
	scheduler.Start()
	t.Cleanup(scheduler.Stop)

	recv := func() storage.SyncRun {
		t.Helper()
		select {
		case run := <-alerter.ch:
			return run
		case <-time.After(5 * time.Second):
			t.Fatal("scheduler did not notify the alerter")
			return storage.SyncRun{}
		}
	}
	outcomes := map[string]string{}
	for i := 0; i < 2; i++ {
		run := recv()
		outcomes[run.SourceName] = run.Outcome
	}
	if outcomes["a"] != "success" || outcomes["b"] != "error" {
		t.Fatalf("alerter observed = %v, want a=success b=error", outcomes)
	}
}
