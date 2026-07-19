// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Costroid/costroid/internal/api"
	"github.com/Costroid/costroid/internal/storage"
)

const scheduledRunTimeout = time.Hour

type schedulerTimer interface {
	C() <-chan time.Time
	Stop() bool
}

type schedulerClock interface {
	Now() time.Time
	NewTimer(time.Duration) schedulerTimer
}

type realSchedulerClock struct{}

func (realSchedulerClock) Now() time.Time { return time.Now().UTC() }
func (realSchedulerClock) NewTimer(d time.Duration) schedulerTimer {
	return realSchedulerTimer{timer: time.NewTimer(d)}
}

type realSchedulerTimer struct{ timer *time.Timer }

func (t realSchedulerTimer) C() <-chan time.Time { return t.timer.C }
func (t realSchedulerTimer) Stop() bool          { return t.timer.Stop() }

type syncRunRecorder interface {
	RecordSyncRun(context.Context, storage.SyncRun) error
}

// syncAlerter is notified of every completed scheduled run. The implementation
// (alert.Notifier) owns the edge-triggered dedup state machine and decides
// whether to send; it never returns an error, so a broken alert channel can
// never affect the scheduler.
type syncAlerter interface {
	NotifySyncRun(context.Context, storage.SyncRun)
}

// noopAlerter is the default alerter: a serve without configured alert channels
// (and every scheduler test) uses it, so no alert is ever emitted unless serve
// sets a real Notifier after construction.
type noopAlerter struct{}

func (noopAlerter) NotifySyncRun(context.Context, storage.SyncRun) {}

// anomalyChecker scans for and alerts on new cost anomalies after a
// cost-changing run. The implementation (alert.AnomalyNotifier) owns its own
// persisted dedup and never returns an error, so a scan or send failure can
// never affect the scheduler.
type anomalyChecker interface {
	CheckAndNotify(ctx context.Context)
}

// noopAnomalyChecker is the default: a serve without opt-in anomaly alerting
// (and every scheduler test) uses it, so no anomaly scan runs unless serve sets
// a real alert.AnomalyNotifier after construction.
type noopAnomalyChecker struct{}

func (noopAnomalyChecker) CheckAndNotify(context.Context) {}

type scheduledRunResult struct {
	jobs         []ingestJobResult
	discoveryErr error
}

type scheduledSourceRunner func(context.Context, scheduledSource, ingestOutput) scheduledRunResult

type scheduledSourceState struct {
	source    scheduledSource
	nextRunAt time.Time
}

type ingestScheduler struct {
	clock    schedulerClock
	recorder syncRunRecorder
	runner   scheduledSourceRunner
	logger   *slog.Logger
	// alerter is notified of every completed run. It defaults to a no-op inside
	// newIngestScheduler; serve --sync sets a real alert.Notifier after
	// construction (before Start), so the constructor signature and every test
	// call site stay unchanged.
	alerter syncAlerter
	// anomalyChecker is invoked after a cost-changing run to alert on new cost
	// anomalies. It defaults to a no-op inside newIngestScheduler; serve --sync
	// sets a real alert.AnomalyNotifier after construction when anomaly alerting
	// is opted in, exactly like alerter.
	anomalyChecker anomalyChecker

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.RWMutex
	sources []scheduledSourceState
}

func newIngestScheduler(parent context.Context, clock schedulerClock, recorder syncRunRecorder, sources []scheduledSource, runner scheduledSourceRunner, logger *slog.Logger) *ingestScheduler {
	ctx, cancel := context.WithCancel(parent)
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	states := make([]scheduledSourceState, 0, len(sources))
	now := clock.Now().UTC()
	for _, source := range sources {
		states = append(states, scheduledSourceState{source: source, nextRunAt: now})
	}
	return &ingestScheduler{
		clock: clock, recorder: recorder, runner: runner, logger: logger,
		alerter:        noopAlerter{},
		anomalyChecker: noopAnomalyChecker{},
		ctx:            ctx, cancel: cancel, sources: states,
	}
}

func (s *ingestScheduler) Start() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.loop()
	}()
}

// Stop cancels any in-flight run and joins the scheduler goroutine. serve calls
// it before closing the shared DuckDB handle.
func (s *ingestScheduler) Stop() {
	s.cancel()
	s.wg.Wait()
}

