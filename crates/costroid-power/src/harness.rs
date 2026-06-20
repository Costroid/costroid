//! The benchmark harness (M3) — turns a local run (token counts + power over wall-clock) into
//! the §3.2 economics: **Wh**, **J·token⁻¹**, and **$·(1M tok)⁻¹**, with the measurement mode
//! stamped (R6).
//!
//! Two paths:
//! - [`estimate_run`] — the **estimated / what-if** path (no subprocess): power from the
//!   profile's `load_watts`, throughput from the Gemma 4 manifest's `estimated_tok_s`. The
//!   universal fallback and the CI-testable, hardware-free path.
//! - [`run_measured`] — the **measured** path: run the model via a [`Runner`] subprocess while
//!   reading the selected [`PowerSampler`], over a measured wall-clock. (In M3a the only live
//!   measured sampler is the constant wall meter — a single read suffices; periodic capture for
//!   the time-varying on-chip sources is the M3b enhancement.)
//!
//! Both feed [`compute_report`], the pure cost core (the deterministic deciding test runs on
//! **synthetic** power fixtures here). No real power number is fabricated (R10).

use crate::cost::{local_run_cost, CostInputs};
use crate::error::PowerError;
use crate::mode::MeasurementMode;
use crate::models::ModelSpec;
use crate::profile::ResolvedProfile;
use crate::runner::{RunSpec, Runner};
use crate::sampler::PowerSampler;

/// The default benchmark-suite id (rides the `x_BenchmarkId` stamp, R10 reproducibility).
pub const DEFAULT_BENCHMARK_SUITE: &str = "gemma4-local-v1";

/// The computed economics of one local run — what the CLI maps onto a `LocalRunEvent` /
/// FOCUS row. Money is `f64` here (this crate's physics domain); the CLI formats the final
/// figures to decimal strings for the FOCUS money columns.
#[derive(Debug, Clone, PartialEq)]
pub struct LocalRunReport {
    pub model: String,
    pub quant: String,
    pub runtime_kind: String,
    pub tokens_in: u64,
    pub tokens_out: u64,
    pub run_seconds: f64,
    pub avg_power_watts: f64,
    pub energy_wh: f64,
    pub joules_per_token: f64,
    pub energy_cost: f64,
    pub amortized_hw_cost: f64,
    pub local_run_cost: f64,
    /// `$ / 1M tokens`, over **total** (prompt + generated) tokens — the comparable efficiency
    /// figure for the break-even vs cloud (M4).
    pub local_cost_per_1m: f64,
    pub measurement_mode: MeasurementMode,
    /// The `x_HardwareProfile` stamp (`"{id}@{as_of}"`).
    pub hardware_profile_stamp: String,
    pub benchmark_id: String,
    pub electricity_rate_per_kwh: f64,
    pub hardware_price: f64,
    pub hardware_lifetime_seconds: f64,
    pub currency: String,
}

/// A stable, bounded benchmark id identifying the run configuration (R10 reproducibility):
/// `"{suite}/{model}/{quant}/{runtime}"`.
pub fn benchmark_id(suite: &str, model: &str, quant: &str, runtime_kind: &str) -> String {
    format!("{suite}/{model}/{quant}/{runtime_kind}")
}

/// The average power draw over a run from an evenly-spaced sample sequence (M3a power
/// integration — a rectangular mean; periodic capture for time-varying sources is M3b). Errors
/// on an empty sequence or any non-finite/negative sample (R6: never a silent NaN cost).
pub fn average_watts(samples: &[f64]) -> Result<f64, PowerError> {
    if samples.is_empty() {
        return Err(PowerError::InvalidProfile(
            "no power samples to average".to_string(),
        ));
    }
    let mut sum = 0.0;
    for &w in samples {
        if !w.is_finite() || w < 0.0 {
            return Err(PowerError::InvalidProfile(format!(
                "power sample must be finite and non-negative, got {w}"
            )));
        }
        sum += w;
    }
    Ok(sum / samples.len() as f64)
}

