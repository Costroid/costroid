//! The authoritative, human-gated allowlist of **measured** local-inference benchmarks (M3b).
//!
//! Honesty in code (R10): a benchmark row may claim a *measured* number only if a human has
//! attested — here, in committed source — that the shipped `benchmarks/**` data for that model is a
//! real captured run. This module is that single source of truth; it is **empty** until the M3b
//! wall-meter run lands.
//!
//! The post-M3b drift-guard (`apps/cli/tests/post_m3b_refresh.rs`) keys off [`MEASURED_MODELS`]:
//! a benchmark row claiming a measured mode whose model is **absent** here FAILS the build (a
//! fabricated or half-flipped measured claim cannot ship), and a model **present** here MUST carry
//! exactly its declared mode in BOTH the manifest run and the raw FOCUS row, with `x_Estimated`
//! cleared. Bare cross-consistency would let a fabricated-but-internally-consistent measured row
//! pass; the allowlist is the provenance anchor that doesn't.

use crate::mode::MeasurementMode;

/// The authoritative, human-gated allowlist of model ids whose committed `benchmarks/**` dataset
/// carries a **real measured run** — paired with the exact [`MeasurementMode`] that run used.
///
/// **Empty until the M3b wall-meter run lands.** Adding an entry is a human ATTESTATION (R10) that
/// this model's shipped benchmark *manifest run* AND *raw FOCUS row* are real captured numbers, not
/// estimates. See the module docs for the guard contract.
pub const MEASURED_MODELS: &[(&str, MeasurementMode)] = &[
    // Phase 2 (human, after the wall-meter run), e.g.:
    //   ("gemma-4-31b-dense", MeasurementMode::MeasuredWallmeter),
];

/// Find a model's declared measured mode in an allowlist slice. The seam exists so the lookup logic
/// is unit-testable against a synthetic list without fabricating bundled data (the public
/// [`measured_mode_for`] binds it to [`MEASURED_MODELS`]).
fn lookup(list: &[(&str, MeasurementMode)], model_id: &str) -> Option<MeasurementMode> {
    list.iter()
        .find(|(id, _)| *id == model_id)
        .map(|(_, mode)| *mode)
}

/// The declared measured mode for `model_id`, if it is on the authoritative [`MEASURED_MODELS`]
/// allowlist; `None` for any model still estimated (every model, pre-M3b).
pub fn measured_mode_for(model_id: &str) -> Option<MeasurementMode> {
    lookup(MEASURED_MODELS, model_id)
}

#[cfg(test)]
mod tests {
    // Repo rule: clippy denies `unwrap`/`expect` even in tests; use `let-else { panic! }`.
    use super::*;

    #[test]
    fn measured_models_is_empty_pre_m3b() {
        // The all-estimated invariant lives here as code: nothing is attested measured yet, so the
        // benchmarks drift-guard's empty-allowlist case == "every committed row must be estimated".
        assert!(
            MEASURED_MODELS.is_empty(),
            "MEASURED_MODELS must stay empty until the M3b measurement run is committed"
        );
        assert!(measured_mode_for("gemma-4-31b-dense").is_none());
    }

    #[test]
    fn lookup_resolves_the_paired_mode() {
        // Exercise the find logic against a synthetic allowlist (no bundled data touched).
        let list = [
            ("alpha", MeasurementMode::MeasuredWallmeter),
            ("beta", MeasurementMode::Estimated),
        ];
        assert_eq!(
            lookup(&list, "alpha"),
            Some(MeasurementMode::MeasuredWallmeter)
        );
        assert_eq!(lookup(&list, "beta"), Some(MeasurementMode::Estimated));
        assert_eq!(lookup(&list, "gamma"), None);
    }
}