// SyncSchedule implements api.SyncScheduleProvider.
func (s *ingestScheduler) SyncSchedule() []api.SyncScheduleSource {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]api.SyncScheduleSource, 0, len(s.sources))
	for _, state := range s.sources {
		result = append(result, api.SyncScheduleSource{
			Name: state.source.name, Connector: state.source.connector,
			Tenant: state.source.tenant, Interval: state.source.intervalText,
			NextRunAt: state.nextRunAt.UTC(),
		})
	}
	return result
}

func (s *ingestScheduler) loop() {
	for {
		if s.ctx.Err() != nil {
			return
		}
		index, wait := s.nextDue()
		if index >= 0 {
			s.run(index)
			continue
		}
		timer := s.clock.NewTimer(wait)
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-timer.C():
		}
	}
}

// nextDue selects the oldest due timestamp, using config order as the stable
// tie-breaker. This runs every source due at startup before an overdue source's
// coalesced rerun can overtake the rest of that startup cycle.
func (s *ingestScheduler) nextDue() (index int, wait time.Duration) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.ctx.Err() != nil {
		return -1, 0
	}
	now := s.clock.Now().UTC()
	selected := -1
	var earliest time.Time
	for i, state := range s.sources {
		if selected == -1 || state.nextRunAt.Before(earliest) {
			selected = i
			earliest = state.nextRunAt
		}
	}
	if selected == -1 {
		return -1, time.Hour
	}
	if !earliest.After(now) {
		return selected, 0
	}
	return -1, earliest.Sub(now)
}

func (s *ingestScheduler) run(index int) {
	started := s.clock.Now().UTC()
	s.mu.Lock()
	state := &s.sources[index]
	source := state.source
	state.nextRunAt = started.Add(source.interval)
	s.mu.Unlock()

	s.logger.Info("scheduled sync started", "source", source.name, "connector", source.connector, "tenant", source.tenant)
	output := ingestOutput{
		stdout: syncLogWriter{logger: s.logger, source: source, level: slog.LevelInfo},
		stderr: syncLogWriter{logger: s.logger, source: source, level: slog.LevelError},
	}

	runCtx, cancel := context.WithCancel(s.ctx)
	resultCh := make(chan scheduledRunResult, 1)
	go func() { resultCh <- s.runner(runCtx, source, output) }()
	timeUntilTimeout := started.Add(scheduledRunTimeout).Sub(s.clock.Now().UTC())
	timer := s.clock.NewTimer(timeUntilTimeout)
	var (
		result   scheduledRunResult
		timedOut bool
	)
	select {
	case result = <-resultCh:
		timer.Stop()
	case <-timer.C():
		// The runner may have delivered its result in the same instant the
		// timer fired (both channels ready, select picks randomly); prefer
		// the real result over a fabricated timeout.
		select {
		case result = <-resultCh:
			timer.Stop()
		default:
			timedOut = true
			cancel()
			result = <-resultCh
		}
	case <-s.ctx.Done():
		timer.Stop()
		cancel()
		result = <-resultCh
	}
	cancel()

	finished := s.clock.Now().UTC()
	run := summarizeScheduledRun(source, started, finished, result, timedOut)
	// Record with a non-cancellable context: a run that finishes (or is
	// interrupted) during shutdown must still leave its row, and the store
	// is guaranteed open here because serve closes it only after Stop joins
	// this goroutine.
	if err := s.recorder.RecordSyncRun(context.WithoutCancel(s.ctx), run); err != nil {
		s.logger.Error("recording scheduled sync", "source", source.name, "connector", source.connector, "error", err)
	}
	// Notify unconditionally: the alerter's state machine decides whether to
	// send, and it must observe successes too (the recovery transition). Uses
	// s.ctx (cancellable) so a Stop during shutdown cancels an in-flight send
	// rather than delaying shutdown; a best-effort alert dropped at shutdown is
	// fine because the run is durably recorded and re-seeded from SyncStatuses
	// on restart. NotifySyncRun never returns an error and is hard-bounded by a
	// per-send timeout, so a slow or broken endpoint can never crash or deadlock
	// the scheduler.
	s.alerter.NotifySyncRun(s.ctx, run)
	// After a run that actually changed cost data, scan for and alert on new
	// cost anomalies. A failed run or a skip-only run changed nothing, so no new
	// anomaly is possible; skipping avoids a wasted full-history scan. Uses s.ctx
	// (cancellable) so a Stop cancels an in-flight send. CheckAndNotify never
	// returns an error (a scan, insert, or send failure is swallowed and
	// logged), so it can never break the scheduler. No tenant is threaded from
	// run: a cost anomaly is a property of the DefaultTenant aggregate cost
	// history, which the notifier scans on its own.
	if (run.Outcome == "success" || run.Outcome == "partial") && run.RecordsIngested > 0 {
		s.anomalyChecker.CheckAndNotify(s.ctx)
	}
	finishAttrs := []any{
		"source", source.name, "connector", source.connector, "tenant", source.tenant,
		"outcome", run.Outcome, "duration", finished.Sub(started).String(),
		"periods_processed", run.PeriodsProcessed, "periods_skipped", run.PeriodsSkipped,
		"records_ingested", run.RecordsIngested,
	}
	if run.Error != "" {
		finishAttrs = append(finishAttrs, "error", run.Error)
	}
	s.logger.Info("scheduled sync finished", finishAttrs...)
}