/// The pure §3.2 cost core: from an average power draw + wall-clock + token counts + the dated
/// assumptions, compute the full [`LocalRunReport`]. The `$/1M` denominator is **total** tokens
/// (prompt + generated). Returns a typed [`PowerError`] (zero tokens, zero duration, …) — never
/// a panic or NaN. The deterministic deciding test exercises this on synthetic power.
#[allow(clippy::too_many_arguments)]
pub fn compute_report(
    model: &str,
    quant: &str,
    runtime_kind: &str,
    tokens_in: u64,
    tokens_out: u64,
    run_seconds: f64,
    avg_power_watts: f64,
    measurement_mode: MeasurementMode,
    profile: &ResolvedProfile,
    benchmark_id: String,
) -> Result<LocalRunReport, PowerError> {
    let total_tokens = tokens_in.saturating_add(tokens_out);
    let inputs = CostInputs {
        avg_power_watts,
        run_seconds,
        electricity_rate_per_kwh: profile.electricity_rate_per_kwh,
        hardware_price: profile.hardware_price,
        hardware_lifetime_seconds: profile.hardware_lifetime_seconds,
        tokens_in_run: total_tokens,
    };
    let cost = local_run_cost(&inputs)?;
    let energy_wh = cost.energy_kwh * 1000.0;
    // total_tokens > 0 is guaranteed here: local_run_cost returns ZeroTokens otherwise.
    let joules_per_token = (avg_power_watts * run_seconds) / total_tokens as f64;

    Ok(LocalRunReport {
        model: model.to_string(),
        quant: quant.to_string(),
        runtime_kind: runtime_kind.to_string(),
        tokens_in,
        tokens_out,
        run_seconds,
        avg_power_watts,
        energy_wh,
        joules_per_token,
        energy_cost: cost.energy_cost,
        amortized_hw_cost: cost.amortized_hw_cost,
        local_run_cost: cost.local_run_cost,
        local_cost_per_1m: cost.local_cost_per_1m,
        measurement_mode,
        hardware_profile_stamp: profile.stamp.clone(),
        benchmark_id,
        electricity_rate_per_kwh: profile.electricity_rate_per_kwh,
        hardware_price: profile.hardware_price,
        hardware_lifetime_seconds: profile.hardware_lifetime_seconds,
        currency: profile.currency.clone(),
    })
}

/// The **estimated / what-if** path (no subprocess, every OS): derive run time from the
/// manifest's `estimated_tok_s` and power from the profile's `load_watts`, then compute the
/// economics with `measurement_mode = Estimated`. `tokens_in` + `tokens_out` are the scenario's
/// token volumes.
pub fn estimate_run(
    spec: &ModelSpec,
    quant: &str,
    runtime_kind: &str,
    tokens_in: u64,
    tokens_out: u64,
    profile: &ResolvedProfile,
    suite: &str,
) -> Result<LocalRunReport, PowerError> {
    if spec.estimated_tok_s <= 0.0 || !spec.estimated_tok_s.is_finite() {
        return Err(PowerError::InvalidProfile(format!(
            "model {} has a non-positive estimated_tok_s ({})",
            spec.id, spec.estimated_tok_s
        )));
    }
    // Decode time dominates; estimate the run wall-clock from the generated tokens / tok_s.
    let run_seconds = (tokens_out as f64) / spec.estimated_tok_s;
    let id = benchmark_id(suite, &spec.id, quant, runtime_kind);
    compute_report(
        &spec.id,
        quant,
        runtime_kind,
        tokens_in,
        tokens_out,
        run_seconds,
        profile.load_watts,
        MeasurementMode::Estimated,
        profile,
        id,
    )
}

