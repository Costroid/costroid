//! The `PowerSampler` abstraction (§6.3) — three sources behind one trait, plus the runtime
//! selector that satisfies R1 ("keep every feature; degrade gracefully at runtime").
//!
//! **M0 scaffold.** The trait, the three implementations, the runtime selector, and the
//! cfg/feature gating are in place and build on every target; the *full* sysfs probing logic,
//! the inference runner, and the benchmark harness land at **M3** (and the on-hardware sysfs
//! confirmation is the **M3b** human handoff). No real power number is ever fabricated here.

use crate::error::PowerError;
use crate::mode::MeasurementMode;

/// A source of instantaneous power draw, in watts.
///
/// Implementations are cheap to construct; the real work is in [`PowerSampler::probe`] (is this
/// source usable on this host, right now?) and [`PowerSampler::sample_watts`] (read it). The
/// selector calls `probe` to choose a source and then samples it repeatedly over a run.
pub trait PowerSampler {
    /// Which measurement mode this sampler represents — stamped onto every record (R6).
    fn mode(&self) -> MeasurementMode;

    /// Whether this source can actually be read on this host *now*. Must never panic; a
    /// missing/unreadable sensor returns `false` (not an error), so the selector can fall
    /// through to the next source. This is what lets the sysfs path self-disable transparently
    /// under WSL2 instead of failing the build or the run.
    fn probe(&self) -> bool;

    /// Read instantaneous power draw in watts. Returns [`PowerError::SensorUnavailable`] /
    /// [`PowerError::SensorRead`] rather than panicking when the source cannot be read.
    fn sample_watts(&self) -> Result<f64, PowerError>;
}

// ============================================================================================
// SysfsPowerSampler — amdgpu hwmon `power1_average` (native Linux + `power` feature only)
// ============================================================================================

/// Reads `power1_average` from an amdgpu hwmon node (µW → W). Real readings are compiled in
/// **only** under `#[cfg(all(target_os = "linux", feature = "power"))]`; on every other target,
/// and whenever the `power` feature is off, it compiles to a stub whose `probe()` is `false`
/// and whose `sample_watts()` returns [`PowerError::SensorUnavailable`] — so the type always
/// exists, the build is green everywhere, and no command is ever removed (R1).
#[derive(Debug, Clone)]
pub struct SysfsPowerSampler {
    /// The hwmon node, e.g. `/sys/class/drm/card0/device/hwmon/hwmon3/power1_average`.
    /// Resolved by discovery at M3; carried here so the scaffold's shape is final.
    pub node_path: String,
}

impl SysfsPowerSampler {
    /// Construct a sampler bound to a specific hwmon `power1_average` node path. Discovery of
    /// the correct node (`/sys/class/drm/card*/device/hwmon/hwmon*/power1_average`, §5.4) is an
    /// M3 task; this constructor takes the resolved path so callers and tests are stable now.
    pub fn new(node_path: impl Into<String>) -> Self {
        Self {
            node_path: node_path.into(),
        }
    }
}

impl PowerSampler for SysfsPowerSampler {
    fn mode(&self) -> MeasurementMode {
        MeasurementMode::MeasuredSysfs
    }

    #[cfg(all(target_os = "linux", feature = "power"))]
    fn probe(&self) -> bool {
        // Real probe: the node must exist AND parse as an integer microwatt value. (Full
        // runtime capability detection — multiple cards, permission diagnostics — is M3.)
        self.sample_watts().is_ok()
    }

    #[cfg(not(all(target_os = "linux", feature = "power")))]
    fn probe(&self) -> bool {
        // Stub on non-Linux / `power` off: the native driver isn't bound (e.g. WSL2), so this
        // source self-disables and the selector falls through to wall-meter/estimated.
        false
    }

    #[cfg(all(target_os = "linux", feature = "power"))]
    fn sample_watts(&self) -> Result<f64, PowerError> {
        let raw = std::fs::read_to_string(&self.node_path).map_err(|e| PowerError::SensorRead {
            path: self.node_path.clone(),
            reason: e.to_string(),
        })?;
        let micro_watts: f64 = raw.trim().parse().map_err(|_| PowerError::SensorRead {
            path: self.node_path.clone(),
            reason: format!("expected integer microwatts, got {:?}", raw.trim()),
        })?;
        Ok(micro_watts / 1_000_000.0)
    }

    #[cfg(not(all(target_os = "linux", feature = "power")))]
    fn sample_watts(&self) -> Result<f64, PowerError> {
        Err(PowerError::SensorUnavailable(format!(
            "amdgpu hwmon sysfs power source is unavailable on this build/target \
             (path {}); the `power` feature is off or this is not native Linux",
            self.node_path
        )))
    }
}

// ============================================================================================
// WallMeterPowerSampler — true total-system draw from an external meter (every OS)
// ============================================================================================

/// True total-system power from a user-configured external meter. The M0 scaffold models the
/// simplest source — a constant manual value (watts) the user supplies; the CSV/log-feed and
/// smart-plug-local-API variants (still blocking I/O, never an async HTTP client — D) land at
/// M3. Works on every OS, so measured energy stays available from WSL2/Windows.
#[derive(Debug, Clone)]
pub struct WallMeterPowerSampler {
    watts: f64,
}

impl WallMeterPowerSampler {
    /// A constant wall-meter reading in watts (manual entry). Rejects non-positive values.
    pub fn constant(watts: f64) -> Result<Self, PowerError> {
        if watts > 0.0 {
            Ok(Self { watts })
        } else {
            Err(PowerError::InvalidProfile(format!(
                "wall-meter watts must be positive, got {watts}"
            )))
        }
    }
}

