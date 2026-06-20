//! The measurement-mode stamp carried by every reading and every cost record.
//!
//! Honesty in code (R6): the source of a power figure — a real sysfs sensor, a real external
//! wall meter, or a transparent estimate — is stamped on the record itself, never inferred
//! later. The string forms are the canonical `x_MeasurementMode` FOCUS extension-column values
//! (§6.4); they are stable wire identifiers, not display labels.

use serde::{Deserialize, Serialize};

/// How a power reading was obtained. Ordered most- to least-authoritative along the revised
/// (2026-06-20) **wall-meter-led** ladder (§5.3/§5.4): the wall meter is the truest cross-OS
/// figure, the on-chip readers (sysfs on Linux, LibreHardwareMonitor on Windows) are
/// package-grade, estimated is the universal fallback.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum MeasurementMode {
    /// Real total-system draw from a user-configured external wall meter (typically ~20–40%
    /// higher than on-chip package power, R6). Works on every OS, including WSL2/Windows — the
    /// **recommended** measured source.
    MeasuredWallmeter,
    /// Real amdgpu hwmon `power1_average` (whole-APU **package** power — disclose the
    /// CPU+GPU-shared-rail caveat, R6). Native-Linux only; the optional on-chip path.
    MeasuredSysfs,
    /// Real LibreHardwareMonitor SMU **Package** sensor (whole-APU package power, R6) read
    /// out-of-process from its localhost JSON endpoint. Windows-only; the optional on-chip
    /// path there. The live read is the M3b field-verification; M3a ships the parser seam.
    MeasuredLhm,
    /// Derived from a transparent hardware/power profile + utilization. The universal
    /// fallback and the basis for what-if scenarios.
    Estimated,
}

impl MeasurementMode {
    /// The canonical `x_MeasurementMode` value emitted to FOCUS (§6.4): `measured_wallmeter`,
    /// `measured_sysfs`, `measured_lhm`, or `estimated`.
    pub fn as_focus_str(self) -> &'static str {
        match self {
            MeasurementMode::MeasuredWallmeter => "measured_wallmeter",
            MeasurementMode::MeasuredSysfs => "measured_sysfs",
            MeasurementMode::MeasuredLhm => "measured_lhm",
            MeasurementMode::Estimated => "estimated",
        }
    }

    /// Whether this mode reflects a real physical measurement (vs an estimate). Drives the
    /// `x_Estimated` flag and the "measured vs estimated" UI cue.
    pub fn is_measured(self) -> bool {
        !matches!(self, MeasurementMode::Estimated)
    }
}

#[cfg(test)]
mod tests {
    // Repo rule: clippy denies `unwrap`/`expect` even in tests; use `let-else { panic! }`.
    use super::*;

    #[test]
    fn all_four_modes_have_stable_focus_strings_and_measured_flags() {
        // The `x_MeasurementMode` wire identifiers (§6.4) — stable, not display labels.
        assert_eq!(
            MeasurementMode::MeasuredWallmeter.as_focus_str(),
            "measured_wallmeter"
        );
        assert_eq!(
            MeasurementMode::MeasuredSysfs.as_focus_str(),
            "measured_sysfs"
        );
        assert_eq!(MeasurementMode::MeasuredLhm.as_focus_str(), "measured_lhm");
        assert_eq!(MeasurementMode::Estimated.as_focus_str(), "estimated");
        // Every real-measurement mode (incl. the new LHM source) is `is_measured`; only the
        // estimated fallback is not — this drives `x_Estimated` on the FOCUS row.
        assert!(MeasurementMode::MeasuredWallmeter.is_measured());
        assert!(MeasurementMode::MeasuredSysfs.is_measured());
        assert!(MeasurementMode::MeasuredLhm.is_measured());
        assert!(!MeasurementMode::Estimated.is_measured());
    }

    #[test]
    fn measured_lhm_round_trips_through_serde() {
        let Ok(json) = serde_json::to_string(&MeasurementMode::MeasuredLhm) else {
            panic!("MeasurementMode should serialize");
        };
        assert_eq!(json, "\"measured_lhm\"");
        let Ok(back) = serde_json::from_str::<MeasurementMode>(&json) else {
            panic!("MeasurementMode should deserialize");
        };
        assert_eq!(back, MeasurementMode::MeasuredLhm);
    }
}