/// The **measured** path: run the model via the [`Runner`] subprocess while reading the selected
/// [`PowerSampler`], then compute the economics stamped with the sampler's mode. The model's
/// output text is never read (R4 — the runner discards it).
///
/// `model_id` is the **economic identity** (the Gemma 4 manifest id) the report + benchmark id
/// key on — distinct from `run_spec.model`, which is what the runtime actually loads (an Ollama
/// tag / a GGUF path that need not equal the manifest id). `run_seconds` is the **runtime's own
/// reported total time** ([`RunOutput::run_seconds`]) — the interval power is drawn over (more
/// accurate for energy than the host wall-clock, and deterministic for testing). M3a reads the
/// sampler **once** after the run (correct for the constant wall meter — the recommended live
/// measured source); periodic capture for the time-varying on-chip sources is the M3b
/// enhancement. The runner is injected, so a `StubRunner` makes this CI-testable without a
/// subprocess.
pub fn run_measured(
    runner: &dyn Runner,
    sampler: &dyn PowerSampler,
    run_spec: &RunSpec,
    model_id: &str,
    profile: &ResolvedProfile,
    suite: &str,
) -> Result<LocalRunReport, PowerError> {
    let out = runner.run(run_spec)?;
    let avg_power_watts = sampler.sample_watts()?;
    let id = benchmark_id(suite, model_id, &run_spec.quant, runner.kind());
    compute_report(
        model_id,
        &run_spec.quant,
        runner.kind(),
        out.tokens_in,
        out.tokens_out,
        out.run_seconds,
        avg_power_watts,
        sampler.mode(),
        profile,
        id,
    )
}

#[cfg(test)]
mod tests {
    // Repo rule: clippy denies `unwrap`/`expect` even in tests; use `let-else { panic! }`.
    use super::*;
    use crate::profile::{bundled_power_profiles, ProfileOverrides};
    use crate::runner::{RunOutput, StubRunner};
    use crate::sampler::WallMeterPowerSampler;

    fn resolved() -> ResolvedProfile {
        let Ok(profiles) = bundled_power_profiles() else {
            panic!("profiles parse");
        };
        let Ok(r) = profiles.resolve(&ProfileOverrides {
            // Pin the assumptions so the worked example is exact (independent of the bundled
            // defaults drifting): 160 W base is overridden per-test; rate 0.10, $2000 / 3-yr.
            electricity_rate_per_kwh: Some(0.10),
            hardware_price: Some(2000.0),
            hardware_lifetime_seconds: Some(94_608_000.0),
            ..ProfileOverrides::default()
        }) else {
            panic!("resolve");
        };
        r
    }

    #[test]
    fn average_watts_handles_constant_and_varying_sequences() {
        // Constant: a steady wall-meter reading.
        let Ok(c) = average_watts(&[155.0, 155.0, 155.0]) else {
            panic!("constant samples average")
        };
        assert!((c - 155.0).abs() < 1e-12);
        // Varying: a time-varying on-chip sequence (mean of 100,200,150 = 150).
        let Ok(v) = average_watts(&[100.0, 200.0, 150.0]) else {
            panic!("varying samples average")
        };
        assert!((v - 150.0).abs() < 1e-12);
        // Empty / NaN / negative fail closed.
        assert!(average_watts(&[]).is_err());
        assert!(average_watts(&[f64::NAN]).is_err());
        assert!(average_watts(&[-1.0]).is_err());
    }

    #[test]
    fn compute_report_matches_the_worked_example_3_2() {
        // The §3.2 worked example: 160 W for 100 s = 16,000 J = 0.0044444 kWh.
        // energy_cost @ $0.10/kWh = 0.00044444; HW $2000/3yr*100s = 0.0021140.
        // 50,000 total tokens → per-1M = local_run_cost * 20. J/token = 16000/50000 = 0.32.
        let profile = resolved();
        let Ok(rep) = compute_report(
            "gemma-4-26b-a4b",
            "Q4_K_M",
            "ollama",
            10_000,
            40_000,
            100.0,
            160.0,
            MeasurementMode::MeasuredWallmeter,
            &profile,
            "gemma4-local-v1/gemma-4-26b-a4b/Q4_K_M/ollama".to_string(),
        ) else {
            panic!("worked example computes");
        };
        assert!((rep.energy_wh - 4.444_444).abs() < 1e-4); // 0.0044444 kWh * 1000
        assert!((rep.energy_cost - 0.000_444_444).abs() < 1e-7);
        assert!((rep.amortized_hw_cost - 0.002_113_99).abs() < 1e-5);
        assert!((rep.local_run_cost - (rep.energy_cost + rep.amortized_hw_cost)).abs() < 1e-12);
        assert!((rep.local_cost_per_1m - rep.local_run_cost * 20.0).abs() < 1e-9);
        assert!((rep.joules_per_token - 0.32).abs() < 1e-9);
        assert_eq!(rep.measurement_mode, MeasurementMode::MeasuredWallmeter);
        assert_eq!(rep.currency, "USD");
    }

