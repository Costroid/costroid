//! The `PowerSampler` abstraction (§6.3) — **four** sources behind one trait, plus the
//! wall-meter-led runtime selector that satisfies R1 ("keep every feature; degrade gracefully
//! at runtime").
//!
//! The four sources, along the revised (2026-06-20) wall-meter-led ladder (§5.3/§5.4):
//! [`WallMeterPowerSampler`] (true total-system draw, cross-OS, recommended),
//! [`SysfsPowerSampler`] (amdgpu `power1_average`, native-Linux on-chip),
//! [`WindowsLhmPowerSampler`] (LibreHardwareMonitor package sensor, Windows on-chip — the M3a
//! parser seam; live read at M3b), and [`EstimatedPowerSampler`] (the universal fallback).
//!
//! The full sysfs probing logic and the LHM live read land at **M3b** (the on-hardware
//! confirmation is the human handoff). No real power number is ever fabricated here (R10).

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
// WindowsLhmPowerSampler — LibreHardwareMonitor SMU Package sensor (Windows on-chip, optional)
// ============================================================================================

/// Reads the SMU **Package** power sensor from a running LibreHardwareMonitor's separate-process
/// JSON endpoint (`http://localhost:8085/data.json`). MPL-2.0 stays cleanly **out-of-process**
/// — no linking, no FFI, no .NET in our binary (§5.4) — and the read is a blocking
/// local-loopback read, not a network egress.
///
/// **M3a ships the parser seam only** ([`parse_package_watts`](Self::parse_package_watts) — a
/// pure JSON→watts function, tested against a committed fixture). The live loopback read is the
/// **M3b** field-verification on the actual 8060S (gated `#[cfg(all(target_os = "windows",
/// feature = "power"))]`, loopback-only, with its own offline-gate carve-out), so M3a carries
/// **zero** `TcpStream`/AF_INET code and the CLI stays byte-for-byte no-network. Until then
/// [`probe`](PowerSampler::probe) is `false` and [`sample_watts`](PowerSampler::sample_watts)
/// reports unavailable, so the selector falls through.
#[derive(Debug, Clone)]
pub struct WindowsLhmPowerSampler {
    /// The LibreHardwareMonitor data endpoint, e.g. `http://localhost:8085/data.json`.
    /// Carried so the scaffold's shape is final; the live read (M3b) uses it.
    pub endpoint: String,
}

impl WindowsLhmPowerSampler {
    /// Construct a sampler bound to an LHM JSON endpoint (default
    /// `http://localhost:8085/data.json`). The live read lands at M3b; this constructor takes
    /// the endpoint so callers and the selector are stable now.
    pub fn new(endpoint: impl Into<String>) -> Self {
        Self {
            endpoint: endpoint.into(),
        }
    }

    /// Parse the whole-APU **Package** power (watts) out of LibreHardwareMonitor's
    /// `/data.json` tree (M3a parser seam — the live loopback fetch is M3b). LHM nests sensors
    /// under `Children`; the SMU package-power leaf has `Text` containing "Package", `Type ==
    /// "Power"`, and a `Value` like `"62.5 W"`. Walks the tree, prefers a `Type == "Power"`
    /// node, and parses the watts. A typed [`PowerError`] if no package-power sensor is found —
    /// never a panic. (Disclose: this is whole-APU package power, not GPU-only — R6.)
    pub fn parse_package_watts(json: &str) -> Result<f64, PowerError> {
        let root: serde_json::Value =
            serde_json::from_str(json).map_err(|e| PowerError::SensorRead {
                path: "lhm-data.json".to_string(),
                reason: format!("LHM data.json failed to parse: {e}"),
            })?;
        find_package_watts(&root).ok_or_else(|| {
            PowerError::SensorUnavailable(
                "no Package power sensor (Type=Power, Value in watts) found in the LHM data.json \
                 tree"
                    .to_string(),
            )
        })
    }
}

/// Parse a LHM value string like `"62.5 W"` into watts (`62.5`). Returns `None` if it is not a
/// watt value (e.g. a temperature `"54.0 °C"`).
fn lhm_watts_value(value: &str) -> Option<f64> {
    let trimmed = value.trim();
    let num = trimmed
        .strip_suffix('W')
        .or_else(|| trimmed.strip_suffix('w'))?;
    num.trim().parse::<f64>().ok()
}

