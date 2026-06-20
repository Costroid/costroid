//! `costroid-power` — the local-inference economics engine (the headline differentiator, §3.1.B).
//!
//! This crate owns the **dual-mode** power/cost model: it turns a local model run (token counts +
//! real or estimated power draw integrated over wall-clock time) into **joules/token**, **Wh per
//! 1M tokens**, and **$ per 1M tokens**, with the measurement source stamped on every figure (R6).
//!
//! # Status — M3a in progress (built on the M0 scaffold)
//! In place and green on every target: the [`PowerSampler`] abstraction (§6.3) with its **four**
//! sources and the wall-meter-led runtime selector, the [`MeasurementMode`] stamp (§6.4), and
//! the [`cost`] model (§3.2) with deterministic worked-example tests. **Deferred to M3b
//! (human-gated, on real hardware):** the live sysfs read confirmation on the gfx1151 APU, the
//! LibreHardwareMonitor live loopback read (M3a ships its parser seam), and a captured
//! joules/token figure — no real power number is ever fabricated in code (R10).
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
pub mod models;
pub mod profile;
mod sampler;

pub use cost::{cloud_cost, local_run_cost, CostInputs, LocalRunCost};
pub use error::PowerError;
pub use mode::MeasurementMode;
pub use models::{bundled_models, ModelManifest, ModelQuality, ModelSpec, GEMMA4_MANIFEST_AS_OF};
pub use profile::{
    bundled_power_profiles, ElectricityRate, HardwareProfile, PowerProfiles, ProfileOverrides,
    ResolvedProfile, DEFAULT_HARDWARE_PROFILE_ID, POWER_PROFILE_AS_OF,
};
pub use sampler::{
    select_sampler, EstimatedPowerSampler, PowerSampler, SysfsPowerSampler, WallMeterPowerSampler,
    WindowsLhmPowerSampler,
};
