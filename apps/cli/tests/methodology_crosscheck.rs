//! M6 T5 — the `e`-formula REAL cross-check + the cited-assumption cross-check.
//!
//! This is a value comparison, NOT a prose "matches the code" claim:
//!
//! 1. It runs the production helper `costroid_core::local_energy_only_rate` on the **committed**
//!    `samples/benchmark/gemma-4-31b-dense.bench.json` row and asserts the numeric result equals the
//!    worked example printed in `docs/methodology.md` (`e = 0.000000516666665 USD/token`). If either
//!    the doc or the engine drifts, this test fails — the worked example can never silently lie.
//! 2. It ties the **default electricity rate cited in the doc** (0.16 USD/kWh) to the single source of
//!    truth — `crates/costroid-power/profiles/hardware.v1.json` — so the doc figure can't drift from
//!    the dated, stamped assumption it claims to quote.
//!
//! Fully offline. The local-inference row is loaded from the committed artifact (no `power` feature
//! needed), exactly as the samples deciding test does.

use std::path::PathBuf;

use costroid_focus::{FocusExportEnvelope, FocusRecord};
use rust_decimal::Decimal;

fn repo_root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../..")
}

fn read_to_string(path: &PathBuf) -> String {
    match std::fs::read_to_string(path) {
        Ok(value) => value,
        Err(err) => panic!("reading {} should succeed: {err}", path.display()),
    }
}

/// The worked-example value PRINTED in `docs/methodology.md` §4:
///   energy-only = 0.0420431253 − 0.031709792 = 0.0103333333
///   e           = 0.0103333333 ÷ 20000       = 0.000000516666665 USD/token
/// `0.000000516666665` is exactly `516666665 × 10^-15` (a terminating decimal — verified).
const METHODOLOGY_E_DOC_VALUE: &str = "0.000000516666665";

#[test]
fn e_formula_matches_the_methodology_worked_example() {
    // 1) The doc must actually print the value (so this test pins the doc, not just the engine).
    let methodology = read_to_string(&repo_root().join("docs/methodology.md"));
    assert!(
        methodology.contains(METHODOLOGY_E_DOC_VALUE),
        "docs/methodology.md must print the worked-example e = {METHODOLOGY_E_DOC_VALUE}"
    );

    // 2) Run the production helper on the committed benchmark row.
    let bench = repo_root().join("samples/benchmark/gemma-4-31b-dense.bench.json");
    let json = read_to_string(&bench);
    let envelope: FocusExportEnvelope<FocusRecord> = match serde_json::from_str(&json) {
        Ok(value) => value,
        Err(err) => panic!("the committed bench artifact should deserialize: {err}"),
    };
    assert_eq!(
        envelope.focus_version, "1.3",
        "the bench artifact is FOCUS 1.3"
    );
    assert_eq!(
        envelope.rows.len(),
        1,
        "the bench artifact has exactly one row"
    );

    let rate = match costroid_core::local_energy_only_rate(&envelope.rows) {
        Ok(Some(value)) => value,
        Ok(None) => panic!("the committed local row must yield an energy-only rate, got None"),
        Err(err) => panic!("local_energy_only_rate should succeed on the committed row: {err}"),
    };

    let expected = match Decimal::from_str_exact(METHODOLOGY_E_DOC_VALUE) {
        Ok(value) => value,
        Err(err) => panic!("the doc value should parse as a Decimal: {err}"),
    };

    // Numeric equality independent of trailing-zero scale.
    assert_eq!(
        rate.normalize(),
        expected.normalize(),
        "the engine's energy-only rate ({rate}) must equal the methodology worked example \
         ({expected}) — the doc and the code may not drift"
    );
}

#[test]
fn methodology_cites_the_real_default_electricity_rate() {
    // The doc cites a default electricity rate of 0.16 USD/kWh; that value MUST be the one in the
    // single source of truth (the dated, stamped power profile), not a hand-typed figure.
    let methodology = read_to_string(&repo_root().join("docs/methodology.md"));
    assert!(
        methodology.contains("0.16 USD/kWh"),
        "docs/methodology.md must cite the default electricity rate (0.16 USD/kWh)"
    );

    let profile =
        read_to_string(&repo_root().join("crates/costroid-power/profiles/hardware.v1.json"));
    let value: serde_json::Value = match serde_json::from_str(&profile) {
        Ok(v) => v,
        Err(err) => panic!("hardware.v1.json should parse: {err}"),
    };
    let rate = value
        .get("electricity_rate")
        .and_then(|r| r.get("value"))
        .and_then(serde_json::Value::as_f64);
    let currency = value
        .get("electricity_rate")
        .and_then(|r| r.get("currency"))
        .and_then(serde_json::Value::as_str);
    assert_eq!(
        rate,
        Some(0.16),
        "the profile's electricity_rate.value must be the 0.16 the doc cites"
    );
    assert_eq!(
        currency,
        Some("USD"),
        "the profile's electricity_rate.currency must be USD (the doc cites USD/kWh)"
    );
}