/// Recursively find the whole-APU **Package** power (watts) in an LHM `/data.json` node tree.
/// Prefers a node whose `Text` contains "Package" with a watt `Value` (and `Type == "Power"`
/// when the field is present), depth-first.
fn find_package_watts(node: &serde_json::Value) -> Option<f64> {
    if let Some(text) = node.get("Text").and_then(serde_json::Value::as_str) {
        if text.to_lowercase().contains("package") {
            // If a Type is present it must be "Power"; if absent, a watt-shaped Value suffices.
            let type_ok = match node.get("Type").and_then(serde_json::Value::as_str) {
                Some(t) => t.eq_ignore_ascii_case("power"),
                None => true,
            };
            if type_ok {
                if let Some(watts) = node
                    .get("Value")
                    .and_then(serde_json::Value::as_str)
                    .and_then(lhm_watts_value)
                {
                    return Some(watts);
                }
            }
        }
    }
    if let Some(children) = node.get("Children").and_then(serde_json::Value::as_array) {
        for child in children {
            if let Some(watts) = find_package_watts(child) {
                return Some(watts);
            }
        }
    }
    None
}

impl PowerSampler for WindowsLhmPowerSampler {
    fn mode(&self) -> MeasurementMode {
        MeasurementMode::MeasuredLhm
    }

    fn probe(&self) -> bool {
        // Parser-only seam in M3a: the live loopback read is the M3b field-verification, so this
        // source self-disables for now and the selector falls through to estimated.
        false
    }

