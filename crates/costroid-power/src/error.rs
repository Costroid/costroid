//! Typed errors for the local-inference economics crate.
//!
//! Per the repo invariant (CLAUDE.md golden rules + the ⚑ Readiness gate, D) there is **no
//! `unwrap`/`expect`/`panic!`** in this crate's library code: every failure mode — an
//! unreadable/absent sensor, a zero-duration or zero-token run, an invalid hardware profile —
//! is surfaced as a [`PowerError`] variant the caller can handle and the UI can stamp.

use thiserror::Error;

/// Anything that can go wrong while sampling power or computing local-inference economics.
#[derive(Debug, Error)]
pub enum PowerError {
    /// The selected power source is not available on this host (e.g. the amdgpu hwmon
    /// `power1_average` node is absent under WSL2, or the `power` feature is off). The
    /// runtime selector treats this as "fall through to the next source", never a crash.
    #[error("power sensor unavailable: {0}")]
    SensorUnavailable(String),

    /// A sensor node existed but could not be read or parsed (I/O error, non-integer
    /// microwatt value, transient permission failure).
    #[error("failed to read power sensor at {path}: {reason}")]
    SensorRead { path: String, reason: String },

    /// Energy/cost math was asked to divide by a zero (or negative) run duration. A run must
    /// have positive wall-clock seconds to integrate power into energy.
    #[error("run duration must be positive, got {0} s")]
    NonPositiveDuration(f64),

    /// Per-token economics were requested for a run that produced zero tokens — the
    /// `$/token` figure is undefined, so the caller must handle it rather than emit ∞/NaN.
    #[error("token count must be positive to compute per-token cost")]
    ZeroTokens,

    /// A hardware/power profile carried a non-physical value (negative price, zero lifetime,
    /// utilization outside (0, 1]).
    #[error("invalid hardware profile: {0}")]
    InvalidProfile(String),
}
