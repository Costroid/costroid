//! The bundled, dated, overridable power/cost assumptions (M3, R8).
//!
//! The estimated-mode economics (§3.2) rest on assumptions — hardware power draw, hardware
//! price/lifetime, and the electricity rate. R8 requires these be **dated, stamped, and
//! overridable**, never hidden magic numbers. They ship as a **vendored data artifact**
//! (`profiles/hardware.v1.json`, compiled in via `include_str!`, never fetched), every value
//! flagged `estimated` (R6/R10 — community ranges, not measured), and are overridable per-run
//! (CLI flags / `[power]` config, applied via [`ProfileOverrides`]). The winning profile id is
//! stamped on each local row's `x_HardwareProfile` as `"{id}@{as_of}"`.

use serde::{Deserialize, Serialize};

use crate::error::PowerError;

/// The bundled assumption set, compiled in (never fetched — R8).
const BUNDLED_PROFILE_JSON: &str = include_str!("../profiles/hardware.v1.json");

/// The `as_of` date the bundled artifact records — pinned in Rust so a swapped/edited artifact
/// is caught by the loader test (the R8 "stamp the assumption + date" discipline).
pub const POWER_PROFILE_AS_OF: &str = "2026-06-20";

/// The id of the default hardware profile (the founder's measurement instrument, §5.2).
pub const DEFAULT_HARDWARE_PROFILE_ID: &str = "strix-halo-128gb";

/// The full bundled assumption set: the hardware profiles + the default electricity rate.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct PowerProfiles {
    /// The date these assumptions were recorded (R8 stamp).
    pub as_of: String,
    /// Where the figures came from + the honesty disclaimer (R10).
    pub source: String,
    pub hardware_profiles: Vec<HardwareProfile>,
    pub electricity_rate: ElectricityRate,
}

/// One dated, estimated hardware/power profile.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct HardwareProfile {
    pub id: String,
    pub description: String,
    /// The estimated average inference power draw, in watts (the estimated-mode sampler basis).
    pub load_watts: f64,
    /// The community-measured span `[min, max]` the `load_watts` point sits within (context).
    #[serde(default)]
    pub load_watts_range: Option<[f64; 2]>,
    pub idle_watts: f64,
    pub hardware_price: f64,
    pub hardware_lifetime_seconds: f64,
    pub memory_bandwidth_gbps: f64,
    /// Always `true` for the bundled profile — these are estimates, never measured (R10).
    pub estimated: bool,
}

/// The dated default electricity rate (R8: stamped + overridable).
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct ElectricityRate {
    pub value: f64,
    pub currency: String,
    pub as_of: String,
    pub label: String,
    pub estimated: bool,
}

/// Per-run overrides (from `costroid bench` flags / the `[power]` config). Each `None` keeps
/// the bundled default; a `Some` replaces it. The CLI builds this; `costroid-power` applies it
/// (so the leaf crate never depends on `costroid-config`).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct ProfileOverrides {
    /// Override the hardware profile selected (default [`DEFAULT_HARDWARE_PROFILE_ID`]).
    pub hardware_profile_id: Option<String>,
    pub load_watts: Option<f64>,
    pub electricity_rate_per_kwh: Option<f64>,
    pub hardware_price: Option<f64>,
    pub hardware_lifetime_seconds: Option<f64>,
}

/// The resolved, validated assumptions for one run — what the harness consumes. The stamp id
/// `"{profile_id}@{as_of}"` rides the row's `x_HardwareProfile` (R8).
#[derive(Debug, Clone, PartialEq)]
pub struct ResolvedProfile {
    /// The `x_HardwareProfile` stamp: `"{profile_id}@{as_of}"`.
    pub stamp: String,
    /// The estimated average load draw used by the estimated sampler (watts).
    pub load_watts: f64,
    pub electricity_rate_per_kwh: f64,
    /// The currency the rate / resulting costs are quoted in (FOCUS `BillingCurrency`).
    pub currency: String,
    pub hardware_price: f64,
    pub hardware_lifetime_seconds: f64,
}

/// Parse the bundled assumption set (never fetched — R8). A malformed bundled artifact is a
/// typed [`PowerError`], never a panic.
pub fn bundled_power_profiles() -> Result<PowerProfiles, PowerError> {
    serde_json::from_str(BUNDLED_PROFILE_JSON).map_err(|e| {
        PowerError::InvalidProfile(format!("bundled hardware.v1.json failed to parse: {e}"))
    })
}

impl PowerProfiles {
    /// The hardware profile with the given id, if present.
    pub fn profile(&self, id: &str) -> Option<&HardwareProfile> {
        self.hardware_profiles.iter().find(|p| p.id == id)
    }