    fn sample_watts(&self) -> Result<f64, PowerError> {
        Err(PowerError::SensorUnavailable(format!(
            "LibreHardwareMonitor live read is not built in M3a (parser-only seam; endpoint {}); \
             the localhost:8085 read is the M3b field-verification on the actual hardware",
            self.endpoint
        )))
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
// Runtime selector (D1, wall-meter-led) — wall meter if configured → else on-chip if available
// (sysfs on Linux / LibreHardwareMonitor on Windows) → else estimated
// ============================================================================================

/// Choose the active power source along the revised (2026-06-20) **wall-meter-led** ladder
/// (§5.3/§5.4, D1): a configured wall meter wins (it is the truest cross-OS figure); else the
/// on-chip package-grade source available on this host (`sysfs` on native Linux, then
/// LibreHardwareMonitor on Windows); else the estimated fallback (always available). The
/// selected sampler's [`PowerSampler::mode`] is what gets stamped onto every record.
///
/// This deliberately prefers a user's deliberately-configured true-draw meter over the less
/// honest on-chip *package* reading — reversing the M0 scaffold's sysfs-first order. Takes
/// already-constructed candidates so the caller owns configuration/discovery (kept simple for
/// the scaffold); M3 adds full sysfs node discovery + diagnostics in front of this.
pub fn select_sampler(
    wall_meter: Option<WallMeterPowerSampler>,
    sysfs: Option<SysfsPowerSampler>,
    lhm: Option<WindowsLhmPowerSampler>,
    estimated: EstimatedPowerSampler,
) -> Box<dyn PowerSampler> {
    if let Some(w) = wall_meter {
        if w.probe() {
            return Box::new(w);
        }
    }
    if let Some(s) = sysfs {
        if s.probe() {
            return Box::new(s);
        }
    }
    if let Some(l) = lhm {
        if l.probe() {
            return Box::new(l);
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
    fn lhm_seam_self_disables_until_the_m3b_live_read_lands() {
        // M3a ships the LHM parser seam only; `probe` is false so the selector falls through.
        let lhm = WindowsLhmPowerSampler::new("http://localhost:8085/data.json");
        assert!(!lhm.probe());
        assert_eq!(lhm.mode(), MeasurementMode::MeasuredLhm);
        assert!(lhm.sample_watts().is_err());
    }

    // The LHM JSON parser (T10) — exercised against a committed fixture (the live loopback read
    // is M3b). The fixture is a hardware-sensor tree, not user content (R4-safe).
    const LHM_DATA_JSON: &str = include_str!("../../../fixtures/local/lhm-data.json");

    #[test]
    fn parses_the_package_power_from_the_lhm_fixture() {
        let Ok(watts) = WindowsLhmPowerSampler::parse_package_watts(LHM_DATA_JSON) else {
            panic!("the committed LHM data.json fixture must yield a Package power");
        };
        // The CPU "Powers" group's Package sensor reads 62.5 W (whole-APU package power, R6) —
        // chosen over "CPU Cores" / "GPU SoC".
        assert!((watts - 62.5).abs() < 1e-9);
    }

    #[test]
    fn r4_the_lhm_fixture_carries_no_prompt_or_completion_content() {
        // The LHM fixture is a hardware-sensor tree by construction; guard it with the SAME
        // content-marker scan the stats fixtures use (runner.rs
        // `r4_the_stats_fixtures_carry_no_prompt_or_completion_content`) so a future real LHM
        // capture that accidentally embeds generated text can't slip in.
        let lower = LHM_DATA_JSON.to_lowercase();
        for forbidden in ["prompt:", "completion", "response:", "assistant:", "user:"] {
            assert!(
                !lower.contains(forbidden),
                "R4: the LHM fixture must carry no content marker `{forbidden}`"
            );
        }
    }

    #[test]
    fn lhm_parser_fails_closed_on_missing_or_malformed_data() {
        // No Package power sensor present → typed unavailable, never a panic.
        let no_pkg = r#"{"Text":"Sensor","Children":[{"Text":"CPU Cores","Value":"30.0 W","Type":"Power"}]}"#;
        assert!(WindowsLhmPowerSampler::parse_package_watts(no_pkg).is_err());
        // Malformed JSON → typed read error.
        assert!(WindowsLhmPowerSampler::parse_package_watts("{not json").is_err());
        // A "Package" node that is a temperature, not watts, is not mistaken for power.
        let temp = r#"{"Text":"Package","Value":"54.0 °C","Type":"Temperature"}"#;
        assert!(WindowsLhmPowerSampler::parse_package_watts(temp).is_err());
    }

    #[test]
    fn selector_is_wall_meter_led_then_falls_through_to_estimated() {
        // D1 (wall-meter-led): a configured wall meter wins; with none configured the on-chip
        // sources (a stub-false sysfs node + the parser-only LHM seam) self-disable, so the
        // selector falls all the way to estimated.
        let sysfs =
            SysfsPowerSampler::new("/sys/class/drm/card0/device/hwmon/hwmon0/power1_average");
        let lhm = WindowsLhmPowerSampler::new("http://localhost:8085/data.json");
        let (Ok(wall), Ok(est)) = (
            WallMeterPowerSampler::constant(174.0),
            EstimatedPowerSampler::new(160.0, 1.0),
        ) else {
            panic!("both fallback samplers have valid inputs")
        };
        let chosen = select_sampler(
            Some(wall),
            Some(sysfs.clone()),
            Some(lhm.clone()),
            est.clone(),
        );
        // A configured wall meter always wins (it probes true on every OS).
        assert_eq!(chosen.mode(), MeasurementMode::MeasuredWallmeter);

        // With NO wall meter, both on-chip sources self-disable here (sysfs stub-false off the
        // `power` feature / off Linux; LHM parser-only seam), so estimated is chosen.
        let chosen_no_wall = select_sampler(None, Some(sysfs), Some(lhm), est);
        #[cfg(not(all(target_os = "linux", feature = "power")))]
        assert_eq!(chosen_no_wall.mode(), MeasurementMode::Estimated);
        let _ = chosen_no_wall.sample_watts();
    }

    #[test]
    fn selector_falls_all_the_way_to_estimated() {
        let Ok(est) = EstimatedPowerSampler::new(160.0, 1.0) else {
            panic!("valid profile")
        };
        let chosen = select_sampler(None, None, None, est);
        assert_eq!(chosen.mode(), MeasurementMode::Estimated);
        assert!(chosen.sample_watts().is_ok());
    }

    // The D1 *reversal* proof: a configured wall meter must beat a sysfs node that actually
    // PROBES TRUE — which only happens on native Linux with the `power` feature + a readable
    // µW node. We synthesize a readable node with a temp file so the test is hermetic. Under
    // the OLD (M0) sysfs-first order this would return `MeasuredSysfs`; under D1 it returns
    // `MeasuredWallmeter`. Gated to the only build where `SysfsPowerSampler::probe` can be true.
    #[cfg(all(target_os = "linux", feature = "power"))]
    #[test]
    fn selector_prefers_a_configured_wall_meter_over_a_present_sysfs_node() {
        let path =
            std::env::temp_dir().join(format!("costroid-power-sysfs-{}.txt", std::process::id()));
        // 150 W expressed in microwatts (the sysfs `power1_average` unit).
        if std::fs::write(&path, "150000000").is_err() {
            panic!("temp sysfs node should be writable");
        }
        let Some(node) = path.to_str() else {
            panic!("temp path is valid utf-8")
        };
        let sysfs = SysfsPowerSampler::new(node);
        assert!(sysfs.probe(), "the synthesized node must read (probe true)");

        let (Ok(wall), Ok(est)) = (
            WallMeterPowerSampler::constant(174.0),
            EstimatedPowerSampler::new(160.0, 1.0),
        ) else {
            panic!("fallback samplers have valid inputs")
        };
        // Wall meter present → wins over the probing-true sysfs node (the D1 reversal).
        let chosen = select_sampler(Some(wall), Some(sysfs.clone()), None, est.clone());
        assert_eq!(chosen.mode(), MeasurementMode::MeasuredWallmeter);
        // No wall meter → the probing-true sysfs node is the on-chip source chosen.
        let chosen_no_wall = select_sampler(None, Some(sysfs), None, est);
        assert_eq!(chosen_no_wall.mode(), MeasurementMode::MeasuredSysfs);

        let _ = std::fs::remove_file(&path);
    }
}