    #[test]
    fn compute_report_via_a_varying_sample_sequence() {
        // Integrate a time-varying sequence (mean 150 W) over 60 s, then cost it.
        let Ok(avg) = average_watts(&[100.0, 200.0, 150.0]) else {
            panic!("avg")
        };
        let profile = resolved();
        let Ok(rep) = compute_report(
            "gemma-4-31b-dense",
            "Q8_0",
            "llama.cpp",
            5_000,
            5_000,
            60.0,
            avg,
            MeasurementMode::MeasuredSysfs,
            &profile,
            "b".to_string(),
        ) else {
            panic!("computes")
        };
        // 150 W * 60 s = 9000 J = 0.0025 kWh = 2.5 Wh.
        assert!((rep.energy_wh - 2.5).abs() < 1e-9);
        // J/token over 10,000 total tokens = 0.9.
        assert!((rep.joules_per_token - 0.9).abs() < 1e-9);
    }

    #[test]
    fn estimate_run_derives_time_from_manifest_tok_s_and_stamps_estimated() {
        let Ok(manifest) = crate::models::bundled_models() else {
            panic!("manifest")
        };
        let Some(moe) = manifest.model("gemma-4-26b-a4b") else {
            panic!("moe present")
        };
        let profile = resolved();
        let Ok(rep) = estimate_run(moe, "Q4_K_M", "ollama", 1_000, 9_600, &profile, "suite") else {
            panic!("estimate computes")
        };
        // run_seconds = 9600 / 96 tok/s = 100 s (no subprocess).
        assert!((rep.run_seconds - 100.0).abs() < 1e-9);
        assert_eq!(rep.measurement_mode, MeasurementMode::Estimated);
        assert_eq!(rep.benchmark_id, "suite/gemma-4-26b-a4b/Q4_K_M/ollama");
        assert!(rep.local_run_cost > 0.0);
    }

    #[test]
    fn run_measured_uses_a_stub_runner_and_a_constant_wall_meter() {
        let stub = StubRunner {
            output: RunOutput {
                tokens_in: 500,
                tokens_out: 1_500,
                run_seconds: 12.0,
                tok_s: Some(96.0),
            },
        };
        let Ok(wall) = WallMeterPowerSampler::constant(165.0) else {
            panic!("wall meter")
        };
        let spec = RunSpec {
            binary_path: "unused".to_string(),
            // What the runtime LOADS — a GGUF path, deliberately distinct from the manifest id.
            model: "/models/gemma-4-26b-a4b-Q4_K_M.gguf".to_string(),
            quant: "Q4_K_M".to_string(),
            prompt: "fixed benchmark prompt".to_string(),
            max_tokens: 1_500,
            extra_args: vec![],
        };
        let profile = resolved();
        // `run_spec.model` is what the runtime loads (the GGUF path above); the economic identity
        // is the manifest id passed separately — the report keys on the latter, not the path.
        let Ok(rep) = run_measured(
            &stub,
            &wall,
            &spec,
            "gemma-4-26b-a4b",
            &profile,
            "gemma4-local-v1",
        ) else {
            panic!("measured run computes")
        };
        assert_eq!(rep.tokens_out, 1_500);
        assert_eq!(
            rep.model, "gemma-4-26b-a4b",
            "report keys on the manifest id"
        );
        assert_eq!(rep.measurement_mode, MeasurementMode::MeasuredWallmeter);
        assert!((rep.avg_power_watts - 165.0).abs() < 1e-12);
        // run_seconds is the runtime's reported total time (deterministic via the stub).
        assert!((rep.run_seconds - 12.0).abs() < 1e-12);
        assert_eq!(rep.runtime_kind, "stub");
    }
}
