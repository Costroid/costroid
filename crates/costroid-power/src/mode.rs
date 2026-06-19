//! The measurement-mode stamp carried by every reading and every cost record.
//!
//! Honesty in code (R6): the source of a power figure — a real sysfs sensor, a real external
//! wall meter, or a transparent estimate — is stamped on the record itself, never inferred
//! later. The string forms are the canonical `x_MeasurementMode` FOCUS extension-column values
//! (§6.4); they are stable wire identifiers, not display labels.

use serde::{Deserialize, Serialize};

/// How a power reading was obtained. Ordered most- to least-authoritative, matching the
/// selector's fallback ladder (sysfs → wall meter → estimated).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum MeasurementMode {
    /// Real amdgpu hwmon `power1_average` (whole-APU **package** power — disclose the
    /// CPU+GPU-shared-rail caveat, R6). Native-Linux only.
    MeasuredSysfs,
    /// Real total-system draw from a user-configured external wall meter (typically ~20–40%
    /// higher than package power, R6). Works on every OS, including WSL2/Windows.
    MeasuredWallmeter,
    /// Derived from a transparent hardware/power profile + utilization. The universal
    /// fallback and the basis for what-if scenarios.
    Estimated,
}

impl MeasurementMode {
    /// The canonical `x_MeasurementMode` value emitted to FOCUS (§6.4): `measured_sysfs`,
    /// `measured_wallmeter`, or `estimated`.
    pub fn as_focus_str(self) -> &'static str {
        match self {
            MeasurementMode::MeasuredSysfs => "measured_sysfs",
            MeasurementMode::MeasuredWallmeter => "measured_wallmeter",
            MeasurementMode::Estimated => "estimated",
        }
    }

    /// Whether this mode reflects a real physical measurement (vs an estimate). Drives the
    /// `x_Estimated` flag and the "measured vs estimated" UI cue.
    pub fn is_measured(self) -> bool {
        !matches!(self, MeasurementMode::Estimated)
    }
}
