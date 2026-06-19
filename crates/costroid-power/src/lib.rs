//! `costroid-power` — the local-inference economics engine (the headline differentiator, §3.1.B).
//!
//! This crate owns the **dual-mode** power/cost model: it turns a local model run (token counts +
//! real or estimated power draw integrated over wall-clock time) into **joules/token**, **Wh per
//! 1M tokens**, and **$ per 1M tokens**, with the measurement source stamped on every figure (R6).
//!
//! # Status — M0 scaffold (do not mistake for the M3 engine)
//! In place and green on every target: the [`PowerSampler`] abstraction (§6.3) with its three
//! sources and runtime selector, the [`MeasurementMode`] stamp (§6.4), and the [`cost`] model
//! (§3.2) with deterministic worked-example tests. **Deferred to M3:** real sysfs node discovery,
//! runtime capability diagnostics, the subprocess inference runner (llama.cpp/Ollama — A2), the
//! benchmark harness, and the FOCUS-record mapping (where this meets `costroid-focus`). The
//! on-hardware sysfs confirmation (a captured joules/token figure on the gfx1151 APU) is the
//! **M3b human handoff** — no real power number is ever fabricated in code (R10).
//!
//! # Invariants honored here
//! - **No `unwrap`/`expect`/`panic!`** in library code; every failure is a [`PowerError`].
//! - **Gated by `#[cfg(target_os = "linux")]` + the off-by-default `power` feature** (R1, named
//!   `power` not `telemetry`): non-Linux / feature-off builds compile a clean "unavailable"
//!   path, never a broken build, and the default workspace build never links a power path.
//! - **No network, no GPU FFI:** the inference runner is a *subprocess* (A2), so this crate
//!   pulls no HTTP client and (with `unsafe_code = "forbid"` workspace-wide) no `unsafe` bindings.

pub mod cost;
mod error;
mod mode;
mod sampler;

pub use cost::{cloud_cost, local_run_cost, CostInputs, LocalRunCost};
pub use error::PowerError;
pub use mode::MeasurementMode;
pub use sampler::{
    select_sampler, EstimatedPowerSampler, PowerSampler, SysfsPowerSampler, WallMeterPowerSampler,
};