    /// Resolve the run's assumptions: select the profile (override id → else
    /// [`DEFAULT_HARDWARE_PROFILE_ID`]), apply the per-field overrides, validate every value is
    /// physically sane, and build the `x_HardwareProfile` stamp. Returns a typed
    /// [`PowerError`] for an unknown profile id or a non-positive override — never a panic.
    pub fn resolve(&self, overrides: &ProfileOverrides) -> Result<ResolvedProfile, PowerError> {
        let id = overrides
            .hardware_profile_id
            .as_deref()
            .unwrap_or(DEFAULT_HARDWARE_PROFILE_ID);
        let base = self.profile(id).ok_or_else(|| {
            PowerError::InvalidProfile(format!("unknown hardware profile id `{id}`"))
        })?;

        let load_watts = overrides.load_watts.unwrap_or(base.load_watts);
        let electricity_rate_per_kwh = overrides
            .electricity_rate_per_kwh
            .unwrap_or(self.electricity_rate.value);
        let hardware_price = overrides.hardware_price.unwrap_or(base.hardware_price);
        let hardware_lifetime_seconds = overrides
            .hardware_lifetime_seconds
            .unwrap_or(base.hardware_lifetime_seconds);

        // Validate (R6 honesty): a non-physical override is a typed error, never a silent
        // negative/zero/NaN cost. (Zero rate / zero hardware price are legitimate scenarios —
        // matching `cost::local_run_cost`; load_watts + lifetime must be strictly positive.)
        // The explicit `is_nan()` guards keep a NaN (e.g. a `--electricity-rate nan` flag) from
        // slipping through a direct `<=`/`<` comparison (NaN compares false to everything).
        if load_watts <= 0.0 || load_watts.is_nan() {
            return Err(PowerError::InvalidProfile(format!(
                "load_watts must be positive, got {load_watts}"
            )));
        }
        if hardware_lifetime_seconds <= 0.0 || hardware_lifetime_seconds.is_nan() {
            return Err(PowerError::InvalidProfile(format!(
                "hardware_lifetime_seconds must be positive, got {hardware_lifetime_seconds}"
            )));
        }
        if electricity_rate_per_kwh < 0.0
            || electricity_rate_per_kwh.is_nan()
            || hardware_price < 0.0
            || hardware_price.is_nan()
        {
            return Err(PowerError::InvalidProfile(format!(
                "electricity_rate_per_kwh / hardware_price must be non-negative, got {electricity_rate_per_kwh} / {hardware_price}"
            )));
        }

        Ok(ResolvedProfile {
            stamp: format!("{}@{}", base.id, self.as_of),
            load_watts,
            electricity_rate_per_kwh,
            currency: self.electricity_rate.currency.clone(),
            hardware_price,
            hardware_lifetime_seconds,
        })
    }
}

#[cfg(test)]
mod tests {
    // Repo rule: clippy denies `unwrap`/`expect` even in tests; use `let-else { panic! }`.
    use super::*;

    #[test]
    fn bundled_power_profiles_parses_with_pinned_as_of() {
        let Ok(profiles) = bundled_power_profiles() else {
            panic!("the bundled hardware.v1.json must parse");
        };
        // R8: a swapped/edited artifact changes `as_of`, which the pinned const catches.
        assert_eq!(profiles.as_of, POWER_PROFILE_AS_OF);
        let Some(strix) = profiles.profile(DEFAULT_HARDWARE_PROFILE_ID) else {
            panic!("the default strix-halo profile must be present");
        };
        // R10: every bundled value is an ESTIMATE, never measured.
        assert!(strix.estimated, "hardware profile is stamped estimated");
        assert!(
            profiles.electricity_rate.estimated,
            "rate is stamped estimated"
        );
        assert!(strix.load_watts > 0.0);
        // The default rate is the USD template (so local rows land in the USD lane).
        assert_eq!(profiles.electricity_rate.currency, "USD");
    }

    #[test]
    fn resolve_uses_the_default_profile_and_builds_the_stamp() {
        let Ok(profiles) = bundled_power_profiles() else {
            panic!("profiles must parse");
        };
        let Ok(resolved) = profiles.resolve(&ProfileOverrides::default()) else {
            panic!("the default profile must resolve");
        };
        assert_eq!(resolved.stamp, "strix-halo-128gb@2026-06-20");
        assert_eq!(resolved.currency, "USD");
        // The default rate flows through verbatim (no override).
        assert!((resolved.electricity_rate_per_kwh - 0.16).abs() < 1e-12);
    }

    #[test]
    fn overrides_replace_only_the_fields_they_set() {
        let Ok(profiles) = bundled_power_profiles() else {
            panic!("profiles must parse");
        };
        let overrides = ProfileOverrides {
            electricity_rate_per_kwh: Some(0.42),
            load_watts: Some(180.0),
            ..ProfileOverrides::default()
        };
        let Ok(resolved) = profiles.resolve(&overrides) else {
            panic!("override should resolve");
        };
        assert!((resolved.electricity_rate_per_kwh - 0.42).abs() < 1e-12);
        assert!((resolved.load_watts - 180.0).abs() < 1e-12);
        // Untouched fields keep the bundled default.
        assert!((resolved.hardware_price - 2000.0).abs() < 1e-12);
    }

    #[test]
    fn an_unknown_profile_id_is_a_typed_error_not_a_panic() {
        let Ok(profiles) = bundled_power_profiles() else {
            panic!("profiles must parse");
        };
        let overrides = ProfileOverrides {
            hardware_profile_id: Some("no-such-gpu".to_string()),
            ..ProfileOverrides::default()
        };
        assert!(matches!(
            profiles.resolve(&overrides),
            Err(PowerError::InvalidProfile(_))
        ));
    }

    #[test]
    fn a_non_physical_override_is_rejected() {
        let Ok(profiles) = bundled_power_profiles() else {
            panic!("profiles must parse");
        };
        // Zero/negative load watts and a negative rate are rejected.
        for bad in [
            ProfileOverrides {
                load_watts: Some(0.0),
                ..ProfileOverrides::default()
            },
            ProfileOverrides {
                electricity_rate_per_kwh: Some(-0.10),
                ..ProfileOverrides::default()
            },
            ProfileOverrides {
                hardware_lifetime_seconds: Some(0.0),
                ..ProfileOverrides::default()
            },
        ] {
            assert!(matches!(
                profiles.resolve(&bad),
                Err(PowerError::InvalidProfile(_))
            ));
        }
    }
}
