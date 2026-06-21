//! Deciding test for the curated `samples/` demo datasets (M6 T1, §6.7).
//!
//! Fully offline. Loads each of the three synthetic packs and asserts it parses, lands in the
//! right lane with the pinned row counts/token totals, and round-trips through Costroid's FOCUS
//! export. The **inverse honesty guard** (shared with T8) asserts every committed local-inference
//! row carries `x_MeasurementMode == "estimated"` — no committed artifact may claim a measured
//! number pre-M3b.
//!
//! Lanes are driven through the REAL `costroid` binary as a subprocess (env-isolated, so the
//! developer's own logs can never leak in) for the developer-tool + cloud-API packs, and by
//! loading the committed `.bench.json` artifacts directly for the local-inference pack — so this
//! test runs in the default `cargo test --workspace` (no `power` feature needed).

use std::path::{Path, PathBuf};
use std::process::Command;

use costroid_focus::{FocusExportEnvelope, FocusRecord};
use rust_decimal::prelude::ToPrimitive;

fn repo_root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../..")
}

fn samples_dir() -> PathBuf {
    repo_root().join("samples")
}

/// Run the real `costroid` binary, neutralizing every discovery env override the binary honors
/// EXCEPT the ones explicitly set here — so the only logs it can ever read are the committed
/// samples, never the developer's real logs.
fn run_costroid(args: &[&str], extra_env: &[(&str, &str)]) -> String {
    let bin = env!("CARGO_BIN_EXE_costroid");
    let mut cmd = Command::new(bin);
    cmd.args(args);
    // Neutralize discovery overrides (start from a clean slate).
    for key in [
        "HOME",
        "USERPROFILE",
        "CLAUDE_CONFIG_DIR",
        "CODEX_HOME",
        "CURSOR_DATA_DIR",
        "XDG_STATE_HOME",
        "ANTHROPIC_API_KEY",
    ] {
        cmd.env(key, "");
    }
    // A HOME that does not exist (so default discovery finds nothing).
    cmd.env("HOME", "/costroid-nonexistent-home");
    for (k, v) in extra_env {
        cmd.env(k, v);
    }
    let out = match cmd.output() {
        Ok(value) => value,
        Err(err) => panic!("running `costroid {args:?}` should spawn: {err}"),
    };
    assert!(
        out.status.success(),
        "`costroid {args:?}` exited non-zero: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    match String::from_utf8(out.stdout) {
        Ok(value) => value,
        Err(err) => panic!("`costroid {args:?}` stdout should be UTF-8: {err}"),
    }
}

fn parse_envelope(json: &str) -> Vec<FocusRecord> {
    match serde_json::from_str::<FocusExportEnvelope<FocusRecord>>(json) {
        Ok(envelope) => {
            assert_eq!(envelope.focus_version, "1.3", "samples export is FOCUS 1.3");
            envelope.rows
        }
        Err(err) => panic!("FOCUS JSON should deserialize into rows: {err}\n{json}"),
    }
}

fn read_file(path: &Path) -> String {
    match std::fs::read_to_string(path) {
        Ok(value) => value,
        Err(err) => panic!("reading {} should succeed: {err}", path.display()),
    }
}

/// Sum the `x_ConsumedTokens` across rows as a u128 (token counts never overflow this).
fn total_consumed_tokens(rows: &[FocusRecord]) -> u128 {
    let mut total: u128 = 0;
    for row in rows {
        let Some(tokens) = row.x_consumed_tokens.to_u128() else {
            panic!("x_ConsumedTokens should be a whole token count")
        };
        total += tokens;
    }
    total
}

/// Assert each lane's CSV export parses back / is schema-shaped (header + the right row count).
fn assert_csv_round_trips(rows: &[FocusRecord], expected_rows: usize) {
    let csv = match costroid_core::export_focus_csv(rows) {
        Ok(value) => value,
        Err(err) => panic!("CSV export should succeed: {err}"),
    };
    let data_rows = csv.lines().count().saturating_sub(1); // minus the header
    assert_eq!(
        data_rows, expected_rows,
        "CSV export should have header + {expected_rows} data rows"
    );
    assert!(
        csv.contains("x_Lane"),
        "CSV export carries the x_Lane column"
    );
}

/// Assert each lane's JSON export parses back into the same rows (round-trip identity).
fn assert_json_round_trips(rows: &[FocusRecord]) {
    let json = match costroid_core::export_focus_json(rows.to_vec()) {
        Ok(value) => value,
        Err(err) => panic!("JSON export should succeed: {err}"),
    };
    let parsed = parse_envelope(&json);
    assert_eq!(
        &parsed, rows,
        "JSON export must round-trip to the same rows"
    );
}

