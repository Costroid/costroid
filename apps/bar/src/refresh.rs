//! The off-UI-thread snapshot refresh loop and the UI-side refresh state machine.
//!
//! `collect_local_snapshot` is synchronous local-file I/O; running it on the egui frame
//! would hitch the UI, so it runs on a dedicated **worker thread** that hands a fresh
//! `EngineSnapshot` + `NowSummary` back over a channel (STEP6-TASKBAR-DESIGN §8). The
//! worker is a pure executor — it refreshes on request only; the *cadence* (the ~30 s
//! timer + window-show + manual ⟳) is the UI's, decided by the pure `due_for_refresh`
//! predicate, so the timing is deterministic and unit-testable.

use std::sync::mpsc::{self, Receiver, Sender};
use std::thread::JoinHandle;
use std::time::{Duration, Instant};

use costroid_core::{
    collect_local_snapshot, now_summary, EngineSnapshot, HostEnv, NowOptions, NowSummary,
};

/// Background refresh cadence — slow and battery-friendly, since the bar is always-on
/// (far slower than the TUI's 2 s `--live`); STEP6-TASKBAR-DESIGN §8.
pub const REFRESH_INTERVAL: Duration = Duration::from_secs(30);

/// A successful collection: the snapshot and its derived now-summary. One snapshot fans
/// out to every panel, exactly as the TUI does.
pub struct Loaded {
    pub snapshot: EngineSnapshot,
    pub summary: NowSummary,
}

/// The outcome of one refresh. `Err` carries a short, user-facing reason so a failed
/// collect degrades to a visible status, never a panic (STEP6-TASKBAR-DESIGN §8 / §11).
pub enum RefreshOutcome {
    Ok(Box<Loaded>),
    Err(String),
}

/// Whether the worker is currently collecting.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Phase {
    Idle,
    InFlight,
}

/// The UI-side refresh state: the in-flight phase, the last good load, the last error,
/// and when the last collect completed (for the auto-timer). Pure — no egui, no threads.
#[derive(Default)]
pub struct RefreshState {
    phase: PhaseCell,
    loaded: Option<Loaded>,
    error: Option<String>,
    last_completed: Option<Instant>,
}

// `Phase` has no `Default`; wrap it so `RefreshState` can derive `Default` as `Idle`.
struct PhaseCell(Phase);
impl Default for PhaseCell {
    fn default() -> Self {
        PhaseCell(Phase::Idle)
    }
}

impl RefreshState {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn phase(&self) -> Phase {
        self.phase.0
    }

    pub fn loaded(&self) -> Option<&Loaded> {
        self.loaded.as_ref()
    }

    pub fn error(&self) -> Option<&str> {
        self.error.as_deref()
    }

    pub fn has_data(&self) -> bool {
        self.loaded.is_some()
    }

    /// How long since the last completed collect, if any (drives `due_for_refresh`).
    pub fn since_last_completed(&self) -> Option<Duration> {
        self.last_completed.map(|t| t.elapsed())
    }

    /// Note that a refresh was just requested — the worker is now collecting.
    pub fn mark_requested(&mut self) {
        self.phase = PhaseCell(Phase::InFlight);
    }

    /// Apply a worker outcome: back to idle; a success replaces the data and clears the
    /// error; a failure keeps the last good data but records the error so the UI can show
    /// it. `completed_at` is normally `Instant::now()` (a parameter so the transition is
    /// testable without a clock).
    pub fn apply(&mut self, outcome: RefreshOutcome, completed_at: Instant) {
        self.phase = PhaseCell(Phase::Idle);
        self.last_completed = Some(completed_at);
        match outcome {
            RefreshOutcome::Ok(loaded) => {
                self.loaded = Some(*loaded);
                self.error = None;
            }
            RefreshOutcome::Err(reason) => {
                self.error = Some(reason);
            }
        }
    }
}

/// Whether a fresh refresh is due: only when idle, and only once `interval` has elapsed
/// since the last completion (or no collect has completed yet). Pure and deterministic.
pub fn due_for_refresh(phase: Phase, since_last: Option<Duration>, interval: Duration) -> bool {
    phase == Phase::Idle && since_last.is_none_or(|elapsed| elapsed >= interval)
}

/// A handle to the background refresh worker. Dropping it closes the request channel,
/// which ends the worker thread cleanly.
pub struct RefreshWorker {
    request_tx: Sender<()>,
    outcome_rx: Receiver<RefreshOutcome>,
    _handle: Option<JoinHandle<()>>,
}