impl PowerSampler for WallMeterPowerSampler {
    fn mode(&self) -> MeasurementMode {
        MeasurementMode::MeasuredWallmeter
    }

    fn probe(&self) -> bool {
        self.watts > 0.0
    }

    fn sample_watts(&self) -> Result<f64, PowerError> {
        Ok(self.watts)
    }
}

// ============================================================================================
// EstimatedPowerSampler — transparent hardware/power profile (every OS; universal fallback)
// ============================================================================================

/// Derives power from a transparent profile: load draw scaled by utilization. The universal
/// fallback, always available, and the basis for what-if scenarios (D/§3.1).
#[derive(Debug, Clone)]
pub struct EstimatedPowerSampler {
    load_watts: f64,
    utilization: f64,
}

impl EstimatedPowerSampler {
    /// Build from a load-power figure (watts at full inference load) and a utilization fraction
    /// in `(0, 1]`. Both are stamped as assumptions on the resulting record (R6/R8).
    pub fn new(load_watts: f64, utilization: f64) -> Result<Self, PowerError> {
        if load_watts <= 0.0 {
            return Err(PowerError::InvalidProfile(format!(
                "load_watts must be positive, got {load_watts}"
            )));
        }
        if !(utilization > 0.0 && utilization <= 1.0) {
            return Err(PowerError::InvalidProfile(format!(
                "utilization must be in (0, 1], got {utilization}"
            )));
        }
        Ok(Self {
            load_watts,
            utilization,
        })
    }
}

impl PowerSampler for EstimatedPowerSampler {
    fn mode(&self) -> MeasurementMode {
        MeasurementMode::Estimated
    }

    fn probe(&self) -> bool {
        true
    }

    fn sample_watts(&self) -> Result<f64, PowerError> {
        Ok(self.load_watts * self.utilization)
    }
}

// ============================================================================================
// Runtime selector — sysfs if present → else wall meter if configured → else estimated
// ============================================================================================

/// Choose the most-authoritative usable power source (§6.3): a probed sysfs sampler wins; else a
/// configured wall meter; else the estimated fallback (which is always available). The selected
/// sampler's [`PowerSampler::mode`] is what gets stamped onto every record.
///
/// Takes already-constructed candidates so the caller owns configuration/discovery (kept simple
/// for the scaffold); M3 adds full sysfs node discovery + diagnostics in front of this.
pub fn select_sampler(
    sysfs: Option<SysfsPowerSampler>,
    wall_meter: Option<WallMeterPowerSampler>,
    estimated: EstimatedPowerSampler,
) -> Box<dyn PowerSampler> {
    if let Some(s) = sysfs {
        if s.probe() {
            return Box::new(s);
        }
    }
    if let Some(w) = wall_meter {
        if w.probe() {
            return Box::new(w);
        }
    }
    Box::new(estimated)
}

#[cfg(test)]
mod tests {
    // Repo rule (CLAUDE.md): clippy denies `unwrap`/`expect` even in tests, so these use
    // `let-else { panic!(...) }` / `assert!` to fail loudly without the banned calls.
    use super::*;

    #[test]
    fn estimated_sampler_scales_load_by_utilization() {
        let Ok(s) = EstimatedPowerSampler::new(160.0, 0.5) else {
            panic!("160 W @ 0.5 util is a valid profile")
        };
        let Ok(watts) = s.sample_watts() else {
            panic!("estimated sampler always reads")
        };
        assert!((watts - 80.0).abs() < 1e-9);
        assert_eq!(s.mode(), MeasurementMode::Estimated);
        assert!(s.probe());
    }

    #[test]
    fn estimated_sampler_rejects_non_physical_profiles() {
        assert!(EstimatedPowerSampler::new(-1.0, 0.5).is_err());
        assert!(EstimatedPowerSampler::new(160.0, 0.0).is_err());
        assert!(EstimatedPowerSampler::new(160.0, 1.5).is_err());
    }

    #[test]
    fn wall_meter_is_available_on_every_os() {
        let Ok(w) = WallMeterPowerSampler::constant(174.0) else {
            panic!("174 W is a positive reading")
        };
        assert!(w.probe());
        assert_eq!(w.mode(), MeasurementMode::MeasuredWallmeter);
        assert!(WallMeterPowerSampler::constant(0.0).is_err());
    }

    #[test]
    fn sysfs_self_disables_when_unavailable_and_selector_falls_through() {
        // On the default build (no `power` feature) and on every non-Linux target, the sysfs
        // sampler probes false, so the selector must fall through to the wall meter.
        let sysfs =
            SysfsPowerSampler::new("/sys/class/drm/card0/device/hwmon/hwmon0/power1_average");
        let (Ok(wall), Ok(est)) = (
            WallMeterPowerSampler::constant(174.0),
            EstimatedPowerSampler::new(160.0, 1.0),
        ) else {
            panic!("both fallback samplers have valid inputs")
        };
        let chosen = select_sampler(Some(sysfs), Some(wall), est);
        // Without the `power` feature the sysfs probe is false → wall meter wins.
        #[cfg(not(all(target_os = "linux", feature = "power")))]
        assert_eq!(chosen.mode(), MeasurementMode::MeasuredWallmeter);
        // With the feature on a real node may or may not exist; either way a mode is chosen.
        let _ = chosen.sample_watts();
    }

    #[test]
    fn selector_falls_all_the_way_to_estimated() {
        let Ok(est) = EstimatedPowerSampler::new(160.0, 1.0) else {
            panic!("valid profile")
        };
        let chosen = select_sampler(None, None, est);
        assert_eq!(chosen.mode(), MeasurementMode::Estimated);
        assert!(chosen.sample_watts().is_ok());
    }
}