#[test]
fn samples_local_logs_collect_into_the_developer_tool_lane() {
    let local = samples_dir().join("local-logs");
    let claude = local.join("claude");
    let codex = local.join("codex");
    let json = run_costroid(
        &["export", "--format", "json"],
        &[
            ("CLAUDE_CONFIG_DIR", claude.to_string_lossy().as_ref()),
            ("CODEX_HOME", codex.to_string_lossy().as_ref()),
        ],
    );
    let rows = parse_envelope(&json);

    // Non-empty, every row in the developer_tool lane.
    assert!(!rows.is_empty(), "samples/local-logs must produce rows");
    assert_eq!(rows.len(), 14, "pinned: 14 developer_tool rows");
    assert!(
        rows.iter().all(|r| r.x_lane == "developer_tool"),
        "every sample local-log row is the developer_tool lane"
    );
    // Pinned token total across the priced per-meter rows.
    assert_eq!(
        total_consumed_tokens(&rows),
        622_000,
        "pinned: 622,000 total consumed tokens"
    );
    // The synthetic models are present (a faithful demo, not an empty pass).
    assert!(rows.iter().any(|r| r.x_model == "claude-opus-4-8"));
    assert!(rows.iter().any(|r| r.x_model == "claude-sonnet-4-6"));
    assert!(rows.iter().any(|r| r.x_model == "gpt-5.5"));

    assert_csv_round_trips(&rows, 14);
    assert_json_round_trips(&rows);
}

#[test]
fn samples_cloud_focus_imports_into_the_cloud_api_lane() {
    let csv_path = samples_dir().join("cloud-focus").join("aws-focus-v12.csv");
    let path_str = csv_path.to_string_lossy().to_string();
    let json = run_costroid(
        &[
            "import",
            "--format",
            "focus-csv",
            "--version",
            "auto",
            "--out",
            "json",
            path_str.as_str(),
        ],
        &[],
    );
    let rows = parse_envelope(&json);

    assert_eq!(rows.len(), 4, "pinned: 4 cloud_api rows");
    assert!(
        rows.iter().all(|r| r.x_lane == "cloud_api"),
        "every imported sample row is the cloud_api lane"
    );
    // Source-priced (authoritative cost): the demo cloud bill totals 9.60 USD.
    let mut total = rust_decimal::Decimal::ZERO;
    for row in &rows {
        total += row.billed_cost;
    }
    assert_eq!(
        total,
        rust_decimal::Decimal::new(96, 1),
        "pinned: 9.6 USD source-priced cloud total"
    );
    // R4: the bounded inference-profile id is carried; no profile NAME ever appears.
    assert!(
        rows.iter()
            .any(|r| r.x_inference_profile_id.as_deref() == Some("aip-0a1b2c3d4e5f6789")),
        "the bounded x_InferenceProfileId is carried"
    );

    assert_csv_round_trips(&rows, 4);
    assert_json_round_trips(&rows);
}

#[test]
fn samples_benchmark_rows_are_local_inference_and_strictly_estimated() {
    let bench = samples_dir().join("benchmark");
    let files = ["gemma-4-31b-dense.bench.json", "gemma-4-26b-a4b.bench.json"];

    let mut all_rows: Vec<FocusRecord> = Vec::new();
    for name in files {
        let rows = parse_envelope(&read_file(&bench.join(name)));
        assert_eq!(rows.len(), 1, "each bench artifact has exactly one row");
        all_rows.extend(rows);
    }

    assert_eq!(all_rows.len(), 2, "pinned: 2 local_inference rows");
    assert!(
        all_rows.iter().all(|r| r.x_lane == "local_inference"),
        "every committed bench row is the local_inference lane"
    );
    // INVERSE HONESTY GUARD (shared with T8): every committed local row is estimated, never
    // a measured number pre-M3b. `ESTIMATED_MODE` is tied to the `MeasurementMode::Estimated`
    // wire serialization (the `#[cfg(feature = "power")]` assert below pins them together) but
    // expressed as the literal so this guard runs in the default `cargo test --workspace`.
    const ESTIMATED_MODE: &str = "estimated";
    assert!(
        all_rows
            .iter()
            .all(|r| r.x_measurement_mode.as_deref() == Some(ESTIMATED_MODE)),
        "every committed local-inference row MUST be x_MeasurementMode == \"estimated\""
    );
    #[cfg(feature = "power")]
    assert_eq!(
        ESTIMATED_MODE,
        costroid_power::MeasurementMode::Estimated.as_focus_str(),
        "the inverse-guard literal must equal the MeasurementMode::Estimated serialization"
    );
    assert!(
        all_rows.iter().all(|r| r.x_estimated),
        "an estimated row also carries x_Estimated == true"
    );
    // Each run is 2,000 in + 18,000 out = 20,000 total tokens (the pinned demo volume).
    assert_eq!(
        total_consumed_tokens(&all_rows),
        40_000,
        "pinned: 2 rows × 20,000 total consumed tokens"
    );

    assert_csv_round_trips(&all_rows, 2);
    assert_json_round_trips(&all_rows);
}