func summarizeScheduledRun(source scheduledSource, started, finished time.Time, result scheduledRunResult, timedOut bool) storage.SyncRun {
	run := storage.SyncRun{
		SourceName: source.name, Connector: source.connector, TenantID: source.tenant,
		StartedAt: started, FinishedAt: finished,
	}
	if timedOut {
		run.Outcome = "error"
		run.Error = fmt.Sprintf("scheduled run timed out after %s", scheduledRunTimeout)
		return run
	}
	if result.discoveryErr != nil {
		run.Outcome = "error"
		run.Error = result.discoveryErr.Error()
		return run
	}
	var succeeded, failed int
	var failures []string
	for _, job := range result.jobs {
		if job.err != nil {
			failed++
			failures = append(failures, job.err.Error())
		} else {
			succeeded++
		}
		if job.skippedUnchanged && job.err == nil {
			run.PeriodsSkipped++
		} else if job.err == nil {
			run.PeriodsProcessed++
		}
		if !job.skippedUnchanged {
			run.RecordsIngested += int64(job.recordsIngested)
		}
	}
	switch {
	case failed == 0:
		run.Outcome = "success"
	case succeeded > 0:
		run.Outcome = "partial"
		run.Error = strings.Join(failures, "; ")
	default:
		run.Outcome = "error"
		run.Error = strings.Join(failures, "; ")
	}
	return run
}

func runScheduledSource(store *storage.DuckDB) scheduledSourceRunner {
	return func(ctx context.Context, source scheduledSource, output ingestOutput) scheduledRunResult {
		jobs, err := buildScheduledJobs(ctx, store, source, output)
		if err != nil {
			return scheduledRunResult{discoveryErr: err}
		}
		return scheduledRunResult{jobs: runIngestCore(ctx, store, jobs, source.tenant, output)}
	}
}

func buildScheduledJobs(ctx context.Context, store *storage.DuckDB, source scheduledSource, output ingestOutput) ([]ingestJob, error) {
	switch cfg := source.config.(type) {
	case awsFocusSource:
		return buildAWSFocusJobs(ctx, cfg, "", false, output)
	case awsFocusS3Source:
		return buildAWSFocusS3Jobs(ctx, store, cfg, "", false, output)
	case azureFocusSource:
		return buildAzureFocusJobs(ctx, store, cfg, "", false, output)
	case gcpFocusBQSource:
		return buildGCPFocusBQJobs(ctx, store, cfg, "", false, output)
	case anthropicCostSource:
		return buildAnthropicCostJobs(ctx, store, cfg, "", false, output)
	case openAICostSource:
		return buildOpenAICostJobs(ctx, store, cfg, "", false, output)
	case focusCSVSource:
		return buildFocusCSVJobs(ctx, cfg, "", false, output)
	default:
		return nil, fmt.Errorf("unsupported scheduled connector configuration %T", source.config)
	}
}

type syncLogWriter struct {
	logger *slog.Logger
	source scheduledSource
	level  slog.Level
}

func (w syncLogWriter) Write(p []byte) (int, error) {
	message := strings.TrimSuffix(string(p), "\n")
	if message != "" {
		w.logger.Log(context.Background(), w.level, "scheduled sync detail",
			"source", w.source.name, "connector", w.source.connector,
			"tenant", w.source.tenant, "message", message)
	}
	return len(p), nil
}

var _ api.SyncScheduleProvider = (*ingestScheduler)(nil)
