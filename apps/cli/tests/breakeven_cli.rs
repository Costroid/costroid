//! Integration test for the public `costroid breakeven` subcommand (M4 T7/T8, `--features power`).
//!
//! Runs the REAL binary in estimated/what-if mode (no subprocess, no hardware, no network) and
//! asserts the rendered verdict is well-formed: it names the cloud model, surfaces the crossover +
//! the assumption stamp + the labeled DeepSWE-Bench overlay, carries NO ANSI escape under
//! `--plain`, and rejects an unknown `--compare-to` model with a clear message.
#![cfg(feature = "power")]

use std::process::Command;

use costroid_core::{
    blended_cloud_per_token, breakeven, cloud_price_per_token, BreakevenInputs, BreakevenOutcome,
    TokenType, UsdAmount,
};
use costroid_power::{
    bundled_models, bundled_power_profiles, estimate_run, ProfileOverrides, DEFAULT_BENCHMARK_SUITE,
};
use rust_decimal::Decimal;

fn run(args: &[&str]) -> std::process::Output {
    let bin = env!("CARGO_BIN_EXE_costroid");
    match Command::new(bin).args(args).output() {
        Ok(value) => value,
        Err(err) => panic!("running `costroid {}` should spawn: {err}", args.join(" ")),
    }
}

/// Parse the printed `Local breaks even at <N> tokens/day.` integer.
fn parse_crossover(stdout: &str) -> Decimal {
    let marker = "breaks even at ";
    let Some(idx) = stdout.find(marker) else {
        panic!("no crossover line in:\n{stdout}");
    };
    let rest = &stdout[idx + marker.len()..];
    let Some(end) = rest.find(" tokens/day") else {
        panic!("malformed crossover line:\n{stdout}");
    };
    match Decimal::from_str_exact(rest[..end].trim()) {
        Ok(value) => value,
        Err(err) => panic!("crossover {:?} is not a number: {err}", &rest[..end]),
    }
}

/// Recompute a CrossesAt V* via the library for a given local marginal rate `e`.
fn crossover_for(local_energy_per_token: Decimal, cloud_per_token: Decimal) -> Decimal {
    let inputs = BreakevenInputs {
        local_energy_per_token,
        hardware_capex: UsdAmount::from_usd(Decimal::from(2000)),
        depreciation_period_days: Decimal::from(1000),
        cloud_per_token,
        max_tokens_per_day: None,
    };
    match breakeven(&inputs) {
        Ok(BreakevenOutcome::CrossesAt { tokens_per_day }) => tokens_per_day,
        other => panic!("expected a crossover, got {other:?}"),
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
fn the_binary_crossover_uses_energy_only_e_not_the_capex_inclusive_total() {
    // L2-1 — the energy-only landmine enforced THROUGH THE BINARY (run_breakeven's wiring
    // energy_only_rate → BreakevenInputs.local_energy_per_token), which the helper-level unit test
    // cannot reach. Fixed flags make the estimated harness deterministic; we recompute BOTH the
    // energy-only V* and the capex-inclusive total V* from the library report and assert the
    // binary printed the energy-only one (a regression to local_run_cost would print the other).
    let flags = [
        "breakeven",
        "--plain",
        "--model",
        "gemma-4-26b-a4b",
        "--electricity-rate",
        "0.16",
        "--hardware-price",
        "2000",
        "--depreciation-period-days",
        "1000",
        "--tokens-in",
        "1000",
        "--tokens-out",
        "9600",
        "--compare-to",
        "claude-opus-4-8",
        "--output-share",
        "0.8",
        "--utilization",
        "1.0",
    ];
    let out = run(&flags);
    assert!(
        out.status.success(),
        "breakeven exited non-zero: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    let printed = parse_crossover(&String::from_utf8_lossy(&out.stdout));

    // Recompute the harness report deterministically (same inputs the binary used: profile default
    // hardware, electricity 0.16; hardware_price/lifetime are NOT break-even inputs).
    let Ok(manifest) = bundled_models() else {
        panic!("manifest parses");
    };
    let Ok((spec, quant)) = manifest.resolve("gemma-4-26b-a4b", None) else {
        panic!("model resolves");
    };
    let Ok(profiles) = bundled_power_profiles() else {
        panic!("profiles parse");
    };
    let overrides = ProfileOverrides {
        electricity_rate_per_kwh: Some(0.16),
        ..ProfileOverrides::default()
    };
    let Ok(resolved) = profiles.resolve(&overrides) else {
        panic!("profile resolves");
    };
    let Ok(report) = estimate_run(
        spec,
        &quant,
        "ollama",
        1_000,
        9_600,
        &resolved,
        DEFAULT_BENCHMARK_SUITE,
    ) else {
        panic!("library estimate computes");
    };
    let total_tokens = Decimal::from(1_000 + 9_600_u64);
    let (Some(energy_cost), Some(total_cost)) = (
        Decimal::from_f64_retain(report.energy_cost),
        Decimal::from_f64_retain(report.local_run_cost),
    ) else {
        panic!("report costs are finite");
    };
    let (Some(energy_e), Some(total_e)) = (
        energy_cost.checked_div(total_tokens),
        total_cost.checked_div(total_tokens),
    ) else {
        panic!("rates divide");
    };

    // The blended cloud rate the binary used (claude-opus-4-8 at output_share 0.8).
    let rate = |tt| match cloud_price_per_token("claude-opus-4-8", tt, None) {
        Ok(Some(price)) => price.price_per_token,
        other => panic!("cloud price for opus: {other:?}"),
    };
    let Ok(cloud) = blended_cloud_per_token(
        rate(TokenType::Input),
        rate(TokenType::Output),
        Decimal::new(8, 1),
    ) else {
        panic!("cloud blends");
    };

    let v_energy = crossover_for(energy_e, cloud).round_dp(0);
    let v_total = crossover_for(total_e, cloud).round_dp(0);

    // Discriminator: with these flags the two crossovers must round to DIFFERENT integers, so the
    // assertion below can actually detect the regression (else the test would be toothless).
    assert_ne!(
        v_energy, v_total,
        "energy-only ({v_energy}) and total ({v_total}) V* must be distinguishable at integer \
         resolution for this test to discriminate"
    );
    // The real guard: the binary printed the ENERGY-ONLY crossover, not the capex-inclusive total.
    assert_eq!(
        printed.round_dp(0),
        v_energy,
        "the binary's V* must be the energy-only crossover {v_energy}, not the total-rate {v_total}"
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

#[test]
fn breakeven_rejects_a_non_positive_tokens_per_day() {
    let out = run(&["breakeven", "--plain", "--tokens-per-day", "0"]);
    assert!(
        !out.status.success(),
        "--tokens-per-day 0 must be rejected (a meaningless daily volume)"
    );
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert!(
        stderr.contains("tokens-per-day"),
        "the error should name tokens-per-day; got: {stderr}"
    );
}