impl RefreshWorker {
    /// Spawn the worker. It collects on every `request()` and calls
    /// `ctx.request_repaint()` when a result is ready so the UI wakes immediately. If the
    /// thread cannot be spawned (rare), the worker degrades to one queued error outcome
    /// rather than hanging.
    pub fn spawn(ctx: egui::Context) -> Self {
        let (request_tx, request_rx) = mpsc::channel::<()>();
        let (outcome_tx, outcome_rx) = mpsc::channel::<RefreshOutcome>();

        let worker_tx = outcome_tx.clone();
        let handle = std::thread::Builder::new()
            .name("costroid-bar-refresh".to_owned())
            .spawn(move || worker_loop(&request_rx, &worker_tx, &ctx));

        let handle = match handle {
            Ok(handle) => Some(handle),
            Err(err) => {
                let _ = outcome_tx.send(RefreshOutcome::Err(format!(
                    "could not start the refresh worker: {err}"
                )));
                None
            }
        };

        Self {
            request_tx,
            outcome_rx,
            _handle: handle,
        }
    }

    /// Ask the worker to refresh. A no-op if the worker has stopped.
    pub fn request(&self) {
        let _ = self.request_tx.send(());
    }

    /// Drain the next ready outcome, if any (non-blocking).
    pub fn poll(&self) -> Option<RefreshOutcome> {
        self.outcome_rx.try_recv().ok()
    }
}

fn worker_loop(
    request_rx: &Receiver<()>,
    outcome_tx: &Sender<RefreshOutcome>,
    ctx: &egui::Context,
) {
    let env = HostEnv::detect();
    // Block until a refresh is requested; `Err` means the UI dropped its sender → exit.
    while request_rx.recv().is_ok() {
        // Coalesce any extra requests that piled up while we were about to collect.
        while request_rx.try_recv().is_ok() {}
        let outcome = collect(&env);
        if outcome_tx.send(outcome).is_err() {
            break;
        }
        ctx.request_repaint();
    }
}

fn collect(env: &HostEnv) -> RefreshOutcome {
    match collect_local_snapshot(env) {
        Ok(snapshot) => {
            let summary = now_summary(&snapshot, NowOptions::default());
            RefreshOutcome::Ok(Box::new(Loaded { snapshot, summary }))
        }
        Err(err) => RefreshOutcome::Err(format!("could not read local data: {err}")),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn empty_loaded() -> Box<Loaded> {
        let snapshot = EngineSnapshot {
            generated_at: match chrono::DateTime::from_timestamp(1_900_000_000, 0) {
                Some(dt) => dt,
                None => panic!("invalid test timestamp"),
            },
            usage_events: Vec::new(),
            focus_rows: Vec::new(),
            limit_windows: Vec::new(),
            providers: Vec::new(),
            capabilities: Vec::new(),
        };
        let summary = now_summary(&snapshot, NowOptions::default());
        Box::new(Loaded { snapshot, summary })
    }

    #[test]
    fn due_for_refresh_only_when_idle_and_interval_elapsed() {
        let interval = Duration::from_secs(30);
        assert!(
            due_for_refresh(Phase::Idle, None, interval),
            "first refresh (nothing collected yet) is due"
        );
        assert!(!due_for_refresh(
            Phase::Idle,
            Some(Duration::from_secs(5)),
            interval
        ));
        assert!(due_for_refresh(
            Phase::Idle,
            Some(Duration::from_secs(30)),
            interval
        ));
        assert!(due_for_refresh(
            Phase::Idle,
            Some(Duration::from_secs(90)),
            interval
        ));
        assert!(
            !due_for_refresh(Phase::InFlight, None, interval),
            "never start a second collect while one is in flight"
        );
        assert!(!due_for_refresh(
            Phase::InFlight,
            Some(Duration::from_secs(120)),
            interval
        ));
    }

    #[test]
    fn new_state_is_idle_and_empty() {
        let state = RefreshState::new();
        assert_eq!(state.phase(), Phase::Idle);
        assert!(!state.has_data());
        assert!(state.error().is_none());
        assert!(state.since_last_completed().is_none());
    }

    #[test]
    fn request_then_success_clears_error_and_stores_data() {
        let mut state = RefreshState::new();
        state.mark_requested();
        assert_eq!(state.phase(), Phase::InFlight);

        state.apply(RefreshOutcome::Ok(empty_loaded()), Instant::now());
        assert_eq!(state.phase(), Phase::Idle);
        assert!(state.has_data());
        assert!(state.error().is_none());
        assert!(state.since_last_completed().is_some());
    }

    #[test]
    fn failure_keeps_last_good_data_and_records_error() {
        let mut state = RefreshState::new();
        state.apply(RefreshOutcome::Ok(empty_loaded()), Instant::now());
        assert!(state.has_data());

        state.mark_requested();
        state.apply(
            RefreshOutcome::Err("could not read local data: boom".to_owned()),
            Instant::now(),
        );
        assert_eq!(state.phase(), Phase::Idle);
        assert!(
            state.has_data(),
            "a failed refresh must not drop the last good data"
        );
        assert_eq!(state.error(), Some("could not read local data: boom"));
    }

    #[test]
    fn success_after_failure_clears_the_error() {
        let mut state = RefreshState::new();
        state.apply(RefreshOutcome::Err("boom".to_owned()), Instant::now());
        assert_eq!(state.error(), Some("boom"));
        state.apply(RefreshOutcome::Ok(empty_loaded()), Instant::now());
        assert!(state.error().is_none());
        assert!(state.has_data());
    }
}
