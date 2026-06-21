//! Integration test for the public `costroid breakeven` subcommand (M4 T7/T8, `--features power`).
//!
//! Runs the REAL binary in estimated/what-if mode (no subprocess, no hardware, no network) and
//! asserts the rendered verdict is well-formed: it names the cloud model, surfaces the crossover +
//! the assumption stamp + the labeled DeepSWE-Bench overlay, carries NO ANSI escape under
//! `--plain`, and rejects an unknown `--compare-to` model with a clear message.
#![cfg(feature = "power")]

use std::process::Command;

fn run(args: &[&str]) -> std::process::Output {
    let bin = env!("CARGO_BIN_EXE_costroid");
    match Command::new(bin).args(args).output() {
        Ok(value) => value,
        Err(err) => panic!("running `costroid {}` should spawn: {err}", args.join(" ")),
    }
}

#[test]
fn breakeven_plain_renders_a_well_formed_verdict_with_no_ansi() {
    let out = run(&[
        "breakeven",
        "--plain",
        "--compare-to",
        "claude-opus-4-8",
        "--tokens-per-day",
        "5000000",
    ]);
    assert!(
        out.status.success(),
        "breakeven exited non-zero: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    let stdout = String::from_utf8_lossy(&out.stdout);

    // The verdict names the cloud model and states a break-even outcome.
    assert!(
        stdout.contains("claude-opus-4-8"),
        "names the cloud model:\n{stdout}"
    );
    assert!(
        stdout.to_lowercase().contains("break"),
        "states a break-even verdict:\n{stdout}"
    );
    // The sensitivity range, the assumption stamp, and the labeled dated DeepSWE overlay are shown.
    assert!(stdout.contains("Sensitivity range"), "shows a range (R6)");
    assert!(stdout.contains("Assumptions"), "shows the assumption stamp");
    assert!(
        stdout.contains("DeepSWE-Bench") && stdout.contains("2026-06-14"),
        "shows the labeled, dated DeepSWE overlay:\n{stdout}"
    );
    // --plain is byte-for-byte ASCII: no ANSI escape sequence.
    assert!(
        !stdout.contains('\u{1b}'),
        "--plain output must contain no ANSI escape"
    );
}

#[test]
fn breakeven_with_an_unknown_cloud_model_fails_clearly() {
    let out = run(&["breakeven", "--plain", "--compare-to", "no-such-model-xyz"]);
    assert!(
        !out.status.success(),
        "an unknown --compare-to model must fail, not silently succeed"
    );
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert!(
        stderr.contains("no-such-model-xyz"),
        "the error should name the unknown model; got: {stderr}"
    );
}

#[test]
fn breakeven_rejects_an_out_of_range_utilization() {
    let out = run(&["breakeven", "--plain", "--utilization", "2"]);
    assert!(
        !out.status.success(),
        "--utilization 2 must be rejected (it would inflate the feasibility ceiling)"
    );
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert!(
        stderr.contains("utilization"),
        "the error should name utilization; got: {stderr}"
    );
}
