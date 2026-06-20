//! Integration test for the public `costroid import` subcommand (T19).
//!
//! Runs the REAL binary against a synthetic FOCUS v1.2 fixture and asserts its emitted
//! FOCUS 1.3 output is **byte-identical** to the library path
//! (`focus_import` → `focus_records_from_v12_import` → `export_focus_*`) — so the CLI is a
//! thin, faithful wrapper that adds no divergent behavior. Covers both the CSV and JSON
//! input legs and both output serializations.

use std::path::PathBuf;
use std::process::Command;

use costroid_core::focus_records_from_v12_import;
use costroid_providers::focus_import::{import_focus_csv, import_focus_json};

fn fixture(name: &str) -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../fixtures/focus/v1.2")
        .join(name)
}

/// The library path: import the foreign file, normalize to the 1.3 ledger, serialize.
fn library_output(name: &str, csv_input: bool, csv_output: bool) -> String {
    let data = match std::fs::read_to_string(fixture(name)) {
        Ok(value) => value,
        Err(err) => panic!("fixture {name} should read: {err}"),
    };
    let import = if csv_input {
        import_focus_csv(&data)
    } else {
        import_focus_json(&data)
    };
    let import = match import {
        Ok(value) => value,
        Err(err) => panic!("library import should succeed: {err}"),
    };
    let rows = match focus_records_from_v12_import(&import.events, &import.detection.version) {
        Ok(value) => value,
        Err(err) => panic!("library normalization should succeed: {err}"),
    };
    if csv_output {
        match costroid_core::export_focus_csv(&rows) {
            Ok(value) => value,
            Err(err) => panic!("csv export should succeed: {err}"),
        }
    } else {
        match costroid_core::export_focus_json(rows) {
            Ok(value) => value,
            Err(err) => panic!("json export should succeed: {err}"),
        }
    }
}

fn run_import(input_format: &str, out_format: &str, name: &str) -> String {
    let bin = env!("CARGO_BIN_EXE_costroid");
    let output = match Command::new(bin)
        .args(["import", "--format", input_format, "--out", out_format])
        .arg(fixture(name))
        .output()
    {
        Ok(value) => value,
        Err(err) => panic!("running `{bin} import` should succeed: {err}"),
    };
    assert!(
        output.status.success(),
        "import exited non-zero: {}",
        String::from_utf8_lossy(&output.stderr)
    );
    match String::from_utf8(output.stdout) {
        Ok(value) => value,
        Err(err) => panic!("import stdout should be UTF-8: {err}"),
    }
}

#[test]
fn import_csv_to_csv_is_byte_identical_to_the_library_path() {
    let cli = run_import("focus-csv", "csv", "synthetic-v12-marked.csv");
    let lib = library_output("synthetic-v12-marked.csv", true, true);
    assert_eq!(
        cli, lib,
        "CLI csv import must equal the library path byte-for-byte"
    );
    // Non-vacuous: the output is a real FOCUS 1.3 cloud_api ledger, source-priced.
    assert!(
        cli.contains("cloud_api"),
        "imported rows are cloud_api lane"
    );
    assert!(
        cli.contains("x_FocusInputVersion"),
        "carries the import provenance column"
    );
    assert!(cli.lines().count() >= 3, "header + two data rows");
}

#[test]
fn import_csv_to_json_is_byte_identical_to_the_library_path() {
    let cli = run_import("focus-csv", "json", "synthetic-v12-marked.csv");
    let lib = library_output("synthetic-v12-marked.csv", true, false);
    assert_eq!(cli, lib);
    assert!(
        cli.contains("\"focusVersion\": \"1.3\""),
        "emits the 1.3 envelope"
    );
}

#[test]
fn import_json_input_leg_matches_the_library_path() {
    let cli = run_import("focus-json", "csv", "synthetic-v12.json");
    let lib = library_output("synthetic-v12.json", false, true);
    assert_eq!(cli, lib);
}

#[test]
fn import_pricing_override_reprices_a_usage_only_row() {
    // D5: --pricing-override layers a user file over the bundled catalog. A USAGE-ONLY cloud
    // row (no BilledCost) for a catalog model is repriced; the override tier wins and stamps
    // its provenance. (Source-priced rows are authoritative and unaffected.)
    let dir = std::env::temp_dir().join(format!("costroid-override-cli-{}", std::process::id()));
    if std::fs::create_dir_all(&dir).is_err() {
        panic!("temp dir should create");
    }
    let usage_csv = dir.join("usage-only.csv");
    if std::fs::write(
        &usage_csv,
        "BilledCost,ChargePeriodStart,SkuId,ConsumedQuantity\n\
         ,2026-06-15T10:00:00Z,claude-sonnet-4-6,1000000\n",
    )
    .is_err()
    {
        panic!("usage csv should write");
    }
    let override_json = dir.join("override.json");
    if std::fs::write(
        &override_json,
        r#"{"schema_version":"1","source":"override","as_of":"2026-06-20",
            "content_hash":"deadbeefcafe","currency":"USD","models":[
            {"provider":"anthropic","model":"claude-sonnet-4-6","service_name":"Anthropic API",
             "rates":[{"meter":"output","unit":"1M_tokens","price":"999.00"}]}]}"#,
    )
    .is_err()
    {
        panic!("override json should write");
    }

    let bin = env!("CARGO_BIN_EXE_costroid");
    let output = match Command::new(bin)
        .args(["import", "--format", "focus-csv", "--out", "csv"])
        .arg("--pricing-override")
        .arg(&override_json)
        .arg(&usage_csv)
        .output()
    {
        Ok(value) => value,
        Err(err) => panic!("running import should succeed: {err}"),
    };
    assert!(
        output.status.success(),
        "import exited non-zero: {}",
        String::from_utf8_lossy(&output.stderr)
    );
    let out = String::from_utf8_lossy(&output.stdout);
    // 1M tokens × 999.00/1M = 999.00 (the override rate, not the curated 15.00).
    assert!(
        out.contains("999"),
        "the override rate priced the row: {out}"
    );
    assert!(
        out.contains("override@2026-06-20#deadbeef"),
        "the row records the override provenance stamp: {out}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn import_aws_sample_drops_provider_specific_columns() {
    // The AWS-shaped sample carries x_ServiceCode / x_UsageType; the importer must drop
    // them (R4 — no provider-specific free text reaches the ledger).
    let cli = run_import("focus-csv", "csv", "synthetic-aws-v12.csv");
    assert!(!cli.contains("ServiceCode"), "x_ServiceCode dropped");
    assert!(!cli.contains("UsageType"), "x_UsageType dropped");
    assert!(
        !cli.contains("BedrockModelUnits"),
        "the usage-type value dropped"
    );
    assert!(
        cli.contains("cloud_api"),
        "rows still imported into the cloud lane"
    );
}
