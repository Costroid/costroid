//! Integration test for the public `costroid bench` subcommand (M3 T11, `--features power`).
//!
//! Runs the REAL binary in **estimated / what-if** mode (no subprocess, no hardware) and asserts
//! its emitted `local_inference` FOCUS row is **value-equivalent** to the library path
//! (`bundled_models` + `bundled_power_profiles` + `estimate_run`). The row's `ChargePeriodStart`
//! is `Utc::now()` (non-deterministic), so this checks the economics columns rather than a
//! byte-identical match — proving the CLI is a thin, faithful wrapper over the engine.
#![cfg(feature = "power")]

use std::collections::HashMap;
use std::process::Command;

use costroid_power::{bundled_models, bundled_power_profiles, estimate_run, ProfileOverrides};

/// Parse a single-data-row FOCUS CSV into a column→value map.
fn parse_single_row(csv: &str) -> HashMap<String, String> {
    let mut lines = csv.lines();
    let (Some(header), Some(row)) = (lines.next(), lines.next()) else {
        panic!("expected a header + one data row, got:\n{csv}");
    };
    assert!(
        lines.next().is_none(),
        "bench emits exactly one local_inference row"
    );
    let cols: Vec<&str> = header.split(',').collect();
    let vals: Vec<&str> = row.split(',').collect();
    assert_eq!(cols.len(), vals.len(), "one cell per column");
    cols.iter()
        .map(|c| c.to_string())
        .zip(vals.iter().map(|v| v.to_string()))
        .collect()
}

fn cell_f64(row: &HashMap<String, String>, key: &str) -> f64 {
    match row.get(key) {
        Some(v) => v
            .parse::<f64>()
            .unwrap_or_else(|_| panic!("{key} = {v:?} is not f64")),
        None => panic!("missing column {key}"),
    }
}

#[test]
fn bench_estimated_mode_emits_a_local_row_equivalent_to_the_library() {
    let bin = env!("CARGO_BIN_EXE_costroid");
    // Estimated mode (no --measure → no subprocess, no hardware needed).
    let out = match Command::new(bin)
        .args([
            "bench",
            "--model",
            "gemma-4-26b-a4b",
            "--runtime",
            "ollama",
            "--tokens-out",
            "9600",
            "--tokens-in",
            "1000",
            "--electricity-rate",
            "0.10",
            "--hardware-price",
            "2000",
            "--hardware-lifetime-seconds",
            "94608000",
            "--out",
            "csv",
        ])
        .output()
    {
        Ok(value) => value,
        Err(err) => panic!("running `costroid bench` should succeed: {err}"),
    };
    assert!(
        out.status.success(),
        "bench exited non-zero: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    let csv = String::from_utf8_lossy(&out.stdout);
    let row = parse_single_row(&csv);

    // Structural: the row is a local_inference estimate via ollama.
    assert_eq!(
        row.get("x_Lane").map(String::as_str),
        Some("local_inference")
    );
    assert_eq!(
        row.get("x_MeasurementMode").map(String::as_str),
        Some("estimated")
    );
    assert_eq!(row.get("x_Estimated").map(String::as_str), Some("true"));
    assert_eq!(row.get("x_RuntimeKind").map(String::as_str), Some("ollama"));
    assert_eq!(
        row.get("x_Model").map(String::as_str),
        Some("gemma-4-26b-a4b")
    );
    // Consumed tokens = generated tokens (tokens_out).
    assert_eq!(cell_f64(&row, "x_ConsumedTokens") as u64, 9_600);

    // Value-equivalence: recompute the same estimate via the library and compare the economics.
    let Ok(manifest) = bundled_models() else {
        panic!("manifest parses")
    };
    let Ok((spec, quant)) = manifest.resolve("gemma-4-26b-a4b", None) else {
        panic!("model resolves")
    };
    let Ok(profiles) = bundled_power_profiles() else {
        panic!("profiles parse")
    };
    let overrides = ProfileOverrides {
        electricity_rate_per_kwh: Some(0.10),
        hardware_price: Some(2000.0),
        hardware_lifetime_seconds: Some(94_608_000.0),
        ..ProfileOverrides::default()
    };
    let Ok(resolved) = profiles.resolve(&overrides) else {
        panic!("profile resolves")
    };
    let Ok(report) = estimate_run(
        spec,
        &quant,
        "ollama",
        1_000,
        9_600,
        &resolved,
        "gemma4-local-v1",
    ) else {
        panic!("library estimate computes")
    };

    // The CLI's cost + energy columns match the library report (modulo the rounding the mapping
    // applies). A faithful wrapper.
    assert!(
        (cell_f64(&row, "BilledCost") - report.local_run_cost).abs() < 1e-9,
        "CLI BilledCost {} != library local_run_cost {}",
        cell_f64(&row, "BilledCost"),
        report.local_run_cost
    );
    assert!(
        (cell_f64(&row, "x_MeasuredWh") - report.energy_wh).abs() < 1e-3,
        "CLI x_MeasuredWh {} != library energy_wh {}",
        cell_f64(&row, "x_MeasuredWh"),
        report.energy_wh
    );
    assert_eq!(
        row.get("x_BenchmarkId").map(String::as_str),
        Some(report.benchmark_id.as_str())
    );
    assert_eq!(
        row.get("x_HardwareProfile").map(String::as_str),
        Some(report.hardware_profile_stamp.as_str())
    );
}

#[test]
fn bench_measure_without_a_wall_meter_fails_with_a_clear_message() {
    let bin = env!("CARGO_BIN_EXE_costroid");
    let out = match Command::new(bin)
        .args(["bench", "--measure", "--model", "gemma-4-26b-a4b"])
        .output()
    {
        Ok(value) => value,
        Err(err) => panic!("running `costroid bench --measure` should spawn: {err}"),
    };
    assert!(
        !out.status.success(),
        "--measure without --wall-meter-watts must fail (the M3a measured source is the wall meter)"
    );
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert!(
        stderr.contains("wall-meter-watts"),
        "the error should name --wall-meter-watts; got: {stderr}"
    );
}
